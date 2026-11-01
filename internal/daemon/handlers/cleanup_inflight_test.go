package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// gatedBackend counts Cleanup calls and holds each one until gate is closed.
type gatedBackend struct {
	sentinelBackend
	entered chan struct{}
	gate    chan struct{}
	calls   atomic.Int32
}

func (b *gatedBackend) Cleanup(_ context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	b.calls.Add(1)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.gate
	return backend.CleanupOutcome{}, nil
}

// TestCleanupRefusesConcurrentTeardown: now that a teardown can legitimately
// run for minutes, a user who retries (or the CLI and the TUI at once) can
// easily launch a second `git worktree remove` against the same path. Two of
// them corrupt each other's .git/worktrees bookkeeping, so the second call
// must be refused outright.
func TestCleanupRefusesConcurrentTeardown(t *testing.T) {
	t.Parallel()
	bk := &gatedBackend{
		entered: make(chan struct{}, 1),
		gate:    make(chan struct{}),
	}
	ws := pigtail.Workspace{
		ID:             "ws_dup",
		ProjectID:      "proj_1",
		State:          pigtail.StateShipped,
		Path:           t.TempDir(),
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, bk)
	svc.Bus = events.New()

	r := chi.NewRouter()
	r.Delete("/workspaces/{id}", CleanupWorkspace(svc, testResponder{}))

	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(first, httptest.NewRequest(http.MethodDelete, "/workspaces/ws_dup", nil))
	}()

	select {
	case <-bk.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first teardown never reached the backend")
	}

	// Run it off the test goroutine: without the guard the second call walks
	// straight into the backend and blocks on the same gate, which would hang
	// the test instead of failing it.
	second := httptest.NewRecorder()
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		r.ServeHTTP(second, httptest.NewRequest(http.MethodDelete, "/workspaces/ws_dup", nil))
	}()
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		close(bk.gate)
		t.Fatal("second concurrent cleanup was not refused; it entered the backend")
	}
	if second.Code != http.StatusConflict {
		close(bk.gate)
		t.Fatalf("second concurrent cleanup: got %d, want %d", second.Code, http.StatusConflict)
	}

	close(bk.gate)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first handler never returned")
	}

	if first.Code != http.StatusOK {
		t.Errorf("first cleanup: got %d, want 200", first.Code)
	}
	if got := bk.calls.Load(); got != 1 {
		t.Errorf("Backend.Cleanup called %d times, want exactly 1", got)
	}
}

// TestCleanupReleasesInflightSlot checks the guard is not sticky: once a
// teardown finishes, the same workspace id can be cleaned again (it may have
// failed the first time and been retried).
func TestCleanupReleasesInflightSlot(t *testing.T) {
	t.Parallel()
	bk := &gatedBackend{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	close(bk.gate) // never block
	ws := pigtail.Workspace{
		ID:             "ws_seq",
		ProjectID:      "proj_1",
		State:          pigtail.StateShipped,
		Path:           t.TempDir(),
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, bk)
	svc.Bus = events.New()

	r := chi.NewRouter()
	r.Delete("/workspaces/{id}", CleanupWorkspace(svc, testResponder{}))

	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, httptest.NewRequest(http.MethodDelete, "/workspaces/ws_seq", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first cleanup: got %d, want 200", rec1.Code)
	}

	// The row is gone now, so a repeat must 404 — not 409, which would mean
	// the in-flight slot was never released.
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodDelete, "/workspaces/ws_seq", nil))
	if rec2.Code == http.StatusConflict {
		t.Fatalf("in-flight slot was never released (got 409)")
	}
}
