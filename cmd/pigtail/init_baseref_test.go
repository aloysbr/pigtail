package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", branch)
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestDetectBaseRefRemoteHead(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "remote.git")
	src := newRepo(t, "master")
	runGit(t, filepath.Dir(bare), "clone", "-q", "--bare", src, bare)
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(clone), "clone", "-q", bare, clone)
	got, err := detectBaseRef(clone)
	if err != nil || got != "origin/master" {
		t.Fatalf("got %q, %v; want origin/master", got, err)
	}
}

func TestDetectBaseRefNoRemote(t *testing.T) {
	repo := newRepo(t, "trunk")
	got, err := detectBaseRef(repo)
	if err != nil || got != "trunk" {
		t.Fatalf("got %q, %v; want trunk", got, err)
	}
}

func TestDetectBaseRefDetached(t *testing.T) {
	repo := newRepo(t, "main")
	runGit(t, repo, "checkout", "-q", "--detach")
	_, err := detectBaseRef(repo)
	if err == nil || !strings.Contains(err.Error(), "--base") {
		t.Fatalf("want an error naming --base, got %v", err)
	}
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

func TestInitNonTTYWithoutYesRefuses(t *testing.T) {
	repo := newRepo(t, "main")
	before := inode(t, repo)
	old := initIsTTY
	initIsTTY = func() bool { return false }
	defer func() { initIsTTY = old }()
	err := initFromGitRepo(repo, "main", false, "")
	if err == nil || !strings.Contains(err.Error(), "rerun with --yes") ||
		!strings.Contains(err.Error(), "would move "+repo) {
		t.Fatalf("want a --yes refusal, got %v", err)
	}
	if inode(t, repo) != before {
		t.Fatal("repo inode changed")
	}
	if _, err := os.Stat(filepath.Join(repo, "main")); err == nil {
		t.Fatal("main/ was created")
	}
}

func TestInitEmptyAnswerAborts(t *testing.T) {
	repo := newRepo(t, "main")
	before := inode(t, repo)
	oldT, oldIn := initIsTTY, initStdin
	initIsTTY = func() bool { return true }
	initStdin = strings.NewReader("\n")
	defer func() { initIsTTY, initStdin = oldT, oldIn }()
	if err := initFromGitRepo(repo, "main", false, ""); err != nil {
		t.Fatalf("abort should not error: %v", err)
	}
	if inode(t, repo) != before {
		t.Fatal("repo moved on an empty answer")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(".git gone")
	}
}

func TestSetBaseRef(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pigtail.yaml")
	if err := os.WriteFile(p, []byte("defaults:\n  agent: claude\n  base_ref: origin/main\n  branch_prefix: feat/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setBaseRef(dir, "origin/master"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "  base_ref: origin/master\n") || strings.Contains(string(b), "origin/main") {
		t.Fatalf("not rewritten: %s", b)
	}
}
