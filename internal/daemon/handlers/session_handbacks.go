package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Turn ends by skill (D11, D12): the hooks record a session's last skill and
// each turn end; the weekly review reads the count of turns handed
// back without a declared wait, by skill.

// maxTurnIDBytes bounds a turn id (the transcript's promptId, a UUID today).
const maxTurnIDBytes = 128

// handbacksDefaultWindow is GET /session-handbacks' window without since.
const handbacksDefaultWindow = 7 * 24 * time.Hour

// SetSessionSkill handles POST /peers/{session}/skill {skill}: the skill the
// session just invoked becomes its last one. 204.
func (rl *Relay) SetSessionSkill(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SessionSkillRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		p, ok := rl.actingAs(callerOf(r).scope, chi.URLParam(r, "session"))
		if !ok {
			unknownSession(w, r, resp)
			return
		}
		if !waittype.ValidSkillName(req.Skill) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "skill must be a skill name (letters, digits, : _ . -, at most 100)", nil)
			return
		}
		if err := rl.svc.Store.SetSessionSkill(p.SessionID, req.Skill, rl.cfg.Now()); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// PostSessionTurn handles POST /peers/{session}/turn {turn_id, turn_start,
// event}: a Stop or StopFailure of that turn, recorded in the session's
// registered scope. 204.
func (rl *Relay) PostSessionTurn(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SessionTurnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		p, ok := rl.actingAs(callerOf(r).scope, chi.URLParam(r, "session"))
		if !ok {
			unknownSession(w, r, resp)
			return
		}
		if req.Event != pigtail.TurnEventStop && req.Event != pigtail.TurnEventFailure {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "event must be stop or failure", nil)
			return
		}
		if req.TurnID == "" || len(req.TurnID) > maxTurnIDBytes || strings.Contains(req.TurnID, "/") {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "turn_id must be 1 to 128 bytes, without /", nil)
			return
		}
		if req.TurnStart.IsZero() {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "turn_start is required", nil)
			return
		}
		if err := rl.svc.Store.RecordTurnEnd(p.SessionID, string(sessionScope(*p)), req.TurnID, req.TurnStart, rl.cfg.Now(), req.Event); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if req.Event == pigtail.TurnEventStop {
			rl.inferWorking(*p, req) // after RecordTurnEnd: the turn stays counted undeclared (D12)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// inferWorking acts on an undeclared Stop (nothing declared since the turn
// began): a final message closing on a 🔄 line (req.Working) declares the
// working wait the agent forgot, so the sidebar shows "Background · <reason>"
// rather than "Your turn"; it is a declared wait, cleared like one (the
// next prompt, the next turn). Without that line, a working wait an earlier
// turn left is over ("your next turn clears it"), whatever the native
// status order the loop saw. A declared turn is left alone. Errors are
// logged: the turn end is recorded already.
func (rl *Relay) inferWorking(p pigtail.PeerSession, req pigtail.SessionTurnRequest) {
	// A session in Standby gets no inferred wait (sub_x000000000000041): a
	// declared working would hold its /compact. An unreadable Standby too —
	// never a wait posed on a failed read.
	if in, why := rl.inStandby(p.SessionID); in {
		if strings.HasPrefix(why, "standby unreadable") {
			rl.svc.logf("relay: turn end of %s: %s: no working inferred", p.SessionID, why)
		}
		return
	}
	declared, err := rl.svc.Store.DeclaredSince(p.SessionID, req.TurnStart)
	if err != nil || declared {
		if err != nil {
			rl.svc.logf("relay: turn end of %s: %v", p.SessionID, err)
		}
		return
	}
	if !req.Working {
		ws, err := rl.svc.Store.SessionWaits(p.SessionID)
		if err != nil {
			rl.svc.logf("relay: turn end of %s: %v", p.SessionID, err)
			return
		}
		for _, w := range ws {
			if w.Source == pigtail.WaitSourceDeclared && w.Type == string(waittype.Working) && w.Since.Before(req.TurnStart) {
				if err := rl.ClearWaits(p.SessionID, pigtail.WaitSourceDeclared); err != nil {
					rl.svc.logf("relay: turn end of %s: %v", p.SessionID, err)
				}
			}
		}
		return
	}
	reason := waittype.CleanReason(req.Reason)
	if relay.SecretShape(reason) != "" {
		reason = ""
	}
	wait := pigtail.SessionWait{Type: string(waittype.Working), Reason: reason, Source: pigtail.WaitSourceDeclared, Since: rl.cfg.Now()}
	if err := rl.recordWait(p, wait, nil); err != nil {
		rl.svc.logf("relay: turn end of %s: %v", p.SessionID, err)
	}
}

// SessionHandbacks handles GET /session-handbacks?since=<RFC 3339>: the
// turns handed back without a declared wait since then (default 7 days), by
// skill, over the scopes the caller may read.
func (rl *Relay) SessionHandbacks(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := rl.cfg.Now().Add(-handbacksDefaultWindow)
		if v := r.URL.Query().Get("since"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "since must be an RFC 3339 time", nil)
				return
			}
			since = t
		}
		view := callerOf(r).scope // a scope reads its own turns, the master every scope's
		if scope.SeesAll(view) {
			view = scope.Any
		}
		skills, err := rl.svc.Store.HandbacksBySkill(since, view)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, pigtail.SessionHandbacksResponse{Since: since.UTC(), Skills: skills})
	}
}
