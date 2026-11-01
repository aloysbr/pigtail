package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// teardownBudget bounds a workspace teardown once it has started. It is a
// backstop against a hung git process, NOT a UX budget: removing a worktree
// with a populated node_modules is tens of thousands of unlink() calls and
// legitimately takes minutes. The UX budget belongs to the client (see
// `pigtail clean --timeout`), which may give up long before this fires — the
// teardown then keeps running to completion in the daemon.
const teardownBudget = 30 * time.Minute

// CleanupRequest is the optional body of DELETE /workspaces/:id.
type CleanupRequest struct {
	Force      bool `json:"force,omitempty"`
	KeepBranch bool `json:"keep_branch,omitempty"`
}

// CleanupResponse describes what was actually done.
type CleanupResponse struct {
	WorkspaceID string `json:"workspace_id"`

	// BranchDeleted / BranchKept / BranchKeptReason mirror
	// backend.CleanupOutcome. They exist so `✓ cleaned <id>` stops being an
	// unqualified claim: a teardown that declines to delete a branch has to
	// say so where the user is looking (issue #53).
	BranchDeleted    string `json:"branch_deleted,omitempty"`
	BranchKept       string `json:"branch_kept,omitempty"`
	BranchKeptReason string `json:"branch_kept_reason,omitempty"`

	// ServersStopped lists the dev servers stopped before the teardown.
	ServersStopped []pigtail.StoppedServer `json:"servers_stopped,omitempty"`
}

// CleanupWorkspace handles DELETE /workspaces/:id.
//
// Steps:
//  1. Load workspace (404 if unknown).
//  2. Refuse if any AgentSession is still running, unless --force.
//  3. Stop the workspace's dev servers (process trees) and prove them gone:
//     409 DEV_SERVER_RUNNING otherwise, nothing removed, --force included.
//  4. Kill tmux sessions of the workspace's agents.
//  5. Call Backend.Cleanup (with same force flag).
//  6. Delete workspace + its agent sessions from the store.
//  7. Emit cleanup_started/done + deleted events.
func CleanupWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req CleanupRequest
		// Body is optional.
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
				return
			}
		}

		ws, err := svc.Store.GetWorkspace(id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrWorkspaceNotFound, "no such workspace", map[string]interface{}{"id": id})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		// One teardown at a time per workspace: two concurrent
		// `git worktree remove` on the same path corrupt each other's
		// administrative records.
		if !svc.cleanups.acquire(id) {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
				"cleanup already in progress for this workspace",
				map[string]interface{}{"id": id})
			return
		}
		defer svc.cleanups.release(id)

		// Safety check: agents still running.
		if !req.Force {
			for _, sess := range ws.AgentSessions {
				if sess.State == pigtail.AgentStateRunning {
					resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
						"agent is still running; pause/kill it first or pass force=true",
						map[string]interface{}{"agent_session_id": sess.ID})
					return
				}
			}
		}

		svc.Bus.Publish(pigtail.Event{
			Type: pigtail.EventWorkspaceCleanupStart,
			Data: map[string]interface{}{"workspace_id": ws.ID},
		})
		started := time.Now()

		// The teardown is destructive and not resumable for free: killing git
		// halfway through `worktree remove` leaves a directory full of deleted
		// tracked files. Detach it from the request context so a client that
		// disconnects (Ctrl-C, its own timeout) cannot corrupt the worktree.
		// WithoutCancel keeps the context values — notably the request_id set
		// by requestIDMW — so the logs stay correlatable.
		tctx, tcancel := context.WithTimeout(context.WithoutCancel(r.Context()), teardownBudget)
		defer tcancel()

		// cleanupFailed terminates the cleanup_started event published above:
		// without it, a failed teardown leaves subscribers hanging on a start
		// with no end. It also puts the reason in the daemon log, where the
		// git output (carried by the error) is the only trace of what the
		// filesystem actually did.
		cleanupFailed := func(err error) {
			svc.logf("cleanup failed: workspace=%s path=%s force=%v elapsed=%s: %v",
				ws.ID, ws.Path, req.Force, time.Since(started).Round(time.Millisecond), err)
			svc.Bus.Publish(pigtail.Event{
				Type: pigtail.EventWorkspaceCleanupFailed,
				Data: map[string]interface{}{"workspace_id": ws.ID, "error": err.Error()},
			})
		}

		// Step 3: dev servers first, before any agent, backend or state
		// removal. A server that survives refuses the whole clean — and
		// --force does not lift it.
		stopped, err := stopWorkspaceServers(svc, ws)
		if err != nil {
			cleanupFailed(err)
			var sre *devserver.ServersRunningError
			if errors.As(err, &sre) {
				resp.Error(w, r, statusForCode(pigtail.ErrDevServerRunning), pigtail.ErrDevServerRunning,
					err.Error()+" — nothing was removed; stop it, then retry",
					map[string]interface{}{"workspace_id": ws.ID, "servers": sre.Failures})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		// Stop activity watchers + kill agent tmux sessions.
		for i := range ws.AgentSessions {
			if svc.Watcher != nil {
				svc.Watcher.Stop(ws.AgentSessions[i].ID)
			}
			if svc.Agent != nil {
				_ = svc.Agent.Kill(tctx, &ws.AgentSessions[i])
			}
		}

		// Best-effort: the backend can use the project to find its repo when
		// the workspace directory is too broken to answer for itself. A
		// missing project row is not fatal — the backend copes with nil.
		proj, _ := svc.Store.GetProject(ws.ProjectID)

		// Resolved BEFORE the teardown, while the worktree still exists:
		// devserver keys its state directory on the symlink-resolved worktree
		// path, which can only be resolved on a path that is still there.
		statePath := devserver.StateDir(ws.Path)

		var outcome backend.CleanupOutcome
		if svc.Backend != nil {
			var err error
			outcome, err = svc.Backend.Cleanup(tctx, ws, backend.CleanupOptions{
				Force:      req.Force,
				KeepBranch: req.KeepBranch,
				Project:    proj,
			})
			// A branch the teardown declined to touch is worth a daemon-side
			// line whether or not the teardown then failed: it is the only
			// record once the response is gone.
			if outcome.BranchKept != "" {
				svc.logf("cleanup kept branch: workspace=%s branch=%s reason=%s",
					ws.ID, outcome.BranchKept, outcome.BranchKeptReason)
			}
			if err != nil {
				cleanupFailed(err)
				var apiErr *pigtail.APIError
				if errors.As(err, &apiErr) {
					resp.Error(w, r, statusForCode(apiErr.Code), apiErr.Code, apiErr.Message, apiErr.Details)
					return
				}
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrBackendError, err.Error(), nil)
				return
			}
		}

		// The worktree is gone; so is anything pigtail recorded about its
		// servers. Runtime state now lives outside the worktree, so it no
		// longer disappears with it — without this it would accumulate, and a
		// dev server's log is the part that is measured in megabytes.
		// Best-effort: leftover state is untidy, never incorrect.
		_ = os.RemoveAll(statePath)

		if err := svc.Store.DeleteWorkspace(ws.ID); err != nil {
			cleanupFailed(err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "delete workspace: "+err.Error(), nil)
			return
		}

		svc.Bus.Publish(pigtail.Event{
			Type: pigtail.EventWorkspaceCleanupDone,
			Data: map[string]interface{}{"workspace_id": ws.ID},
		})
		svc.Bus.Publish(pigtail.Event{
			Type: pigtail.EventWorkspaceDeleted,
			Data: map[string]interface{}{"workspace_id": ws.ID},
		})

		resp.JSON(w, r, http.StatusOK, CleanupResponse{
			WorkspaceID:      ws.ID,
			BranchDeleted:    outcome.BranchDeleted,
			BranchKept:       outcome.BranchKept,
			BranchKeptReason: outcome.BranchKeptReason,
			ServersStopped:   stopped,
		})
	}
}
