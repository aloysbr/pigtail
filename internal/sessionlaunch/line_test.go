package sessionlaunch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
)

func TestLineShape(t *testing.T) {
	l := Line("jp", "plan", "spawn")
	for _, want := range []string{
		"/bin/zsh -i -c ",
		"C_LAUNCH_PROTO",
		"exec /bin/zsh -i",
		"$" + EnvSpawnPrompt,
		"$" + EnvSpawnCwd,
	} {
		if !strings.Contains(l, want) {
			t.Errorf("launch line lacks %q: %s", want, l)
		}
	}
	if strings.Contains(l, "cmux send") {
		t.Fatal("the prompt must never be typed")
	}
}

// TestLineIsASCII: cmux types --command into the new terminal and mangles
// non-ASCII bytes on the way (é arrives as c3 83 c2 a9); --env values arrive
// intact. So the command line must be pure ASCII and carry neither the prompt
// nor the cwd, which travel as environment variables.
func TestLineIsASCII(t *testing.T) {
	prompt, cwd := "/example-skill reprends le sujet — café", "/w/fraîches"
	l := Line("jp", "plan", "spawn")
	for i := 0; i < len(l); i++ {
		if l[i] >= 0x80 {
			t.Fatalf("non-ASCII byte %#x at %d in the launch line: %s", l[i], i, l)
		}
	}
	env := Env(cwd, prompt)
	if len(env) != 2 || env[0] != EnvSpawnCwd+"="+cwd || env[1] != EnvSpawnPrompt+"="+prompt {
		t.Fatalf("launch env = %q", env)
	}
}

// runLine runs the line's inner script under zsh -f with a stub `c` that
// prints the gate it sees and its argv (pattern of cmd/pigtail's
// TestLaunchLineExecutes). Skipped where zsh is absent (CI Linux image).
func runLine(t *testing.T, line, cwd, prompt string) string {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	inner := strings.Replace(innerScript(t, line), LaunchTail, "exit 0", 1)
	stub := `c() { print -r -- "GATE=${PIGTAIL_GATE_PASSED-unset}"; for a in "$@"; do print -r -- "ARG=$a"; done }; `
	cmd := exec.Command(zsh, "-f", "-c", "C_LAUNCH_PROTO=1; "+stub+inner)
	cmd.Env = append(environWithout(GatePassedVar), Env(cwd, prompt)...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// environWithout is os.Environ minus name: a test run from a spawned session
// inherits PIGTAIL_GATE_PASSED, which the stub would read as the line's.
func environWithout(name string) []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, name+"=") {
			env = append(env, e)
		}
	}
	return env
}

// TestLineWithoutGate: an empty gate emits no PIGTAIL_GATE_PASSED prefix at
// all (a daemon launch is the user's own, Q1), and the mode still reaches c.
func TestLineWithoutGate(t *testing.T) {
	l := Line("mp", "auto", "")
	if strings.Contains(l, GatePassedVar) {
		t.Fatalf("a gateless line names %s: %s", GatePassedVar, l)
	}
	out := runLine(t, l, t.TempDir(), "hello")
	want := "GATE=unset\nARG=mp\nARG=--permission-mode\nARG=auto\nARG=hello\n"
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

// TestLineWithGate: a gate reason is a one-command prefix of the c call.
func TestLineWithGate(t *testing.T) {
	l := Line("mp", "auto", "spawn")
	if !strings.Contains(l, GatePassedVar+"=spawn") {
		t.Fatalf("no gate prefix: %s", l)
	}
	if out := runLine(t, l, t.TempDir(), "hello"); !strings.HasPrefix(out, "GATE=spawn\n") {
		t.Fatalf("c saw %q", out)
	}
}

// TestSpawnEnvCarriesPromptByEnv: the non-ASCII prompt travels only in the
// environment, never in the line.
func TestSpawnEnvCarriesPromptByEnv(t *testing.T) {
	const prompt = "/example-skill reprends le sujet sub_x — écrit « é »"
	cell := claudeprofile.Cell{ID: "mp", ConfigDir: "/cfg/mp"}
	env := SpawnEnv(cell, "/w/d", prompt)
	want := []string{
		"CLAUDE_CONFIG_DIR=/cfg/mp",
		"CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV=1",
		"CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV_KEYS=CLAUDE_CONFIG_DIR",
		EnvSpawnCwd + "=/w/d",
		EnvSpawnPrompt + "=" + prompt,
	}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env = %q, want %q", env, want)
	}
	if l := Line(cell.ID, "auto", ""); strings.Contains(l, "écrit") || strings.Contains(l, "sub_x") {
		t.Fatalf("the prompt leaked into the line: %s", l)
	}
}

// innerScript undoes the outer `/bin/zsh -i -c '<inner>'` quoting (a copy of
// cmd/pigtail's helper).
func innerScript(t *testing.T, line string) string {
	t.Helper()
	const pfx = "/bin/zsh -i -c "
	if !strings.HasPrefix(line, pfx) {
		t.Fatalf("unexpected line %q", line)
	}
	out, err := exec.Command("/bin/sh", "-c", `printf %s `+strings.TrimPrefix(line, pfx)).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// launcherSegment is the part of the inner line a shell with the launcher
// (C_LAUNCH_PROTO ≥ 1) runs: everything up to the else.
func launcherSegment(t *testing.T, line string) string {
	t.Helper()
	inner := innerScript(t, line)
	i := strings.Index(inner, "; else ")
	if i < 0 {
		t.Fatalf("no else branch in %q", inner)
	}
	return inner[:i+len("; else ")]
}

// TestLineWithLauncherUnchanged: the launcher branch, copied from the line
// before the direct launch existed, byte for byte (the author's path).
func TestLineWithLauncherUnchanged(t *testing.T) {
	const head = `__pt_p="$PIGTAIL_SPAWN_PROMPT"; __pt_d="$PIGTAIL_SPAWN_CWD"; unset PIGTAIL_SPAWN_PROMPT PIGTAIL_SPAWN_CWD; if (( ${C_LAUNCH_PROTO:-0} >= 1 )); then `
	for _, tc := range []struct{ cell, mode, gate, want string }{
		{"jp", "plan", "spawn", head + `PIGTAIL_GATE_PASSED=spawn C_CWD="$__pt_d" c jp --permission-mode 'plan' "$__pt_p"; else `},
		{"mp", "", "", head + `C_CWD="$__pt_d" c mp "$__pt_p"; else `},
	} {
		if got := launcherSegment(t, Line(tc.cell, tc.mode, tc.gate)); got != tc.want {
			t.Errorf("Line(%q,%q,%q) launcher branch\n got %s\nwant %s", tc.cell, tc.mode, tc.gate, got, tc.want)
		}
		if l := Line(tc.cell, tc.mode, tc.gate); !strings.HasSuffix(l, `; fi; exec /bin/zsh -i'`) {
			t.Errorf("tail changed: %s", l)
		}
	}
}

// directRun is what a stub claude (or cmux wrapper) recorded.
type directRun struct {
	called bool
	lines  []string // CWD=, GATE=, PROMPT_ENV=, ARG=…
	output string   // the shell's own stdout+stderr
}

// stubClaude writes an executable at path that records its cwd, the gate and
// prompt carriers it sees, and its argv into out.
func stubClaude(t *testing.T, path, out string) {
	t.Helper()
	script := "#!/bin/sh\n{ printf 'CWD=%s\\n' \"$(pwd -P)\"; printf 'GATE=%s\\n' \"${PIGTAIL_GATE_PASSED-unset}\"; printf 'PROMPT_ENV=%s\\n' \"${PIGTAIL_SPAWN_PROMPT-unset}\"; for a in \"$@\"; do printf 'ARG=%s\\n' \"$a\"; done; } > " + ShQuote(out) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatal(err)
	}
}

// runDirect runs the line's inner script under zsh -f in a shell without the
// launcher (C_LAUNCH_PROTO unset, no c), PATH = a stub claude + /usr/bin:/bin.
// wrapper: also put a stub cmux-claude-wrapper next to a fake
// CMUX_BUNDLED_CLI_PATH. The inner script under zsh -f reads no ~/.zshrc.
func runDirect(t *testing.T, line, cwd, prompt string, wrapper bool) (claude, wrap directRun) {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	bin, cmuxBin := t.TempDir(), t.TempDir()
	claudeOut, wrapOut := filepath.Join(bin, "claude.out"), filepath.Join(cmuxBin, "wrap.out")
	stubClaude(t, filepath.Join(bin, "claude"), claudeOut)
	env := []string{}
	for _, e := range os.Environ() {
		switch k, _, _ := strings.Cut(e, "="); k {
		case GatePassedVar, LauncherProtoVar, "CMUX_BUNDLED_CLI_PATH", "PATH", EnvSpawnCwd, EnvSpawnPrompt:
		default:
			env = append(env, e)
		}
	}
	env = append(env, "PATH="+bin+":/usr/bin:/bin")
	if wrapper {
		stubClaude(t, filepath.Join(cmuxBin, "cmux-claude-wrapper"), wrapOut)
		env = append(env, "CMUX_BUNDLED_CLI_PATH="+filepath.Join(cmuxBin, "cmux"))
	}
	inner := strings.Replace(innerScript(t, line), LaunchTail, "exit 0", 1)
	cmd := exec.Command(zsh, "-f", "-c", "unset C_LAUNCH_PROTO; "+inner)
	cmd.Env = append(env, Env(cwd, prompt)...)
	cmd.Stdin = nil
	out, _ := cmd.CombinedOutput()
	read := func(p string) directRun {
		r := directRun{output: string(out)}
		if b, err := os.ReadFile(p); err == nil {
			r.called = true
			r.lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		}
		return r
	}
	return read(claudeOut), read(wrapOut)
}

// TestLineWithoutLauncherRunsClaude: a shell without the launcher runs
// claude itself, in the session's directory, with the mode and the prompt,
// no gate prefix and the prompt carrier gone from its environment.
func TestLineWithoutLauncherRunsClaude(t *testing.T) {
	prompt := `/example-skill reprends — café « é » it's $HOME ` + "`id`"
	cwd := filepath.Join(t.TempDir(), "fraîches it's")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"CWD=" + real, "GATE=unset", "PROMPT_ENV=unset", "ARG=--permission-mode", "ARG=plan", "ARG=" + prompt}
	claude, wrap := runDirect(t, Line("main", "plan", "spawn"), cwd, prompt, false)
	if !claude.called || wrap.called || strings.Join(claude.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("claude ran %v %q (wrapper %v); shell said %q\nwant %q", claude.called, claude.lines, wrap.called, claude.output, want)
	}
	// Inside cmux the wrapper next to its bundled CLI runs instead of claude.
	claude, wrap = runDirect(t, Line("main", "plan", "spawn"), cwd, prompt, true)
	if claude.called || !wrap.called || strings.Join(wrap.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("wrapper ran %v %q (claude %v); shell said %q", wrap.called, wrap.lines, claude.called, wrap.output)
	}
	// No mode: no flag at all.
	claude, _ = runDirect(t, Line("main", "", ""), cwd, prompt, false)
	if got := strings.Join(claude.lines, "\n"); strings.Contains(got, "--permission-mode") || !strings.HasSuffix(got, "ARG="+prompt) {
		t.Fatalf("modeless run: %q", claude.lines)
	}
}

// TestLineWithoutLauncherBadCwd: a missing directory runs nothing and says
// so on one stderr line naming it — never a session in the wrong directory.
func TestLineWithoutLauncherBadCwd(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "gone")
	claude, _ := runDirect(t, Line("main", "plan", "spawn"), cwd, "hello", false)
	if claude.called {
		t.Fatalf("claude ran in a missing directory: %q", claude.lines)
	}
	out := strings.TrimSuffix(claude.output, "\n")
	if strings.Count(out, "\n") != 0 || !strings.Contains(out, cwd) || !strings.Contains(out, "nothing was launched") {
		t.Fatalf("want one line naming %s, got %q", cwd, claude.output)
	}
}
