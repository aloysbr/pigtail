package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/sessionlaunch"
)

func TestValidatePrompt(t *testing.T) {
	t.Parallel()
	ok := []string{"/example-skill resume sub_abc123", "it's $HOME `x`; rm -rf /"}
	for _, p := range ok {
		if err := validatePrompt(p); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	bad := []string{"", "   ", "line1\nline2", "a\rb", "tab\there", "bell\a", "-p hello", strings.Repeat("x", 501)}
	for _, p := range bad {
		if err := validatePrompt(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestResolvePermissionMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, args, cellDef, current string
		want                         permDecision
		wantErr                      string
	}{
		{"same everywhere", "/x/claude --settings s", "auto", "auto", permDecision{"auto", "auto", "cell default", "auto"}, ""},
		{"lowered mid-session (shift-tab)", "/x/claude", "auto", "plan", permDecision{"plan", "auto", "cell default", "plan"}, ""},
		{"raised mid-session: launch wins", "/x/claude --permission-mode default", "auto", "auto", permDecision{"default", "default", "caller argv", "auto"}, ""},
		{"nothing in argv nor settings", "/x/claude", "", "acceptEdits", permDecision{"default", "default", "Claude default", "acceptEdits"}, ""},
		{"launched bypassed", "/x/claude --dangerously-skip-permissions", "auto", "auto", permDecision{}, "bypassPermissions"},
		{"cell default bypass", "/x/claude", "bypassPermissions", "auto", permDecision{}, "bypassPermissions"},
		{"currently bypassed", "/x/claude", "auto", "bypassPermissions", permDecision{}, "bypassPermissions now"},
		{"unknown current mode", "/x/claude", "auto", "yolo", permDecision{}, "unknown permission mode"},
	}
	for _, c := range cases {
		got, err := resolvePermissionMode(c.args, c.cellDef, c.current)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want one naming %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v", c.name, got, err, c.want)
		}
	}
}

func TestReadPermissionStamp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGTAIL_PERMISSION_STAMP_DIR", dir)
	now := time.Unix(1_000_000, 0)
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readPermissionStamp("sess-1", now); err == nil || !strings.Contains(err.Error(), "relance la commande") {
		t.Fatalf("missing = %v", err)
	}
	write("sess-1", "plan 999990\n")
	if m, err := readPermissionStamp("sess-1", now); err != nil || m != "plan" {
		t.Fatalf("fresh = %q %v", m, err)
	}
	// A go given 9 minutes after the hook ran (permission dialog) is still this call's.
	write("sess-1", "auto 999460\n")
	if m, err := readPermissionStamp("sess-1", now); err != nil || m != "auto" {
		t.Fatalf("9 min old = %q %v", m, err)
	}
	write("sess-1", "auto 999340\n") // 11 min
	if _, err := readPermissionStamp("sess-1", now); err == nil || !strings.Contains(err.Error(), "old") || !strings.Contains(err.Error(), "relance la commande") {
		t.Fatalf("stale = %v", err)
	}
	write("sess-1", "garbage")
	if _, err := readPermissionStamp("sess-1", now); err == nil {
		t.Fatal("corrupt record accepted")
	}
}

func TestReadPermissionStampNamesDivergedIDs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGTAIL_PERMISSION_STAMP_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "hook-id"), []byte("auto 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readPermissionStamp("pidfile-id", time.Now())
	if err == nil || !strings.Contains(err.Error(), "pidfile-id") || !strings.Contains(err.Error(), "1 other record(s)") || !strings.Contains(err.Error(), "possibly") {
		t.Fatalf("diverged = %v", err)
	}
	if strings.Contains(err.Error(), "hook-id") {
		t.Fatalf("a foreign session id leaked into the message: %v", err)
	}
	// An older record (another session, minutes ago) is no hint at all.
	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "hook-id"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := readPermissionStamp("pidfile-id", time.Now()); err == nil || strings.Contains(err.Error(), "other record") {
		t.Fatalf("old foreign record cited: %v", err)
	}
}

func TestConsumePermissionStamp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGTAIL_PERMISSION_STAMP_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "s"), []byte("auto 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := consumePermissionStamp("s"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s")); !os.IsNotExist(err) {
		t.Fatalf("record still there: %v", err)
	}
	if err := consumePermissionStamp("s"); err != nil {
		t.Fatalf("consuming twice = %v, want nil", err)
	}
}

// TestLaunchLineExecutes runs the generated line's inner script under zsh -f
// with a stub `c` and the launch env: the prompt (non-ASCII and shell
// metacharacters included), the cwd and the mode must reach c's argv byte for
// byte, the carrier variables must be gone before c runs (they would show in
// ps -E and could be misread as PWD=…), and a shell without the launcher (no
// C_LAUNCH_PROTO) must run claude itself in the cwd instead of c.
// Skipped where zsh is absent (CI Linux image).
func TestLaunchLineExecutes(t *testing.T) {
	t.Parallel()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	prompt := `it's é— $HOME ` + "`id`" + `; echo pwned PWD=/evil`
	cwd := t.TempDir() + "/it's-fraîches"
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(l, pre string) string {
		inner := strings.Replace(innerScript(t, l), launchTail, `print -r -- "AFTER=${PIGTAIL_GATE_PASSED-unset}${(k)parameters[PIGTAIL_GATE_PASSED]}"; exit 0`, 1)
		stub := `c() { print -r -- "GATE=${PIGTAIL_GATE_PASSED-unset}"; print -r -- "CWD=$C_CWD"; print -r -- "LEFT=${` + envSpawnPrompt + `-unset}${` + envSpawnCwd + `-unset}"; for a in "$@"; do print -r -- "ARG=$a"; done }; `
		cmd := exec.Command(zsh, "-f", "-c", pre+stub+inner)
		// A session born from a spawn carries PIGTAIL_GATE_PASSED: the stub must not see the caller's.
		base := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, sessionlaunch.GatePassedVar+"=") })
		cmd.Env = append(base, launchEnv(cwd, prompt)...)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}

	out := run(launchLine("hb", "plan", "spawn"), "C_LAUNCH_PROTO=1; ")
	want := "GATE=spawn\nCWD=" + cwd + "\nLEFT=unsetunset\nARG=hb\nARG=--permission-mode\nARG=plan\nARG=" + prompt + "\nAFTER=unset\n"
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}

	// No launcher (no C_LAUNCH_PROTO): c is never called, claude runs
	// itself — here a stub first on PATH, never the real one.
	bin := t.TempDir()
	stub := "#!/bin/sh\nprintf 'CLAUDE=%s\\n' \"$(pwd -P)\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatal(err)
	}
	out = run(launchLine("hb", "plan", "spawn"), "unset C_LAUNCH_PROTO CMUX_BUNDLED_CLI_PATH; PATH="+shQuote(bin)+":/usr/bin:/bin; ")
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if want := "CLAUDE=" + realCwd + "\nCLAUDE=--permission-mode\nCLAUDE=plan\nCLAUDE=" + prompt + "\nAFTER=unset\n"; out != want {
		t.Fatalf("no launcher: got\n%s\nwant\n%s", out, want)
	}

	out = run(launchLine("hb", "", "spawn"), "C_LAUNCH_PROTO=1; ")
	if strings.Contains(out, "--permission-mode") {
		t.Fatalf("no mode flag expected: %q", out)
	}
}

// innerScript undoes the outer `/bin/zsh -i -c '<inner>'` quoting.
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
