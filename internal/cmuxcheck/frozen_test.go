package cmuxcheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// A session cmux resumed hidden keeps a frozen width (the no-signal rule
// below allows Ctrl+C for this case only); its screen reads
// garbled, no input box is found. Idle, not the focused surface, it is quit
// by Ctrl+C (two presses, a third only when the first clears a draft;
// each read back) then started from its shell —
// under the policy: rescue (default) only when a human named it
// (--surface; never the daemon's pilot), unread in any --fix. An unreadable draft is lost. Never any
// text typed into that input, never a signal.

// frozenScreen: testdata/screen-frozen-width.txt, the shape of a read (cmux
// 0.64.24, claude 2.1.29x) of a session cmux resumed hidden, with invented
// text: the last 15 lines of the screen, the conversation above them trimmed.
// No input box is found and no line starts with "❯" — the glyph sits inside
// wrapped lines ("…each sample and ❯ /example-skill", "le❯ /station-setup"):
// no prompt line can be read back on such a screen.
func frozenScreen(t *testing.T) string {
	b, err := os.ReadFile("testdata/screen-frozen-width.txt")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if _, found := claudesession.InputDraft(s); found {
		t.Fatal("fixture: the frozen-width screen must show no input box")
	}
	if !strings.Contains(s, "❯") {
		t.Fatal("fixture: the frozen-width screen must hold the ❯ glyph, inside a line")
	}
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimLeftFunc(l, unicode.IsSpace), "❯") {
			t.Fatalf("fixture: no line may start with ❯ (got %q)", l)
		}
	}
	if _, ok := claudesession.DialogShown(s); ok {
		t.Fatal("fixture: the frozen-width screen shows no dialog")
	}
	return s
}

const (
	quitHint       = "Ctrl+C twice, then the start line; an unreadable draft is lost"
	fixSurfaceHint = "pigtail cmux check --fix --surface S-A"
)

// frozenRun: S-A resumed by cmux 20 s after the launch (no marker), idle,
// its screen garbled; a typed launch line would start pid 41.
func frozenRun(t *testing.T) (*world, *pane) {
	w := newWorld()
	w.add(t, 40, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", ts("2026-10-08T10:31:20Z"), "")
	w.screens["S-A"] = frozenScreen(t)
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{40: true}, exitOK: true,
		spawnPID: 41, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	return w, p
}

// T-a: rescue policy — a general --fix leaves the row LEFT with the exact
// command; named by --surface (any case), or under the unread policy, the row
// plans ActQuit and --fix repairs it with two presses and the start line;
// named without --fix, ActQuit is planned and nothing typed (TO FIX).
func TestFrozenUnreadQuitPolicy(t *testing.T) {
	left := map[string]func(o *Options){
		"inventory":                            func(o *Options) {},
		"--fix":                                func(o *Options) { o.Fix = true },
		"--fix --ctrl-c rescue, nothing named": func(o *Options) { o.Fix, o.Quit = true, QuitRescue },
	}
	for name, set := range left {
		w, p := frozenRun(t)
		w.screens["S-A"] = frozenScreen(t)
		o := runOpts(t)
		set(&o)
		r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-A")
		if len(p.sent) != 0 || r.Category() != CatLeft || hasAction(r, ActQuit) ||
			!strings.Contains(r.Blocked, fixSurfaceHint) || !strings.Contains(r.Blocked, quitHint) {
			t.Errorf("%s: sent %q, row %+v (%s); want LEFT with the --surface command", name, p.sent, r, r.Category())
		}
	}
	quit := map[string]func(o *Options){
		"--fix --surface":             func(o *Options) { o.Fix, o.Surfaces = true, []string{"S-A"} },
		"--fix --surface, lower case": func(o *Options) { o.Fix, o.Surfaces = true, []string{"s-a"} },
		"--fix --ctrl-c unread":       func(o *Options) { o.Fix, o.Quit = true, QuitUnread },
	}
	for name, set := range quit {
		w, p := frozenRun(t)
		p.garbled = frozenScreen(t)
		w.screens["S-A"] = p.screenFor()
		o := runOpts(t)
		set(&o)
		r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-A")
		if r.Category() != CatRepaired || len(r.Done) == 0 || r.Done[0] != ActQuit || r.PID != 41 || !r.frozen {
			t.Errorf("%s: sent %q, row %+v (%s); want repaired by ActQuit on pid 41", name, p.sent, r, r.Category())
		}
		if len(p.sent) < 3 || p.sent[0] != "\x03" || p.sent[1] != "\x03" || !strings.HasPrefix(p.sent[2], " export ") {
			t.Errorf("%s: typed %q, want \\x03, \\x03, the start line", name, p.sent)
		}
	}
	// Revue 2 M6: named, no --fix — ActQuit planned, TO FIX, nothing typed.
	w, p := frozenRun(t)
	p.garbled = frozenScreen(t)
	w.screens["S-A"] = p.screenFor()
	o := runOpts(t)
	o.Surfaces = []string{"S-A"}
	r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-A")
	if len(p.sent) != 0 || r.Category() != CatFix || !hasAction(r, ActQuit) || !r.frozen {
		t.Errorf("--surface without --fix: sent %q, row %+v (%s); want ActQuit planned, TO FIX, nothing typed", p.sent, r, r.Category())
	}
}

// The path the hint names: once that claude is quit by hand, the pane's
// shell back at its prompt, `--fix --surface` starts it (ActStart) with the
// binding's line — the first run LEFT, the second one repaired.
func TestFrozenQuitByHandThenFixStarts(t *testing.T) {
	w, p := frozenRun(t)
	o := runOpts(t)
	o.Fix = true
	if r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-A"); r.Category() != CatLeft || !strings.Contains(r.Blocked, fixSurfaceHint) {
		t.Fatalf("frozen: row %+v (%s), want LEFT with the hint", r, r.Category())
	}

	// Ctrl+C twice by hand: pid 40 ends, the shell is back at its prompt.
	p.alive[40] = false
	w.procs = nil
	delete(w.env, 40)
	delete(w.native, 40)
	w.idleSurface["S-A"] = true
	w.screens["S-A"] = "~/dev/project % "

	o = runOpts(t)
	o.Fix = true
	r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-A")
	if !hasAction(r, ActStart) || r.Category() != CatRepaired || r.PID != 41 {
		t.Fatalf("after the quit: row %+v (%s), want started (ActStart), repaired on pid 41", r, r.Category())
	}
	if len(p.sent) != 1 || !strings.HasPrefix(p.sent[0], r.Line) || !strings.Contains(r.Line, "sid-a") || !strings.Contains(r.Line, "PIGTAIL_CMUX_RELAUNCH=7") {
		t.Fatalf("typed %q (line %q), want the binding's start line alone", p.sent, r.Line)
	}
}

// A busy signal: LEFT, never the hint (quitting would lose the work).
func TestFrozenUnreadBusyNoHint(t *testing.T) {
	w, p := frozenRun(t)
	n := w.native[40]
	n.Status = "busy"
	w.native[40] = n
	o := runOpts(t)
	o.Fix, o.Surfaces = true, []string{"S-A"}
	r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-A")
	if len(p.sent) != 0 || !strings.Contains(r.Blocked, "busy") || strings.Contains(r.Blocked, quitHint) || r.Category() != CatLeft {
		t.Fatalf("busy: sent %q, row %+v (%s); want nothing typed, LEFT, no hint", p.sent, r, r.Category())
	}
}

// No frozen-width problem (relaunched by an earlier --fix, mode lost): the
// unreadable screen blocks as before, named or not.
func TestFrozenUnreadNeedsFrozenWidth(t *testing.T) {
	w := newWorld()
	w.add(t, 13, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	w.screens["S-A"] = frozenScreen(t)
	r := row(t, classify(t, w, Options{Fix: true, Surfaces: []string{"S-A"}}), "S-A")
	if r.Blocked != "no input box on screen" {
		t.Fatalf("row = %+v, want blocked on the input box alone", r)
	}
}

// Every other never-touch rule holds, named or not, and keeps the hint off:
// the caller's own surface, an unknown effective mode.
func TestFrozenUnreadOtherBlocksHold(t *testing.T) {
	w, _ := frozenRun(t)
	r := row(t, classify(t, w, Options{Fix: true, Surfaces: []string{"S-A"}, Self: "S-A"}), "S-A")
	if !strings.Contains(r.Blocked, "the session running this check") || strings.Contains(r.Blocked, fixSurfaceHint) {
		t.Fatalf("self: blocked %q, want the self block and no --fix --surface hint", r.Blocked)
	}

	w, _ = frozenRun(t)
	w.badSettings["/cfg/a"] = true
	w.procs[0].Args = "claude --resume sid-a" // mode from the account: unreadable
	r = row(t, classify(t, w, Options{Fix: true, Surfaces: []string{"S-A"}}), "S-A")
	if !strings.Contains(r.Blocked, "never guessed") || strings.Contains(r.Blocked, quitHint) {
		t.Fatalf("mode unknown: row = %+v", r)
	}
}

// No signal (D21 of sub_x000000000000043, kept by D1): the check stops a
// claude by typing — /exit read back, or \x03 through cmux. One case lifted
// by D4 of sub_x000000000000024 (the user, 2026-10-26): the resume of a
// dead-input session, whose keystrokes never reach its pty, stops the old
// claude by SIGTERM. resume.go alone may hold a signal, SIGTERM only,
// through the Repairer's Kill seam (default killTERMOnly); never SIGKILL,
// SIGSTOP nor any other, never os.FindProcess. No other source file of the
// package may send one.
func TestNoSignalSent(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sawResume := false
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if f == "resume.go" {
			sawResume = true
			checkResumeSignals(t, src)
			continue
		}
		// .Kill( catches a call through the Repairer's seam (r.Kill(pid, 15)
		// sends a real SIGTERM), killTERMOnly( a direct call of its default.
		for _, bad := range []string{"syscall.Kill", "syscall.SIG", "unix.Kill", "unix.SIG", "os.FindProcess", ".Signal(", ".Kill(", "killTERMOnly("} {
			if strings.Contains(src, bad) {
				t.Errorf("%s holds %q: the check never signals a process (D21; resume.go alone, D4)", f, bad)
			}
		}
	}
	if !sawResume {
		t.Fatal("resume.go not read: the narrowed guard proves nothing")
	}
}

// checkResumeSignals: in resume.go, SIGTERM only, sent only through r.Kill,
// and the one real system call inside killTERMOnly, behind its refusal.
func checkResumeSignals(t *testing.T, src string) {
	t.Helper()
	for _, bad := range []string{"os.FindProcess", ".Signal(", "unix.Kill", "unix.SIG", "SIGKILL", "SIGSTOP", "SIGHUP", "SIGINT", "SIGQUIT", "SIGCONT", "SIGUSR"} {
		if strings.Contains(src, bad) {
			t.Errorf("resume.go holds %q: SIGTERM only (D4)", bad)
		}
	}
	for _, m := range regexp.MustCompile(`syscall\.SIG[A-Z0-9]*`).FindAllString(src, -1) {
		if m != "syscall.SIGTERM" {
			t.Errorf("resume.go names %s: SIGTERM only (D4)", m)
		}
	}
	calls := regexp.MustCompile(`r\.Kill\(([^)]*)\)`).FindAllStringSubmatch(src, -1)
	if len(calls) != 1 || !strings.HasSuffix(calls[0][1], ", syscall.SIGTERM") {
		t.Errorf("r.Kill calls %q: want exactly one, with syscall.SIGTERM", calls)
	}
	if n, d := strings.Count(src, "killTERMOnly("), strings.Count(src, "func killTERMOnly("); n != 1 || d != 1 {
		t.Errorf("resume.go calls killTERMOnly directly (%d mentions with the definition): only through r.Kill", n)
	}
	if loops := enclosingLoops(src, strings.Index(src, "r.Kill(")); loops > 0 {
		t.Errorf("r.Kill sits inside %d for loop(s): one SIGTERM, never repeated", loops)
	}
	if n := strings.Count(src, "syscall.Kill("); n != 1 {
		t.Errorf("resume.go holds %d syscall.Kill calls, want the one in killTERMOnly", n)
	}
	body := src[strings.Index(src, "func killTERMOnly("):]
	body = body[:strings.Index(body, "\n}\n")]
	guard, call := strings.Index(body, "sig != syscall.SIGTERM"), strings.Index(body, "syscall.Kill(")
	if guard < 0 || call < 0 || guard > call {
		t.Errorf("killTERMOnly must refuse any signal but SIGTERM before syscall.Kill:\n%s", body)
	}
}

// enclosingLoops counts the open for blocks around src[at] (brace depth,
// line by line: a { opened on a line starting with "for " is a loop).
func enclosingLoops(src string, at int) int {
	if at < 0 {
		return 0
	}
	var stack []bool
	for _, line := range strings.SplitAfter(src[:at], "\n") {
		trimmed := strings.TrimSpace(line)
		for _, c := range line {
			switch c {
			case '{':
				stack = append(stack, strings.HasPrefix(trimmed, "for ") || trimmed == "for {")
			case '}':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}
		}
	}
	n := 0
	for _, loop := range stack {
		if loop {
			n++
		}
	}
	return n
}
