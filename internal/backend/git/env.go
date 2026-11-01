package git

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// localEnvVars are the repository-scoped environment variables git itself
// enumerates through `git rev-parse --local-env-vars`. Every one of them
// overrides where a git command looks for the repository, and each takes
// precedence over the process working directory.
//
// Why pigtail must strip them: git EXPORTS these to every hook it runs. A
// `pigtail` invoked from inside a pre-push, post-checkout or post-merge hook —
// directly, or indirectly through a test suite or a Makefile the hook calls —
// therefore inherits GIT_DIR pointing at the hook's own repository. Every
// subsequent `git` call then operates on THAT repository no matter which
// directory pigtail hands it, because the environment wins over cmd.Dir.
//
// Measured on 2026-09-04, and the reason this file exists: pushing this very
// branch ran the repo's pre-push hook, which runs `make test-race`; the test
// suite's own `git remote set-url origin <temp path>` — aimed at a throwaway
// clone under t.TempDir() — landed on the REAL pigtail repository instead and
// left origin pointing at a deleted temp directory. Same suite run from a
// normal shell: no effect. The only difference was the inherited GIT_DIR.
//
// The list is hardcoded rather than shelled out for on every call; it is part
// of git's stable interface, and re-deriving it would need the very subprocess
// this protects.
var localEnvVars = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CONFIG",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_GRAFT_FILE",
	"GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_REPLACE_REF_BASE",
	"GIT_PREFIX",
	"GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
}

// scrubbedEnv returns the process environment minus every repository-scoped git
// variable, so a git subprocess resolves its repository from its working
// directory alone — which is the only thing the caller actually chose.
//
// Non-repository git variables (GIT_AUTHOR_NAME, GIT_SSH_COMMAND, GIT_ASKPASS,
// credential helpers…) are deliberately preserved: they carry identity and
// transport configuration the user set on purpose, and dropping them would
// break authentication rather than protect anything.
func scrubbedEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && isLocalEnvVar(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ScrubLocalEnv removes git's repository-scoped variables from the CURRENT
// process environment.
//
// gitCmd already shields every git subprocess this package starts; this is for
// the callers that start their own — chiefly test binaries, which build
// throwaway repositories and drive them with raw exec.Command("git", …). One
// call from a package's TestMain covers all of its call sites at once, and the
// ones added later, which per-site edits would not.
//
// Not for production code: a running pigtail must not mutate the environment it
// was handed. Production paths get their protection from gitCmd instead.
func ScrubLocalEnv() {
	for _, v := range localEnvVars {
		// os.Unsetenv only fails on a platform that cannot represent the name;
		// these are fixed ASCII identifiers, so there is nothing to recover
		// from and nowhere useful to report it.
		_ = os.Unsetenv(v)
	}
}

func isLocalEnvVar(name string) bool {
	for _, v := range localEnvVars {
		if name == v {
			return true
		}
	}
	return false
}

// gitCmd builds a git command rooted at dir, with the inherited repository
// scope stripped. Every git subprocess in this package goes through it.
func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	return cmd
}
