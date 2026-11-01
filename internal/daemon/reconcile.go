package daemon

import (
	"context"
	"log"
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `BackendData["branch"]` is stamped when a workspace is created and never
// refreshed, so a `git switch` inside a worktree leaves the record naming a
// branch the worktree left days ago. Issue #53 stopped the *teardown* from
// trusting that snapshot — the destructive half — and what remained is that
// `pigtail list` lies, and that every switched-away worktree has its branch
// deliberately kept at teardown: safe, but it accumulates.
//
// This is the pass that keeps the record true. Two design constraints put it
// here rather than in a handler:
//
//   - `DisplayRef(ws)` is pure and synchronous — no context, no error — so it
//     cannot do git I/O at all. Giving it a store handle would not change that:
//     the signature is the constraint, not the dependency.
//   - refreshing inside `GET /workspaces` would mean a git call on an endpoint
//     the menu bar polls every few seconds, and a read that writes to the
//     store.
//
// So `DisplayRef` keeps stamping from the record, unchanged, and a background
// pass keeps the record worth stamping from. One `git worktree list
// --porcelain` per project per tick — the batching in
// backend.RefResolver is what makes that cheap enough to be uninteresting.

// branchReconcileInterval is how often the records are checked against git.
//
// Minutes rather than seconds: nothing breaks while a record is stale — the
// teardown resolves from git anyway since #53 — so this only has to be faster
// than a human noticing `pigtail list` is wrong, not faster than a `git
// switch`.
const branchReconcileInterval = 5 * time.Minute

// runBranchReconcile keeps BackendData["branch"] in step with git.
func (s *Server) runBranchReconcile(ctx context.Context) {
	resolver, ok := s.backend.(backend.RefResolver)
	if !ok {
		return // a backend whose workspaces cannot move refs behind our back
	}

	t := time.NewTicker(branchReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reconcileBranchesOnce(ctx, resolver)
		}
	}
}

func (s *Server) reconcileBranchesOnce(ctx context.Context, resolver backend.RefResolver) {
	if s.store == nil {
		return
	}
	// Every state, not just the active ones: a workspace parked as shipped is
	// exactly the one whose record will be read at teardown.
	wss, err := s.store.ListWorkspaces("")
	if err != nil {
		log.Printf("daemon: branch reconciliation could not list workspaces: %v", err)
		return
	}
	reconcileBranchRecords(ctx, wss, resolver, s.store.UpdateWorkspaceBackendData, log.Printf)
}

// reconcileBranchRecords compares each workspace's recorded branch against the
// one git reports and writes back the differences. Returns how many records
// were updated.
//
// Free of the Server and the store so the rules below are testable as rules.
// The logger may be nil.
func reconcileBranchRecords(
	ctx context.Context,
	wss []pigtail.Workspace,
	resolver backend.RefResolver,
	update func(id string, backendData map[string]any) error,
	logf func(format string, args ...interface{}),
) int {
	if resolver == nil || len(wss) == 0 {
		return 0
	}
	refs, err := resolver.ResolveRefs(ctx, wss)
	if err != nil {
		// A failed resolve is not evidence about any workspace. Write nothing
		// and try again next tick.
		if logf != nil {
			logf("daemon: branch reconciliation could not resolve refs: %v", err)
		}
		return 0
	}

	updated := 0
	for _, ws := range wss {
		live, found := refs[ws.ID]
		// The rule this whole pass rests on: **absent never clears a record**.
		// A detached HEAD, a vanished directory and a git that could not answer
		// all arrive as "absent", and the record is precisely the last known
		// name for when nothing else can say. Overwriting it with "" would
		// throw away the only thing the teardown has left to work from.
		if !found || live == "" {
			continue
		}
		stored, _ := ws.BackendData["branch"].(string)
		if stored == live {
			continue
		}

		// Copy rather than mutate: BackendData belongs to the caller's slice,
		// and the other keys (base_branch above all) must survive a refresh
		// that only knows about one of them.
		next := make(map[string]any, len(ws.BackendData)+1)
		for k, v := range ws.BackendData {
			next[k] = v
		}
		next["branch"] = live

		if err := update(ws.ID, next); err != nil {
			if logf != nil {
				logf("daemon: could not refresh the recorded branch of %s: %v", ws.ID, err)
			}
			continue
		}
		updated++
		if logf != nil {
			// Worth a line each: this drift is confusing to meet later with no
			// record of when the record and the worktree parted ways.
			logf("daemon: workspace %s is on %q, not the recorded %q; record refreshed",
				ws.ID, live, stored)
		}
	}
	return updated
}
