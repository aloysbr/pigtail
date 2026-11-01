package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// gitServices builds Services on the real git backend and one shared
// allocator, with a project on a fresh repo (no pigtail.yaml: no hooks).
func gitServices(t *testing.T) (*Services, *pigtail.Project) {
	t.Helper()
	container := t.TempDir()
	mainDir := filepath.Join(container, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	proj := &pigtail.Project{
		ID: "prj_t", Name: "t", RootPath: container, MainWorktree: "main", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {39350, 39399}},
	}
	if err := st.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	alloc := ports.New(st, ports.WithBindProbe(func(int) bool { return true }))
	return &Services{Store: st, Bus: events.New(), Backend: gitbackend.New(alloc), Ports: alloc}, proj
}

func callRoute(t *testing.T, path string, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Post(path, h)
	buf, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf)))
	return rec
}

func callNewWorkspace(t *testing.T, svc *Services, req pigtail.NewWorkspaceRequest) *httptest.ResponseRecorder {
	t.Helper()
	return callRoute(t, "/workspaces/new", NewWorkspace(svc, testResponder{}), req)
}

func callDispatch(t *testing.T, svc *Services, req pigtail.DispatchRequest) *httptest.ResponseRecorder {
	t.Helper()
	return callRoute(t, "/dispatch", Dispatch(svc, testResponder{}), req)
}

// At the instant the row is written, the port must still be pending: this is
// the window the base left open (git.go confirmed before the handler
// persisted). Red on the base: Pending is empty when the seam runs.
func TestPortStillPendingWhenTheRowIsPersisted(t *testing.T) {
	t.Parallel()
	requireGit(t)
	svc, proj := gitServices(t)
	var pendingAtPersist bool
	svc.CreateWorkspace = func(ws *pigtail.Workspace) error {
		pendingAtPersist = svc.Ports.Pending("dev")[ws.Ports["dev"]]
		return svc.Store.CreateWorkspace(ws)
	}
	rec := callNewWorkspace(t, svc, pigtail.NewWorkspaceRequest{ProjectID: proj.ID, Intent: "pending at persist", BaseRef: "main", NoFetch: true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if !pendingAtPersist {
		t.Fatal("the port left the pending set before its row was written")
	}
	if len(svc.Ports.Pending("dev")) != 0 {
		t.Fatalf("port still pending once the create finished: %v", svc.Ports.Pending("dev"))
	}
}

// A create abandoned after Backend.Create gives its port back: the next
// allocation can have it again.
func TestAbandonedCreateReleasesItsPort(t *testing.T) {
	t.Parallel()
	requireGit(t)
	svc, proj := gitServices(t)
	svc.CreateWorkspace = func(*pigtail.Workspace) error { return errors.New("disk I/O error") }
	rec := callNewWorkspace(t, svc, pigtail.NewWorkspaceRequest{ProjectID: proj.ID, Intent: "persist fails", BaseRef: "main", NoFetch: true})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(svc.Ports.Pending("dev")) != 0 {
		t.Fatalf("port still pending after an abandoned create: %v", svc.Ports.Pending("dev"))
	}
	first := proj.PortRanges["dev"][0]
	if p, err := svc.Ports.Allocate(proj.PortRanges, "dev"); err != nil || p != first {
		t.Fatalf("next allocation got %d (%v), want the released %d", p, err, first)
	}
}

// Every early return after Backend.Create releases — including the guard's
// own failure to list sessions. A regression guard for the deferred
// settlePorts: on the base the port was confirmed inside Create, so nothing
// stayed pending — the red proof of D3 is
// TestPortStillPendingWhenTheRowIsPersisted.
func TestDispatchListSessionsFailureReleasesPort(t *testing.T) {
	t.Parallel()
	requireGit(t)
	svc, proj := gitServices(t)
	svc.ListAgentSessions = func(string) ([]pigtail.AgentSession, error) { return nil, errors.New("database is locked") }
	rec := callDispatch(t, svc, pigtail.DispatchRequest{ProjectID: proj.ID, Intent: "list fails", BaseRef: "main", NoFetch: true})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(svc.Ports.Pending("dev")) != 0 {
		t.Fatalf("port still pending after an abandoned dispatch: %v", svc.Ports.Pending("dev"))
	}
	first := proj.PortRanges["dev"][0]
	if p, err := svc.Ports.Allocate(proj.PortRanges, "dev"); err != nil || p != first {
		t.Fatalf("next allocation got %d (%v), want the released %d", p, err, first)
	}
}
