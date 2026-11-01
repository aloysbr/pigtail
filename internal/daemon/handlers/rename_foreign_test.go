package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// rename --force kills this workspace's dev servers. A port held by a process
// of another worktree is not one of them: refuse (409) and kill nothing.
func TestRenameForceRefusesForeignListener(t *testing.T) {
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
	svc := &Services{
		Store:       st,
		PortBusy:    func(p int) (bool, []int, error) { return p == 39813, []int{4242}, nil },
		ProcessCwds: func([]int) (map[int]string, error) { return map[int]string{4242: filepath.Join(root, "other")}, nil },
		KillPIDs:    func(p []int) { killed = append(killed, p) },
	}
	r := chi.NewRouter()
	r.Post("/workspaces/{id}/rename", RenameWorkspace(svc, envelopeResponder{}))
	rec := doJSON(t, r, "POST", "/workspaces/ws1/rename", map[string]interface{}{"new_slug": "new", "force": true})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d body %s, want 409", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{"held by another worktree", "pigtail ports reassign", `"holder_path"`, "other"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q: %s", want, body)
		}
	}
	if len(killed) != 0 {
		t.Fatalf("killed %v: a foreign listener", killed)
	}
}
