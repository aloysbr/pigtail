package devserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestRunHooksKillsTheWholeProcessGroup is the one that matters.
//
// A hook runs as `sh -c "npm install"`, so cancelling the command kills the
// *shell*. Anything the shell started keeps running, reparented to init, still
// holding npm's lock — invisible and unkillable through pigtail. The fix is
// Setpgid plus a signal to -pgid; this test is what tells the difference.
func TestRunHooksKillsTheWholeProcessGroup(t *testing.T) {
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(StateDir(wt)) })
	marker := filepath.Join(wt, "orphan-survived")
	pidFile := filepath.Join(wt, "child.pid")

	// A background child that outlives the shell unless the group is killed.
	// It writes its pid first: that file says it was spawned, and once that
	// pid is gone (killed, or done) the marker can no longer appear.
	hooks := []pigtail.HookSpec{{
		Name: "spawner",
		Command: "sh -c 'echo $$ > " + pidFile + ".tmp && mv " + pidFile + ".tmp " + pidFile +
			"; sleep 2; touch " + marker + "' & sleep 30",
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunHooks(ctx, wt, hooks) }()

	child := 0
	testutil.Eventually(t, 30*time.Second, "the child spawned", func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && child > 0
	})
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled hook must be reported as an error")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunHooks did not return after cancellation: the hook is unbounded")
	}

	// Wait for the child to be gone: killed with the group, or done after
	// its own sleep — if the group survived, the marker lands first.
	testutil.Eventually(t, 30*time.Second, "the hook's child gone", func() bool {
		return errors.Is(syscall.Kill(child, 0), syscall.ESRCH)
	})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the hook's child outlived the cancelled hook: only the shell was killed, not the group")
	}
}

// TestRunHooksWritesToItsOwnLog: the output used to go to the daemon's stdout,
// which under launchd is an unrotated daemon.log that ended up holding every
// npm install of every workspace.
func TestRunHooksWritesToItsOwnLog(t *testing.T) {
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(StateDir(wt)) })

	hooks := []pigtail.HookSpec{{Name: "greet", Command: "echo hook-output-marker"}}
	if err := RunHooks(context.Background(), wt, hooks); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(LogPath(wt, "hooks"))
	if err != nil {
		t.Fatalf("no hook log written: %v", err)
	}
	if !strings.Contains(string(body), "hook-output-marker") {
		t.Errorf("hook output is not in its log:\n%s", body)
	}
	if !strings.Contains(string(body), "greet") {
		t.Errorf("the log does not name the hook it ran:\n%s", body)
	}
}

// A hook that fails must still fail — the bounding must not swallow it.
func TestRunHooksStillReportsAFailingHook(t *testing.T) {
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(StateDir(wt)) })

	err := RunHooks(context.Background(), wt, []pigtail.HookSpec{
		{Name: "boom", Command: "exit 7"},
	})
	if err == nil {
		t.Fatal("a failing hook was not reported")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("the error does not name the hook: %v", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("a plain failure was misreported as a timeout: %v", err)
	}
}

// IfFile still gates: a hook whose sentinel is absent must not run.
func TestRunHooksSkipsWhenIfFileMissing(t *testing.T) {
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(StateDir(wt)) })
	marker := filepath.Join(wt, "ran")

	if err := RunHooks(context.Background(), wt, []pigtail.HookSpec{
		{Name: "deps", Command: "touch " + marker, IfFile: "package.json"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook ran although its IfFile sentinel is absent")
	}
}

// TestRunHooksNamesTheHookThatStopped: the record (and spawn's refusal) must
// say which hook failed, and that the hooks after it did not run.
func TestRunHooksNamesTheHookThatStopped(t *testing.T) {
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(StateDir(wt)) })
	never := filepath.Join(wt, "never")
	err := RunHooks(context.Background(), wt, []pigtail.HookSpec{
		{Name: "deps", Command: "true"},
		{Name: "husky", Command: "exit 3"},
		{Name: "env", Command: "touch " + never},
	})
	var he *HookError
	if !errors.As(err, &he) || he.Hook != "husky" {
		t.Fatalf("err = %v, want a *HookError naming husky", err)
	}
	if _, statErr := os.Stat(never); statErr == nil {
		t.Fatal("a hook after the failed one ran")
	}
}
