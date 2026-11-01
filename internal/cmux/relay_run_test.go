package cmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCLI installs a cmux stand-in that runs script, through the variable
// cliPath honours first.
func fakeCLI(t *testing.T, script string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cmux")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBundledCLI, p)
	// Spawning a shell under a loaded `make check -race` can exceed the 3 s
	// production bound; these tests are about the error text, not the bound.
	old := relayCallTimeout
	relayCallTimeout = time.Minute
	t.Cleanup(func() { relayCallTimeout = old })
}

// A failed cmux call carries cmux's own explanation, not just "exit status 1".
func TestRunErrorCarriesStderr(t *testing.T) {
	fakeCLI(t, `printf 'Error: ERROR: Missing status key or value\n  usage: set_status <key>\n' >&2; exit 1`)
	_, err := run("set-status", "k")
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "cmux set-status") || !strings.Contains(msg, "exit status 1") {
		t.Errorf("err = %q, want the command and the exit status", msg)
	}
	if !strings.Contains(msg, "Missing status key or value usage: set_status <key>") {
		t.Errorf("err = %q, want cmux's stderr on one line", msg)
	}
}

func TestRunErrorStderrIsBounded(t *testing.T) {
	fakeCLI(t, `i=0; while [ $i -lt 200 ]; do printf 'socket not found at /tmp/cmux.sock ' >&2; i=$((i+1)); done; exit 1`)
	_, err := run("list-windows", "--json")
	if err == nil {
		t.Fatal("want an error")
	}
	if n := len([]rune(err.Error())); n > 400 {
		t.Errorf("error is %d runes long: stderr must be truncated", n)
	}
	if !strings.Contains(err.Error(), "…") {
		t.Errorf("err = %q, want a visible truncation mark", err)
	}
}

func TestRunErrorWithoutStderrKeepsExitStatus(t *testing.T) {
	fakeCLI(t, `exit 3`)
	_, err := run("ping")
	if err == nil || err.Error() != "cmux ping: exit status 3" {
		t.Fatalf("err = %v", err)
	}
}

func TestRunSuccessReturnsStdoutOnly(t *testing.T) {
	fakeCLI(t, `echo out; echo noise >&2`)
	out, err := run("ping")
	if err != nil || string(out) != "out\n" {
		t.Fatalf("out = %q err = %v", out, err)
	}
}

// A write aimed at a closed tab answers not_found: cmux is up, the tab is gone.
func TestRunErrorNotFound(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   bool
	}{
		{"workspace-action", `printf 'Error: not_found: Workspace not found\n' >&2; exit 1`, true},
		{"list-status", `printf 'Error: ERROR: Tab not found\n' >&2; exit 1`, true},
		{"invalid params", `printf 'Error: invalid_params: bad value\n' >&2; exit 1`, false},
		{"no stderr", `exit 1`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeCLI(t, c.script)
			_, err := run("workspace-action", "--workspace", "x")
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, ErrWorkspaceNotFound); got != c.want {
				t.Errorf("errors.Is(ErrWorkspaceNotFound) = %v, want %v (err = %v)", got, c.want, err)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Errorf("the exit error must stay reachable: %v", err)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		if errors.Is(runError("workspace-action", context.DeadlineExceeded), ErrWorkspaceNotFound) {
			t.Error("a deadline is not a gone workspace")
		}
	})
}
