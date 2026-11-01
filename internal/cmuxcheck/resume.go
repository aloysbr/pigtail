package cmuxcheck

import (
	"errors"
	"fmt"
	"syscall"

	"github.com/aloysbr/pigtail/internal/inputprobe"
)

// Resume of a dead-input session in a new tab (sub_x000000000000024, spec §
// Comportement 5): its keystrokes never reach its pty, so /exit cannot pass.
// Every guard is read before anything stops; then the old claude gets a
// SIGTERM (never a forced kill, never a pause), a new tab opens in the same
// workspace without focus, the binding's line is typed at its shell, and the
// dead tab closes only once the new claude is seen on the conversation.

// Visible lines of the resume (plan ## Goût, G2), verbatim.
const (
	resultInputBack  = "input back, nothing done"
	infoOldTabKept   = "old tab not closed"
	blockNoCwd       = "binding names no working directory: resume by hand"
	refusedNotNamed  = "resume refused: only for a surface named by --surface"
	handResumePrefix = "new claude not seen in the new tab, old tab kept: resume by hand with `"
	// stopFirstFormat: the old claude may still run — never two claudes on
	// one transcript, so the line says to stop it first.
	stopFirstFormat = "old claude (pid %d) still running after SIGTERM, nothing opened: stop it first, then resume by hand with `%s`"
)

// resultProbeInconclusive: the re-probe before the stop could not tell.
const resultProbeInconclusive = "input probe inconclusive, nothing done"

// TabNotOpenedMessage is the Result of a resume whose new tab did not open.
func TabNotOpenedMessage(line string) string {
	return "old claude stopped, new tab not opened, old tab kept: resume by hand with `" + line + "`"
}

// StopFirstMessage is the Result of a resume whose old claude did not stop.
func StopFirstMessage(pid int, line string) string { return fmt.Sprintf(stopFirstFormat, pid, line) }

// killTERMOnly is the Repairer's default Kill: SIGTERM to a real pid, and
// nothing else (D4 of sub_x000000000000024 lifts D21 for this one signal;
// TestNoSignalSent pins it).
func killTERMOnly(pid int, sig syscall.Signal) error {
	if sig != syscall.SIGTERM || pid <= 1 {
		return fmt.Errorf("refused signal %v to pid %d: SIGTERM only, to a claude pid", sig, pid)
	}
	return syscall.Kill(pid, sig)
}

// stillAliveError: the SIGTERM failed or the old claude outlived
// ExitTimeout: nothing opened, no forced kill; the line says to stop it first.
type stillAliveError struct {
	pid   int
	line  string
	cause error
}

func (e *stillAliveError) Error() string { return StopFirstMessage(e.pid, e.line) }
func (e *stillAliveError) Unwrap() error { return e.cause }

// HandResumeMessage is the Result of a resume whose new claude was not seen.
func HandResumeMessage(line string) string { return handResumePrefix + line + "`" }

// blockedError: a guard of the resume failed before anything stopped — the
// row ends Blocked with that text.
type blockedError struct{ why string }

func (e *blockedError) Error() string { return e.why }

// errInputBack: the input answers again: nothing was done.
var errInputBack = errors.New(resultInputBack)

// errProbeUnknown: the re-probe could not tell: nothing was done.
var errProbeUnknown = errors.New(resultProbeInconclusive)

// tabNotOpenedError: the old claude is stopped but the new tab did not open:
// the old tab stays, the line is given.
type tabNotOpenedError struct {
	line  string
	cause error
}

func (e *tabNotOpenedError) Error() string { return TabNotOpenedMessage(e.line) }
func (e *tabNotOpenedError) Unwrap() error { return e.cause }

// notSeenError: the old claude is stopped, the new tab open, but no new
// claude was seen on the conversation: the old tab stays, the line is given.
type notSeenError struct {
	line  string
	cause error
}

func (e *notSeenError) Error() string { return HandResumeMessage(e.line) }
func (e *notSeenError) Unwrap() error { return e.cause }

// resume runs ActResume on row, whose claude is pid. It returns the new
// claude's pid.
func (r *Repairer) resume(row *Row, pid int) (int, error) {
	// 1. Guards, all before any stop.
	if r.Kill == nil || r.NewTab == nil || r.CloseTab == nil {
		return pid, errors.New("resume seams not wired: nothing stopped")
	}
	lc, err := r.soleClaude(row.SurfaceID, pid)
	if err != nil {
		return pid, err
	}
	switch {
	// Defence in depth: the real pilot guard is inputDead's, at plan time
	// (no resume planned on the pilot); Run sets r.Pilot only under
	// --unattended, which names no surface, so this case should never fire.
	case r.Pilot != "" && sameSurface(row.SurfaceID, r.Pilot):
		return pid, &blockedError{infoPilotNoResume}
	case row.binding == nil:
		return pid, &blockedError{infoNoBinding}
	case lc.SessionID == "" || row.binding.CheckpointID != lc.SessionID:
		return pid, &blockedError{infoCheckpointDiffer}
	case row.bypass:
		return pid, &blockedError{blockUnsafeModeRelaunch}
	}
	if why := unresumable(r.D, *row.binding); why != "" {
		return pid, &blockedError{why}
	}
	cfg := row.binding.ConfigDir()
	if cfg == "" {
		cfg = defaultConfigDir()
	}
	if lc.ConfigDir == "" || canon(cfg) != canon(lc.ConfigDir) {
		return pid, &blockedError{blockAccountDiffers}
	}
	if row.cwd == "" {
		return pid, &blockedError{blockNoCwd}
	}
	if busy := busySignals(r.D, row.SurfaceID, pid, lc.ConfigDir, lc.SessionID); len(busy) > 0 {
		return pid, &busyError{Reasons: busy}
	}
	if r.D.ProbeInput == nil {
		return pid, errors.New("input probe not wired: nothing stopped")
	}
	// The re-probe's tty is read now, and claude must read it alone: the
	// probe re-reads it before each write and refuses a tty that moved.
	if r.D.TTYOf == nil {
		return pid, errProbeUnknown
	}
	info, err := r.D.TTYOf(pid)
	if err != nil || info.TTY == "" || !info.Foreground {
		return pid, errProbeUnknown
	}
	switch r.D.ProbeInput(inputprobe.Target{Surface: row.SurfaceID, PID: pid, SessionID: lc.SessionID, TTY: info.TTY}).Verdict {
	case inputprobe.VerdictDead:
	case inputprobe.VerdictUnknown:
		return pid, errProbeUnknown
	default:
		return pid, errInputBack
	}
	ws := row.workspace
	if ws == "" {
		if r.WorkspaceOf == nil {
			return pid, errors.New("workspace of the surface unknown: nothing stopped")
		}
		if ws, err = r.WorkspaceOf(row.SurfaceID); err != nil {
			return pid, fmt.Errorf("workspace of the surface unreadable (%w): nothing stopped", err)
		}
	}

	// 2. Stop the old claude: SIGTERM only, once, to the pid read again
	// right now (never a pid reused since the guards).
	if _, err := r.soleClaude(row.SurfaceID, pid); err != nil {
		return pid, err
	}
	if err := r.Kill(pid, syscall.SIGTERM); err != nil {
		return pid, &stillAliveError{pid: pid, line: row.Line, cause: fmt.Errorf("SIGTERM failed: %w", err)}
	}
	for i := 0; r.PidAlive(pid); i++ {
		if i >= int(r.ExitTimeout/r.Poll) {
			return pid, &stillAliveError{pid: pid, line: row.Line, cause: fmt.Errorf("no exit within %s, no forced kill sent", r.ExitTimeout)}
		}
		r.Sleep(r.Poll)
	}

	// 3. A new tab in the same workspace, without focus; the line at its shell.
	newID, err := r.NewTab(ws, row.cwd)
	if err != nil {
		return 0, &tabNotOpenedError{line: row.Line, cause: fmt.Errorf("new tab: %w", err)}
	}
	nrow := *row
	nrow.SurfaceID = newID
	idle := false
	for i := 0; ; i++ {
		if ok, err := shellIdle(r.D, newID, r.CmuxPID); err == nil && ok {
			idle = true
			break
		}
		if i >= int(r.ShellTimeout/r.Poll) {
			break
		}
		r.Sleep(r.Poll)
	}
	if !idle {
		return 0, &notSeenError{line: row.Line, cause: fmt.Errorf("the new tab's shell never reached its prompt within %s: start line not typed", r.ShellTimeout)}
	}
	if err := r.gate(&nrow, 0, gateOpts{}); err != nil {
		return 0, &notSeenError{line: row.Line, cause: err}
	}
	before, err := r.pids()
	if err != nil {
		return 0, &notSeenError{line: row.Line, cause: err}
	}
	npid, err := r.typeAndSee(&nrow, before)
	if err != nil {
		return npid, &notSeenError{line: row.Line, cause: err}
	}

	// 4. Close the dead tab, only now.
	if err := r.CloseTab(row.SurfaceID, ws); err != nil {
		row.Info = append(row.Info, infoOldTabKept, "close: "+err.Error())
	}
	return npid, nil
}

// soleClaude reads Live and returns the one claude on surface, which must
// still be pid; anything else is an error and nothing is stopped.
func (r *Repairer) soleClaude(surface string, pid int) (LiveClaude, error) {
	live, err := Live(r.D)
	if err != nil {
		return LiveClaude{}, fmt.Errorf("process listing before the stop: %w (nothing stopped)", err)
	}
	var lc LiveClaude
	found := 0
	for _, x := range live[surface] {
		found++
		if x.PID == pid {
			lc = x
		}
	}
	if found != 1 || lc.PID != pid || pid <= 0 {
		return LiveClaude{}, fmt.Errorf("claude on the surface is no longer pid %d: nothing stopped", pid)
	}
	return lc, nil
}
