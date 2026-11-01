package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The backup chain is only as good as its last link, and that link is not
// "the file reopens" — it is "the daemon runs on it". internal/daemon/store
// asserts the restore itself; this file asserts the thing the operator
// actually needs afterwards, on a database that was genuinely broken first.
//
// It lives here rather than in the store package because starting a daemon is
// what is being asserted: the store cannot import it without a cycle.

// TestDaemonStartsOnADatabaseRestoredFromACorruptOne is the end of the chain:
// seed, back up, corrupt the live file beyond opening, restore, and serve.
//
// The last assertion is the load-bearing one. A daemon answering /health
// proves nothing on its own — it answers on an empty database too, which is
// exactly the failure mode a silent restore-to-nothing would produce. Reading
// the restored project back *through the API* is what distinguishes "started"
// from "started on the right data".
func TestDaemonStartsOnADatabaseRestoredFromACorruptOne(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pigtail.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_survivor", Name: "survivor", RootPath: "/tmp/survivor", DefaultBackend: "git",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.BackupRotated(store.BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Truncate in half: a valid header over pages that are gone. A crash
	// mid-write leaves this shape, and SQLite refuses it rather than treating
	// it as a database to create.
	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(dbPath, fi.Size()/2); err != nil {
		t.Fatal(err)
	}
	if broken, err := store.Open(dbPath); err == nil {
		_ = broken.Close()
		t.Fatal("the truncated database still opens; this test would prove nothing")
	}

	if _, err := store.Restore(dbPath, 0, time.Now()); err != nil {
		t.Fatalf("restore over the corrupt database: %v", err)
	}

	restored, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("the daemon's own store cannot open the restored database: %v", err)
	}
	defer restored.Close() //nolint:errcheck

	srv, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: ""}, restored, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer startDaemon(t, srv)()

	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", srv.cfg.SocketPath)
		},
	}}
	waitDaemonUp(t, srv.cfg.SocketPath)

	resp, err := httpc.Get("http://pigtail/health")
	if err != nil {
		t.Fatalf("the daemon does not answer on the restored database: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health status = %d, want 200", resp.StatusCode)
	}

	projResp, err := httpc.Get("http://pigtail/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer projResp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(projResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data []pigtail.Project `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode /projects: %v body=%s", err, body)
	}
	var found bool
	for _, p := range env.Data {
		if p.ID == "prj_survivor" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the daemon started, but not on the restored data: /projects = %s", body)
	}
}

// waitDaemonUp blocks until the daemon's socket accepts a connection.
func waitDaemonUp(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", socketPath)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon socket %s never came up", socketPath)
}
