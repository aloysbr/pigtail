package cmux

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShimUsable pins what counts as a working per-surface claude shim. The
// failure observed live is a cmux-cli-shims/<SURFACE_ID>/ directory that
// exists but is empty: `claude` typed in that pane then falls through PATH to
// the bare binary, without cmux's --settings, and cmux never registers the
// session.
func TestShimUsable(t *testing.T) {
	root := t.TempDir()

	missing := filepath.Join(root, "missing")
	if ShimUsable(missing) {
		t.Error("a shim root that does not exist is not usable")
	}

	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if ShimUsable(empty) {
		t.Error("an empty shim root (no claude shim inside) is not usable")
	}

	zero := filepath.Join(root, "zero")
	writeFile(t, filepath.Join(zero, "claude"), "", 0o700)
	if ShimUsable(zero) {
		t.Error("a zero-byte claude shim is not usable")
	}

	noexec := filepath.Join(root, "noexec")
	writeFile(t, filepath.Join(noexec, "claude"), "#!/bin/sh\n", 0o600)
	if ShimUsable(noexec) {
		t.Error("a non-executable claude shim is not usable")
	}

	dir := filepath.Join(root, "dir")
	if err := os.MkdirAll(filepath.Join(dir, "claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if ShimUsable(dir) {
		t.Error("a directory named claude is not a usable shim")
	}

	ok := filepath.Join(root, "ok")
	writeFile(t, filepath.Join(ok, "claude"), "#!/usr/bin/env bash\nexec true\n", 0o700)
	if !ShimUsable(ok) {
		t.Error("a non-empty executable claude shim is usable")
	}

	if ShimUsable("") {
		t.Error("an unknown shim root is not usable")
	}
}

// TestClaudeWrapperNextToBundledCLI: the wrapper ships next to the cmux CLI,
// which is exactly how cmux's own shim finds it.
func TestClaudeWrapperNextToBundledCLI(t *testing.T) {
	bin := t.TempDir()
	cli := filepath.Join(bin, "cmux")
	writeFile(t, cli, "#!/bin/sh\n", 0o700)
	wrapper := filepath.Join(bin, ClaudeWrapperName)
	writeFile(t, wrapper, "#!/bin/sh\n", 0o700)
	t.Setenv(EnvBundledCLI, cli)

	got, err := ClaudeWrapper()
	if err != nil {
		t.Fatalf("ClaudeWrapper: %v", err)
	}
	if got != wrapper {
		t.Fatalf("ClaudeWrapper = %q, want %q", got, wrapper)
	}
}

func TestClaudeWrapperMissing(t *testing.T) {
	bin := t.TempDir()
	cli := filepath.Join(bin, "cmux")
	writeFile(t, cli, "#!/bin/sh\n", 0o700)
	t.Setenv(EnvBundledCLI, cli)

	if got, err := ClaudeWrapper(); err == nil {
		t.Fatalf("ClaudeWrapper = %q, want an error when no wrapper sits next to the CLI", got)
	}

	// Present but not executable is just as unusable.
	writeFile(t, filepath.Join(bin, ClaudeWrapperName), "#!/bin/sh\n", 0o600)
	if got, err := ClaudeWrapper(); err == nil {
		t.Fatalf("ClaudeWrapper = %q, want an error for a non-executable wrapper", got)
	}
}

// TestSurfaceFromEnviron reads a process's cmux surface out of its
// environment, as `ps -E` renders it: argv then the environment, space
// separated. The last occurrence wins, because the environment follows argv
// and argv is free text a user controls.
func TestSurfaceFromEnviron(t *testing.T) {
	const id = "2B514208-AFE5-4711-835C-EA9ACD31E843"
	const root = "/var/folders/x/T/cmux-cli-shims/" + id
	line := "/opt/homebrew/bin/claude --append-system-prompt CMUX_SURFACE_ID=bogus " +
		"PATH=/usr/bin CMUX_SURFACE_ID=" + id + " CMUX_CLAUDE_WRAPPER_SHIM_ROOT=" + root + " TERM=xterm"

	got := SurfaceFromEnviron(line)
	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
	if got.ShimRoot != root {
		t.Errorf("ShimRoot = %q, want %q", got.ShimRoot, root)
	}

	// /proc/<pid>/environ is NUL-separated: same answer.
	nul := "CMUX_SURFACE_ID=" + id + "\x00CMUX_CLAUDE_WRAPPER_SHIM_ROOT=" + root + "\x00"
	if got := SurfaceFromEnviron(nul); got.ID != id || got.ShimRoot != root {
		t.Errorf("NUL-separated environ = %+v", got)
	}

	if got := SurfaceFromEnviron("/opt/homebrew/bin/claude PATH=/usr/bin"); got.ID != "" {
		t.Errorf("no cmux surface in environ, got %+v", got)
	}
}

// TestSurfaceShimRootDefault: an environment that names the surface but not
// the shim root (older cmux) falls back to cmux's layout under $TMPDIR.
func TestSurfaceShimRootDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	const id = "2B514208-AFE5-4711-835C-EA9ACD31E843"
	got := SurfaceFromEnviron("CMUX_SURFACE_ID=" + id)
	want := filepath.Join(tmp, "cmux-cli-shims", id)
	if got.ShimRoot != want {
		t.Fatalf("ShimRoot = %q, want %q", got.ShimRoot, want)
	}
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestProcessSurfaceReadsTheEnvironment(t *testing.T) {
	saved := procEnviron
	t.Cleanup(func() { procEnviron = saved })
	const id = "2B514208-AFE5-4711-835C-EA9ACD31E843"
	procEnviron = func(pid int) (string, error) {
		if pid != 42 {
			t.Fatalf("pid = %d, want 42", pid)
		}
		return "/bin/claude CMUX_SURFACE_ID=" + id + " CMUX_CLAUDE_WRAPPER_SHIM_ROOT=/r", nil
	}
	s, err := ProcessSurface(42)
	if err != nil || s.ID != id || s.ShimRoot != "/r" {
		t.Fatalf("ProcessSurface = %+v, %v", s, err)
	}

	procEnviron = func(int) (string, error) { return "", os.ErrNotExist }
	if _, err := ProcessSurface(42); err == nil {
		t.Fatal("an unreadable environment must be an error, not \"not in cmux\"")
	}
}

func TestResumeLine(t *testing.T) {
	got := ResumeLine("/Apps/c mux/cmux-claude-wrapper", "/cfg/mt", "abc")
	want := `CLAUDE_CONFIG_DIR='/cfg/mt' '/Apps/c mux/cmux-claude-wrapper' --resume abc --permission-mode auto`
	if got != want {
		t.Errorf("ResumeLine = %q, want %q", got, want)
	}
	if got := ResumeLine("", "/cfg/mt", "abc"); got != `CLAUDE_CONFIG_DIR='/cfg/mt' claude --resume abc --permission-mode auto` {
		t.Errorf("ResumeLine without wrapper = %q", got)
	}
}

func TestEnvValue(t *testing.T) {
	cases := []struct {
		name, environ, key, want string
		ok                       bool
	}{
		{"macOS ps -E", "/bin/claude --resume x CLAUDE_RELAUNCH_FILE=/s/_dev_ttys1.sid CLAUDE_CONFIG_DIR=/c/mt", "CLAUDE_RELAUNCH_FILE", "/s/_dev_ttys1.sid", true},
		{"linux NUL", "A=1\x00CLAUDE_RELAUNCH_PROTO=1\x00B=2", "CLAUDE_RELAUNCH_PROTO", "1", true},
		{"last occurrence wins", "claude CLAUDE_CONFIG_DIR=/a X=1 CLAUDE_CONFIG_DIR=/b", "CLAUDE_CONFIG_DIR", "/b", true},
		{"absent", "claude CLAUDE_CONFIG_DIR=/a", "CLAUDE_RELAUNCH_FILE", "", false},
		{"suffix of another name is not a match", "claude XCLAUDE_RELAUNCH_FILE=/x", "CLAUDE_RELAUNCH_FILE", "", false},
		{"empty value is present", "claude CLAUDE_RELAUNCH_PROTO= Y=1", "CLAUDE_RELAUNCH_PROTO", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := EnvValue(c.environ, c.key)
			if got != c.want || ok != c.ok {
				t.Fatalf("EnvValue(%q, %q) = %q, %v; want %q, %v", c.environ, c.key, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestProcessEnvironUsesTheSeam(t *testing.T) {
	saved := procEnviron
	t.Cleanup(func() { procEnviron = saved })
	procEnviron = func(pid int) (string, error) {
		if pid != 7 {
			t.Fatalf("pid = %d, want 7", pid)
		}
		return "claude CLAUDE_CONFIG_DIR=/c", nil
	}
	got, err := ProcessEnviron(7)
	if err != nil || got != "claude CLAUDE_CONFIG_DIR=/c" {
		t.Fatalf("ProcessEnviron = %q, %v", got, err)
	}
}
