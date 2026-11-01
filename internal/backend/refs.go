package backend

import (
	"context"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RefResolver reports the work ref each workspace *currently* holds, as
// opposed to the one recorded when it was created.
//
// It is an optional capability on the `MergeInspector` model (architecture.md
// §9): meaningful for a backend whose workspaces can move to another ref
// behind pigtail's back — git, where a `git switch` is a normal thing to do
// mid-task — and meaningless for one where the workspace *is* the ref. A
// caller asserts and degrades:
//
//	if rr, ok := be.(backend.RefResolver); ok { … }
//
// It is deliberately a batch call. `git worktree list --porcelain` names every
// worktree of a repo in one invocation, so refreshing thirteen workspaces costs
// one git call rather than thirteen — which is what makes running this
// periodically affordable instead of a per-request cost on a polled endpoint.
type RefResolver interface {
	// ResolveRefs maps workspace ID → the ref that workspace holds now.
	//
	// A workspace is **absent from the map** when its ref cannot be
	// established — it holds no ref (a detached HEAD), its directory is gone,
	// or the backend could not answer. Absent must never be read as "clear the
	// record": the whole point of the record is to be the last known name when
	// nothing else can say.
	//
	// The error is reserved for a failure that says nothing about any
	// particular workspace. One unreachable workspace among many is reported
	// by omission, not by failing the call.
	ResolveRefs(ctx context.Context, wss []pigtail.Workspace) (map[string]string, error)
}
