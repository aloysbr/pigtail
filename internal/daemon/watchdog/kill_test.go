package watchdog

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestHelperListener is not a real test: re-executed as a subprocess by
// TestDefaultKillerFallback, it listens on the given port until killed.
func TestHelperListener(t *testing.T) {
	if os.Getenv("WATCHDOG_HELPER") != "1" {
		t.Skip("helper process only")
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("WATCHDOG_PORT"))
	if err != nil {
		os.Exit(1)
	}
	defer l.Close()
	time.Sleep(60 * time.Second)
}

// TestDefaultKillerFallback exercises the real signal escalation: a child
// listener in its own process group (like a spawned dev server) must be
// gone and its port free after DefaultKiller. Gated on lsof, which
// devserver.IsPortBusy shells out to.
func TestDefaultKillerFallback(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	// Reserve a free port, then release it for the child.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	// The listener runs inside the target's worktree: the escalation only
	// signals the worktree's own processes.
	wsDir := t.TempDir()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=TestHelperListener") //nolint:gosec // re-exec of the test binary
	cmd.Dir = wsDir
	cmd.Env = append(os.Environ(), "WATCHDOG_HELPER=1", fmt.Sprintf("WATCHDOG_PORT=%d", port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Wait for the child listener to come up.
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy, _, _ := devserver.IsPortBusy(port)
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper listener never bound its port")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Workspace path resolves to no pigtail project → DefaultKiller takes
	// the process-group fallback directly.
	target := Target{
		Workspace: pigtail.Workspace{ID: "ws_kill", Path: wsDir},
		Port:      port,
		PIDs:      []int{cmd.Process.Pid},
	}
	if err := DefaultKiller(target); err != nil {
		t.Fatalf("DefaultKiller: %v", err)
	}
	if busy, pids, _ := devserver.IsPortBusy(port); busy {
		t.Fatalf("port :%d still busy after kill (pids %v)", port, pids)
	}
}
