package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// postRecorder is a daemon that records the rows POSTed to /peers and echoes
// nothing back; GET /peers answers an empty list.
type postRecorder struct {
	mu     sync.Mutex
	posted [][]pigtail.PeerSession
}

func (r *postRecorder) rows() [][]pigtail.PeerSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]pigtail.PeerSession(nil), r.posted...)
}

func startPostRecorder(t *testing.T) *postRecorder {
	t.Helper()
	rec := &postRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/peers", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			var body pigtail.PeersRequest
			_ = json.NewDecoder(req.Body).Decode(&body)
			rec.mu.Lock()
			rec.posted = append(rec.posted, body.Peers)
			rec.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	dir := testutil.MkdirTemp(t, "ptpost-") // short: unix socket path cap
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	pointClientAt(t, sock)
	t.Cleanup(func() { _ = srv.Close(); _ = os.RemoveAll(dir) })
	return rec
}

// deadPID returns the pid of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// fakeCmuxWithDeadSession points cmux at a script listing one session whose
// pid is dead and whose config dir is set, so peers.Alive reports it dead.
func fakeCmuxWithDeadSession(t *testing.T, sid string) {
	t.Helper()
	cfg := t.TempDir()
	script := filepath.Join(t.TempDir(), "cmux")
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nsessions) echo '{\"sessions\":[{\"session_id\":%q,\"agent\":\"claude\",\"pid\":%d,\"session_dir\":%q,\"workspace_id\":\"W1\",\"surface_id\":\"S1\"}]}' ;;\n*) exit 1 ;;\nesac\n",
		sid, deadPID(t), cfg)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cmux.EnvBundledCLI, script)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	// Hermetic self row: nothing of the developer's real cell, cmux tab or cwd.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv(cmux.EnvWorkspaceID, "TEST-WS")
	t.Setenv(cmux.EnvSurfaceID, "TEST-SURFACE")
	t.Setenv("CLAUDE_PID", "")
	t.Chdir(t.TempDir())
}

func TestRegisterPostsLiveSessionsOnly(t *testing.T) {
	const dead, self = "dead-session-1", "self-session-1"
	fakeCmuxWithDeadSession(t, dead)
	t.Setenv("CLAUDE_CODE_SESSION_ID", self)
	rec := startPostRecorder(t)

	if _, err := register(context.Background(), sessionClient(os.Getenv), os.Stderr); err != nil {
		t.Fatal(err)
	}
	posts := rec.rows()
	if len(posts) != 1 {
		t.Fatalf("posts = %d", len(posts))
	}
	if len(posts[0]) != 1 || posts[0][0].SessionID != self {
		t.Fatalf("posted %+v, want only the caller's row", posts[0])
	}
}

func TestRelayPeersKeepsDeadTabAsDead(t *testing.T) {
	const dead, self = "dead-session-2", "self-session-2"
	fakeCmuxWithDeadSession(t, dead)
	t.Setenv("CLAUDE_CODE_SESSION_ID", self)
	rec := startPostRecorder(t)

	byID := relayPeers(context.Background(), newClient(), os.Stderr)
	for _, row := range rec.rows()[0] {
		if row.SessionID == dead {
			t.Fatalf("dead session was posted: %+v", row)
		}
	}
	p, ok := byID[dead]
	if !ok {
		t.Fatalf("dead tab missing from the listing: %+v", byID)
	}
	if got := peerState(p, true); got != "dead" {
		t.Fatalf("state = %q, want dead", got)
	}
}
