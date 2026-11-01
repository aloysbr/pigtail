package cmuxcheck

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/inputprobe"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	MarkerEnv    = "PIGTAIL_CMUX_RELAUNCH"
	ContinueText = "cmux a redémarré pendant ton tour — reprends là où tu en étais." // D2, verbatim

	ResumeWindow = 10 * time.Minute // cmux resumes its sessions within this after its launch
	CutWindow    = time.Hour        // a turn cut by the relaunch ended at most this before the quit
	Settle       = 2 * time.Second  // status changed this recently: still moving (respawn's rwSettle)
	QuietFor     = time.Minute      // transcript written this recently: still working
)

// HumanName is how the pilot messages name the human (settings.yaml
// human.name). The daemon and the CLI set it at start; empty reads as "the user".
var HumanName = "the user"

func humanName() string {
	if HumanName == "" {
		return "the user"
	}
	return HumanName
}

// PilotText is what the check types to the pilot (D5, D10); PilotTextCut
// replaces the continue when its turn was cut. Verbatim, French like
// ContinueText; no backslash (cmux send reads escape sequences).
func PilotText() string {
	return "cmux a redémarré — lance `pigtail cmux check --last` et présente à " + humanName() + " ce qui l'attend."
}

func PilotTextCut() string {
	return "cmux a redémarré pendant ton tour — lance `pigtail cmux check --last`, présente à " + humanName() + " ce qui l'attend, puis reprends là où tu en étais."
}

type Action string

const (
	ActClearRestore Action = "clear-restore-line"
	ActRelaunch     Action = "relaunch"
	ActStart        Action = "start"
	ActContinue     Action = "continue"
	ActQuit         Action = "quit-unread" // D1, D2: Ctrl+C then the start line
	// ActResume (sub_x000000000000024): a dead input, resumed in a new tab
	// of its workspace on --fix --surface; the old tab closes once the new
	// claude is seen.
	ActResume Action = "resume-new-tab"
)

// Options drive Run (Task 6); Classify reads Surfaces and Self.
type Options struct {
	// CmuxResumeOff (D1, sub_x000000000000037): cmux's resume setting is off —
	// cmux resumed nothing, pigtail starts the sessions. No claude is
	// "resumed by cmux": the frozen-width inference is off, and a claude found
	// in the resume window was started by hand (taken as wanted). Zero value =
	// today's behaviour.
	CmuxResumeOff bool

	Fix      bool
	Surfaces []string // only these surface ids; empty = all
	Self     string   // CMUX_SURFACE_ID of the caller: never touched
	Before   *Photo   // lot 2: the photo `cmux restart` took before quitting
	Dir      string   // cmux Application Support dir
	Sentinel string
	Events   []string
	Peers    func() ([]pigtail.PeerSession, error)
	// PeersScope is the only scope Peers can see, "" when it sees every
	// scope (D7): a non-master cell reads its own scope's peers only, and a
	// photo rebuilt from them says so (Run, peersScopeNote) — never silently.
	PeersScope string
	Ping       func() error
	Quit       QuitPolicy // D1, T-a: "" = rescue
	Heartbeat  func()     // T-f: refreshes the running file before each row's repairs; nil outside --fix
	// Pilot (D3, D4): the pilot surface, set by Run (resolvePilot); never
	// "named" for Ctrl+C under the rescue policy (D8). PilotOnly: Fix touches
	// its row alone (--fix-pilot), the others are reported.
	Pilot            string
	PilotOnly        bool
	MaintenanceOwner string       // D4 second source: the active maintenance's owner session
	Marker           *PilotMarker // D4: the one-shot marker, loaded by the command; nil = none
	// Unattended (D8): a pass started by pigtaild — its --surface (the second
	// pass, D6) names nothing for Ctrl+C.
	Unattended bool
	// PilotMsg (T-i, revue PR B I2): pilot-msg.json, loaded by the command —
	// the launch whose pilot already got the message, typed once per launch.
	// nil = none.
	PilotMsg *PilotMsgState
	// InStandby (D16, sub_x000000000000041): is the session parked by
	// /standby? The command reads the hooks' marker file (no route, like
	// Running). An error counts as in Standby. nil = none is.
	InStandby func(sessionID string) (bool, error)
	// Pick (ProbeTargets only): narrows the candidates to the ones to probe
	// now (the tracker's due filter and cap), before TTYOf runs on them. Nil
	// keeps every candidate.
	Pick func([]inputprobe.Target) []inputprobe.Target
	// Probe (sub_x000000000000024): run the input probe (Deps.ProbeInput,
	// a terminal status query on the session's own tty, never a keystroke) on every idle session; off for an
	// unattended pass (pigtaild's own loop probes).
	Probe bool
}

// standby reads Options.InStandby for one session: an error is in Standby,
// with its reason.
func (o Options) standby(sid string) (bool, string) {
	if o.InStandby == nil || sid == "" {
		return false, ""
	}
	in, err := o.InStandby(sid)
	if err != nil {
		return true, "standby marker unreadable: " + err.Error()
	}
	return in, ""
}

// QuitPolicy: when an idle claude whose screen is unreadable may be quit by
// Ctrl+C (D1, T-a). Rescue, the default until the first acceptance, only
// when a human named that surface (--surface) — never the daemon's pilot
// before the acceptance (D8); unread in any --fix. The D2 rescue after a
// failed read-back runs under both, on a frozen row only.
type QuitPolicy string

const (
	QuitRescue QuitPolicy = "rescue"
	QuitUnread QuitPolicy = "unread"
)

func ParseQuitPolicy(s string) (QuitPolicy, error) {
	switch QuitPolicy(s) {
	case QuitRescue, QuitUnread:
		return QuitPolicy(s), nil
	}
	return "", fmt.Errorf("--ctrl-c: %q is not rescue or unread", s)
}

// named: the surface was named explicitly by a human — by --surface (any
// case) on a pass pigtaild did not start (D8). The pilot is never named: under
// rescue, a pilot whose screen is unreadable stays LEFT with its command.
func (o Options) named(surface string) bool {
	return !o.Unattended && len(o.Surfaces) > 0 && o.wants(surface)
}

func (o Options) wants(surface string) bool {
	if len(o.Surfaces) == 0 {
		return true
	}
	for _, s := range o.Surfaces {
		if sameSurface(s, surface) {
			return true
		}
	}
	return false
}

// sameSurface: a surface id read without case — cmux prints it upper case,
// a hand-typed --surface may not; last.json's merge reads it the same way.
func sameSurface(a, b string) bool { return surfaceKey(a) == surfaceKey(b) }

func surfaceKey(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// unknownSurfaces: the --surface ids found in neither photo — a usage
// error, never an empty pass that reads "all fine".
func unknownSurfaces(o Options, photos ...Photo) []string {
	known := map[string]bool{}
	for _, ph := range photos {
		for _, p := range ph.Panels {
			known[surfaceKey(p.SurfaceID)] = true
		}
	}
	var out []string
	for _, s := range o.Surfaces {
		if !known[surfaceKey(s)] {
			out = append(out, s)
		}
	}
	return out
}

// Row is one claude binding of the photo before, and what to do about it.
type Row struct {
	SurfaceID string `json:"surface_id"`
	Title     string `json:"title"` // the pane's own title (its session's)
	// WorkspaceTitle: the pane's workspace's custom title, a bound subject's
	// title of a scope nothing proves (D23) — printed to the master only
	// (Report.Redacted).
	WorkspaceTitle string   `json:"workspace_title,omitempty"`
	SessionID      string   `json:"session_id"`
	ConfigDir      string   `json:"config_dir"`      // the binding's account
	LiveConfigDir  string   `json:"live_config_dir"` // the running process's
	PID            int      `json:"pid,omitempty"`
	Self           bool     `json:"self,omitempty"`
	Problems       []string `json:"problems,omitempty"`
	Info           []string `json:"info,omitempty"`
	Actions        []Action `json:"actions,omitempty"`
	Blocked        string   `json:"blocked,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Line           string   `json:"line,omitempty"`
	Result         string   `json:"result,omitempty"`
	Done           []Action `json:"done,omitempty"`
	Remaining      bool     `json:"remaining"`
	// InputDead: the input probe saw no reaction (sub_x000000000000024).
	InputDead bool `json:"input_dead,omitempty"`
	// ClosedOrphan: the surface was closed since the relaunch and its
	// conversation runs on no surface the check sees (D10: info, counted in
	// the report's header line, never a defect).
	ClosedOrphan bool `json:"closed_orphan,omitempty"`
	// Masked: set only on a row of a printed report that the reader cannot
	// read (Report.Redacted, D18) — its class, the one thing it shows beside
	// the short surface id. Never set in last.json.
	Masked Category `json:"masked,omitempty"`
	// frozen (D2): resumed hidden at a frozen width (or its screen
	// unreadable) — the only rows the Ctrl+C rescue touches; not serialized
	// (Fix runs right after Classify, in the same process).
	frozen bool
	// messageSent (revue PR B I1): Enter went out on a pilot text (PilotText
	// or PilotTextCut) in this pass — the message counts as typed whatever
	// the turn-start read says. Not serialized: PilotMessage reads it in the
	// same pass, the once-per-launch state keeps it beyond.
	messageSent bool
	// standby (D16): the row's session is in Standby (Options.InStandby, an
	// unreadable marker included, then standbyErr says why) — no text is
	// typed into it: no continue, no pilot message. Its relaunch stays (it
	// types at the shell; the session keeps its Standby, D24). Not serialized.
	standby    bool
	standbyErr string
	// A dead-input row's resume (ActResume), not serialized: the binding's
	// cwd, the surface named by --surface, the binding and its bypass, the
	// panel's workspace.
	cwd       string
	named     bool
	binding   *Binding
	bypass    bool
	workspace string
}

func (r *Row) problem(s string) { r.Problems = append(r.Problems, s) }
func (r *Row) block(s string) {
	if r.Blocked == "" {
		r.Blocked = s
	} else {
		r.Blocked += "; " + s
	}
}

// LiveClaude is one running interactive claude inside a cmux surface.
type LiveClaude struct {
	PID                                                   int
	Args, SurfaceID, ConfigDir, Marker, SessionID, Status string
	Kind                                                  string
	StatusAt, StartedAt                                   time.Time
}

// Live lists the claude processes per cmux surface. Non-interactive ones
// (`claude -p` under a session inherits its CMUX_SURFACE_ID) are dropped, and
// so is a process with no native file that descends from another listed
// process of the same surface: cmux 0.65 runs `cmux hooks claude …` children
// under each claude, whose command line carries the same token. A child with
// a native file stays (a wrapper script that does not exec its claude makes
// the real claude its child). A failing process listing is an error, never
// "nothing runs".
func Live(d Deps) (map[string][]LiveClaude, error) {
	procs, err := d.Procs()
	if err != nil {
		return nil, err
	}
	type cand struct {
		lc        LiveClaude
		ancestors []int
		native    bool
	}
	var cands []cand
	surfaceOf := map[int]string{}
	for _, p := range procs {
		env, err := d.Environ(p.PID)
		if err != nil {
			continue // gone since the listing
		}
		sf, _ := cmux.EnvValue(env, cmux.EnvSurfaceID)
		if sf == "" {
			continue
		}
		surfaceOf[p.PID] = sf
		cfg, _ := cmux.EnvValue(env, "CLAUDE_CONFIG_DIR")
		if cfg == "" {
			cfg = defaultConfigDir()
		}
		mk, _ := cmux.EnvValue(env, MarkerEnv)
		lc := LiveClaude{PID: p.PID, Args: p.Args, SurfaceID: sf, ConfigDir: cfg, Marker: mk}
		n, ok := d.Native(cfg, p.PID)
		if ok {
			if n.Kind != "" && n.Kind != "interactive" {
				continue
			}
			lc.SessionID, lc.Status, lc.Kind = n.SessionID, n.Status, n.Kind
			lc.StatusAt, lc.StartedAt = time.UnixMilli(n.StatusUpdatedAt), time.UnixMilli(n.StartedAt)
		}
		cands = append(cands, cand{lc, p.Ancestors, ok})
	}
	out := map[string][]LiveClaude{}
	for _, c := range cands {
		if !c.native && descendsOnSurface(c.ancestors, c.lc.SurfaceID, surfaceOf) {
			continue
		}
		out[c.lc.SurfaceID] = append(out[c.lc.SurfaceID], c.lc)
	}
	return out, nil
}

// descendsOnSurface: one of the ancestors is a listed process of surface.
func descendsOnSurface(ancestors []int, surface string, surfaceOf map[int]string) bool {
	for _, a := range ancestors {
		if sf, ok := surfaceOf[a]; ok && sf == surface {
			return true
		}
	}
	return false
}

var restoreLineRe = regexp.MustCompile(`^cmux restore claude \S+$`)

// Classify compares each claude binding of the photo before with what runs
// now, and plans the repairs in order: clear the input, relaunch (or start),
// continue.
func Classify(before, now Photo, live map[string][]LiveClaude, l Launch, o Options, d Deps) []Row {
	var rows []Row
	for _, p := range before.Panels {
		if p.Binding == nil || !o.wants(p.SurfaceID) {
			continue
		}
		r := Row{SurfaceID: p.SurfaceID, Title: p.Title, WorkspaceTitle: p.WorkspaceTitle, SessionID: p.Binding.CheckpointID, ConfigDir: p.Binding.ConfigDir()}
		r.standby, r.standbyErr = o.standby(r.SessionID)
		if !p.Binding.AutoResume {
			r.Info = append(r.Info, "binding autoResume=false: not resumed, by design")
		} else {
			classifyPanel(&r, p, before, now, live, l, o, d)
		}
		r.Remaining = len(r.Problems) > 0
		rows = append(rows, r)
	}
	// Round 6: a pane created after the last quit is not in the photo before;
	// a session that died there, its tab kept, would go unseen. Once the
	// relaunch is no longer recent (cmux is done resuming), the current
	// photo's bound panes without a claude are classified too — T-j, else
	// reported only (D12, decided 2026-10-08, B: never started in this lot).
	// A pane of the current photo with a live claude is not read here.
	if !d.Now().Before(l.Start.Add(ResumeWindow)) {
		for _, p := range now.Panels {
			if p.Binding == nil || !p.Binding.AutoResume || !o.wants(p.SurfaceID) || len(live[p.SurfaceID]) > 0 {
				continue
			}
			if _, inBefore := before.Panel(p.SurfaceID); inBefore {
				continue
			}
			r := Row{SurfaceID: p.SurfaceID, Title: p.Title, WorkspaceTitle: p.WorkspaceTitle, SessionID: p.Binding.CheckpointID, ConfigDir: p.Binding.ConfigDir(),
				Info: []string{"pane absent from the photo before: read from the current photo"}}
			r.standby, r.standbyErr = o.standby(r.SessionID)
			mode, bypass := guardBypass(p.ExpectedMode())
			r.Mode = mode
			if bypass {
				r.Info = append(r.Info, "binding asks bypassPermissions: never restored by the check (D8)")
			}
			classifyNoClaude(&r, p, before, now, live, l, d, bypass, true)
			r.Remaining = len(r.Problems) > 0
			rows = append(rows, r)
		}
	}
	return rows
}

// restoreBypass: may the check restore a bypassPermissions binding? D8,
// decided 2026-10-08: never — signalled only. The rule lives here and in
// guardBypass only.
const restoreBypass = false

// guardBypass applies D8: with restoreBypass false, a bypassPermissions
// binding gets no mode to restore (guarded=true) and the caller never types
// a launch line for it — it is signalled, never relaunched nor started.
func guardBypass(want string) (mode string, guarded bool) {
	if want == claudesession.ModeBypass && !restoreBypass {
		return "", true
	}
	return want, false
}

// EffectiveMode is the mode a claude runs in (T-f, F10): its argv's, else its
// account's permissions.defaultMode. An unreadable settings.json is an error,
// never a guess.
func EffectiveMode(d Deps, args, cfg string) (string, error) {
	if m := claudesession.PermissionMode(args); m != "" {
		return m, nil
	}
	if cfg == "" {
		cfg = defaultConfigDir()
	}
	return d.DefaultMode(cfg)
}

// accountDefault is the binding account's defaultMode, "" when unreadable:
// RelaunchLine then composes a line that names the mode explicitly.
func accountDefault(d Deps, b Binding) string {
	cfg := b.ConfigDir()
	if cfg == "" {
		cfg = defaultConfigDir()
	}
	m, err := d.DefaultMode(cfg)
	if err != nil {
		return ""
	}
	return m
}

func classifyPanel(r *Row, p Panel, before, now Photo, live map[string][]LiveClaude, l Launch, o Options, d Deps) {
	b := *p.Binding
	lives := live[p.SurfaceID]
	// The surface is matched by its id only; a process by the CMUX_SURFACE_ID
	// of its own environment (Live). Never by a tty (T-c, F9); the one tty read
	// is a pane shell's, from cmux tree, unique, login → running cmux (D22).
	if _, ok := now.Panel(p.SurfaceID); !ok {
		// D10 (decided 2026-10-08, A): info, never a defect; counted in the
		// report's header line when the conversation runs nowhere.
		if where := aliveElsewhere(live, b.CheckpointID, p.SurfaceID); len(where) > 0 {
			r.Info = append(r.Info, "surface closed since the relaunch (conversation alive in "+strings.Join(where, ", ")+")")
		} else {
			r.Info = append(r.Info, "surface closed since the relaunch, conversation alive nowhere")
			r.ClosedOrphan = true
		}
		return
	}
	mode, bypass := guardBypass(p.ExpectedMode())
	r.Mode = mode
	if bypass {
		r.Info = append(r.Info, "binding asks bypassPermissions: never restored by the check (D8)")
	}
	if p.SurfaceID == o.Self {
		r.Self = true
		r.block("the session running this check: relaunch it yourself")
	}
	lc, n := pick(lives, b.CheckpointID)
	switch {
	case n == 0:
		classifyNoClaude(r, p, before, now, live, l, d, bypass, false)
		return
	case n > 1:
		r.problem(fmt.Sprintf("%d claude processes on one surface", n))
		r.block("several claude processes: which one to relaunch would be a guess")
		return
	}
	r.PID, r.LiveConfigDir = lc.PID, lc.ConfigDir

	// T-h (F11): a claude stopped on a dialog (folder trust after the resume,
	// background work at /exit) waits on the user. Read before T-g: Claude has
	// no native file yet behind the trust dialog. Never answered, nothing
	// typed (no \x15, no /exit, no continue): blocked, a remaining defect.
	if sc, err := d.Screen(p.SurfaceID); err == nil {
		if kind, ok := claudesession.DialogShown(sc); ok {
			r.problem("stopped on a dialog (" + kind + "): yours to answer")
			r.block("dialog on screen: never answered by the check — answer it, then run `pigtail cmux check --fix`")
			return
		}
	}

	// T-g: the conversation on the surface must be the photo's checkpoint.
	// Relaunching from the binding would close the present one; relaunching
	// the present one would need a line nothing proves. Blocked, signalled.
	switch {
	case lc.SessionID == "":
		r.problem("conversation on the surface unknown (no native session file)")
		r.block("cannot tell which conversation runs: never relaunched blind")
		return
	case lc.SessionID != b.CheckpointID:
		r.problem(fmt.Sprintf("another conversation runs on the surface (%s, the photo says %s)", lc.SessionID, b.CheckpointID))
		r.block("relaunching would swap the conversation: relaunch it by hand")
		return
	}

	// D9 (decided 2026-10-08, A): background work the relaunch killed is
	// reported only — a problem on the row, no action, nothing typed.
	if lost, unread := lostBackground(d, l, lc, p.SurfaceID); lost != "" {
		r.problem("background work lost by the relaunch (" + lost + ")")
	} else if unread != "" {
		r.Info = append(r.Info, "background work of the previous process unreadable: "+unread)
	}

	relaunch, frozen := false, false
	// D1 (2026-10-24, the user): a process started once cmux's resume window is
	// over was not started by cmux's resume: an account or mode that differs
	// from the binding is a change made on purpose, info only. No startedAt
	// (epoch) reads as inside the window: today's behaviour.
	late := o.CmuxResumeOff || !lc.StartedAt.Before(l.Start.Add(ResumeWindow))
	if cfg := b.ConfigDir(); cfg != "" && canon(cfg) != canon(lc.ConfigDir) {
		if late {
			r.Info = append(r.Info, fmt.Sprintf("account changed since the relaunch: runs on %s, binding says %s (started after cmux's resume window: taken as wanted)", lc.ConfigDir, cfg))
		} else {
			r.problem(fmt.Sprintf("wrong account: runs on %s, binding says %s", lc.ConfigDir, cfg))
			relaunch = true
		}
	}
	if !o.CmuxResumeOff && lc.Marker != strconv.Itoa(l.PID) && !lc.StartedAt.Before(l.Start.Add(-time.Minute)) && lc.StartedAt.Before(l.Start.Add(ResumeWindow)) {
		r.problem("resumed by cmux: its width may stay frozen")
		relaunch, frozen = true, true
	}
	r.frozen = frozen
	switch eff, err := EffectiveMode(d, lc.Args, lc.ConfigDir); {
	case bypass:
		// D8: signalled above, never compared nor restored
	case r.Mode == "":
		r.Info = append(r.Info, "permission mode unknown: the binding names none")
	case err != nil:
		r.problem("effective permission mode unreadable: " + err.Error())
		r.block("effective permission mode unknown: never guessed")
	case eff != r.Mode && late:
		r.Info = append(r.Info, fmt.Sprintf("permission mode changed since the relaunch: runs with %q, binding says %s (started after cmux's resume window: taken as wanted)", eff, r.Mode))
	case eff != r.Mode:
		r.problem(fmt.Sprintf("permission mode %s lost (runs with %q: argv, else the account default)", r.Mode, eff))
		relaunch = true
	}

	// D2 (2026-10-24): a relaunch (or the quit that precedes it) resumes the
	// binding's sid under the binding's account; without its transcript it
	// would resume nothing. Blocked, never typed. Not a "busy:" reason.
	if relaunch && !bypass {
		if why := unresumable(d, b); why != "" {
			r.block(why)
		}
	}

	cut := false
	if path, ok := d.Transcript(lc.ConfigDir, lc.SessionID); ok {
		if c, isCut := claudesession.TurnCut(path); isCut && l.Previous != nil &&
			!c.At.Before(l.Previous.Last.Add(-CutWindow)) && c.At.Before(l.Start) {
			r.problem("turn cut by the relaunch (" + c.Why + ")")
			cut = true
		}
	}

	// The one busy predicate, read again by Repairer.relaunch right before
	// /exit (a repair pass lasts minutes, Review Focus 2).
	busy := busySignals(d, p.SurfaceID, lc.PID, lc.ConfigDir, lc.SessionID)
	restore, quit := false, false
	screen, err := d.Screen(p.SurfaceID)
	if err != nil {
		r.block("screen unreadable: " + cmux.OneLine(err.Error(), 120))
	} else {
		draft, found := claudesession.InputDraft(screen)
		switch {
		case !found:
			quit = unreadScreen(r, p.SurfaceID, frozen && !bypass && len(busy) == 0, o.Quit == QuitUnread || o.named(p.SurfaceID))
		case restoreLineRe.MatchString(draft):
			r.problem("`cmux restore claude …` left in the input")
			restore = true
		case draft != "":
			r.block("draft in the input")
		}
	}
	if len(busy) > 0 {
		r.block("busy: " + strings.Join(busy, ", "))
	}

	if restore {
		r.Actions = append(r.Actions, ActClearRestore)
	}
	switch {
	case quit: // D1: the quit relaunches; no /exit into an unreadable input
		r.Actions = append(r.Actions, ActQuit)
		r.Line = RelaunchLine(b, r.Mode, accountDefault(d, b), l.PID)
	case relaunch && bypass:
		r.block(blockUnsafeModeRelaunch)
	case relaunch:
		r.Actions = append(r.Actions, ActRelaunch)
		r.Line = RelaunchLine(b, r.Mode, accountDefault(d, b), l.PID)
	}
	switch {
	case cut && r.standby && r.standbyErr != "":
		r.Info = append(r.Info, r.standbyErr+"; continue not typed")
	case cut && r.standby: // D16: the reprise waits for the user's prompt
		r.Info = append(r.Info, "standby (D16): continue not typed")
	case cut:
		r.Actions = append(r.Actions, ActContinue)
	}
}

// unreadScreen applies D1 (2026-10-09, reversing D21 of the previous subject
// for Ctrl+C only): an idle claude whose screen shows no input box is quit by
// Ctrl+C then started again — never any text typed into that input, never a
// signal. quitOK: the frozen-width problem is on the row and nothing else
// stops it (no other block, no busy signal, no bypass binding); allowed: the
// policy lets it go (T-a). Reports whether ActQuit is planned.
func unreadScreen(r *Row, surface string, quitOK, allowed bool) bool {
	if !quitOK || r.Blocked != "" {
		r.block("no input box on screen")
		return false
	}
	if !allowed {
		r.block("no input box on screen: the screen reads garbled by the frozen width — run `pigtail cmux check --fix --surface " +
			surface + "` (Ctrl+C twice, then the start line; an unreadable draft is lost)")
		return false
	}
	r.problem("screen unreadable (frozen width): quit by Ctrl+C, then started again")
	return true
}

// classifyNoClaude plans a pane where no claude runs: T-j first (D11), then
// the start line once the pane's shell sits at its prompt. Shared by the
// panels of the photo before and those of the current photo only (round 6);
// for the latter (currentOnly) D12 (decided 2026-10-08, B) stops after T-j:
// an info, no action, no line, not a remaining defect. Follow-up (C), not
// in this lot: before the daemon goes to fix (D6), a transcript ending on
// /exit becomes "exited by hand" and the others may be started.
func classifyNoClaude(r *Row, p Panel, before, now Photo, live map[string][]LiveClaude, l Launch, d Deps, bypass, currentOnly bool) {
	b := *p.Binding
	// T-j (F14): the conversation already runs elsewhere, or the binding is
	// a duplicate — never started a second time, nothing typed.
	if msg, defect, stale := staleDuplicate(p, before, now, live, d); stale {
		if !defect { // D11 (decided 2026-10-08, A): alive elsewhere = info
			r.Info = append(r.Info, msg)
			return
		}
		r.problem(msg) // D11: rule (3) stays a remaining defect (exit 1)
		r.block("which surface to start it on would be a guess: start it by hand")
		return
	}
	if currentOnly { // D12 = B: reported, never started in this lot
		r.Info = append(r.Info, "not started: session dead in a surviving tab (run it by hand)")
		return
	}
	r.problem("no live claude on its surface (binding autoResume=true)")
	if bypass {
		r.block("binding runs bypassPermissions: start it by hand (D8)")
		return
	}
	// D2 (2026-10-24): no start line when the binding's transcript is absent.
	if why := unresumable(d, b); why != "" {
		r.block(why)
		return
	}
	r.Line = RelaunchLine(b, r.Mode, accountDefault(d, b), l.PID)
	idle, err := shellIdle(d, p.SurfaceID, l.PID)
	switch {
	case err != nil:
		r.block("pane state unreadable: " + err.Error())
	case !idle:
		r.block("the pane is not at its shell prompt")
	default:
		r.Actions = []Action{ActStart}
	}
}

// lostBackground (F17, D9): what the relaunch killed — tasks of the previous
// cmux run never ended, or stopped by the resume (transcript), and claude's
// "Background … didn't finish" line on screen. unread says why the
// transcript could not tell; never read as "nothing lost".
func lostBackground(d Deps, l Launch, lc LiveClaude, surface string) (lost, unread string) {
	var why []string
	if l.Previous != nil {
		if path, ok := d.Transcript(lc.ConfigDir, lc.SessionID); ok {
			ids, err := claudesession.LostBackground(path, l.Previous.First, lc.StartedAt)
			switch {
			case err != nil:
				unread = err.Error()
			case len(ids) > 0:
				why = append(why, fmt.Sprintf("tasks %v", ids))
			}
		}
	}
	if sc, err := d.Screen(surface); err == nil && claudesession.LostBackgroundShown(sc) {
		why = append(why, "the screen says a background task didn't finish")
	}
	return strings.Join(why, "; "), unread
}

// pick returns the live claude serving sid on the surface, else the only
// one (classifyPanel then checks it serves sid, T-g); n is how many there are.
func pick(lives []LiveClaude, sid string) (LiveClaude, int) {
	for _, lc := range lives {
		if lc.SessionID == sid {
			return lc, 1
		}
	}
	if len(lives) == 1 {
		return lives[0], 1
	}
	return LiveClaude{}, len(lives)
}

// aliveElsewhere lists the other surfaces where a live claude serves sid.
func aliveElsewhere(live map[string][]LiveClaude, sid, surface string) []string {
	var out []string
	for sf, lives := range live {
		if sf == surface {
			continue
		}
		for _, lc := range lives {
			if sid != "" && lc.SessionID == sid {
				out = append(out, sf)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

var alreadyRunningRe = regexp.MustCompile(`(?i)already running in process (\d+)`)

// alreadyRunningPID reads claude's answer to cmux's restore in a pane's
// shell (F14) among the last 12 non-blank lines of its screen.
func alreadyRunningPID(screen string) (string, bool) {
	var lines []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	if m := alreadyRunningRe.FindStringSubmatch(strings.Join(lines, "\n")); m != nil {
		return m[1], true
	}
	return "", false
}

// staleDuplicate applies T-j to a pane with no live claude: (1) Live shows
// the binding's conversation on another surface — the direct proof; (2) else
// the pane's screen shows claude's "already running in process N" — a
// conversation alive on no surface the check sees, only while a claude N is
// listed (round 7: the line outlives N); (3) else the conversation
// is bound to several panes still open (boundSurfaces) and none runs it —
// which one to start would be a guess. D11 (decided 2026-10-08, A): (1) and
// (2) are info with the hint to close or rebind the pane, defect=false — the
// conversation runs, nothing is lost; (3) is a remaining defect.
func staleDuplicate(p Panel, before, now Photo, live map[string][]LiveClaude, d Deps) (msg string, defect, stale bool) {
	sid := p.Binding.CheckpointID
	if where := aliveElsewhere(live, sid, p.SurfaceID); len(where) > 0 {
		// É31 (D18, D23): the twin may run on another scope's account — name it
		// by its short surface id, never by its title.
		names := make([]string, len(where))
		for i, sf := range where {
			names[i] = maintenance.ShortID(sf)
		}
		return "stale duplicate binding: conversation alive in " + strings.Join(names, ", ") + " — close or rebind this pane", false, true
	}
	if sc, err := d.Screen(p.SurfaceID); err == nil {
		if pid, ok := alreadyRunningPID(sc); ok && procListed(d, pid) {
			return "stale duplicate binding: claude says the conversation is already running in process " + pid +
				", on no surface the check sees — close or rebind this pane", false, true
		}
	}
	if n := len(boundSurfaces(before, now, sid)); n > 1 {
		return fmt.Sprintf("duplicate binding: %s bound to %d open surfaces, alive on none the check sees", sid, n), true, true
	}
	return "", false, false
}

// procListed (review round 7): claude's "already running in process N"
// stays on screen after N dies, so (2) holds only while a claude process N
// is listed now. A failing listing keeps (2): never a start on a guess.
func procListed(d Deps, pid string) bool {
	procs, err := d.Procs()
	if err != nil {
		return true
	}
	for _, p := range procs {
		if strconv.Itoa(p.PID) == pid {
			return true
		}
	}
	return false
}

// boundSurfaces lists the surfaces still open now that bind sid with
// autoResume: panels of the photo before whose surface exists in the current
// photo (round 6: a closed duplicate is no duplicate), plus the panes only in
// the current photo.
func boundSurfaces(before, now Photo, sid string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(q Panel) {
		if q.Binding == nil || !q.Binding.AutoResume || q.Binding.CheckpointID != sid || seen[q.SurfaceID] {
			return
		}
		if _, open := now.Panel(q.SurfaceID); !open {
			return
		}
		seen[q.SurfaceID] = true
		out = append(out, q.SurfaceID)
	}
	for _, q := range before.Panels {
		add(q)
	}
	for _, q := range now.Panels {
		if _, inBefore := before.Panel(q.SurfaceID); !inBefore {
			add(q)
		}
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// busySignals is the single busy predicate (Review Focus 2): the six forms —
// status not idle, status changed < Settle ago, "esc to interrupt" or a
// background row on screen, a live Bash-tool shell, the transcript written
// < QuietFor ago, background work pending in the transcript (F13). Classify
// reads it once; Repairer.relaunch reads it again right before /exit, since
// a sequential repair pass can last ~5 min. Status, transcript and shells are
// re-read from Deps, never from a LiveClaude snapshot. Unreadable = busy;
// an unreadable screen is left to the caller (Classify blocks on it, the
// repairer's draft check refuses it).
func busySignals(d Deps, surface string, pid int, cfg, sid string) []string {
	var busy []string
	var since time.Time
	switch n, ok := d.Native(cfg, pid); {
	case !ok:
		busy = append(busy, "status unknown")
	case n.Status != "idle":
		busy = append(busy, "status "+orUnknown(n.Status))
	default:
		if at := time.UnixMilli(n.StatusUpdatedAt); d.Now().Sub(at) < Settle {
			busy = append(busy, "status changed "+d.Now().Sub(at).String()+" ago")
		}
		since = time.UnixMilli(n.StartedAt)
	}
	if path, ok := d.Transcript(cfg, sid); ok {
		if mt, err := d.ModTime(path); err == nil && d.Now().Sub(mt) < QuietFor {
			busy = append(busy, "transcript written "+d.Now().Sub(mt).Round(time.Second).String()+" ago")
		}
		// F13: a finished turn with a subagent or shell still in the
		// background — /exit would open "Background work is running".
		if ids, err := claudesession.PendingBackground(path, since); err != nil {
			busy = append(busy, "background work unreadable")
		} else if len(ids) > 0 {
			busy = append(busy, fmt.Sprintf("background work %v", ids))
		}
	}
	if screen, err := d.Screen(surface); err == nil {
		if strings.Contains(screen, "esc to interrupt") {
			busy = append(busy, "esc to interrupt")
		}
		if claudesession.BackgroundWorkShown(screen) {
			busy = append(busy, "background work shown")
		}
	}
	if sh, err := d.Shells(pid); err != nil {
		busy = append(busy, "Bash-tool shells unreadable")
	} else if len(sh) > 0 {
		busy = append(busy, fmt.Sprintf("Bash-tool shell %v", sh))
	}
	return busy
}

func canon(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

// sq always single-quotes one shell word (cwd, ids, mode: `cd -- '/w/b'`).
func sq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// shq quotes an environment value only when it needs it.
func shq(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-=:") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// GatePassedEnv is the one-command prefix that tells the session gate this
// launch is a relaunch of a session already counted (cmd/pigtail
// session_spawn_line.go reads the same name): never exported, never persistent.
const GatePassedEnv = "PIGTAIL_GATE_PASSED" //nolint:gosec // an env var name, not a credential

// RelaunchLine is what the check types in the pane's shell (F5): a leading
// space (kept out of history; the shell sometimes eats the first char),
// the binding's environment plus the relaunch marker exported, then the
// binding's own command when it yields mode — its own --permission-mode, else
// the account's defaultMode (F10) — else a line composed from the binding
// that keeps cmux's claude shim (--settings cmux) and names mode explicitly.
func RelaunchLine(b Binding, mode, accountDefault string, cmuxPID int) string {
	env := map[string]string{MarkerEnv: strconv.Itoa(cmuxPID)}
	for k, v := range b.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := make([]string, len(keys))
	for i, k := range keys {
		kv[i] = k + "=" + shq(env[k])
	}
	cmd := b.Command
	prefix := GatePassedEnv + "=cmux-fix " // a prefix of the command that runs, never in the export list
	yields := (Panel{Binding: &b}).ExpectedModeFromCommand()
	if yields == "" {
		yields = accountDefault
	}
	if cmd == "" || (mode != "" && yields != mode) {
		cmd = `"$([ -x "${CMUX_CLAUDE_WRAPPER_SHIM:-}" ] && printf %s "$CMUX_CLAUDE_WRAPPER_SHIM" || printf claude)" --resume ` + sq(b.CheckpointID)
		if mode != "" {
			cmd += " --permission-mode " + sq(mode)
		}
		cmd = prefix + cmd
		if b.Cwd != "" {
			// After the &&: a leading prefix would apply to cd alone.
			cmd = "cd -- " + sq(b.Cwd) + " && " + cmd
		}
		return " export " + strings.Join(kv, " ") + "; " + cmd
	}
	return " export " + strings.Join(kv, " ") + "; " + prefix + cmd
}

// unresumable (D2): "" when the binding's conversation has a transcript under
// the binding's account, else the reason the row is blocked. A lookup failure
// is never read as "absent", nor as "found".
func unresumable(d Deps, b Binding) string {
	cfg := b.ConfigDir()
	if cfg == "" {
		cfg = defaultConfigDir()
	}
	if d.TranscriptFound == nil {
		return "transcript lookup not wired: never relaunched blind"
	}
	found, err := d.TranscriptFound(cfg, b.CheckpointID)
	switch {
	case err != nil:
		return fmt.Sprintf("transcript of %s under %s unreadable (%s): never relaunched blind", b.CheckpointID, cfg, cmux.OneLine(err.Error(), 120))
	case !found:
		return fmt.Sprintf("no transcript of %s under %s (the binding's account): a relaunch would resume nothing — relaunch it by hand", b.CheckpointID, cfg)
	}
	return ""
}
