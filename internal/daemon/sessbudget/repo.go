package sessbudget

import (
	"os"
	"path/filepath"
	"strings"
)

// repoFromCwd names the git repository containing dir without running git:
// it walks up to the first `.git` entry. A `.git` directory names its parent
// folder; a `.git` file (`gitdir: …`, a linked worktree) is followed through
// its `commondir` to the main repository, so a linked worktree reports its
// repo, not its branch folder. "" when no repository is found.
func repoFromCwd(dir string) string {
	if dir == "" {
		return ""
	}
	for d := filepath.Clean(dir); ; {
		gp := filepath.Join(d, ".git")
		if fi, err := os.Lstat(gp); err == nil {
			if fi.IsDir() {
				return filepath.Base(d)
			}
			if name := mainRepoOfGitFile(gp, d); name != "" {
				return name
			}
			return filepath.Base(d) // an unreadable pointer: the folder itself
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// mainRepoOfGitFile follows a `.git` file to the common git dir and returns
// the folder name of the main repository.
func mainRepoOfGitFile(gitFile, base string) string {
	b, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	gitdir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(base, gitdir)
	}
	common := gitdir
	if cb, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		c := strings.TrimSpace(string(cb))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		common = filepath.Clean(c)
	}
	if filepath.Base(common) == ".git" {
		return filepath.Base(filepath.Dir(common))
	}
	return filepath.Base(common) // a bare repository
}
