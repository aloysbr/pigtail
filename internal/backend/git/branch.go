package git

import (
	"path/filepath"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// DeriveBranch is the branch Create makes for an intent: override when set,
// else <prefix><slug(intent)> (prefix: the project's defaults.branch_prefix,
// else "feat/"), the prefix's type not repeated, "agent-<id>" for an empty slug.
// Exported so `pigtail session spawn --new-worktree --dry-run` prints the exact
// branch the real run creates.
func DeriveBranch(p *pigtail.Project, intent, override string) string {
	if override != "" {
		return override
	}
	prefix := "feat/"
	if p.Defaults != nil && p.Defaults.BranchPrefix != "" {
		prefix = p.Defaults.BranchPrefix
	}
	slug := dedupePrefix(slugify(intent), prefix)
	if slug == "" {
		slug = "agent-" + shortID()
	}
	return prefix + slug
}

// WorktreePathFor is where Create puts the worktree of branch: a child folder
// of the project container, `<root>/feat-auth-refactor` for `feat/auth-refactor`.
func WorktreePathFor(p *pigtail.Project, branch string) string {
	return filepath.Join(p.RootPath, strings.ReplaceAll(branch, "/", "-"))
}

// MainWorktreePath is the project's main worktree inside its container. Git
// commands must run from here (not the container itself).
func MainWorktreePath(p *pigtail.Project) string {
	name := p.MainWorktree
	if name == "" {
		name = "main"
	}
	return filepath.Join(p.RootPath, name)
}
