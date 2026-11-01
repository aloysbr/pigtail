package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitT(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestCanonicalRoot(t *testing.T) {
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitT(t, "init", "-q", "-b", "main", repo)
	gitT(t, "-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x")
	if got, err := CanonicalRoot(repo); err != nil || got != repo {
		t.Fatalf("plain repo = %q %v, want %q", got, err, repo)
	}
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := CanonicalRoot(sub); err != nil || got != repo {
		t.Fatalf("subdir = %q %v, want %q", got, err, repo)
	}
	wtParent, _ := filepath.EvalSymlinks(t.TempDir())
	wt := filepath.Join(wtParent, "wt")
	gitT(t, "-C", repo, "worktree", "add", "-q", "-b", "feat", wt)
	if got, err := CanonicalRoot(wt); err != nil || got != repo {
		t.Fatalf("linked worktree = %q %v, want the main checkout %q", got, err, repo)
	}
	if _, err := CanonicalRoot(t.TempDir()); err == nil {
		t.Fatal("non-git dir: want an error")
	}
	bare := filepath.Join(t.TempDir(), "b.git")
	gitT(t, "init", "-q", "--bare", bare)
	if _, err := CanonicalRoot(bare); err == nil {
		t.Fatal("bare repo: want an error (no working tree to key on)")
	}
	// A submodule's common dir is <super>/.git/modules/<name>: no main checkout to key on.
	inner, _ := filepath.EvalSymlinks(t.TempDir())
	gitT(t, "init", "-q", "-b", "main", inner)
	gitT(t, "-C", inner, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x")
	gitT(t, "-C", repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", inner, "sub")
	if _, err := CanonicalRoot(filepath.Join(repo, "sub")); err == nil {
		t.Fatal("submodule: want an error (common dir is .git/modules/sub)")
	}
}
