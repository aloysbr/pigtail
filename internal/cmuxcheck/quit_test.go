package cmuxcheck

import (
	"strings"
	"testing"
	"time"
)

// quitRow: S-A, pid 40 idle at frozen width (screen garbled, no box), its
// binding's start line ready; a typed line starts pid 41.
func quitRow(t *testing.T) (*world, *pane, Row) {
	w := newWorld()
	w.add(t, 40, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{40: true}, garbled: frozenScreen(t),
		spawnPID: 41, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	w.screens["S-A"] = p.screenFor()
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 40, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActQuit}, Line: " export CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7; claude --resume sid-a --permission-mode auto",
		Problems: []string{"screen unreadable (frozen width): quit by Ctrl+C, then started again"}}
	return w, p, row
}

// F8, measured: two presses, the footer between them, pid gone, shell back,
// the start line, repaired on the new pid.
func TestQuitTwoPressesThenStart(t *testing.T) {
	w, p, row := quitRow(t)
	repairer(w, p).Fix(&row)
	want := []string{"\x03", "\x03", row.Line + "\n"}
	if strings.Join(p.sent, "|") != strings.Join(want, "|") {
		t.Fatalf("typed %q, want %q", p.sent, want)
	}
	if row.Remaining || row.PID != 41 || row.Category() != CatRepaired || len(row.Done) != 1 || row.Done[0] != ActQuit {
		t.Fatalf("row %+v (%s), want repaired on pid 41", row, row.Category())
	}
}

// F9 (guarded, measured at N2): a draft in the unreadable input — the first
// press clears it (no footer), the second arms, the third confirms.
func TestQuitDraftThreePresses(t *testing.T) {
	w, p, row := quitRow(t)
	p.draft = "half a sentence"
	repairer(w, p).Fix(&row)
	if strings.Count(strings.Join(p.sent, ""), "\x03") != 3 || row.Remaining {
		t.Fatalf("typed %q, row %+v; want three presses then the start line", p.sent, row)
	}
}

// Review Focus 3: claude ignores Ctrl+C — two presses with no footer, stop.
func TestQuitIgnoredStops(t *testing.T) {
	w, p, row := quitRow(t)
	p.ctrlCIgnored = true
	repairer(w, p).Fix(&row)
	if strings.Join(p.sent, "|") != "\x03|\x03" || !row.Remaining || !strings.Contains(row.Result, "no \"Press Ctrl-C again to exit\" footer after 2 Ctrl+C presses") || !p.alive[40] {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// Review Focus 3: the pid ends on the first press — a contradiction: the
// start line is not typed, the row says why.
func TestQuitExitOnFirstPressNotStarted(t *testing.T) {
	w, p, row := quitRow(t)
	p.exitOnFirst = true
	repairer(w, p).Fix(&row)
	if strings.Join(p.sent, "|") != "\x03" || !row.Remaining || !strings.Contains(row.Result, "exited on Ctrl+C press 1") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// Review Focus 2: focus moves onto the pane after the first press — no
// second press, the row busy.
func TestQuitStopsWhenFocusArrives(t *testing.T) {
	w, p, row := quitRow(t)
	rep := repairer(w, p)
	calls := 0
	rep.D.Focused = func() (string, error) {
		calls++
		if calls > 1 {
			return "S-A", nil
		}
		return "", nil
	}
	rep.Fix(&row)
	if strings.Join(p.sent, "|") != "\x03" || row.Blocked != "busy: pane has keyboard focus" || !p.alive[40] {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// F9 (guarded): a launcher holds the pane after the exit — the shell never
// comes back, the start line is not typed.
func TestQuitLauncherPaneNotStarted(t *testing.T) {
	w, p, row := quitRow(t)
	p.launcher = true
	repairer(w, p).Fix(&row)
	if strings.Count(strings.Join(p.sent, ""), "\x03") != 2 || strings.Contains(strings.Join(p.sent, ""), "export") ||
		!strings.Contains(row.Result, "did not come back to its shell prompt") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// D1: busy before the first press (a turn runs: native status) — nothing typed.
func TestQuitBusyTypesNothing(t *testing.T) {
	w, p, row := quitRow(t)
	n := w.native[40]
	n.Status = "busy"
	w.native[40] = n
	repairer(w, p).Fix(&row)
	if len(p.sent) != 0 || !strings.HasPrefix(row.Blocked, "busy: ") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// Review Focus 6, D12 (revue 2 N2): the confirmation lands after Claude's
// window — the footer goes, then is drawn again (re-armed). The sequence
// stops there: no third press, the pid untouched, no start line.
func TestQuitLateConfirmStops(t *testing.T) {
	w, p, row := quitRow(t)
	p.lateConfirm = true
	rep := repairer(w, p)
	read := rep.D.Screen
	rep.D.Screen = func(s string) (string, error) {
		out, err := read(s)
		if p.rearm { // this read showed no footer; Claude draws it again for the next one
			p.rearm, p.armed = false, true
			w.screens[s] = p.screenFor()
		}
		return out, err
	}
	rep.Fix(&row)
	if strings.Join(p.sent, "|") != "\x03|\x03" || !row.Remaining || !p.alive[40] ||
		!strings.Contains(row.Result, "missed Claude's Ctrl+C window: quit by hand") {
		t.Fatalf("typed %q, row %+v; want two presses, then stop", p.sent, row)
	}
}

// Review Focus 6 (revue 2 N1): claude keeps the footer drawn while it exits
// and its pid ends after more reads than CtrlCWait/CtrlCPoll — still the
// measured F8 exit (≤ 2.1 s), read within CtrlCExitWait: two presses, then
// the start line; never taken for a re-arm, never a third press.
func TestQuitSlowExitFooterStays(t *testing.T) {
	w, p, row := quitRow(t)
	rep := repairer(w, p)
	p.slowExit = int(rep.CtrlCWait/rep.CtrlCPoll) + 3
	rep.Fix(&row)
	want := []string{"\x03", "\x03", row.Line + "\n"}
	if strings.Join(p.sent, "|") != strings.Join(want, "|") || row.Remaining || row.PID != 41 {
		t.Fatalf("typed %q (want %q), row %+v", p.sent, want, row)
	}
}

// Revue 2 M9: the real latencies of the path (arming press → footer read →
// confirmation sent → pid gone) go on the row, for N2 item 3 to compare
// with Claude's window.
func TestQuitTimingInfo(t *testing.T) {
	w, p, row := quitRow(t)
	rep := repairer(w, p)
	clock := tNow
	rep.Now = func() time.Time { clock = clock.Add(10 * time.Millisecond); return clock }
	rep.Fix(&row)
	want := "Ctrl+C timing: footer 10ms after the arming press, confirmation 10ms after the footer, pid gone 10ms after it"
	if row.Remaining || !strings.Contains(strings.Join(row.Info, " | "), want) {
		t.Fatalf("row %+v; want info %q", row, want)
	}
}

// Review Focus 6 (revue 1 #8): the footer is drawn before the first press
// (an earlier pass stopped after one press) — waited out once, never taken
// for the check's own arm; still there = nothing typed.
func TestQuitFooterAlreadyShown(t *testing.T) {
	w, p, row := quitRow(t)
	p.armed = true
	w.screens["S-A"] = p.screenFor()
	repairer(w, p).Fix(&row)
	if len(p.sent) != 0 || !row.Remaining || !p.alive[40] ||
		!strings.Contains(row.Result, "footer already shown before the first Ctrl+C") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// T-c, T-i (revue 1, missing test): the confirming press opens "Background
// work is running" — Esc alone, never Enter; the row ends busy, no start line.
func TestQuitDialogEsc(t *testing.T) {
	w, p, row := quitRow(t)
	p.ctrlCDialog = true
	repairer(w, p).Fix(&row)
	if strings.Join(p.sent, "|") != "\x03|\x03|\x1b" || row.Blocked != "busy: background work" || !p.alive[40] {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// Revue 1 #9: claude quit, then the start line refused (the focus arrived
// after the second press) — the row says the pane sits at its shell.
func TestQuitThenStartRefusedSaysQuit(t *testing.T) {
	w, p, row := quitRow(t)
	rep := repairer(w, p)
	calls := 0
	rep.D.Focused = func() (string, error) {
		calls++
		if calls > 2 { // press 1, press 2, then launch
			return "S-A", nil
		}
		return "", nil
	}
	rep.Fix(&row)
	if strings.Join(p.sent, "|") != "\x03|\x03" || row.Blocked != "busy: pane has keyboard focus" ||
		!strings.Contains(row.Result, "claude quit; start line not typed: busy (pane has keyboard focus)") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}
