package cmuxcheck

import (
	"strings"
	"testing"
)

// D2, real failure ×4 (repair.go:131-134): the slash menu at frozen width
// blinds the /exit read-back. \x15, then Ctrl+C twice, then the start line;
// the row is repaired and says it was rescued.
func TestRescueAfterExitReadBack(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true, slashBlind: true,
		spawnPID: 31, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"resumed by cmux"}, frozen: true}
	repairer(w, p).Fix(&row)
	want := []string{"/exit", "\x15", "\x03", "\x03", " export X=1; claude\n"}
	if strings.Join(p.sent, "|") != strings.Join(want, "|") {
		t.Fatalf("typed %q, want %q", p.sent, want)
	}
	if row.Remaining || row.PID != 31 || len(row.Info) == 0 || !strings.Contains(row.Info[len(row.Info)-1], "rescued by Ctrl+C after: unexpected input after typing /exit") {
		t.Fatalf("row %+v", row)
	}
}

// D2, real failure ×1 (repair.go:103-111): the restore line not cleared —
// the first press clears it, two more quit, the start line; the ActRelaunch
// after it is done by the rescue (pid already new), never a second /exit.
func TestRescueAfterClearReadBack(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, clearBlind: true, draft: "cmux restore claude sid-a",
		spawnPID: 31, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	w.screens["S-A"] = p.screenFor()
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActClearRestore, ActRelaunch}, Line: " export X=1; claude", Problems: []string{"restore line", "resumed by cmux"}, frozen: true}
	repairer(w, p).Fix(&row)
	want := []string{"\x15", "\x03", "\x03", "\x03", " export X=1; claude\n"}
	if strings.Join(p.sent, "|") != strings.Join(want, "|") || row.Remaining || row.PID != 31 || len(row.Done) != 2 {
		t.Fatalf("typed %q (want %q), row %+v", p.sent, want, row)
	}
}

// D2: a frozen row with no start line (bypass binding) — no rescue, the
// first failure stands, nothing beyond the \x15 typed.
func TestRescueNeedsALine(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, clearBlind: true, draft: "cmux restore claude sid-a"}
	w.screens["S-A"] = p.screenFor()
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a",
		Actions: []Action{ActClearRestore}, Problems: []string{"restore line", "resumed by cmux"}, frozen: true}
	repairer(w, p).Fix(&row)
	if strings.Join(p.sent, "|") != "\x15" || !row.Remaining || !strings.Contains(row.Result, "input not cleared") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}

// Review Focus 7 (revue 1 #3, D2 « pane gelé »): a row relaunched for a
// lost permission mode at a healthy width — a failed /exit read-back there more
// likely means a person typing at the same moment: no Ctrl+C, the first
// failure stands.
func TestRescueOnlyOnFrozen(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true, slashBlind: true,
		spawnPID: 31, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"permission mode auto lost"}} // frozen: false
	repairer(w, p).Fix(&row)
	if strings.Join(p.sent, "|") != "/exit|\x15" || !row.Remaining || !p.alive[30] ||
		!strings.Contains(row.Result, "unexpected input after typing /exit") || strings.Contains(row.Result, "Ctrl+C") {
		t.Fatalf("typed %q, row %+v", p.sent, row)
	}
}
