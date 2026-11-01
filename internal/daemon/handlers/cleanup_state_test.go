package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// removingBackend tears the worktree directory down for real, like the git
// backend does — which is what makes the ordering under test observable.
type removingBackend struct {
	sentinelBackend
}

func (b *removingBackend) Cleanup(_ context.Context, ws *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	return backend.CleanupOutcome{}, os.RemoveAll(ws.Path)
}

// TestCleanupRemovesTheWorktreeRuntimeState: per-server runtime state (the
// ownership marker, the dev server's log) no longer lives in the worktree, so
// it no longer disappears when the worktree does. Without an explicit removal
// it would accumulate for every worktree ever cleaned — and the log is the
// part measured in megabytes.
//
// The path is resolved BEFORE the teardown on purpose: the state dir is keyed
// on the symlink-resolved worktree path, and symlinks can only be resolved on
// a path that still exists. This test fails if that ordering is inverted,
// because the backend here really removes the directory (on macOS t.TempDir()
// hands out a /var/folders path, i.e. behind the /var → /private/var symlink,
// so an after-the-fact resolution yields a different key).
func TestCleanupRemovesTheWorktreeRuntimeState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	wtDir := t.TempDir()
	stateDir, err := devserver.EnsureStateDir(wtDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "dev.owner"), []byte("ws-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := pigtail.Workspace{
		ID:             "ws_state",
		ProjectID:      "proj_1",
		State:          pigtail.StateShipped,
		Path:           wtDir,
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, &removingBackend{})
	svc.Bus = events.New()

	r := chi.NewRouter()
	r.Delete("/workspaces/{id}", CleanupWorkspace(svc, testResponder{}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/workspaces/ws_state", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("runtime state outlived its worktree at %s (stat err = %v)", stateDir, err)
	}
}
