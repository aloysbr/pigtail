package devserver

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckOwnListenerRefusesForeign(t *testing.T) {
	container := t.TempDir()
	mine, theirs := filepath.Join(container, "feat-y"), filepath.Join(container, "feat-x")
	old := processCwds
	processCwds = func([]int) (map[int]string, error) { return map[int]string{4242: filepath.Join(theirs, "apps", "web")}, nil }
	t.Cleanup(func() { processCwds = old })

	running, err := checkOwnListener(&Context{ContainerDir: container}, mine, "dev", "dev", 3013, []int{4242})
	var fe *ForeignListenerError
	if running || !errors.As(err, &fe) {
		t.Fatalf("running=%v err=%v, want a ForeignListenerError", running, err)
	}
	msg := err.Error()
	for _, want := range []string{"port 3013", theirs, "pid 4242", "pigtail ports reassign --kind dev", mine} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

func TestStartRefusesUnknownHolder(t *testing.T) {
	old := processCwds
	processCwds = func([]int) (map[int]string, error) { return map[int]string{}, nil }
	t.Cleanup(func() { processCwds = old })
	_, err := checkOwnListener(&Context{ContainerDir: t.TempDir()}, t.TempDir(), "dev", "dev", 3013, []int{7})
	if err == nil || !strings.Contains(err.Error(), "unknown holder") {
		t.Fatalf("err %v", err)
	}
}

func TestCheckOwnListenerKeepsAlreadyRunning(t *testing.T) {
	mine := t.TempDir()
	old := processCwds
	processCwds = func([]int) (map[int]string, error) { return map[int]string{9: mine}, nil }
	t.Cleanup(func() { processCwds = old })
	running, err := checkOwnListener(&Context{ContainerDir: filepath.Dir(mine)}, mine, "dev", "dev", 3013, []int{9})
	if !running || err != nil {
		t.Fatalf("running=%v err=%v, want already running", running, err)
	}
}

func TestStartRefusesForeignListener(t *testing.T) {
	container := t.TempDir()
	mine, theirs := filepath.Join(container, "feat-y"), filepath.Join(container, "feat-x")
	ctx := testContextWithServer(t, container, mine, "PORT=3013\n")
	oldBusy, oldCwds, oldBg, oldOut := portBusy, processCwds, runBackgroundFn, out
	t.Cleanup(func() { portBusy, processCwds, runBackgroundFn, out = oldBusy, oldCwds, oldBg, oldOut })
	portBusy = func(int) (bool, []int, error) { return true, []int{4242}, nil }
	processCwds = func([]int) (map[int]string, error) { return map[int]string{4242: theirs}, nil }
	launched := false
	runBackgroundFn = func(string, string, string, int) error { launched = true; return nil }
	var buf bytes.Buffer
	out = &buf

	err := Start(ctx, "feat-y", "dev", false)
	var fe *ForeignListenerError
	if !errors.As(err, &fe) || fe.HolderPath != theirs {
		t.Fatalf("err %v, want a ForeignListenerError naming %s", err, theirs)
	}
	if strings.Contains(buf.String(), "already running") || launched {
		t.Fatalf("printed %q, launched=%v: neither 'already running' nor a launch", buf.String(), launched)
	}
}

// Same refusal through the cmux sidecar path: no sidecar is spawned.
func TestStartInCmuxRefusesForeignListener(t *testing.T) {
	defer installSeams(t)()
	ctx, wtName, _ := cmuxFixture(t)
	theirs := filepath.Join(ctx.ContainerDir, "feat-other")
	oldCwds := processCwds
	t.Cleanup(func() { processCwds = oldCwds })
	portBusy = func(int) (bool, []int, error) { return true, []int{4242}, nil }
	processCwds = func([]int) (map[int]string, error) { return map[int]string{4242: theirs}, nil }
	spawned := false
	spawnSidecar = func(string, string, string) error { spawned = true; return nil }
	var buf bytes.Buffer
	out = &buf

	err := StartInCmux(ctx, wtName, "dev")
	var fe *ForeignListenerError
	if !errors.As(err, &fe) || fe.HolderPath != theirs {
		t.Fatalf("err %v, want a ForeignListenerError naming %s", err, theirs)
	}
	if spawned || strings.Contains(buf.String(), "already running") {
		t.Fatalf("spawned=%v output %q: neither a sidecar nor 'already running'", spawned, buf.String())
	}
}
