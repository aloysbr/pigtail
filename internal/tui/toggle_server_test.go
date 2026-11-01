package tui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestHelperListener is not a real test: re-executed as a subprocess, it holds
// a port until killed. Same pattern as internal/devserver's and the watchdog's
// — duplicated rather than shared because os.Args[0] re-execs THIS package's
// test binary. A separate process is required: the code under test kills the
// PIDs holding the port, and an in-process listener would kill the test binary.
func TestHelperListener(t *testing.T) {
	if os.Getenv("TUI_HELPER") != "1" {
		t.Skip("helper process only")
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("TUI_PORT"))
	if err != nil {
		os.Exit(1)
	}
	defer l.Close()
	time.Sleep(60 * time.Second)
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// spawnPortListener holds port from a helper process running in dir: the
// toggle only stops a listener whose cwd is inside the worktree.
func spawnPortListener(t *testing.T, port int, dir string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=TestHelperListener") //nolint:gosec // re-exec of the test binary
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TUI_HELPER=1", fmt.Sprintf("TUI_PORT=%d", port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
}

func waitPortBusy(t *testing.T, port int, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if busy, _, _ := devserver.IsPortBusy(port); busy == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port :%d never became busy=%v", port, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stoppedRequest is one POST /servers/stopped seen by the fake daemon.
type stoppedRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Server      string `json:"server"`
}

// TestToggleServerOffAnnouncesTheStop: the TUI's dev/storybook toggle kills the
// server's PIDs directly (it must not go through devserver.Stop, which prints
// to stdout and purges the Turbopack cache). It therefore has to announce the
// stop itself — otherwise every stop from the TUI, the surface a bare `pigtail`
// opens, is reported by the daemon as a server that died on its own.
func TestToggleServerOffAnnouncesTheStop(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	container := t.TempDir()
	yaml := "version: 1\nname: t\nbackend: git\nmain_worktree: main\nports:\n  dev: [3000, 3099]\n"
	if err := os.WriteFile(filepath.Join(container, "pigtail.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(container, "wt")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	port := freeTestPort(t)
	if err := os.WriteFile(filepath.Join(wtDir, ".env.local"),
		[]byte(fmt.Sprintf("PORT=%d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	spawnPortListener(t, port, wtDir)
	waitPortBusy(t, port, true)

	var mu sync.Mutex
	var got []stoppedRequest
	busyAtNotify := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/servers/stopped" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		var req stoppedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		got = append(got, req)
		busyAtNotify, _, _ = devserver.IsPortBusy(port)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client := pigtail.NewHTTPClient(strings.TrimPrefix(srv.URL, "http://"), "")

	msg := execToggleServer(client, wtDir, "dev", "ws1")()

	done, ok := msg.(actionDoneMsg)
	if !ok || !strings.Contains(string(done), "stopped") {
		t.Fatalf("toggle returned %#v, want an actionDoneMsg reporting a stop", msg)
	}
	waitPortBusy(t, port, false)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != (stoppedRequest{WorkspaceID: "ws1", Server: "dev"}) {
		t.Fatalf("daemon received %+v, want one deliberate-stop notice for ws1:dev", got)
	}
	// Same ordering rule as the CLI path: the daemon sweeps on its own clock,
	// so a notice that lands after the port is free arrives too late.
	if !busyAtNotify {
		t.Fatal("the stop was announced after the port was already free — the daemon can see the gap first")
	}
}

// TestToggleServerSurvivesAnUnreachableDaemon: the notification is
// best-effort. With no daemon behind the client, the stop must still happen —
// a dev server the user asked to stop is not held hostage by the daemon.
func TestToggleServerSurvivesAnUnreachableDaemon(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	container := t.TempDir()
	yaml := "version: 1\nname: t\nbackend: git\nmain_worktree: main\nports:\n  dev: [3000, 3099]\n"
	if err := os.WriteFile(filepath.Join(container, "pigtail.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(container, "wt")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	port := freeTestPort(t)
	if err := os.WriteFile(filepath.Join(wtDir, ".env.local"),
		[]byte(fmt.Sprintf("PORT=%d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	spawnPortListener(t, port, wtDir)
	waitPortBusy(t, port, true)

	// A port nothing listens on: every call fails fast, like a dead daemon.
	client := pigtail.NewHTTPClient(fmt.Sprintf("127.0.0.1:%d", freeTestPort(t)), "")

	msg := execToggleServer(client, wtDir, "dev", "ws1")()

	if done, ok := msg.(actionDoneMsg); !ok || !strings.Contains(string(done), "stopped") {
		t.Fatalf("toggle returned %#v, want the stop to happen anyway", msg)
	}
	waitPortBusy(t, port, false)
}

// TestToggleServerSparesForeignListener: the worktree's port is held by a
// process running in another worktree (the 2026-10-05 incident). The toggle
// must neither kill it nor start a second server — it says who holds it.
func TestToggleServerSparesForeignListener(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	container := t.TempDir()
	yaml := "version: 1\nname: t\nbackend: git\nmain_worktree: main\nports:\n  dev: [3000, 3099]\n"
	if err := os.WriteFile(filepath.Join(container, "pigtail.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	wtDir, otherDir := filepath.Join(container, "wt"), filepath.Join(container, "other")
	for _, d := range []string{wtDir, otherDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	port := freeTestPort(t)
	if err := os.WriteFile(filepath.Join(wtDir, ".env.local"),
		[]byte(fmt.Sprintf("PORT=%d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	spawnPortListener(t, port, otherDir)
	waitPortBusy(t, port, true)

	msg := execToggleServer(nil, wtDir, "dev", "ws1")()

	done, ok := msg.(actionDoneMsg)
	if !ok || !strings.Contains(string(done), "held by another worktree") || !strings.Contains(string(done), "other") {
		t.Fatalf("toggle returned %#v, want a refusal naming the holder", msg)
	}
	time.Sleep(300 * time.Millisecond)
	if busy, _, _ := devserver.IsPortBusy(port); !busy {
		t.Fatal("the other worktree's listener was killed")
	}
}
