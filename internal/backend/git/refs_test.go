package git

import (
	"context"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `BackendData["branch"]` is stamped at creation and never refreshed, so a
// `git switch` inside a worktree leaves the record describing a branch the
// worktree left days ago. Issue #53 stopped the *teardown* from trusting that
// snapshot; what remained is that `pigtail list` shows the stale name, and that
// every switched-away worktree now has its branch deliberately left behind at
// teardown — safe, but it accumulates.
//
// ResolveRefs is the live answer. It is a batch call on purpose: `git worktree
// list --porcelain` names every worktree of a repo in one invocation, so
// refreshing thirteen worktrees costs one git call, not thirteen. That is what
// makes a periodic reconciliation affordable at all.

func TestResolveRefsReportsTheBranchAWorktreeActuallyHolds(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "say hello", []string{"feat", "say-hello"})
	_ = mainDir

	b := New(nil)
	got, err := b.ResolveRefs(context.Background(), []pigtail.Workspace{*ws})
	if err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}
	if got[ws.ID] != "feat/say-hello" {
		t.Fatalf("ResolveRefs = %v, want %s → feat/say-hello", got, ws.ID)
	}

	// The case the whole entry is about: the worktree moves on, the record
	// does not.
	switchTo(t, ws.Path, "feat/somewhere-else")
	got, err = b.ResolveRefs(context.Background(), []pigtail.Workspace{*ws})
	if err != nil {
		t.Fatalf("ResolveRefs after switch: %v", err)
	}
	if got[ws.ID] != "feat/somewhere-else" {
		t.Fatalf("ResolveRefs = %v, want the branch git actually has", got)
	}
	// And the stored snapshot is untouched: resolving is not persisting.
	if stored, _ := ws.BackendData["branch"].(string); stored != "feat/say-hello" {
		t.Fatalf("ResolveRefs mutated the workspace record: %q", stored)
	}
}

// A detached HEAD holds no branch. Reporting "" would be indistinguishable
// from "resolved to nothing", and a caller that writes it back erases a real
// name with an empty one. Absent from the map is the only honest answer.
func TestResolveRefsOmitsADetachedWorktree(t *testing.T) {
	requireGit(t)
	_, ws := scratchRepoAndWorkspace(t, "detach me", []string{"feat", "detach-me"})

	gitRun(t, ws.Path, "checkout", "-q", "--detach")

	b := New(nil)
	got, err := b.ResolveRefs(context.Background(), []pigtail.Workspace{*ws})
	if err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}
	if ref, present := got[ws.ID]; present {
		t.Fatalf("a detached worktree resolved to %q; it must be absent from the map", ref)
	}
}

// A workspace whose directory is gone cannot answer, and must not be guessed
// at either. Same rule as the detached case: absent, not empty.
func TestResolveRefsOmitsAnUnresolvableWorkspace(t *testing.T) {
	requireGit(t)
	b := New(nil)

	got, err := b.ResolveRefs(context.Background(), []pigtail.Workspace{
		{ID: "ws_gone", Path: "/nonexistent/definitely/not/here"},
	})
	if err != nil {
		t.Fatalf("ResolveRefs must not fail over one unreachable workspace: %v", err)
	}
	if _, present := got["ws_gone"]; present {
		t.Fatal("an unreachable workspace must be absent from the map")
	}
}

// The batching claim, asserted rather than assumed: several worktrees of one
// repo must cost one `worktree list`, since that is the argument for running
// this on a timer at all.
func TestResolveRefsBatchesOneGitCallPerRepo(t *testing.T) {
	requireGit(t)
	_, wsA := scratchRepoAndWorkspace(t, "first one", []string{"feat", "first-one"})

	b := New(nil)
	var calls int
	b.countWorktreeList = func() { calls++ }

	if _, err := b.ResolveRefs(context.Background(), []pigtail.Workspace{*wsA, *wsA, *wsA}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("three workspaces of one repo cost %d `worktree list` calls, want 1", calls)
	}
}

func TestResolveRefsOnAnEmptyListDoesNothing(t *testing.T) {
	b := New(nil)
	got, err := b.ResolveRefs(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want an empty map", got)
	}
}
