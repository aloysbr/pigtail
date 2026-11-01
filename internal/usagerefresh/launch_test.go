package usagerefresh

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeClaude writes an executable shell script named claude in dir. It
// records argv, cwd, the cwd's content and its env in rec, then behaves by
// CLAUDE_CONFIG_DIR's basename: fail → stderr + exit 3, hang → exec sleep.
// Never a real claude (Global Constraints).
func fakeClaude(t *testing.T, dir, rec string) string {
	t.Helper()
	p := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"{ printf 'args=%s\\n' \"$*\"; printf 'cwd=%s\\n' \"$(pwd)\"; printf 'ls=%s\\n' \"$(ls -A)\"; env; } > " + strconv.Quote(rec) + "\n" +
		"case \"$CLAUDE_CONFIG_DIR\" in\n" +
		"  */fail) echo 'starting' >&2; echo 'Error: not logged in' >&2; exit 3;;\n" +
		"  */hang) exec sleep 5;;\n" +
		"  */noisy) yes x | head -c 70000 >&2; echo >&2; echo 'Error: X' >&2; exit 3;;\n" +
		"esac\n" +
		"echo 'usage ok'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCleanPathDropsShimAndRelative(t *testing.T) {
	got := CleanPath("/tmp/x/cmux-cli-shims/ABC:/root/shim:.:bin:/usr/local/bin:/opt/homebrew/bin", "/root/shim")
	if got != "/usr/local/bin:/opt/homebrew/bin" {
		t.Fatalf("CleanPath = %q", got)
	}
}

// Review Focus 4: the cmux shim comes first on PATH inside a cmux tab.
func TestResolveClaudeSkipsCmuxShim(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, "T", "cmux-cli-shims", "E1F7")
	real := filepath.Join(root, "real")
	for _, d := range []string{shim, real} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		fakeClaude(t, d, filepath.Join(root, "rec"))
	}
	bin, err := ResolveClaude(CleanPath(shim+":"+real, shim))
	if err != nil || bin != filepath.Join(real, "claude") {
		t.Fatalf("bin = %q, err = %v", bin, err)
	}
}

func TestResolveClaudeNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("x"), 0o644); err != nil { // not executable
		t.Fatal(err)
	}
	_, err := ResolveClaude(dir)
	if !errors.Is(err, ErrClaudeNotFound) || err.Error() != "claude not found on PATH (cmux shim skipped)" {
		t.Fatalf("err = %v", err)
	}
}

// S6: nothing of the calling session leaks into the launched claude.
func TestEnvStripsSessionAndShimVars(t *testing.T) {
	in := []string{
		"HOME=/Users/a", "USER=a", "LANG=fr_FR.UTF-8", "TMPDIR=/tmp/a", "HTTPS_PROXY=http://p",
		"PATH=/shim:/usr/bin", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=s", "CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_CODE_MESSAGING_SOCKET=/x", "CLAUDE_RELAUNCH_FILE=/r", "CLAUDE_CONFIG_DIR=/caller",
		"CMUX_CLAUDE_WRAPPER_SHIM_ROOT=/shim", "CMUX_SURFACE_ID=x", "ANTHROPIC_API_KEY=k", "PIGTAIL_SCOPE=perso",
	}
	got := map[string]string{}
	for _, kv := range Env(in, "/cells/jp", "/usr/bin") {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("duplicate %s", k)
		}
		got[k] = v
	}
	want := map[string]string{
		"HOME": "/Users/a", "USER": "a", "LANG": "fr_FR.UTF-8", "TMPDIR": "/tmp/a", "HTTPS_PROXY": "http://p",
		"PATH": "/usr/bin", "CLAUDE_CONFIG_DIR": "/cells/jp", "CMUX_CLAUDE_HOOKS_DISABLED": "1",
	}
	if len(got) != len(want) {
		t.Fatalf("env = %v\nwant %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func newFakeLauncher(t *testing.T) (*Launcher, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(root, "rec")
	fakeClaude(t, bin, rec)
	env := []string{"HOME=" + root, "PATH=" + bin + ":/usr/bin:/bin", "CLAUDE_RELAUNCH_FILE=/must/not/leak"}
	return New(env), rec
}

// AC7: argv, absolute binary, empty temp cwd removed afterwards, clean env.
func TestLaunchRunsUsageInEmptyTempDir(t *testing.T) {
	l, rec := newFakeLauncher(t)
	if err := l.Launch(context.Background(), "/cells/jp"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"args=-p --safe-mode --no-session-persistence --strict-mcp-config /usage\n", "\nls=\n", "\nCLAUDE_CONFIG_DIR=/cells/jp\n", "\nCMUX_CLAUDE_HOOKS_DISABLED=1\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("record lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "CLAUDE_RELAUNCH_FILE") {
		t.Error("CLAUDE_RELAUNCH_FILE leaked")
	}
	cwd := strings.TrimPrefix(strings.SplitN(s[strings.Index(s, "cwd="):], "\n", 2)[0], "cwd=")
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("temp cwd %s still there (err %v)", cwd, err)
	}
	if !filepath.IsAbs(l.Bin) {
		t.Errorf("bin %q not absolute", l.Bin)
	}
}

func TestLaunchExitCodeAndLastStderrLine(t *testing.T) {
	l, _ := newFakeLauncher(t)
	err := l.Launch(context.Background(), "/cells/fail")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || err.Error() != "claude exited 3: Error: not logged in" {
		t.Fatalf("err = %v", err)
	}
}

// S8: past 64 KB of stderr, the reason still quotes the last line.
func TestLaunchLongStderrKeepsLastLine(t *testing.T) {
	l, _ := newFakeLauncher(t)
	err := l.Launch(context.Background(), "/cells/noisy")
	if err == nil || err.Error() != "claude exited 3: Error: X" {
		t.Fatalf("err = %v", err)
	}
}

// Review Focus 3: the deadline kills the process and Launch returns promptly.
func TestLaunchTimeoutKills(t *testing.T) {
	l, _ := newFakeLauncher(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := l.Launch(ctx, "/cells/hang")
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ctx = %v", ctx.Err())
	}
}

func TestLaunchWithoutBinary(t *testing.T) {
	l := New([]string{"PATH=" + t.TempDir()})
	if err := l.Launch(context.Background(), "/cells/jp"); !errors.Is(err, ErrClaudeNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// AC8 / D3: pigtail never calls the OAuth endpoint itself.
func TestUsageRefreshImportsNoNetwork(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		seen++
		for _, im := range af.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if p == "net" || strings.HasPrefix(p, "net/") {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no source file read — the guard looks at nothing")
	}
}
