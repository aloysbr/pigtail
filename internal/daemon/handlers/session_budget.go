package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sessionSamplesDefaultWindow is how far back GET /session-budget/samples
// reads when no since is given.
const sessionSamplesDefaultWindow = 24 * time.Hour

// RegisterSessionBudgetRoutes mounts every /session-budget* route behind
// scopeMW (a sample row names a session's repo, cwd and subject). scopeMW is
// unexported, so the routes are mounted by this one group, called from
// buildRouter.
func RegisterSessionBudgetRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Group(func(r chi.Router) {
		r.Use(scopeMW(resp))
		r.Get("/session-budget/samples", GetSessionSamples(svc, resp))
		r.Get("/session-budget", GetSessionBudget(svc, resp))
		r.Post("/session-budget/leases", PostSessionLease(svc, resp))
		r.Post("/session-budget/leases/release", PostSessionLeaseRelease(svc, resp))
	})
}

// GetSessionSamples handles GET /session-budget/samples?since=<RFC3339> — the
// sampler's records since then, oldest first. A non-master caller gets the
// rows of its own scope only; the aggregate counts (live, busy) and the memory
// levels stay, they are the machine's. A session with no registered scope is
// the master's alone (D17).
func GetSessionSamples(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := time.Now().Add(-sessionSamplesDefaultWindow)
		if v := r.URL.Query().Get("since"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "since: want RFC3339", nil)
				return
			}
			since = t
		}
		out := pigtail.SessionSamples{Samples: []pigtail.SessionSample{}}
		if svc.SessionSampler != nil {
			samples, err := svc.SessionSampler.History().Read(since)
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			caller := callerOf(r).scope
			for _, s := range samples {
				s.Sessions = visibleSampleRows(caller, s.Sessions)
				out.Samples = append(out.Samples, s)
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// visibleSampleRows keeps the rows caller may read.
func visibleSampleRows(caller scope.Scope, rows []pigtail.SessionSampleRow) []pigtail.SessionSampleRow {
	if scope.SeesAll(caller) {
		return rows
	}
	kept := make([]pigtail.SessionSampleRow, 0, len(rows))
	for _, row := range rows {
		target := scope.Master() // no scope registered: the master's alone
		if row.Scope != "" {
			target = scope.Scope(row.Scope)
		}
		if scope.CanRead(caller, target) {
			kept = append(kept, row)
		}
	}
	return kept
}

// GetSessionBudget handles GET /session-budget: the gate's state for an agent
// ask (Controller.Snapshot), aggregate counts only — no session row, so
// nothing of another scope to drop and no title to show (D23).
func GetSessionBudget(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := pigtail.SessionBudgetState{Gate: "open"}
		if svc.SessionBudget != nil {
			s := svc.SessionBudget.Snapshot()
			out.Enabled, out.Busy, out.Budget = s.Enabled, s.Busy, s.Budget
			if s.Enabled && !s.Gate.Admitted {
				out.Gate, out.Reason = "closed", s.Gate.Reason
			}
			out.DeferredWakes = pigtail.DeferredWakes{
				Count: s.DeferredWakes, OldestAgeSeconds: int(s.DeferredOldestAge / time.Second),
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// PostSessionLease handles POST /session-budget/leases: the one « admit
// now? » of a spawn (kind agent, no_wait required) or of the user's launch (kind
// human, admitted always, over_budget read by the gate). One JSON answer, no
// stream, no queue; a refusal stores nothing. The decision is Decide's.
func PostSessionLease(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SessionLeaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON body", nil)
			return
		}
		kind := sessbudget.Kind(req.Kind)
		switch kind {
		case sessbudget.KindHuman:
		case sessbudget.KindAgent:
			if !req.NoWait {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "no_wait required for kind agent", nil)
				return
			}
		default:
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "kind: want human or agent", nil)
			return
		}
		if svc.SessionBudget == nil { // no controller: fail-open
			out := pigtail.SessionLeaseAnswer{Admitted: true}
			if kind == sessbudget.KindHuman {
				out.OverBudget = new(bool)
			}
			resp.JSON(w, r, http.StatusOK, out)
			return
		}
		a := svc.SessionBudget.TryAdmitAs(kind, string(callerOf(r).scope))
		out := pigtail.SessionLeaseAnswer{Admitted: a.Admitted, Busy: a.Busy, Budget: a.Budget, Reason: a.Reason, LeaseID: a.LeaseID}
		if kind == sessbudget.KindHuman {
			over := a.OverBudget
			out.OverBudget = &over
			out.LeaseID = ""
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// PostSessionLeaseRelease handles POST /session-budget/leases/release: gives
// an admitted lease back (pre-launch failure: dropped, the gap restored when
// no admission came after it), or with launched:true reports the launch (the
// lease counts until the next sample). Idempotent: 204 for an unknown or
// released id, and for another scope's lease, which it leaves untouched.
func PostSessionLeaseRelease(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SessionLeaseRelease
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON body", nil)
			return
		}
		c := svc.SessionBudget
		if c != nil && req.LeaseID != "" {
			owner, ok := c.LeaseOwner(req.LeaseID)
			if ok && (owner == "" || scope.CanRead(callerOf(r).scope, scope.Scope(owner))) {
				if req.Launched {
					c.MarkLaunched(req.LeaseID)
				} else {
					c.ReleaseLease(req.LeaseID)
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
