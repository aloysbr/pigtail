package backend

import (
	"context"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// MainRefreshAction is what a refresh pass did with a project's main worktree.
type MainRefreshAction string

const (
	// MainRefreshUpdated: the main worktree was fast-forwarded onto its base.
	MainRefreshUpdated MainRefreshAction = "updated"

	// MainRefreshUpToDate: it was already at the base — nothing to do, and
	// nothing unsafe about it either.
	MainRefreshUpToDate MainRefreshAction = "up-to-date"

	// MainRefreshSkipped: the backend declined to touch it. Reason says why,
	// in a clause fit to show a human.
	MainRefreshSkipped MainRefreshAction = "skipped"
)

// MainRefresh reports one attempt at refreshing a project's main worktree.
//
// Skipping is a first-class outcome, not an error: the whole value of this
// operation is that it *abstains* whenever the gesture would not be strictly
// risk-free, and an abstention a caller cannot name is indistinguishable from
// a feature that never ran — which is exactly how the staleness it fixes
// survived for months.
type MainRefresh struct {
	// Path is the main worktree directory the pass looked at.
	Path string

	// Base is the ref it would have fast-forwarded onto (e.g. "origin/main").
	Base string

	// Action is what happened.
	Action MainRefreshAction

	// Reason explains an abstention. Empty exactly when Action is not
	// MainRefreshSkipped.
	Reason string

	// From and To are short SHAs, set only when Action is MainRefreshUpdated.
	From, To string

	// Behind is how many commits of Base the main worktree was missing when
	// the pass looked — the size of the drift, measured before any move.
	//
	// It is what turns a verdict into a sentence a human acts on: "left as-is
	// because the tree is dirty" is a fact about pigtail, "23 commits behind
	// and left as-is because the tree is dirty" is a fact about the checkout
	// they are about to read. Carried on *every* action, abstentions
	// included — an abstention over a checkout that is level costs nothing,
	// one over a checkout 23 commits behind is the bug this family exists for.
	//
	// 0 means level, or not measurable: a backend that cannot count (no
	// remote-tracking base to count against, a directory that is gone) says 0
	// rather than guessing, so a caller must read it as "no known drift",
	// never as "proven level".
	Behind int
}

// MainRefresher fast-forwards a project's *main* worktree onto its base ref,
// so the checkout that gets READ stops drifting behind the ones pigtail
// creates from it.
//
// It is an optional capability on the MergeInspector model (architecture.md
// §9): meaningful for a backend that has one shared, long-lived checkout
// people read from — git — and meaningless for one where every workspace is
// self-contained. A caller asserts and degrades:
//
//	if mr, ok := be.(backend.MainRefresher); ok { … }
//
// Implementations MUST be conservative to the point of uselessness rather
// than move a checkout someone else is using: a shared working tree belongs
// to whoever put it in its current state, and a tool that "helpfully" moves
// it is the accident this whole guard family exists to prevent. Declining is
// reported through MainRefresh.Reason, never as an error — the error is
// reserved for "the question could not be asked at all".
type MainRefresher interface {
	RefreshMain(ctx context.Context, p *pigtail.Project) (MainRefresh, error)
}
