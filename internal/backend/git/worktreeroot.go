package git

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// WorktreeRoot returns the absolute root of the git worktree containing dir —
// the linked worktree itself when dir is inside one, not the repository that
// owns it.
//
// It is the one piece of git the CLI needs locally (`pigtail rename` infers its
// workspace from the current directory), and it lives here because no
// `exec.Command("git", …)` may exist outside this package (architecture.md §8).
// Going through gitCmd also strips the repository-scoped variables a git hook
// exports, so a `pigtail rename` run from a hook resolves dir, not the hook's
// repository.
//
// On failure the error is git's own diagnostic, trimmed and with nothing
// prepended, so callers can wrap it as they see fit.
func WorktreeRoot(dir string) (string, error) {
	args := []string{"rev-parse", "--show-toplevel"}
	if dir == "" {
		return "", errNoGitDir(args)
	}
	out, err := gitCmd(context.Background(), filepath.Clean(dir), args...).CombinedOutput()
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
