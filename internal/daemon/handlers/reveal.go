package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// POST /peers/{session}/reveal (sub_x000000000000025, task 15, « Aller à la
// session »): bring the session's cmux tab to the front, on the user's click on a
// native dialog's card in the reader. It writes nothing in the store.
//
//   - a session the caller may not read (peerVisible, the scope of /peers)
//     answers 404 exactly as an unknown one, and cmux is never called;
//   - a call from an agent session (X-Pigtail-Session) is refused 403 before
//     anything: taking the user's screen is his gesture, never an agent's (the
//     same guard as POST /waits/{id}/answer);
//   - a session with no cmux_workspace_id answers 409; a workspace cmux no
//     longer has (tab closed) 409 too; cmux unreachable 503.
//
// This file is the only daemon code that references cmux.Reveal
// (TestRevealOnlyInRevealHandler, the fifth place pigtaild runs cmux:
// CLAUDE.md § cmux sidebar).

// reveal runs the seam when set (tests), else the real cmux.
func (rl *Relay) reveal(workspaceID string) error {
	if rl.cfg.Reveal != nil {
		return rl.cfg.Reveal(workspaceID)
	}
	return cmux.Reveal(workspaceID)
}

// RevealSession handles POST /peers/{session}/reveal.
func (rl *Relay) RevealSession(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(pigtail.HeaderSession) != "" {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrUnauthorized, "reveal a session from the web front or your terminal, not from an agent session", nil)
			return
		}
		id := chi.URLParam(r, "session")
		p, err := rl.svc.Store.GetPeer(id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if err != nil || p == nil || !peerVisible(callerOf(r).scope, *p) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such session", nil)
			return
		}
		if p.CmuxWorkspaceID == "" {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "session "+id+" has no cmux workspace", nil)
			return
		}
		switch err := rl.reveal(p.CmuxWorkspaceID); {
		case err == nil:
			resp.JSON(w, r, http.StatusOK, map[string]string{"session_id": id, "cmux_workspace_id": p.CmuxWorkspaceID})
		case errors.Is(err, cmux.ErrRevealNotOpen):
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "session "+id+": its cmux tab is not open", nil)
		case errors.Is(err, cmux.ErrInvalidArg):
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "session "+id+": "+err.Error(), nil)
		default:
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrCmuxUnreachable, err.Error(), nil)
		}
	}
}
