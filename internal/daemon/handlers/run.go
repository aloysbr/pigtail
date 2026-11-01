package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sharedWorktreePreamble is prepended to an agent's intent when it joins a
// workspace that already has another live agent. It encodes the orchestrator
// contract: sub-agents never commit (lint-staged backup stashes are global,
// and concurrent git index writes race), and they stay inside their assigned
// file zones. Skipped when a reviewed plan is provided (the plan already
// carries the discipline).
const sharedWorktreePreamble = "You are sharing this worktree with other active agents. " +
	"Do NOT run git commit/add — the orchestrator owns all commits. " +
	"Stay within the file zones assigned in your subject.\n\n"

// RunAgent handles POST /workspaces/{id}/run — spawn an additional agent
// session inside an existing workspace WITHOUT creating a worktree. Multiple
// concurrent sessions per workspace are allowed (that is the feature). It
// never touches the workspace row (state, ports, .env.local).
func RunAgent(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		var req pigtail.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.Intent == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "intent is required", nil)
			return
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

		// The worktree must still exist on disk; running an agent in a vanished
		// path would fail confusingly. Point the operator at gc + adopt.
		if ws.Path == "" {
			resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError,
				"workspace has no path — run pigtail gc + adopt", map[string]interface{}{"id": id})
			return
		}
		if _, serr := os.Stat(ws.Path); serr != nil {
			resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError,
				"workspace path is gone — run pigtail gc + adopt",
				map[string]interface{}{"id": id, "path": ws.Path})
			return
		}

		proj, err := svc.Store.GetProject(ws.ProjectID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrProjectNotFound, "unknown project for workspace",
					map[string]interface{}{"project_id": ws.ProjectID})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		// The profile and the launch margin guard before anything is written.
		lp, ok := resolveLaunchProfile(w, r, resp, proj, req.ProfileID)
		if !ok {
			return
		}
		if err := guardLaunch(svc, &lp, req.OverMargin); err != nil {
			refuseOverMargin(w, r, resp, err)
			return
		}

		// NO anti-collision guard here: concurrent agents are the whole point.
		// We never touch ws.State / ws.Ports / .env.local.

		intent := req.Intent

		// If another agent is already live AND no reviewed plan is provided,
		// inject the shared-worktree no-commit discipline into the intent.
		if req.PlanContent == "" {
			sessions, lerr := svc.Store.ListAgentSessionsByWorkspace(ws.ID)
			if lerr != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "list sessions: "+lerr.Error(), nil)
				return
			}
			for _, s := range sessions {
				if isLiveAgentState(s.State) {
					intent = sharedWorktreePreamble + intent
					break
				}
			}
		}

		// --- write reviewed plan into the workspace -------------------
		var planPath string
		if req.PlanContent != "" {
			pp, werr := writePlanFile(ws.Path, req.PlanFilename, req.PlanContent)
			if werr != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "write plan: "+werr.Error(), nil)
				return
			}
			planPath = pp
		}

		sess, ok := spawnAgentInto(w, r, svc, resp, proj, ws, spawnParams{
			Intent:    intent,
			ProfileID: req.ProfileID,
			AgentKind: req.AgentKind,
			PlanFirst: resolveRunPlanFirst(&req),
			PlanPath:  planPath,
			Profile:   &lp,
		})
		if !ok {
			return
		}

		resp.JSON(w, r, http.StatusCreated, pigtail.RunResponse{
			WorkspaceID:    ws.ID,
			AgentSessionID: sess.ID,
			Margin:         lp.Margin,
		})
	}
}

// resolveRunPlanFirst mirrors resolvePlanFirst for RunRequest: an explicit
// PlanFirst wins; otherwise enable plan mode only when no reviewed plan was
// provided.
func resolveRunPlanFirst(req *pigtail.RunRequest) bool {
	if req.PlanFirst != nil {
		return *req.PlanFirst
	}
	return req.PlanContent == ""
}
