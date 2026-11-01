package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sentinelBackend is a minimal backend.Backend whose DisplayRef always returns
// a fixed sentinel, making it easy to verify that the handlers stamp the field.
type sentinelBackend struct {
	sentinel string
}

func (b *sentinelBackend) Kind() string { return "sentinel" }
func (b *sentinelBackend) Create(_ context.Context, _ *pigtail.Project, _ backend.CreateIntent) (*pigtail.Workspace, error) {
	return nil, nil
}
func (b *sentinelBackend) Snapshot(_ context.Context, _ *pigtail.Workspace, _ string) (backend.SnapshotID, error) {
	return "", nil
}
func (b *sentinelBackend) Diff(_ context.Context, _ *pigtail.Workspace, _, _ *backend.SnapshotID) (backend.Diff, error) {
	return backend.Diff{}, nil
}
func (b *sentinelBackend) Status(_ context.Context, _ *pigtail.Workspace) (backend.Status, error) {
	return backend.Status{}, nil
}
func (b *sentinelBackend) Cleanup(_ context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	return backend.CleanupOutcome{}, nil
}
func (b *sentinelBackend) Move(_ context.Context, _ *pigtail.Workspace, _ string) error { return nil }
func (b *sentinelBackend) RenameBranch(_ context.Context, _ *pigtail.Workspace, _ string) error {
	return nil
}
func (b *sentinelBackend) DisplayRef(ws *pigtail.Workspace) string { return b.sentinel }
func (b *sentinelBackend) SupportsDelivery() bool                  { return false }

// newDispatchTestSvc creates a Services with an in-memory store seeded with
// the given workspaces and the provided backend.
func newDispatchTestSvc(t *testing.T, workspaces []pigtail.Workspace, bk backend.Backend) *Services {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for i := range workspaces {
		if err := st.CreateWorkspace(&workspaces[i]); err != nil {
			t.Fatalf("seed workspace %q: %v", workspaces[i].ID, err)
		}
	}
	return &Services{Store: st, Backend: bk}
}

// TestListWorkspacesStampsDisplayRef verifies that GET /workspaces stamps
// DisplayRef on every workspace using the backend.
func TestListWorkspacesStampsDisplayRef(t *testing.T) {
	t.Parallel()
	bk := &sentinelBackend{sentinel: "sentinel-ref"}
	ws := pigtail.Workspace{
		ID:             "ws_ref_list",
		ProjectID:      "proj_1",
		Intent:         "my intent",
		State:          pigtail.StateBuilding,
		Path:           "/tmp/ws_ref_list",
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, bk)

	r := chi.NewRouter()
	r.Get("/workspaces", ListWorkspaces(svc, testResponder{}))

	rec := doJSON(t, r, "GET", "/workspaces", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out []pigtail.Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 workspace, got %d", len(out))
	}
	if out[0].DisplayRef != "sentinel-ref" {
		t.Errorf("DisplayRef = %q, want %q", out[0].DisplayRef, "sentinel-ref")
	}
}

// TestGetWorkspaceStampsDisplayRef verifies that GET /workspaces/{id} stamps
// DisplayRef on the returned workspace using the backend.
func TestGetWorkspaceStampsDisplayRef(t *testing.T) {
	t.Parallel()
	bk := &sentinelBackend{sentinel: "sentinel-ref"}
	ws := pigtail.Workspace{
		ID:             "ws_ref_get",
		ProjectID:      "proj_1",
		Intent:         "my intent",
		State:          pigtail.StateBuilding,
		Path:           "/tmp/ws_ref_get",
		BackendKind:    "sentinel",
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, bk)

	r := chi.NewRouter()
	r.Get("/workspaces/{id}", GetWorkspace(svc, testResponder{}))

	rec := doJSON(t, r, "GET", "/workspaces/ws_ref_get", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out pigtail.Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.DisplayRef != "sentinel-ref" {
		t.Errorf("DisplayRef = %q, want %q", out.DisplayRef, "sentinel-ref")
	}
}
