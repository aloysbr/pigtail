package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// blockingBackend records the context Cleanup received, signals on started,
// then blocks until release is closed. It lets a test cancel the HTTP request
// while the teardown is mid-flight and observe what happened to the context
// the backend is actually using.
type blockingBackend struct {
	sentinelBackend
	started chan struct{}
	release chan struct{}

	// capturedCtx is written before started is closed, so the channel
	// close/receive pair gives the reader a happens-before edge.
	capturedCtx context.Context
}

func (b *blockingBackend) Cleanup(ctx context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	b.capturedCtx = ctx
	close(b.started)
	<-b.release
	return backend.CleanupOutcome{}, nil
}

// TestCleanupSurvivesClientDisconnect is the regression test for issue #50:
// a client that goes away (Ctrl-C, timeout) must not cancel the git worktree
// teardown mid-deletion, which leaves a half-destroyed worktree behind.
func TestCleanupSurvivesClientDisconnect(t *testing.T) {
	t.Parallel()
	bk := &blockingBackend{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	ws := pigtail.Workspace{
		ID:             "ws_detach",
		ProjectID:      "proj_1",
		Intent:         "detach me",
		State:          pigtail.StateShipped,
		Path:           t.TempDir(),
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, bk)
	svc.Bus = events.New()
	sub := svc.Bus.Subscribe()
	defer sub.Close()

	r := chi.NewRouter()
	r.Delete("/workspaces/{id}", CleanupWorkspace(svc, testResponder{}))

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req := httptest.NewRequest(http.MethodDelete, "/workspaces/ws_detach", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(rec, req)
	}()

	select {
	case <-bk.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Backend.Cleanup was never called")
	}

	// The client disconnects mid-teardown.
	cancelReq()

	// No wait: a cancel reaches the contexts derived from it synchronously,
	// inside cancelReq (the request context and chi's value context are
	// standard ones). A teardown context derived from the request would be
	// Done here already; the one cleanup.go builds (context.WithoutCancel)
	// never is.

	if err := bk.capturedCtx.Err(); err != nil {
		t.Fatalf("teardown context was cancelled by the client disconnect: %v", err)
	}
	select {
	case <-bk.capturedCtx.Done():
		t.Fatal("teardown context Done() closed after the client disconnected")
	default:
	}

	dl, ok := bk.capturedCtx.Deadline()
	if !ok {
		t.Fatal("teardown context has no deadline; it needs a hung-git backstop")
	}
	if remaining := time.Until(dl); remaining < time.Minute {
		t.Fatalf("teardown budget is only %v; expected a multi-minute backstop", remaining)
	}

	close(bk.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never returned")
	}

	// The teardown completed despite the client having left: the store row
	// is gone and cleanup_done was published.
	if _, err := svc.Store.GetWorkspace("ws_detach"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("workspace row should be deleted, got err=%v", err)
	}
	if !waitForEvent(t, sub, pigtail.EventWorkspaceCleanupDone) {
		t.Fatalf("expected %s to be published", pigtail.EventWorkspaceCleanupDone)
	}
}

// failingBackend always refuses to clean up.
type failingBackend struct {
	sentinelBackend
}

func (b *failingBackend) Cleanup(_ context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	return backend.CleanupOutcome{}, errors.New("git worktree remove: exit status 128: fatal: validation failed")
}

// TestCleanupPublishesFailureEvent checks a failed teardown terminates the
// cleanup_started it published, and lands in the daemon log with the git
// output attached.
func TestCleanupPublishesFailureEvent(t *testing.T) {
	t.Parallel()
	ws := pigtail.Workspace{
		ID:             "ws_fail",
		ProjectID:      "proj_1",
		State:          pigtail.StateShipped,
		Path:           t.TempDir(),
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, &failingBackend{})
	svc.Bus = events.New()

	var logMu sync.Mutex
	var logged []string
	svc.Logf = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	sub := svc.Bus.Subscribe()
	defer sub.Close()

	r := chi.NewRouter()
	r.Delete("/workspaces/{id}", CleanupWorkspace(svc, testResponder{}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/workspaces/ws_fail", nil))

	if rec.Code == http.StatusOK {
		t.Fatalf("expected a failure status, got %d", rec.Code)
	}
	if !waitForEvent(t, sub, pigtail.EventWorkspaceCleanupFailed) {
		t.Errorf("expected %s to be published", pigtail.EventWorkspaceCleanupFailed)
	}
	if _, err := svc.Store.GetWorkspace("ws_fail"); err != nil {
		t.Errorf("the store row must survive a failed teardown: %v", err)
	}

	logMu.Lock()
	defer logMu.Unlock()
	if len(logged) != 1 || !strings.Contains(logged[0], "validation failed") {
		t.Errorf("expected the git output in the daemon log, got %v", logged)
	}
}

// waitForEvent drains sub looking for an event of the given type.
func waitForEvent(t *testing.T, sub *events.Subscription, want pigtail.EventType) bool {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				return false
			}
			if e.Type == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
