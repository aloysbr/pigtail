package devserver

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A cwd probe that fails once (lsof under load) is retried: the worktree's
// own server is still recognised, and stopped.
func TestStopRetriesCwdProbeOnce(t *testing.T) {
	container := t.TempDir()
	mine := filepath.Join(container, "feat-y")
	ctx := testContextWithServer(t, container, mine, "PORT=3013\n")
	oldBusy, oldCwds, oldKill, oldOut := portBusy, processCwds, killPIDsFn, out
	t.Cleanup(func() { portBusy, processCwds, killPIDsFn, out = oldBusy, oldCwds, oldKill, oldOut })
	portBusy = func(int) (bool, []int, error) { return true, []int{9}, nil }
	calls := 0
	processCwds = func([]int) (map[int]string, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("lsof cwd: signal: killed")
		}
		return map[int]string{9: mine}, nil
	}
	var killed [][]int
	killPIDsFn = func(p []int) { killed = append(killed, p) }
	var buf bytes.Buffer
	out = &buf

	if err := Stop(ctx, "feat-y", ""); err != nil {
		t.Fatal(err)
	}
	if len(killed) != 1 || !reflect.DeepEqual(killed[0], []int{9}) {
		t.Fatalf("killed %v, want [[9]]: the retry must attribute the server; output %q", killed, buf.String())
	}
}

// Two failed probes: the owner is unknown. Nothing is killed, and the output
// says so — it never claims another worktree holds the port.
func TestStopOwnerUnknownKeepsAndSaysSo(t *testing.T) {
	container := t.TempDir()
	mine := filepath.Join(container, "feat-y")
	ctx := testContextWithServer(t, container, mine, "PORT=3013\n")
	oldBusy, oldCwds, oldKill, oldOut := portBusy, processCwds, killPIDsFn, out
	t.Cleanup(func() { portBusy, processCwds, killPIDsFn, out = oldBusy, oldCwds, oldKill, oldOut })
	portBusy = func(int) (bool, []int, error) { return true, []int{9}, nil }
	calls := 0
	processCwds = func([]int) (map[int]string, error) {
		calls++
		return nil, errors.New("lsof cwd: EMFILE")
	}
	var killed [][]int
	killPIDsFn = func(p []int) { killed = append(killed, p) }
	var buf bytes.Buffer
	out = &buf

	if err := Stop(ctx, "feat-y", ""); err != nil {
		t.Fatal(err)
	}
	if len(killed) != 0 {
		t.Fatalf("killed %v on an unknown owner", killed)
	}
	if calls != 2 {
		t.Errorf("cwd probe ran %d times, want 2 (one retry)", calls)
	}
	got := buf.String()
	if strings.Contains(got, "another worktree") {
		t.Errorf("output %q claims another worktree on a failed probe", got)
	}
	want := "kept dev (:3013) — owner unknown (cwd probe failed: lsof cwd: EMFILE; pid 9), not stopped; retry"
	if !strings.Contains(got, want) {
		t.Errorf("output %q lacks %q", got, want)
	}
}

// `pigtail dev` on a port whose owner cannot be determined starts nothing
// and says the owner is unknown — not "already running", not "another
// worktree".
func TestCheckOwnListenerOwnerUnknown(t *testing.T) {
	old, oldOut := processCwds, out
	t.Cleanup(func() { processCwds, out = old, oldOut })
	processCwds = func([]int) (map[int]string, error) { return nil, errors.New("lsof cwd: EMFILE") }
	var buf bytes.Buffer
	out = &buf
	skip, err := checkOwnListener(&Context{ContainerDir: t.TempDir()}, t.TempDir(), "dev", "dev", 3013, []int{7})
	if !skip || err != nil {
		t.Fatalf("skip=%v err=%v, want skip without error", skip, err)
	}
	got := buf.String()
	if strings.Contains(got, "another worktree") || strings.Contains(got, "already running") {
		t.Errorf("output %q misattributes an unknown owner", got)
	}
	if !strings.Contains(got, "owner unknown (cwd probe failed: lsof cwd: EMFILE; pid 7)") {
		t.Errorf("output %q does not say the owner is unknown", got)
	}
}

func TestClassifyListeners(t *testing.T) {
	c := t.TempDir()
	wt, theirs := filepath.Join(c, "feat-y"), filepath.Join(c, "feat-x")
	o := ClassifyListeners([]int{3, 1, 2}, wt, func([]int) (map[int]string, error) {
		return map[int]string{1: wt, 2: filepath.Join(theirs, "apps")}, nil
	})
	if o.Unknown() || !reflect.DeepEqual(o.Owned, []int{1}) || !reflect.DeepEqual(o.PIDs, []int{1, 2, 3}) {
		t.Fatalf("%+v", o)
	}
	if o.HeldElsewhere() {
		t.Error("an owned pid listens: not held elsewhere")
	}
	o = ClassifyListeners([]int{2}, wt, func([]int) (map[int]string, error) {
		return map[int]string{2: theirs}, nil
	})
	if !o.HeldElsewhere() || o.Describe(c) != "held by another worktree ("+theirs+", pid 2)" || o.HolderPath(c) != theirs {
		t.Fatalf("foreign: %q %q", o.Describe(c), o.HolderPath(c))
	}
	o = ClassifyListeners([]int{2}, wt, func([]int) (map[int]string, error) { return nil, errors.New("boom") })
	if !o.Unknown() || !o.HeldElsewhere() || len(o.Owned) != 0 || o.HolderPath(c) != "" {
		t.Fatalf("unknown: %+v", o)
	}
	if got := o.Describe(c); got != "owner unknown (cwd probe failed: boom; pid 2)" {
		t.Fatalf("unknown: %q", got)
	}
}

// ProcessCwds never hangs on a stuck lsof: the probe is bounded.
func TestProcessCwdsTimesOut(t *testing.T) {
	oldCmd, oldTimeout := lsofCwdCommand, cwdProbeTimeout
	t.Cleanup(func() { lsofCwdCommand, cwdProbeTimeout = oldCmd, oldTimeout })
	lsofCwdCommand = func(ctx context.Context, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "10")
	}
	cwdProbeTimeout = 100 * time.Millisecond
	start := time.Now()
	_, err := ProcessCwds([]int{1})
	if err == nil {
		t.Fatal("a stuck lsof must fail the probe")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("probe took %v, want ~100ms", d)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err %q does not say it timed out", err)
	}
}
