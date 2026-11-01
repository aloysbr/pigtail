package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget/sessbudgettest"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Rows of another scope are dropped for a non-master caller; the aggregate
// counts (live, busy) stay: they are the machine's, not a session's.
func TestSamplesRouteDropsOtherScopeRows(t *testing.T) {
	t.Parallel()
	h := sessbudget.NewHistory(filepath.Join(t.TempDir(), "s.jsonl"))
	now := time.Now().UTC()
	rows := []pigtail.SessionSampleRow{
		{SessionID: "s-master-a", Scope: string(scope.Master()), Status: "busy"},
		{SessionID: "s-default", Scope: string(scope.Default()), Status: "busy"},
		{SessionID: "s-none", Scope: "", Status: "idle"}, // no scope: the master sees it alone (D17)
	}
	if err := h.Append(sessbudget.Sample{TS: now, Live: 3, Busy: 2, Sessions: rows}); err != nil {
		t.Fatal(err)
	}
	smp := sessbudget.New(sessbudget.Deps{}, h, time.Second)
	r := chi.NewRouter()
	RegisterSessionBudgetRoutes(r, &Services{SessionSampler: smp}, testResponder{})

	get := func(callerScope string) pigtail.SessionSamples {
		req := httptest.NewRequest("GET", "/session-budget/samples?since="+now.Add(-time.Hour).Format(time.RFC3339), nil)
		if callerScope != "" {
			req.Header.Set(pigtail.HeaderScope, callerScope)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", callerScope, rec.Code, rec.Body.String())
		}
		return decodeSamples(t, rec)
	}

	master := get(string(scope.Master()))
	if len(master.Samples) != 1 || len(master.Samples[0].Sessions) != 3 {
		t.Fatalf("master sees every row: %+v", master)
	}
	def := get(string(scope.Default()))
	if len(def.Samples) != 1 {
		t.Fatalf("default caller: %+v", def)
	}
	got := def.Samples[0]
	if got.Live != 3 || got.Busy != 2 {
		t.Fatalf("aggregate counts must stay: %+v", got)
	}
	if string(scope.Default()) == string(scope.Master()) {
		t.Skip("default scope is the master in this table")
	}
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != "s-default" {
		t.Fatalf("non-master caller sees another scope's rows: %+v", got.Sessions)
	}
}

func TestSamplesRouteBadSince(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	RegisterSessionBudgetRoutes(r, &Services{}, testResponder{})
	req := httptest.NewRequest("GET", "/session-budget/samples?since=yesterday", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad since: %d", rec.Code)
	}
}

func TestSamplesRouteWithoutSampler(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	RegisterSessionBudgetRoutes(r, &Services{}, testResponder{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/session-budget/samples", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no sampler: %d %s", rec.Code, rec.Body.String())
	}
	if out := decodeSamples(t, rec); out.Samples == nil || len(out.Samples) != 0 {
		t.Fatalf("want [] not null: %+v", out)
	}
}

func decodeSamples(t *testing.T, rec *httptest.ResponseRecorder) pigtail.SessionSamples {
	t.Helper()
	var out pigtail.SessionSamples
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

// budgetRouter mounts the /session-budget routes on a gate-on controller put
// in fixture f's state, on a fake clock.
func budgetRouter(t *testing.T, f sessbudgettest.Fixture) (*chi.Mux, *sessbudget.Controller, *sessbudgettest.Clock) {
	t.Helper()
	clk := sessbudgettest.NewClock()
	c := sessbudget.NewController(sessbudgettest.Config(), clk.Now, t.Logf)
	for _, s := range f.Samples(clk) {
		c.Observe(s)
	}
	r := chi.NewRouter()
	RegisterSessionBudgetRoutes(r, &Services{SessionBudget: c}, testResponder{})
	return r, c, clk
}

func budgetDo(t *testing.T, r http.Handler, method, path, body, callerScope string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if callerScope != "" {
		req.Header.Set(pigtail.HeaderScope, callerScope)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// lease posts a lease ask and decodes the answer, failing on a non-200.
func lease(t *testing.T, r http.Handler, body string) (pigtail.SessionLeaseAnswer, map[string]any) {
	t.Helper()
	rec := budgetDo(t, r, "POST", "/session-budget/leases", body, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lease %s: %d %s", body, rec.Code, rec.Body.String())
	}
	var a pigtail.SessionLeaseAnswer
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	return a, raw
}

const agentAsk = `{"kind":"agent","origin":"spawn","repo":"pigtail","no_wait":true}`

func release(t *testing.T, r http.Handler, body string) {
	t.Helper()
	if rec := budgetDo(t, r, "POST", "/session-budget/leases/release", body, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("release %s: %d %s", body, rec.Code, rec.Body.String())
	}
}

func budgetState(t *testing.T, r http.Handler) pigtail.SessionBudgetState {
	t.Helper()
	rec := budgetDo(t, r, "GET", "/session-budget", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /session-budget: %d %s", rec.Code, rec.Body.String())
	}
	var s pigtail.SessionBudgetState
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionBudgetRouteReportsDisabled(t *testing.T) {
	t.Parallel()
	cfg := sessbudgettest.Config()
	cfg.Ceiling = 0
	r := chi.NewRouter()
	RegisterSessionBudgetRoutes(r, &Services{SessionBudget: sessbudget.NewController(cfg, time.Now, nil)}, testResponder{})
	if s := budgetState(t, r); s.Enabled {
		t.Fatalf("ceiling 0: %+v", s)
	}
	on, _, _ := budgetRouter(t, sessbudgettest.AboveFloor)
	if s := budgetState(t, on); !s.Enabled {
		t.Fatalf("ceiling > 0: %+v", s)
	}
}

func TestSessionBudgetRouteShowsDeferredGaugeNoRows(t *testing.T) {
	t.Parallel()
	f := sessbudgettest.AboveFloor
	r, c, clk := budgetRouter(t, f)
	c.SetDeferredWakes(2, clk.Now().Add(-3*time.Minute))
	rec := budgetDo(t, r, "GET", "/session-budget", "", "")
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sessions", "queue", "positions"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("GET /session-budget lists %q: %s", k, rec.Body.String())
		}
	}
	s := budgetState(t, r)
	if s.DeferredWakes.Count != 2 || s.DeferredWakes.OldestAgeSeconds != 180 {
		t.Fatalf("deferred gauge: %+v", s.DeferredWakes)
	}
	if s.Busy != f.Busy || s.Budget != f.Budget() || s.Gate != "open" {
		t.Fatalf("state: %+v", s)
	}
}

func TestLeaseAgentWithoutNoWaitIs400(t *testing.T) {
	t.Parallel()
	r, c, _ := budgetRouter(t, sessbudgettest.AboveFloor)
	rec := budgetDo(t, r, "POST", "/session-budget/leases", `{"kind":"agent"}`, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no_wait required for kind agent") {
		t.Fatalf("agent without no_wait: %d %s", rec.Code, rec.Body.String())
	}
	if !c.LastAdmission().IsZero() {
		t.Fatal("a 400 recorded an admission")
	}
	if rec := budgetDo(t, r, "POST", "/session-budget/leases", `{"kind":"wake","no_wait":true}`, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("kind wake over HTTP: %d", rec.Code)
	}
}

func TestHumanLeaseAlwaysAdmitted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		f    sessbudgettest.Fixture
		over bool
	}{{sessbudgettest.RedFull, true}, {sessbudgettest.AboveFloor, false}} {
		r, _, _ := budgetRouter(t, c.f)
		for _, body := range []string{`{"kind":"human"}`, `{"kind":"human","no_wait":true}`} {
			a, raw := lease(t, r, body)
			if !a.Admitted || a.OverBudget == nil || *a.OverBudget != c.over {
				t.Fatalf("%s %s: %+v", c.f.Name, body, raw)
			}
			if c.over && a.Reason == "" || !c.over && a.Reason != "" {
				t.Fatalf("%s: reason %q", c.f.Name, a.Reason)
			}
			for _, k := range []string{"busy", "budget", "reason", "admitted", "over_budget"} {
				if _, ok := raw[k]; !ok {
					t.Fatalf("%s: no %q in %v", c.f.Name, k, raw)
				}
			}
		}
	}
}

func TestNoWaitAdmitted(t *testing.T) {
	t.Parallel()
	f := sessbudgettest.AboveFloor
	r, c, clk := budgetRouter(t, f)
	first, _ := lease(t, r, agentAsk)
	clk.Advance(sessbudgettest.MinGap)
	c.Observe(f.AdvanceSample(clk))
	a, raw := lease(t, r, agentAsk)
	if !first.Admitted || !a.Admitted || a.LeaseID == "" || a.Reason != "" {
		t.Fatalf("admitted ask: %+v", raw)
	}
	if _, ok := raw["over_budget"]; ok {
		t.Fatalf("over_budget on an agent answer: %v", raw)
	}
}

func TestNoWaitRefusedStoresNothingAndCarriesBusyAndBudget(t *testing.T) {
	t.Parallel()
	f := sessbudgettest.RedFull
	r, c, _ := budgetRouter(t, f)
	a, raw := lease(t, r, agentAsk)
	if a.Admitted || a.LeaseID != "" || a.Reason != sessbudgettest.RedCause || a.Busy != f.Busy || a.Budget != f.Budget() {
		t.Fatalf("refusal: %+v", raw)
	}
	if !c.LastAdmission().IsZero() || c.Input(sessbudget.KindAgent).Busy != f.Busy {
		t.Fatal("a refusal stored something")
	}
}

func TestLeaseReleaseRestoresGap(t *testing.T) {
	t.Parallel()
	f := sessbudgettest.AboveFloor
	r, c, _ := budgetRouter(t, f)
	a, _ := lease(t, r, agentAsk)
	release(t, r, `{"lease_id":"`+a.LeaseID+`"}`)
	if !c.LastAdmission().IsZero() {
		t.Fatalf("lastAdmissionAt %v after release", c.LastAdmission())
	}
	if b, raw := lease(t, r, agentAsk); !b.Admitted {
		t.Fatalf("next ask refused after a release: %v", raw)
	}
}

func TestLeaseReleaseIdempotentAndUnknownIs204(t *testing.T) {
	t.Parallel()
	r, _, _ := budgetRouter(t, sessbudgettest.AboveFloor)
	a, _ := lease(t, r, agentAsk)
	release(t, r, `{"lease_id":"`+a.LeaseID+`"}`)
	release(t, r, `{"lease_id":"`+a.LeaseID+`"}`)
	release(t, r, `{"lease_id":"sl_unknown"}`)
	release(t, r, `{"lease_id":"sl_unknown","launched":true}`)
}

func TestLeaseReleaseNoopWhenLaterAdmission(t *testing.T) {
	t.Parallel()
	f := sessbudgettest.AboveFloor
	r, c, clk := budgetRouter(t, f)
	a1, _ := lease(t, r, agentAsk)
	clk.Advance(sessbudgettest.MinGap)
	c.Observe(f.AdvanceSample(clk))
	a2, _ := lease(t, r, agentAsk)
	if !a1.Admitted || !a2.Admitted {
		t.Fatal("asks refused")
	}
	at2 := c.LastAdmission()
	release(t, r, `{"lease_id":"`+a1.LeaseID+`"}`)
	if !c.LastAdmission().Equal(at2) {
		t.Fatalf("release of an older lease moved lastAdmissionAt to %v", c.LastAdmission())
	}
}

func TestLeaseReleaseDropsPendingLease(t *testing.T) {
	t.Parallel()
	f := sessbudgettest.AboveFloor
	r, _, _ := budgetRouter(t, f)
	a, _ := lease(t, r, agentAsk)
	if s := budgetState(t, r); s.Busy != f.Busy+1 {
		t.Fatalf("pending lease not in busy: %+v", s)
	}
	release(t, r, `{"lease_id":"`+a.LeaseID+`"}`)
	if s := budgetState(t, r); s.Busy != f.Busy {
		t.Fatalf("released lease still in busy: %+v", s)
	}
}

func TestLeaseLaunchedCountsUntilNextSample(t *testing.T) {
	t.Parallel()
	f := sessbudgettest.AboveFloor
	r, c, clk := budgetRouter(t, f)
	a, _ := lease(t, r, agentAsk)
	at := c.LastAdmission()
	release(t, r, `{"lease_id":"`+a.LeaseID+`","launched":true}`)
	if s := budgetState(t, r); s.Busy != f.Busy+1 || !c.LastAdmission().Equal(at) {
		t.Fatalf("launched report gave the place back: %+v, last %v", s, c.LastAdmission())
	}
	c.Observe(f.AdvanceSample(clk))
	if s := budgetState(t, r); s.Busy != f.Busy {
		t.Fatalf("launched lease still counted after a later sample: %+v", s)
	}
	release(t, r, `{"lease_id":"sl_nope","launched":true}`)
}

func TestLeaseRefusalCarriesReason(t *testing.T) {
	t.Parallel()
	r, _, _ := budgetRouter(t, sessbudgettest.AboveFloor)
	if a, _ := lease(t, r, agentAsk); !a.Admitted {
		t.Fatal("first ask refused")
	}
	a, raw := lease(t, r, agentAsk)
	if a.Admitted || a.Reason != sessbudget.ReasonGap(sessbudgettest.MinGap) {
		t.Fatalf("second ask inside the gap: %v", raw)
	}
}

// A lease belongs to its caller's scope: another non-master scope cannot
// give it back or report it launched (the answer stays 204, naming nothing).
func TestLeaseReleaseScopedToCallerScope(t *testing.T) {
	t.Parallel()
	f := sessbudgettest.AboveFloor
	r, _, _ := budgetRouter(t, f)
	var other scope.Scope
	for _, s := range scope.Active().Names() {
		if s != scope.Master() {
			other = s
			break
		}
	}
	if other == "" {
		t.Skip("one scope only")
	}
	rec := budgetDo(t, r, "POST", "/session-budget/leases", agentAsk, string(scope.Master()))
	var a pigtail.SessionLeaseAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if !a.Admitted {
		t.Fatalf("master ask: %s", rec.Body.String())
	}
	if rec := budgetDo(t, r, "POST", "/session-budget/leases/release", `{"lease_id":"`+a.LeaseID+`"}`, string(other)); rec.Code != http.StatusNoContent {
		t.Fatalf("foreign release: %d", rec.Code)
	}
	if s := budgetState(t, r); s.Busy != f.Busy+1 {
		t.Fatalf("another scope released the master's lease: %+v", s)
	}
	if rec := budgetDo(t, r, "POST", "/session-budget/leases/release", `{"lease_id":"`+a.LeaseID+`"}`, string(scope.Master())); rec.Code != http.StatusNoContent {
		t.Fatalf("own release: %d", rec.Code)
	}
	if s := budgetState(t, r); s.Busy != f.Busy {
		t.Fatalf("own release ignored: %+v", s)
	}
}
