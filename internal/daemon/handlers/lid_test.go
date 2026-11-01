package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeLid implements LidService without any exec.
type fakeLid struct {
	st       lid.Status
	pr       lid.ProbeResult
	err      error
	gotHours int
	ons      int
	probes   int
}

func (f *fakeLid) On(hours int) (lid.Status, error) {
	f.ons++
	f.gotHours = hours
	if f.err != nil {
		return lid.Status{}, f.err
	}
	f.st.SleepDisabled = true
	return f.st, nil
}

func (f *fakeLid) Off() (lid.Status, error) {
	if f.err != nil {
		return lid.Status{}, f.err
	}
	f.st.SleepDisabled = false
	return f.st, nil
}

func (f *fakeLid) Status() (lid.Status, error) { return f.st, f.err }

func (f *fakeLid) Probe() (lid.ProbeResult, error) {
	f.probes++
	return f.pr, f.err
}

func newLidRouter(f *fakeLid) chi.Router {
	svc := &Services{}
	if f != nil {
		svc.Lid = f
	}
	r := chi.NewRouter()
	RegisterLidRoutes(r, svc, testResponder{})
	return r
}

func serveLid(r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.ServeHTTP(rec, req)
	return rec
}

func lidErrCode(t *testing.T, rec *httptest.ResponseRecorder) (pigtail.ErrorCode, string) {
	t.Helper()
	var e struct {
		Code    pigtail.ErrorCode `json:"code"`
		Message string            `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error: %v (body=%s)", err, rec.Body.String())
	}
	return e.Code, e.Message
}

func TestLidProbe(t *testing.T) {
	t.Parallel()
	f := &fakeLid{pr: lid.ProbeResult{RuleOK: true, Warning: lid.ProbeNoTimerWarning}}
	rec := serveLid(newLidRouter(f), "POST", "/lid/probe", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got pigtail.LidProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.RuleOK || got.Warning != lid.ProbeNoTimerWarning || f.probes != 1 {
		t.Fatalf("got %+v, probes %d", got, f.probes)
	}

	f = &fakeLid{pr: lid.ProbeResult{RuleOK: false}}
	rec = serveLid(newLidRouter(f), "POST", "/lid/probe", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rule_ok":false`) {
		t.Fatalf("rule not ok: %d %s", rec.Code, rec.Body.String())
	}

	f = &fakeLid{err: errors.New("pmset -g: boom")}
	rec = serveLid(newLidRouter(f), "POST", "/lid/probe", "")
	if code, msg := lidErrCode(t, rec); rec.Code != http.StatusInternalServerError || code != pigtail.ErrInternal || !strings.Contains(msg, "boom") {
		t.Fatalf("probe error: %d %s %s", rec.Code, code, msg)
	}

	rec = serveLid(newLidRouter(nil), "POST", "/lid/probe", "")
	if code, _ := lidErrCode(t, rec); rec.Code != http.StatusNotImplemented || code != pigtail.ErrLidUnavailable {
		t.Fatalf("nil service: %d %s", rec.Code, code)
	}
}

func TestLidNilService(t *testing.T) {
	t.Parallel()
	rec := serveLid(newLidRouter(nil), "GET", "/lid", "")
	if code, _ := lidErrCode(t, rec); rec.Code != http.StatusNotImplemented || code != pigtail.ErrLidUnavailable {
		t.Fatalf("got %d %s", rec.Code, code)
	}
}

func TestLidRearmPassesThrough(t *testing.T) {
	t.Parallel()
	f := &fakeLid{st: lid.Status{SleepDisabled: true}}
	r := newLidRouter(f)
	for _, body := range []string{`{"on":true,"hours":8}`, `{"on":true,"hours":3}`} {
		if rec := serveLid(r, "POST", "/lid", body); rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d (%s)", body, rec.Code, rec.Body.String())
		}
	}
	if f.ons != 2 || f.gotHours != 3 {
		t.Fatalf("ons = %d, gotHours = %d", f.ons, f.gotHours)
	}
}

func TestLidOnDefaultHours(t *testing.T) {
	t.Parallel()
	f := &fakeLid{}
	if rec := serveLid(newLidRouter(f), "POST", "/lid", `{"on":true}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if f.gotHours != lid.DefaultHours {
		t.Fatalf("gotHours = %d, want %d", f.gotHours, lid.DefaultHours)
	}
}

func TestLidNotSetUp(t *testing.T) {
	t.Parallel()
	f := &fakeLid{err: lid.ErrNotSetUp}
	rec := serveLid(newLidRouter(f), "POST", "/lid", `{"on":true,"hours":2}`)
	code, msg := lidErrCode(t, rec)
	if rec.Code != http.StatusPreconditionFailed || code != pigtail.ErrLidNotSetUp || !strings.Contains(msg, "pigtail lid setup") {
		t.Fatalf("got %d %s %q", rec.Code, code, msg)
	}
}

func TestLidBadHours(t *testing.T) {
	t.Parallel()
	f := &fakeLid{err: lid.ErrHours}
	if rec := serveLid(newLidRouter(f), "POST", "/lid", `{"on":true,"hours":99}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad hours: %d", rec.Code)
	}
	if rec := serveLid(newLidRouter(&fakeLid{}), "POST", "/lid", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d", rec.Code)
	}
}

func TestLidStatusGuards(t *testing.T) {
	t.Parallel()
	closed, open := true, false
	cases := []struct {
		st   lid.Status
		want []string
		not  []string
	}{
		{lid.Status{SleepDisabled: true, GuardError: "lid reading failed: x", ThermalState: "fair", LidClosed: &closed},
			[]string{`"guard_error":"lid reading failed: x"`, `"thermal_state":"fair"`, `"lid_closed":true`}, nil},
		{lid.Status{SleepDisabled: true, ThermalState: "serious", LidClosed: &open},
			[]string{`"lid_closed":false`}, []string{"guard_error"}},
		{lid.Status{}, nil, []string{"guard_error", "thermal_state", "lid_closed"}},
	}
	for _, c := range cases {
		rec := serveLid(newLidRouter(&fakeLid{st: c.st}), "GET", "/lid", "")
		body := rec.Body.String()
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("body %s: missing %s", body, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(body, n) {
				t.Errorf("body %s: unexpected %s", body, n)
			}
		}
	}
}

func TestLidStatusNoTimer(t *testing.T) {
	t.Parallel()
	f := &fakeLid{st: lid.Status{SleepDisabled: true}}
	rec := serveLid(newLidRouter(f), "GET", "/lid", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "deadline") || !strings.Contains(rec.Body.String(), `"sleep_disabled":true`) || !strings.Contains(rec.Body.String(), `"battery_floor":25`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
