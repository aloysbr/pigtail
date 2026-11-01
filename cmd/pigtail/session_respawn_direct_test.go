package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRespawnScriptWithLauncherUnchanged: the c branch of the shell relaunch
// script, copied before the direct launch existed, byte for byte (the
// author's path), and the script still ends its if.
func TestRespawnScriptWithLauncherUnchanged(t *testing.T) {
	t.Parallel()
	r := respawnRun{ID: "r1", SurfaceRef: "surface:7", Cwd: "/w", Cell: "ip", ConfigDir: "/cfg/ip", FreshCell: "mp", FreshConfigDir: "/cfg/mp", Mode: "auto", Prompt: "/example-skill x", Model: "sonnet"}
	const want = "# pigtail session respawn r1: typed into surface:7 by its worker, removed by it\n" +
		"if (( ${C_LAUNCH_PROTO:-0} >= 1 )) && (( $+functions[c] )); then\n" +
		"  PIGTAIL_GATE_PASSED=respawn C_CWD='/w' c mp --permission-mode 'auto' --model 'sonnet' '/example-skill x'\n" +
		"else\n"
	s := shellRelaunchScript(r, "sid-old")
	if !strings.HasPrefix(s, want) || !strings.HasSuffix(s, "\nfi\n") {
		t.Fatalf("launcher branch drifted:\n%s\nwant prefix:\n%s", s, want)
	}
}

// runRespawnDirect sources the script in a zsh -f without c nor
// C_LAUNCH_PROTO, PATH = a stub claude + /usr/bin:/bin; wrapper adds a stub
// cmux-claude-wrapper next to a fake CMUX_BUNDLED_CLI_PATH. It returns what
// the stub claude and the stub wrapper recorded ("" = not run) and the
// shell's own output.
func runRespawnDirect(t *testing.T, script string, wrapper bool) (claude, wrap, out string) {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	bin, cmuxBin := t.TempDir(), t.TempDir()
	stub := func(path, rec string) {
		s := "#!/bin/sh\n{ printf 'CWD=%s\\n' \"$(pwd -P)\"; printf 'CFG=%s\\n' \"${CLAUDE_CONFIG_DIR-unset}\"; printf 'GATE=%s\\n' \"${PIGTAIL_GATE_PASSED-unset}\"; for a in \"$@\"; do printf 'ARG=%s\\n' \"$a\"; done; } > " + shQuote(rec) + "\n"
		if err := os.WriteFile(path, []byte(s), 0o755); err != nil { //nolint:gosec // a test stub must be executable
			t.Fatal(err)
		}
	}
	claudeRec, wrapRec := filepath.Join(bin, "claude.out"), filepath.Join(cmuxBin, "wrap.out")
	stub(filepath.Join(bin, "claude"), claudeRec)
	env := []string{}
	for _, e := range os.Environ() {
		switch k, _, _ := strings.Cut(e, "="); k {
		case "PIGTAIL_GATE_PASSED", "C_LAUNCH_PROTO", "CMUX_BUNDLED_CLI_PATH", "PATH", "CLAUDE_CONFIG_DIR":
		default:
			env = append(env, e)
		}
	}
	env = append(env, "PATH="+bin+":/usr/bin:/bin", "CLAUDE_CONFIG_DIR=/cfg/caller")
	if wrapper {
		stub(filepath.Join(cmuxBin, "cmux-claude-wrapper"), wrapRec)
		env = append(env, "CMUX_BUNDLED_CLI_PATH="+filepath.Join(cmuxBin, "cmux"))
	}
	path := filepath.Join(t.TempDir(), "r.relaunch.zsh")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(zsh, "-f", "-c", "source "+shQuote(path)+`; print -r -- "LEFT=${__pt_c-unset}"`)
	cmd.Env = env
	b, _ := cmd.CombinedOutput()
	read := func(p string) string { r, _ := os.ReadFile(p); return string(r) }
	return read(claudeRec), read(wrapRec), string(b)
}

// TestRespawnScriptWithoutLauncherRunsClaude: in a shell without c, the
// script runs claude itself in the session's directory, on the fresh cell's
// config dir, with the mode, the model and the prompt — no gate prefix, no
// --resume — and leaves no variable behind in the tab's shell.
func TestRespawnScriptWithoutLauncherRunsClaude(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "fraîches it's")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "/example-skill reprends — « é » it's $HOME"
	r := respawnRun{ID: "r1", SurfaceRef: "surface:7", Cwd: cwd, Cell: "ip", ConfigDir: "/cfg/ip", FreshCell: "mp", FreshConfigDir: "/cfg/m p", Mode: "auto", Prompt: prompt, Model: "sonnet"}
	want := "CWD=" + real + "\nCFG=/cfg/m p\nGATE=unset\nARG=--permission-mode\nARG=auto\nARG=--model\nARG=sonnet\nARG=" + prompt + "\n"
	claude, wrap, out := runRespawnDirect(t, shellRelaunchScript(r, "sid-old"), false)
	if claude != want || wrap != "" || out != "LEFT=unset\n" {
		t.Fatalf("claude recorded\n%s\nwant\n%s\nwrapper %q, shell %q", claude, want, wrap, out)
	}
	claude, wrap, _ = runRespawnDirect(t, shellRelaunchScript(r, "sid-old"), true)
	if wrap != want || claude != "" {
		t.Fatalf("wrapper recorded\n%s\nwant\n%s\nclaude %q", wrap, want, claude)
	}
	// No rotation, no model: the cell's own dir, no --model.
	r.FreshCell, r.FreshConfigDir, r.Model = "", "", ""
	claude, _, _ = runRespawnDirect(t, shellRelaunchScript(r, "sid-old"), false)
	if !strings.Contains(claude, "CFG=/cfg/ip\n") || strings.Contains(claude, "--model") || strings.Contains(claude, "--resume") {
		t.Fatalf("claude recorded\n%s", claude)
	}
}

// TestRespawnScriptWithoutLauncherBadCwd: a missing directory launches
// nothing and prints one line naming it and how to resume the old session
// on its own cell.
func TestRespawnScriptWithoutLauncherBadCwd(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "gone")
	r := respawnRun{ID: "r1", SurfaceRef: "surface:7", Cwd: cwd, Cell: "ip", ConfigDir: "/cfg/ip", FreshCell: "mp", FreshConfigDir: "/cfg/mp", Mode: "auto", Prompt: "/example-skill x"}
	claude, _, out := runRespawnDirect(t, shellRelaunchScript(r, "sid-old"), false)
	if claude != "" {
		t.Fatalf("claude ran in a missing directory: %q", claude)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 || lines[1] != "LEFT=unset" || !strings.Contains(lines[0], cwd) ||
		!strings.Contains(lines[0], "nothing launched") || !strings.Contains(lines[0], "CLAUDE_CONFIG_DIR=/cfg/ip claude --resume sid-old") {
		t.Fatalf("want one line naming %s and the resume, got %q", cwd, out)
	}
}
