package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain strips git's repository-scoped environment for the whole package.
//
// This is a safety belt, not a convenience. These tests build throwaway repos
// under t.TempDir() and drive them with `git` subprocesses; a GIT_DIR inherited
// from the environment overrides each subprocess's working directory, so every
// one of those commands would silently target the inherited repository instead.
//
// That is not hypothetical. Git exports GIT_DIR to every hook it runs, so this
// suite executed from a pre-push hook (the repo's own hook runs `make
// test-race`) had `git remote set-url origin <t.TempDir()>/gone.git` land on the
// REAL pigtail repository — origin left pointing at a temp path that no longer
// existed, and the push refused. Measured 2026-09-04; the same suite run from a
// normal shell was harmless. One process-wide scrub covers every helper here,
// including the ~34 raw exec.Command("git", …) call sites, and every test added
// later, which per-call-site edits would not.
func TestMain(m *testing.M) {
	ScrubLocalEnv()
	os.Exit(m.Run())
}

// TestScrubbedEnvDropsRepositoryScope proves the scrub removes exactly the
// repository-scoped variables and keeps everything else — dropping identity or
// transport settings (GIT_SSH_COMMAND, GIT_AUTHOR_NAME…) would break
// authentication rather than protect anything.
func TestScrubbedEnvDropsRepositoryScope(t *testing.T) {
	t.Setenv("GIT_DIR", "/somewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/somewhere")
	t.Setenv("GIT_INDEX_FILE", "/somewhere/.git/index")
	t.Setenv("GIT_AUTHOR_NAME", "keep me")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /keep/me")

	var sawAuthor, sawSSH bool
	for _, kv := range scrubbedEnv() {
		name, _, _ := strings.Cut(kv, "=")
		if isLocalEnvVar(name) {
			t.Errorf("scrubbedEnv kept repository-scoped %s", name)
		}
		switch name {
		case "GIT_AUTHOR_NAME":
			sawAuthor = true
		case "GIT_SSH_COMMAND":
			sawSSH = true
		}
	}
	if !sawAuthor || !sawSSH {
		t.Error("scrubbedEnv dropped identity/transport variables it must preserve")
	}
}

// TestGitCmdIgnoresInheritedGitDir is the regression test for the corruption:
// with GIT_DIR pointing at repo A, a command aimed at repo B must act on B.
// Before the fix it acted on A, whatever directory it was given.
func TestGitCmdIgnoresInheritedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoA := mustInitRepo(t, t.TempDir())
	repoB := mustInitRepo(t, t.TempDir())

	// Give each repo a distinguishable remote, then point GIT_DIR at A.
	gitRun(t, repoA, "remote", "add", "origin", "https://example.invalid/a.git")
	gitRun(t, repoB, "remote", "add", "origin", "https://example.invalid/b.git")
	t.Setenv("GIT_DIR", filepath.Join(repoA, ".git"))

	got, err := captureGit(context.Background(), repoB, "config", "--get", "remote.origin.url")
	if err != nil {
		t.Fatalf("captureGit: %v", err)
	}
	if want := "https://example.invalid/b.git"; strings.TrimSpace(got) != want {
		t.Errorf("captureGit read %q, want %q — the inherited GIT_DIR won over the target directory", strings.TrimSpace(got), want)
	}

	// And the write path: a mutation aimed at B must not reach A.
	if err := runGit(context.Background(), repoB, "remote", "set-url", "origin", "https://example.invalid/b2.git"); err != nil {
		t.Fatalf("runGit: %v", err)
	}
	stillA, err := captureGit(context.Background(), repoA, "config", "--get", "remote.origin.url")
	if err != nil {
		t.Fatalf("captureGit on repo A: %v", err)
	}
	if want := "https://example.invalid/a.git"; strings.TrimSpace(stillA) != want {
		t.Errorf("repo A was modified through the inherited GIT_DIR: %q, want %q", strings.TrimSpace(stillA), want)
	}
}
