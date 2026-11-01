package cmuxcheck

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
)

// Repairer types into panes. Every step reads the screen back before the
// next keystroke: Enter is pressed only on the exact text expected.
type Repairer struct {
	D            Deps
	Send         func(surface, text string) error
	PidAlive     func(int) bool
	Sleep        func(time.Duration)
	Poll         time.Duration
	ExitTimeout  time.Duration
	SeenTimeout  time.Duration
	SeenSettle   time.Duration // F16: a new claude must still run this long after it is seen
	ReadyTimeout time.Duration
	CmuxPID      int    // the running cmux (Launch.PID), set by Run: a pane shell is the child of its login (D22)
	Pilot        string // D5: the pilot surface (set by Run) — its continue is PilotTextCut()

	CtrlCWait     time.Duration    // T-c: how long a press has to show its effect
	CtrlCPoll     time.Duration    // T-c: read step after a press — Claude's confirmation window is short (~0.8 s), Poll (500 ms) is too coarse
	CtrlCExitWait time.Duration    // revue 2 N1: reads after the confirming press — F8 measured the exit at ≤ 2.1 s, longer than CtrlCWait
	ShellTimeout  time.Duration    // T-c: the pane's shell back at its prompt after the quit
	Now           func() time.Time // revue 2 M9: clock of the "Ctrl+C timing" info; nil = no line

	// The dead-input resume (ActResume, sub_x000000000000024): Kill sends
	// the old claude its SIGTERM (never SIGSTOP nor SIGKILL; default
	// killTERMOnly, resume.go — D21 of sub_x000000000000043 kept, lifted for
	// this one case by D4 of sub_x000000000000024, the user 2026-10-26), NewTab opens
	// the new tab without focus, CloseTab closes the dead one, WorkspaceOf
	// finds the surface's workspace when the photo does not name it.
	Kill        func(pid int, sig syscall.Signal) error
	NewTab      func(workspace, cwd string) (string, error)
	CloseTab    func(surface, workspace string) error
	WorkspaceOf func(surface string) (string, error)
}

func DefaultRepairer(d Deps) *Repairer {
	return &Repairer{D: d, Send: cmux.SendText, PidAlive: claudesession.PidAlive, Sleep: time.Sleep,
		Poll: 500 * time.Millisecond, ExitTimeout: 15 * time.Second, SeenTimeout: 30 * time.Second, SeenSettle: 3 * time.Second, ReadyTimeout: time.Minute,
		CtrlCWait: 1500 * time.Millisecond, CtrlCPoll: 100 * time.Millisecond, CtrlCExitWait: 3 * time.Second, ShellTimeout: 10 * time.Second, Now: time.Now,
		Kill: killTERMOnly, NewTab: cmux.NewTab, CloseTab: cmux.CloseSurface, WorkspaceOf: cmux.WorkspaceOfSurface}
}

// Fix runs the row's actions in order; the first failure stops the row and
// leaves it Remaining with the step and the reason.
func (r *Repairer) Fix(row *Row) {
	if row.Blocked != "" || len(row.Actions) == 0 {
		row.Remaining = len(row.Problems) > 0
		return
	}
	pid := row.PID
	if slices.Contains(row.Actions, ActResume) { // the resume runs alone (resume.go)
		r.fixResume(row, pid)
		return
	}
	rescued := false
	for _, a := range row.Actions {
		// T-h: re-read before each action — a dialog may have opened since
		// Classify, or on the claude just relaunched. Never typed into.
		// DialogShown ignores only an answered folder-trust dialog above the
		// last box (T-k, round 6); every other kind counts on the whole tail.
		err := r.dialog(row.SurfaceID)
		if err == nil {
			switch a {
			case ActClearRestore:
				var npid int
				npid, err = r.clearInput(row, pid)
				rescued, pid = npid != pid, npid
			case ActRelaunch:
				if !rescued { // D2: the rescue already relaunched it
					pid, err = r.relaunch(row, pid)
				}
			case ActStart:
				pid, err = r.launch(row, true)
			case ActContinue:
				err = r.cont(row, pid)
			case ActQuit:
				if err = r.quit(row, pid); err == nil {
					if pid, err = r.launch(row, false); err != nil {
						err = fmt.Errorf("claude quit; start line not typed: %w", err) // revue 1 #9: the pane is at its shell
					}
				}
			}
		}
		var ae *aliveElsewhereError
		if errors.As(err, &ae) { // D11: alive elsewhere = info, nothing typed, not a defect
			row.Info = append(row.Info, ae.Error())
			row.Result, row.Remaining = "", false
			return
		}
		if err != nil {
			row.Result = fmt.Sprintf("%s failed: %v — the pane shows: %s", a, err, r.tail(row.SurfaceID))
			var be *busyError
			switch {
			case errors.As(err, &be):
				row.Blocked = "busy: " + strings.Join(be.Reasons, ", ") // busy since Classify: nothing typed
			case errors.Is(err, errBackground):
				row.Blocked = "busy: background work" // T-i: nothing left typed, a busy row
			}
			row.Remaining = true
			row.PID = pid
			return
		}
		row.Done = append(row.Done, a)
	}
	row.PID = pid
	row.Result = "repaired"
	row.Remaining = false
}

// dialog is an error when the surface shows a Claude Code dialog (T-h).
func (r *Repairer) dialog(surface string) error {
	if s, err := r.D.Screen(surface); err == nil {
		if kind, ok := claudesession.DialogShown(s); ok {
			return fmt.Errorf("stopped on a dialog (%s): nothing typed — answer it, then run `pigtail cmux check --fix`", kind)
		}
	}
	return nil
}

func (r *Repairer) draft(surface string) (string, bool) {
	s, err := r.D.Screen(surface)
	if err != nil {
		return "", false
	}
	return claudesession.InputDraft(s)
}

func (r *Repairer) clearInput(row *Row, pid int) (int, error) {
	if err := r.gate(row, pid, gateOpts{full: true}); err != nil {
		return pid, err
	}
	if err := r.Send(row.SurfaceID, "\x15"); err != nil {
		return pid, err
	}
	r.Sleep(500 * time.Millisecond)
	if d, ok := r.draft(row.SurfaceID); !ok || d != "" {
		return r.rescue(row, pid, fmt.Errorf("input not cleared (shows %q)", d))
	}
	return pid, nil
}

// rescue (D2): a read-back failed on a frozen pane — at frozen width the
// slash menu blinds InputDraft (real: "unexpected input after typing /exit"
// ×4, "input not cleared" ×1). Ctrl+C (quit) then the start line, when the
// row is frozen and has one; otherwise the first failure stands (a healthy
// width that does not read back more likely has a person typing, revue 1 #3).
func (r *Repairer) rescue(row *Row, pid int, cause error) (int, error) {
	if !row.frozen || row.Line == "" || pid <= 0 {
		return pid, cause
	}
	if err := r.quit(row, pid); err != nil {
		return pid, fmt.Errorf("%w; Ctrl+C rescue: %w", cause, err)
	}
	npid, err := r.launch(row, false)
	if err != nil {
		return npid, fmt.Errorf("%w; Ctrl+C rescue: claude quit; start line not typed: %w", cause, err)
	}
	row.Info = append(row.Info, "rescued by Ctrl+C after: "+cause.Error())
	return npid, nil
}

func (r *Repairer) relaunch(row *Row, pid int) (int, error) {
	if d, ok := r.draft(row.SurfaceID); !ok || d != "" {
		return pid, fmt.Errorf("input not empty before /exit (%q)", d)
	}
	// Review Focus 2: the six busy forms read again, the very predicate
	// Classify used — minutes may have passed since it ran —, behind the
	// gate (T-b: dialog, keyboard focus).
	if err := r.gate(row, pid, gateOpts{full: true}); err != nil {
		return pid, err
	}
	before, err := r.pids()
	if err != nil {
		return pid, err
	}
	if err := r.Send(row.SurfaceID, "/exit"); err != nil {
		return pid, err
	}
	r.Sleep(600 * time.Millisecond)
	if d, ok := r.draft(row.SurfaceID); !ok || d != "/exit" {
		_ = r.Send(row.SurfaceID, "\x15") // the check's own text (T-b exception)
		return r.rescue(row, pid, fmt.Errorf("unexpected input after typing /exit (%q): Enter not pressed", d))
	}
	if err := r.gate(row, pid, gateOpts{}); err != nil {
		_ = r.Send(row.SurfaceID, "\x15") // the check's own /exit (T-b exception; cost accepted)
		return pid, err
	}
	if err := r.Send(row.SurfaceID, "\r"); err != nil {
		return pid, err
	}
	if err := r.waitExit(row.SurfaceID, pid, "/exit"); err != nil {
		return pid, err
	}
	r.Sleep(time.Second)
	// Revue 2 N3: waitExit may last ExitTimeout + 1 s — the start line has
	// its own gate.
	if err := r.gate(row, 0, gateOpts{}); err != nil {
		return 0, fmt.Errorf("claude quit; start line not typed: %w", err) // the pane is at its shell
	}
	return r.typeAndSee(row, before)
}

func (r *Repairer) launch(row *Row, startAction bool) (int, error) {
	// Classify saw the pane at its shell prompt; something may run there now.
	idle, err := shellIdle(r.D, row.SurfaceID, r.CmuxPID)
	if err != nil {
		return 0, fmt.Errorf("the pane is no longer at its shell prompt (%w): start line not typed", err)
	}
	if !idle {
		return 0, errors.New("the pane is no longer at its shell prompt: start line not typed")
	}
	// T-j: the conversation may have come up on another surface since
	// Classify — never started a second time. Unreadable = not typed.
	live, err := Live(r.D)
	if err != nil {
		return 0, fmt.Errorf("process listing before the start line: %w", err)
	}
	if where := aliveElsewhere(live, row.SessionID, row.SurfaceID); len(where) > 0 {
		return 0, &aliveElsewhereError{Where: where}
	}
	// D4 (sub_x000000000000037): cmux's resume is off, so the pane holding
	// the keyboard focus at boot would stay a bare shell. A start (never a
	// live claude: pid 0 here, dialog checked first by gate) is allowed
	// through the focus refusal only when the shell input line, read right
	// now, is empty. Anything else stays "left for the user".
	focusBypassed := false
	if err := r.gate(row, 0, gateOpts{}); err != nil {
		var be *busyError
		if !startAction || !errors.As(err, &be) || len(be.Reasons) != 1 || be.Reasons[0] != focusReason {
			return 0, err // startAction: an ActStart row only, never the start after a quit or a relaunch
		}
		if !r.focusedShellLineEmpty(row.SurfaceID) {
			return 0, err
		}
		focusBypassed = true
	}
	before, err := r.pids()
	if err != nil {
		return 0, err
	}
	return r.typeAndSeeFocus(row, before, focusBypassed)
}

// focusedShellLineEmpty: the pane's last non-blank screen line is a shell
// prompt (ends with %, $, # or ❯ after trimming spaces) with nothing after
// it. Unreadable screen or unrecognised prompt = false (nothing typed).
func (r *Repairer) focusedShellLineEmpty(surface string) bool {
	s, err := r.D.Screen(surface)
	if err != nil {
		return false
	}
	return shellPromptEmptyRe.MatchString(lastLine(s))
}

// shellPromptEmptyRe: a prompt char followed by at least one space or NBSP
// up to the end of the raw line. cmux read-screen keeps the trailing spaces
// and draws the one after the prompt as NBSP (U+00A0); Go's \s does not match
// NBSP. Typed text ending in a prompt char (`echo 50%`) has no space after it:
// not an empty prompt. (A typed trailing space after such a char is read as
// empty: accepted, the read-back before Send is the only guard.)
var shellPromptEmptyRe = regexp.MustCompile(`[%$#❯][ \x{00A0}\t]+$`)

func blankLine(l string) bool {
	return strings.TrimFunc(l, func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\u00a0' }) == ""
}

// lastLine: the last non-blank line of a screen, raw (trailing spaces kept:
// the empty prompt is told from typed text by them).
func lastLine(screen string) string {
	ls := strings.Split(screen, "\n")
	for i := len(ls) - 1; i >= 0; i-- {
		if !blankLine(ls[i]) {
			return strings.TrimRight(ls[i], "\r")
		}
	}
	return ""
}

func (r *Repairer) typeAndSee(row *Row, before map[int]bool) (int, error) {
	return r.typeAndSeeFocus(row, before, false)
}

// typeAndSeeFocus: focusBypassed (D4) re-reads the input line right before
// the keystroke — nothing in between — and leaves the row busy on focus when
// the user has typed since.
func (r *Repairer) typeAndSeeFocus(row *Row, before map[int]bool, focusBypassed bool) (int, error) {
	if focusBypassed && !r.focusedShellLineEmpty(row.SurfaceID) {
		return 0, &busyError{Reasons: []string{focusReason}}
	}
	if err := r.Send(row.SurfaceID, row.Line+"\n"); err != nil {
		return 0, err
	}
	return r.seen(row, before)
}

var errNotSeen = errors.New("no new claude seen on the surface")

// aliveElsewhereError: T-j met at the start line — the conversation came up
// on another surface since Classify. D11: info with the hint, not a defect.
type aliveElsewhereError struct{ Where []string }

func (e *aliveElsewhereError) Error() string {
	return "stale duplicate binding: conversation now alive in " + strings.Join(e.Where, ", ") +
		" — close or rebind this pane (start line not typed)"
}

// errBackground: the session has background work (F13, T-i) — the row ends
// "busy: background work", never relaunched.
var errBackground = errors.New("background work running: not relaunched")

// errSentNoTurn (revue PR B I1): Enter went out on the typed text, but no
// turn was seen starting within ReadyTimeout — the native status may lag, or
// the turn ended between two reads. On a pilot text the message counts as
// typed (Row.messageSent): never typed again.
var errSentNoTurn = errors.New("text typed and Enter pressed, but no turn seen")

// focusReason: the gate's refusal on the surface holding cmux's keyboard
// focus (T-b) — an occupation the second pass retries (D6, D9).
const focusReason = "pane has keyboard focus"

// SecondPassDelay (D6): how long after its first pass pigtaild runs one more
// pass on the rows left busy — the watcher's delay (cmuxcheck_wiring.go) and
// the pilot's focus note read this one value.
const SecondPassDelay = 5 * time.Minute

// busyError: the gate found the session busy, or its pane holding the
// keyboard focus, right before a keystroke — the row ends "busy: <reasons>".
type busyError struct{ Reasons []string }

func (e *busyError) Error() string {
	return "busy (" + strings.Join(e.Reasons, ", ") + "): nothing typed"
}

// cfgOf: the account of the claude running on the row — the live one when
// known, else the binding's.
func cfgOf(row *Row) string {
	if row.LiveConfigDir != "" {
		return row.LiveConfigDir
	}
	return row.ConfigDir
}

// gateOpts: what the gate reads (T-b). full: the six forms of busySignals,
// before the first keystroke into a claude the check did not start; else
// its native status idle (between Ctrl+C presses, into a claude the check
// just started — the full form would block on its fresh transcript).
// noDialog: the caller read this screen for the dialog a moment ago
// (afterPress, between presses: the confirmation window is short, T-c).
// focusOK: the pilot message only (D9) — typed through the keyboard focus.
type gateOpts struct{ full, noDialog, focusOK bool }

// gate is read right before every keystroke (T-b, D1): no Claude Code dialog
// on the pane (T-h), not the surface holding cmux's keyboard focus, and for a
// live claude (pid > 0) not busy. The focused pane is an occupation (the
// second pass, D6, tries again); a focus it cannot read is a plain error:
// nothing typed.
func (r *Repairer) gate(row *Row, pid int, g gateOpts) error {
	if !g.noDialog {
		if err := r.dialog(row.SurfaceID); err != nil {
			return err
		}
	}
	if !g.focusOK {
		if r.D.Focused == nil {
			return errors.New("keyboard focus unreadable (no seam): nothing typed")
		}
		f, err := r.D.Focused()
		if err != nil {
			return fmt.Errorf("keyboard focus unreadable (%w): nothing typed", err)
		}
		if sameSurface(f, row.SurfaceID) {
			return &busyError{Reasons: []string{focusReason}}
		}
	}
	if pid <= 0 {
		return nil
	}
	if g.full {
		if busy := busySignals(r.D, row.SurfaceID, pid, cfgOf(row), row.SessionID); len(busy) > 0 {
			return &busyError{Reasons: busy}
		}
		return nil
	}
	if n, ok := r.D.Native(cfgOf(row), pid); !ok || n.Status != "idle" {
		return &busyError{Reasons: []string{"status " + orUnknown(n.Status)}}
	}
	return nil
}

// cancelOwnExitDialog closes the "Background work is running" dialog the
// check's own /exit or Ctrl+C just opened (T-i b) — the only dialog it ever
// touches; cause names which. Esc alone: Enter would take the preselected
// option and kill the session, a digit picks one. Gone and the input box
// back = errBackground.
func (r *Repairer) cancelOwnExitDialog(surface, cause string) error {
	if err := r.Send(surface, "\x1b"); err != nil {
		return fmt.Errorf("background-work dialog opened by %s, Esc not sent (%w): answer it by hand, never Enter", cause, err)
	}
	for i := 0; i <= int(r.ExitTimeout/r.Poll); i++ {
		r.Sleep(r.Poll)
		s, err := r.D.Screen(surface)
		if err != nil {
			continue
		}
		if _, found := claudesession.InputDraft(s); found && !exitDialogShown(s) {
			return errBackground
		}
	}
	return fmt.Errorf("background-work dialog opened by %s still open after Esc: answer it by hand, never Enter", cause)
}

// exitDialogShown: "Background work is running" on the end of the screen,
// below the last input box (É2, review round 5), never the whole screen,
// where the conversation may quote it. Round 6: no longer DialogShown, which
// reads that kind on the whole tail (fails safe for Classify); here a miss
// leaves the pid alive — "did not exit", nothing typed.
func exitDialogShown(screen string) bool {
	return claudesession.ExitDialogBelowBox(screen)
}

// waitExit counts polls rather than reading the clock, so tests with a
// no-op Sleep terminate.
// after names what should have ended it: "/exit" or "Ctrl+C".
func (r *Repairer) waitExit(surface string, pid int, after string) error {
	for i := 0; r.PidAlive(pid); i++ {
		if s, err := r.D.Screen(surface); err == nil && exitDialogShown(s) {
			return r.cancelOwnExitDialog(surface, after) // opened by our own /exit or Ctrl+C (T-i)
		}
		if i >= int(r.ExitTimeout/r.Poll) {
			return fmt.Errorf("claude pid %d did not exit within %s after %s", pid, r.ExitTimeout, after)
		}
		r.Sleep(r.Poll)
	}
	return nil
}

func (r *Repairer) pids() (map[int]bool, error) {
	procs, err := r.D.Procs()
	if err != nil {
		return nil, fmt.Errorf("list claude processes: %w", err)
	}
	m := make(map[int]bool, len(procs))
	for _, p := range procs {
		m[p.PID] = true
	}
	return m, nil
}

// seen waits for a new claude on the row's surface (its own CMUX_SURFACE_ID,
// never a tty) and checks it runs on the binding's account, in the expected
// effective mode (argv, else the account default — F10), then that it is
// still there on the row's conversation a moment later (settled, F16).
func (r *Repairer) seen(row *Row, before map[int]bool) (int, error) {
	firstDead := 0 // first new claude of the surface seen already dead
	for i := 0; i <= int(r.SeenTimeout/r.Poll); i++ {
		if procs, err := r.D.Procs(); err == nil {
			cands, dead := r.newClaudesOn(procs, before, row.SurfaceID, nil)
			if firstDead == 0 && len(dead) > 0 {
				firstDead = dead[0].PID
			}
			if len(cands) > 0 {
				c := cands[0]
				cfg, err := r.checkStarted(row, c)
				if err != nil {
					// A short-lived claude with another env must not fail the
					// row either: look for the claude the start line ran.
					if next, ncfg, ok := r.successor(row, before, map[int]bool{c.PID: true}); ok {
						row.LiveConfigDir = row.ConfigDir
						return r.settled(row, next, ncfg, before)
					}
					return c.PID, err
				}
				row.LiveConfigDir = row.ConfigDir
				return r.settled(row, c.PID, cfg, before)
			}
		}
		r.Sleep(r.Poll)
	}
	if firstDead != 0 { // it died at once and no live successor appeared
		return firstDead, fmt.Errorf("claude pid %d exited at once: start it from a fresh workspace (no other new claude on the surface within %s)", firstDead, r.SeenTimeout)
	}
	return 0, fmt.Errorf("%w within %s", errNotSeen, r.SeenTimeout)
}

// newClaudesOn: the claude processes of procs not in before nor skip, on
// surface. alive are the living ones — minus one whose ancestor is another of
// them (cmux's `cmux hooks claude …` children): the start line ran the
// top-most one. dead are those already gone, kept only for seen's « exited at
// once » memory. Each pid is read once. 2026-10-28: an ephemeral claude of the
// surface was taken for the relaunch (5 false « exited at once »); Live()
// already drops such children, seen() did not.
func (r *Repairer) newClaudesOn(procs []claudesession.Proc, before map[int]bool, surface string, skip map[int]bool) (alive, dead []claudesession.Proc) {
	var cands []claudesession.Proc
	on := map[int]bool{}
	for _, p := range procs {
		if before[p.PID] || skip[p.PID] {
			continue
		}
		env, err := r.D.Environ(p.PID)
		if err != nil {
			continue
		}
		if s, _ := cmux.EnvValue(env, cmux.EnvSurfaceID); s != surface {
			continue
		}
		if !r.PidAlive(p.PID) {
			dead = append(dead, p)
			continue
		}
		on[p.PID] = true
		cands = append(cands, p)
	}
	for _, p := range cands {
		if !slices.ContainsFunc(p.Ancestors, func(a int) bool { return on[a] }) {
			alive = append(alive, p)
		}
	}
	return alive, dead
}

// checkStarted: the new claude runs on the binding's account, in the
// expected effective mode (F10). Returns its CLAUDE_CONFIG_DIR.
func (r *Repairer) checkStarted(row *Row, p claudesession.Proc) (string, error) {
	env, err := r.D.Environ(p.PID)
	if err != nil {
		return "", fmt.Errorf("claude pid %d: environment unreadable: %w", p.PID, err)
	}
	cfg, _ := cmux.EnvValue(env, "CLAUDE_CONFIG_DIR")
	if row.ConfigDir != "" && canon(cfg) != canon(row.ConfigDir) {
		return cfg, fmt.Errorf("relaunched on the wrong account (%s, want %s)", cfg, row.ConfigDir)
	}
	if row.Mode != "" {
		eff, err := EffectiveMode(r.D, p.Args, cfg)
		if err != nil {
			return cfg, fmt.Errorf("relaunched without permission mode %s (effective mode unknown: %w; argv %s)", row.Mode, err, cmux.OneLine(p.Args, 160))
		}
		if eff != row.Mode {
			return cfg, fmt.Errorf("relaunched without permission mode %s (effective %q; argv %s)", row.Mode, eff, cmux.OneLine(p.Args, 160))
		}
	}
	return cfg, nil
}

// successor: another new claude on the surface, within SeenTimeout, that
// passes checkStarted — the one the start line ran when the pid seen first
// was short-lived. settled then reads its native file (the conversation).
func (r *Repairer) successor(row *Row, before map[int]bool, tried map[int]bool) (int, string, bool) {
	for i := 0; i <= int(r.SeenTimeout/r.Poll); i++ {
		if procs, err := r.D.Procs(); err == nil {
			alive, _ := r.newClaudesOn(procs, before, row.SurfaceID, tried)
			for _, p := range alive {
				if cfg, err := r.checkStarted(row, p); err == nil {
					return p.PID, cfg, true
				}
			}
		}
		r.Sleep(r.Poll)
	}
	return 0, "", false
}

// settled (F16, review round 6): the new claude must still run, SeenSettle
// after it was seen, on the row's conversation. A resume typed in a tab whose
// binding is stale exits at once on « Resume this session with », with no
// error: without this the row would read "repaired". Behind the folder-trust
// dialog Claude has no native file yet: the dialog is the answer (T-h). Polls
// up to SeenTimeout for the native file, never longer. A pid that died is
// replaced by a successor on the surface when one passes checkStarted.
func (r *Repairer) settled(row *Row, pid int, cfg string, before map[int]bool) (int, error) {
	if cfg == "" {
		cfg = defaultConfigDir()
	}
	tried := map[int]bool{}
	r.Sleep(r.SeenSettle)
	for i := 0; ; i++ {
		if !r.PidAlive(pid) {
			tried[pid] = true
			if next, ncfg, ok := r.successor(row, before, tried); ok {
				pid, cfg, i = next, ncfg, -1
				if cfg == "" {
					cfg = defaultConfigDir()
				}
				r.Sleep(r.SeenSettle) // F16: the successor gets its own settle wait
				continue              // the checks below run on the successor
			}
			return pid, fmt.Errorf("claude pid %d exited at once: start it from a fresh workspace (no other new claude on the surface within %s)", pid, r.SeenTimeout)
		}
		if err := r.dialog(row.SurfaceID); err != nil {
			return pid, err
		}
		if n, ok := r.D.Native(cfg, pid); ok {
			if n.SessionID != row.SessionID {
				return pid, fmt.Errorf("claude pid %d serves conversation %q, not %q: start it from a fresh workspace", pid, n.SessionID, row.SessionID)
			}
			return pid, nil
		}
		if i >= int(r.SeenTimeout/r.Poll) {
			return pid, fmt.Errorf("claude pid %d wrote no native session file within %s", pid, r.SeenTimeout)
		}
		r.Sleep(r.Poll)
	}
}

// cont types the continue (D2 of the previous subject) — on the pilot, the
// pilot message that replaces it (D5).
func (r *Repairer) cont(row *Row, pid int) error {
	text := ContinueText
	if r.Pilot != "" && sameSurface(row.SurfaceID, r.Pilot) {
		text = PilotTextCut()
	}
	return r.typeConfirmed(row, pid, text, pid != row.PID)
}

// typeConfirmed types text once the session sits idle with an empty input,
// presses Enter only on that exact text read back, then checks the turn
// started. started: the check launched this claude (light gate); else the
// full one (revue 1 #4). A pilot text goes through the keyboard focus and is
// never erased (D9): a wrong read-back may hold a keystroke of the user.
func (r *Repairer) typeConfirmed(row *Row, pid int, text string, started bool) error {
	cfg := cfgOf(row)
	pilot := text == PilotText() || text == PilotTextCut()
	ready := false
	status, draft, found := "", "", false
	for i := 0; i <= int(r.ReadyTimeout/r.Poll); i++ {
		if err := r.dialog(row.SurfaceID); err != nil {
			return err // T-h: never typed into a dialog
		}
		n, ok := r.D.Native(cfg, pid)
		status = ""
		if ok {
			status = n.Status
		}
		draft, found = r.draft(row.SurfaceID)
		if status == "idle" && found && draft == "" {
			ready = true
			break
		}
		r.Sleep(r.Poll)
	}
	if !ready {
		if status != "idle" { // revue 1 #4: an occupation — the second pass retries
			return &busyError{Reasons: []string{"status " + orUnknown(status)}}
		}
		if !found { // revue PR B M4
			return errors.New("no input box on screen (status idle)")
		}
		return fmt.Errorf("draft in the input (%q)", draft)
	}
	if err := r.gate(row, pid, gateOpts{full: !started, focusOK: pilot}); err != nil {
		return err
	}
	if err := r.Send(row.SurfaceID, text); err != nil {
		return err
	}
	r.Sleep(300 * time.Millisecond)
	if d, _ := r.draft(row.SurfaceID); !sameText(d, text) {
		if pilot {
			return &leftInInputError{why: fmt.Sprintf("read back as %q", d)}
		}
		_ = r.Send(row.SurfaceID, "\x15")
		return fmt.Errorf("continue text not found in the input (%q): Enter not pressed", d)
	}
	if err := r.gate(row, pid, gateOpts{focusOK: pilot}); err != nil {
		if pilot { // D9, T-b: the pilot message never gets \x15
			// Not a busyError on purpose: a busy refusal here is no
			// "pending" — the text sits in the input, a second pass would
			// see a draft.
			why := err.Error()
			var be *busyError
			if errors.As(err, &be) {
				why = strings.Join(be.Reasons, ", ")
			}
			return &leftInInputError{why: why}
		}
		_ = r.Send(row.SurfaceID, "\x15") // the check's own text (T-b exception)
		return err
	}
	// Revue PR B M2: read the input again right before Enter — a key typed
	// since the read-back (the pilot text goes through the focus, D9) would
	// be submitted with it.
	if d, _ := r.draft(row.SurfaceID); !sameText(d, text) {
		if pilot {
			return &leftInInputError{why: fmt.Sprintf("the input changed before Enter: %q", d)}
		}
		_ = r.Send(row.SurfaceID, "\x15")
		return fmt.Errorf("the input changed before Enter (%q): Enter not pressed", d)
	}
	if err := r.Send(row.SurfaceID, "\r"); err != nil {
		return err
	}
	if pilot {
		row.messageSent = true // I1: from here on, the message is typed
	}
	for i := 0; i <= int(r.ReadyTimeout/r.Poll); i++ {
		if n, ok := r.D.Native(cfg, pid); ok && n.Status != "idle" {
			return nil
		}
		r.Sleep(r.Poll)
	}
	return fmt.Errorf("%w within %s", errSentNoTurn, r.ReadyTimeout)
}

// sameText: the input draft reads the typed text exactly, whitespace and
// wrapping aside (InputDraft joins wrapped lines with "\n", D10).
func sameText(draft, text string) bool {
	return strings.Join(strings.Fields(draft), " ") == text
}

// leftInInputError: a pilot text typed but Enter not pressed — it stays in
// the input, never erased (D9); no "pending", since a second pass would read
// a draft. Its line is the report's whole message (revue PR B M3).
type leftInInputError struct{ why string }

func (e *leftInInputError) Error() string {
	return "left in the input, Enter not pressed (" + e.why + ") — never erased (D9)"
}

// PilotMessage types PilotText into the pilot after its repairs (D5), unless
// the continue already carried it — through the keyboard focus when its
// input reads empty (D9). pending: an occupation stopped it (busy, a turn
// still running) — the second pass retries; otherwise a refusal stays. A
// pilot in Standby gets nothing (D16).
func (r *Repairer) PilotMessage(row *Row) (msg string, typed, pending bool) {
	if row.standby { // D16: no text into a session in Standby, nor a second pass
		return "not typed: the pilot is in Standby (D16)", false, false
	}
	for _, a := range row.Done {
		if a == ActContinue {
			return "typed with the continue", true, false
		}
	}
	if row.messageSent { // I1: Enter went out on PilotTextCut(), no turn seen
		return "typed with the continue (" + errSentNoTurn.Error() + ")", true, false
	}
	switch {
	case row.Self:
		return "not typed: the pilot runs this check", false, false
	case row.PID <= 0:
		return "not typed: no live claude on the pilot surface", false, false
	case row.Blocked == "busy: "+focusReason:
		// Revue PR B M1, D9: the repair waits for the focus (the second
		// pass), the message alone may go through it — its input read empty
		// below, never \x15.
	case busyBlocked(row.Blocked): // revue 1 #4: a healthy row is not Remaining — read Blocked itself
		return "pending: " + row.Blocked, false, true
	case row.Remaining || row.Blocked != "":
		return "not typed: the pilot's row is left (" + firstNonEmpty(row.Blocked, row.Result) + ")", false, false
	}
	if d, ok := r.draft(row.SurfaceID); !ok {
		return "not typed: no input box on the pilot's screen", false, false // revue PR B M4
	} else if d != "" {
		return fmt.Sprintf("not typed: the pilot's input is not empty (%q)", d), false, false
	}
	started := false
	for _, a := range row.Done {
		started = started || a == ActRelaunch || a == ActStart || a == ActQuit
	}
	err := r.typeConfirmed(row, row.PID, PilotText(), started)
	var be *busyError
	switch {
	case err == nil:
		return "typed", true, false
	case row.messageSent: // I1: Enter went out — typed, whatever the turn-start read says
		return "typed (" + err.Error() + ")", true, false
	case errors.As(err, new(*leftInInputError)): // M3: the line says it all
		return err.Error(), false, false
	case errors.As(err, &be):
		return "pending: busy: " + strings.Join(be.Reasons, ", "), false, true
	default:
		return "not typed: " + err.Error(), false, false
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (r *Repairer) tail(surface string) string {
	s, err := r.D.Screen(surface)
	if err != nil {
		return "(screen unreadable)"
	}
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, " ⏎ ")
}

// fixResume runs ActResume (sub_x000000000000024) and files its outcome on
// the row: a guard → Blocked; the input back → nothing done; the new claude
// not seen → the line to type by hand, the old tab kept.
func (r *Repairer) fixResume(row *Row, pid int) {
	if !row.named {
		row.Blocked, row.Remaining = refusedNotNamed, true
		return
	}
	npid, err := r.resume(row, pid)
	var (
		be *blockedError
		ns *notSeenError
		tn *tabNotOpenedError
		bu *busyError
		sa *stillAliveError
	)
	// The errors after the stop come first: their cause may wrap a guard's
	// error (a busy gate on the new tab), never to be read as « nothing done ».
	switch {
	case err == nil:
		row.Done = append(row.Done, ActResume)
		row.PID, row.Result, row.Remaining = npid, "repaired", false
	case errors.As(err, &sa):
		row.Result, row.Remaining = sa.Error(), true
		if sa.cause != nil {
			row.Info = append(row.Info, sa.cause.Error())
		}
	case errors.As(err, &tn):
		row.Result, row.Remaining, row.PID = tn.Error(), true, 0
		row.Info = append(row.Info, tn.cause.Error())
	case errors.As(err, &ns):
		row.Result, row.Remaining = ns.Error(), true
		if ns.cause != nil {
			row.Info = append(row.Info, ns.cause.Error())
		}
		row.PID = npid
	case errors.Is(err, errInputBack):
		row.Result, row.Remaining = resultInputBack, false
	case errors.Is(err, errProbeUnknown):
		row.Result, row.Remaining = resultProbeInconclusive, true
	case errors.As(err, &be):
		row.Blocked, row.Remaining = be.why, true
	case errors.As(err, &bu):
		row.Blocked, row.Remaining = "busy: "+strings.Join(bu.Reasons, ", "), true
	default:
		row.Result = fmt.Sprintf("%s failed: %v", ActResume, err)
		row.Remaining = true
	}
}
