package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realPath resolves symlinks so a t.TempDir() under /var compares equal to the
// /private/var path git reports on macOS.
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return r
}

func TestWorktreeRootFromMainSubdir(t *testing.T) {
	requireGit(t)
	mainDir := mustInitRepo(t, t.TempDir())
	sub := filepath.Join(mainDir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := WorktreeRoot(sub)
	if err != nil {
		t.Fatalf("WorktreeRoot(%s): %v", sub, err)
	}
	if got != realPath(t, mainDir) {
		t.Fatalf("WorktreeRoot = %q, want %q", got, realPath(t, mainDir))
	}
}

// A linked worktree is its own root: the answer must be the worktree, not the
// repository that owns it — that is what rename matches against.
func TestWorktreeRootFromLinkedWorktree(t *testing.T) {
	requireGit(t)
	container := t.TempDir()
	mainDir := mustInitRepo(t, container)
	wt := filepath.Join(container, "linked")
	gitRun(t, mainDir, "worktree", "add", "-b", "linked", wt)
	sub := filepath.Join(wt, "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := WorktreeRoot(sub)
	if err != nil {
		t.Fatalf("WorktreeRoot(%s): %v", sub, err)
	}
	if got != realPath(t, wt) {
		t.Fatalf("WorktreeRoot = %q, want the linked worktree %q", got, realPath(t, wt))
	}
}

// Outside any repository the error is git's own diagnostic, trimmed and with
// nothing prepended: the CLI wraps it as "not inside a git worktree: …".
func TestWorktreeRootOutsideRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(realPath(t, dir)))
	// git translates its diagnostics (fr_FR: « ni ceci ni aucun … ») : pin the
	// English message this test matches.
	t.Setenv("LC_ALL", "C")

	got, err := WorktreeRoot(dir)
	if err == nil {
		t.Fatalf("WorktreeRoot outside a repo returned %q, want an error", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "not a git repository") {
		t.Fatalf("error = %q, want git's \"not a git repository\" diagnostic", msg)
	}
	if strings.HasPrefix(msg, "git ") || msg != strings.TrimSpace(msg) {
		t.Fatalf("error = %q, want git's output alone, trimmed", msg)
	}
}

func TestWorktreeRootRefusesEmptyDir(t *testing.T) {
	if _, err := WorktreeRoot(""); err == nil || !strings.Contains(err.Error(), "no working directory") {
		t.Fatalf("WorktreeRoot(\"\") error = %v, want the empty-dir refusal", err)
	}
}
