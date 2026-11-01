package git

import (
	"context"
	"strings"
)

// PaneRepo is what subject suggest reads of the repository a pane sits in.
type PaneRepo struct{ Branch, DefaultBranch, Repo string }

// ReadPaneRepo reads, with three read-only git calls (no write, no fetch):
//   - Branch: symbolic-ref --short -q HEAD ("" when detached);
//   - DefaultBranch: symbolic-ref --short refs/remotes/origin/HEAD without its
//     "origin/" prefix — the first choice of defaultBaseRef, without its
//     network fallbacks ("" when absent);
//   - Repo: the last segment of remote.origin.url without ".git" ("" when absent).
//
// Outside a repository or on any error the field is "": it never fails.
func ReadPaneRepo(ctx context.Context, dir string) PaneRepo {
	var out PaneRepo
	if dir == "" {
		return out
	}
	read := func(args ...string) string {
		b, err := gitCmd(ctx, dir, args...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	out.Branch = read("symbolic-ref", "--short", "-q", "HEAD")
	out.DefaultBranch = strings.TrimPrefix(read("symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"), "origin/")
	if url := read("config", "--get", "remote.origin.url"); url != "" {
		url = strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
		if i := strings.LastIndexAny(url, "/:"); i >= 0 {
			url = url[i+1:]
		}
		out.Repo = url
	}
	return out
}
