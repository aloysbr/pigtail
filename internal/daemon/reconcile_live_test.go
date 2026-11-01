package daemon

import (
	"context"
	"os/exec"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// reconcile_test.go asserts the *rules* against a fake resolver. This asserts
// the wiring: a real git backend, a real worktree, a real SQLite store, and
// the one gesture the whole entry is about — a `git switch` performed inside
// the worktree, behind pigtail's back.
//
// Without it, every piece could be individually correct and the record still
// never move: the fake resolver cannot catch a workspace ID that does not
// survive the round trip through the store, or a repoRoot that resolves
// differently for a real worktree than for a literal path.
func TestReconcileRefreshesTheRecordAfterARealGitSwitch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	st, err := store.Open(dir + "/pigtail.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	container := t.TempDir()
	mainDir := container + "/main"
	for _, args := range [][]string{{"init", "-b", "main", mainDir}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	for _, args := range [][]string{{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = mainDir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	be := gitbackend.New(nil)
	proj := &pigtail.Project{ID: "prj_live", RootPath: container, MainWorktree: "main", DefaultBackend: "git"}
	if err := st.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	ws, err := be.Create(context.Background(), proj, backend.CreateIntent{
		Intent: "say hello", BaseRef: "main", NoFetch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ws.ProjectID = proj.ID
	if err := st.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded branch at creation: %v", ws.BackendData["branch"])

	c := exec.Command("git", "-C", ws.Path, "switch", "-q", "-c", "feat/moved-by-hand")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("switch: %v: %s", err, out)
	}

	wss, _ := st.ListWorkspaces("")
	n := reconcileBranchRecords(context.Background(), wss, be, st.UpdateWorkspaceBackendData, t.Logf)
	t.Logf("reconciled %d record(s)", n)

	got, _ := st.GetWorkspace(ws.ID)
	t.Logf("recorded branch after reconciliation: %v", got.BackendData["branch"])
	if got.BackendData["branch"] != "feat/moved-by-hand" {
		t.Fatalf("record still stale: %v", got.BackendData["branch"])
	}
}
