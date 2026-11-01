package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// cleanRecorder records which workspaces a stub daemon actually finished
// deleting. Guarded by a mutex: the handler runs on the httptest server's
// goroutines while the test reads from its own.
type cleanRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (c *cleanRecorder) add(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, id)
}

func (c *cleanRecorder) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ids...)
}

// newSlowCleanDaemon serves DELETE /workspaces/{id}, sleeping `delay` on each
// call so a test can prove whether the per-target budget is really per-target.
func newSlowCleanDaemon(t *testing.T, delay time.Duration) (*stubDaemon, *cleanRecorder) {
	t.Helper()
	rec := &cleanRecorder{}

	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/workspaces/")
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			// Client gave up: don't record, don't answer.
			return
		}
		rec.add(id)
		writeData(w, http.StatusOK, map[string]any{"workspace_id": id})
	})

	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	return s, rec
}

// TestCleanUsesAPerTargetDeadline proves each `pigtail clean` target gets its
// own budget. With a single context shared by the whole loop, the budget is
// consumed by the first targets and the later ones die with
// "context deadline exceeded" (issue #50).
func TestCleanUsesAPerTargetDeadline(t *testing.T) {
	const perCallDelay = 120 * time.Millisecond
	s, rec := newSlowCleanDaemon(t, perCallDelay)

	prev := cleanTimeout
	cleanTimeout = 200 * time.Millisecond
	t.Cleanup(func() { cleanTimeout = prev })

	withStubDaemon(t, s, func() {
		cmd := cleanCmd()
		cmd.SetArgs([]string{"ws_one", "ws_two", "ws_three"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("clean: %v", err)
		}
	})

	if got := rec.snapshot(); len(got) != 3 {
		t.Fatalf("expected all 3 targets cleaned, got %d: %v", len(got), got)
	}
}

// TestCleanTimeoutFlagOverridesDefault checks --timeout is wired through to
// the per-target budget.
func TestCleanTimeoutFlagOverridesDefault(t *testing.T) {
	s, _ := newSlowCleanDaemon(t, 5*time.Second)

	withStubDaemon(t, s, func() {
		cmd := cleanCmd()
		cmd.SetArgs([]string{"ws_slow", "--timeout", "50ms"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected clean to time out with --timeout=50ms")
		}
		if !strings.Contains(err.Error(), "deadline exceeded") {
			t.Fatalf("expected a deadline error, got %v", err)
		}
	})
}

func TestPrintCleanOutcomeNamesStoppedServers(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printCleanOutcome(&buf, "ws_cf8935", &pigtail.CleanupResponse{
		ServersStopped:   []pigtail.StoppedServer{{Kinds: []string{"dev", "web"}, Port: 3044, PIDs: []int{9000001, 9000002}}},
		BranchKept:       "x",
		BranchKeptReason: "unmerged",
	})
	want := "✓ cleaned ws_cf8935\n" +
		"  ○ stopped dev+web (:3044, pid 9000001,9000002)\n" +
		"  ⚠ branch x kept: unmerged\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
	buf.Reset()
	printCleanOutcome(&buf, "ws_a", nil)
	if buf.String() != "✓ cleaned ws_a\n" {
		t.Fatalf("nil outcome printed %q", buf.String())
	}
}
