package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func decodePurge(t *testing.T, body string) pigtail.PurgeResponse {
	t.Helper()
	var out pigtail.PurgeResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	return out
}

func newPurgeRouter(t *testing.T) (chi.Router, *rammon.RAMMonitor) {
	t.Helper()
	rc := rammon.Defaults()
	rc.IdlePurgeGrace = 10 * time.Minute
	mon := rammon.New(events.New(), emptyStore{}, rammon.WithConfig(rc))
	svc := &Services{RAMMonitor: mon}
	r := chi.NewRouter()
	RegisterPurgeRoutes(r, svc, testResponder{})
	return r, mon
}

func TestGetPurge(t *testing.T) {
	t.Parallel()
	r, _ := newPurgeRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/purge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := decodePurge(t, rec.Body.String()); got.GraceSeconds != 600 {
		t.Fatalf("GraceSeconds=%d, want 600", got.GraceSeconds)
	}
}

func TestSetPurge(t *testing.T) {
	t.Parallel()
	r, mon := newPurgeRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/purge", strings.NewReader(`{"grace_seconds":1800}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := decodePurge(t, rec.Body.String()); got.GraceSeconds != 1800 {
		t.Fatalf("GraceSeconds=%d, want 1800", got.GraceSeconds)
	}
	if mon.IdlePurgeGrace() != 30*time.Minute {
		t.Fatalf("monitor grace=%s, want 30m", mon.IdlePurgeGrace())
	}
}

func TestSetPurgeOff(t *testing.T) {
	t.Parallel()
	r, mon := newPurgeRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/purge", strings.NewReader(`{"grace_seconds":0}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if mon.IdlePurgeGrace() != 0 {
		t.Fatalf("monitor grace=%s, want 0", mon.IdlePurgeGrace())
	}
}

func TestSetPurgeNegative(t *testing.T) {
	t.Parallel()
	r, _ := newPurgeRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/purge", strings.NewReader(`{"grace_seconds":-5}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestSetPurgeInvalidJSON(t *testing.T) {
	t.Parallel()
	r, _ := newPurgeRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/purge", strings.NewReader(`not-json`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestGetPurgeNilMonitor(t *testing.T) {
	t.Parallel()
	svc := &Services{RAMMonitor: nil}
	r := chi.NewRouter()
	RegisterPurgeRoutes(r, svc, testResponder{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/purge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := decodePurge(t, rec.Body.String()); got.GraceSeconds != 0 {
		t.Fatalf("nil monitor GraceSeconds=%d, want 0", got.GraceSeconds)
	}
}
