package cmuxcheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// pane simulates one cmux surface: what is typed changes the screen, /exit+\r
// ends the pid, a typed launch line starts a new claude.
type pane struct {
	t           *testing.T
	w           *world
	surface     string
	draft       string
	alive       map[int]bool
	sent        []string
	exitOK      bool // /exit makes the pid end
	spawnPID    int  // pid a typed line starts (0: nothing starts)
	spawnEnv    string
	spawnArg    string
	dialog      bool   // the spawned claude stops on the folder-trust dialog (F11)
	bgWork      bool   // F13: /exit+\r opens "Background work is running" instead of exiting
	exitDlg     bool   // that dialog is on screen
	escKeeps    bool   // Esc does not close it (Claude Code changed)
	escaped     bool   // Esc was sent to that dialog
	exitsAtOnce bool   // F16: the spawned claude exits at once (stale binding, « Resume this session with »)
	spawnSID    string // conversation the spawned claude serves ("" = sid-a)
	// An ephemeral claude of the surface, born with the start line and listed
	// before the real one (2026-10-28: 5 false « exited at once »).
	ephemeralPID   int
	ephemeralReads int    // PidAlive reads where it still lives (then dead)
	ephemeralEnv   string // replaces spawnEnv for the ephemeral when non-empty
	shellLine      string // text typed at the bare shell's prompt (exited pane)

	garbled      string // screen while claude runs at frozen width ("" = the readable idle box)
	armed        bool   // "Press Ctrl-C again to exit" drawn under the box
	ctrlCIgnored bool   // \x03 changes nothing
	exitOnFirst  bool   // the first \x03 ends the pid (contradiction)
	launcher     bool   // after claude exits, the pane does not come back to its shell prompt
	exited       bool   // claude ended by Ctrl+C
	lateConfirm  bool   // D12: the confirming press lands after Claude's window — the footer goes, then is drawn again (re-armed), pid alive
	rearm        bool   // set by a late confirmation: the next screen read draws the footer again (TestQuitLateConfirmStops)
	slowExit     int    // revue 2 N1: the confirming press ends the pid only after that many PidAlive reads; the footer stays drawn meanwhile
	exitIn       int    // reads left before a slow exit ends the pid
	ctrlCDialog  bool   // the confirming press opens "Background work is running" (as /exit does with bgWork)
	slashBlind   bool   // a draft starting with "/" draws the slash menu at frozen width: no box read (D2)
	clearBlind   bool   // \x15 leaves the input as is (D2)
	wrap         int    // the box wraps the draft at word boundaries every wrap runes (frozen width ≈ 80 cols, D10)
	mangle       bool   // a typed text reads back altered (a keystroke of the user mixed in)
	noTurn       bool   // I1: Enter on a continue or pilot text submits it, but the native status stays idle (lagging file, short turn)
}

func (p *pane) screenFor() string {
	if p.exited {
		return "Resume this session with:\nclaude --resume sid-a\n\n~/dev/project % " + p.shellLine
	}
	base := strings.Replace(screenFile(p.t, "screen-idle-empty.txt"), "❯", "❯ "+strings.Join(wrapWords(p.draft, p.wrap), "\n  "), 1)
	if p.garbled != "" {
		base = p.garbled
	}
	if p.slashBlind && strings.HasPrefix(p.draft, "/") {
		base = frozenScreen(p.t)
	}
	if p.armed {
		lines := strings.Split(strings.TrimRight(base, "\n"), "\n")
		lines[len(lines)-1] = "  " + claudesession.CtrlCAgainText
		base = strings.Join(lines, "\n")
	}
	if p.exitDlg {
		return screenFile(p.t, "screen-exit-dialog.txt")
	}
	if p.bgWork && p.escaped {
		return screenFile(p.t, "screen-exit-dialog-cancelled.txt") // real read after Esc
	}
	if p.dialog && p.alive[p.spawnPID] {
		return screenFile(p.t, "screen-trust-dialog.txt")
	}
	return base
}

func (p *pane) send(surface, text string) error {
	p.sent = append(p.sent, text)
	switch {
	case p.exitDlg && text == "\x1b":
		p.exitDlg, p.escaped = p.escKeeps, true
	case p.exitDlg:
		p.t.Errorf("typed %q into the background-work dialog: only Esc is allowed (an Enter kills the session)", text)
	case text == "\r" && p.draft == "/exit" && p.bgWork:
		p.exitDlg, p.draft = true, ""
	case text == "\x03":
		switch {
		case p.ctrlCIgnored:
		case p.armed && p.lateConfirm:
			p.armed, p.rearm = false, true // Claude's window had closed: the footer goes, a later read draws it again
		case p.armed && p.slowExit > 0:
			p.exitIn = p.slowExit // the footer stays drawn; pidAlive ends the pid after slowExit reads
		case p.armed && p.ctrlCDialog:
			p.armed, p.exitDlg, p.bgWork = false, true, true
		case p.exitOnFirst || p.armed:
			p.armed = false
			p.endClaude()
		case p.draft != "":
			p.draft = "" // Claude clears the input; no footer
		default:
			p.armed = true
		}
	case text == "\x15":
		if !p.clearBlind {
			p.draft = ""
		}
	case text == "\r":
		if p.draft == "/exit" && p.exitOK {
			for pid := range p.alive {
				p.alive[pid] = false
			}
		}
		if !p.noTurn && (p.draft == ContinueText || p.draft == PilotText() || p.draft == PilotTextCut()) {
			n := p.w.native[p.spawnOr()]
			n.Status = "busy"
			p.w.native[p.spawnOr()] = n
		}
		p.draft = ""
	case strings.HasPrefix(text, " export ") && p.spawnPID != 0:
		p.exited = false
		p.w.idleSurface[p.surface] = false // the shell runs claude in the foreground
		p.alive[p.spawnPID] = !p.exitsAtOnce
		if p.ephemeralPID != 0 {
			p.w.procs = append(p.w.procs, claudesession.Proc{PID: p.ephemeralPID, Args: p.spawnArg})
			env := p.spawnEnv
			if p.ephemeralEnv != "" {
				env = p.ephemeralEnv
			}
			p.w.env[p.ephemeralPID] = "claude CMUX_SURFACE_ID=" + p.surface + " " + env
		}
		p.w.procs = append(p.w.procs, claudesession.Proc{PID: p.spawnPID, Args: p.spawnArg})
		p.w.env[p.spawnPID] = "claude CMUX_SURFACE_ID=" + p.surface + " " + p.spawnEnv
		sid := p.spawnSID
		if sid == "" {
			sid = "sid-a"
		}
		p.w.native[p.spawnPID] = claudesession.Native{PID: p.spawnPID, SessionID: sid, Status: "idle", Kind: "interactive", StatusUpdatedAt: tNow.Add(-time.Hour).UnixMilli()}
	default:
		p.draft += text
		if p.mangle {
			p.draft += " x"
		}
	}
	p.w.screens[p.surface] = p.screenFor()
	return nil
}

// wrapWords cuts s at spaces into lines of at most n runes (n ≤ 0: one line),
// as Claude Code's input box wraps a long draft (D10).
func wrapWords(s string, n int) []string {
	if n <= 0 || s == "" {
		return []string{s}
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) <= n:
			cur += " " + w
		default:
			lines, cur = append(lines, cur), w
		}
	}
	return append(lines, cur)
}

// endClaude: claude exits on the confirming Ctrl+C; the pane's login shell
// is back at its prompt (simPanes) unless a launcher holds the pane.
func (p *pane) endClaude() {
	for pid := range p.alive {
		p.alive[pid] = false
		delete(p.w.env, pid)
		delete(p.w.native, pid)
	}
	var keep []claudesession.Proc
	for _, pr := range p.w.procs {
		if !p.alive[pr.PID] && pr.PID != p.spawnPID {
			if _, was := p.alive[pr.PID]; was {
				continue
			}
		}
		keep = append(keep, pr)
	}
	p.w.procs = keep
	p.exited = true
	if !p.launcher {
		p.w.idleSurface[p.surface] = true
	}
	p.w.screens[p.surface] = p.screenFor() // also called from pidAlive, outside send
}

// pidAlive is the repairer's PidAlive: a slow exit (slowExit) ends the pid
// only after that many reads, the footer drawn until then (revue 2 N1).
func (p *pane) pidAlive(pid int) bool {
	if p.ephemeralPID != 0 && pid == p.ephemeralPID {
		if p.ephemeralReads <= 0 {
			return false
		}
		p.ephemeralReads--
		return true
	}
	if p.exitIn > 0 {
		if p.exitIn--; p.exitIn == 0 {
			p.armed = false
			p.endClaude()
		}
	}
	return p.alive[pid]
}

func (p *pane) spawnOr() int {
	if p.alive[p.spawnPID] {
		return p.spawnPID
	}
	for pid, a := range p.alive {
		if a {
			return pid
		}
	}
	return 0
}

func repairer(w *world, p *pane) *Repairer {
	return &Repairer{D: w.deps(), Send: p.send, PidAlive: p.pidAlive,
		Sleep: func(time.Duration) {}, Poll: time.Millisecond, ExitTimeout: 5 * time.Millisecond,
		SeenTimeout: 5 * time.Millisecond, SeenSettle: time.Millisecond, ReadyTimeout: 5 * time.Millisecond, CmuxPID: simCmuxPID,
		CtrlCWait: 5 * time.Millisecond, CtrlCPoll: time.Millisecond, CtrlCExitWait: 20 * time.Millisecond, ShellTimeout: 5 * time.Millisecond}
}

func TestFixRelaunchThenContinue(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true,
		spawnPID: 31, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch, ActContinue}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if row.Remaining || row.PID != 31 || len(row.Done) != 2 {
		t.Fatalf("row = %+v", row)
	}
	want := []string{"/exit", "\r", " export X=1; claude\n", ContinueText, "\r"}
	if strings.Join(p.sent, "|") != strings.Join(want, "|") {
		t.Fatalf("typed %q, want %q", p.sent, want)
	}
}

// T-h / F11: the relaunched claude stops on the folder-trust dialog ("No,
// exit" preselected) — nothing more is typed (no continue, no Enter, no
// \x15); the row stays remaining and says why.
func TestFixNeverTypesIntoDialog(t *testing.T) {
	w := newWorld()
	w.add(t, 33, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{33: true}, exitOK: true, dialog: true,
		spawnPID: 34, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 33, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch, ActContinue}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	want := []string{"/exit", "\r", " export X=1; claude\n"}
	if strings.Join(p.sent, "|") != strings.Join(want, "|") || !row.Remaining || !strings.Contains(row.Result, "dialog") ||
		!strings.Contains(row.Result, "answer it, then run `pigtail cmux check --fix`") {
		t.Fatalf("typed %q, row %+v; want %q then stop on the dialog, the next step named", p.sent, row, want)
	}
}

func TestFixSkipsBlockedRow(t *testing.T) {
	w := newWorld()
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{}}
	row := Row{SurfaceID: "S-A", Blocked: "busy: esc to interrupt", Actions: []Action{ActRelaunch}, Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if len(p.sent) != 0 || !row.Remaining {
		t.Fatalf("blocked row touched: sent %q, row %+v", p.sent, row)
	}
}

func TestFixStopsWhenExitLeavesSomethingElse(t *testing.T) {
	w := newWorld()
	w.add(t, 32, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{32: true}}
	p.draft = "" // /exit is typed, but the screen then shows something else
	r := repairer(w, p)
	r.Send = func(s, text string) error {
		if text == "/exit" {
			text = "/exi" // what lands differs: never press Enter on it
		}
		return p.send(s, text)
	}
	row := Row{SurfaceID: "S-A", PID: 32, Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	r.Fix(&row)
	if !row.Remaining || strings.Contains(strings.Join(p.sent, "|"), "\r") {
		t.Fatalf("Enter pressed on an unexpected input: %q / %+v", p.sent, row)
	}
}

// F13 (12:46): the check's own /exit opens "Background work is running".
// It is cancelled with Esc alone, the session lives, the row is busy.
func TestFixCancelsOwnExitDialog(t *testing.T) {
	w := newWorld()
	w.add(t, 41, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{41: true}, exitOK: true, bgWork: true}
	row := Row{SurfaceID: "S-A", PID: 41, Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if got := strings.Join(p.sent, "|"); got != "/exit|\r|\x1b" {
		t.Fatalf("sent %q, want /exit, Enter, then Esc only", got)
	}
	if p.exitDlg || !p.alive[41] {
		t.Fatalf("dialog open %v, session alive %v: want the dialog gone and the session alive", p.exitDlg, p.alive[41])
	}
	if !row.Remaining || row.Blocked != "busy: background work" || len(row.Done) != 0 {
		t.Fatalf("row = %+v, want blocked busy: background work", row)
	}
}

// Esc does not close it: nothing else is typed, the row says so.
func TestFixExitDialogThatStays(t *testing.T) {
	w := newWorld()
	w.add(t, 42, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{42: true}, exitOK: true, bgWork: true, escKeeps: true}
	row := Row{SurfaceID: "S-A", PID: 42, Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if got := strings.Join(p.sent, "|"); got != "/exit|\r|\x1b" {
		t.Fatalf("sent %q, want a single Esc and nothing after", got)
	}
	if !row.Remaining || !strings.Contains(row.Result, "never Enter") {
		t.Fatalf("row = %+v", row)
	}
}

// Classify ran a while ago: a subagent launched since is seen in the
// transcript right before /exit, and nothing is typed.
func TestFixRechecksBackgroundBeforeExit(t *testing.T) {
	w := newWorld()
	w.add(t, 43, "S-A", "/cfg/a", "sid-a", "claude", tNow.Add(-time.Hour), "")
	tp := filepath.Join(t.TempDir(), "a.jsonl")
	line := `{"type":"user","timestamp":"` + tNow.Add(-time.Minute).UTC().Format(time.RFC3339) + `","toolUseResult":{"isAsync":true,"agentId":"a9"}}`
	if err := os.WriteFile(tp, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.paths["sid-a"] = tp
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{43: true}, exitOK: true}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 43, LiveConfigDir: "/cfg/a", Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if len(p.sent) != 0 || !strings.HasPrefix(row.Blocked, "busy: background work") || !row.Remaining {
		t.Fatalf("sent %q, row %+v", p.sent, row)
	}
}

// Review Focus 2: every busy form that appears between Classify and the
// /exit is seen by the same predicate, right before /exit — nothing typed.
func TestFixRechecksBusyBeforeExit(t *testing.T) {
	cases := map[string]func(t *testing.T, w *world){
		"esc to interrupt": func(t *testing.T, w *world) {
			w.screens["S-A"] = strings.Replace(w.screens["S-A"], "\n─", "\n✻ Working… (esc to interrupt)\n─", 1)
		},
		"status busy": func(_ *testing.T, w *world) { n := w.native[44]; n.Status = "busy"; w.native[44] = n },
		"status just moved": func(_ *testing.T, w *world) {
			n := w.native[44]
			n.StatusUpdatedAt = tNow.Add(-time.Second).UnixMilli()
			w.native[44] = n
		},
		"bash shell": func(_ *testing.T, w *world) { w.shells[44] = []int{99} },
		"transcript fresh": func(t *testing.T, w *world) {
			tp := filepath.Join(t.TempDir(), "a.jsonl")
			if err := os.WriteFile(tp, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			w.paths["sid-a"], w.mtimes[tp] = tp, tNow.Add(-10*time.Second)
		},
	}
	for name, mutate := range cases {
		w := newWorld()
		w.add(t, 44, "S-A", "/cfg/a", "sid-a", "claude", tNow.Add(-time.Hour), "")
		mutate(t, w) // after Classify: the row below was planned on an idle session
		p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{44: true}, exitOK: true}
		row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 44, LiveConfigDir: "/cfg/a", Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
		repairer(w, p).Fix(&row)
		if len(p.sent) != 0 || !strings.HasPrefix(row.Blocked, "busy: ") || !row.Remaining {
			t.Errorf("%s: sent %q, row %+v; want nothing typed, a busy row", name, p.sent, row)
		}
	}
}

// ActStart: the pane's shell was at its prompt at Classify; a command run
// there since must not receive the start line. Still at the prompt: typed.
func TestFixStartRechecksShell(t *testing.T) {
	for _, idle := range []bool{false, true} {
		w := newWorld()
		w.screens["S-A"] = screenFile(t, "screen-idle-empty.txt")
		w.idleSurface["S-A"] = idle
		p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{},
			spawnPID: 45, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
		row := Row{SurfaceID: "S-A", SessionID: "sid-a", ConfigDir: "/cfg/a", Mode: "auto", Actions: []Action{ActStart}, Line: " export X=1; claude", Problems: []string{"x"}}
		repairer(w, p).Fix(&row)
		if !idle && (len(p.sent) != 0 || !row.Remaining || !strings.Contains(row.Result, "shell prompt")) {
			t.Errorf("pane busy: sent %q, row %+v; want nothing typed", p.sent, row)
		}
		if idle && (len(p.sent) != 1 || row.Remaining || row.PID != 45) {
			t.Errorf("pane at its prompt: sent %q, row %+v; want the start line, repaired", p.sent, row)
		}
	}
}

func TestFixPidThatDoesNotExit(t *testing.T) {
	w := newWorld()
	w.add(t, 33, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{33: true}, exitOK: false}
	row := Row{SurfaceID: "S-A", PID: 33, Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if !row.Remaining || !strings.Contains(row.Result, "did not exit") {
		t.Fatalf("row = %+v", row)
	}
	for _, s := range p.sent {
		if strings.HasPrefix(s, " export ") {
			t.Fatal("never type the relaunch line while the old claude still runs")
		}
	}
}

func TestFixRelaunchWrongModeIsReported(t *testing.T) {
	w := newWorld()
	w.add(t, 34, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{34: true}, exitOK: true,
		spawnPID: 35, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a", spawnArg: "claude --resume sid-a"}
	row := Row{SurfaceID: "S-A", PID: 34, ConfigDir: "/cfg/a", Mode: "auto", Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if !row.Remaining || !strings.Contains(row.Result, "permission mode") {
		t.Fatalf("row = %+v", row)
	}
}

// F10: the relaunched claude has no --permission-mode in its argv, but its
// account's defaultMode is the expected one — repaired, not "mode lost".
func TestFixSeenAcceptsAccountDefault(t *testing.T) {
	w := newWorld()
	w.defaults["/cfg/a"] = "auto"
	w.add(t, 37, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{37: true}, exitOK: true,
		spawnPID: 38, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 37, ConfigDir: "/cfg/a", Mode: "auto", Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if row.Remaining || row.PID != 38 {
		t.Fatalf("row = %+v, want repaired: auto is the account default", row)
	}
}

func TestFixClearRestoreLine(t *testing.T) {
	w := newWorld()
	w.add(t, 36, "S-B", "/cfg/b", "sid-b", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-B", alive: map[int]bool{36: true}, draft: "cmux restore claude sid-b"}
	w.screens["S-B"] = p.screenFor()
	row := Row{SurfaceID: "S-B", PID: 36, Actions: []Action{ActClearRestore}, Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if row.Remaining || len(p.sent) != 1 || p.sent[0] != "\x15" {
		t.Fatalf("sent %q, row %+v", p.sent, row)
	}
}

// T-k / F15 (13:24): the trust dialog was answered by hand, its text left
// above the input box drawn again — the dialog guard lets the clear through.
func TestFixClearRestoreAfterDialog(t *testing.T) {
	w := newWorld()
	w.add(t, 46, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{46: true}, draft: "cmux restore claude sid-a"}
	w.screens["S-A"] = screenFile(t, "screen-restore-after-trust.txt")
	row := Row{SurfaceID: "S-A", PID: 46, Actions: []Action{ActClearRestore}, Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if row.Remaining || len(p.sent) != 1 || p.sent[0] != "\x15" {
		t.Fatalf("sent %q, row %+v; want the restore line cleared once the dialog is answered", p.sent, row)
	}
}

// T-j / F14: S-B was classified "start sid-a" (alive nowhere); sid-a has come
// up on S-A since. The start line is never typed — no second resume. D11:
// the row ends as info with the hint, not a remaining defect.
func TestFixStartRechecksAliveElsewhere(t *testing.T) {
	w := newWorld()
	w.screens["S-B"] = screenFile(t, "screen-idle-empty.txt")
	w.idleSurface["S-B"] = true
	w.add(t, 47, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a", tNow, "") // after Classify
	p := &pane{t: t, w: w, surface: "S-B", alive: map[int]bool{},
		spawnPID: 48, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a"}
	row := Row{SurfaceID: "S-B", SessionID: "sid-a", ConfigDir: "/cfg/a", Actions: []Action{ActStart}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	info := strings.Join(row.Info, " ")
	if len(p.sent) != 0 || row.Remaining || !strings.Contains(info, "alive in S-A") || !strings.Contains(info, "close or rebind this pane") {
		t.Fatalf("sent %q, row %+v; want nothing typed, info naming S-A with the hint, not remaining (D11)", p.sent, row)
	}
}

// F16 (review round 6): a resume typed in a tab whose binding is stale exits
// at once on « Resume this session with », with no error. The new pid is
// seen, then gone after the settle — or it serves another conversation: the
// row fails, never "repaired".
func TestFixSeenExitsAtOnce(t *testing.T) {
	for name, c := range map[string]struct {
		exits bool
		sid   string
		want  string
	}{
		"exited at once":              {true, "sid-a", "exited at once: start it from a fresh workspace"},
		"serves another conversation": {false, "sid-other", "start it from a fresh workspace"},
	} {
		w := newWorld()
		w.screens["S-A"] = screenFile(t, "screen-idle-empty.txt")
		w.idleSurface["S-A"] = true
		p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{}, spawnPID: 49, exitsAtOnce: c.exits, spawnSID: c.sid,
			spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
		row := Row{SurfaceID: "S-A", SessionID: "sid-a", ConfigDir: "/cfg/a", Mode: "auto", Actions: []Action{ActStart}, Line: " export X=1; claude", Problems: []string{"x"}}
		repairer(w, p).Fix(&row)
		if len(p.sent) != 1 || !row.Remaining || len(row.Done) != 0 || !strings.Contains(row.Result, c.want) {
			t.Errorf("%s: sent %q, row %+v; want one line typed, the row failed with %q", name, p.sent, row, c.want)
		}
	}
}

// É2 (review round 5): the background-work dialog the check's own /exit may
// open is read below the last input box (claudesession.ExitDialogBelowBox) —
// quoted above the box it is no dialog; opened below it, it is. Round 6:
// DialogShown reads that kind on the whole tail; this reading stays, since
// missing it leaves the pid alive ("did not exit"), nothing typed.
func TestExitDialogShownReadsTheEnd(t *testing.T) {
	dlg := screenFile(t, "screen-exit-dialog.txt")
	idle := screenFile(t, "screen-idle-empty.txt")
	for name, c := range map[string]struct {
		screen string
		want   bool
	}{
		"dialog alone":         {dlg, true},
		"quoted above the box": {dlg + "\n" + idle, false},
		"opened below a box":   {idle + "\n" + dlg, true},
		"idle":                 {idle, false},
	} {
		if got := exitDialogShown(c.screen); got != c.want {
			t.Errorf("%s: exitDialogShown = %v, want %v", name, got, c.want)
		}
	}
}

// T-b: the surface holding cmux's keyboard focus is never typed into — not
// /exit, not the start line, not continue; the row ends busy (the second
// pass, D6, tries again).
func TestFixNeverTypesIntoFocusedSurface(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	w.focused = "s-a" // cmux prints upper case; compared without case
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true,
		spawnPID: 31, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch, ActContinue}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if len(p.sent) != 0 || !row.Remaining || row.Blocked != "busy: pane has keyboard focus" {
		t.Fatalf("sent %q, row %+v; want nothing typed, busy: pane has keyboard focus", p.sent, row)
	}
}

// T-b: focus unreadable = nothing typed (fail closed), not an occupation.
func TestFixFocusUnreadableTypesNothing(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, draft: "cmux restore claude sid-a"}
	w.screens["S-A"] = p.screenFor()
	rep := repairer(w, p)
	rep.D.Focused = func() (string, error) { return "", errors.New("cmux identify: signal: killed") }
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a",
		Actions: []Action{ActClearRestore}, Problems: []string{"restore line"}}
	rep.Fix(&row)
	if len(p.sent) != 0 || !row.Remaining || !strings.Contains(row.Result, "keyboard focus unreadable") {
		t.Fatalf("sent %q, row %+v", p.sent, row)
	}
}

// F2: \x15 on the restore line is now behind the full gate — a session busy
// since Classify gets nothing typed.
func TestFixClearRestoreRechecksBusy(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, draft: "cmux restore claude sid-a"}
	w.screens["S-A"] = p.screenFor()
	n := w.native[30]
	n.Status = "busy"
	w.native[30] = n
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a",
		Actions: []Action{ActClearRestore}, Problems: []string{"restore line"}}
	repairer(w, p).Fix(&row)
	if len(p.sent) != 0 || !strings.HasPrefix(row.Blocked, "busy: ") {
		t.Fatalf("sent %q, row %+v; want nothing typed, busy", p.sent, row)
	}
}

// T-b, revue 2 N3: the start line typed after /exit has its own gate —
// waitExit may last 15 s + 1 s; focus that arrives meanwhile gets nothing
// typed, and the row says claude quit (the pane sits at its shell).
func TestRelaunchStartLineStopsOnFocus(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true,
		spawnPID: 31, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	rep := repairer(w, p)
	calls := 0
	rep.D.Focused = func() (string, error) {
		calls++
		if calls >= 3 { // gate before /exit, before its Enter, then the start line
			return "S-A", nil
		}
		return "", nil
	}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	rep.Fix(&row)
	if strings.Join(p.sent, "|") != "/exit|\r" || row.Blocked != "busy: pane has keyboard focus" ||
		!strings.Contains(row.Result, "claude quit; start line not typed: busy (pane has keyboard focus)") {
		t.Fatalf("sent %q, row %+v; want /exit and Enter only, the start line refused", p.sent, row)
	}
}

// D16: --fix types no text into a session in Standby — no ContinueText, no
// PilotTextCut, no PilotText; out of Standby the continue goes (control).
func TestRepairStandbyTypesNoText(t *testing.T) {
	for _, c := range []struct {
		name    string
		pilot   bool
		standby bool
		want    string
	}{
		{"standby", false, true, ""},
		{"standby pilot", true, true, ""},
		{"out of standby", false, false, ContinueText + "|\r"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, p := pilotWorld(t)
			cutTurn(t, w, "sid-a")
			o := runOpts(t)
			o.Fix, o.Surfaces = true, []string{"S-A"}
			if c.pilot {
				o.PilotOnly, o.MaintenanceOwner, o.Unattended = true, "sid-a", true
			}
			o.InStandby = func(sid string) (bool, error) { return c.standby && sid == "sid-a", nil }
			rep := Run(w.deps(), repairer(w, p), o)
			got := strings.Join(p.sent, "|")
			for _, txt := range []string{ContinueText, PilotTextCut(), PilotText()} {
				if c.standby && strings.Contains(got, txt) {
					t.Fatalf("typed %q into a session in Standby (report %+v)", p.sent, rep.Pilot)
				}
			}
			if got != c.want {
				t.Fatalf("typed %q, want %q", p.sent, c.want)
			}
		})
	}
}

// Faux échec du 2026-10-28 (5 lignes « exited at once », la session relancée
// tournait) : un claude éphémère de la même surface, né avec la ligne, n'est
// jamais pris pour la relance — qu'il soit déjà mort au repérage (reads 0)
// ou qu'il meure pendant settled (reads 1).
func TestFixSeenSkipsEphemeralClaude(t *testing.T) {
	for _, reads := range []int{0, 1} {
		w := newWorld()
		w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
		p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true,
			spawnPID: 31, ephemeralPID: 29, ephemeralReads: reads,
			spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
		row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
			Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
		repairer(w, p).Fix(&row)
		if row.Remaining || row.PID != 31 || row.Result != "repaired" {
			t.Fatalf("reads=%d: row = %+v, want repaired on pid 31", reads, row)
		}
	}
}

// Un éphémère d'un autre compte/mode ne fait pas échouer la ligne : seen
// cherche un successeur avant de rendre l'erreur de checkStarted.
// `ephemeralReads: 1` est nécessaire : à 0 l'éphémère est déjà mort quand
// newClaudesOn le lit, la branche successeur ne tournerait jamais.
func TestFixSeenEphemeralOtherEnvStillFindsSuccessor(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true,
		spawnPID: 31, ephemeralPID: 29, ephemeralReads: 1, ephemeralEnv: "CLAUDE_CONFIG_DIR=/cfg/x",
		spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if row.Remaining || row.PID != 31 {
		t.Fatalf("row = %+v, want repaired on pid 31 (the ephemeral had another account)", row)
	}
}

// The successor must serve the row's conversation: a new claude on the
// surface serving another one is never adopted (Review Focus 4).
func TestFixSeenSuccessorOtherConversationNotAdopted(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true,
		spawnPID: 31, ephemeralPID: 29, ephemeralReads: 1, spawnSID: "sid-b",
		spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if !row.Remaining || !strings.Contains(row.Result, `serves conversation "sid-b"`) {
		t.Fatalf("row = %+v, want left: the successor serves another conversation", row)
	}
}

func focusedBareShell(t *testing.T, shellLine string) (*pane, *Row, *Repairer) {
	t.Helper()
	w := newWorld()
	w.idleSurface["S-B"] = true
	w.focused = "s-b"
	p := &pane{t: t, w: w, surface: "S-B", alive: map[int]bool{}, exited: true, shellLine: shellLine,
		spawnPID: 41, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/b PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-b", spawnSID: "sid-b"}
	w.screens["S-B"] = p.screenFor()
	row := &Row{SurfaceID: "S-B", SessionID: "sid-b", ConfigDir: "/cfg/b", Actions: []Action{ActStart},
		Line: " export X=1; claude", Problems: []string{"no live claude"}}
	return p, row, repairer(w, p)
}

// D4: resume off, the pane cmux put in keyboard focus at boot sits at a bare
// shell with an empty input line: started anyway.
func TestFixStartFocusedBareShellEmptyLineStarts(t *testing.T) {
	p, row, rep := focusedBareShell(t, "")
	rep.Fix(row)
	if row.Remaining || len(p.sent) != 1 || p.sent[0] != " export X=1; claude\n" {
		t.Fatalf("row = %+v, typed %q: want the start line typed once", row, p.sent)
	}
}

// D4: the focused pane has something typed (the user is writing): nothing sent,
// not even \x15; the row stays "busy: pane has keyboard focus".
func TestFixStartFocusedShellWithTypedTextSendsNothing(t *testing.T) {
	p, row, rep := focusedBareShell(t, "git sta")
	rep.Fix(row)
	if len(p.sent) != 0 || !row.Remaining || row.Blocked != "busy: "+focusReason {
		t.Fatalf("row = %+v, typed %q: want nothing sent, left busy on focus", row, p.sent)
	}
}

// D4 bound: a focused pane with a live claude is still refused (gate unchanged).
func TestFixStartFocusedLiveClaudeStillRefused(t *testing.T) {
	w := newWorld()
	w.add(t, 30, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow, "")
	w.focused = "s-a"
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{30: true}, exitOK: true,
		spawnPID: 31, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	row := Row{SurfaceID: "S-A", SessionID: "sid-a", PID: 30, ConfigDir: "/cfg/a", LiveConfigDir: "/cfg/a", Mode: "auto",
		Actions: []Action{ActRelaunch}, Line: " export X=1; claude", Problems: []string{"x"}}
	repairer(w, p).Fix(&row)
	if len(p.sent) != 0 || !row.Remaining || row.Blocked != "busy: "+focusReason {
		t.Fatalf("sent %q, row %+v; want nothing typed, busy on focus", p.sent, row)
	}
}

// D4 bound: a dialog on the focused pane stays an error, nothing typed.
func TestFixStartFocusedDialogStillRefused(t *testing.T) {
	p, row, rep := focusedBareShell(t, "")
	rep.D.Screen = func(string) (string, error) { return screenFile(t, "screen-trust-dialog.txt"), nil }
	rep.Fix(row)
	if len(p.sent) != 0 || !row.Remaining || !strings.Contains(row.Result, "stopped on a dialog") {
		t.Fatalf("row = %+v, typed %q: want the dialog error, nothing typed", row, p.sent)
	}
}

// Review round 1: cmux read-screen draws the space after the prompt as NBSP
// (U+00A0) and keeps trailing spaces: an empty prompt of either kind starts
// the focused pane; typed text ending in a prompt char (`echo 50%`) is not an
// empty prompt, nothing is sent.
func TestFixStartFocusedPromptShapes(t *testing.T) {
	for name, c := range map[string]struct {
		screen string
		starts bool
	}{
		"zsh percent":             {"~/dev/project % ", true},
		"nbsp after chevron":      {"some output\n❯\u00a0\n\n", true},
		"nbsp after percent":      {"~/dev/project %\u00a0", true},
		"typed text ending in %":  {"~/dev/project ❯ echo 50%", false},
		"typed text after prompt": {"~/dev/project % git sta", false},
	} {
		p, row, rep := focusedBareShell(t, "")
		rep.D.Screen = func(string) (string, error) { return c.screen, nil }
		rep.Fix(row)
		started := len(p.sent) == 1 && p.sent[0] == " export X=1; claude\n"
		if started != c.starts || (!c.starts && len(p.sent) != 0) {
			t.Errorf("%s: typed %q, want start=%v", name, p.sent, c.starts)
		}
	}
}
