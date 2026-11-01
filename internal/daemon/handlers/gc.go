package handlers

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// GCWorkspaces handles POST /workspaces/gc.
//
// It removes workspace rows whose worktree directory no longer exists on
// disk (orphans left behind by a manual `git worktree move`, an out-of-band
// deletion, etc.). Workspaces with a live agent session are never collected,
// defensively, even though a dead path should not carry a live agent.
//
// With dry_run set, no rows are deleted: the response lists the candidates.
func GCWorkspaces(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.GCRequest
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
				return
			}
		}

		all, err := svc.Store.ListWorkspaces("")
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		var removed []string
		for i := range all {
			ws := &all[i]
			if _, statErr := os.Stat(ws.Path); statErr == nil {
				continue // path still exists — keep it
			} else if !os.IsNotExist(statErr) {
				continue // unreadable for some other reason — be conservative
			}

			// Defensive: never collect a workspace that still has a live agent.
			sessions, lerr := svc.Store.ListAgentSessionsByWorkspace(ws.ID)
			if lerr != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "list sessions: "+lerr.Error(), nil)
				return
			}
			live := false
			for _, s := range sessions {
				if isLiveAgentState(s.State) {
					live = true
					break
				}
			}
			if live {
				continue
			}

			if req.DryRun {
				removed = append(removed, ws.ID)
				continue
			}
			if err := svc.Store.DeleteWorkspace(ws.ID); err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "delete workspace: "+err.Error(), nil)
				return
			}
			svc.Bus.Publish(pigtail.Event{
				Type: pigtail.EventWorkspaceDeleted,
				Data: map[string]interface{}{"workspace_id": ws.ID, "reason": "gc"},
			})
			removed = append(removed, ws.ID)
		}

		resp.JSON(w, r, http.StatusOK, pigtail.GCResponse{
			DeletedIDs: removed,
			DryRun:     req.DryRun,
		})
	}
}
