package heavycmd

import (
	"os"
	"path/filepath"
	"strings"
)

// lintStagedConfigs are the config file names lint-staged reads. Presence is
// what we test — never execution: this code runs in PreToolUse(Bash), on the
// hot path of every shell command the agent issues.
var lintStagedConfigs = []string{
	".lintstagedrc", ".lintstagedrc.json", ".lintstagedrc.yaml", ".lintstagedrc.yml",
	".lintstagedrc.js", ".lintstagedrc.cjs", ".lintstagedrc.mjs",
	"lint-staged.config.js", "lint-staged.config.cjs", "lint-staged.config.mjs",
	"lint-staged.config.ts", "lint-staged.config.mts",
}

// preCommitHooks are where a pre-commit hook lives: git's own directory, and
// husky's, which is what the JS ecosystem actually installs.
var preCommitHooks = []string{
	filepath.Join(".git", "hooks", "pre-commit"),
	filepath.Join(".husky", "pre-commit"),
	filepath.Join(".husky", "_", "pre-commit"),
}

// gitCommitIsHeavy reports whether `git commit` in dir will run a heavy
// pre-commit hook.
//
// Why this is in scope at all: a commit looks like a cheap command and is
// one, until a repo hangs eslint or tsc off its pre-commit hook — then it
// costs what that lint costs, at the exact moment several agents are wrapping
// up at once. It was left out of the first cut ("wait for the observe-mode
// numbers"), and the measurement arrived in another form: on 2026-09-24 a
// session, finding nothing to wait on, wrote its own wait — on *free* memory,
// which sits at 0.1 GiB on macOS for ever, so the wait could not end.
//
// Both signals are required. A pre-commit hook that runs `go test` or a
// formatter is not what this classifies, and a lint-staged config in a repo
// whose hooks were never installed runs nothing at all.
func gitCommitIsHeavy(args []string, dir string) bool {
	for _, a := range args {
		switch a {
		case "--no-verify", "-n", "--dry-run":
			// Hooks skipped, or nothing committed: nothing heavy will run.
			// `-n` is git's own short form of --no-verify for commit.
			return false
		}
	}
	root, ok := repoRoot(dir)
	if !ok {
		return false
	}
	hooked := false
	for _, h := range preCommitHooks {
		if st, err := os.Stat(filepath.Join(root, h)); err == nil && !st.IsDir() {
			hooked = true
			break
		}
	}
	return hooked && usesLintStaged(root)
}

// usesLintStaged reports whether root configures lint-staged, by a config
// file or a mention in package.json (which is where the "lint-staged" key
// lives when it is not in a file of its own).
func usesLintStaged(root string) bool {
	for _, name := range lintStagedConfigs {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return true
		}
	}
	pkg := filepath.Join(root, "package.json")
	st, err := os.Stat(pkg)
	if err != nil || st.Size() > 512_000 {
		return false
	}
	data, err := os.ReadFile(pkg)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "lint-staged")
}

// repoRoot walks up from dir to the first directory holding a .git entry
// (a directory in a plain clone, a file in a worktree or submodule).
func repoRoot(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// gitCommitArgs returns the arguments after the `commit` subcommand, and
// whether this git invocation is a commit at all. It steps over git's own
// global options, including the ones that take a value (`git -C dir commit`),
// and follows -C so the hook is looked for where the commit will run.
func gitCommitArgs(args []string, dir string) (rest []string, cwd string, ok bool) {
	cwd = dir
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "-C", "--git-dir", "--work-tree", "-c", "--namespace", "--exec-path":
			if i+1 >= len(args) {
				return nil, cwd, false
			}
			if args[i] == "-C" {
				cwd = joinDir(cwd, args[i+1])
			}
			i += 2
		default:
			i++
		}
	}
	if i >= len(args) || args[i] != "commit" {
		return nil, cwd, false
	}
	return args[i+1:], cwd, true
}
