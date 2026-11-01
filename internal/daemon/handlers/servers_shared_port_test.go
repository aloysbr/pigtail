package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newSharedPortDeathRouter is newDeathTestRouter with the monorepo shape: the
// one process on :3030 is declared as both `dev` and `web`.
func newSharedPortDeathRouter(t *testing.T) (*chi.Mux, *rammon.RAMMonitor, *bool) {
	t.Helper()
	live := true
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}}
	cfg := rammon.Defaults()
	cfg.FloorBytes = 1
	cfg.BatteryEnabled = false
	mon := rammon.New(events.New(), st,
		rammon.WithConfig(cfg),
		testPathsExist,
		rammon.WithServersFunc(func(pigtail.Workspace) []rammon.ServerProc {
			return []rammon.ServerProc{{Name: "dev", Port: 3030}, {Name: "web", Port: 3030}}
		}),
		rammon.WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 && live {
				return true, []int{4242}, nil
			}
			return false, nil, nil
		}),
		rammon.WithKiller(func([]int) {}),
	)
	svc := &Services{RAMMonitor: mon}
	r := chi.NewRouter()
	RegisterServerRoutes(r, svc, testResponder{})
	return r, mon, &live
}

// TestListServersDeathCarriesEveryNameOnThePort: one process under two names
// is one entry in recent_deaths, naming both in `servers` while `server`
// keeps the first for clients that only read that field (the menu bar).
func TestListServersDeathCarriesEveryNameOnThePort(t *testing.T) {
	t.Parallel()
	r, mon, live := newSharedPortDeathRouter(t)

	mon.Sweep(context.Background())
	*live = false
	mon.Sweep(context.Background())

	rec := doJSON(t, r, "GET", "/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var raw struct {
		RecentDeaths []map[string]json.RawMessage `json:"recent_deaths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.RecentDeaths) != 1 {
		t.Fatalf("recent_deaths = %s, want exactly one entry for the shared port", rec.Body.String())
	}
	var servers []string
	if err := json.Unmarshal(raw.RecentDeaths[0]["servers"], &servers); err != nil {
		t.Fatalf("servers field missing or malformed in %s: %v", rec.Body.String(), err)
	}
	if !reflect.DeepEqual(servers, []string{"dev", "web"}) {
		t.Errorf("servers = %v, want [dev web]", servers)
	}
	var server string
	_ = json.Unmarshal(raw.RecentDeaths[0]["server"], &server)
	if server != "dev" {
		t.Errorf("server = %q, want dev (first of servers)", server)
	}
}

// TestMarkServerStoppedCoversThePortTwin: the CLI announces the name it was
// asked to stop; the daemon must not report the other name on that port.
func TestMarkServerStoppedCoversThePortTwin(t *testing.T) {
	t.Parallel()
	r, mon, live := newSharedPortDeathRouter(t)

	mon.Sweep(context.Background())
	rec := doJSON(t, r, "POST", "/servers/stopped", map[string]string{
		"workspace_id": "ws1", "server": "web",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204: %s", rec.Code, rec.Body.String())
	}
	*live = false
	mon.Sweep(context.Background())

	if deaths := mon.RecentDeaths(time.Now()); len(deaths) != 0 {
		t.Fatalf("stopping web reported dev as a death: %+v", deaths)
	}
}
