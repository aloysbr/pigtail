package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/gates"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const gatesOut = `[
 {"id":"t1","title":"Work gate","action":"merge it","scope":"gx","added":"2026-10-01T09:00:00+02:00","status":"fired","errors":0,"fired_at":"2026-10-02T09:00:00+02:00","subject_id":"sub_x000000000000034","value":"v","last_check":"2026-10-02T09:00:00+02:00"},
 {"id":"p1","title":"Zqx perso gate","action":"secret","scope":"perso","added":"2026-10-01T09:00:00+02:00","status":"open","errors":0,"fired_at":null,"subject_id":null,"value":"v","last_check":"2026-10-08T08:00:00+02:00"}
]`

func gatesRouter(src *gates.Source) *chi.Mux {
	r := chi.NewRouter()
	RegisterGateRoutes(r, &Services{Gates: src}, testResponder{})
	return r
}

func getGates(t *testing.T, r http.Handler, scopeHeader string) pigtail.GatesResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/gates", nil)
	if scopeHeader != "" {
		req.Header.Set(pigtail.HeaderScope, scopeHeader)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /gates: %d %s", rec.Code, rec.Body)
	}
	var out pigtail.GatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func fixedRun(out string, err error) gates.Runner {
	return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
}

func TestGatesNotConfigured(t *testing.T) {
	t.Parallel()
	for _, src := range []*gates.Source{nil, {Cmd: ""}} {
		got := getGates(t, gatesRouter(src), string(scope.Master()))
		if got.Configured || got.Counts != nil || got.Error != nil || len(got.Fired)+len(got.Open) != 0 {
			t.Fatalf("unconfigured = %+v", got)
		}
	}
}

func TestGatesMasterSeesAllWorkSeesWork(t *testing.T) {
	t.Parallel()
	r := gatesRouter(&gates.Source{Cmd: "gate", Run: fixedRun(gatesOut, nil)})
	m := getGates(t, r, string(scope.Master()))
	if !m.Configured || !m.Complete || m.Counts == nil || m.Counts.Fired != 1 || m.Counts.Open != 1 {
		t.Fatalf("master = %+v", m)
	}
	work := getGates(t, r, "") // no header = the default scope (work), fail-closed
	if work.Complete || work.Counts.Open != 0 || len(work.Open) != 0 || work.Counts.Fired != 1 {
		t.Fatalf("work caller = %+v, must not see the perso gate", work)
	}
}

func TestGatesSourceErrorHasNoCounts(t *testing.T) {
	t.Parallel()
	ee := errors.New("boom")
	r := gatesRouter(&gates.Source{Cmd: "gate", Run: fixedRun("", ee)})
	got := getGates(t, r, string(scope.Master()))
	if !got.Configured || got.Error == nil || got.Error.Kind != gates.ErrFailed || got.Counts != nil {
		t.Fatalf("source error = %+v, want error.kind failed and no counts", got)
	}
}
