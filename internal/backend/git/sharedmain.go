package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SharedMainCheckout reports whether dir is the main working tree of a
// repository that has linked worktrees — the checkout several sessions share,
// where `pigtail session spawn` must never start a session. A directory outside
// any repository, or a linked worktree, is not one.
func SharedMainCheckout(dir string) (bool, error) {
	dir = filepath.Clean(dir)
	out, err := gitCmd(context.Background(), dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir").Output()
	if err != nil {
		return false, nil // not a git repository
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || f[0] != f[1] {
		return false, nil // a linked worktree: its git dir is not the common dir
	}
	wl, err := gitCmd(context.Background(), dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git worktree list in %s: %w", dir, err)
	}
	return strings.Count("\n"+string(wl), "\nworktree ") > 1, nil
}

// MainCheckoutRoot reports whether dir is inside a repository's main working
// tree, and its top level. A linked worktree answers main=false, linked=true;
// a directory outside any repository answers false, false, nil.
func MainCheckoutRoot(dir string) (root string, main, linked bool, err error) {
	dir = filepath.Clean(dir)
	out, gerr := gitCmd(context.Background(), dir, "rev-parse", "--path-format=absolute",
		"--git-dir", "--git-common-dir", "--show-toplevel").Output()
	if gerr != nil {
		return "", false, false, nil // not a git repository
	}
	f := strings.Fields(string(out))
	if len(f) != 3 {
		return "", false, false, fmt.Errorf("git rev-parse in %s: unexpected output %q", dir, out)
	}
	if f[0] != f[1] {
		return "", false, true, nil
	}
	return f[2], true, false, nil
}

// LinkedWorktrees lists the linked worktrees of the repository whose main
// checkout is root (D3, R19): absolute, symlink-resolved, sorted. The main
// checkout itself and entries whose directory is gone (prunable) are left
// out. It reuses worktreeBranchesByPath (refs.go), the package's one parser of
// `git worktree list --porcelain`, rather than a second scan.
func LinkedWorktrees(root string) ([]string, error) {
	byPath, ok := worktreeBranchesByPath(context.Background(), root, nil)
	if !ok {
		return nil, fmt.Errorf("git worktree list in %s failed", root)
	}
	main := resolvedPath(root)
	var out []string
	for p := range byPath {
		if p == main {
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}
