package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// projectFor rebuilds the pigtail.Project that scratchRepoAndWorkspace used,
// so a test can hand it to Cleanup via CleanupOptions.Project.
func projectFor(mainDir string) *pigtail.Project {
	return &pigtail.Project{
		ID:             "prj_t",
		RootPath:       filepath.Dir(mainDir),
		MainWorktree:   filepath.Base(mainDir),
		DefaultBackend: "git",
	}
}

// worktreeListed reports whether git still has an administrative record for
// path in the repo at mainDir.
func worktreeListed(t *testing.T, mainDir, path string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", mainDir, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v: %s", err, out)
	}
	// Compare resolved paths: TempDir on macOS is /var → /private/var.
	want, _ := filepath.EvalSymlinks(path)
	for _, line := range strings.Split(string(out), "\n") {
		p, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		got, _ := filepath.EvalSymlinks(strings.TrimSpace(p))
		if got != "" && got == want {
			return true
		}
		if strings.TrimSpace(p) == path {
			return true
		}
	}
	return false
}

// TestCleanupFinishesInterruptedTeardownWithProject covers F1: when the
// worktree's .git file is gone (an interrupted `git worktree remove` deletes
// it), the workspace can no longer name its own repo. Given the Project, the
// backend must still finish the job instead of silently reporting success.
func TestCleanupFinishesInterruptedTeardownWithProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	mainDir, ws := scratchRepoAndWorkspace(t, "broken teardown", []string{"feat/broken-teardown"})

	// Simulate the state left by an interrupted teardown: the .git file at
	// the worktree root is one of the first things git removes.
	if err := os.Remove(filepath.Join(ws.Path, ".git")); err != nil {
		t.Fatalf("remove worktree .git: %v", err)
	}

	b := New(nil)
	_, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force:   true,
		Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Errorf("worktree directory should be gone, stat err = %v", err)
	}
	if worktreeListed(t, mainDir, ws.Path) {
		t.Errorf("git still has an administrative record for %s", ws.Path)
	}
}

// TestCleanupProceedsOnUnusableWorktree covers level 1 of the dirty check: a
// directory that is no longer a worktree holds no work to protect, so the
// guard must not stand in the way even without --force.
func TestCleanupProceedsOnUnusableWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	mainDir, ws := scratchRepoAndWorkspace(t, "unusable", []string{"feat/unusable"})

	if err := os.Remove(filepath.Join(ws.Path, ".git")); err != nil {
		t.Fatalf("remove worktree .git: %v", err)
	}

	b := New(nil)
	if _, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Project: projectFor(mainDir),
	}); err != nil {
		t.Fatalf("cleanup without --force on an unusable worktree: %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Errorf("worktree directory should be gone, stat err = %v", err)
	}
}

// TestCleanupReportsInterruptedTeardown covers level 3: a worktree whose only
// change is missing tracked files still refuses without --force (we do not
// presume intent behind a deliberate `rm`), but the message must say what is
// actually going on instead of claiming "uncommitted changes".
func TestCleanupReportsInterruptedTeardown(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, ws := scratchRepoAndWorkspace(t, "half torn", []string{"feat/half-torn"})
	seedTrackedFiles(t, ws.Path, "a.txt", "b.txt", "c.txt")

	// The shape an interrupted `git worktree remove` leaves: tracked files
	// deleted in the working tree, nothing staged, nothing modified.
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.Remove(filepath.Join(ws.Path, name)); err != nil {
			t.Fatal(err)
		}
	}

	b := New(nil)
	_, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{})
	var apiErr *pigtail.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if apiErr.Details["likely_interrupted_teardown"] != true {
		t.Errorf("expected likely_interrupted_teardown in details, got %v", apiErr.Details)
	}
	if got := apiErr.Details["missing_count"]; got != 2 {
		t.Errorf("missing_count = %v, want 2", got)
	}
	if !strings.Contains(apiErr.Message, "interrupted teardown") {
		t.Errorf("message should name the real cause, got %q", apiErr.Message)
	}
}

// TestCleanupStillRefusesRealWorkAlongsideDeletions is the proof the guard is
// not weakened: as soon as one real modification is present, the historical
// refusal (and its message) must come back, deletions or not.
func TestCleanupStillRefusesRealWorkAlongsideDeletions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, ws := scratchRepoAndWorkspace(t, "mixed", []string{"feat/mixed"})
	seedTrackedFiles(t, ws.Path, "a.txt", "b.txt")

	if err := os.Remove(filepath.Join(ws.Path, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "b.txt"), []byte("precious work"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := New(nil)
	_, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{})
	var apiErr *pigtail.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if apiErr.Message != "worktree has uncommitted changes; use --force to clean anyway" {
		t.Errorf("expected the historical refusal message, got %q", apiErr.Message)
	}
	if apiErr.Details["likely_interrupted_teardown"] != nil {
		t.Errorf("a modified file is not an interrupted teardown: %v", apiErr.Details)
	}
	if _, serr := os.Stat(filepath.Join(ws.Path, "b.txt")); serr != nil {
		t.Errorf("the modified file must survive the refusal: %v", serr)
	}
}

// seedTrackedFiles writes and commits files inside the worktree so that
// deleting them afterwards produces the " D" porcelain shape.
func seedTrackedFiles(t *testing.T, wsPath string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(wsPath, n), []byte("content of "+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		append([]string{"-C", wsPath, "add"}, names...),
		{"-C", wsPath, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "seed"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// TestCleanupFailsLoudlyWithoutProject is the other half of F1: with no way
// to resolve the repo, Cleanup must report an error rather than return nil
// and let the caller delete the store row (which orphans the directory).
func TestCleanupFailsLoudlyWithoutProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, ws := scratchRepoAndWorkspace(t, "orphan teardown", []string{"feat/orphan-teardown"})

	if err := os.Remove(filepath.Join(ws.Path, ".git")); err != nil {
		t.Fatalf("remove worktree .git: %v", err)
	}

	b := New(nil)
	_, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{Force: true})
	if err == nil {
		t.Fatal("expected an error when the repo root cannot be resolved, got nil")
	}
	if _, serr := os.Stat(ws.Path); serr != nil {
		t.Errorf("nothing was removable, the directory should still be there: %v", serr)
	}
}

// TestCleanupRefusesToDeleteAnUnregisteredPath guards the one destructive path
// git does not sanction. Removing a prunable worktree by hand is only safe
// because git still vouches for it; a ws.Path that this repo has no record of
// — stale after a manual move, or adopted wrong — must be left alone.
func TestCleanupRefusesToDeleteAnUnregisteredPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	mainDir, ws := scratchRepoAndWorkspace(t, "unregistered", []string{"feat/unregistered"})

	// A directory that looks like an interrupted teardown (no .git file) but
	// that the repo never knew about.
	stray := filepath.Join(t.TempDir(), "not-a-worktree")
	if err := os.MkdirAll(stray, 0o750); err != nil {
		t.Fatalf("mkdir stray: %v", err)
	}
	keep := filepath.Join(stray, "precious.txt")
	if err := os.WriteFile(keep, []byte("do not delete me"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}
	ws.Path = stray

	b := New(nil)
	_, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force:   true,
		Project: projectFor(mainDir),
	})
	if err == nil {
		t.Fatal("cleanup should refuse a path the repo has no worktree record for")
	}
	if !strings.Contains(err.Error(), "no worktree record") {
		t.Errorf("error should name the reason, got: %v", err)
	}
	if _, serr := os.Stat(keep); serr != nil {
		t.Errorf("the stray directory must be untouched: %v", serr)
	}
}
