package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// CanonicalRoot returns the directory Claude Code keys a repository's project
// settings (folder trust included) on: the main working tree, whichever of the
// repository's worktrees dir is in — the parent of the common git dir. A bare
// repository or a submodule (common dir not named .git) has none: error.
func CanonicalRoot(dir string) (string, error) {
	out, err := gitCmd(context.Background(), filepath.Clean(dir), "rev-parse", "--path-format=absolute", "--git-common-dir").CombinedOutput()
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(out)))
	}
	common := filepath.Clean(strings.TrimSpace(string(out)))
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("git common dir %s is not a .git directory (bare repository or submodule): no main checkout to key on", common)
	}
	root := filepath.Dir(common)
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return root, nil
}
