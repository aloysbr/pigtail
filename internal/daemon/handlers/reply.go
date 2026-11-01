package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ReplyRequest is the body of POST /agents/:id/reply.
type ReplyRequest struct {
	Content string `json:"content"`
}

// ReplyAgent handles POST /agents/:id/reply.
//
// Resolves the AgentSession (by id), looks up its workspace to load
// session state, refuses if the session is not running, then forwards
// `content` to the agent backend (which types it into tmux).
func ReplyAgent(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req ReplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.Content == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "content is required", nil)
			return
		}

		// AgentSessions live attached to workspaces in the store. Walk
		// workspaces until we find ours. (Future: dedicated index.)
		sess, err := findAgentSession(svc.Store, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrAgentNotFound, "no such agent session", map[string]interface{}{"id": id})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if sess.State != pigtail.AgentStateRunning && sess.State != pigtail.AgentStatePausedInput {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
				"agent is not accepting input",
				map[string]interface{}{"state": string(sess.State)})
			return
		}
		if svc.Agent == nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "no agent runtime configured", nil)
			return
		}
		if err := svc.Agent.Reply(r.Context(), sess, req.Content); err != nil {
			var apiErr *pigtail.APIError
			if errors.As(err, &apiErr) {
				resp.Error(w, r, statusForCode(apiErr.Code), apiErr.Code, apiErr.Message, apiErr.Details)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		// Bump last_event_at so the triage row reflects the interaction.
		_ = svc.Store.UpdateAgentSessionState(sess.ID, sess.State)

		svc.Bus.Publish(pigtail.Event{
			Type:      pigtail.EventAgentResumed,
			Timestamp: time.Now().UTC(),
			Data: map[string]interface{}{
				"agent_session_id": sess.ID,
				"workspace_id":     sess.WorkspaceID,
				"via":              "reply",
			},
		})

		resp.JSON(w, r, http.StatusAccepted, map[string]interface{}{
			"agent_session_id": sess.ID,
		})
	}
}

// findAgentSession scans every workspace for the requested agent id.
// O(N workspaces) — fine for v1; revisit when N>>100.
func findAgentSession(st *store.Store, id string) (*pigtail.AgentSession, error) {
	workspaces, err := st.ListWorkspaces("")
	if err != nil {
		return nil, err
	}
	for _, ws := range workspaces {
		sessions, err := st.ListAgentSessionsByWorkspace(ws.ID)
		if err != nil {
			continue
		}
		for i := range sessions {
			if sessions[i].ID == id {
				return &sessions[i], nil
			}
		}
	}
	return nil, store.ErrNotFound
}
