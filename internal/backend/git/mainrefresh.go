package git

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RefreshMain implements backend.MainRefresher: fetch the base's remote, then
// fast-forward the project's main worktree onto it — under the same four
// conditions the create path uses, unchanged.
//
// Why this exists as its own entry point, next to the create path that already
// calls refreshMainWorktree: the create path only fires when someone runs
// `pigtail new`. It refreshes the main checkout when a worktree is *created
// from* it, never when an agent *reads* it — and reading is when the staleness
// hurts. Measured 2026-09-07: a main worktree 23 commits behind origin/main
// with a pristine tree, i.e. every condition for a safe fast-forward met, and
// nothing between two `pigtail new` invocations to trigger one. The daemon
// sweep (internal/daemon/refreshmain.go) is what calls this.
//
// The base is resolved exactly as Create resolves it minus the intent: the
// project default, else defaultBaseRef. There is no caller-supplied base here
// on purpose — a periodic pass must move the main worktree onto the ref the
// project itself declares, never onto one a request talked it into.
func (b *Backend) RefreshMain(ctx context.Context, p *pigtail.Project) (backend.MainRefresh, error) {
	if p == nil {
		return backend.MainRefresh{}, fmt.Errorf("refresh main: nil project")
	}
	gitDir := MainWorktreePath(p)
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		// A registered project whose container moved or was deleted. Not an
		// error worth failing the sweep over, but worth naming once.
		return backend.MainRefresh{
			Path:   gitDir,
			Action: backend.MainRefreshSkipped,
			Reason: "main worktree directory is missing",
		}, nil
	}

	base := ""
	if p.Defaults != nil {
		base = p.Defaults.BaseRef
	}
	if base == "" {
		base = defaultBaseRef(ctx, gitDir)
	}

	// Same order as Create: refresh the remote-tracking ref first, otherwise
	// the fast-forward below would only ever reach the last-known tip. Both
	// halves are best-effort and neither is fatal.
	fetchErr := fetchBaseRemoteErr(ctx, gitDir, base)
	res := refreshMainWorktree(ctx, gitDir, base)

	// A fetch that failed (offline, unreachable remote) does not stop the
	// fast-forward: whatever was already fetched is still an improvement, and
	// an *Updated* outcome is a real one. But "up-to-date with a ref we could
	// not refresh" is not the same claim as "up-to-date", and reporting it as
	// the latter would be the exact failure this pass exists to end — a stale
	// checkout that looks current. So it is downgraded to an abstention that
	// names what went wrong.
	if fetchErr != nil && res.Action == backend.MainRefreshUpToDate {
		res.Action = backend.MainRefreshSkipped
		res.Reason = fmt.Sprintf("could not fetch %s, so freshness is unproven (%v)", base, fetchErr)
	}
	return res, nil
}

// refreshMainWorktree fast-forwards the project's main worktree onto a
// remote-tracking base, so the checkout everyone READS is as fresh as the base
// we just branched from.
//
// Why this exists next to fetchBaseRemote: fetching updates refs, not the
// working tree. Branching from origin/main yields a correct new worktree while
// leaving the main directory itself stale — and that directory is what agents
// are pointed at to answer "what does main look like?". The session journal
// carries both failures separately: worktrees created behind origin/main, AND a
// comparison run against the stale main directory that raised a false duplicate.
//
// Acts alone ONLY when the gesture is strictly risk-free — all four required:
//   - the base is remote-tracking (nothing to fast-forward onto otherwise);
//   - the main worktree is on the base's own branch: not detached, not parked
//     on someone's feature branch;
//   - its working tree is clean — no tracked modification, no staged change,
//     no untracked file;
//   - the move is a genuine fast-forward (--ff-only), so no merge commit is
//     created and no history is rewritten.
//
// Anything else is left untouched: a dirty or repurposed main checkout belongs
// to whoever put it in that state, and a tool that "helpfully" moves it is the
// very accident this guard family exists to prevent.
//
// It returns what it did rather than printing it. Silence was fine while the
// only caller was an interactive `pigtail new` the user was watching; a
// background sweep that abstains without saying so is indistinguishable from
// one that never runs, so the decision now travels to a caller that can log it.
func refreshMainWorktree(ctx context.Context, gitDir, base string) backend.MainRefresh {
	res := backend.MainRefresh{Path: gitDir, Base: base}
	skip := func(format string, args ...any) backend.MainRefresh {
		res.Action = backend.MainRefreshSkipped
		res.Reason = fmt.Sprintf(format, args...)
		return res
	}

	// (1) remote-tracking base.
	slash := strings.IndexByte(base, '/')
	if slash <= 0 {
		return skip("base %q is local — nothing remote to fast-forward onto", base)
	}
	branch := base[slash+1:]
	if branch == "" {
		return skip("base %q names no branch", base)
	}

	// How far behind the base this checkout is, measured BEFORE any of the
	// remaining guards — so an abstention carries the size of the drift it is
	// leaving in place, not just its own reason. Read-only (`rev-list
	// --count`), so it loosens nothing: the four conditions below are still
	// the only thing that decides whether anything moves.
	res.Behind = behindCount(ctx, gitDir, base)

	// (2) on the base's own branch — not detached, not parked elsewhere.
	head, err := captureGit(ctx, gitDir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return skip("HEAD is detached or unreadable (%v)", err)
	}
	if got := strings.TrimSpace(head); got != branch {
		return skip("checked out on %q, not %q", got, branch)
	}

	// (3) pristine tree. --porcelain covers tracked modifications, staged
	// changes and untracked files in one shot; any output at all means the
	// tree is not pristine. The entries themselves stay out of the reason —
	// the count is what a reader needs, the paths are the user's business.
	status, err := captureGit(ctx, gitDir, "status", "--porcelain")
	if err != nil {
		return skip("working tree state unreadable (%v)", err)
	}
	if trimmed := strings.TrimSpace(status); trimmed != "" {
		n := len(strings.Split(trimmed, "\n"))
		return skip("working tree not pristine (%d uncommitted or untracked entr%s)", n, plural(n))
	}

	before := shortHead(ctx, gitDir)

	// (4) a genuine fast-forward.
	if err := runGit(ctx, gitDir, "merge", "--ff-only", base); err != nil {
		// The local branch carries commits of its own, or git refused for
		// another reason. Never fatal.
		return skip("not a fast-forward onto %s (%v)", base, err)
	}

	after := shortHead(ctx, gitDir)
	res.From, res.To = before, after
	if before == after {
		res.Action = backend.MainRefreshUpToDate
		res.From, res.To = "", ""
		return res
	}
	res.Action = backend.MainRefreshUpdated
	return res
}

// behindCount reports how many commits of base the checkout's HEAD is missing.
//
// `rev-list --count HEAD..base` answers that for any HEAD — on the base's own
// branch, parked on a feature branch, or detached — which is what makes it
// usable on the abstention paths, where the whole point is to say how much
// staleness is being left behind.
//
// 0 on any failure: an unfetched base, a repo that cannot answer. The count is
// there to make a message concrete, and a guessed number would be worse than
// no number — see MainRefresh.Behind, which callers must read as "no known
// drift" rather than "proven level".
func behindCount(ctx context.Context, gitDir, base string) int {
	out, err := captureGit(ctx, gitDir, "rev-list", "--count", "HEAD.."+base)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// shortHead returns the abbreviated HEAD SHA, or "" when git cannot say. Only
// ever used for a log line, so an empty answer is not worth an error.
func shortHead(ctx context.Context, gitDir string) string {
	out, err := captureGit(ctx, gitDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// logMainRefresh reports a create-path refresh through the backend's logger.
//
// It stays quiet about a *local* base: "you asked for a local base, so nothing
// was fast-forwarded" is a restatement of the request, and `pigtail new --base
// main` is a normal thing to do. Every other abstention is worth a line —
// before this, the only trace of one was a stderr print on the merge failure,
// so a main worktree left behind because it was parked on a feature branch
// looked exactly like one that had been refreshed.
func (b *Backend) logMainRefresh(res backend.MainRefresh) {
	if b == nil || b.logf == nil {
		return
	}
	switch res.Action {
	case backend.MainRefreshUpdated:
		b.logf("pigtail: main worktree %s fast-forwarded onto %s%s (%s → %s)",
			res.Path, res.Base, wasBehindClause(res.Behind), res.From, res.To)
	case backend.MainRefreshSkipped:
		if strings.IndexByte(res.Base, '/') <= 0 {
			return // local base: the caller asked for exactly this
		}
		b.logf("pigtail: main worktree %s left as-is%s — %s",
			res.Path, isBehindClause(res.Behind), res.Reason)
	}
}

// wasBehindClause / isBehindClause render the drift as an insertable clause,
// empty when there is no known drift (see MainRefresh.Behind: 0 also means
// "could not measure", so saying nothing is the honest rendering). Past tense
// for a move that closed the gap, present for one that left it open.
func wasBehindClause(behind int) string {
	if behind <= 0 {
		return ""
	}
	return fmt.Sprintf(", %s behind", commitCount(behind))
}

func isBehindClause(behind int) string {
	if behind <= 0 {
		return ""
	}
	return fmt.Sprintf(", still %s behind", commitCount(behind))
}

// commitCount pluralises a commit count: "1 commit", "23 commits".
func commitCount(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}
