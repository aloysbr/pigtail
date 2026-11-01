package backend

import (
	"context"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// MergeStatus is the three-valued answer to "is this workspace's work already
// contained in its base ref?".
//
// Three values, not two, on purpose. A backend can prove containment, but it
// can almost never prove *absence* of containment — a squash whose conflicts
// were resolved by hand leaves no trace a mechanical check can follow. Callers
// that destroy things must be able to tell "we looked and found nothing" from
// "we could not look", so that a sweep never reports 0 candidates when the
// truth is 0 answers (issue #54).
type MergeStatus string

const (
	// MergeContained: the work is provably present in the base ref.
	MergeContained MergeStatus = "contained"

	// MergeAbsent: every available check ran and found no trace of the work
	// in the base ref. NOT a proof that the work is unmerged — an undetectable
	// merge style lands here too. Safe to treat as "don't delete".
	MergeAbsent MergeStatus = "absent"

	// MergeUnknown: the question could not be answered (missing refs, a
	// vanished worktree, a git failure, an exhausted scan budget, a cancelled
	// context). Callers MUST surface a count of these rather than folding them
	// into MergeAbsent.
	MergeUnknown MergeStatus = "unknown"
)

// MergeState is the result of a containment check against a base ref.
type MergeState struct {
	// Status is the three-valued verdict. See MergeStatus.
	Status MergeStatus

	// How names the detection path that proved containment. Backend-defined;
	// the git backend uses "merged" (ancestor), "squashed" (patch-equivalent
	// commits) and "squash-merged" (net change carried by one base commit).
	// Empty unless Status is MergeContained.
	How string

	// BaseRef is the ref the comparison was actually made against, which may
	// differ from the workspace's recorded base (e.g. the base's upstream was
	// fresher). Shown to the user so a surprising verdict is explainable.
	BaseRef string

	// Note is a short human-readable addendum: the evidence when contained
	// (e.g. the base commit carrying the work), or the reason when unknown.
	Note string
}

// Contained reports whether the work is provably in the base ref.
func (m MergeState) Contained() bool { return m.Status == MergeContained }

// MergeInspector is an OPTIONAL backend capability: deciding whether a
// workspace's work is already contained in its base ref.
//
// Optional rather than part of Backend because the answer is only meaningful
// for backends with a notion of a shared base that work flows into. A
// filesystem or Notion backend may have no such thing, and forcing a stub
// implementation on it would be worse than an honest type assertion
// (architecture.md §9). Callers degrade gracefully:
//
//	if mi, ok := be.(backend.MergeInspector); ok { … }
//
// Implementations MUST honor ctx: this runs during the *selection* phase of a
// destructive sweep, before the first deletion, where a hang is invisible to
// any per-deletion timeout.
type MergeInspector interface {
	MergeState(ctx context.Context, ws *pigtail.Workspace) (MergeState, error)
}
