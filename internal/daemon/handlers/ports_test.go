package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// portsServices builds Services for the ports route: a store holding prj
// (dev 39500-39599) and ws_a, whose .env.local says PORT=envPort and whose
// registry says dbPort; the git backend's real PortFile; one allocator;
// nothing listening unless a test swaps PortBusy.
func portsServices(t *testing.T, envPort, dbPort int) (*Services, *pigtail.Workspace, *ports.Allocator) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	if err := st.CreateProject(&pigtail.Project{ID: "prj", Name: "p", RootPath: root, DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {39500, 39599}}}); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(root, "a")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, ".env.local"), []byte(fmt.Sprintf("# keep\nPORT=%d\n", envPort)), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &pigtail.Workspace{ID: "ws_a", ProjectID: "prj", Path: a,
		Ports: map[string]int{"dev": dbPort}, State: pigtail.StateBuilding, BackendKind: "git"}
	if err := st.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	alloc := ports.New(st, ports.WithBindProbe(func(int) bool { return true }))
	svc := &Services{
		Store: st, Bus: events.New(), Backend: gitbackend.New(alloc), Ports: alloc,
		PortBusy:    func(int) (bool, []int, error) { return false, nil, nil },
		ProcessCwds: func([]int) (map[int]string, error) { return map[int]string{}, nil },
	}
	return svc, ws, alloc
}

func callPatchPorts(t *testing.T, svc *Services, id string, req pigtail.PatchPortsRequest) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Patch("/workspaces/{id}/ports", PatchWorkspacePorts(svc, envelopeResponder{}))
	buf, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/workspaces/"+id+"/ports", bytes.NewReader(buf)))
	return rec
}

func TestPortsReassignRefusesWhileOwnServerRuns(t *testing.T) {
	t.Parallel()
	svc, ws, alloc := portsServices(t, 39500, 39500)
	svc.PortBusy = func(p int) (bool, []int, error) { return p == 39500, []int{9}, nil }
	svc.ProcessCwds = func([]int) (map[int]string, error) { return map[int]string{9: ws.Path}, nil }
	rec := callPatchPorts(t, svc, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeReassign, Kinds: []string{"dev"}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "pigtail stop") {
		t.Fatalf("status %d body %s, want 409 naming pigtail stop", rec.Code, rec.Body)
	}
	if len(alloc.Pending("dev")) != 0 {
		t.Fatalf("pending %v after a refusal", alloc.Pending("dev"))
	}
}

func TestPortsReassignAllowedWhenOnlyAForeignerHoldsIt(t *testing.T) {
	t.Parallel()
	svc, _, alloc := portsServices(t, 39500, 39500)
	svc.PortBusy = func(p int) (bool, []int, error) { return p == 39500, []int{4242}, nil }
	svc.ProcessCwds = func([]int) (map[int]string, error) { return map[int]string{4242: "/elsewhere"}, nil }
	rec := callPatchPorts(t, svc, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeReassign, Kinds: []string{"dev"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got, _ := svc.Store.GetWorkspace("ws_a")
	if got.Ports["dev"] == 39500 {
		t.Fatalf("registry %v: not reassigned", got.Ports)
	}
	if len(alloc.Pending("dev")) != 0 {
		t.Fatalf("pending %v after success", alloc.Pending("dev"))
	}
}

func TestPortsResyncRefusesForeignListener(t *testing.T) {
	t.Parallel()
	svc, ws, _ := portsServices(t, 39513, 39500)
	svc.PortBusy = func(p int) (bool, []int, error) { return p == 39513, []int{4242}, nil }
	svc.ProcessCwds = func([]int) (map[int]string, error) {
		return map[int]string{4242: filepath.Join(filepath.Dir(ws.Path), "b", "apps", "web")}, nil
	}
	rec := callPatchPorts(t, svc, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync})
	body := rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, `"holder_path"`) || !strings.Contains(body, filepath.Join(filepath.Dir(ws.Path), "b")) {
		t.Fatalf("status %d body %s, want 409 with the holder's worktree", rec.Code, body)
	}
	if got, _ := svc.Store.GetWorkspace("ws_a"); got.Ports["dev"] != 39500 {
		t.Fatalf("registry changed on a refusal: %v", got.Ports)
	}
}

// reassign wrote .env.local, then the registry refused: the file must be put
// back byte for byte, the allocated port released, and the error answered —
// never a "run resync" dead end.
func TestPortsReassignRestoresEnvLocalWhenTheRegistryFails(t *testing.T) {
	t.Parallel()
	svc, ws, alloc := portsServices(t, 39500, 39500)
	orig, _ := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
	svc.UpdateWorkspacePorts = func(string, map[string]int) error { return errors.New("database is locked") }

	rec := callPatchPorts(t, svc, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeReassign, Kinds: []string{"dev"}})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "database is locked") ||
		!strings.Contains(rec.Body.String(), ".env.local restored") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if now, _ := os.ReadFile(filepath.Join(ws.Path, ".env.local")); string(now) != string(orig) {
		t.Fatalf(".env.local not restored: %q, want %q", now, orig)
	}
	if len(alloc.Pending("dev")) != 0 {
		t.Fatalf("allocated port left pending: %v", alloc.Pending("dev"))
	}
	if got, _ := svc.Store.GetWorkspace("ws_a"); got.Ports["dev"] != 39500 {
		t.Fatalf("registry changed: %v", got.Ports)
	}
}
