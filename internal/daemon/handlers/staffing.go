package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/staffing"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The staffing scheduler's routes (sub_x000000000000004): its state, its
// settings and its decision journal. Places, queues and decisions name
// subjects of every scope: a non-master caller sees its own scope's only
// (redactStaffing). The settings are global: only the master changes them,
// with the cross-scope opt-in, and never turns launch on from a Claude session.

// StaffingService is the scheduler, as the routes see it
// (*staffing.Scheduler).
type StaffingService interface {
	State() (pigtail.StaffingState, error)
	Update(pigtail.StaffingSettings) (pigtail.StaffingState, error)
	Decisions(since time.Time, limit int) ([]pigtail.StaffingDecision, error)
}

const (
	// staffingDecisionsWindow and staffingDecisionsLimit are the defaults of
	// GET /staffing/decisions.
	staffingDecisionsWindow = 24 * time.Hour
	staffingDecisionsLimit  = 200
)

// Registered with the subject routes, behind scopeMW: the settings write is
// behind the freeze (fz), the reads are not.
func registerStaffingRoutes(r chi.Router, fz chi.Router, svc *Services, resp Responder) {
	r.Get("/staffing", GetStaffing(svc, resp))
	fz.Post("/staffing", PostStaffing(svc, resp))
	r.Get("/staffing/decisions", GetStaffingDecisions(svc, resp))
}

func staffingOff(w http.ResponseWriter, r *http.Request, resp Responder) {
	resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrInternal, "staffing off", nil)
}

// subjectsChanged asks the scheduler for a tour after a subject write (a
// creation, a rank, a status, a phase): those writes publish no event.
func (svc *Services) subjectsChanged() {
	if svc.SubjectsChanged != nil {
		svc.SubjectsChanged()
	}
}

// GetStaffing handles GET /staffing.
func GetStaffing(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.Staffing == nil {
			staffingOff(w, r, resp)
			return
		}
		st, err := svc.Staffing.State()
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		redactStaffing(callerOf(r).scope, &st, nil) // st is a copy; its slices and maps are rebuilt, never written
		resp.JSON(w, r, http.StatusOK, st)
	}
}

// launchFromSessionRefusal is the 403 of mode launch asked from a Claude
// session (its CLI sends X-Pigtail-Session): the exact command to type.
func launchFromSessionRefusal(human string) string {
	return "only " + human + " turns launch on — run it from a terminal outside any Claude session: pigtail --scope " +
		string(scope.Master()) + " staffing mode launch --cross-scope"
}

// PostStaffing handles POST /staffing: master only, with the cross-scope
// opt-in (a global setting changes every scope's behaviour).
func PostStaffing(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.Staffing == nil {
			staffingOff(w, r, resp)
			return
		}
		c := callerOf(r)
		if !scope.SeesAll(c.scope) || !c.cross {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch,
				"only the master cell changes the staffing settings (--cross-scope)", nil)
			return
		}
		var in pigtail.StaffingSettings
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		// The scheduler's own rule (Settings.Validate), on the fields asked;
		// an absent field keeps its value and is checked as a valid one.
		probe := staffing.Settings{Mode: in.Mode, Ceiling: 1}
		if probe.Mode == "" {
			probe.Mode = staffing.DefaultMode
		}
		if in.Ceiling != nil {
			probe.Ceiling = *in.Ceiling
		}
		if err := probe.Validate(); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		// A guard against an agent's mistake, not a security boundary: the
		// CLI of an agent session sends its session id (as AnswerWait).
		if in.Mode == pigtail.StaffingLaunch && r.Header.Get(pigtail.HeaderSession) != "" {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrUnauthorized, launchFromSessionRefusal(svc.humanName()), nil)
			return
		}
		st, err := svc.Staffing.Update(in)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		redactStaffing(c.scope, &st, nil)
		resp.JSON(w, r, http.StatusOK, st)
	}
}

// GetStaffingDecisions handles GET /staffing/decisions?since=&limit=
// (defaults: the last 24 h, 200 lines).
func GetStaffingDecisions(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.Staffing == nil {
			staffingOff(w, r, resp)
			return
		}
		q := r.URL.Query()
		since := time.Now().Add(-staffingDecisionsWindow)
		if v := q.Get("since"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "since: want an RFC 3339 time", nil)
				return
			}
			since = t
		}
		limit := staffingDecisionsLimit
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "limit: want a positive integer", nil)
				return
			}
			limit = n
		}
		ds, err := svc.Staffing.Decisions(since, limit)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		out := make([]pigtail.StaffingDecision, len(ds)) // masked in a copy, never the service's own lines
		copy(out, ds)
		redactStaffing(callerOf(r).scope, nil, out)
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// staffingVisible: a place, a candidate or a queue of scope s is shown to
// caller. An empty or unknown scope is not (fail-closed).
func staffingVisible(caller scope.Scope, s string) bool {
	sc, err := scope.Parse(s)
	return err == nil && scope.CanRead(caller, sc)
}

// redactStaffing masks, for a non-master caller, what names another scope:
// in the state (st, may be nil) its places (still counted in places_used),
// its queues and the last decision; in the decisions (ds, in place) each one
// of another scope. Nothing is masked for the master.
func redactStaffing(caller scope.Scope, st *pigtail.StaffingState, ds []pigtail.StaffingDecision) {
	if scope.SeesAll(caller) {
		return
	}
	if st != nil {
		places := make([]pigtail.StaffingPlace, 0, len(st.Places))
		for _, p := range st.Places {
			if staffingVisible(caller, p.Scope) {
				places = append(places, p)
			}
		}
		st.Places = places
		queues := make(map[string][]pigtail.StaffingCandidate, len(st.Queues))
		for k, q := range st.Queues {
			if staffingVisible(caller, k) {
				queues[k] = redactCandidates(caller, q, false)
			}
		}
		st.Queues = queues
		if st.Last != nil {
			d := redactDecision(caller, *st.Last)
			st.Last = &d
		}
	}
	for i := range ds {
		ds[i] = redactDecision(caller, ds[i])
	}
}

// redactCandidates keeps the caller's own candidates; an over-margin or
// over-budget text (it may name accounts and cells) is dropped, as clearAll drops every one.
func redactCandidates(caller scope.Scope, cs []pigtail.StaffingCandidate, clearAll bool) []pigtail.StaffingCandidate {
	if cs == nil {
		return nil
	}
	out := make([]pigtail.StaffingCandidate, 0, len(cs))
	for _, c := range cs {
		if !staffingVisible(caller, c.Scope) {
			continue
		}
		if clearAll || c.Skip == pigtail.StaffingSkipOverMargin || c.Skip == pigtail.StaffingSkipOverBudget {
			c.Detail = ""
		}
		out = append(out, c)
	}
	return out
}

// redactDecision masks one decision for a non-master caller. A decision of
// another scope — or naming a subject or a launch with no scope — is rebuilt
// from the fields visible by design only, so a field added later stays
// masked; its reason becomes the fixed label of its outcome and gate.
func redactDecision(caller scope.Scope, d pigtail.StaffingDecision) pigtail.StaffingDecision {
	foreign := d.Scope != "" && !staffingVisible(caller, d.Scope) ||
		d.Scope == "" && (d.SubjectID != "" || d.LaunchID != "")
	out := d
	if foreign {
		out = pigtail.StaffingDecision{TS: d.TS, LastAt: d.LastAt, Repeats: d.Repeats, Mode: d.Mode,
			PlacesUsed: d.PlacesUsed, Ceiling: d.Ceiling, Outcome: d.Outcome, Gate: d.Gate,
			Reason: staffingLabel(d.Outcome, d.Gate)}
	}
	refused := d.Gate == pigtail.StaffingGateOverMargin || d.Gate == pigtail.StaffingGateOverBudget
	out.Skipped = redactCandidates(caller, d.Skipped, refused)
	if refused {
		out.Reason = ""
	}
	return out
}

// staffingLabel is the fixed text of an outcome and its gate.
func staffingLabel(outcome, gate string) string {
	s := strings.ReplaceAll(outcome, "_", " ")
	if gate != "" {
		s += " (" + strings.ReplaceAll(gate, "_", " ") + ")"
	}
	return s
}
