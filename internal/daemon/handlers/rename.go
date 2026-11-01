package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// backendBranchKey is the BackendData key used by the git backend to persist
// the branch name. Only the rename handler writes it; all readers MUST call
// backend.Backend.DisplayRef instead (architecture.md §8).
const backendBranchKey = "branch"

// RenameWorkspace handles POST /workspaces/{id}/rename.
//
// It relocates a workspace's worktree directory to a new slug under the
// project root (and optionally renames its branch), updating the store to
// match. State, workspace ID, agent sessions, and tmux session names are
// left untouched — only the path (and, with rename_branch, backend_data)
// change.
//
// Preflight guards:
//   - A live agent session (running / paused) blocks the rename even with
//     force — moving the worktree out from under a working agent is unsafe.
//   - Busy dev servers block the rename; force kills their PIDs first.
//   - The destination must not already exist on disk nor be claimed by
//     another workspace row.
func RenameWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		var req pigtail.RenameRequest
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
				return
			}
		}

		// --- validate the slug -------------------------------------------
		slug := strings.TrimSpace(req.NewSlug)
		if slug == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "new_slug is required", nil)
			return
		}
		if strings.ContainsAny(slug, "/\\") || slug == "." || strings.Contains(slug, "..") {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"new_slug must be a single path segment (no separators or '..')",
				map[string]interface{}{"new_slug": slug})
			return
		}

		// --- load workspace + project ------------------------------------
		ws, err := svc.Store.GetWorkspace(id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrWorkspaceNotFound, "no such workspace", map[string]interface{}{"id": id})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		proj, err := svc.Store.GetProject(ws.ProjectID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrProjectNotFound, "workspace's project not found", map[string]interface{}{"project_id": ws.ProjectID})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if proj.RootPath == "" {
			resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError, "project has no root_path", nil)
			return
		}

		newPath := filepath.Join(proj.RootPath, slug)
		// Defence in depth: the slug validation above guarantees a single
		// path segment, so the join must land directly under the root.
		if filepath.Dir(newPath) != filepath.Clean(proj.RootPath) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"new_slug escapes the project root",
				map[string]interface{}{"new_slug": slug})
			return
		}

		// --- idempotent no-op when the path already matches --------------
		// (still applies a branch rename if requested and the slug differs
		// from the current branch tail).
		if ws.Path == newPath && !req.RenameBranch {
			resp.JSON(w, r, http.StatusOK, pigtail.RenameResponse{
				WorkspaceID: ws.ID, OldPath: ws.Path, NewPath: newPath,
			})
			return
		}

		// --- guard: live agent (force does NOT override) -----------------
		for _, sess := range ws.AgentSessions {
			if isLiveAgentState(sess.State) {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
					"an agent is live on this workspace; pause/kill it before renaming",
					map[string]interface{}{"agent_session_id": sess.ID})
				return
			}
		}

		// --- guard: busy dev servers (force kills them) ------------------
		// The ports are the ones .env.local names (the resolver rammon, the
		// watchdog and the TUI share), and only listeners running inside the
		// worktree are this workspace's servers. A port held from another
		// worktree is refused before any kill: the rename has no business
		// killing another workspace's server.
		var cfg *pigtail.ProjectConfig
		if dctx, cerr := devserver.ResolveContextFromPath(ws.Path); cerr == nil && dctx != nil {
			cfg = dctx.Config
		}
		var busyPIDs []int
		busyPorts := map[string]int{}
		wsPorts := devserver.WorkspacePorts(ws.Path, cfg, ws.Ports)
		kinds := make([]string, 0, len(wsPorts))
		for kind := range wsPorts {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			port := wsPorts[kind]
			own, _ := listenerOf(svc, port, ws.Path)
			if own.HeldElsewhere() {
				msg := fmt.Sprintf("port %d is %s; it is not this workspace's server — run \"pigtail ports reassign\" first",
					port, own.Describe(filepath.Dir(ws.Path)))
				if own.Unknown() {
					msg = fmt.Sprintf("port %d: %s — not stopped; retry the rename", port, own.Describe(filepath.Dir(ws.Path)))
				}
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, msg,
					heldElsewhereDetails(own, filepath.Dir(ws.Path), kind, port))
				return
			}
			if len(own.Owned) > 0 {
				busyPorts[kind] = port
				busyPIDs = append(busyPIDs, own.Owned...)
			}
		}
		if len(busyPorts) > 0 {
			if !req.Force {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
					"a dev server is running on this workspace; stop it or pass force=true",
					map[string]interface{}{"ports": busyPorts})
				return
			}
			// A forced rename stops this workspace's dev servers on purpose, so
			// mark the stops before killing. Without it the next sweep reports
			// each one as a server that died on its own: the workspace row
			// stays in the store (so its servers are still expected) and their
			// ports go free — the exact shape of a death. Marked in-process
			// like every other daemon stop path; the daemon never notifies
			// itself over HTTP.
			if svc.RAMMonitor != nil {
				svc.RAMMonitor.MarkIntentionalStopsForWorkspace(ws.ID)
			}
			killPIDs(svc, busyPIDs)
		}

		// --- guard: destination collision --------------------------------
		if ws.Path != newPath {
			if _, err := os.Stat(newPath); err == nil {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
					"destination directory already exists",
					map[string]interface{}{"path": newPath})
				return
			} else if !errors.Is(err, os.ErrNotExist) {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "stat destination: "+err.Error(), nil)
				return
			}
			// Another workspace row already owns this path?
			if other, found, lerr := workspaceByPath(svc, newPath); lerr != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "lookup path: "+lerr.Error(), nil)
				return
			} else if found && other != ws.ID {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
					"another workspace already uses this path",
					map[string]interface{}{"path": newPath, "workspace_id": other})
				return
			}
		}

		if svc.Backend == nil {
			resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError, "no backend configured", nil)
			return
		}

		oldPath := ws.Path

		// --- move the worktree -------------------------------------------
		if oldPath != newPath {
			if err := svc.Backend.Move(r.Context(), ws, newPath); err != nil {
				var apiErr *pigtail.APIError
				if errors.As(err, &apiErr) {
					resp.Error(w, r, statusForCode(apiErr.Code), apiErr.Code, apiErr.Message, apiErr.Details)
					return
				}
				resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError, "move worktree: "+err.Error(), nil)
				return
			}
		}

		// --- optionally rename the branch --------------------------------
		var newBranch string
		var updatedBackendData map[string]any
		if req.RenameBranch {
			oldBranch := svc.Backend.DisplayRef(ws)
			newBranch = slug
			if oldBranch != newBranch {
				if err := svc.Backend.RenameBranch(r.Context(), ws, newBranch); err != nil {
					// The directory already moved; report a partial failure
					// rather than silently leaving an inconsistent state.
					resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError,
						"worktree moved but branch rename failed: "+err.Error(),
						map[string]interface{}{"old_path": oldPath, "new_path": newPath})
					return
				}
				updatedBackendData = cloneBackendData(ws.BackendData)
				updatedBackendData[backendBranchKey] = newBranch
			} else {
				newBranch = "" // nothing changed
			}
		}

		// --- persist the new path (and backend_data) ---------------------
		if err := svc.Store.UpdateWorkspacePath(ws.ID, newPath, updatedBackendData); err != nil {
			// The on-disk move succeeded but the store update failed. Try a
			// best-effort compensating move back so disk and DB agree.
			if oldPath != newPath {
				ws.Path = newPath
				if rbErr := svc.Backend.Move(context.Background(), ws, oldPath); rbErr == nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal,
						"persist failed; rolled the worktree back to its original path: "+err.Error(),
						map[string]interface{}{"path": oldPath})
					return
				}
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal,
				"persist failed AND rollback failed; worktree is at "+newPath+
					" but the store still points at "+oldPath+
					" — run `pigtail gc` then `pigtail adopt` to reconcile: "+err.Error(),
				map[string]interface{}{"old_path": oldPath, "new_path": newPath})
			return
		}

		svc.Bus.Publish(pigtail.Event{
			Type: pigtail.EventWorkspaceRenamed,
			Data: map[string]interface{}{
				"workspace_id": ws.ID,
				"old_path":     oldPath,
				"new_path":     newPath,
				"new_branch":   newBranch,
			},
		})

		resp.JSON(w, r, http.StatusOK, pigtail.RenameResponse{
			WorkspaceID: ws.ID,
			OldPath:     oldPath,
			NewPath:     newPath,
			NewBranch:   newBranch,
		})
	}
}

// workspaceByPath returns the id of the workspace owning path, if any.
func workspaceByPath(svc *Services, path string) (id string, found bool, err error) {
	all, err := svc.Store.ListWorkspaces("")
	if err != nil {
		return "", false, err
	}
	for i := range all {
		if all[i].Path == path {
			return all[i].ID, true, nil
		}
	}
	return "", false, nil
}

// cloneBackendData returns a shallow copy of src so we can mutate it without
// touching the cached workspace's map.
func cloneBackendData(src map[string]interface{}) map[string]any {
	out := make(map[string]any, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	return out
}
