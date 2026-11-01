package devserver

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestHelperDialer is not a real test: re-executed as a subprocess by
// spawnPortClient, it opens a TCP connection to DEVSERVER_DIAL_PORT, signals
// readiness by creating DEVSERVER_READY_FILE, and then holds the connection
// (and itself) alive until killed. It stands in for the thing that actually
// produced this bug in the wild: a browser tab open on a dev server.
//
// Same re-exec pattern as TestHelperListener, and deliberately a *separate
// process*: a client socket opened from the test process itself would carry
// the test process's own pid and be indistinguishable from the listener.
func TestHelperDialer(t *testing.T) {
	if os.Getenv("DEVSERVER_DIALER") != "1" {
		t.Skip("helper process only")
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+os.Getenv("DEVSERVER_DIAL_PORT"))
	if err != nil {
		os.Exit(1)
	}
	defer conn.Close()
	if err := os.WriteFile(os.Getenv("DEVSERVER_READY_FILE"), []byte("connected"), 0o600); err != nil {
		os.Exit(1)
	}
	// Outlives the listener on purpose: the survival assertion needs to tell
	// "killed by pigtail" apart from "exited because the peer went away".
	time.Sleep(60 * time.Second)
}

// spawnPortClient starts a subprocess holding a client connection to port and
// returns it along with a channel closed when that process exits.
//
// The exit channel is the assertion seam: a process SIGKILLed by pigtail but
// never waited on stays a zombie, and `kill(pid, 0)` on a zombie still
// succeeds — so liveness cannot be probed with a signal here. Owning the
// Wait is what makes "still alive" mean it.
func spawnPortClient(t *testing.T, port int) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "client-ready")
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperDialer") //nolint:gosec // re-exec of the test binary
	cmd.Env = append(os.Environ(),
		"DEVSERVER_DIALER=1",
		fmt.Sprintf("DEVSERVER_DIAL_PORT=%d", port),
		"DEVSERVER_READY_FILE="+ready,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return cmd, exited
		}
		select {
		case <-exited:
			t.Fatalf("client helper for :%d exited before connecting", port)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("client helper never connected to :%d", port)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestIsPortBusyReportsOnlyTheListener is the regression test for the
// hardening entry "IsPortBusy counts a port's clients as if they were the
// server". `lsof -ti :<port>` matches a socket by *either* endpoint, so every
// process merely connected to the port lands in the returned pid set — which
// the caller then treats as the server's identity, and in some paths kills.
//
// Observed live on 2026-07-29: an Arc `Browser Helper` pid in the persisted
// pid set of all five running servers, on three different ports.
func TestIsPortBusyReportsOnlyTheListener(t *testing.T) {
	requireLsof(t)

	port := freeTestPort(t)
	listener := spawnPortListener(t, port, "")
	waitPortBusyForTest(t, port)
	client, _ := spawnPortClient(t, port)

	busy, pids, err := IsPortBusy(port)
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Fatal("a port with a live listener must read as busy")
	}

	listenerPID := listener.Process.Pid
	clientPID := client.Process.Pid
	for _, pid := range pids {
		if pid == clientPID {
			t.Fatalf("a client of :%d (pid %d) must not be reported as holding the port; got pids %v (listener is %d)",
				port, clientPID, pids, listenerPID)
		}
	}
	if len(pids) != 1 || pids[0] != listenerPID {
		t.Fatalf("expected exactly the listener pid %d on :%d, got %v", listenerPID, port, pids)
	}
}

// TestStopSparesAPortsClients is the consequence half, and the reason this is
// not merely an idle-counter inaccuracy: stopServers feeds the very same pid
// set to KillPIDs (SIGTERM then SIGKILL). A `pigtail stop` on a worktree whose
// dev server has an open browser tab therefore signals the browser.
func TestStopSparesAPortsClients(t *testing.T) {
	requireLsof(t)

	container := t.TempDir()
	port := freeTestPort(t)
	wtA := mkFakeWorktreeDir(t, container, "wt-a", port)

	spawnPortListener(t, port, wtA)
	waitPortBusyForTest(t, port)
	_, clientExited := spawnPortClient(t, port)

	ctx := &Context{
		ContainerDir: container,
		Config: &pigtail.ProjectConfig{
			Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
			Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
		},
	}

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := Stop(ctx, "wt-a", ""); err != nil {
		t.Fatal(err)
	}

	// Stop returns after KillPIDs' own SIGTERM→SIGKILL window, so a client
	// caught in the kill set is already gone by now; the grace only covers
	// the child's own teardown.
	select {
	case <-clientExited:
		t.Fatal("pigtail stop killed a process merely connected to the dev server's port")
	case <-time.After(time.Second):
	}

	waitPortFreeForTest(t, port)
}
