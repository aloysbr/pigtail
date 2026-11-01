package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadInfo(t *testing.T) {
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitT(t, "init", "-q", "-b", "feat/x", repo)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, "-C", repo, "add", "a.txt")
	gitT(t, "-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "x")
	want, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	branch, sha, dirty, err := HeadInfo(context.Background(), repo)
	if err != nil || branch != "feat/x" || sha != strings.TrimSpace(string(want)) || len(sha) != 40 || dirty {
		t.Fatalf("clean = %q %q %v %v, want feat/x %s false", branch, sha, dirty, err, want)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, dirty, err := HeadInfo(context.Background(), repo); err != nil || !dirty {
		t.Fatalf("after editing a tracked file: dirty=%v err=%v, want true", dirty, err)
	}
	if _, _, _, err := HeadInfo(context.Background(), t.TempDir()); err == nil {
		t.Fatal("non-git dir: want an error")
	}
}
