package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/cpuslots"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newCPUSlotRouter wires the /cpu/slots routes against an arbiter with an
// explicit budget and no revocation. The returned channel fires (best-effort,
// never blocking) after every state change the arbiter makes, so tests can
// wait on a real event instead of polling Status() on a timer.
func newCPUSlotRouter(t *testing.T, budget int) (*chi.Mux, *cpuslots.Arbiter, chan struct{}) {
	t.Helper()
	notify := make(chan struct{}, 1)
	arb := cpuslots.New(events.New(),
		cpuslots.WithConfig(cpuslots.Config{Budget: budget, MaxHold: time.Hour}),
		cpuslots.WithLogger(func(string, ...any) {}),
		cpuslots.WithNotify(func() {
			select {
			case notify <- struct{}{}:
			default: // a signal is already pending; the reader will re-check Status()
			}
		}),
	)
	svc := &Services{CPUSlots: arb}
	r := chi.NewRouter()
	RegisterCPUSlotRoutes(r, svc, testResponder{})
	return r, arb, notify
}

// waitForSlots blocks on notify until cond holds against the arbiter's
// current Status(), instead of polling on a sleep loop — notify is the seam
// cpuslots.WithNotify exists for.
func waitForSlots(t *testing.T, arb *cpuslots.Arbiter, notify chan struct{}, what string, cond func(cpuslots.Status) bool) {
	t.Helper()
	if cond(arb.Status()) {
		return
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-notify:
			if cond(arb.Status()) {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s (status %+v)", what, arb.Status())
			return
		}
	}
}

func TestGetCPUSlotsReportsTheArbiterState(t *testing.T) {
	t.Parallel()
	r, arb, _ := newCPUSlotRouter(t, 4)
	g, err := arb.Acquire(context.Background(), "ws1:vitest", "a")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer arb.Release(g)

	rec := doJSON(t, r, "GET", "/cpu/slots", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp pigtail.CPUSlotsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Budget != 4 || resp.Held != 1 || resp.Waiting != 0 {
		t.Fatalf("response = %+v, want budget 4 held 1 waiting 0", resp)
	}
	// Share reflects the group's own demand (Held+Waiting), not the idle
	// budget: a lone group with no waiters has proven it wants exactly what
	// it holds. This is cpuslots.shares()'s tested behaviour — see
	// TestSharesThreeGroupsOneModest in internal/daemon/cpuslots/decide_test.go,
	// where a Held:1 group keeps share 1 even against a budget of 9.
	if len(resp.Groups) != 1 || resp.Groups[0].Group != "ws1:vitest" || resp.Groups[0].Share != 1 {
		t.Fatalf("groups = %+v, want one ws1:vitest with share 1", resp.Groups)
	}
}

// groups must marshal as [] rather than null so clients can iterate blindly.
func TestGetCPUSlotsEmptyGroupsMarshalAsArray(t *testing.T) {
	t.Parallel()
	r, _, _ := newCPUSlotRouter(t, 4)
	rec := doJSON(t, r, "GET", "/cpu/slots", nil)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if string(raw["groups"]) != "[]" {
		t.Fatalf("groups = %s, want []", raw["groups"])
	}
}

func TestAcquireCPUSlotRejectsAMissingGroup(t *testing.T) {
	t.Parallel()
	r, _, _ := newCPUSlotRouter(t, 4)
	rec := doJSON(t, r, "POST", "/cpu/slots", pigtail.CPUSlotRequest{Label: "a"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestAcquireCPUSlotOnADisabledArbiter(t *testing.T) {
	t.Parallel()
	r, _, _ := newCPUSlotRouter(t, 0)
	rec := doJSON(t, r, "POST", "/cpu/slots", pigtail.CPUSlotRequest{Group: "ws1:vitest"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code pigtail.ErrorCode `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != pigtail.ErrCPUSlotsDisabled {
		t.Fatalf("code = %q, want %q", body.Code, pigtail.ErrCPUSlotsDisabled)
	}
}

// The load-bearing behaviour of the whole design: the connection *is* the
// slot. This test opens a real one against a real listener, reads the grant,
// then drops the connection and checks the slot came back — no heartbeat, no
// TTL, no PID.
func TestAcquireCPUSlotIsReleasedWhenTheClientDisconnects(t *testing.T) {
	t.Parallel()
	r, arb, notify := newCPUSlotRouter(t, 1)
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/cpu/slots",
		strings.NewReader(`{"group":"ws1:vitest","label":"Button.test.tsx"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("do: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The grant arrives before the connection closes: the handler flushed it.
	var grant pigtail.CPUSlotGrant
	if err := json.NewDecoder(resp.Body).Decode(&grant); err != nil {
		resp.Body.Close()
		cancel()
		t.Fatalf("decode grant: %v", err)
	}
	if !grant.Granted || grant.Degraded {
		resp.Body.Close()
		cancel()
		t.Fatalf("grant = %+v, want granted and not degraded", grant)
	}
	waitForSlots(t, arb, notify, "the slot to be booked", func(s cpuslots.Status) bool { return s.Held == 1 })

	// Drop the connection exactly as a killed vitest worker would.
	cancel()
	resp.Body.Close()
	waitForSlots(t, arb, notify, "the slot to come back", func(s cpuslots.Status) bool { return s.Held == 0 })
}

func TestCPUSlotHistoryRoute(t *testing.T) {
	t.Parallel()
	hist := cpuslots.NewHistory(filepath.Join(t.TempDir(), "cpu-slots-history.jsonl"))
	arb := cpuslots.New(events.New(),
		cpuslots.WithConfig(cpuslots.Config{Budget: 1, MaxHold: time.Hour}),
		cpuslots.WithLogger(func(string, ...any) {}),
		cpuslots.WithHistory(hist))
	r := chi.NewRouter()
	RegisterCPUSlotRoutes(r, &Services{CPUSlots: arb}, testResponder{})

	g, err := arb.Acquire(context.Background(), "ws1:vitest", "Button.test.tsx")
	if err != nil {
		t.Fatal(err)
	}
	arb.Release(g)

	rec := doJSON(t, r, "GET", "/cpu/slots/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out pigtail.CPUSlotHistory
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Records) != 1 || out.Records[0].Group != "ws1:vitest" || out.Records[0].Outcome != pigtail.CPUSlotOutcomeReleased {
		t.Fatalf("history = %+v", out)
	}
	if rec = doJSON(t, r, "GET", "/cpu/slots/history?since=garbage", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad since: status = %d", rec.Code)
	}
	// No arbiter: an empty list, not an error.
	r2 := chi.NewRouter()
	RegisterCPUSlotRoutes(r2, &Services{}, testResponder{})
	rec = doJSON(t, r2, "GET", "/cpu/slots/history", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"records":[]`) {
		t.Fatalf("no arbiter: %d %s", rec.Code, rec.Body.String())
	}
}
