package git

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ResolveRefs implements backend.RefResolver: which branch each worktree holds
// right now, as opposed to the one stamped into BackendData at creation.
//
// Grouped by repository, one `git worktree list --porcelain` per repo. That
// batching is the reason a periodic reconciliation is affordable: thirteen
// worktrees of one project cost one git invocation, not thirteen. Resolving
// them one at a time would re-read the same porcelain output thirteen times.
//
// A workspace is omitted rather than mapped to "" whenever its ref cannot be
// established — detached HEAD, missing directory, git failure. The caller
// writes what it finds and leaves alone what it does not; "" would be
// indistinguishable from a resolved empty name and would erase a real record.
func (b *Backend) ResolveRefs(ctx context.Context, wss []pigtail.Workspace) (map[string]string, error) {
	out := make(map[string]string, len(wss))
	if len(wss) == 0 {
		return out, nil
	}

	// repoRoot per workspace, resolved once and reused: parentRepoRoot shells
	// out too, so caching it is the same saving as batching the porcelain.
	roots := make(map[string]string, len(wss))
	byRoot := map[string][]pigtail.Workspace{}
	var rootless []pigtail.Workspace
	for _, ws := range wss {
		if ws.Path == "" {
			continue
		}
		root, seen := roots[ws.Path]
		if !seen {
			root = parentRepoRoot(ctx, ws.Path)
			roots[ws.Path] = root
		}
		if root == "" {
			rootless = append(rootless, ws)
			continue
		}
		byRoot[root] = append(byRoot[root], ws)
	}

	for root, group := range byRoot {
		byPath, ok := worktreeBranchesByPath(ctx, root, b.countWorktreeList)
		if !ok {
			// The porcelain is unavailable for this repo; fall back per
			// workspace rather than dropping the whole group.
			rootless = append(rootless, group...)
			continue
		}
		for _, ws := range group {
			if res, found := byPath[resolvedPath(ws.Path)]; found {
				if res.known && !res.detached && res.branch != "" {
					out[ws.ID] = res.branch
				}
				continue
			}
			// Registered nowhere in this repo: ask the worktree itself.
			rootless = append(rootless, ws)
		}
	}

	for _, ws := range rootless {
		res := resolveWorktreeBranch(ctx, "", ws.Path)
		if res.known && !res.detached && res.branch != "" {
			out[ws.ID] = res.branch
		}
	}
	return out, nil
}

// worktreeBranchesByPath reads one repo's `worktree list --porcelain` and
// returns every entry keyed by resolved path. It is branchFromWorktreeList's
// scan with the "find one path" filter removed, so the same output answers for
// every workspace of the repo.
func worktreeBranchesByPath(ctx context.Context, repoRoot string, count func()) (map[string]worktreeBranch, bool) {
	if count != nil {
		count()
	}
	out, err := captureGit(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, false
	}
	byPath := map[string]worktreeBranch{}
	var current string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			current = resolvedPath(p)
			continue
		}
		if current == "" {
			continue
		}
		if name, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			byPath[current] = worktreeBranch{branch: name, known: true}
			continue
		}
		if line == "detached" {
			byPath[current] = worktreeBranch{detached: true, known: true}
		}
	}
	return byPath, true
}

// resolvedPath normalises a path for comparison, the way branchFromWorktreeList
// does: git reports symlink-resolved paths while a stored path may still carry
// a symlinked prefix (/var → /private/var on macOS).
func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
