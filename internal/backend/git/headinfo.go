package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// HeadInfo reads the branch, the full HEAD sha and whether tracked files
// differ from HEAD (the scope of `git describe --dirty`) in dir — what
// `pigtail runtime claim` records about the checkout that holds the
// installed runtime. A detached HEAD reads branch "HEAD". Outside a
// repository it fails.
func HeadInfo(ctx context.Context, dir string) (branch, sha string, dirty bool, err error) {
	if dir == "" {
		return "", "", false, errNoGitDir([]string{"rev-parse", "HEAD"})
	}
	out, err := gitCmd(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", "", false, fmt.Errorf("git rev-parse --abbrev-ref HEAD in %s: %w", dir, err)
	}
	branch = strings.TrimSpace(string(out))
	out, err = gitCmd(ctx, dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", "", false, fmt.Errorf("git rev-parse HEAD in %s: %w", dir, err)
	}
	sha = strings.TrimSpace(string(out))
	err = gitCmd(ctx, dir, "diff", "--quiet", "HEAD").Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		dirty = true
	default:
		return "", "", false, fmt.Errorf("git diff --quiet HEAD in %s: %w", dir, err)
	}
	return branch, sha, dirty, nil
}
