package git

import (
	"context"
	"testing"
)

func TestReadPaneRepo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, dir, "checkout", "-q", "-b", "feat/x")
	git(t, dir, "update-ref", "refs/remotes/origin/develop", "HEAD")
	git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	git(t, dir, "config", "remote.origin.url", "git@github.com:aloysbr/pigtail.git")

	got := ReadPaneRepo(ctx, dir)
	if got != (PaneRepo{Branch: "feat/x", DefaultBranch: "develop", Repo: "pigtail"}) {
		t.Fatalf("ReadPaneRepo = %+v", got)
	}

	git(t, dir, "config", "remote.origin.url", "https://github.com/acme/tools")
	if got := ReadPaneRepo(ctx, dir); got.Repo != "tools" {
		t.Fatalf("https url without .git: %+v", got)
	}

	git(t, dir, "checkout", "-q", "--detach")
	if got := ReadPaneRepo(ctx, dir); got.Branch != "" || got.DefaultBranch != "develop" {
		t.Fatalf("detached HEAD: %+v", got)
	}

	if got := ReadPaneRepo(ctx, t.TempDir()); got != (PaneRepo{}) {
		t.Fatalf("outside a repo: %+v", got)
	}
	if got := ReadPaneRepo(ctx, ""); got != (PaneRepo{}) {
		t.Fatalf("empty dir: %+v", got)
	}
}
