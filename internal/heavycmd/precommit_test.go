package heavycmd

import (
	"os"
	"path/filepath"
	"testing"
)

// repoWith builds a throwaway repo: hook says whether a pre-commit hook is
// installed, staged whether lint-staged is configured, and where.
func repoWith(t *testing.T, hook, lintStagedInPkg, lintStagedFile bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hook {
		if err := os.WriteFile(filepath.Join(root, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nnpx lint-staged\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pkg := `{"name":"x","scripts":{"lint":"eslint ."}}`
	if lintStagedInPkg {
		pkg = `{"name":"x","lint-staged":{"*.ts":"eslint --fix"}}`
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if lintStagedFile {
		if err := os.WriteFile(filepath.Join(root, ".lintstagedrc.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGitCommitClassification(t *testing.T) {
	heavy := repoWith(t, true, true, false)
	byFile := repoWith(t, true, false, true)
	noHook := repoWith(t, false, true, false)
	noLintStaged := repoWith(t, true, false, false)

	cases := []struct {
		name string
		argv []string
		dir  string
		want Class
	}{
		{"lint-staged in package.json behind a hook", []string{"git", "commit", "-m", "x"}, heavy, ClassPrecommit},
		{"lint-staged in a config file", []string{"git", "commit", "-m", "x"}, byFile, ClassPrecommit},
		{"a hook that is not installed runs nothing", []string{"git", "commit", "-m", "x"}, noHook, ""},
		{"a hook without lint-staged is not ours to classify", []string{"git", "commit", "-m", "x"}, noLintStaged, ""},
		{"--no-verify skips the hook", []string{"git", "commit", "--no-verify", "-m", "x"}, heavy, ""},
		{"-n skips it too", []string{"git", "commit", "-n", "-m", "x"}, heavy, ""},
		{"--dry-run commits nothing", []string{"git", "commit", "--dry-run"}, heavy, ""},
		{"global options before the subcommand", []string{"git", "-c", "user.name=x", "commit", "-m", "y"}, heavy, ClassPrecommit},
		{"-C moves where the hook is looked for", []string{"git", "-C", heavy, "commit", "-m", "y"}, t.TempDir(), ClassPrecommit},
		{"other git subcommands stay cheap", []string{"git", "status"}, heavy, ""},
		{"git log is not a commit", []string{"git", "log", "--oneline"}, heavy, ""},
		{"outside any repo", []string{"git", "commit", "-m", "x"}, t.TempDir(), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := Classify(c.argv, c.dir)
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

// A commit inside a worktree: .git is a file there, not a directory, and the
// hooks live in the parent clone's .git — which is why repoRoot stops at the
// first .git *entry* of either kind.
func TestGitCommitInWorktreeLayout(t *testing.T) {
	root := repoWith(t, true, true, false)
	sub := filepath.Join(root, "apps", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := Classify([]string{"git", "commit", "-m", "x"}, sub); got != ClassPrecommit {
		t.Fatalf("got %q from a subdirectory, want %q", got, ClassPrecommit)
	}
}
