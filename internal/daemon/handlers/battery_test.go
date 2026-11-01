package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// emptyStore satisfies the rammon store interface with no workspaces.
type emptyStore struct{}

func (emptyStore) ListWorkspaces(pigtail.WorkspaceState) ([]pigtail.Workspace, error) {
	return nil, nil
}

func decodeBattery(t *testing.T, body string) pigtail.BatteryResponse {
	t.Helper()
	var out pigtail.BatteryResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	return out
}

func newBatteryRouter(t *testing.T) chi.Router {
	t.Helper()
	rc := rammon.Defaults()
	mon := rammon.New(events.New(), emptyStore{}, rammon.WithConfig(rc))
	svc := &Services{RAMMonitor: mon}
	r := chi.NewRouter()
	RegisterBatteryRoutes(r, svc, testResponder{})
	return r
}

func TestGetBattery(t *testing.T) {
	t.Parallel()
	r := newBatteryRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/battery", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := decodeBattery(t, rec.Body.String())
	if !got.Enabled || got.GraceSeconds != 600 {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestSetBatteryToggle(t *testing.T) {
	t.Parallel()
	r := newBatteryRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/battery", strings.NewReader(`{"enabled":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := decodeBattery(t, rec.Body.String())
	if got.Enabled {
		t.Fatalf("POST enabled=false → Enabled=%v, want false", got.Enabled)
	}
}

func TestSetBatteryInvalidJSON(t *testing.T) {
	t.Parallel()
	r := newBatteryRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/battery", strings.NewReader(`not-json`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestGetBatteryNilMonitor(t *testing.T) {
	t.Parallel()
	svc := &Services{RAMMonitor: nil}
	r := chi.NewRouter()
	RegisterBatteryRoutes(r, svc, testResponder{})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/battery", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := decodeBattery(t, rec.Body.String())
	if got.Enabled || got.GraceSeconds != 0 {
		t.Fatalf("nil monitor should return empty BatteryResponse, got: %+v", got)
	}
}
