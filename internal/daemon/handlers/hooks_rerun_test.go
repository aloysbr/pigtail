package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seedHooksWorkspace records ws_hooks at wt (project prj_h) with outcome r (nil = unknown).
func seedHooksWorkspace(t *testing.T, st *store.Store, wt string, r *pigtail.HooksResult) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.CreateWorkspace(&pigtail.Workspace{ID: "ws_hooks", ProjectID: "prj_h", Intent: "x",
		State: pigtail.StateBuilding, Path: wt, BackendKind: "sentinel", CreatedAt: now, LastActivityAt: now}); err != nil {
		t.Fatal(err)
	}
	if r != nil {
		if err := st.SetWorkspaceHooks("ws_hooks", *r); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRunWorkspaceHooksRepairsAFailure (criterion 4): a failed workspace whose
// hooks now pass is recorded ok, and the answer says so.
func TestRunWorkspaceHooksRepairsAFailure(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(devserver.StateDir(wt)) })
	fixed := filepath.Join(wt, "fixed")
	root := hookProject(t, "  - name: husky\n    command: touch "+fixed+"\n")
	r, st := newHooksRouter(t, root, wt)
	seedHooksWorkspace(t, st, wt, &pigtail.HooksResult{Status: pigtail.HooksFailed, Hook: "husky", Error: "exit 3", Log: "/old"})

	rec := doJSON(t, r, "POST", "/workspaces/ws_hooks/hooks/run", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("status %d body %s, want 200 with hooks ok", rec.Code, rec.Body.String())
	}
	ws, _ := st.GetWorkspace("ws_hooks")
	if ws.Hooks == nil || ws.Hooks.Status != pigtail.HooksOK || ws.Hooks.Hook != "" || ws.Hooks.Log != devserver.LogPath(wt, "hooks") {
		t.Fatalf("hooks = %+v, want ok, no hook, the worktree's log", ws.Hooks)
	}
	if _, err := os.Stat(fixed); err != nil {
		t.Fatal("the hook did not run")
	}
}

// TestRunWorkspaceHooksRecordsANewFailure: still broken → 200, failed, the hook named.
func TestRunWorkspaceHooksRecordsANewFailure(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(devserver.StateDir(wt)) })
	root := hookProject(t, "  - name: husky\n    command: exit 3\n")
	r, st := newHooksRouter(t, root, wt)
	seedHooksWorkspace(t, st, wt, nil)
	rec := doJSON(t, r, "POST", "/workspaces/ws_hooks/hooks/run", nil)
	ws, _ := st.GetWorkspace("ws_hooks")
	if rec.Code != http.StatusOK || ws.Hooks == nil || ws.Hooks.Status != pigtail.HooksFailed || ws.Hooks.Hook != "husky" {
		t.Fatalf("status %d hooks %+v, want 200 and failed on husky", rec.Code, ws.Hooks)
	}
}

// TestRunWorkspaceHooksRefusals: running → 409 and nothing runs; unknown → 404;
// a project without hooks → 400.
func TestRunWorkspaceHooksRefusals(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(devserver.StateDir(wt)) })
	ran := filepath.Join(wt, "ran")
	root := hookProject(t, "  - name: deps\n    command: touch "+ran+"\n")
	r, st := newHooksRouter(t, root, wt)
	seedHooksWorkspace(t, st, wt, &pigtail.HooksResult{Status: pigtail.HooksRunning, Log: "/l"})
	if rec := doJSON(t, r, "POST", "/workspaces/ws_hooks/hooks/run", nil); rec.Code != http.StatusConflict {
		t.Fatalf("running: status %d, want 409", rec.Code)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("a second run started while the first was running")
	}
	if rec := doJSON(t, r, "POST", "/workspaces/ws_nope/hooks/run", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: status %d, want 404", rec.Code)
	}
	if err := os.WriteFile(filepath.Join(root, pigtail.ProjectConfigFilename), []byte("version: 1\nname: h\nhooks: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = st.SetWorkspaceHooks("ws_hooks", pigtail.HooksResult{Status: pigtail.HooksFailed, Log: "/l"})
	if rec := doJSON(t, r, "POST", "/workspaces/ws_hooks/hooks/run", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no hooks: status %d, want 400", rec.Code)
	}
}
