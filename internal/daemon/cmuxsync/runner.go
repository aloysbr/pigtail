package cmuxsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Runner is the sidebar loop inside pigtaild (spec §3.1, PR 4): it observes
// cmux, the session registry and the bound subjects, computes the wanted
// sidebar with the pure engine (Compute, Damper, Diff) and applies the
// difference through cmux.Sidebar.
//
// One goroutine (Run) owns the engine state; the HTTP side only flips the
// switch, nudges, queues the restore of a detached workspace and reads a
// snapshot, all under mu. Tests drive Step directly with an injected clock.
//
// The loop writes only OwnedKeys, plus the title and description of bound
// workspaces it still owns; the order comes only from Arrange (folders.go),
// applied while the cmux window is not key (D2).
//
// A failing listing does not pause the loop while the last good one is under
// ListingMaxAge: pills are written from it, text waits (D1·A, 2026-10-04).
type Runner struct {
	cfg  Config
	deps Deps

	// Engine state: touched by the Run goroutine (Step) only.
	active     bool
	applied    map[string]Applied
	damper     Damper
	natives    *nativeCache
	workspaces []Workspace // the windows the loop may write
	// pinned is the set PinnedWorkspaces serves: written by the Run goroutine,
	// read by any; nil whenever the loop is not trusted.
	pinned        atomic.Pointer[map[string]bool]
	allowed       map[string]bool
	listedAt      time.Time
	listStartedAt time.Time // when the last good slow listing began
	lastSlow      time.Time
	lastReconcile time.Time
	needReconcile bool
	surfaces      map[string]surfaceSet // last good listing per workspace
	surfacesDown  bool                  // the last surface listing failed
	lastTextWrite map[string]time.Time
	reverts       map[string]*revertState // title reverts per workspace (D5); memory only
	cellText      map[string]string       // cell-only description posed on an unbound workspace (D1); memory only
	knownCells    map[string]bool         // the cell ids read this pass (Deps.KnownCells)
	published     map[string]published
	retryAt       time.Time
	backoff       time.Duration
	pauseUntil    time.Time
	panics        []time.Time
	servers       ServerTracker
	waits         WaitTracker
	serverView    []Server // the servers Compute shows, from the last read
	serversAt     time.Time
	// busySeen holds the session instances (instanceKey) the loop saw busy
	// since they started: they ran a turn, so an idle one reads Your turn
	// (D24). Memory only.
	busySeen map[string]bool
	// lastMaintErr is the last unreadable maintenance-state error logged: one
	// line per distinct error, not one per tick.
	lastMaintErr string
	// lastOffTopicErr is the last off-topic read error logged: one line per distinct error, not one per pass.
	lastOffTopicErr string
	// lastPaused is the last successful pause read (paused.go), kept through
	// a read error; lastPausedErr logs one line per distinct error.
	lastPaused    map[string]bool
	lastPausedErr string
	lastRoles     []RoleAlert
	lastRoleErr   string
	pauseMissing  map[string]int // closeMissingPause: listings missed per session, memory only

	// The last check's defects, read at most once per SlowInterval (D13).
	checkDefects []CheckDefect
	checkAt      time.Time
	// droppedLogged: surface → reported pid of the check pills already logged
	// as dropped (D3), one line per drop; memory only.
	droppedLogged map[string]int
	lastCheckErr  string // one log line per distinct error

	// The folder arrangement (runner_folders.go): touched by Run only.
	folderWins     []string               // allowed windows of the last good listing
	wins           map[string]*winFolders // each allowed window's arrangement state (D8)
	lastDesired    map[string]Desired     // the pass's damped Desired, for ranks
	focusAt        time.Time              // last focus read that could start a round
	collapseLoaded bool                   // collapsedMem read (once)
	collapsedMem   map[string]bool        // folder → collapsed, as the user left it (spec 7)
	foldersStopped bool                   // a created folder read back wrong: no folder write
	focusDropped   map[string]bool        // focus named like a spaces-table folder, logged once
	spaceDragErr   string                 // last failed drag write, logged once until a write succeeds (space_drag.go)
	// The birth clock (births.go): touched by Run only.
	births        map[string]time.Time // workspace → first seen; zero = old
	birthsMissing map[string]int       // consecutive slow listings without it
	birthsLoaded  bool
	birthsFirst   bool // no usable file yet: the next allowed listing marks all old

	listRetryAt time.Time     // next listing attempt while listings fail
	listBackoff time.Duration // BackoffMin doubling to BackoffMax
	peersDown   bool          // the last `cmux sessions` snapshot failed
	timing      passTiming    // the current pass's step times (passlog.go)
	// Pill checkpoints (checkpoint.go, D3 b): armed for the current pass
	// only; lastSync is the clock of the last sync (pass or checkpoint);
	// statusWrites counts the pill writes per workspace, so the reconcile
	// keeps a write made after it read that workspace's list-status.
	ckptArmed    bool
	lastSync     time.Time
	statusWrites map[string]uint64

	mu       sync.Mutex
	wake     chan struct{}
	want     bool // switched on (flag at start, then POST /cmux/sidebar)
	panicked bool // switched off by repeated panics
	nudge    bool
	// serversDirty asks the next pass to re-read the servers: a server died
	// or was evicted (bus), so its pill must not wait for the slow tick.
	serversDirty bool
	// waitsDirty: a wait was recorded or cleared (WaitsChanged); the next
	// pill checkpoint runs without waiting for FastInterval.
	waitsDirty bool
	// restores queues the bindings detached through the API: their title
	// and description go back on the next pass that can reach cmux.
	restores []pigtail.SubjectCmuxBinding
	// trust holds the spawned sessions stopped on Claude's "trust this
	// folder" dialog, by workspace (D14): the handler writes, the loop reads.
	trust map[string]trustSpawn
	// respawn holds the state of each `pigtail session respawn` run, by
	// workspace (D16): the handler writes, the loop reads.
	respawn map[string]respawnMark
	// prompts holds the transcript read cursor of each (workspace, session)
	// on an abandoned mark's surface, loop-owned (respawnPrompted); slowSeq
	// counts the slow passes (a missing transcript is looked up once each).
	prompts map[string]promptCursor
	slowSeq uint64
	// turns is the transcript cursor of each session holding a declared wait
	// (turn_reader.go), loop-owned.
	turns map[string]turnCursor
	// lastPrompted is the last transcript-read verdict of respawnPrompted: a
	// pills-only sync (checkpoint.go) reuses it and never reads a transcript.
	lastPrompted map[string]time.Time
	available    bool
	lastErr      string
	reconcAt     time.Time
	errs         []time.Time
	view         []pigtail.CmuxSidebarWorkspace

	// listDown: the last listing attempt failed while the last good listing
	// is still trusted (ListingMaxAge): pills are written from it, text is
	// not (D1·A). Written by the Run goroutine under mu, read by State.
	listDown      bool
	listErr       string
	listDownSince time.Time

	// foldersNote explains a folder arrangement that is idle or stopped;
	// foldersResume asks Run to lift a stop (SetEnabled(true)). Under mu.
	foldersNote   string
	foldersResume bool
	// foldersWait is, per allowed window, the folder round owed and not yet
	// applied (D3 of sub_x000000000000019, per window since D32 of
	// sub_x000000000000007; noteWait/clearWait); the key "" is the loop's own
	// wait when no allowed window is listed. Written by Run under mu, read by
	// State.
	foldersWait map[string]*pigtail.CmuxFoldersWait
	// waitLog dates the same waits for the daemon log; unavailable() keeps it
	// (runner_folders.go, waitTrace). Under mu.
	waitLog map[string]*waitTrace

	// The D12 toggle (folders_toggle.go): a request is queued on toggleCh and
	// served by Step on the Run goroutine; done closes when Run returns.
	// foldWins and foldWinsAt publish, under mu, the allowed windows of the
	// last good listing and when it was made (the toggle acts on every one,
	// D8; State lists them).
	toggleCh   chan toggleReq
	done       chan struct{}
	foldWins   []string
	foldWinsAt time.Time
}

// surfaceSet is a workspace's open surfaces as one `cmux tree` read them.
type surfaceSet struct {
	ids map[string]bool
	at  time.Time
}

// surfacesMaxAge bounds how long a surface set read before a failing
// listing stays trusted, in slow passes (4 × 30 s by default).
const surfacesMaxAge = 4

type published struct {
	attention string
	count     int
	waitType  string
	reason    string
	subject   string
	servers   string
}

// Config tunes the loop. Zero durations take the spec's values (Defaults).
type Config struct {
	// Enabled is the switch at start (pigtaild --cmux-sidebar).
	Enabled bool
	// Windows restricts every write to these cmux windows (UUIDs). Empty =
	// all windows. Listings still read every window: a workspace is closed
	// only when no window shows it.
	Windows []string

	FastInterval      time.Duration // attention pass (native files): 1 s
	SlowInterval      time.Duration // cmux listings + session snapshot: 30 s
	ReconcileInterval time.Duration // list-status anti-drift: 10 min
	OpInterval        time.Duration // pause between two writes (10 ops/s): 100 ms
	BackoffMin        time.Duration // cmux unreachable: 5 s, doubling…
	BackoffMax        time.Duration // …up to 60 s
	ListingMaxAge     time.Duration // last good listing trusted for pills while listings fail: 10 min (D1·A)
	PanicLimit        int           // panics in PanicWindow that switch the loop off: 5
	PanicWindow       time.Duration // 10 min
	PanicPause        time.Duration // rest after a panic: 30 s
	ShutdownBudget    time.Duration // clearing pills on daemon stop: 2 s

	// ClaudeCodeIdleFix switches the temporary claude_code patch on
	// (claudecode_patch.go; pigtaild -cmux-claude-code-fix, on by default).
	ClaudeCodeIdleFix bool

	// Folders switches the folder arrangement on (pigtaild --cmux-folders;
	// D2, sub_x000000000000041). It acts only while the loop is on.
	Folders bool
	// GroupMoves is !cmux.ReorderMovesGroups (P4b): folder blocks are placed
	// by group moves before any reorder.
	GroupMoves bool
	// AddNeedsRemove is !cmux.AddMovesFromGroup (P2 (b)).
	AddNeedsRemove bool
	// FolderNoSort is pigtaild --cmux-folder-sort=false (sub_x000000000000030,
	// D5-A): the arrangement files each workspace in its folder and never
	// sorts (ArrangeInput.NoSort); the gravity owes no round.
	FolderNoSort   bool
	ArrangeSettle  time.Duration // no group re-read this long after a folder write: 2 s
	FoldersWaitLog time.Duration // a folder round owed this long logs its wait: 2 min
	FocusPoll      time.Duration // at most one focus read per this to start a round: 2 s
	// BirthGrace is how long an unbound, undeclared workspace stays where it
	// was born before « Inbox » takes it: 30 min (D31).
	BirthGrace time.Duration
}

// Defaults are the spec's cadences (§3.1, §4.2, §4.4, §4.9).
func Defaults() Config {
	return Config{
		FastInterval:      time.Second,
		SlowInterval:      30 * time.Second,
		ReconcileInterval: 10 * time.Minute,
		OpInterval:        100 * time.Millisecond,
		BackoffMin:        5 * time.Second,
		BackoffMax:        60 * time.Second,
		ListingMaxAge:     10 * time.Minute,
		PanicLimit:        5,
		PanicWindow:       10 * time.Minute,
		PanicPause:        30 * time.Second,
		ShutdownBudget:    2 * time.Second,
		ArrangeSettle:     2 * time.Second,
		FocusPoll:         2 * time.Second,
		FoldersWaitLog:    2 * time.Minute,
		BirthGrace:        30 * time.Minute,
	}
}

// Deps are the loop's seams. Sidebar and Store are required.
type Deps struct {
	Sidebar cmux.Sidebar
	Store   *store.Store
	// RefreshPeers snapshots the Claude sessions cmux hosts (`cmux sessions
	// list`), upserts them into peer_sessions, and returns their ids. Nil
	// skips the refresh. A failure is logged and pauses nothing. The ids do not decide which session counts: that
	// list is a persisted history that keeps sessions whose pane is long
	// closed; whether a dead session counts is the surface listing's call.
	RefreshPeers func() ([]string, error)
	// Servers reads the dev servers (listening ones and recent deaths), on
	// the slow tick and after ServersChanged; nil = no server pill.
	Servers func() ServerSnapshot
	// Native reads a session's native file; nil = claudesession.ReadNative.
	Native func(cfg string, pid int) (claudesession.Native, bool)
	// Alive reports whether a registered session runs; nil = its pid runs
	// and its native file (read through the cache) still names it.
	Alive func(pigtail.PeerSession) bool
	// PidAlive reports whether a pid runs; nil = claudesession.PidAlive.
	PidAlive func(int) bool
	// Publish emits a bus event; nil = none.
	Publish func(pigtail.Event)
	// ClearWaits drops a session's recorded waits of the given sources when
	// the loop judges them over (WaitTracker): the relay's path, which also
	// removes the wait marker and publishes session.wait_changed. gone: the
	// loop judged the session gone (Ended, or not alive WaitGoneConfirm
	// apart) — the wait its subject owns stays open (D29-A). Nil = the store
	// directly.
	ClearWaits func(sessionID string, sources []string, gone bool)
	// ClearHeldWait drops a session's held wait the loop judges over, only
	// while holderPID still holds it (a conditional delete: a newer holder's
	// wait stays); reports whether a row went. Nil = the store directly.
	ClearHeldWait func(sessionID string, holderPID int) (bool, error)
	// Now is the clock; nil = time.Now.
	Now func() time.Time
	// Sleep paces writes; nil = time.Sleep.
	Sleep func(time.Duration)
	// Warnings adds environment warnings to the state (cmux settings that
	// hide the pills); nil = none.
	Warnings func() []string
	// Logf logs; nil = log.Printf.
	Logf func(string, ...any)
	// MarkersChanged rewrites the workspace-subject markers after a closed workspace lost its link or off-topic row; nil = none.
	MarkersChanged func()
	// Maintenance reads the maintenance state (maintenance.Watch.Current);
	// nil = no pill.
	Maintenance func() (maintenance.State, bool, error)
	// MaintenanceDir is the directory Maintenance reads, named in the log
	// line of an unreadable state (its errors carry the cause, not the path).
	MaintenanceDir string
	// KnownCells lists every cell id of the cell table, so a cell-only
	// description posed before a restart is still recognised as pigtail's;
	// nil = no recognition.
	KnownCells func() map[string]bool
	// FrozenSessions lists the sessions the current freeze refused (D4,
	// maintenance.FrozenSessions); nil = none.
	FrozenSessions func() map[string]bool
	// CmuxCheck reads the surfaces the last `pigtail cmux check` left in
	// defect (D13, daemon.cmuxCheckDefects); nil = no check pill.
	CmuxCheck func() ([]CheckDefect, error)
	// DeadInputs lists the surfaces whose input the daemon's probe found
	// dead (sub_x000000000000024, memory only); nil = none.
	DeadInputs func() []string

	// Folders writes the cmux groups (internal/cmux/folders.go); nil = no
	// folder arrangement.
	Folders cmux.Folders
	// FolderOrder is the folder order (D6), « Inbox » and « Standby »
	// (last) included; nil = no folder arrangement.
	FolderOrder func() []string
	// StandbyGone ends the Standby of a session whose workspace two successful
	// listings missed (closeMissing; the relay's EndStandbyGone: row, marker,
	// event); nil = no purge.
	StandbyGone func(session, workspace string) error
	// StandbyPinned ends the Standby of a session whose tab is pinned by hand
	// (D29 of sub_x000000000000014: a pin is a prio, the last gesture wins;
	// the relay's EndStandbyPinned: row, marker, event, log line); nil = the
	// pinned tab in Standby is left alone.
	StandbyPinned func(session, workspace string) error
	// Standby is the session ids in Standby (Store.ListStandby, one read for
	// the whole table); an error skips the round. nil = none.
	Standby func() (map[string]bool, error)
	// MacNeed is the session ids holding a Mac wait that announced a duration
	// (MacNeedSessions over Store.ListSessionWaits, one read per pass): the
	// ones « Maintenance » files, if alive. An error skips the round. nil = no
	// « Maintenance » folder: the arrangement runs as before it.
	MacNeed func() (map[string]bool, error)
	// Paused is the session_pause rows (Store.ListPauses), for the
	// « Paused » pill (paused.go); nil = none.
	Paused func() ([]pigtail.SessionPause, error)
	// RoleAlerts is the open role alerts, for the « Role » pill (role.go); nil = none.
	RoleAlerts func() ([]RoleAlert, error)
	// PauseGone ends the pause of a session whose workspace two successful
	// listings missed (closeMissingPause; the relay's EndPauseGone: row,
	// event); nil = no purge.
	PauseGone func(session, workspace string) error
	// Focus is the open focus (sub_x000000000000007), read once per pass:
	// FocusSource over Store.ActiveFocus. Its error is a read error only and
	// skips the round (a seal error is logged by FocusSource, never returned).
	// nil = no focus.
	Focus func() ([]pigtail.Focus, error)
	// FolderIcon is a folder's SF Symbol (D11); nil or "" = none set.
	FolderIcon func(folder string) string
	// ShellAtPrompt reports whether a surface's shell sits at its prompt (the
	// D22 proof of cmuxcheck); nil = no anchor is ever closable (spec 3).
	ShellAtPrompt func(surfaceID string) (bool, error)
	// SurfaceNeverRendered reports whether cmux never rendered a surface (tty
	// null and render_health not_started, cmux.SurfaceNeverRendered): such a
	// generated anchor has no shell to prove and counts as bare (D22); nil =
	// the shell proof only.
	SurfaceNeverRendered func(surfaceID string) (bool, error)
	// LoadCollapse and SaveCollapse keep the folded state of the folders
	// across a restart (spec 7, LoadCollapseFile / SaveCollapseFile); nil =
	// memory only.
	LoadCollapse func() (map[string]bool, error)
	SaveCollapse func(map[string]bool) error
	// LoadBirths and SaveBirths keep the birth clock across a restart
	// (births.go; the boolean says the file exists); nil = memory only, read
	// as an absent file.
	LoadBirths func() (map[string]time.Time, bool, error)
	SaveBirths func(map[string]time.Time) error
	// BirthsModTime says when the birth file was last written (births.go,
	// BirthsFileModTime). A file older than Config.BirthGrace says nothing
	// about the tabs opened since: nil = not asked.
	BirthsModTime func() (time.Time, error)
	// FolderReads is the arrangement's store reads; nil = Store.
	FolderReads folderReads
	// SubjectSpaceSet writes a subject's own space after a hand drag (D5–D7 of
	// sub_x000000000000033): space "" = its theme's; from is the effective space
	// before, for the trace. nil = no space drag is read: a dragged tab is filed back.
	SubjectSpaceSet func(subjectID, space, from string) error
	// OffTopicSpaceSet rewrites an off-topic declaration's space after a hand
	// drag (D2), traced (Store.SetOffTopicSpaceByDrag, never the PATCH path).
	OffTopicSpaceSet func(workspace, space, from string) error
	// SpaceScope is a space's scope (spaces.yaml); ok false = unknown.
	SpaceScope func(name string) (scope string, ok bool)
	// ReadPrompts reads the user's prompts after an instant from a byte
	// offset of a transcript (respawnPrompted); nil = claudesession.PromptsAfter.
	ReadPrompts func(path string, from int64, since time.Time) ([]claudesession.Prompt, int64, error)
	// FindTranscript resolves a session's transcript path in its config dir;
	// nil = claudesession.Find. An error or none = ("", false).
	FindTranscript func(sessionID, configDir string) (string, bool)
	// ReadTurns tells whether a transcript holds an entry that opens a model
	// turn after an instant, read from a byte offset (a declared wait's
	// confirmation of the next turn); nil = claudesession.TurnOpenedAfter.
	ReadTurns func(path string, from int64, since time.Time) (ran, covered bool, next int64, err error)
}

// folderReads is the narrow seam of the five store reads of the folder
// arrangement: an error there skips the round, never reads as « no theme »,
// « no subject » or « not hosted ».
type folderReads interface {
	ListPeers(includeEnded bool) ([]pigtail.PeerSession, error)
	GetTheme(id string) (*pigtail.Theme, error)
	GetSubject(id string) (*pigtail.Subject, error)
	ListAllCmuxBindings() ([]pigtail.SubjectCmuxBinding, error)
	ListOffTopic() ([]pigtail.WorkspaceOffTopic, error)
}

// spaceDragWired: the three space drag Deps are set, so hand drags into a
// space folder are read (space_drag.go) and the engine's writes are noted.
func (r *Runner) spaceDragWired() bool {
	return r.deps.SubjectSpaceSet != nil && r.deps.OffTopicSpaceSet != nil && r.deps.SpaceScope != nil
}

// New builds a runner. It does nothing until Run (or Step) is called.
func New(cfg Config, deps Deps) *Runner {
	def := Defaults()
	setDur := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	setDur(&cfg.FastInterval, def.FastInterval)
	setDur(&cfg.SlowInterval, def.SlowInterval)
	setDur(&cfg.ReconcileInterval, def.ReconcileInterval)
	setDur(&cfg.OpInterval, def.OpInterval)
	setDur(&cfg.BackoffMin, def.BackoffMin)
	setDur(&cfg.BackoffMax, def.BackoffMax)
	setDur(&cfg.ListingMaxAge, def.ListingMaxAge)
	setDur(&cfg.PanicWindow, def.PanicWindow)
	setDur(&cfg.PanicPause, def.PanicPause)
	setDur(&cfg.ShutdownBudget, def.ShutdownBudget)
	setDur(&cfg.ArrangeSettle, def.ArrangeSettle)
	setDur(&cfg.FocusPoll, def.FocusPoll)
	setDur(&cfg.FoldersWaitLog, def.FoldersWaitLog)
	setDur(&cfg.BirthGrace, def.BirthGrace)
	if cfg.PanicLimit <= 0 {
		cfg.PanicLimit = def.PanicLimit
	}
	for i, w := range cfg.Windows {
		cfg.Windows[i] = strings.ToUpper(strings.TrimSpace(w))
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Sleep == nil {
		deps.Sleep = time.Sleep
	}
	if deps.Logf == nil {
		deps.Logf = log.Printf
	}
	if deps.FolderReads == nil && deps.Store != nil {
		deps.FolderReads = deps.Store
	}
	return &Runner{
		cfg: cfg, deps: deps,
		applied:       map[string]Applied{},
		natives:       newNativeCache(deps.Native),
		surfaces:      map[string]surfaceSet{},
		lastTextWrite: map[string]time.Time{},
		reverts:       map[string]*revertState{},
		cellText:      map[string]string{},
		published:     map[string]published{},
		statusWrites:  map[string]uint64{},
		wake:          make(chan struct{}, 1),
		toggleCh:      make(chan toggleReq, 1),
		done:          make(chan struct{}),
		want:          cfg.Enabled,
	}
}

// Run drives the loop until ctx ends, then clears the pills it posed within
// ShutdownBudget. Titles stay: they are persisted, and the next start (or a
// switch-off) gives them back.
func (r *Runner) Run(ctx context.Context) {
	defer close(r.done) // after shutdown: a late toggle gets ErrFoldersIdle
	t := time.NewTicker(r.cfg.FastInterval)
	defer t.Stop()
	hold := time.NewTimer(time.Hour)
	defer hold.Stop()
	var arm holdArm
	armHold := func() { // after every pass or wake: the nearest hold's end
		if !hold.Stop() {
			select {
			case <-hold.C:
			default:
			}
		}
		if wait, ok := arm.next(r.holdDeadline(), r.deps.Now()); ok {
			hold.Reset(wait)
		}
	}
	r.Step()
	armHold()
	for {
		select {
		case <-ctx.Done():
			r.shutdown()
			return
		case <-hold.C:
			arm.fired(r.holdDeadline())
			r.holdWake()
		case <-t.C:
			r.Step()
		case <-r.wake:
			r.Step()
		}
		armHold()
	}
}

// holdArm decides when Run's hold timer is armed. A deadline already passed
// (it ended during a pass or a wake) wakes at once, once per deadline.
type holdArm struct{ woken time.Time }

// next is the delay to arm for the deadline d (zero: none running) seen at
// now, and whether to arm at all.
func (a *holdArm) next(d, now time.Time) (time.Duration, bool) {
	if d.IsZero() {
		return 0, false
	}
	if wait := d.Sub(now); wait > 0 {
		return wait, true
	}
	if d.Equal(a.woken) {
		return 0, false // already woken for it: the tick decides, no hot loop
	}
	return 0, true // passed during a pass: wake at once
}

// fired records that the wake for deadline d ran.
func (a *holdArm) fired(d time.Time) { a.woken = d }

// holdDeadline is when the nearest waiting hold ends (zero: none running).
// Run goroutine only, like the damper it reads.
func (r *Runner) holdDeadline() time.Time { return r.damper.NextDeadline() }

// holdWake runs at the end of a waiting hold, between two ticks: it syncs
// the pills alone, from the listing the loop holds, so the pill goes with
// the hold and not a tick later. No listing, no text, no folder, and no cmux
// call when nothing changes. Skipped (the tick decides) whenever a pass
// would not write from the state it has: loop off or paused, cmux down, a
// reconcile owed, a listing too old or degraded. Run goroutine only, never
// inside a pass.
func (r *Runner) holdWake() {
	now := r.deps.Now()
	r.mu.Lock()
	on := r.want
	r.mu.Unlock()
	if !on || !r.active || now.Before(r.pauseUntil) || !r.isAvailable() ||
		r.needReconcile || r.listDown || !r.cacheUsable(now) {
		return
	}
	if _, err := r.sync(now, true); err != nil {
		r.deps.Logf("cmuxsync: hold wake: %v (left to the pass)", err)
	}
}

// --- control (any goroutine) ------------------------------------------------

// SetEnabled switches the loop on or off until the daemon restarts. Off
// clears the owned pills and gives the titles and descriptions back on the
// next pass. On also lifts a switch-off caused by repeated panics.
func (r *Runner) SetEnabled(on bool) {
	r.mu.Lock()
	r.want = on
	if on {
		r.panicked = false
		r.foldersResume = true // lifts a folder stop (runner_folders.go)
	} else {
		r.foldWins, r.foldWinsAt = nil, time.Time{} // no toggle from a list read before
		r.foldersWait = nil                         // no « since » that runs on while off
		r.waitLog = nil
	}
	r.mu.Unlock()
	r.poke()
}

// Nudge asks for an immediate pass with fresh cmux listings (a link was
// written or retitled).
func (r *Runner) Nudge() {
	r.mu.Lock()
	r.nudge = true
	r.mu.Unlock()
	r.poke()
}

// trustSpawn is a spawned Claude stopped on "trust this folder" (D14). Memory
// only: a daemon restart forgets it.
type trustSpawn struct {
	pid   int
	since time.Time
}

// TrustPending records a spawned session waiting on Claude's trust dialog in
// workspace wsID and asks for a pass with fresh listings.
func (r *Runner) TrustPending(wsID string, pid int) {
	r.mu.Lock()
	if r.trust == nil {
		r.trust = map[string]trustSpawn{}
	}
	r.trust[strings.ToUpper(wsID)] = trustSpawn{pid: pid, since: r.deps.Now()}
	r.nudge = true
	r.mu.Unlock()
	r.poke()
}

// trustPendingSet is the workspaces still waiting on the trust dialog. One is
// dropped once its session registered and started (a session of that
// workspace whose native file was read: Claude writes it only after the
// dialog), once its Claude no longer runs (pane closed, or quit, without
// accepting), or once a listing made after it no longer shows its workspace.
func (r *Runner) trustPendingSet(ss []Session) map[string]bool {
	started := map[string]bool{}
	for _, s := range ss {
		if !s.NativeStatusAt.IsZero() {
			started[s.WorkspaceID] = true
		}
	}
	alive := r.deps.PidAlive
	if alive == nil {
		alive = claudesession.PidAlive
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for ws, p := range r.trust {
		switch {
		case started[ws], !alive(p.pid), r.listedAt.After(p.since) && !r.allowed[ws]:
			delete(r.trust, ws)
		default:
			out[ws] = true
		}
	}
	return out
}

// RespawnWaitReason is the pill's reason for each wait a respawn may report
// (pigtail.CmuxRespawnRequest.Wait): the daemon's one table of those words.
var RespawnWaitReason = map[string]string{
	pigtail.CmuxRespawnWaitDraft:      "draft in the input",
	pigtail.CmuxRespawnWaitBackground: "background work",
	pigtail.CmuxRespawnWaitScreen:     "screen unreadable",
}

// RespawnMark records the state of a respawn run on workspace wsID (D16),
// replacing the workspace's last one; the request was validated by the
// handler. An unchanged mark (the worker's periodic re-post) asks for no
// pass; a new one asks for a pass with fresh listings.
func (r *Runner) RespawnMark(wsID string, req pigtail.CmuxRespawnRequest) {
	m := respawnMark{state: req.State, surface: strings.ToUpper(req.SurfaceID)}
	if req.State == pigtail.CmuxRespawnWaiting {
		m.reason, m.pid = RespawnWaitReason[req.Wait], req.PID
	}
	ws := strings.ToUpper(wsID)
	r.mu.Lock()
	if r.respawn == nil {
		r.respawn = map[string]respawnMark{}
	}
	if old, ok := r.respawn[ws]; ok && old.state == m.state && old.reason == m.reason && old.surface == m.surface && old.pid == m.pid {
		r.mu.Unlock()
		return
	}
	m.since = r.deps.Now()
	r.respawn[ws] = m
	r.nudge = true
	r.mu.Unlock()
	r.poke()
}

// RespawnClear drops workspace wsID's respawn mark (the worker stopped
// waiting, or the respawn succeeded).
func (r *Runner) RespawnClear(wsID string) {
	ws := strings.ToUpper(wsID)
	r.mu.Lock()
	_, ok := r.respawn[ws]
	delete(r.respawn, ws)
	if ok {
		r.nudge = true
	}
	r.mu.Unlock()
	if ok {
		r.poke()
	}
}

// respawnSet is the respawn marks still standing. Any mark drops once a
// listing made after it no longer shows its workspace. A waiting one drops
// once its worker no longer runs. A final one (abandoned, failed) stays
// until the user acts on its surface, read on a live session there:
//   - relaunched: a session started (native startedAt) after the mark, in
//     any status — a failed respawn closed the old one itself, so this is
//     Claude launched (or resumed) by hand, often straight with a prompt and
//     never read idle (incident 2026-10-04). The respawn's own launch that
//     failed its checks started before the mark and does not count. The
//     daemon's clock against Claude's: one host, one wall clock.
//   - prompted: a session read busy after an idle read seen since the mark
//     — the old session still there (abandoned), both instants on Claude's
//     clock (statusUpdatedAt), as WaitTracker compares them.
//   - prompted in the transcript (prompted, from respawnPrompted), for an
//     abandoned mark only: a typed prompt (a user entry or a queued_command
//     attachment, origin human, an image-only one included; not pigtaild's
//     wake or /compact, not a turnless command) stamped after the mark, in a
//     live session of the surface — the one clear that holds while Claude
//     keeps its native file busy for a background task (incident
//     2026-10-23). A failed mark never reads it: the respawn's own fresh
//     launch carries a prompt in argv.
func (r *Runner) respawnSet(ss []Session, prompted map[string]time.Time) map[string]respawnMark {
	alive := r.deps.PidAlive
	if alive == nil {
		alive = claudesession.PidAlive
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]respawnMark{}
	for ws, m := range r.respawn {
		if r.listedAt.After(m.since) && !r.allowed[ws] {
			delete(r.respawn, ws)
			continue
		}
		if m.state == pigtail.CmuxRespawnWaiting {
			if !alive(m.pid) {
				delete(r.respawn, ws)
				continue
			}
			out[ws] = m
			continue
		}
		acted := false
		// prompted only ever holds abandoned marks; the since check keeps a
		// newer mark clear of a cursor read for an older one.
		if t, ok := prompted[ws]; ok && t.Equal(m.since) {
			acted = true
		}
		for _, s := range ss {
			if s.WorkspaceID != ws || s.SurfaceID != m.surface || !s.Alive {
				continue
			}
			switch {
			case s.StartedAt.After(m.since): // relaunched
				acted = true
			case s.NativeStatusAt.IsZero():
			case s.Status == "idle" && m.idleStatusAt.IsZero():
				m.idleStatusAt = s.NativeStatusAt
			case s.Status == "busy" && !m.idleStatusAt.IsZero() && s.NativeStatusAt.After(m.idleStatusAt): // prompted
				acted = true
			}
		}
		if acted {
			delete(r.respawn, ws)
			continue
		}
		r.respawn[ws] = m
		out[ws] = m
	}
	return out
}

// Released queues the restore of a workspace whose link was removed through
// the API (detach): b is the binding as it was before the delete.
func (r *Runner) Released(b pigtail.SubjectCmuxBinding) {
	if len(RestoreOps(b)) == 0 {
		return
	}
	r.mu.Lock()
	r.restores = append(r.restores, b)
	r.mu.Unlock()
	r.poke()
}

// ServersChanged asks for an immediate pass with a fresh server reading: a
// dev server died or was evicted (resources.server_died / server_evicted),
// and its pill should not wait up to a slow interval. No cmux listing.
func (r *Runner) ServersChanged() {
	r.mu.Lock()
	r.serversDirty = true
	r.mu.Unlock()
	r.poke()
}

func (r *Runner) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// State is the loop's state for GET /cmux/sidebar, unfiltered: the handler
// drops the subjects the caller may not read.
func (r *Runner) State() pigtail.CmuxSidebarState {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.deps.Now()
	st := pigtail.CmuxSidebarState{
		Enabled:           r.want,
		ClaudeCodeIdleFix: r.cfg.ClaudeCodeIdleFix,
		DisabledByPanic:   r.panicked,
		CmuxAvailable:     r.available,
		ErrorsLastHour:    countSince(r.errs, now.Add(-time.Hour)),
		Windows:           append([]string(nil), r.cfg.Windows...),
		Folders:           r.cfg.Folders && r.want,
		FoldersNote:       r.foldersNote,
		Warnings:          []string{},
		Workspaces:        make([]pigtail.CmuxSidebarWorkspace, 0, len(r.view)),
	}
	if !r.reconcAt.IsZero() {
		t := r.reconcAt.UTC()
		st.LastReconcileAt = &t
	}
	if r.panicked {
		st.Warnings = append(st.Warnings, "switched off after repeated panics: pigtail cmux sidebar on to retry")
	}
	if r.want && !r.available && r.lastErr != "" {
		st.Warnings = append(st.Warnings, "cmux unreachable: "+r.lastErr)
	}
	if r.want && r.available && r.listDown {
		st.Warnings = append(st.Warnings, "cmux listings failing since "+r.listDownSince.Local().Format("15:04")+": pills from the last listing, titles paused")
	}
	if r.deps.Warnings != nil {
		st.Warnings = append(st.Warnings, r.deps.Warnings()...)
	}
	if r.want && r.cfg.Folders && r.available { // unreachable: no window read, no round applied
		st.FoldersWindows = slices.Clone(r.foldWins)
		st.FoldersWait = r.waitList()
	}
	if !r.want {
		// Switched off: the last view is about to be cleared by the loop;
		// answering with it would show pills that are being removed.
		return st
	}
	for _, w := range r.view {
		c := w
		c.Servers = append([]pigtail.CmuxSidebarServer{}, w.Servers...)
		c.Cells = append([]string(nil), w.Cells...)
		c.CellScopes = append([]string(nil), w.CellScopes...)
		if w.Subject != nil {
			s := *w.Subject
			c.Subject = &s
		}
		st.Workspaces = append(st.Workspaces, c)
	}
	return st
}

func countSince(ts []time.Time, since time.Time) int {
	n := 0
	for _, t := range ts {
		if !t.Before(since) {
			n++
		}
	}
	return n
}

// --- the pass (Run goroutine) -------------------------------------------------

// errUnavailable marks a pass cut short because cmux did not answer.
var errUnavailable = errors.New("cmux unavailable")

// Step runs one pass. A panic is recovered, reported, and followed by a rest;
// PanicLimit of them within PanicWindow switch the loop off.
func (r *Runner) Step() {
	now := r.deps.Now()
	defer func() {
		if rec := recover(); rec != nil {
			r.onPanic(now, rec)
		}
	}()
	select { // D12: between two passes, on the Run goroutine
	case q := <-r.toggleCh:
		r.serveToggle(q)
	default:
	}
	r.pass(now)
}

func (r *Runner) pass(now time.Time) {
	start := r.deps.Now()
	r.timing = passTiming{}
	defer func() {
		if d := r.deps.Now().Sub(start); d > SlowPassLog {
			r.deps.Logf("%s", r.timing.line(d))
		}
	}()
	r.mu.Lock()
	on, nudge, serversDirty := r.want, r.nudge, r.serversDirty
	r.nudge, r.serversDirty = false, false
	r.waitsDirty = false // the pass's own sync covers any change from before
	restores := r.restores
	r.restores = nil
	r.mu.Unlock()

	if !on {
		if !r.active {
			r.requeue(restores) // given back once the loop can reach cmux again
			return              // switched off: not one cmux call
		}
		for _, b := range restores {
			if err := r.restore(now, b); err != nil {
				r.deps.Logf("cmuxsync: switch-off: %v", err)
			}
		}
		r.deactivate(now)
		return
	}
	if now.Before(r.pauseUntil) {
		r.requeue(restores)
		return
	}
	if !r.isAvailable() && now.Before(r.retryAt) {
		r.requeue(restores)
		return
	}
	if !r.active {
		r.active, r.needReconcile = true, true
		r.lastSlow = time.Time{}
		r.rearmBirths() // off → on: the clock did not run meanwhile
		r.forgetSpaceDrags()
	}
	// Pill checkpoints (D3 b): only from an applied state seeded by cmux and a
	// listing young enough to write pills from.
	r.ckptArmed = r.isAvailable() && !r.needReconcile && r.cacheUsable(now)
	defer func() { r.ckptArmed = false }()

	listFailed := false // counted once, at the end, unless the pass ends in unavailable (which counts it)
	slowDue := r.lastSlow.IsZero() || nudge || !r.isAvailable() || r.listDown || now.Sub(r.lastSlow) >= r.cfg.SlowInterval
	// While degraded, listings wait for their own backoff (also while writes
	// are paused: retrying a failing listing on every pass would hammer a
	// saturated cmux). listRetryAt is always finite, so the pause is left at
	// the first listing that answers.
	slowPass := slowDue && (!r.listDown || !now.Before(r.listRetryAt))
	listedOK := false // the degraded mode is left only once the reconcile it owes answers too
	if slowPass {
		r.slowSeq++
		var slowErr error
		r.timed(&r.timing.slow, func() { slowErr = r.slow(now) })
		if err := slowErr; err != nil {
			if !r.cacheUsable(now) {
				r.requeue(restores)
				r.unavailable(now, err)
				return
			}
			r.listingFailed(now, err)
			listFailed = true
		} else {
			listedOK = true
		}
	}
	// Checked after the attempt, never before it: a pass in the listing
	// backoff must not write from a listing older than ListingMaxAge.
	if r.listDown && !r.cacheUsable(now) {
		r.requeue(restores)
		r.unavailable(now, fmt.Errorf("cmux listings failing for over %s: %s", r.cfg.ListingMaxAge, r.listErr))
		return
	}
	if (!r.listDown || listedOK) && (r.needReconcile || now.Sub(r.lastReconcile) >= r.cfg.ReconcileInterval) {
		var recErr error
		r.timed(&r.timing.reconcile, func() { recErr = r.reconcile(now) })
		if err := recErr; err != nil {
			// The listing just succeeded: the cache is usable. A degraded
			// mode already on keeps its backoff and start time.
			r.listingFailed(now, err)
			listFailed, listedOK = true, false
		}
	}
	if listedOK {
		r.listingOK()
	}
	if !r.listDown {
		r.setAvailable()
	}
	if serversDirty && !r.serversAt.Equal(now) {
		r.readServers(now)
	}
	if r.listDown {
		r.requeue(restores) // a restore writes text: it waits for a fresh listing
		restores = nil
	}
	for i, b := range restores {
		if err := r.restore(now, b); err != nil {
			r.requeue(restores[i:])
			r.unavailable(now, err)
			return
		}
	}
	var written int
	var err error
	r.timed(&r.timing.sync, func() { written, err = r.sync(r.deps.Now(), false) })
	if err != nil {
		r.unavailable(now, err)
		return
	}
	if listFailed {
		r.countError(now)
	}
	if r.cfg.Folders && r.deps.Folders != nil && r.deps.FolderOrder != nil {
		switch {
		case !r.listDown:
			r.timed(&r.timing.arrange, func() { r.arrange(now) })
		case !r.foldersStopped: // stopped stays the reason: no listing would lift it
			r.noteWait(now, r.dirtyWins(), waitListingDown) // a round owed, no listing to apply it from
		}
	}
	if r.listDown && written > 0 {
		// Only a write that went through proves cmux takes them; a degraded
		// pass with nothing to write leaves the availability as it was.
		r.setAvailable()
	}
	if slowPass && !r.listDown && r.cfg.ClaudeCodeIdleFix {
		r.timed(&r.timing.claudeCode, func() { r.fixClaudeCode(now) })
	}
}

func (r *Runner) requeue(bs []pigtail.SubjectCmuxBinding) {
	if len(bs) == 0 {
		return
	}
	r.mu.Lock()
	r.restores = append(append([]pigtail.SubjectCmuxBinding(nil), bs...), r.restores...)
	r.mu.Unlock()
}

func (r *Runner) isAvailable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.available
}

func (r *Runner) setAvailable() {
	r.mu.Lock()
	was := r.available
	r.available, r.lastErr = true, ""
	r.mu.Unlock()
	r.backoff = 0
	if !was {
		r.deps.Logf("cmuxsync: cmux reachable, sidebar loop active")
	}
}

// unavailable backs off (BackoffMin doubling to BackoffMax) and forces a full
// listing and reconcile once cmux answers again. Logged once per change.
func (r *Runner) unavailable(now time.Time, err error) {
	r.mu.Lock()
	was := r.available || r.lastErr == ""
	r.available, r.lastErr = false, oneLine(err.Error())
	r.errs = append(pruneBefore(r.errs, now.Add(-time.Hour)), now)
	r.foldWins, r.foldWinsAt = nil, time.Time{}
	// No round can be applied: no wait running on stale (D3 of
	// sub_x000000000000019).
	r.foldersWait = nil
	r.mu.Unlock()
	r.pinned.Store(nil) // the ticks return early for up to BackoffMax: no stale pin meanwhile
	if r.backoff == 0 {
		r.backoff = r.cfg.BackoffMin
	} else {
		r.backoff *= 2
	}
	if r.backoff > r.cfg.BackoffMax {
		r.backoff = r.cfg.BackoffMax
	}
	r.retryAt = now.Add(r.backoff)
	r.needReconcile = true
	if was {
		r.deps.Logf("cmuxsync: cmux unreachable, writes paused: %v", err)
	}
}

// cacheUsable: a good listing exists and is young enough to write pills from.
func (r *Runner) cacheUsable(now time.Time) bool {
	return !r.listedAt.IsZero() && now.Sub(r.listedAt) <= r.cfg.ListingMaxAge
}

// listingFailed enters (or stays in) the degraded mode: pills from the last
// good listing, no text, listings retried on their own backoff, a reconcile
// owed once they answer. Logged once per change.
func (r *Runner) listingFailed(now time.Time, err error) {
	if r.listBackoff == 0 {
		r.listBackoff = r.cfg.BackoffMin
	} else {
		r.listBackoff *= 2
	}
	if r.listBackoff > r.cfg.BackoffMax {
		r.listBackoff = r.cfg.BackoffMax
	}
	// From the end of the attempt, not the start of the pass: a listing that
	// hung up to its 10 s bound would otherwise have spent the 5 s and 10 s
	// backoffs inside the call and be retried on the very next pass.
	r.listRetryAt = r.deps.Now().Add(r.listBackoff)
	r.needReconcile = true // the error is counted by pass (once), not here
	r.mu.Lock()
	first := !r.listDown
	r.listDown, r.listErr = true, oneLine(err.Error())
	if first {
		r.listDownSince = now
	}
	r.mu.Unlock()
	if first {
		r.deps.Logf("cmuxsync: cmux listing failed, pills written from the last listing (%s old), titles paused: %v",
			now.Sub(r.listedAt).Round(time.Second), err)
	}
}

// listingOK leaves the degraded mode.
func (r *Runner) listingOK() {
	r.listBackoff, r.listRetryAt = 0, time.Time{}
	r.mu.Lock()
	was := r.listDown
	r.listDown, r.listErr, r.listDownSince = false, "", time.Time{}
	r.mu.Unlock()
	if was {
		r.deps.Logf("cmuxsync: cmux listings answer again")
	}
}

// refreshPeers takes the registry snapshot (`cmux sessions`). Its failure is
// logged once and pauses nothing: the hooks keep registering sessions, and
// the pills read the store (D1·A; 30 of 55 pauses on 2026-10-04).
func (r *Runner) refreshPeers(now time.Time) {
	if r.deps.RefreshPeers == nil {
		return
	}
	var err error
	r.timed(&r.timing.peers, func() { _, err = r.deps.RefreshPeers() })
	switch {
	case err != nil && !r.peersDown:
		r.peersDown = true
		r.countError(now)
		r.deps.Logf("cmuxsync: cmux sessions failed, registry not refreshed: %v", err)
	case err == nil && r.peersDown:
		r.peersDown = false
		r.deps.Logf("cmuxsync: cmux sessions answers again")
	}
}

func pruneBefore(ts []time.Time, before time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if !t.Before(before) {
			out = append(out, t)
		}
	}
	return out
}

func oneLine(s string) string { return cmux.OneLine(s, 240) }

// slow reads every window's workspaces, their open surfaces and the session
// snapshot, then applies the closed-workspace rule to the links (spec §4.6).
func (r *Runner) slow(now time.Time) error {
	r.pillCheckpoint()
	started := r.deps.Now()
	wins, err := r.deps.Sidebar.ListWindows()
	if err != nil {
		r.pinned.Store(nil)
		return err
	}
	var all []Workspace
	var surfErr error
	listed := map[string]bool{}
	for _, w := range wins {
		r.pillCheckpoint()
		ws, err := r.deps.Sidebar.ListWorkspaces(w.ID)
		if err != nil {
			r.pinned.Store(nil)
			return err
		}
		r.pillCheckpoint()
		sf, err := r.deps.Sidebar.ListSurfaces(w.ID)
		if err != nil && surfErr == nil {
			surfErr = err
		}
		for _, x := range ws {
			listed[x.ID] = true
			if err == nil {
				r.setSurfaces(x.ID, sf, now)
			}
			all = append(all, WorkspaceFromCmux(w.ID, x))
		}
	}
	r.noteSurfaces(now, surfErr)
	for id := range r.surfaces {
		if !listed[id] {
			delete(r.surfaces, id)
		}
	}
	r.listedAt, r.listStartedAt, r.lastSlow = r.deps.Now(), started, now
	r.readServers(now)
	r.workspaces, r.allowed, r.folderWins = nil, map[string]bool{}, nil
	for _, w := range wins {
		if r.windowAllowed(w.ID) {
			r.folderWins = append(r.folderWins, w.ID)
		}
	}
	r.forgetClosedWindows() // a window no longer listed loses its arrangement state (D8)
	r.mu.Lock()
	r.foldWins, r.foldWinsAt = slices.Clone(r.folderWins), r.listedAt
	r.mu.Unlock()
	for _, w := range all {
		if r.windowAllowed(w.WindowID) {
			w.Surfaces = r.openSurfaces(w.ID, now)
			r.workspaces = append(r.workspaces, w)
			r.allowed[w.ID] = true
		}
	}
	r.storePinned()
	if len(wins) > 0 {
		r.closeMissing(all)
		var listedIDs, allIDs []string
		for _, w := range r.workspaces {
			listedIDs = append(listedIDs, w.ID)
		}
		for _, w := range all {
			allIDs = append(allIDs, w.ID)
		}
		r.noteBirths(listedIDs, allIDs, now)
	}
	r.pillCheckpoint()
	r.refreshPeers(now)
	return nil
}

// storePinned publishes the pinned workspaces of the windows the loop works
// on (r.workspaces), from the rows slow() just read: no cmux call of its own.
// Only a row with pinned=true counts, not a member of a pinned group (D9).
func (r *Runner) storePinned() {
	set := map[string]bool{}
	for _, w := range r.workspaces {
		if w.Pinned {
			set[strings.ToUpper(w.ID)] = true
		}
	}
	r.pinned.Store(&set)
}

// PinnedWorkspaces is a copy of the pinned cmux workspace ids (upper case) of
// the last trusted pass, for admission's focus priority. nil before the first
// pass, when the loop never ran, was switched off, or stopped being trusted
// (a failed listing or write). It makes no cmux call.
func (r *Runner) PinnedWorkspaces() map[string]bool {
	p := r.pinned.Load()
	if p == nil {
		return nil
	}
	out := make(map[string]bool, len(*p))
	for k, v := range *p {
		out[k] = v
	}
	return out
}

// readServers refreshes the servers Compute shows. Cheap enough for the slow
// tick and the rare server event: one port probe pass in rammon.
func (r *Runner) readServers(now time.Time) {
	if r.deps.Servers == nil {
		return
	}
	r.serverView = r.servers.Observe(now, r.deps.Servers())
	r.serversAt = now
}

// setSurfaces records what a successful `cmux tree` of the workspace's
// window showed. A workspace the tree missed (opened between the two
// listings) gets no set: unknown until the next pass, so never dead.
func (r *Runner) setSurfaces(wsID string, tree map[string][]string, now time.Time) {
	ids, ok := tree[wsID]
	if !ok {
		delete(r.surfaces, wsID)
		return
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[strings.ToUpper(id)] = true
	}
	r.surfaces[wsID] = surfaceSet{ids: set, at: now}
}

// openSurfaces is the set Compute trusts for a workspace, nil when unknown.
//
// When `cmux tree` fails, the last good set is kept rather than dropped:
// dropping it would clear a real "Dead" and light it again on recovery —
// the pill would blink with every transient failure. The failed listing
// itself never adds a surface, so it cannot make a session dead; the only
// cost is a pane closed during the failure, whose dead session keeps its
// pill until the set expires. Past surfacesMaxAge slow passes the set is
// no longer trusted: the pill goes once and stays off while cmux fails.
func (r *Runner) openSurfaces(wsID string, now time.Time) map[string]bool {
	set, ok := r.surfaces[wsID]
	if !ok || now.Sub(set.at) > surfacesMaxAge*r.cfg.SlowInterval {
		return nil
	}
	return set.ids
}

// noteSurfaces logs a failing surface listing once per change. It does not
// make cmux unreachable: the rest of the pass (waiting pills, titles) does
// not depend on it.
func (r *Runner) noteSurfaces(now time.Time, err error) {
	switch {
	case err != nil && !r.surfacesDown:
		r.surfacesDown = true
		r.countError(now)
		r.deps.Logf("cmuxsync: cmux tree failed, last surfaces kept, no new dead pill: %v", err)
	case err == nil && r.surfacesDown:
		r.surfacesDown = false
		r.deps.Logf("cmuxsync: cmux tree answers again")
	}
}

func (r *Runner) windowAllowed(id string) bool {
	if len(r.cfg.Windows) == 0 {
		return true
	}
	for _, w := range r.cfg.Windows {
		if w == id {
			return true
		}
	}
	return false
}

// closeMissing drops a link (a Standby row too, closeMissingStandby) whose workspace two consecutive successful
// listings missed (UUIDs survive a cmux restart, spec C2: absent = closed).
func (r *Runner) closeMissing(all []Workspace) {
	live := make(map[string]bool, len(all))
	for _, w := range all {
		live[w.ID] = true
	}
	bs, err := r.deps.Store.ListAllCmuxBindings()
	if err != nil { // the off-topic rows wait for the next pass too: one listing missed, never a wrong drop
		r.deps.Logf("cmuxsync: list links: %v", err)
		return
	}
	changed := false
	for _, b := range bs {
		switch {
		case live[b.CmuxWorkspaceID] && b.MissingTicks > 0:
			_ = r.deps.Store.SetCmuxMissingTicks(b.CmuxWorkspaceID, 0)
		case !live[b.CmuxWorkspaceID] && b.MissingTicks+1 >= 2:
			if err := r.deps.Store.ForgetCmuxWorkspace(b.CmuxWorkspaceID); err == nil {
				r.note(b.SubjectID, "workspace cmux "+b.CmuxWorkspaceID+" fermé : lien retiré")
				delete(r.applied, b.CmuxWorkspaceID)
				changed = true
			}
		case !live[b.CmuxWorkspaceID]:
			_ = r.deps.Store.SetCmuxMissingTicks(b.CmuxWorkspaceID, b.MissingTicks+1)
		}
	}
	for _, o := range r.listOffTopic() {
		switch {
		case live[o.CmuxWorkspaceID] && o.MissingTicks > 0:
			_ = r.deps.Store.SetOffTopicMissingTicks(o.CmuxWorkspaceID, 0)
		case !live[o.CmuxWorkspaceID] && o.MissingTicks+1 >= 2:
			if err := r.deps.Store.DeleteOffTopic(o.CmuxWorkspaceID); err == nil {
				r.deps.Logf("cmuxsync: workspace %s closed: off-topic declaration dropped", o.CmuxWorkspaceID)
				changed = true
			}
		case !live[o.CmuxWorkspaceID]:
			_ = r.deps.Store.SetOffTopicMissingTicks(o.CmuxWorkspaceID, o.MissingTicks+1)
		}
	}
	r.closeMissingStandby(live)
	r.closeMissingPause(live)
	if changed && r.deps.MarkersChanged != nil {
		r.deps.MarkersChanged()
	}
}

// closeMissingStandby is the same proof for a Standby row: its workspace
// absent from two successful listings in a row (missedTwice, the count kept in
// the row) ends it through Deps.StandbyGone. A row without a workspace is no
// proof; a finished session whose workspace is open keeps its row (D27); an
// unreadable table purges nothing.
func (r *Runner) closeMissingStandby(live map[string]bool) {
	if r.deps.StandbyGone == nil {
		return
	}
	rows, err := r.deps.Store.ListStandbyWorkspaces()
	if err != nil {
		r.deps.Logf("cmuxsync: list standby: %v", err)
		return
	}
	for _, sb := range rows {
		if sb.CmuxWorkspaceID == "" {
			continue
		}
		next, forget := missedTwice(live[sb.CmuxWorkspaceID], sb.MissingTicks)
		switch {
		case forget:
			if err := r.deps.StandbyGone(sb.SessionID, sb.CmuxWorkspaceID); err != nil {
				r.deps.Logf("cmuxsync: standby of %s (workspace %s closed): %v", sb.SessionID, sb.CmuxWorkspaceID, err)
			}
		case next != sb.MissingTicks:
			_ = r.deps.Store.SetStandbyMissingTicks(sb.SessionID, next)
		}
	}
}

// listOffTopic reads every off-topic declaration; a failure reads as none and is logged once per distinct error.
func (r *Runner) listOffTopic() []pigtail.WorkspaceOffTopic {
	ots, err := r.deps.Store.ListOffTopic()
	if err != nil {
		if msg := err.Error(); msg != r.lastOffTopicErr {
			r.lastOffTopicErr = msg
			r.deps.Logf("cmuxsync: list off-topic: %v", err)
		}
		return nil
	}
	r.lastOffTopicErr = ""
	return ots
}

// reconcile seeds the applied pills from what cmux really shows (spec §4.4):
// a stale pigtail pill persisted by cmux is then cleared by the next Diff.
func (r *Runner) reconcile(now time.Time) error {
	applied := make(map[string]Applied, len(r.workspaces))
	seen := make(map[string]uint64, len(r.workspaces))
	type goneWS struct {
		id  string
		err error
	}
	var gone []goneWS
	for _, w := range r.workspaces {
		r.pillCheckpoint()
		seen[w.ID] = r.statusWrites[w.ID]
		st, err := r.deps.Sidebar.ListStatus(w.ID)
		if errors.Is(err, cmux.ErrWorkspaceNotFound) {
			// Closed since the listing: skip it, not a cmux failure; no seed to keep.
			delete(seen, w.ID)
			gone = append(gone, goneWS{w.ID, err})
			continue
		}
		if err != nil {
			return err
		}
		applied[w.ID] = SeedApplied(st, nil)
	}
	for id, n := range seen {
		if r.statusWrites[id] != n {
			// A checkpoint wrote this workspace's pills after its list-status
			// was read: the seed predates cmux, the applied state that write
			// recorded is the truth (else Diff would never clear that key).
			applied[id] = r.applied[id]
		}
	}
	for _, g := range gone { // after the loop: it iterates r.workspaces
		r.workspaceGone(g.id, "list-status", g.err)
	}
	r.applied, r.lastReconcile, r.needReconcile = applied, now, false
	r.mu.Lock()
	r.reconcAt = now
	r.mu.Unlock()
	return nil
}

// sync is the steady-state pass: compute, damp, diff, apply, publish.
//
// pillsOnly writes statuses only (the pill checkpoints, checkpoint.go).
func (r *Runner) sync(now time.Time, pillsOnly bool) (int, error) {
	r.lastSync = now
	bs, err := r.deps.Store.ListAllCmuxBindings()
	if err != nil {
		return 0, fmt.Errorf("list links: %w", err)
	}
	byWS := map[string]pigtail.SubjectCmuxBinding{}
	var bound []pigtail.SubjectCmuxBinding
	subjects := map[string]SubjectView{}
	themeNames := map[string]string{} // one read per theme per pass: a rename shows next pass
	themeName := func(id string) string {
		if n, ok := themeNames[id]; ok {
			return n
		}
		n := ""
		if th, err := r.deps.Store.GetTheme(id); err == nil {
			n = th.Name
		}
		themeNames[id] = n // "" on error: Description falls back to the scope
		return n
	}
	for _, b := range bs {
		if !r.allowed[b.CmuxWorkspaceID] {
			continue
		}
		byWS[b.CmuxWorkspaceID] = b
		bound = append(bound, b)
		if _, ok := subjects[b.SubjectID]; ok {
			continue
		}
		sub, err := r.deps.Store.GetSubject(b.SubjectID)
		if err != nil {
			continue // unknown subject: its workspace's text is left alone
		}
		phases, _ := r.deps.Store.ListPhasesBySubject(b.SubjectID)
		subjects[b.SubjectID] = SubjectView{Subject: *sub, Phases: phases, ThemeName: themeName(sub.ThemeID)}
	}

	ss := r.waitingSessions(now, pillsOnly)
	var known map[string]bool
	if r.deps.KnownCells != nil {
		known = r.deps.KnownCells()
	}
	r.knownCells = known
	offTopic := map[string]bool{}
	for _, o := range r.listOffTopic() {
		if r.allowed[o.CmuxWorkspaceID] {
			offTopic[o.CmuxWorkspaceID] = true
		}
	}
	// Transcript reads belong to the full sync, once per pass; a pills-only
	// sync (checkpoint) reuses the last verdict.
	prompted := r.lastPrompted
	if !pillsOnly {
		prompted = r.respawnPrompted(ss)
		r.lastPrompted = prompted
	}
	res := Compute(Input{
		CellText:      r.cellText,
		KnownCells:    known,
		Frozen:        r.frozen(),
		Now:           r.listedAt,
		Workspaces:    r.workspaces,
		Sessions:      ss,
		Bindings:      bound,
		Subjects:      subjects,
		LastTextWrite: r.lastTextWrite,
		ListStartedAt: r.listStartedAt,
		Servers:       r.serverView,
		TrustPending:  r.trustPendingSet(ss),
		Respawn:       r.respawnSet(ss, prompted),
		Maintenance:   r.maintenancePill(now),
		CheckDefects:  r.freshCheckDefects(r.cmuxCheck(now), ss),
		DeadInputs:    r.deadInputs(),
		OffTopic:      offTopic,
		Paused:        r.pausedSet(),
		RoleAlerts:    r.roleAlertSet(),
	})
	r.damper.Apply(now, res.Desired)
	r.lastDesired = res.Desired

	textOK := !r.listDown && !pillsOnly // a stale listing cannot tell a manual rename (D1·A)
	if textOK {
		for _, t := range res.Takeovers { // descriptions only since D5
			r.takeover(t)
			b := byWS[t.WorkspaceID]
			b.DescriptionOwner = pigtail.CmuxOwnerUser
			byWS[t.WorkspaceID] = b
		}
	}

	wsByID := make(map[string]Workspace, len(r.workspaces))
	for _, w := range r.workspaces {
		wsByID[w.ID] = w
	}

	// The text pigtail posed lives in the link (it survives a restart and a
	// retitle resets it); only the pills are remembered in memory. An
	// unbound workspace's cell text is in memory, or recognised as one.
	for id := range res.Desired {
		a := r.applied[id]
		b, ok := byWS[id]
		a.Title, a.Description = "", ""
		if ok {
			a.Title, a.Description = b.AppliedTitle, b.AppliedDescription
		} else if ct := r.cellText[id]; ct != "" {
			a.Description = ct
			cur := strings.TrimSpace(wsByID[id].Description)
			fresh := r.listStartedAt.Before(r.lastTextWrite[id].Add(TakeoverReadGuard)) // cmux reads lag: a listing begun before the write ended + guard may predate it
			if textOK && !fresh && cur != "" && cur != ct && !isCellText(cur, known) {
				// Written by hand over pigtail's cell text: it is the
				// user's now, never cleared on switch-off.
				delete(r.cellText, id)
				a.Description = ""
			}
		} else if cur := strings.TrimSpace(wsByID[id].Description); isCellText(cur, known) {
			a.Description = cur
		}
		r.applied[id] = a
	}
	if textOK {
		r.pruneReverts()
		for _, rv := range res.Reverts {
			if !r.revertTitle(now, rv) {
				continue
			}
			// The link keeps applied_title: Diff re-poses Desired.Title, and
			// apply does not capture a new prev_title (the name from before
			// the link stays the one a detach gives back).
			a := r.applied[rv.WorkspaceID]
			a.Title = ""
			r.applied[rv.WorkspaceID] = a
		}
	}
	n := 0
	for _, op := range Diff(res.Desired, r.applied) {
		if !textOK && op.Kind != OpSetStatus && op.Kind != OpClearStatus {
			continue // applied on the first pass with a fresh listing
		}
		if n > 0 {
			r.deps.Sleep(r.cfg.OpInterval)
		}
		sent, err := r.apply(now, op, byWS, wsByID)
		if err != nil {
			return n, err
		}
		if sent {
			n++
		}
	}
	r.publishChanges(res.Desired, byWS, subjects)
	r.natives.sweep()
	return n, nil
}

// sessions joins the registry with the native files. An old row whose pane
// is long gone is left to Compute: it lights nothing once its surface is
// missing from the workspace's open surfaces.
// instanceKey names one run of a session: its id, its Claude pid and the
// startedAt of its native file (ms; --resume writes a new one). D24.
func instanceKey(sessionID string, pid int, startedAt int64) string {
	return sessionID + "|" + strconv.Itoa(pid) + "|" + strconv.FormatInt(startedAt, 10)
}

func (r *Runner) sessions() []Session {
	ps, err := r.deps.Store.ListPeers(true)
	if err != nil {
		r.deps.Logf("cmuxsync: list sessions: %v", err)
		return nil
	}
	waits, err := r.deps.Store.ListSessionWaits()
	if err != nil {
		r.deps.Logf("cmuxsync: list session waits: %v", err)
	}
	standby := r.standbySessions()
	if r.busySeen == nil {
		r.busySeen = map[string]bool{}
	}
	seen := map[string]bool{}
	defer func() {
		for k := range r.busySeen {
			if !seen[k] {
				delete(r.busySeen, k) // the instance is gone, resumed or out of the windows
			}
		}
	}()
	var out []Session
	for _, p := range ps {
		ws := strings.ToUpper(p.CmuxWorkspaceID)
		if !r.allowed[ws] {
			continue
		}
		s := Session{
			SessionID:   p.SessionID,
			WorkspaceID: ws,
			SurfaceID:   strings.ToUpper(p.CmuxSurfaceID),
			PID:         p.PID,
			Ended:       p.EndedAt != nil,
			Status:      turnStatus(p.Status),
			UpdatedAt:   p.UpdatedAt,
			Scope:       p.Scope,
			CellID:      p.CellID,
			ConfigDir:   p.ConfigDir,
			Standby:     standby[p.SessionID],
		}
		for _, w := range waits[p.SessionID] {
			if !placeWait(&s, w) {
				r.deps.Logf("cmuxsync: wait of %s: unknown source %q, ignored", p.SessionID, w.Source)
			}
		}
		n, native := r.natives.get(p.ConfigDir, p.PID)
		native = native && n.SessionID == p.SessionID
		if native {
			s.Status, s.Kind, s.WaitingFor = turnStatus(n.Status), n.Kind, n.WaitingFor
			if n.StatusUpdatedAt > 0 {
				s.UpdatedAt = time.UnixMilli(n.StatusUpdatedAt)
				s.NativeStatusAt = s.UpdatedAt
			}
			if n.StartedAt > 0 {
				s.StartedAt = time.UnixMilli(n.StartedAt)
			}
			key := instanceKey(p.SessionID, p.PID, n.StartedAt)
			seen[key] = true
			if s.Status == "busy" {
				r.busySeen[key] = true
			}
			s.BusySeen = r.busySeen[key]
		}
		s.Alive = r.sessionAlive(p)
		out = append(out, s)
	}
	return out
}

// standbySessions is the session ids in Standby (Deps.Standby), for
// Session.Standby. nil Deps.Standby = none; a read error is logged and reads
// none, so the sidebar keeps its Your turn rather than hide it on a guess.
func (r *Runner) standbySessions() map[string]bool {
	if r.deps.Standby == nil {
		return nil
	}
	sb, err := r.deps.Standby()
	if err != nil {
		r.deps.Logf("cmuxsync: list standby: %v", err)
		return nil
	}
	return sb
}

// sessionAlive is the loop's one judgment of a registered session's life
// (Session.Alive, the Standby filing rule): a finished row is not alive;
// else Deps.Alive decides; a row without pid or config dir (registered by a
// hook, pid not known yet) is not dead; else its pid runs and its native
// file (read through the cache) still names it.
func (r *Runner) sessionAlive(p pigtail.PeerSession) bool {
	switch {
	case p.EndedAt != nil:
		return false
	case r.deps.Alive != nil:
		return r.deps.Alive(p)
	case p.PID <= 0 || p.ConfigDir == "":
		return true
	}
	n, ok := r.natives.get(p.ConfigDir, p.PID)
	return ok && n.SessionID == p.SessionID && claudesession.PidAlive(p.PID)
}

// placeWait puts a recorded wait in its session's slot for its source (F4:
// an explicit switch, so a new source is never read as another). False: a
// source the loop does not know, placed nowhere.
func placeWait(s *Session, w pigtail.SessionWait) bool {
	v := Wait{Type: waittype.Type(w.Type), Reason: w.Reason, Since: w.Since}
	if w.Until != nil {
		v.Until = *w.Until
	}
	switch w.Source {
	case pigtail.WaitSourceAuto:
		s.Auto = v
	case pigtail.WaitSourceDeclared:
		s.Declared = v
	case pigtail.WaitSourceHeld:
		v.HolderPID = w.HolderPID
		s.Held = v
	default:
		return false
	}
	return true
}

// waitingSessions is sessions() with the waits that are over dropped
// (WaitTracker), from the store too. Once per pass: the tracker counts
// observations.
//
// A declared wait is confirmed by the transcript (turnRan); a pills-only sync
// reads none and answers from the last full verdict.
func (r *Runner) waitingSessions(now time.Time, pillsOnly bool) []Session {
	ss := r.sessions()
	r.waits.pidAlive = r.deps.PidAlive
	r.waits.turnRan = r.turnRanFull
	if pillsOnly {
		r.waits.turnRan = r.turnRanCached
	}
	if !pillsOnly {
		r.primeTurnCursors(ss)
	}
	for i := range ss {
		r.endWaits(now, &ss[i])
	}
	r.waits.Sweep(nil)
	r.sweepTurnCursors(ss)
	return ss
}

// clearStore is the store's clear of a session's waits: a gone session's
// keeps the wait its subject owns (D29-A).
func clearStore(st *store.Store, session string, sources []string, gone bool) error {
	var err error
	if gone {
		_, err = st.ClearGoneSessionWaits(session, sources...)
	} else {
		_, err = st.ClearSessionWaits(session, sources...)
	}
	return err
}

// endWaits drops the waits of s the tracker judges over (spec §4.8). A held
// wait goes by its holder (ClearHeldWait), never by source: between the read
// and the delete, a new Mac-hold helper may have posed its own.
func (r *Runner) endWaits(now time.Time, s *Session) {
	done := r.waits.Observe(now, *s)
	if len(done) == 0 {
		return
	}
	var rest []string
	for _, src := range done {
		if src == pigtail.WaitSourceHeld {
			r.endHeldWait(s)
			continue
		}
		rest = append(rest, src)
	}
	if len(rest) == 0 {
		return
	}
	// Observe returns declared or auto sources of a session not alive only
	// in its Ended and gone-confirmed branches.
	gone := s.Ended || !s.Alive
	if r.deps.ClearWaits != nil {
		r.deps.ClearWaits(s.SessionID, rest, gone)
	} else if err := clearStore(r.deps.Store, s.SessionID, rest, gone); err != nil {
		r.deps.Logf("cmuxsync: clear waits of %s: %v", s.SessionID, err)
		return
	}
	for _, src := range rest {
		switch src {
		case pigtail.WaitSourceAuto:
			s.Auto = Wait{}
		case pigtail.WaitSourceDeclared:
			s.Declared = Wait{}
		}
	}
}

// endHeldWait drops s's held wait while its observed holder still holds it,
// and empties s.Held: a newer holder's wait shows from the next read.
func (r *Runner) endHeldWait(s *Session) {
	drop := r.deps.ClearHeldWait
	if drop == nil {
		drop = r.deps.Store.ClearHeldWait
	}
	if _, err := drop(s.SessionID, s.Held.HolderPID); err != nil {
		r.deps.Logf("cmuxsync: clear held wait of %s: %v", s.SessionID, err)
		return
	}
	s.Held = Wait{}
}

// apply performs one op. Before pigtail's first write on a field, what the
// field showed is saved in the link, so RestoreOps gives back the right
// title on detach or switch-off (PR 3 hardening). An argument cmux would
// misread is a bug upstream: logged and skipped, never retried in a loop.
// It reports whether cmux received the op: an op cmux never received (not
// allowed, no way back, skipped) proves nothing about cmux.
func (r *Runner) apply(now time.Time, op Op, byWS map[string]pigtail.SubjectCmuxBinding, wsByID map[string]Workspace) (bool, error) {
	if !r.allowed[op.WorkspaceID] {
		return false, nil
	}
	b, bound := byWS[op.WorkspaceID]
	w := wsByID[op.WorkspaceID]
	switch {
	case op.Kind == OpRename && bound && b.AppliedTitle == "":
		prev := w.Title
		if w.HasCustomTitle {
			prev = w.CustomTitle
		}
		if err := r.deps.Store.CaptureCmuxPrev(op.WorkspaceID, store.CmuxFieldTitle, prev, w.HasCustomTitle); err != nil {
			r.deps.Logf("cmuxsync: save previous title of %s: %v", op.WorkspaceID, err)
			return false, nil // never rename without a way back
		}
	case op.Kind == OpSetDescription && bound && b.AppliedDescription == "":
		prev := w.Description
		if r.isOwnCellText(op.WorkspaceID, prev) {
			prev = "" // pigtail's own cell text is nothing to give back
		}
		if err := r.deps.Store.CaptureCmuxPrev(op.WorkspaceID, store.CmuxFieldDescription, prev, false); err != nil {
			r.deps.Logf("cmuxsync: save previous description of %s: %v", op.WorkspaceID, err)
			return false, nil
		}
	}
	if err := op.Apply(r.deps.Sidebar); err != nil {
		if errors.Is(err, cmux.ErrInvalidArg) || errors.Is(err, ErrNotOwned) {
			r.deps.Logf("cmuxsync: %s on %s skipped: %v", op.Kind, op.WorkspaceID, err)
			r.countError(now)
			return false, nil
		}
		if errors.Is(err, cmux.ErrWorkspaceNotFound) {
			r.workspaceGone(op.WorkspaceID, string(op.Kind), err)
			return false, nil
		}
		return false, fmt.Errorf("%w: %s %s: %w", errUnavailable, op.Kind, op.WorkspaceID, err)
	}
	Record(r.applied, op)
	if op.Kind == OpSetStatus || op.Kind == OpClearStatus {
		r.statusWrites[op.WorkspaceID]++
	}
	switch op.Kind {
	case OpRename:
		r.lastTextWrite[op.WorkspaceID] = r.deps.Now() // when the write returned, not when the pass began
		_ = r.deps.Store.SetCmuxApplied(op.WorkspaceID, store.CmuxFieldTitle, op.Text)
	case OpSetDescription:
		r.lastTextWrite[op.WorkspaceID] = r.deps.Now() // when the write returned, not when the pass began
		if !bound {
			r.cellText[op.WorkspaceID] = op.Text // no link to hold it
			break
		}
		delete(r.cellText, op.WorkspaceID)
		_ = r.deps.Store.SetCmuxApplied(op.WorkspaceID, store.CmuxFieldDescription, op.Text)
	case OpClearDescription:
		if !bound {
			delete(r.cellText, op.WorkspaceID)
			r.lastTextWrite[op.WorkspaceID] = r.deps.Now()
		}
	}
	return true, nil
}

// frozen reads the sessions the current freeze refused; nil without the seam.
func (r *Runner) frozen() map[string]bool {
	if r.deps.FrozenSessions == nil {
		return nil
	}
	return r.deps.FrozenSessions()
}

// isOwnCellText: desc is the cell-only description pigtail posed on this
// workspace, from memory or recognised from the cell table.
func (r *Runner) isOwnCellText(id, desc string) bool {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return false
	}
	if desc == r.cellText[id] {
		return true
	}
	return isCellText(desc, r.knownCells)
}

func (r *Runner) countError(now time.Time) {
	r.mu.Lock()
	r.errs = append(pruneBefore(r.errs, now.Add(-time.Hour)), now)
	r.mu.Unlock()
}

// takeover records a description edited by hand: it belongs to the user
// from now on, noted once in the subject's log. The title is never taken
// over since D5: it is put back (revertTitle).
func (r *Runner) takeover(t Takeover) {
	if err := r.deps.Store.SetCmuxOwner(t.WorkspaceID, store.CmuxFieldDescription, pigtail.CmuxOwnerUser); err != nil {
		r.deps.Logf("cmuxsync: takeover of %s: %v", t.WorkspaceID, err)
		return
	}
	r.note(t.SubjectID, "description cmux reprise à la main : pigtail n'y touche plus (pigtail cmux retitle pour la rendre)")
}

// Breaker of the title revert (D5): beyond maxTitleReverts in
// titleRevertWindow on one workspace, something keeps undoing pigtail's
// rename; the loop stops putting it back until the short title changes or
// the daemon restarts.
const (
	maxTitleReverts   = 3
	titleRevertWindow = 10 * time.Minute
)

type revertState struct {
	at      []time.Time
	noted   string // the manual title last noted
	notedAt time.Time
	tripped string // the short title the breaker tripped on; "" = armed
}

// revertTitle decides whether the title of rv's workspace is put back, and
// notes it once per manual title (D5). False once the breaker tripped.
func (r *Runner) revertTitle(now time.Time, rv TitleRevert) bool {
	s := r.reverts[rv.WorkspaceID]
	if s == nil {
		s = &revertState{}
		r.reverts[rv.WorkspaceID] = s
	}
	if s.tripped != "" {
		if s.tripped == rv.Short {
			return false
		}
		*s = revertState{} // a new short title re-arms the breaker
	}
	s.at = pruneBefore(s.at, now.Add(-titleRevertWindow))
	if len(s.at) >= maxTitleReverts {
		s.tripped = rv.Short
		r.deps.Logf("cmuxsync: title of %s undone %d times in %s: no longer put back", rv.WorkspaceID, len(s.at), titleRevertWindow)
		r.note(rv.SubjectID, breakerNote(rv))
		return false
	}
	s.at = append(s.at, now)
	if rv.Manual != s.noted || now.Sub(s.notedAt) >= titleRevertWindow {
		r.note(rv.SubjectID, revertNote(rv))
		s.noted, s.notedAt = rv.Manual, now
	}
	return true
}

// pruneReverts forgets the workspaces cmux no longer lists.
func (r *Runner) pruneReverts() {
	live := make(map[string]bool, len(r.workspaces))
	for _, w := range r.workspaces {
		live[w.ID] = true
	}
	for id := range r.reverts {
		if !live[id] {
			delete(r.reverts, id)
		}
	}
}

// revertNote is the subject's log line when its tab's title is put back (D5,
// G1; G2 = mini-D6 A for a cleared name).
func revertNote(rv TitleRevert) string {
	why := "renommé à la main en « " + rv.Manual + " »"
	if rv.Cleared {
		why = "nom effacé à la main"
	}
	return "titre de l'onglet remis à « " + rv.Short + " » (" + why + ") — pour renommer, change le titre court"
}

// breakerNote is the subject's log line when the breaker trips (G3 = mini-D7 A).
func breakerNote(rv TitleRevert) string {
	return "titre de l'onglet laissé à « " + rv.Manual + " » : défait " + strconv.Itoa(maxTitleReverts) +
		" fois en 10 min, pigtail cesse de le remettre jusqu'au prochain changement de titre court"
}

func (r *Runner) note(subjectID, text string) {
	e := &pigtail.SubjectLogEntry{
		ID: pigtail.PrefixLog + randHex(), SubjectID: subjectID, Timestamp: r.deps.Now().UTC(), Kind: "note", Text: text,
	}
	if err := r.deps.Store.AppendLog(e); err != nil {
		r.deps.Logf("cmuxsync: log on %s: %v", subjectID, err)
	}
}

// workspaceGone drops a workspace cmux answered not_found on from the last
// listing's cache, with no cmux call: later ops and reconciles of the pass skip
// it, and the next listing rebuilds the cache. r.workspaces is replaced by a
// copy, never edited in place (sync and reconcile iterate it). A closed tab is
// not a cmux failure: no countError, no needReconcile.
func (r *Runner) workspaceGone(id, what string, err error) {
	delete(r.allowed, id)
	r.workspaces = slices.DeleteFunc(slices.Clone(r.workspaces), func(w Workspace) bool { return w.ID == id })
	r.deps.Logf("cmuxsync: %s on %s skipped, workspace gone: %v", what, id, err)
}

// restore gives a detached workspace its title and description back.
func (r *Runner) restore(now time.Time, b pigtail.SubjectCmuxBinding) error {
	if !r.allowed[b.CmuxWorkspaceID] {
		return nil // closed since, or outside the windows this loop may write
	}
	for _, op := range RestoreOps(b) {
		if err := op.Apply(r.deps.Sidebar); err != nil {
			if errors.Is(err, cmux.ErrInvalidArg) {
				continue
			}
			if errors.Is(err, cmux.ErrWorkspaceNotFound) {
				r.workspaceGone(b.CmuxWorkspaceID, "restore "+string(op.Kind), err)
				return nil // the next op of this restore aims at the same closed tab
			}
			return fmt.Errorf("%w: restore %s: %w", errUnavailable, b.CmuxWorkspaceID, err)
		}
		r.lastTextWrite[b.CmuxWorkspaceID] = r.deps.Now()
		r.deps.Sleep(r.cfg.OpInterval)
	}
	return nil
}

// deactivate is the switch-off: clear every owned pill, give back every
// title and description pigtail still owns, forget the engine state. Best
// effort: an unreachable cmux keeps what it shows, and the link keeps the
// memory needed to give it back later.
func (r *Runner) deactivate(now time.Time) {
	r.active = false
	r.pinned.Store(nil)
	r.mu.Lock()
	r.foldWins, r.foldWinsAt = nil, time.Time{}
	r.foldersWait = nil
	r.waitLog = nil
	r.trust = nil   // switched on again, no pill without a new registration
	r.respawn = nil // nor a respawn's without a new mark
	r.mu.Unlock()
	for _, op := range ClearOwnedOps(r.applied) {
		if !r.allowed[op.WorkspaceID] {
			continue
		}
		if err := op.Apply(r.deps.Sidebar); err != nil {
			r.deps.Logf("cmuxsync: switch-off: %s on %s: %v", op.Kind, op.WorkspaceID, err)
		}
	}
	if bs, err := r.deps.Store.ListAllCmuxBindings(); err == nil {
		for _, b := range bs {
			ops := RestoreOps(b)
			if len(ops) == 0 || !r.allowed[b.CmuxWorkspaceID] {
				continue
			}
			ok := true
			for _, op := range ops {
				if err := op.Apply(r.deps.Sidebar); err != nil {
					r.deps.Logf("cmuxsync: switch-off: %s on %s: %v", op.Kind, op.WorkspaceID, err)
					ok = false
				}
			}
			if ok {
				_ = r.deps.Store.ReleaseCmuxText(b.CmuxWorkspaceID)
			}
		}
	}
	for id := range r.cellText {
		if !r.allowed[id] {
			continue
		}
		op := Op{Kind: OpClearDescription, WorkspaceID: id}
		if err := op.Apply(r.deps.Sidebar); err != nil {
			r.deps.Logf("cmuxsync: switch-off: %s on %s: %v", op.Kind, op.WorkspaceID, err)
		}
	}
	r.cellText = map[string]string{}
	r.applied = map[string]Applied{}
	r.damper = Damper{}
	r.droppedLogged = nil
	r.published = map[string]published{}
	r.lastTextWrite = map[string]time.Time{}
	r.reverts = map[string]*revertState{}
	r.surfaces, r.surfacesDown = map[string]surfaceSet{}, false
	r.servers, r.serverView, r.serversAt = ServerTracker{}, nil, time.Time{}
	r.waits, r.turns = WaitTracker{}, nil
	r.lastSlow, r.lastReconcile = time.Time{}, time.Time{}
	r.listBackoff, r.listRetryAt, r.peersDown = 0, time.Time{}, false
	r.mu.Lock()
	r.view = nil
	r.listDown, r.listErr, r.listDownSince = false, "", time.Time{}
	r.mu.Unlock()
	r.deps.Logf("cmuxsync: sidebar loop switched off at %s, pills cleared and titles given back", now.Format(time.RFC3339))
}

// shutdown clears the owned pills when the daemon stops, within
// ShutdownBudget: a slow cmux must not hold the daemon's exit.
func (r *Runner) shutdown() {
	r.answerQueuedToggle() // a request still queued is refused, not left waiting
	if !r.active {
		return
	}
	deadline := r.deps.Now().Add(r.cfg.ShutdownBudget)
	for _, op := range ClearOwnedOps(r.applied) {
		if r.deps.Now().After(deadline) {
			return
		}
		if r.allowed[op.WorkspaceID] {
			_ = op.Apply(r.deps.Sidebar)
		}
	}
}

func (r *Runner) onPanic(now time.Time, rec any) {
	r.deps.Logf("cmuxsync: panic in the sidebar loop: %v\n%s", rec, debug.Stack())
	r.publish(pigtail.Event{Type: pigtail.EventResourcesWarning, Data: map[string]interface{}{
		"source": "cmuxsync", "message": fmt.Sprintf("panic in the cmux sidebar loop: %v", rec),
	}})
	r.panics = append(pruneBefore(r.panics, now.Add(-r.cfg.PanicWindow)), now)
	r.pauseUntil = now.Add(r.cfg.PanicPause)
	r.countError(now)
	if len(r.panics) < r.cfg.PanicLimit {
		return
	}
	r.mu.Lock()
	r.want, r.panicked = false, true
	r.mu.Unlock()
	r.panics = nil
	r.deps.Logf("cmuxsync: %d panics within %s, sidebar loop switched off", r.cfg.PanicLimit, r.cfg.PanicWindow)
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.deps.Logf("cmuxsync: panic while switching off: %v", rec)
				r.active = false
			}
		}()
		if r.active {
			r.deactivate(now)
		}
	}()
}

func (r *Runner) publish(e pigtail.Event) {
	if r.deps.Publish != nil {
		r.deps.Publish(e)
	}
}

// publishChanges emits cmux.sidebar_changed for each workspace whose wanted
// state moved, and refreshes the snapshot GET /cmux/sidebar reads.
func (r *Runner) publishChanges(desired map[string]Desired, byWS map[string]pigtail.SubjectCmuxBinding, subjects map[string]SubjectView) {
	view := make([]pigtail.CmuxSidebarWorkspace, 0, len(r.workspaces))
	for _, w := range r.workspaces {
		d := desired[w.ID]
		v := pigtail.CmuxSidebarWorkspace{
			CmuxWorkspaceID: w.ID, WindowID: w.WindowID,
			Attention: string(d.Attention), Servers: wireServers(d.Servers),
			Title: shownTitle(w, d),
		}
		if d.Attention == AttentionWaiting {
			v.WaitingCount = d.WaitingCount
			v.WaitType, v.WaitReason, v.WaitScope, v.WaitFromSession = string(d.WaitType), d.WaitReason, d.WaitScope, d.WaitFromSession
		}
		for _, c := range d.Cells {
			v.Cells = append(v.Cells, c.ID)
			v.CellScopes = append(v.CellScopes, c.Scope)
		}
		subjectID, sc := "", ""
		if b, ok := byWS[w.ID]; ok {
			v.TitleOwner = b.TitleOwner
			if sv, ok := subjects[b.SubjectID]; ok {
				subjectID, sc = sv.Subject.ID, sv.Subject.Scope
				v.Subject = &pigtail.CmuxSidebarSubject{
					ID: sv.Subject.ID, ShortTitle: pigtail.ShortTitle(sv.Subject), Scope: sv.Subject.Scope,
					Status: sv.Subject.Status, Rank: sv.Subject.Rank,
				}
			}
		}
		view = append(view, v)

		cur := published{attention: v.Attention, count: v.WaitingCount, waitType: v.WaitType, reason: v.WaitReason,
			subject: subjectID, servers: serversKey(d.Servers)}
		if r.published[w.ID] == cur {
			continue
		}
		r.published[w.ID] = cur
		sc = sidebarEventScope(sc, v.WaitReason != "" && v.WaitFromSession, v.WaitScope)
		r.publish(pigtail.Event{Type: pigtail.EventCmuxSidebarChanged, Scope: sc, Data: map[string]interface{}{
			"cmux_workspace_id": w.ID, "attention": v.Attention, "wait_type": v.WaitType, "wait_reason": v.WaitReason,
			"waiting_count": v.WaitingCount, "servers": wireServers(d.Servers), "subject_id": subjectID,
		}})
	}
	for id := range r.published {
		if _, ok := desired[id]; !ok {
			delete(r.published, id)
		}
	}
	sort.Slice(view, func(i, j int) bool { return view[i].CmuxWorkspaceID < view[j].CmuxWorkspaceID })
	r.mu.Lock()
	r.view = view
	r.mu.Unlock()
}

// serversKey summarises a workspace's servers, to publish only on change.
func serversKey(ss []Server) string {
	e, _ := serversPill(ss)
	return e.Value + "|" + e.Color
}

func randHex() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// maintenancePill reads the maintenance for the pill. An unreadable state
// shows the error pill and logs once per distinct error — never a silent
// disappearance (D8, D9).
func (r *Runner) maintenancePill(now time.Time) *MaintenancePill {
	if r.deps.Maintenance == nil {
		return nil
	}
	st, ok, err := r.deps.Maintenance()
	if err != nil {
		if msg := err.Error(); msg != r.lastMaintErr {
			r.lastMaintErr = msg
			r.deps.Logf("cmuxsync: maintenance state unreadable (%s), error pill shown: %v", filepath.Join(r.deps.MaintenanceDir, maintenance.FileName), err)
		}
		v, c := maintenance.ErrorPill()
		return &MaintenancePill{Value: v, Color: c}
	}
	r.lastMaintErr = ""
	if !ok {
		return nil
	}
	if v, c, on := maintenance.PillValue(st, now); on {
		p := &MaintenancePill{Value: v, Color: c}
		if ov, _, has := maintenance.OwnerPillValue(st, now, pillMaxRunes); has {
			p.Owner, p.OwnerValue = st.Owner, ov
		}
		return p
	}
	return nil
}

// sidebarEventScope tags a sidebar event: the bound subject's scope, else the
// default scope (an unbound workspace is shown as the default scope). A reason
// told by a session narrows it to the master when that session runs on
// another scope than the tag — or on no scope at all (D17: seen by the master
// only). sessionReason, not waitScope == "", says who told the reason: false
// is no reason or pigtail's own (trust folder, respawn), which keeps the
// workspace's tag as before; true with waitScope "" is a session without scope.
func sidebarEventScope(subjectScope string, sessionReason bool, waitScope string) string {
	sc := subjectScope
	if sc == "" {
		sc = string(scope.Default())
	}
	if !sessionReason {
		return sc
	}
	if strings.TrimSpace(waitScope) == "" || string(scope.Canonical(waitScope)) != sc {
		return string(scope.Master())
	}
	return sc
}

// cmuxCheck returns the last check's defects, re-read at most once per
// SlowInterval: a hand-run `--fix` shows within 30 s. An unreadable report
// shows no pill and is logged once per distinct error.
func (r *Runner) cmuxCheck(now time.Time) []CheckDefect {
	if r.deps.CmuxCheck == nil {
		return nil
	}
	if !r.checkAt.IsZero() && now.Sub(r.checkAt) < r.cfg.SlowInterval {
		return r.checkDefects
	}
	r.checkAt = now
	ds, err := r.deps.CmuxCheck()
	if err != nil {
		if msg := err.Error(); msg != r.lastCheckErr {
			r.lastCheckErr = msg
			r.deps.Logf("cmuxsync: cmux check report unreadable, no check pill: %v", err)
		}
		r.checkDefects = nil
		return nil
	}
	r.lastCheckErr, r.checkDefects = "", ds
	return ds
}

// deadInputs reads the daemon's dead-input surfaces (none without the seam).
func (r *Runner) deadInputs() []string {
	if r.deps.DeadInputs == nil {
		return nil
	}
	return r.deps.DeadInputs()
}
