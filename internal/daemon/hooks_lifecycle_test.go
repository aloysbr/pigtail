package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newHooksServer builds a real Server (no hand-built Services: correction
// n° 232) over a temp store, with project prj_h whose pigtail.yaml declares
// hooks ({{WT}} = the worktree the fakeBackend creates). It returns the
// server, its store, the project root and the worktree.
func newHooksServer(t *testing.T, hooks string) (*Server, *store.Store, string, string) {
	t.Helper()
	root, wt := t.TempDir(), t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(devserver.StateDir(wt)) })
	yaml := "version: 1\nname: h\nhooks:\n" + strings.ReplaceAll(hooks, "{{WT}}", wt)
	if hooks == "" {
		yaml = "version: 1\nname: h\nhooks: []\n"
	}
	if err := os.WriteFile(filepath.Join(root, pigtail.ProjectConfigFilename), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_h", Name: "h", RootPath: root, DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), &fakeBackend{path: wt}, fakeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	return srv, st, root, wt
}

// createOverSocket posts /workspaces/new through the real client, whose
// context is cancelled after 300 ms: the client leaves while the hooks run.
func createOverSocket(t *testing.T, srv *Server, projectID string) {
	t.Helper()
	unixHTTPClient(t, srv.cfg.SocketPath) // waits for the socket
	c := pigtail.NewSocketClient(srv.cfg.SocketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.New(ctx, pigtail.NewWorkspaceRequest{ProjectID: projectID, Intent: "x"}); err == nil {
		t.Fatal("the create returned before its 2 s hook ended; want the client to give up first")
	}
}

// waitRunning waits up to d for a workspace whose hooks are recorded running.
func waitRunning(t *testing.T, st *store.Store, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		all, err := st.ListWorkspaces("")
		if err == nil {
			for _, ws := range all {
				if ws.Hooks != nil && ws.Hooks.Status == pigtail.HooksRunning {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no workspace with hooks running after %s", d)
}

// seedWorkspaceRow records a minimal workspace of prj_h.
func seedWorkspaceRow(t *testing.T, st *store.Store, id string) {
	t.Helper()
	now := time.Now().UTC()
	seedWorkspace(t, st, &pigtail.Workspace{ID: id, ProjectID: "prj_h", Intent: "x", State: pigtail.StateBuilding,
		Path: filepath.Join(t.TempDir(), id), BackendKind: "fake", CreatedAt: now, LastActivityAt: now})
}

// TestDaemonStopCancelsRunningHooks: the hooks run on the daemon's lifetime —
// stopping the daemon stops them (no orphan npm), and Run still returns within
// its bound.
func TestDaemonStopCancelsRunningHooks(t *testing.T) {
	// The hook's shell writes its pid, then sleeps and touches long-done:
	// once that shell is gone (killed, or done), long-done can no longer
	// appear.
	srv, st, root, wt := newHooksServer(t,
		"  - name: long\n    command: echo $$ > {{WT}}/long.pid.tmp && mv {{WT}}/long.pid.tmp {{WT}}/long.pid && sleep 2 && touch {{WT}}/long-done\n")
	stop := startDaemon(t, srv)
	createOverSocket(t, srv, "prj_h") // POST /workspaces/new, client context cancelled after 300 ms
	waitRunning(t, st, 5*time.Second) // a workspace with hooks_status = running
	pid := waitPidFile(t, filepath.Join(wt, "long.pid"))
	stop() // Run must return within daemonStopTimeout
	testutil.Eventually(t, 30*time.Second, "the hook's shell gone", func() bool { return processGone(pid) })
	if _, err := os.Stat(filepath.Join(wt, "long-done")); err == nil {
		t.Fatal("the hook survived the daemon")
	}
	_ = root
}

// TestDaemonStartFailsStaleRunningHooks: a "running" left by a stopped daemon
// becomes a failure at the next start — never a running forever that blocks
// spawn for good.
func TestDaemonStartFailsStaleRunningHooks(t *testing.T) {
	srv, st, _, _ := newHooksServer(t, "")
	seedWorkspaceRow(t, st, "ws_stale")
	_ = st.SetWorkspaceHooks("ws_stale", pigtail.HooksResult{Status: pigtail.HooksRunning, Log: "/l"})
	stop := startDaemon(t, srv)
	defer stop()
	// FailRunningHooks runs synchronously at the head of Run, before
	// net.Listen (server.go): once the socket accepts, the state is final and
	// is read once.
	testutil.Eventually(t, 60*time.Second, "the daemon's socket accepting", func() bool {
		c, err := net.Dial("unix", srv.cfg.SocketPath)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})
	ws, err := st.GetWorkspace("ws_stale")
	if err != nil {
		t.Fatalf("workspace ws_stale unreadable: %v", err)
	}
	if ws.Hooks == nil || ws.Hooks.Status != pigtail.HooksFailed {
		t.Fatalf("hooks of ws_stale = %+v once the daemon listens, want %s", ws.Hooks, pigtail.HooksFailed)
	}
	if !strings.Contains(ws.Hooks.Error, "daemon stopped") {
		t.Fatalf("hooks = %+v", ws.Hooks)
	}
}

// waitPidFile waits for a hook to write its pid to path and returns it.
func waitPidFile(t *testing.T, path string) int {
	t.Helper()
	pid := 0
	testutil.Eventually(t, 30*time.Second, "pid written to "+path, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	return pid
}

// processGone reports whether pid no longer exists (ESRCH).
func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
