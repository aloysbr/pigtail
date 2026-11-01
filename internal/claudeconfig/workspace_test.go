package claudeconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPreTrustWorkspaceWorktreeResolvesToMainCheckout(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", base)
	dev := filepath.Join(base, "Developer")
	repo := filepath.Join(dev, "home", "repo")
	wt := filepath.Join(dev, "home", "repo-wt", "feat")
	cfg := filepath.Join(base, "cell")
	_ = os.MkdirAll(cfg, 0o755)
	for _, a := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
		{"-C", repo, "worktree", "add", "-q", "-b", "feat", wt},
	} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	_ = os.WriteFile(filepath.Join(cfg, FileName), []byte(`{"projects":{"`+dev+`":{"hasTrustDialogAccepted":true}}}`), 0o600)
	r := PreTrustWorkspace(cfg, wt)
	if r.Outcome != Written || r.Key != repo {
		t.Fatalf("%+v, want written on the main checkout %s", r, repo)
	}
	if r := PreTrustWorkspace(cfg, filepath.Join(base, "not-git")); r.Outcome != NotEligible {
		t.Fatalf("non-git worktree: %+v", r)
	}
}

// pigtail's container layout (pigtail init): main checkout at <container>/main,
// worktrees at <container>/<suffix>. The key is <container>/main; <container>
// itself is an ancestor of the worktree and counts.
func TestPreTrustWorkspaceContainerLayout(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", base)
	container := filepath.Join(base, "Developer", "proj")
	main := filepath.Join(container, "main")
	wt := filepath.Join(container, "feat-x")
	cfg := filepath.Join(base, "cell")
	_ = os.MkdirAll(cfg, 0o755)
	for _, a := range [][]string{
		{"init", "-q", "-b", "main", main},
		{"-C", main, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
		{"-C", main, "worktree", "add", "-q", "-b", "feat/x", wt},
	} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	_ = os.WriteFile(filepath.Join(cfg, FileName), []byte(`{"projects":{"`+container+`":{"hasTrustDialogAccepted":true}}}`), 0o600)
	if r := PreTrustWorkspace(cfg, wt); r.Outcome != Written || r.Key != main || r.Ancestor != container {
		t.Fatalf("%+v, want written on %s inherited from %s", r, main, container)
	}
}
