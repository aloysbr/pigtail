package devserver

import (
	"bytes"
	"os/exec"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// withStopNotifier installs a recording notifier for the duration of a test
// and restores the package default afterwards.
func withStopNotifier(t *testing.T) *[][2]string {
	t.Helper()
	var got [][2]string
	orig := notifyStopped
	notifyStopped = func(wt, srv string) { got = append(got, [2]string{wt, srv}) }
	t.Cleanup(func() { notifyStopped = orig })
	return &got
}

// TestStopNotifiesTheDaemon: a stop that actually kills a listener must be
// announced, naming the worktree DIRECTORY (absolute — a bare name cannot
// identify a workspace, see notifyStopped) and the server. Without it the
// daemon's death detector would report every `pigtail stop` as a server that
// died on its own — the false-positive flood that would make the signal
// worthless.
func TestStopNotifiesTheDaemon(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	container := t.TempDir()
	port := freeTestPort(t)
	wtDir := mkFakeWorktreeDir(t, container, "wt-one", port)
	spawnPortListener(t, port, wtDir)
	waitPortBusyForTest(t, port)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	var got [][2]string
	stillBusy := false
	orig := notifyStopped
	notifyStopped = func(wt, srv string) {
		got = append(got, [2]string{wt, srv})
		stillBusy, _, _ = IsPortBusy(port)
	}
	defer func() { notifyStopped = orig }()

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := Stop(ctx, "wt-one", ""); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0] != [2]string{wtDir, "dev"} {
		t.Fatalf("notifications = %v, want one for %s:dev", got, wtDir)
	}
	// Ordering matters: the daemon sweeps on its own clock, and a server that
	// releases its port before the notification lands is reported as a death.
	// Observed for real on 2026-07-27 with a 2s sweep interval.
	if !stillBusy {
		t.Fatal("the stop was announced after the port was already free — the daemon can see the gap first")
	}
}

// TestStopDoesNotNotifyForAnAlreadyDeadServer: a port that was already free
// means the server died on its own — exactly the case the daemon is meant to
// report. Announcing it as a deliberate stop would erase the death.
func TestStopDoesNotNotifyForAnAlreadyDeadServer(t *testing.T) {
	container := t.TempDir()
	port := freeTestPort(t) // nothing listening
	mkFakeWorktreeDir(t, container, "wt-one", port)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	got := withStopNotifier(t)
	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := Stop(ctx, "wt-one", ""); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 0 {
		t.Fatalf("notifications = %v, want none (nothing was stopped)", *got)
	}
}

// TestStopNotifierDefaultsToANoOp: the daemon links this package and calls
// Stop from its watchdog kill path. There, the notifier must do nothing —
// the daemon marks its own stops in-process and must never work to itself
// over HTTP.
func TestStopNotifierDefaultsToANoOp(t *testing.T) {
	orig := notifyStopped
	defer func() { notifyStopped = orig }()

	SetStopNotifier(func(string, string) { t.Fatal("the installed notifier should have been replaced") })
	SetStopNotifier(nil)
	notifyStopped("wt", "dev") // must not panic, must not call the previous one
}
