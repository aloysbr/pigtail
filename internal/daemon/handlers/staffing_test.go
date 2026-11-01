package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The /staffing routes (sub_x000000000000004, Task 6): the scheduler's state,
// its settings (master only, with the cross-scope opt-in; launch never from a
// Claude session) and its decision journal, another scope's fields masked.

// fakeStaffing is a StaffingService answering from memory.
type fakeStaffing struct {
	state   pigtail.StaffingState
	ds      []pigtail.StaffingDecision
	updates []pigtail.StaffingSettings
	since   time.Time
	limit   int
}

func (f *fakeStaffing) State() (pigtail.StaffingState, error) { return f.state, nil }

func (f *fakeStaffing) Update(in pigtail.StaffingSettings) (pigtail.StaffingState, error) {
	f.updates = append(f.updates, in)
	if in.Mode != "" {
		f.state.Mode = in.Mode
	}
	if in.Ceiling != nil {
		f.state.Ceiling = *in.Ceiling
	}
	return f.state, nil
}

func (f *fakeStaffing) Decisions(since time.Time, limit int) ([]pigtail.StaffingDecision, error) {
	f.since, f.limit = since, limit
	return f.ds, nil
}

func staffingRouter(t *testing.T, f StaffingService) *chi.Mux {
	t.Helper()
	_, _, svc := scopedFixtureSvc(t)
	if f != nil {
		svc.Staffing = f
	}
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, detailResponder{})
	return r
}

func decodeStaffing[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body, err)
	}
	return out
}

func TestGetStaffingRedactsOtherScope(t *testing.T) {
	t.Parallel()
	master, def := string(scope.Master()), string(scope.Default())
	f := &fakeStaffing{state: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3, PlacesUsed: 2,
		Places: []pigtail.StaffingPlace{
			{SessionID: "s-work", SubjectID: "sub_t", Scope: def, Label: "t"},
			{SessionID: "s-perso", SubjectID: "sub_p", Scope: master, Label: "p"},
		},
		Queues: map[string][]pigtail.StaffingCandidate{
			def:    {{SubjectID: "sub_t2", Scope: def}},
			master: {{SubjectID: "sub_p2", Scope: master}},
		}}}
	r := staffingRouter(t, f)

	got := decodeStaffing[pigtail.StaffingState](t, doAs(t, r, work, "GET", "/staffing", nil))
	if got.PlacesUsed != 2 || len(got.Places) != 1 || got.Places[0].SessionID != "s-work" {
		t.Fatalf("work caller: %+v", got)
	}
	if _, ok := got.Queues[master]; ok || len(got.Queues[def]) != 1 {
		t.Fatalf("work caller queues: %+v", got.Queues)
	}
	got = decodeStaffing[pigtail.StaffingState](t, doAs(t, r, perso, "GET", "/staffing", nil))
	if got.PlacesUsed != 2 || len(got.Places) != 2 || len(got.Queues) != 2 {
		t.Fatalf("master caller: %+v", got)
	}
}

// markStrings writes LEAK-<path> into every settable string field of v but
// those in keep; a field named Scope gets sc (the scope that triggers the
// masking). Structs, pointers and slices are walked: a field added later is
// marked without touching the test.
func markStrings(v reflect.Value, path string, keep map[string]bool, sc string) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			markStrings(v.Elem(), path, keep, sc)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			markStrings(v.Index(i), path, keep, sc)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f, sf := v.Field(i), v.Type().Field(i)
			if !f.CanSet() || keep[sf.Name] {
				continue
			}
			p := path + "." + sf.Name
			switch {
			case f.Kind() == reflect.String && sf.Name == "Scope":
				f.SetString(sc)
			case f.Kind() == reflect.String:
				f.SetString("LEAK-" + p)
			default:
				markStrings(f, p, keep, sc)
			}
		}
	}
}

func TestStaffingRedactionLeaksNothing(t *testing.T) {
	t.Parallel()
	master := string(scope.Master())
	keep := map[string]bool{
		// StaffingState: visible by design
		"Mode": true, "Launcher": true, "SettingsError": true,
		// StaffingDecision: visible by design
		"Outcome": true, "Gate": true,
	}
	cand := func() pigtail.StaffingCandidate {
		c := pigtail.StaffingCandidate{}
		markStrings(reflect.ValueOf(&c), "Candidate", keep, master)
		return c
	}
	dec := func(outcome, gate string) pigtail.StaffingDecision {
		d := pigtail.StaffingDecision{TS: time.Now(), Mode: pigtail.StaffingLaunch, Outcome: outcome, Gate: gate,
			Skipped: []pigtail.StaffingCandidate{{}, {}}}
		markStrings(reflect.ValueOf(&d), "Decision."+outcome, keep, master)
		return d
	}
	last := dec(pigtail.StaffingOutcomeLaunchStarted, "")
	st := pigtail.StaffingState{Mode: pigtail.StaffingLaunch, Ceiling: 3, PlacesUsed: 1, Launcher: pigtail.StaffingLauncherScheduler,
		Places: []pigtail.StaffingPlace{{}}, Last: &last,
		Queues: map[string][]pigtail.StaffingCandidate{master: {cand()}, "LEAK-Queues-key": {cand()}}}
	markStrings(reflect.ValueOf(&st), "State", keep, master)
	f := &fakeStaffing{state: st, ds: []pigtail.StaffingDecision{
		dec(pigtail.StaffingOutcomeLaunchStarted, ""),
		dec(pigtail.StaffingOutcomeObservedLaunch, ""),
		dec(pigtail.StaffingOutcomeIdle, pigtail.StaffingGateOverMargin),
	}}
	r := staffingRouter(t, f)
	for _, path := range []string{"/staffing", "/staffing/decisions"} {
		rec := doAs(t, r, work, "GET", path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if strings.Contains(body, "LEAK-") || strings.Contains(body, `"`+master+`"`) {
			t.Errorf("%s leaks to work:\n%s", path, body)
		}
		// The master reads everything back, so the markers did reach the handler.
		if b := doAs(t, r, perso, "GET", path, nil).Body.String(); !strings.Contains(b, "LEAK-") {
			t.Errorf("%s: master body carries no marker — the probe is not looking: %s", path, b)
		}
	}
}

func TestPostStaffingMasterOnly(t *testing.T) {
	t.Parallel()
	f := &fakeStaffing{state: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3}}
	r := staffingRouter(t, f)
	body := map[string]any{"mode": "off"}
	for _, c := range []caller{work, workCross, none, perso} {
		rec := doAs(t, r, c, "POST", "/staffing", body)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "SCOPE_MISMATCH") ||
			!strings.Contains(rec.Body.String(), "--cross-scope") {
			t.Fatalf("%s: %d %s", c, rec.Code, rec.Body)
		}
	}
	if len(f.updates) != 0 {
		t.Fatalf("refused callers changed the settings: %+v", f.updates)
	}
	rec := doAs(t, r, persoCross, "POST", "/staffing", body)
	if rec.Code != http.StatusOK || decodeStaffing[pigtail.StaffingState](t, rec).Mode != pigtail.StaffingOff {
		t.Fatalf("master + opt-in: %d %s", rec.Code, rec.Body)
	}
}

func TestPostStaffingInvalid(t *testing.T) {
	t.Parallel()
	f := &fakeStaffing{}
	r := staffingRouter(t, f)
	for _, body := range []any{map[string]any{"mode": "x"}, map[string]any{"ceiling": 0}, map[string]any{"ceiling": 21}, "nope"} {
		rec := doAs(t, r, persoCross, "POST", "/staffing", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
			t.Fatalf("%v: %d %s", body, rec.Code, rec.Body)
		}
	}
	if len(f.updates) != 0 {
		t.Fatalf("invalid settings reached the scheduler: %+v", f.updates)
	}
}

func TestPostStaffingFrozen(t *testing.T) {
	t.Parallel()
	_, _, svc := scopedFixtureSvc(t)
	dir := t.TempDir()
	_ = maintenance.Save(dir, frozen())
	svc.Maintenance = maintenance.NewWatch(dir)
	f := &fakeStaffing{}
	svc.Staffing = f
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, detailResponder{})
	rec := doAs(t, r, persoCross, "POST", "/staffing", map[string]any{"mode": "off"})
	if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "MAINTENANCE_FROZEN") {
		t.Fatalf("frozen: %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, persoCross, "GET", "/staffing", nil); rec.Code != http.StatusOK {
		t.Fatalf("a read must pass the freeze: %d %s", rec.Code, rec.Body)
	}
	if len(f.updates) != 0 {
		t.Fatal("a frozen write reached the scheduler")
	}
}

func TestPostStaffingLaunchRefusedFromSession(t *testing.T) {
	t.Parallel()
	f := &fakeStaffing{state: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3}}
	r := staffingRouter(t, f)
	post := func(mode, session string) *httptest.ResponseRecorder {
		buf := strings.NewReader(`{"mode":"` + mode + `"}`)
		req := httptest.NewRequest("POST", "/staffing", buf)
		req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
		req.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
		if session != "" {
			req.Header.Set(pigtail.HeaderSession, session)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	rec := post("launch", "abc")
	want := "pigtail --scope " + string(scope.Master()) + " staffing mode launch --cross-scope"
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "UNAUTHORIZED") ||
		!strings.Contains(rec.Body.String(), want) {
		t.Fatalf("launch from a session: %d %s", rec.Code, rec.Body)
	}
	if len(f.updates) != 0 || f.state.Mode != pigtail.StaffingAdvise {
		t.Fatalf("settings changed: %+v %s", f.updates, f.state.Mode)
	}
	for _, m := range []string{"off", "advise"} {
		if rec := post(m, "abc"); rec.Code != http.StatusOK {
			t.Fatalf("%s from a session: %d %s", m, rec.Code, rec.Body)
		}
	}
	if rec := post("launch", ""); rec.Code != http.StatusOK || f.state.Mode != pigtail.StaffingLaunch {
		t.Fatalf("launch from a terminal: %d %s", rec.Code, rec.Body)
	}
}

func TestGetStaffingDecisionsSinceLimit(t *testing.T) {
	t.Parallel()
	f := &fakeStaffing{ds: []pigtail.StaffingDecision{{Outcome: pigtail.StaffingOutcomeIdle, Gate: pigtail.StaffingGateNoReadySubject}}}
	r := staffingRouter(t, f)
	before := time.Now()
	rec := doAs(t, r, perso, "GET", "/staffing/decisions", nil)
	if rec.Code != http.StatusOK || len(decodeStaffing[[]pigtail.StaffingDecision](t, rec)) != 1 {
		t.Fatalf("default: %d %s", rec.Code, rec.Body)
	}
	if d := before.Sub(f.since); d < 24*time.Hour-time.Minute || d > 24*time.Hour+time.Minute || f.limit != 200 {
		t.Fatalf("defaults: since %s ago, limit %d", d, f.limit)
	}
	since := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	rec = doAs(t, r, perso, "GET", "/staffing/decisions?since="+since.Format(time.RFC3339)+"&limit=7", nil)
	if rec.Code != http.StatusOK || !f.since.Equal(since) || f.limit != 7 {
		t.Fatalf("explicit: %d %s since=%s limit=%d", rec.Code, rec.Body, f.since, f.limit)
	}
	for _, q := range []string{"?since=yesterday", "?limit=x", "?limit=-1"} {
		if rec := doAs(t, r, perso, "GET", "/staffing/decisions"+q, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	// An empty journal is an empty list, never null.
	f.ds = nil
	if rec := doAs(t, r, perso, "GET", "/staffing/decisions", nil); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty journal: %s", rec.Body)
	}
}

func TestStaffingOffAnswers503(t *testing.T) {
	t.Parallel()
	r := staffingRouter(t, nil)
	for _, c := range []struct{ method, path string }{{"GET", "/staffing"}, {"POST", "/staffing"}, {"GET", "/staffing/decisions"}} {
		rec := doAs(t, r, persoCross, c.method, c.path, map[string]any{"mode": "off"})
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "staffing off") {
			t.Fatalf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

func TestSubjectWritesKickStaffing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		c      caller
		method string
		path   string
		body   any
		setup  func(t *testing.T, r http.Handler) any // returns the body when it needs a seeded id
		want   int
	}{
		{name: "rank", c: perso, method: "POST", path: "/subjects/sub_p", body: map[string]any{"title": "x", "status": "understanding", "rank": 4}, want: 1},
		{name: "ranks", c: perso, method: "POST", path: "/subjects/ranks",
			body: map[string]any{"changes": []map[string]any{{"id": "sub_p", "from": 1, "to": 4}}}, want: 1},
		{name: "create", c: perso, method: "POST", path: "/subjects", body: map[string]any{"theme_id": "thm_p", "title": "new"}, want: 1},
		{name: "promote", c: perso, method: "POST", path: "/captures/promote",
			setup: func(t *testing.T, r http.Handler) any {
				rec := doAs(t, r, perso, "POST", "/captures", map[string]any{"theme_id": "thm_p", "text": "an idea"})
				if rec.Code != http.StatusCreated {
					t.Fatalf("seed capture: %d %s", rec.Code, rec.Body)
				}
				c := decodeStaffing[pigtail.Capture](t, rec)
				return map[string]any{"capture_ids": []string{c.ID}, "title": "promoted"}
			}, want: 1},
		{name: "phase", c: perso, method: "POST", path: "/phases/phs_p", body: map[string]any{"state": "active"}, want: 1},
		{name: "refused", c: work, method: "POST", path: "/subjects/sub_p", body: map[string]any{"title": "x", "status": "understanding"}, want: 0},
		{name: "refused ranks", c: work, method: "POST", path: "/subjects/ranks",
			body: map[string]any{"changes": []map[string]any{{"id": "sub_p", "from": 1, "to": 4}}}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, svc := scopedFixtureSvc(t)
			n := 0
			svc.SubjectsChanged = func() { n++ }
			r := chi.NewRouter()
			RegisterSubjectRoutes(r, svc, detailResponder{})
			body := tc.body
			if tc.setup != nil {
				body = tc.setup(t, r)
			}
			n = 0
			rec := doAs(t, r, tc.c, tc.method, tc.path, body)
			if tc.want > 0 && rec.Code >= 300 {
				t.Fatalf("write: %d %s", rec.Code, rec.Body)
			}
			if n != tc.want {
				t.Fatalf("SubjectsChanged called %d times, want %d (status %d %s)", n, tc.want, rec.Code, rec.Body)
			}
		})
	}
}

// StaffingMargin peeks at the launch margin for the scheduler: a refusal or
// an unmeasured margin closes the gate; nothing is traced.
func TestStaffingMarginPeek(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(true)
	ok, reason := StaffingMargin(rig.svc)("perso")
	if ok || !strings.Contains(reason, "over the margin") {
		t.Fatalf("saturated: %v %q", ok, reason)
	}
	rig.saturate(false)
	if ok, reason := StaffingMargin(rig.svc)("perso"); !ok || reason != "" {
		t.Fatalf("a free account: %v %q", ok, reason)
	}

	base := rig.svc.MarginDeps
	for _, tc := range []struct {
		name  string
		scope string
		prep  func()
		undo  func()
	}{
		{name: "no cell table", scope: "perso",
			prep: func() {
				rig.svc.MarginDeps = func() launchmargin.Deps { d := base(); d.Cells = (*claudeprofile.Table)(nil); return d }
			},
			undo: func() { rig.svc.MarginDeps = base }},
		{name: "rotation off", scope: "perso",
			prep: func() { rig.env[launchmargin.EnvRotation] = "off" },
			undo: func() { delete(rig.env, launchmargin.EnvRotation) }},
		{name: "no candidate", scope: "scope-with-no-cell", prep: func() {}, undo: func() {}},
	} {
		tc.prep()
		ok, reason := StaffingMargin(rig.svc)(tc.scope)
		tc.undo()
		if ok || !strings.HasPrefix(reason, "unmeasured") {
			t.Errorf("%s: %v %q, want a closed gate, unmeasured", tc.name, ok, reason)
		}
	}
	es, rows := rig.marginRows(t)
	if len(es) != 0 || len(rows) != 0 {
		t.Fatalf("a peek traced: spawns %+v journal %+v", es, rows)
	}
}

// A budget refusal is masked like a margin one, in the journal and in the
// queues of GET /staffing.
func TestRedactOverBudgetForNonMaster(t *testing.T) {
	t.Parallel()
	const why = "every account of scope globex is out of its weekly token budget; first release at Mon 15:04"
	sc := string(work.scope)
	cand := pigtail.StaffingCandidate{SubjectID: "sub_x", Scope: sc, Skip: pigtail.StaffingSkipOverBudget, Detail: why}
	dec := pigtail.StaffingDecision{Outcome: pigtail.StaffingOutcomeIdle, Gate: pigtail.StaffingGateOverBudget, Reason: why,
		Skipped: []pigtail.StaffingCandidate{cand}}
	f := &fakeStaffing{state: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3, Last: &dec,
		Queues: map[string][]pigtail.StaffingCandidate{sc: {cand}}}, ds: []pigtail.StaffingDecision{dec}}
	r := staffingRouter(t, f)
	for _, path := range []string{"/staffing", "/staffing/decisions"} {
		rec := doAs(t, r, work, "GET", path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "weekly token budget") {
			t.Errorf("%s: the budget text reached a non-master:\n%s", path, rec.Body)
		}
		if b := doAs(t, r, perso, "GET", path, nil).Body.String(); !strings.Contains(b, "weekly token budget") {
			t.Errorf("%s: the master lost the text: %s", path, b)
		}
	}
}
