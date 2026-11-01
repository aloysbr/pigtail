package cmuxcheck

import (
	"errors"
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// maxCtrlC bounds the sequence (D1, D12): one press may only clear an input
// (a draft, the restore line of the D2 rescue), one arms ("Press Ctrl-C
// again to exit"), one confirms. Nothing is ever pressed after the
// confirmation (D12): without an input to clear, two presses.
const maxCtrlC = 3

type pressState int

const (
	pressNothing pressState = iota // no footer: an input cleared (allowed once)
	pressArmed                     // the footer under the box
	pressGone                      // the pid ended before any confirmation
	pressDialog                    // "Background work is running" (T-i)
)

// errMissedWindow (D12): the confirming press landed after Claude's
// double-press window — the footer went, then was drawn again: claude
// re-armed instead of quitting. Never a further press.
var errMissedWindow = errors.New("missed Claude's Ctrl+C window: quit by hand")

// quit stops an idle claude by Ctrl+C (D1, D2; measured F8): each press
// behind the gate (full before the first; light after, without re-reading
// the dialog afterPress just read — the confirmation window is short), each
// read back. A press that shows the footer arms the next one, the
// confirmation, which is the last press whatever follows (D12): afterConfirm
// waits for the pid to end and the pane's shell to come back, or stops.
// Anything unexpected stops the sequence and nothing more is typed. Never a
// signal (TestNoSignalSent): \x03 is a byte cmux types.
func (r *Repairer) quit(row *Row, pid int) error {
	if err := r.notPreArmed(row.SurfaceID); err != nil {
		return err
	}
	var t ctrlCTiming
	defer t.note(r, row)
	armed := false
	for press := 1; press <= maxCtrlC; press++ {
		// Revue 2 N1: once armed, the pid is read before the gate — a claude
		// that ended would read "busy: status unknown" there (its native file
		// gone), and the row would hide what happened.
		if armed && !r.PidAlive(pid) {
			return fmt.Errorf("claude pid %d exited after the %q footer, before the confirming press: unexpected, start line not typed", pid, claudesession.CtrlCAgainText)
		}
		if err := r.gate(row, pid, gateOpts{full: press == 1, noDialog: press > 1}); err != nil {
			return err
		}
		if !r.PidAlive(pid) {
			return fmt.Errorf("claude pid %d gone before Ctrl+C press %d: nothing more typed", pid, press)
		}
		if err := r.Send(row.SurfaceID, "\x03"); err != nil {
			return err
		}
		if armed { // D12: the confirmation is the last press
			t.confirm = r.now()
			return r.afterConfirm(row.SurfaceID, pid, &t)
		}
		t.press = r.now()
		switch r.afterPress(row.SurfaceID, pid) {
		case pressDialog:
			return r.cancelOwnExitDialog(row.SurfaceID, "Ctrl+C")
		case pressGone:
			return fmt.Errorf("claude pid %d exited on Ctrl+C press %d, before any %q footer: unexpected, start line not typed", pid, press, claudesession.CtrlCAgainText)
		case pressArmed:
			armed, t.footer = true, r.now()
		default:
			if press >= 2 {
				return fmt.Errorf("no %q footer after %d Ctrl+C presses: stopped, nothing more typed", claudesession.CtrlCAgainText, press)
			}
		}
	}
	// Unreachable: press 2 without a footer stops, a confirmation returns.
	return fmt.Errorf("claude pid %d: Ctrl+C sequence ended after %d presses", pid, maxCtrlC)
}

// ctrlCTiming (revue 2 M9): the real latencies of this path — arming press
// → footer read → confirmation sent → pid gone —, put on the row for the N2
// acceptance (item 3 compares them with Claude's window). No line without a
// clock (Repairer.Now) or without an arm.
type ctrlCTiming struct{ press, footer, confirm, exit time.Time }

func (t *ctrlCTiming) note(r *Repairer, row *Row) {
	if r.Now == nil || t.footer.IsZero() {
		return
	}
	d := func(a, b time.Time) string {
		if a.IsZero() || b.IsZero() {
			return "-"
		}
		return b.Sub(a).Round(time.Millisecond).String()
	}
	row.Info = append(row.Info, fmt.Sprintf("Ctrl+C timing: footer %s after the arming press, confirmation %s after the footer, pid gone %s after it",
		d(t.press, t.footer), d(t.footer, t.confirm), d(t.confirm, t.exit)))
}

// now: the Repairer's clock, the zero time without one (tests).
func (r *Repairer) now() time.Time {
	if r.Now == nil {
		return time.Time{}
	}
	return r.Now()
}

// notPreArmed (revue 1 #8): a footer drawn before the first press is not the
// check's arm — an earlier pass stopped after one press, and a press now
// would exit at once. Wait the window out once and re-read; still there =
// nothing typed.
func (r *Repairer) notPreArmed(surface string) error {
	for i := 0; i < 2; i++ {
		s, err := r.D.Screen(surface)
		if err != nil {
			return fmt.Errorf("screen unreadable before the first Ctrl+C (%w): nothing typed", err)
		}
		if !claudesession.CtrlCAgainShown(s) {
			return nil
		}
		if i == 0 {
			r.Sleep(r.CtrlCWait)
		}
	}
	return fmt.Errorf("%q footer already shown before the first Ctrl+C, still there after %s: nothing typed", claudesession.CtrlCAgainText, r.CtrlCWait)
}

// afterPress reads the pane every CtrlCPoll, up to CtrlCWait, after a press
// that is not a confirmation: the first footer seen arms. Polls are counted,
// not timed, so tests with a no-op Sleep terminate.
func (r *Repairer) afterPress(surface string, pid int) pressState {
	for i := 0; i <= int(r.CtrlCWait/r.CtrlCPoll); i++ {
		r.Sleep(r.CtrlCPoll)
		if !r.PidAlive(pid) {
			return pressGone
		}
		s, err := r.D.Screen(surface)
		if err != nil {
			continue
		}
		if exitDialogShown(s) {
			return pressDialog
		}
		if claudesession.CtrlCAgainShown(s) {
			return pressArmed
		}
	}
	return pressNothing
}

// afterConfirm reads the pane every CtrlCPoll, up to CtrlCExitWait (revue 2
// N1: F8 measured the exit at ≤ 2.1 s, longer than CtrlCWait), after the
// confirming press — never followed by another (D12). The pid ending wins,
// then the pane's shell is awaited; a dialog is the check's own (T-i). A
// footer still drawn while claude exits means nothing: only a footer seen
// gone, then drawn again, is a confirmation that missed Claude's window and
// re-armed — stop (D12). A pid still alive at the end: waitExit
// (ExitTimeout), never a press.
func (r *Repairer) afterConfirm(surface string, pid int, t *ctrlCTiming) error {
	gone := false
	for i := 0; i <= int(r.CtrlCExitWait/r.CtrlCPoll); i++ {
		r.Sleep(r.CtrlCPoll)
		if !r.PidAlive(pid) {
			t.exit = r.now()
			return r.waitShell(surface)
		}
		s, err := r.D.Screen(surface)
		if err != nil {
			continue
		}
		switch {
		case exitDialogShown(s):
			return r.cancelOwnExitDialog(surface, "Ctrl+C")
		case !claudesession.CtrlCAgainShown(s):
			gone = true
		case gone:
			return fmt.Errorf("claude pid %d re-armed on the confirming press: %w", pid, errMissedWindow)
		}
	}
	if err := r.waitExit(surface, pid, "Ctrl+C"); err != nil {
		return err
	}
	t.exit = r.now()
	return r.waitShell(surface)
}

// waitShell waits for the pane's own shell at its prompt (shellIdle, D22)
// after claude exited — a launcher that keeps the pane is not a prompt.
func (r *Repairer) waitShell(surface string) error {
	for i := 0; i <= int(r.ShellTimeout/r.Poll); i++ {
		if idle, err := shellIdle(r.D, surface, r.CmuxPID); err == nil && idle {
			return nil
		}
		r.Sleep(r.Poll)
	}
	return fmt.Errorf("the pane did not come back to its shell prompt within %s after the quit (a launcher may hold it): start line not typed", r.ShellTimeout)
}
