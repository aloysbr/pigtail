package cmuxcheck

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

func pilotWorld(t *testing.T) (*world, *pane) {
	w := newWorld()
	w.add(t, 13, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{13: true}}
	return w, p
}

// D5: a healthy pilot gets PilotText, read back exactly, then Enter.
func TestPilotMessageTyped(t *testing.T) {
	w, p := pilotWorld(t)
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	rep := Run(w.deps(), repairer(w, p), o)
	if strings.Join(p.sent, "|") != PilotText()+"|\r" || !rep.Pilot.MessageTyped || rep.Pilot.Message != "typed" {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// D5: the turn was cut — PilotTextCut replaces the continue, typed once.
func TestPilotMessageReplacesContinue(t *testing.T) {
	w, p := pilotWorld(t)
	rep := repairer(w, p)
	rep.Pilot = "S-A"
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 13, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a",
		Actions: []Action{ActContinue}, Problems: []string{"turn cut by the relaunch"}}
	rep.Fix(&row)
	if strings.Join(p.sent, "|") != PilotTextCut()+"|\r" || row.Remaining {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
	if msg, typed, _ := rep.PilotMessage(&row); !typed || !strings.Contains(msg, "with the continue") {
		t.Fatalf("PilotMessage after the cut continue: %q %v", msg, typed)
	}
	if strings.Count(strings.Join(p.sent, "|"), "cmux a redémarré") != 1 {
		t.Fatalf("typed twice: %q", p.sent)
	}
}

// Review Focus 5, T-i: a second pass of the same launch does not type it again.
func TestPilotMessageOnce(t *testing.T) {
	w, p := pilotWorld(t)
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	o.PilotMsg = &PilotMsgState{LaunchPID: simCmuxPID, SurfaceID: "S-A", At: tNow}
	rep := Run(w.deps(), repairer(w, p), o)
	if len(p.sent) != 0 || !rep.Pilot.MessageTyped || !strings.Contains(rep.Pilot.Message, "already typed by an earlier pass") {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// D9: the pilot holds the keyboard focus — the message alone is typed
// through it, its input reading empty.
func TestPilotMessageThroughFocus(t *testing.T) {
	w, p := pilotWorld(t)
	w.focused = "S-A"
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	rep := Run(w.deps(), repairer(w, p), o)
	if strings.Join(p.sent, "|") != PilotText()+"|\r" || !rep.Pilot.MessageTyped {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// D9: the message read back altered (a keystroke mixed in) — never \x15,
// no Enter; the text stays in the input and the report says so.
func TestPilotMessageNoEraseOnMismatch(t *testing.T) {
	w, p := pilotWorld(t)
	p.mangle = true
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	rep := Run(w.deps(), repairer(w, p), o)
	if strings.Join(p.sent, "|") != PilotText() || rep.Pilot.MessageTyped || rep.Pilot.MessagePending ||
		!strings.Contains(rep.Pilot.Message, "left in the input") {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// Revue 1 #4: a healthy pilot in the middle of a turn — pending (the second
// pass retries it), never a plain "not typed"; BusyLeft lists it in pilot mode.
func TestPilotMessagePendingWhenBusy(t *testing.T) {
	w, p := pilotWorld(t)
	n := w.native[13]
	n.Status = "busy"
	w.native[13] = n
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	rep := Run(w.deps(), repairer(w, p), o)
	if len(p.sent) != 0 || !rep.Pilot.MessagePending || !strings.HasPrefix(rep.Pilot.Message, "pending: busy") {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
	if got := rep.BusyLeft(simCmuxPID, true); len(got) != 1 || !sameSurface(got[0], "S-A") {
		t.Fatalf("BusyLeft = %v, want the pilot", got)
	}
}

// D10, revue 1 #13: PilotTextCut (≈140 runes) wraps over two lines at a
// frozen width of about 80 columns; the read-back (InputDraft joins with
// "\n", typeConfirmed compares by strings.Fields) still matches, Enter goes.
func TestPilotMessageWrappedReadsBack(t *testing.T) {
	w, p := pilotWorld(t)
	p.wrap = 80
	rep := repairer(w, p)
	rep.Pilot = "S-A"
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 13, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a",
		Actions: []Action{ActContinue}, Problems: []string{"turn cut by the relaunch"}}
	rep.Fix(&row)
	if strings.Join(p.sent, "|") != PilotTextCut()+"|\r" || row.Remaining {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// A draft in the pilot's input: never typed over — not typed, not pending.
func TestPilotMessageNotOverADraft(t *testing.T) {
	w, p := pilotWorld(t)
	p.draft = "my own words"
	w.screens["S-A"] = p.screenFor()
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	rep := Run(w.deps(), repairer(w, p), o)
	if len(p.sent) != 0 || rep.Pilot.MessageTyped || rep.Pilot.MessagePending || !strings.HasPrefix(rep.Pilot.Message, "not typed: ") {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// T-i: an inventory pass (no --fix) of the same launch keeps the "typed"
// flag — last.json it writes must not let the second pass type it again.
func TestPilotMessageTypedKeptByInventory(t *testing.T) {
	w, p := pilotWorld(t)
	o := runOpts(t)
	o.MaintenanceOwner = "sid-a"
	o.PilotMsg = &PilotMsgState{LaunchPID: simCmuxPID, SurfaceID: "S-A", At: tNow}
	rep := Run(w.deps(), repairer(w, p), o)
	if len(p.sent) != 0 || !rep.Pilot.MessageTyped {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// Revue PR B I1: Enter went out on the pilot message but no turn was seen
// (the native status lags, or the turn ended between two reads) — the message
// counts as typed, never typed again, and the report says the turn was not seen.
func TestPilotMessageEnterWithoutTurnCountsTyped(t *testing.T) {
	w, p := pilotWorld(t)
	p.noTurn = true
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	rep := Run(w.deps(), repairer(w, p), o)
	if strings.Join(p.sent, "|") != PilotText()+"|\r" || !rep.Pilot.MessageTyped || rep.Pilot.MessagePending ||
		!strings.Contains(rep.Pilot.Message, "no turn seen") {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// Revue PR B I1: the same for the pilot's continue (PilotTextCut) — Enter
// pressed, no turn seen: PilotMessage counts it typed and types nothing more.
func TestPilotContinueEnterWithoutTurnCountsTyped(t *testing.T) {
	w, p := pilotWorld(t)
	p.noTurn = true
	rep := repairer(w, p)
	rep.Pilot = "S-A"
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 13, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a",
		Actions: []Action{ActContinue}, Problems: []string{"turn cut by the relaunch"}}
	rep.Fix(&row)
	msg, typed, pending := rep.PilotMessage(&row)
	if strings.Join(p.sent, "|") != PilotTextCut()+"|\r" || !typed || pending || !strings.Contains(msg, "no turn seen") {
		t.Fatalf("typed %q, PilotMessage %q typed=%v pending=%v", p.sent, msg, typed, pending)
	}
}

// Revue PR B M2: the user types a key between the read-back and Enter (the
// pilot text goes through the focus, D9) — the input is read again right
// before Enter: no longer the exact text, no Enter, never erased, and why.
func TestPilotMessageNoEnterWhenInputChangesBeforeEnter(t *testing.T) {
	w, p := pilotWorld(t)
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	r := repairer(w, p)
	native := r.D.Native
	r.D.Native = func(cfg string, pid int) (claudesession.Native, bool) {
		if p.draft == PilotText() { // the light gate's read, after the read-back
			p.draft += "k"
			w.screens["S-A"] = p.screenFor()
		}
		return native(cfg, pid)
	}
	rep := Run(w.deps(), r, o)
	if strings.Join(p.sent, "|") != PilotText() || rep.Pilot.MessageTyped || rep.Pilot.MessagePending ||
		!strings.Contains(rep.Pilot.Message, "changed before Enter") || !strings.Contains(rep.Pilot.Message, "left in the input") {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// Revue PR B M2: the same guard on the plain continue — no Enter on an input
// that no longer reads the exact text.
func TestContinueNoEnterWhenInputChangesBeforeEnter(t *testing.T) {
	w, p := pilotWorld(t)
	r := repairer(w, p)
	native := r.D.Native
	r.D.Native = func(cfg string, pid int) (claudesession.Native, bool) {
		if p.draft == ContinueText {
			p.draft += "k"
			w.screens["S-A"] = p.screenFor()
		}
		return native(cfg, pid)
	}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 13, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a",
		Actions: []Action{ActContinue}, Problems: []string{"turn cut by the relaunch"}}
	r.Fix(&row)
	if strings.Contains(strings.Join(p.sent, "|"), "\r") || !row.Remaining || !strings.Contains(row.Result, "changed before Enter") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// Revue PR B M3: the gate refuses Enter after the read-back — one plain
// line: left in the input, Enter not pressed, and the gate's own reason.
func TestPilotMessageGateRefusalWording(t *testing.T) {
	w, p := pilotWorld(t)
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	r := repairer(w, p)
	native := r.D.Native
	r.D.Native = func(cfg string, pid int) (claudesession.Native, bool) {
		n, ok := native(cfg, pid)
		if p.draft == PilotText() {
			n.Status = "busy" // a turn started under the typed text
		}
		return n, ok
	}
	rep := Run(w.deps(), r, o)
	if strings.Join(p.sent, "|") != PilotText() || rep.Pilot.MessageTyped || rep.Pilot.MessagePending ||
		rep.Pilot.Message != "left in the input, Enter not pressed (status busy) — never erased (D9)" {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
}

// Revue PR B M4: idle with no input box on screen — said as such, never
// "draft in the input (\"\")".
func TestContinueNoInputBoxWording(t *testing.T) {
	w, p := pilotWorld(t)
	w.screens["S-A"] = "some output\nno box here\n"
	r := repairer(w, p)
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 13, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a",
		Actions: []Action{ActContinue}, Problems: []string{"turn cut by the relaunch"}}
	r.Fix(&row)
	if len(p.sent) != 0 || !strings.Contains(row.Result, "no input box on screen") || strings.Contains(row.Result, "draft in the input") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
	if msg, typed, _ := r.PilotMessage(&Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 13, ConfigDir: "/cfg/a"}); typed ||
		!strings.Contains(msg, "no input box") {
		t.Fatalf("PilotMessage = %q", msg)
	}
}

// D13 (revue PR B I3): only a pass pigtaild starts (--unattended) types the
// pilot message; a human's or an agent's --fix / --fix-pilot repairs the
// pilot but never types it — and says why.
func TestPilotMessageOnlyUnattended(t *testing.T) {
	for _, pilotOnly := range []bool{true, false} {
		w, p := pilotWorld(t)
		o := runOpts(t)
		o.Fix, o.PilotOnly, o.MaintenanceOwner = true, pilotOnly, "sid-a"
		rep := Run(w.deps(), repairer(w, p), o)
		if len(p.sent) != 0 || rep.Pilot.MessageTyped || rep.Pilot.MessagePending ||
			!strings.Contains(rep.Pilot.Message, "D13") {
			t.Fatalf("pilotOnly=%v: typed %q, pilot %+v", pilotOnly, p.sent, rep.Pilot)
		}
	}
}

// D13: the pilot's cut turn under a manual --fix gets the plain continue,
// never PilotTextCut.
func TestPilotContinuePlainWhenAttended(t *testing.T) {
	w, p := pilotWorld(t)
	r := repairer(w, p)
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner = true, true, "sid-a"
	Run(w.deps(), r, o)
	if r.Pilot != "" || strings.Contains(strings.Join(p.sent, "|"), "cmux a redémarré") {
		t.Fatalf("attended pass: r.Pilot %q, typed %q", r.Pilot, p.sent)
	}
}

// focusPilotWorld: the pilot needs its relaunch (resumed by cmux) and holds
// the keyboard focus — the gate refuses /exit (D9).
func focusPilotWorld(t *testing.T) (*world, *pane) {
	w := newWorld()
	w.add(t, 40, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", ts("2026-10-08T10:31:20Z"), "")
	w.focused = "S-A"
	return w, &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{40: true}, exitOK: true}
}

// Revue PR B M1, D9: the pilot's repair refused for the keyboard focus — the
// message still goes through the focus in the same pass (input read empty,
// never \x15), and a first pigtaild pass names the second one.
func TestPilotMessageThroughFocusWhenRepairRefused(t *testing.T) {
	w, p := focusPilotWorld(t)
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
	rep := Run(w.deps(), repairer(w, p), o)
	if strings.Join(p.sent, "|") != PilotText()+"|\r" || !rep.Pilot.MessageTyped || rep.Pilot.MessagePending {
		t.Fatalf("typed %q, pilot %+v", p.sent, rep.Pilot)
	}
	want := "pilot holds keyboard focus: click another pane — the second pass retries in " + SecondPassDelay.String()
	if !strings.Contains(strings.Join(rep.Notes, "|"), want) {
		t.Fatalf("notes %v, want %q", rep.Notes, want)
	}
	if got := rep.BusyLeft(simCmuxPID, true); len(got) != 1 {
		t.Fatalf("BusyLeft = %v: the repair itself waits for the second pass", got)
	}
}

// Revue PR B M1: no retry claim where no second pass follows — pigtaild's own
// second pass (--surface), or a pass a human or an agent runs.
func TestPilotFocusNoteNoRetryClaim(t *testing.T) {
	for name, set := range map[string]func(*Options){
		"second pass": func(o *Options) { o.Unattended, o.Surfaces = true, []string{"S-A"} },
		"by hand":     func(o *Options) {},
	} {
		w, p := focusPilotWorld(t)
		o := runOpts(t)
		o.Fix, o.PilotOnly, o.MaintenanceOwner = true, true, "sid-a"
		set(&o)
		rep := Run(w.deps(), repairer(w, p), o)
		notes := strings.Join(rep.Notes, "|")
		if !strings.Contains(notes, "pilot holds keyboard focus: click another pane") || strings.Contains(notes, "retries") {
			t.Fatalf("%s: notes %v", name, rep.Notes)
		}
	}
}

// Revue PR B I2: the once-per-launch state is keyed by the cmux launch — a
// state of an earlier launch (same pid reused, typed before this launch
// started) does not hold the message back.
func TestPilotMessageStateOfAnotherLaunch(t *testing.T) {
	for name, st := range map[string]PilotMsgState{
		"other pid":        {LaunchPID: simCmuxPID + 1, SurfaceID: "S-A", At: tNow},
		"before the start": {LaunchPID: simCmuxPID, SurfaceID: "S-A", At: tNow.Add(-30 * 24 * time.Hour)},
	} {
		w, p := pilotWorld(t)
		o := runOpts(t)
		o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
		o.PilotMsg = &st
		rep := Run(w.deps(), repairer(w, p), o)
		if strings.Join(p.sent, "|") != PilotText()+"|\r" || rep.Pilot.Message != "typed" {
			t.Fatalf("%s: typed %q, pilot %+v", name, p.sent, rep.Pilot)
		}
	}
}

// Revue PR B I2: the state file round-trips; none is fs.ErrNotExist.
func TestPilotMsgStateFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadPilotMsg(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no file: %v", err)
	}
	st := PilotMsgState{LaunchPID: 7, SurfaceID: "S-A", At: tNow}
	if err := SavePilotMsg(dir, st); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPilotMsg(dir)
	if err != nil || got.LaunchPID != 7 || got.SurfaceID != "S-A" || !got.At.Equal(tNow) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, PilotMsgFile)); err != nil {
		t.Fatal(err)
	}
}

// D16: a healthy pilot in Standby gets no PilotText — not pending, not
// typed; out of Standby it is typed (control, TestPilotMessageTyped).
func TestPilotMessageSkipsStandby(t *testing.T) {
	for _, standby := range []bool{true, false} {
		w, p := pilotWorld(t)
		o := runOpts(t)
		o.Fix, o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, true, "sid-a", true
		o.InStandby = func(sid string) (bool, error) { return standby && sid == "sid-a", nil }
		rep := Run(w.deps(), repairer(w, p), o)
		if standby {
			if len(p.sent) != 0 || rep.Pilot.MessageTyped || rep.Pilot.MessagePending || rep.Pilot.Message != "not typed: the pilot is in Standby (D16)" {
				t.Fatalf("in Standby: typed %q, pilot %+v", p.sent, rep.Pilot)
			}
			continue
		}
		if strings.Join(p.sent, "|") != PilotText()+"|\r" || !rep.Pilot.MessageTyped {
			t.Fatalf("out of Standby: typed %q, pilot %+v", p.sent, rep.Pilot)
		}
	}
}
