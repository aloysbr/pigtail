// Package cmuxsync computes what pigtail shows in the cmux sidebar and the
// minimal writes that get it there (spec "Sidebar pigtail", lot 2).
//
// This package is the pure engine: Compute (observed state → wanted sidebar
// per workspace), Damper (attention hysteresis, injected clock) and Diff
// (wanted vs already posed → ops). It runs nothing and starts nothing; the
// runner that feeds it from pigtaild and applies the ops through
// internal/cmux arrives separately.
//
// Ground rules, from the subject's decisions:
//   - the sidebar order comes only from Arrange (folders.go), applied while
//     the cmux window is not key (D2, sub_x000000000000041); no sidebar op
//     kind here can express a reorder.
//   - pigtail writes only its own status keys (OwnedKeys: pigtail-attn,
//     pigtail-srv, pigtail-mnt, pigtail-off, pigtail-chk, pigtail-pause, pigtail-role) — never pigtail-inbox or any other tool's
//     key. One temporary exception:
//     OpClaudeCodeIdle puts cmux's own claude_code row back to Idle
//     (claudecode_patch.go), until cmux ships #15666/#15603.
//   - the attention pill shows the most severe state of the workspace by
//     gravity — Permission > Blocked > Dead > Question > Decision > Waiting
//     for you > Respawn > Waiting for Mac > Your turn > Review > Done >
//     Working > Wrapped — in
//     its gravity's colour (waittype/gravity.go); no busy/idle pill.
//   - a bound workspace is renamed automatically, but a name given by hand
//     wins: once the observed title differs from the one pigtail posed, the
//     title belongs to the user and pigtail stops touching it.
package cmuxsync

import (
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Status keys owned by the loop. Nothing else is ever set or cleared.
const (
	KeyAttention   = "pigtail-attn"
	KeyServers     = "pigtail-srv"
	KeyMaintenance = "pigtail-mnt"
	KeyOffTopic    = "pigtail-off"
	// KeyCmuxCheck: the defects the last `pigtail cmux check` left on this
	// workspace (D13: pigtail posts no cmux notification).
	KeyCmuxCheck = "pigtail-chk"
	// KeyPaused: pigtaild paused this workspace's idle session (paused.go).
	KeyPaused = "pigtail-pause"
	// KeyRole: a role of this workspace's subject missed its deadline (role.go).
	KeyRole = "pigtail-role"
)

// OwnedKeys lists the status keys the loop may write, in Diff order.
var OwnedKeys = []string{KeyAttention, KeyServers, KeyMaintenance, KeyOffTopic, KeyCmuxCheck, KeyPaused, KeyRole}

const (
	// D6: below the attention pill (pigtail-attn, 90), above the inbox pill
	// (70, internal/cmux/relay.go) and the servers pill (60, servers.go).
	maintenancePriority = 80
	maintenanceIcon     = "wrench.and.screwdriver.fill"
	// maintenanceOwnerIcon marks the workspace of the session that set the
	// maintenance (D1, sub_x000000000000012); same key, rank and colour.
	maintenanceOwnerIcon = "key.fill"
)

const (
	offTopicLabel    = "Off-topic"    // D10; the reason is never in the pill (D14), it stays in `off-topic ls`
	offTopicIcon     = "circle.slash" // D11, SF Symbol
	offTopicColor    = "#8E8E93"      // D12, systemGray
	offTopicPriority = 50             // D13: under servers (60), inbox (70), maintenance (80), attention (90)
)

// MaintenancePill is the maintenance as the sidebar shows it on every live
// workspace (D5). nil = no maintenance. Value, Color and OwnerValue come from
// maintenance.PillValue / ErrorPill / OwnerPillValue only.
type MaintenancePill struct {
	Value, Color string
	// Owner is the session that set the maintenance (maintenance.State.Owner);
	// "" = the user outside Claude Code or unreadable: no workspace marked (D2).
	Owner string
	// OwnerValue is the label of the owner's workspace (D1); "" = none.
	OwnerValue string
}

// Owns reports whether the loop may write key.
func Owns(key string) bool {
	for _, k := range OwnedKeys {
		if k == key {
			return true
		}
	}
	return false
}

// Attention is the state the pigtail-attn pill shows.
type Attention string

// Attention states: waiting (a live state wins) or dead (Dead wins). Only
// these two ever produce a pill (decision of 2026-09-30).
const (
	AttentionNone    Attention = ""
	AttentionWaiting Attention = "waiting"
	AttentionDead    Attention = "dead"
)

// Rendering of the attention pill (spec §4.1, §4.8). The labels are English
// (decision of 2026-10-01) and carry no glyph: cmux draws the icon, an SF
// Symbol tinted with the colour, in front of the value. The pill takes the
// label, icon and colour of its winning state (internal/waittype, Dead
// included); an untyped wait reads "Waiting for you" under bell.fill.
const (
	attentionPriority = 90
	// pillMaxRunes is the longest waiting pill that still reads whole in the
	// sidebar's default width (about 38 characters, measured on cmux 0.64.24,
	// 2026-10-01; kept a little under it): the reason is
	// added after the label only when the whole value fits.
	pillMaxRunes = 36
)

// Workspace is a live cmux workspace as the last listing saw it (any
// window). Absent from Input.Workspaces means closed.
type Workspace struct {
	ID             string // UUID, upper case
	WindowID       string
	Title          string
	CustomTitle    string
	HasCustomTitle bool
	Description    string
	// Pinned is the row's `pinned` (D13): never filed in a folder.
	Pinned bool
	// Surfaces holds the surfaces (panes) open in the workspace, upper
	// case. nil means unknown — cmux could not list them, or not recently
	// enough — and then no session of the workspace can read dead.
	Surfaces map[string]bool
}

// WorkspaceFromCmux adapts a cmux listing row.
func WorkspaceFromCmux(windowID string, w cmux.WorkspaceInfo) Workspace {
	return Workspace{
		ID:             w.ID,
		WindowID:       windowID,
		Title:          w.Title,
		CustomTitle:    w.CustomTitle,
		HasCustomTitle: w.HasCustomTitle,
		Description:    w.Description,
		Pinned:         w.Pinned,
	}
}

// Session is one Claude session as the loop sees it: its row in
// peer_sessions joined with its native sessions/<pid>.json. A row outlives
// its pane: `cmux sessions list` is a persisted history, so a session whose
// pane was closed without a SessionEnd stays there, pid gone, forever.
type Session struct {
	SessionID   string
	WorkspaceID string // cmux workspace UUID; empty = not placed, ignored
	SurfaceID   string // cmux surface; empty = unknown, never reads dead
	PID         int    // peer_sessions.pid; 0 = not known yet
	Alive       bool   // claude pid running and still naming this session
	Ended       bool   // SessionEnd seen: a clean exit, never "dead"
	Status      string // native status: busy | idle | waiting
	Kind        string // native kind; only "interactive" (or unknown) counts
	UpdatedAt   time.Time
	// NativeStatusAt is when the native file says the status last changed;
	// zero when no native file naming this session was read.
	NativeStatusAt time.Time
	// StartedAt is when this Claude process started or resumed the session
	// (the native file's startedAt); zero when no native file was read.
	StartedAt time.Time
	// BusySeen: the loop observed this session instance (its native
	// startedAt: a resume is a new instance) busy since it started — it ran
	// a turn (D24). Memory only: a daemon restart forgets it. The native
	// timestamps cannot tell: Claude moves statusUpdatedAt 100–450 ms after
	// startedAt with no prompt, at start and at --resume (B1).
	BusySeen bool
	// Standby: the session is in Standby (Deps.Standby). Its idle turn end
	// reads no inferred Your turn; failures and declared or held waits stay
	// (2026-10-16, the user, sub_x000000000000041).
	Standby bool
	// ConfigDir is the session's Claude config dir (peer_sessions.config_dir):
	// where its transcript lives.
	ConfigDir string
	// Scope is the session's (a scope name): a wait reason it told is shown
	// only to readers of that scope.
	Scope string
	// CellID is the session's cell (peer_sessions.cell_id); "" for a bare
	// claude (D7).
	CellID string
	// WaitingFor is the native file's loose hint while waiting (spec C10).
	WaitingFor string
	// Auto is the wait a Claude Code hook typed; Declared, the one the agent
	// told (`pigtail session wait`); Held, the one a process keeps while it
	// runs (`the Mac-hold helper`: Mac, D1), with its HolderPID. Zero = none
	// (spec §4.8).
	Auto     Wait
	Declared Wait
	Held     Wait
}

// Wait is a recorded wait of a session (session_waits).
type Wait struct {
	Type   waittype.Type
	Reason string
	Since  time.Time
	// Until is the wait's own end (a usage limit's reset, a held wait's
	// deadline); zero = none.
	Until time.Time
	// HolderPID is the process that keeps a held wait; the wait ends when it
	// dies (D4). Zero for the other sources.
	HolderPID int
}

// Set reports whether a wait is recorded.
func (w Wait) Set() bool { return w.Type != waittype.Unknown }

// CellRef is a cell of a workspace's live sessions, with that session's
// scope (who may read it in GET /cmux/sidebar).
type CellRef struct{ ID, Scope string }

// SubjectView is what the sidebar needs from a bound subject.
type SubjectView struct {
	Subject pigtail.Subject
	Phases  []pigtail.SubjectPhase
	// ThemeName is the name of the subject's theme, "" when it could not be
	// read: Description then falls back to the subject's scope.
	ThemeName string
}

// Input is one observation of the world.
type Input struct {
	// Now is when the cmux listing was read.
	Now time.Time
	// Location is the clock's zone for "Limit · HH:MM"; nil = time.Local.
	Location   *time.Location
	Workspaces []Workspace
	Sessions   []Session
	Bindings   []pigtail.SubjectCmuxBinding
	// Subjects holds the bound subjects by id. A binding whose subject is
	// missing leaves its workspace's title and description untouched.
	Subjects map[string]SubjectView
	// Frozen holds the sessions a freeze refused (D4): each reads one red
	// "Blocked · freeze" until the lift.
	Frozen map[string]bool
	// CellText is the cell-only description pigtail last posed on an unbound
	// workspace, by id; memory only.
	CellText map[string]string
	// KnownCells holds every cell id of the cell table: a description made
	// only of these, joined by +, is pigtail's.
	KnownCells map[string]bool
	// LastTextWrite is when pigtail's last rename or description write on a
	// workspace returned. A listing started less than TakeoverReadGuard after
	// it is not trusted to detect a manual rename (cmux reads lag, research
	// §1.2).
	LastTextWrite map[string]time.Time
	// ListStartedAt is when the listing in Workspaces began; zero = Now. A
	// listing that began before a write ended + TakeoverReadGuard is not
	// trusted to detect a manual rename: it may predate the write, however
	// long cmux took to answer.
	ListStartedAt time.Time
	// Servers holds the dev servers to show, each under its owner's cmux
	// workspace (ServerTracker.Observe). One whose workspace is not live is
	// ignored.
	Servers []Server
	// TrustPending holds the workspaces of a spawned session stopped on
	// Claude's "trust this folder" dialog (D14), kept by the Runner: each
	// counts as one red session "Blocked · trust folder".
	TrustPending map[string]bool
	// Respawn holds the state of a `pigtail session respawn` run by
	// workspace (D16), kept by the Runner: waiting and abandoned read
	// "Respawn · <reason>" (orange), failed a red "Blocked · respawn failed".
	Respawn map[string]respawnMark
	// Maintenance is the maintenance pill shown on every live workspace;
	// nil = none (Runner.maintenancePill).
	Maintenance *MaintenancePill
	// CheckDefects are the surfaces the last `pigtail cmux check` left in
	// defect, for the running cmux launch only (Runner.cmuxCheck, D13);
	// nil = none.
	CheckDefects []CheckDefect
	// DeadInputs are the surfaces whose input the daemon's probe found dead
	// (sub_x000000000000024), shown on the check pill; nil = none.
	DeadInputs []string
	// OffTopic holds the workspaces declared off-topic (workspace_off_topic); a bound one shows no pill.
	OffTopic map[string]bool
	// Paused holds the workspaces (upper case) of a paused session
	// (session_pause, pausedWorkspaces).
	Paused map[string]bool
	// RoleAlerts are the open role alerts (role.go); joined to workspaces by Bindings.
	RoleAlerts []RoleAlert
}

// respawnMark is a respawn run's state on its workspace (D16). Memory only:
// a daemon restart forgets it (the worker re-posts a waiting one).
type respawnMark struct {
	state   string // pigtail.CmuxRespawnWaiting | Abandoned | Failed
	reason  string // waiting: what holds it, from respawnWaitReason
	surface string // the run's surface, upper case
	pid     int    // waiting: the worker, the mark dies with it
	since   time.Time
	// idleStatusAt is the native statusUpdatedAt of the first idle read of
	// the surface's session after a final mark: a busy read after it is the
	// user's next prompt, which clears the mark (Claude's clock both sides).
	// A live session on the surface started after since clears a final mark
	// whatever its status: the user relaunched Claude there (respawnSet).
	// An abandoned mark also drops at the user's next typed prompt, read in
	// the transcript (respawnPrompted): the one clear that holds while Claude
	// keeps its native file busy for a background task (2026-10-23).
	idleStatusAt time.Time
}

// TakeoverReadGuard: see Input.LastTextWrite.
const TakeoverReadGuard = 1500 * time.Millisecond

// P22MaxLag (cmux probe P22, measured 2026-10-21 on cmux NIGHTLY 0.64.20, n = 20
// per case): the largest delay between an engine group-add / move / remove
// returning and the first listing showing it (0.007 ms, rounded up to 100 ms).
const P22MaxLag = 100 * time.Millisecond

// DragReadGuard: how long after an engine group-add / group-remove a listing
// that does not show it yet may still be cmux lagging (the hand-drag
// detector's rules 3b/3c; prio_drag.go went with #244, the space drag detector
// of sub_x000000000000033 lot 3 reads it next); past it, a listing that contradicts the write is a hand drag. A
// listing that shows the write is read as pigtail's at once (3a). Derived
// from P22: max(TakeoverReadGuard, 2 × P22MaxLag), rounded up to 500 ms.
const DragReadGuard = 1500 * time.Millisecond

// liveSummary is what a workspace's live sessions add up to: the most severe
// state (waittype.Order), its session's reason and scope, and how many
// sessions sit in each gravity.
type liveSummary struct {
	any         bool
	typ         waittype.Type
	reason      string
	scope       string
	fromSession bool // reason told by a session (scope = its scope); false: pigtail's own
	count       [waittype.GravityCount]int
}

// add counts a session's state; its reason is that session's, read under its scope.
func (l *liveSummary) add(t waittype.Type, reason, scope string) { l.put(t, reason, scope, true) }

// addSystem counts a state pigtail tells itself (trust folder, respawn): no
// session, no scope — shown as the workspace is, as before D17.
func (l *liveSummary) addSystem(t waittype.Type, reason string) { l.put(t, reason, "", false) }

func (l *liveSummary) put(t waittype.Type, reason, scope string, fromSession bool) {
	l.count[t.Gravity()]++
	if !l.any || waittype.Before(t, l.typ) {
		l.any, l.typ, l.reason, l.scope, l.fromSession = true, t, reason, scope, fromSession
	}
}

// Desired is the sidebar pigtail wants on one workspace. An empty Title or
// Description means "leave it alone", not "clear it": pigtail only ever
// clears a title it posed, when it gives the workspace back (RestoreOps).
type Desired struct {
	WorkspaceID string
	// The pill, set by resolve from Live and DeadSurfaces (Compute, then the
	// Damper): Attention is dead when Dead wins, waiting when a live state
	// wins, none otherwise. WaitingCount is the n of " ×n": the sessions of
	// the winner's colour, a dead surface counting as one red (D15).
	// WaitReason is the winner's reason, kept only when n is 1; WaitScope is
	// that session's scope (who may read the reason).
	Attention    Attention
	WaitingCount int
	WaitType     waittype.Type
	WaitReason   string
	WaitScope    string
	// WaitFromSession: WaitReason was told by a session, WaitScope being its
	// scope; false: pigtail's own reason.
	WaitFromSession bool
	// Raw inputs of resolve, from Compute; the Damper smooths each one.
	Live         liveSummary
	DeadSurfaces int
	Title        string
	Description  string
	// Cells are the cells of the workspace's live sessions (D1).
	Cells []CellRef
	// ClearDescription: pigtail's cell-only description must go, no live
	// cell is left on this unbound workspace.
	ClearDescription bool
	Servers          []Server         // the workspace's dev servers, for pigtail-srv
	Maintenance      *MaintenancePill // pigtail-mnt; nil = no pill
	MaintenanceHere  bool             // the maintenance owner runs here: key + OwnerValue (D1)
	Check            *CheckPill       // pigtail-chk; nil = no pill (D13)
	OffTopic         bool             // pigtail-off
	Paused           bool             // pigtail-pause
	Role             *RolePill        // pigtail-role; nil = no pill
}

// resolve picks the pill: the most severe of the best live state and Dead.
func (d *Desired) resolve() {
	l, dead := d.Live, d.DeadSurfaces
	d.Attention, d.WaitType, d.WaitingCount, d.WaitReason, d.WaitScope, d.WaitFromSession = AttentionNone, waittype.Unknown, 0, "", "", false
	switch {
	case dead > 0 && (!l.any || waittype.Before(waittype.Dead, l.typ)):
		d.Attention, d.WaitType = AttentionDead, waittype.Dead
		d.WaitingCount = dead + l.count[waittype.Red]
	case l.any:
		d.Attention, d.WaitType = AttentionWaiting, l.typ
		n := l.count[l.typ.Gravity()]
		if l.typ.Gravity() == waittype.Red {
			n += dead
		}
		d.WaitingCount = n
		if n == 1 && l.typ != waittype.Wrapped { // Wrapped never shows a reason (spec § 1)
			d.WaitReason, d.WaitScope, d.WaitFromSession = l.reason, l.scope, l.fromSession
		}
	}
}

// Statuses renders the owned pills this state calls for.
func (d Desired) Statuses() map[string]cmux.StatusEntry {
	st := map[string]cmux.StatusEntry{}
	if d.Attention != AttentionNone {
		if d.Attention == AttentionDead {
			// Whatever WaitType says: a hand-built Desired{Attention: dead}
			// renders Dead (label via pillValue, icon, colour).
			d.WaitType = waittype.Dead
		}
		t := d.WaitType
		st[KeyAttention] = cmux.StatusEntry{Key: KeyAttention, Value: pillValue(d), Icon: t.Icon(), Color: t.Color(), Priority: attentionPriority}
	}
	if e, ok := serversPill(d.Servers); ok {
		st[KeyServers] = e
	}
	if d.Maintenance != nil {
		v, icon := d.Maintenance.Value, maintenanceIcon
		if d.MaintenanceHere && d.Maintenance.OwnerValue != "" {
			v, icon = d.Maintenance.OwnerValue, maintenanceOwnerIcon
		}
		st[KeyMaintenance] = cmux.StatusEntry{Key: KeyMaintenance, Value: v, Icon: icon, Color: d.Maintenance.Color, Priority: maintenancePriority}
	}
	if d.Check != nil && (d.Check.Left > 0 || d.Check.InputDead > 0) {
		st[KeyCmuxCheck] = checkStatus(*d.Check)
	}
	if d.OffTopic {
		st[KeyOffTopic] = cmux.StatusEntry{Key: KeyOffTopic, Value: offTopicLabel, Icon: offTopicIcon, Color: offTopicColor, Priority: offTopicPriority}
	}
	if d.Paused {
		st[KeyPaused] = pausedStatus()
	}
	if d.Role != nil {
		st[KeyRole] = roleStatus(*d.Role)
	}
	return st
}

// pillValue is the attention pill's text: the label, then " ×n" when
// several sessions share the winner's colour, else " · reason" when the
// whole value fits. A Done with no reason reads "Done · <done command>", a
// Mac with none "Waiting for Mac · <release command>" — the user's own
// commands (waittype.DoneHint, MacHint), "Done" and "Waiting for Mac" alone
// when none is set; Wrapped reads "Wrapped" alone, whatever reason was
// declared (spec § 1).
func pillValue(d Desired) string {
	v := d.WaitType.Label()
	if d.WaitingCount > 1 {
		return v + " ×" + strconv.Itoa(d.WaitingCount)
	}
	if d.WaitType == waittype.Wrapped {
		return v
	}
	r := sidebarText(d.WaitReason)
	if r == "" {
		switch d.WaitType {
		case waittype.Done:
			r = waittype.DoneHint()
		case waittype.Mac:
			r = waittype.MacHint()
		}
	}
	if r != "" {
		if full := v + " · " + r; utf8.RuneCountInString(full) <= pillMaxRunes {
			return full
		}
	}
	return v
}

// Field names a workspace text pigtail may own.
type Field string

// Fields of a takeover.
const (
	FieldTitle       Field = "title"
	FieldDescription Field = "description"
)

// Takeover reports that someone else edited the description of a bound
// workspace: the runner flips the binding's description owner to "user",
// logs it once, and pigtail never writes it again until a retitle. The title
// is never taken over since D5 (FieldTitle is no longer emitted).
type Takeover struct {
	WorkspaceID string
	SubjectID   string
	Field       Field
}

// TitleRevert reports that a bound workspace no longer shows the short title
// pigtail posed — renamed by hand, or its name cleared (D5,
// sub_x000000000000018). The runner puts the short title back and notes it.
type TitleRevert struct {
	WorkspaceID string
	SubjectID   string
	Short       string // the short title put back
	Manual      string // what the sidebar showed instead (pigtail.ShownTitle)
	Cleared     bool   // the name was cleared, not replaced
}

// Result is Compute's answer: the wanted state of every live workspace, plus
// the manual description takeovers and title reverts detected on the way.
type Result struct {
	Desired   map[string]Desired
	Takeovers []Takeover
	Reverts   []TitleRevert
}

// Applied is what pigtail last posed on a workspace and believes is still
// there. Statuses holds owned keys only; an empty Title/Description means
// pigtail posed none.
type Applied struct {
	Statuses    map[string]cmux.StatusEntry
	Title       string
	Description string
}
