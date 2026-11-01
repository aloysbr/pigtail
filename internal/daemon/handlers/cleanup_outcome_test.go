package handlers

import (
	"context"
	"encoding/json"
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
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// keepingBackend reports a branch it declined to delete — the shape issue #53
// introduced, where the teardown succeeds but did not do everything the user
// would assume from "✓ cleaned".
type keepingBackend struct {
	sentinelBackend
}

func (b *keepingBackend) Cleanup(_ context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	return backend.CleanupOutcome{
		BranchKept:       "fix/entity-open-button-reveal-scope",
		BranchKeptReason: `the worktree was on "fix/entity-open-button-reveal-scope", not the recorded "feat/fix-entity-open-button"`,
	}, nil
}

// TestCleanupReportsKeptBranch: a branch the backend declined to delete has to
// reach both the response — the CLI prints from it — and the daemon log, which
// is the only record once the response is gone.
func TestCleanupReportsKeptBranch(t *testing.T) {
	t.Parallel()
	ws := pigtail.Workspace{
		ID:             "ws_kept",
		ProjectID:      "proj_1",
		State:          pigtail.StateShipped,
		Path:           t.TempDir(),
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, &keepingBackend{})
	svc.Bus = events.New()

	var logMu sync.Mutex
	var logged []string
	svc.Logf = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	r := chi.NewRouter()
	r.Delete("/workspaces/{id}", CleanupWorkspace(svc, testResponder{}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/workspaces/ws_kept", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got CleanupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	if got.BranchKept != "fix/entity-open-button-reveal-scope" {
		t.Errorf("branch_kept = %q, want the branch the backend kept", got.BranchKept)
	}
	if !strings.Contains(got.BranchKeptReason, "not the recorded") {
		t.Errorf("branch_kept_reason = %q, want the backend's explanation", got.BranchKeptReason)
	}

	logMu.Lock()
	defer logMu.Unlock()
	if len(logged) != 1 || !strings.Contains(logged[0], "fix/entity-open-button-reveal-scope") {
		t.Errorf("expected the kept branch in the daemon log, got %v", logged)
	}
}

// deletingBackend reports a clean, complete teardown.
type deletingBackend struct {
	sentinelBackend
}

func (b *deletingBackend) Cleanup(_ context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	return backend.CleanupOutcome{BranchDeleted: "feat/all-done"}, nil
}

// TestCleanupStaysQuietOnFullTeardown guards the other direction: the ordinary
// case must not gain a warning line, or the one that matters stops standing out.
func TestCleanupStaysQuietOnFullTeardown(t *testing.T) {
	t.Parallel()
	ws := pigtail.Workspace{
		ID:             "ws_done",
		ProjectID:      "proj_1",
		State:          pigtail.StateShipped,
		Path:           t.TempDir(),
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, &deletingBackend{})
	svc.Bus = events.New()

	var logMu sync.Mutex
	var logged []string
	svc.Logf = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	r := chi.NewRouter()
	r.Delete("/workspaces/{id}", CleanupWorkspace(svc, testResponder{}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/workspaces/ws_done", nil))

	var got CleanupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.BranchDeleted != "feat/all-done" {
		t.Errorf("branch_deleted = %q, want feat/all-done", got.BranchDeleted)
	}
	if got.BranchKept != "" || got.BranchKeptReason != "" {
		t.Errorf("a complete teardown reported a kept branch: %+v", got)
	}

	logMu.Lock()
	defer logMu.Unlock()
	if len(logged) != 0 {
		t.Errorf("a complete teardown should log nothing, got %v", logged)
	}
}
