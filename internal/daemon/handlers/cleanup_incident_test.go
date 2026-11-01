package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// An incident (a dev server on a port): `pigtail
// clean` removed the worktree under a running dev server. These tests use
// ONLY seams that exist on the base (Services.PortBusy / ProcessCwds /
// KillPIDs) and fake pids >= 9000001 (above any real pid_max), so the file
// compiles on origin/main and fails there by behaviour, not by build: it is
// the red-on-base proof. They signal nothing.

// orderLog is one ordered record of the kill and the backend teardown.
type orderLog struct {
	mu   sync.Mutex
	list []string
}

func (l *orderLog) add(s string) { l.mu.Lock(); l.list = append(l.list, s); l.mu.Unlock() }
func (l *orderLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.list...)
}

// orderBackend logs Backend.Cleanup into the same ordered log as the kill.
type orderBackend struct {
	sentinelBackend
	log *orderLog
}

func (b *orderBackend) Cleanup(_ context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	b.log.add("backend.Cleanup")
	return backend.CleanupOutcome{}, nil
}

func deleteWorkspace(t *testing.T, svc *Services, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Delete("/workspaces/{id}", CleanupWorkspace(svc, testResponder{}))
	req := httptest.NewRequest(http.MethodDelete, "/workspaces/"+id, nil)
	if body != "" {
		req = httptest.NewRequest(http.MethodDelete, "/workspaces/"+id, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func incidentWorkspace(t *testing.T, bk backend.Backend) (*Services, *pigtail.Workspace) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wt := t.TempDir()
	ws := pigtail.Workspace{
		ID: "ws_cf8935", ProjectID: "proj_1", State: pigtail.StateShipped, Path: wt,
		BackendKind: "sentinel", Ports: map[string]int{"dev": 3044},
		CreatedAt: time.Now(), LastActivityAt: time.Now(),
	}
	svc := newDispatchTestSvc(t, []pigtail.Workspace{ws}, bk)
	svc.Bus = events.New()
	return svc, &ws
}

// Case A: the server dies when killed. The kill must come BEFORE the
// backend teardown (base: no kill at all, the log is ["backend.Cleanup"]).
func TestCleanupIncidentKillsBeforeTheTeardown(t *testing.T) {
	log := &orderLog{}
	svc, ws := incidentWorkspace(t, &orderBackend{log: log})
	alive := true
	svc.PortBusy = func(int) (bool, []int, error) {
		if alive {
			return true, []int{9000001}, nil
		}
		return false, nil, nil
	}
	svc.ProcessCwds = func([]int) (map[int]string, error) { return map[int]string{9000001: ws.Path}, nil }
	svc.KillPIDs = func(p []int) { log.add(fmt.Sprintf("kill %v", p)); alive = false }

	rec := deleteWorkspace(t, svc, ws.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := log.get()
	if len(got) != 2 || got[0] != "kill [9000001]" || got[1] != "backend.Cleanup" {
		t.Fatalf("order = %v, want [kill [9000001] backend.Cleanup]", got)
	}
}

// Case B: the server survives the kill. The clean must refuse and leave the
// DB row and the state directory intact (base: 200, row and directory gone).
func TestCleanupIncidentRefusesWhenTheServerSurvives(t *testing.T) {
	log := &orderLog{}
	svc, ws := incidentWorkspace(t, &orderBackend{log: log})
	stateDir, err := devserver.EnsureStateDir(ws.Path)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stateDir, "dev.owner")
	if err := os.WriteFile(marker, []byte("ws\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.PortBusy = func(int) (bool, []int, error) { return true, []int{9000001}, nil }
	svc.ProcessCwds = func([]int) (map[int]string, error) { return map[int]string{9000001: ws.Path}, nil }
	svc.KillPIDs = func(p []int) { log.add(fmt.Sprintf("kill %v", p)) } // kills nothing

	rec := deleteWorkspace(t, svc, ws.ID, "")
	if rec.Code == http.StatusOK {
		t.Fatalf("clean answered 200 with a dev server still listening: %s", rec.Body.String())
	}
	for _, e := range log.get() {
		if e == "backend.Cleanup" {
			t.Fatalf("backend.Cleanup ran under a live server: %v", log.get())
		}
	}
	if _, err := svc.Store.GetWorkspace(ws.ID); err != nil {
		t.Fatalf("workspace row gone: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("state directory content gone: %v", err)
	}
}
