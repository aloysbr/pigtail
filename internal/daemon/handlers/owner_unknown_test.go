package handlers

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// failingCwds is a cwd probe that never answers (lsof under load).
func failingCwds(calls *int) func([]int) (map[int]string, error) {
	return func([]int) (map[int]string, error) {
		*calls++
		return nil, errors.New("lsof cwd: EMFILE")
	}
}

// rename --force on a port whose owner cannot be determined: 409, nothing
// killed, and the message says the owner is unknown — not another worktree.
func TestRenameForceOwnerUnknown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := filepath.Join(root, "old")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(&pigtail.Project{ID: "prj1", Name: "p", RootPath: root}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(&pigtail.Workspace{
		ID: "ws1", ProjectID: "prj1", Path: old,
		State: pigtail.StateBuilding, Ports: map[string]int{"dev": 39813},
	}); err != nil {
		t.Fatal(err)
	}
	var killed [][]int
	calls := 0
	svc := &Services{
		Store:       st,
		PortBusy:    func(p int) (bool, []int, error) { return p == 39813, []int{4242}, nil },
		ProcessCwds: failingCwds(&calls),
		KillPIDs:    func(p []int) { killed = append(killed, p) },
	}
	r := chi.NewRouter()
	r.Post("/workspaces/{id}/rename", RenameWorkspace(svc, envelopeResponder{}))
	rec := doJSON(t, r, "POST", "/workspaces/ws1/rename", map[string]interface{}{"new_slug": "new", "force": true})
	body := rec.Body.String()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d body %s, want 409", rec.Code, body)
	}
	if strings.Contains(body, "another worktree") || !strings.Contains(body, "owner unknown (cwd probe failed: lsof cwd: EMFILE; pid 4242)") {
		t.Errorf("body misattributes an unknown owner: %s", body)
	}
	if len(killed) != 0 {
		t.Fatalf("killed %v on an unknown owner", killed)
	}
	if calls != 2 {
		t.Errorf("cwd probe ran %d times, want 2 (one retry)", calls)
	}
}

func TestPortsResyncOwnerUnknown(t *testing.T) {
	t.Parallel()
	svc, ws, _ := portsServices(t, 39513, 39500)
	svc.PortBusy = func(p int) (bool, []int, error) { return p == 39513, []int{4242}, nil }
	calls := 0
	svc.ProcessCwds = failingCwds(&calls)
	rec := callPatchPorts(t, svc, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync})
	body := rec.Body.String()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d body %s, want 409", rec.Code, body)
	}
	if strings.Contains(body, "another worktree") || !strings.Contains(body, "owner unknown (cwd probe failed") {
		t.Errorf("body misattributes an unknown owner: %s", body)
	}
	if got, _ := svc.Store.GetWorkspace(ws.ID); got.Ports["dev"] != 39500 {
		t.Fatalf("registry changed on a refusal: %v", got.Ports)
	}
}

// reassign repairs a port held by another worktree. An unknown owner may be
// this worktree's own server: refuse, and say so.
func TestPortsReassignOwnerUnknown(t *testing.T) {
	t.Parallel()
	svc, _, alloc := portsServices(t, 39500, 39500)
	svc.PortBusy = func(p int) (bool, []int, error) { return p == 39500, []int{4242}, nil }
	calls := 0
	svc.ProcessCwds = failingCwds(&calls)
	rec := callPatchPorts(t, svc, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeReassign, Kinds: []string{"dev"}})
	body := rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, "owner unknown (cwd probe failed") {
		t.Fatalf("status %d body %s, want 409 naming the unknown owner", rec.Code, body)
	}
	if got, _ := svc.Store.GetWorkspace("ws_a"); got.Ports["dev"] != 39500 {
		t.Fatalf("registry changed on a refusal: %v", got.Ports)
	}
	if len(alloc.Pending("dev")) != 0 {
		t.Fatalf("pending %v after a refusal", alloc.Pending("dev"))
	}
}
