package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// newRWShellFake: a Claude c did not start. Once the old Claude is killed the
// tab shows the real shell-prompt screen captured in task 5.
func newRWShellFake(t *testing.T) *rwFake {
	f := newRWFake(t)
	f.run.Relaunch, f.run.RelaunchFile, f.run.LaunchProto = relaunchShell, "", 0
	idle, shell := fixture(t, "screen-idle-empty.txt"), fixture(t, "screen-shell-prompt.txt")
	f.shellScreen = shell
	f.screen = func(f *rwFake, _ int) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.kills == 0 {
			return idle, nil
		}
		return f.shellScreen, nil
	}
	return f
}

// launcherDecoys puts the worker in a temporary directory holding a .prompt
// and a .handoff: a shell-mode withdraw must never remove them (an empty
// RelaunchFile would make promptPath() ".prompt", relative to the cwd).
// Call it after the fake is built: fixture() reads by a relative path.
func launcherDecoys(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, p := range []string{".prompt", ".handoff"} {
		if err := os.WriteFile(p, []byte("decoy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertLauncherDecoys(t *testing.T) {
	t.Helper()
	for _, p := range []string{".prompt", ".handoff"} {
		if !fileExists(p) {
			t.Fatalf("%s removed from the worker's cwd: withdraw touched launcher files in shell mode", p)
		}
	}
}

func TestRespawnWorkerShellRelaunch(t *testing.T) {
	f := newRWShellFake(t)
	launcherDecoys(t)
	var script string
	f.onProcs = func(f *rwFake) { // read the script while it exists (after the kill, before the end)
		if b, err := os.ReadFile(respawnScriptPath(f.run.ID)); err == nil {
			script = string(b)
		}
	}
	e := f.work()
	if e.Result != respawnDone || e.PID != rwNewPID {
		t.Fatalf("final = %+v", e)
	}
	want := []string{`"\x15"`, " source '" + respawnScriptPath(f.run.ID) + "'"}
	if strings.Join(f.typed, "|") != strings.Join(want, "|") {
		t.Fatalf("typed = %q, want %q", f.typed, want)
	}
	for _, s := range []string{"(( ${C_LAUNCH_PROTO:-0} >= 1 )) && (( $+functions[c] ))",
		"C_CWD='" + f.run.Cwd + "' c jp --permission-mode 'plan' '/example-skill resume sub_p'",
		" claude --resume sid-old"} {
		if !strings.Contains(script, s) {
			t.Errorf("script lacks %q:\n%s", s, script)
		}
	}
	if fileExists(respawnScriptPath(f.run.ID)) {
		t.Fatal("relaunch script left behind")
	}
	assertLauncherDecoys(t)
	assertRelaunchRecorded(t, relaunchShell)
}

// every record of a run says how it relaunches — the post-merge gate
// (plan section « Après merge ») counts the first shell run that reaches a terminal result.
func assertRelaunchRecorded(t *testing.T, want string) {
	t.Helper()
	es, err := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if err != nil || len(es) < 2 {
		t.Fatalf("registry: %v %+v", err, es)
	}
	for _, e := range es {
		if e.Relaunch != want {
			t.Fatalf("record %s/%s says relaunch=%q, want %q", e.ID, e.Result, e.Relaunch, want)
		}
	}
}

func TestRespawnWorkerShellWaitsForPrompt(t *testing.T) {
	f := newRWShellFake(t)
	f.shellPolls = 3
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
}

func TestRespawnWorkerShellNotAtPrompt(t *testing.T) {
	f := newRWShellFake(t)
	launcherDecoys(t)
	f.shellPolls = 1 << 30
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "not at its prompt") ||
		!strings.Contains(e.Detail, "nothing typed") || !strings.Contains(e.Detail, " claude --resume sid-old") {
		t.Fatalf("final = %+v", e)
	}
	if len(f.typed) != 0 {
		t.Fatalf("typed into a shell not at its prompt: %q", f.typed)
	}
	f.assertPills(t, "failed")
	if fileExists(respawnScriptPath(f.run.ID)) {
		t.Fatal("relaunch script left behind")
	}
	assertLauncherDecoys(t)
	assertRelaunchRecorded(t, relaunchShell) // a failed shell run is what the gate reads too
}

// ShellAtPrompt reads tpgid == pgid: a shell function waiting for a key
// (_c_run's read -k1) runs in the shell's own group, so the probe says « at
// its prompt ». The screen must show an empty prompt line too (incident
// 2026-10-03: the key read ate the `C`).
func TestRespawnWorkerShellKeyWaitTypesNothing(t *testing.T) {
	f := newRWShellFake(t)
	f.shellScreen = fixture(t, "screen-c-key-prompt.txt") // ps says at its prompt (shellPolls 0)
	launcherDecoys(t)
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "waits for a key") ||
		!strings.Contains(e.Detail, "nothing typed") || !strings.Contains(e.Detail, " claude --resume sid-old") {
		t.Fatalf("final = %+v", e)
	}
	if len(f.typed) != 0 {
		t.Fatalf("typed into a key wait: %q", f.typed)
	}
	f.assertPills(t, "failed")
	if fileExists(respawnScriptPath(f.run.ID)) {
		t.Fatal("relaunch script left behind")
	}
	assertLauncherDecoys(t)
}

// The ps probe itself fails: the tab is never typed
// into and the detail names the probe's error.
func TestRespawnWorkerShellProbeErrorTypesNothing(t *testing.T) {
	f := newRWShellFake(t)
	launcherDecoys(t)
	f.shellErr = errors.New("pane state unreadable")
	e := f.work()
	for _, s := range []string{"not at its prompt", "pane state unreadable", "nothing typed", " claude --resume sid-old"} {
		if e.Result != respawnFailed || !strings.Contains(e.Detail, s) {
			t.Fatalf("detail lacks %q: final = %+v", s, e)
		}
	}
	if len(f.typed) != 0 {
		t.Fatalf("typed while the probe failed: %q", f.typed)
	}
	f.assertPills(t, "failed")
	if fileExists(respawnScriptPath(f.run.ID)) {
		t.Fatal("relaunch script left behind")
	}
	assertLauncherDecoys(t)
}

// ps says « at its prompt » but the screen still shows Claude's input box
// (shellScreenReady, claudesession.InputDraft): nothing is typed.
func TestRespawnWorkerShellClaudeBoxTypesNothing(t *testing.T) {
	f := newRWShellFake(t)
	f.shellScreen = fixture(t, "screen-idle-empty.txt") // Claude's box after the exit; shellPolls 0: ps says ready
	launcherDecoys(t)
	e := f.work()
	for _, s := range []string{"not at its prompt", "Claude's input box", "nothing typed", " claude --resume sid-old"} {
		if e.Result != respawnFailed || !strings.Contains(e.Detail, s) {
			t.Fatalf("detail lacks %q: final = %+v", s, e)
		}
	}
	if len(f.typed) != 0 {
		t.Fatalf("typed into Claude's input box: %q", f.typed)
	}
	f.assertPills(t, "failed")
	assertLauncherDecoys(t)
}

// cutAfterPromptGlyph keeps the screen up to the first prompt glyph of its
// last non-blank line, by index: the left prompt alone, the right prompt
// (powerlevel10k « ✘ TERM 1m 45s ») dropped.
func cutAfterPromptGlyph(t *testing.T, screen string) string {
	t.Helper()
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		j := strings.Index(lines[i], "❯")
		if j < 0 {
			t.Fatalf("last non-blank line has no ❯: %q", lines[i])
		}
		return strings.Join(append(lines[:i:i], lines[i][:j+len("❯")]), "\n")
	}
	t.Fatal("blank screen")
	return ""
}

func TestShellScreenReady(t *testing.T) {
	t.Parallel()
	shell, key := fixture(t, "screen-shell-prompt.txt"), fixture(t, "screen-c-key-prompt.txt")
	// (2026-10-08): a never-used session shows a dimmed
	// Try "…" in its box. A placeholder is empty for the relay wake
	// (InputPlaceholder), but here the box itself means Claude is still drawn:
	// never ready, never read as a shell prompt.
	// cmux read-screen renders the space after ❯ as U+00A0
	// the empty box line of
	// screen-idle-empty.txt is e2 9d af c2 a0 0a, never "❯ \n".
	idle := fixture(t, "screen-idle-empty.txt")
	placeholder := strings.Replace(idle, "❯\u00a0\n", "❯\u00a0Try \"how does <filepath> work?\"\n", 1)
	if placeholder == idle {
		t.Fatal("placeholder fixture not built: screen-idle-empty.txt has no ❯ + U+00A0 line")
	}
	if d, found := claudesession.InputDraft(placeholder); !found || !claudesession.InputPlaceholder(d) {
		t.Fatalf("built screen is not a Try placeholder box: %q %v", d, found)
	}
	// The real capture ends with powerlevel10k's right prompt (« ✘ TERM 1m
	// 45s » after ~52 blanks). bare is the capture without its trailing
	// newline; left is its last line cut right after the left prompt's ❯.
	// U+00A0 is blank (unicode.IsSpace), never the prompt's last character.
	bare := strings.TrimRightFunc(shell, unicode.IsSpace)
	left := cutAfterPromptGlyph(t, shell)
	pad := strings.Repeat(" ", 52)
	for _, c := range []struct {
		name, screen, why string
		ok                bool
	}{
		{"real shell prompt with its right prompt (task 5 capture)", shell, "", true},
		{"real prompt line followed by U+00A0", bare + "\u00a0\n", "", true},
		{"left prompt alone", left + "\n", "", true},
		{"left prompt glyph followed by U+00A0", left + "\u00a0\n", "", true},
		{"left prompt glyph followed by U+00A0 and blank lines", left + "\u00a0\n\n\n", "", true},
		{"glyph alone", "❯\n", "", true},
		{"glyph and U+00A0", "❯\u00a0\n", "", true},
		{"right prompt holding a > after an empty left prompt", "~/x ❯" + pad + "a>b 1s\n", "", true},
		{"launcher key wait", key, "waits for a key", false},
		{"key wait line alone", "↻ Entrée = relancer (hb) · autre touche = quitter \n", "waits for a key", false},
		{"Claude still drawn", idle, "Claude's input box", false},
		{"Claude's empty box with its Try placeholder", placeholder, "Claude's input box", false},
		{"text typed after the prompt", left + " ls\n", "no empty prompt line", false},
		{"text typed after the prompt's U+00A0", left + "\u00a0ls\n", "no empty prompt line", false},
		{"text typed, then the real right prompt", left + " ls" + pad + "✘ TERM 1m 45s\n", "no empty prompt line", false},
		{"text typed, then a right prompt", "❯ foo        ✘ TERM 1s\n", "no empty prompt line", false},
		// The first glyph of the line is the left prompt's: a mark inside the
		// right prompt never makes a typed line read as empty.
		{"text typed, right prompt holding a > then blanks", "~/x ❯ foo" + pad + "git>   1s\n", "no empty prompt line", false},
		{"text typed, right prompt ending with >", "~/x ❯ foo" + pad + "a>\n", "no empty prompt line", false},
		// A mark in the path comes first: fail closed, never a guess.
		{"prompt mark inside the path", "~/a#b ❯\n", "no empty prompt line", false},
		{"blank screen", "\n\n", "no empty prompt line", false},
		{"U+00A0 only", "\u00a0\n", "no empty prompt line", false},
	} {
		ok, why := shellScreenReady(c.screen)
		if ok != c.ok || !strings.Contains(why, c.why) {
			t.Errorf("%s: ok=%v why=%q", c.name, ok, why)
		}
	}
}

func TestRespawnWorkerShellSendFails(t *testing.T) {
	f := newRWShellFake(t)
	launcherDecoys(t)
	f.sendErr = os.ErrDeadlineExceeded
	if e := f.work(); e.Result != respawnFailed || !strings.Contains(e.Detail, " claude --resume sid-old") {
		t.Fatalf("final = %+v", e)
	}
	assertLauncherDecoys(t)
}

// cmux took the source line and failed (timeout, exit 1): it may have
// typed it in part or in full — the detail says so, and failed, no look
// for a fresh Claude.
func TestRespawnWorkerShellLineFailsMayBeTyped(t *testing.T) {
	f := newRWShellFake(t)
	launcherDecoys(t)
	f.lineErr = fmt.Errorf("cmux send: %w", errors.New("signal: killed"))
	e := f.work()
	want := "type the relaunch line: cmux send: signal: killed — it may be typed in part or in full (cmux gives no count); look at the tab before resuming"
	if e.Result != respawnFailed || !strings.Contains(e.Detail, want) ||
		strings.Contains(e.Detail, "nothing typed") || strings.Contains(e.Detail, "no fresh Claude") ||
		!strings.Contains(e.Detail, " claude --resume sid-old") {
		t.Fatalf("final = %+v", e)
	}
	if strings.Join(f.typed, "|") != `"\x15"` { // the clear went through; the line call failed
		t.Fatalf("typed = %q", f.typed)
	}
	f.assertPills(t, "failed")
	assertLauncherDecoys(t)
}

// the warning stays whole in what --status shows of the detail
// (cmux.OneLine(detail, 160), session_respawn.go), even with a cmux stderr
// at runError's 240-rune bound.
func TestRespawnWorkerShellLineFailWarningFitsStatus(t *testing.T) {
	f := newRWShellFake(t)
	f.lineErr = fmt.Errorf("cmux send: exit status 1: %s", strings.Repeat("Error: surface not found ", 10))
	e := f.work()
	if shown := cmux.OneLine(e.Detail, 160); !strings.Contains(shown, "look at the tab before resuming") {
		t.Fatalf("--status shows %q: the warning is cut", shown)
	}
}

// SendLineBounded refused the line before any cmux call (ErrInvalidArg):
// nothing was typed, and the detail says that, not « may be typed ».
func TestRespawnWorkerShellLineRefusedNothingTyped(t *testing.T) {
	f := newRWShellFake(t)
	f.lineErr = cmux.SendLineBounded("S1", " source x") // "S1" is no UUID: refused before exec
	if !errors.Is(f.lineErr, cmux.ErrInvalidArg) {
		t.Fatalf("precondition: SendLineBounded = %v, want ErrInvalidArg", f.lineErr)
	}
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "type the relaunch line: cmux: invalid argument") ||
		!strings.Contains(e.Detail, "— nothing typed") || strings.Contains(e.Detail, "may be typed") {
		t.Fatalf("final = %+v", e)
	}
}

// The detail keeps relaunchErrRunes of the cmux error; the worker's log is the
// one place it stays whole (typeRelaunch's logf). f.work() discards the log, so
// the worker is run here with a buffer.
func TestRespawnWorkerShellLineFailLogKeepsWholeError(t *testing.T) {
	f := newRWShellFake(t)
	launcherDecoys(t)
	whole := "cmux send: exit status 1: Error: surface 1A2B3C4D-0000 not found in window 2" // 79 runes > 43
	f.lineErr = errors.New(whole)
	var log bytes.Buffer
	e := runRespawnWorker(context.Background(), nil, spawnreg.Open(spawnreg.DefaultPath()), f.run, &log)
	if e.Result != respawnFailed {
		t.Fatalf("final = %+v", e)
	}
	for _, l := range strings.Split(log.String(), "\n") {
		if strings.HasSuffix(l, " type the relaunch line: "+whole) {
			return
		}
	}
	t.Fatalf("no log line ends with the whole cmux error %q:\n%s", whole, log.String())
}

func TestRespawnWorkerShellUTF8PromptStaysInTheFile(t *testing.T) {
	f := newRWShellFake(t)
	f.run.Prompt = "/example-skill resume où"
	f.newArgs = "/x/bin/claude --permission-mode plan " + f.run.Prompt
	var script string
	f.onProcs = func(f *rwFake) {
		if b, err := os.ReadFile(respawnScriptPath(f.run.ID)); err == nil {
			script = string(b)
		}
	}
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	for _, l := range f.typed {
		for i := 0; i < len(l); i++ {
			if l[i] > 0x7e {
				t.Fatalf("non-ASCII byte typed: %q", l)
			}
		}
	}
	if !strings.Contains(script, "'/example-skill resume où'") {
		t.Fatalf("prompt not intact in the script:\n%s", script)
	}
}

func TestRespawnWorkerShellInterruptedBeforeKillTypesNothing(t *testing.T) {
	f := newRWShellFake(t)
	f.fgPolls = 1 << 30 // the foreground never ends: the worker waits
	done := make(chan struct{})
	go func() { f.work(); close(done) }()
	for {
		f.mu.Lock()
		cleanup := f.cleanup
		f.mu.Unlock()
		if cleanup != nil {
			cleanup()
			break
		}
		time.Sleep(time.Millisecond)
	}
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.typed) != 0 || f.kills != 0 || fileExists(respawnScriptPath(f.run.ID)) {
		t.Fatalf("typed %q, kills %d", f.typed, f.kills)
	}
}

// Account rotation: the shell relaunch starts the fresh session on the fresh
// cell; the resume line of the message names the old one.
func TestRespawnShellScriptFreshCell(t *testing.T) {
	t.Parallel()
	r := respawnRun{ID: "r1", SurfaceRef: "surface:7", Cwd: "/w", Cell: "ip", ConfigDir: "/cfg/ip", FreshCell: "mp", FreshConfigDir: "/cfg/mp", Mode: "auto", Prompt: "/example-skill x"}
	s := shellRelaunchScript(r, "sid-old")
	if !strings.Contains(s, "C_CWD='/w' c mp --permission-mode 'auto'") || !strings.Contains(s, "CLAUDE_CONFIG_DIR='/cfg/mp' command") ||
		!strings.Contains(s, "CLAUDE_CONFIG_DIR=/cfg/ip claude --resume sid-old") {
		t.Fatalf("script:\n%s", s)
	}
	r.FreshCell = ""
	if s := shellRelaunchScript(r, "sid-old"); !strings.Contains(s, "c ip --permission-mode") {
		t.Fatalf("legacy script:\n%s", s)
	}
}

// The session gate's exemption: the respawn relaunches a session
// that was already counted, so its c line carries PIGTAIL_GATE_PASSED=respawn
// as a one-command prefix of the launcher line, never exported.
func TestShellRelaunchScriptPrefixesGatePassedRespawn(t *testing.T) {
	t.Parallel()
	r := respawnRun{ID: "r1", SurfaceRef: "surface:7", Cwd: "/w", Cell: "ip", Mode: "auto", Prompt: "/example-skill x"}
	s := shellRelaunchScript(r, "sid-old")
	if !strings.Contains(s, "  PIGTAIL_GATE_PASSED=respawn C_CWD='/w' c ip --permission-mode 'auto'") {
		t.Fatalf("the c line lacks the prefix:\n%s", s)
	}
	if strings.Contains(s, "export") || strings.Count(s, "PIGTAIL_GATE_PASSED") != 1 {
		t.Fatalf("the exemption must be one prefix, never exported nor repeated:\n%s", s)
	}
}

func TestRespawnShellScriptModel(t *testing.T) {
	t.Parallel()
	r := respawnRun{ID: "r1", SurfaceRef: "surface:7", Cwd: "/w", Cell: "ip", Mode: "auto", Prompt: "/example-skill x", Model: "sonnet"}
	if s := shellRelaunchScript(r, "sid-old"); !strings.Contains(s, "c ip --permission-mode 'auto' --model 'sonnet' '/example-skill x'") {
		t.Fatalf("script:\n%s", s)
	}
	r.Model = ""
	if s := shellRelaunchScript(r, "sid-old"); strings.Contains(s, "--model") {
		t.Fatalf("script without a model:\n%s", s)
	}
}
