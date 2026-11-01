package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// hookBackend creates a workspace at a fixed temp path, so the hooks have a
// real directory to run in.
type hookBackend struct {
	sentinelBackend
	path string
}

func (b *hookBackend) Create(_ context.Context, p *pigtail.Project, in backend.CreateIntent) (*pigtail.Workspace, error) {
	now := time.Now().UTC()
	return &pigtail.Workspace{ID: "ws_hooks", ProjectID: p.ID, Intent: in.Intent, State: pigtail.StateBuilding,
		Path: b.path, BackendKind: "sentinel", CreatedAt: now, LastActivityAt: now}, nil
}

// hookProject writes a pigtail.yaml with the given hooks and returns its root.
// `name` is required: LoadProjectConfig refuses a file without it, and the
// handlers then run no hook at all.
func hookProject(t *testing.T, hooks string) string {
	t.Helper()
	root := t.TempDir()
	yaml := "version: 1\nname: h\nhooks:\n" + hooks
	if err := os.WriteFile(filepath.Join(root, pigtail.ProjectConfigFilename), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// newHooksRouter serves POST /workspaces/new over a store and a hookBackend.
// Services.Hooks stays nil (context.Background(), untracked): the helper
// compiles unchanged on d0d3d3e, where Services has no Hooks field.
func newHooksRouter(t *testing.T, root, wt string) (*chi.Mux, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(&pigtail.Project{ID: "prj_h", Name: "h", RootPath: root}); err != nil {
		t.Fatal(err)
	}
	svc := &Services{Store: st, Backend: &hookBackend{path: wt}, Bus: events.New()}
	r := chi.NewRouter()
	r.Post("/workspaces/new", NewWorkspace(svc, testResponder{}))
	r.Post("/workspaces/{id}/hooks/run", RunWorkspaceHooks(svc, testResponder{}))
	return r, st
}

// hooksLog is the worktree's hooks.log, quoted in a failure so the run shows
// which hooks began (RunHooks writes "=== hook <name>: …" before each).
func hooksLog(wt string) string {
	b, err := os.ReadFile(devserver.LogPath(wt, "hooks"))
	if err != nil {
		return "(no hooks.log: " + err.Error() + ")"
	}
	return string(b)
}

// waitFile waits up to d for path to exist.
func waitFile(t *testing.T, path string, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// TestNewWorkspaceHooksOutliveTheClient (criterion 1, D0): the client gives up
// while the first hook runs. Every hook must still run to its end. On d0d3d3e
// the hooks ran on r.Context(): the first was killed, the second never began
// (a large monorepo: the dependency hook cut, the later hooks never run).
func TestNewWorkspaceHooksOutliveTheClient(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(devserver.StateDir(wt)) })
	first, second := filepath.Join(wt, "first-done"), filepath.Join(wt, "second-done")
	// The slow hook waits for a gate the test opens only after its client
	// left: it is running when the client goes, whatever the machine's load.
	// The loop gives up after 60 s so a failed run leaves no shell behind.
	gate := filepath.Join(wt, "gate")
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o600) })
	root := hookProject(t,
		"  - name: slow\n    command: i=0; while [ ! -e "+gate+" ] && [ $i -lt 1200 ]; do sleep 0.05; i=$((i+1)); done; touch "+first+"\n"+
			"  - name: after\n    command: touch "+second+"\n")
	r, st := newHooksRouter(t, root, wt)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/workspaces/new",
		strings.NewReader(`{"project_id":"prj_h","intent":"x"}`)).WithContext(ctx)
	done := make(chan struct{})
	go func() { r.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	deadline := time.Now().Add(60 * time.Second) // one wide deadline: it waits for events
	testutil.Eventually(t, time.Until(deadline), "the slow hook begun (=== hook slow in hooks.log)", func() bool {
		return strings.Contains(hooksLog(wt), "=== hook slow")
	})
	cancel() // the client leaves while the first hook runs
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if !waitFile(t, first, time.Until(deadline)) {
		t.Fatalf("the running hook was killed when the client left; hooks.log:\n%s", hooksLog(wt))
	}
	if !waitFile(t, second, time.Until(deadline)) {
		t.Fatalf("the hook after it never ran; hooks.log:\n%s", hooksLog(wt))
	}
	select {
	case <-done:
	case <-time.After(time.Until(deadline)):
		t.Fatal("the handler did not return after its client left")
	}
	ws := waitHooks(t, st, "ws_hooks", time.Until(deadline))
	if ws.Hooks.Status != pigtail.HooksOK || ws.Hooks.Log == "" {
		t.Fatalf("hooks = %+v, want ok with its log", ws.Hooks)
	}
}

// waitHooks waits up to d for the workspace's hooks to leave "running".
func waitHooks(t *testing.T, st *store.Store, id string, d time.Duration) *pigtail.Workspace {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ws, err := st.GetWorkspace(id)
		if err == nil && ws.Hooks != nil && ws.Hooks.Status != pigtail.HooksRunning {
			return ws
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("hooks of %s still unrecorded or running after %s", id, d)
	return nil
}

// TestNewWorkspaceRecordsTheFailedHook (criterion 2, daemon side).
func TestNewWorkspaceRecordsTheFailedHook(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(devserver.StateDir(wt)) })
	root := hookProject(t,
		"  - name: deps\n    command: \"true\"\n"+
			"  - name: husky\n    command: exit 3\n")
	r, st := newHooksRouter(t, root, wt)
	rec := doJSON(t, r, "POST", "/workspaces/new", pigtail.NewWorkspaceRequest{ProjectID: "prj_h", Intent: "x"})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "husky") ||
		!strings.Contains(rec.Body.String(), devserver.LogPath(wt, "hooks")) {
		t.Fatalf("status %d body %s, want 500 naming husky and the log", rec.Code, rec.Body.String())
	}
	ws := waitHooks(t, st, "ws_hooks", 5*time.Second)
	if ws.Hooks.Status != pigtail.HooksFailed || ws.Hooks.Hook != "husky" || ws.Hooks.Log != devserver.LogPath(wt, "hooks") {
		t.Fatalf("hooks = %+v, want failed on husky with its log", ws.Hooks)
	}
}
