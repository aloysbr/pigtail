package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeWSStore satisfies rammon's storeIface structurally: it returns a fixed
// workspace list so the monitor's snapshot is deterministic.
type fakeWSStore struct {
	workspaces []pigtail.Workspace
}

// testPathsExist declares the fixtures' workspaces as present on disk. They
// live at paths like /tmp/ws1 that no test creates, so the production stat
// would read every one of them as a removed worktree and skip it.
var testPathsExist = rammon.WithPathExists(func(string) bool { return true })

func (f *fakeWSStore) ListWorkspaces(pigtail.WorkspaceState) ([]pigtail.Workspace, error) {
	return f.workspaces, nil
}

// newServersTestRouter wires GET /servers against a RAM monitor whose seams are
// fully injected: a fake store with one workspace, a busy port with a known PID,
// a known idle duration, and a known eviction floor. `available` comes from the
// real platform sampler (non-zero, no error on a normal host).
func newServersTestRouter(t *testing.T, st *fakeWSStore, floorBytes uint64) *chi.Mux {
	t.Helper()
	mon := rammon.New(events.New(), st,
		rammon.WithConfig(rammon.Config{FloorBytes: floorBytes}),
		testPathsExist,
		rammon.WithServersFunc(func(ws pigtail.Workspace) []rammon.ServerProc {
			out := make([]rammon.ServerProc, 0, len(ws.Ports))
			for kind, port := range ws.Ports {
				out = append(out, rammon.ServerProc{Name: kind, Port: port})
			}
			return out
		}),
		rammon.WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 {
				return true, []int{4242}, nil
			}
			return false, nil, nil
		}),
		rammon.WithIdleFunc(func(pigtail.Workspace, string) time.Duration {
			return 90 * time.Second
		}),
	)
	svc := &Services{RAMMonitor: mon}
	r := chi.NewRouter()
	r.Get("/servers", ListServers(svc, testResponder{}))
	return r
}

func TestListServersOneRunning(t *testing.T) {
	t.Parallel()
	const floor = uint64(6) << 30
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}}
	r := newServersTestRouter(t, st, floor)

	rec := doJSON(t, r, "GET", "/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.FloorBytes != floor {
		t.Errorf("floor_bytes = %d, want %d", resp.FloorBytes, floor)
	}
	if resp.AvailableBytes == 0 {
		t.Errorf("available_bytes = 0, want a sampled value > 0")
	}
	if len(resp.Servers) != 1 {
		t.Fatalf("servers = %d, want 1: %s", len(resp.Servers), rec.Body.String())
	}
	s := resp.Servers[0]
	if s.WorkspaceID != "ws1" || s.Server != "dev" || s.Port != 3030 {
		t.Errorf("server = %+v, want ws1/dev/3030", s)
	}
	if s.IdleSeconds != 90 {
		t.Errorf("idle_seconds = %d, want 90", s.IdleSeconds)
	}
}

func TestListServersEmptySnapshot(t *testing.T) {
	t.Parallel()
	const floor = uint64(4) << 30
	// No workspaces → no running servers, but headroom is still reported.
	st := &fakeWSStore{workspaces: nil}
	r := newServersTestRouter(t, st, floor)

	rec := doJSON(t, r, "GET", "/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	// servers must marshal as [] (never null) per the handler's pre-init.
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("invalid JSON: %s", rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if string(raw["servers"]) != "[]" {
		t.Errorf("servers = %s, want []", raw["servers"])
	}
	var resp pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.FloorBytes != floor {
		t.Errorf("floor_bytes = %d, want %d", resp.FloorBytes, floor)
	}
}

// TestListServersExposesOwner: an identity derived from the POSIX session is
// implicit by nature; GET /servers is where it becomes inspectable.
func TestListServersExposesOwner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	// Written where the CLI writes it — in pigtail's state dir, keyed by the
	// worktree path — not at the worktree root. That the daemon resolves the
	// same path from the same worktree, in another process, is the property
	// under test here as much as the JSON shape.
	if _, err := devserver.EnsureStateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devserver.OwnerPath(dir, "dev"),
		[]byte("sid:4711@laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: dir, Ports: map[string]int{"dev": 3030}},
	}}
	r := newServersTestRouter(t, st, 6<<30)
	rec := doJSON(t, r, "GET", "/servers", nil)

	var got pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 1 {
		t.Fatalf("want 1 server, got %d", len(got.Servers))
	}
	if got.Servers[0].Owner != "sid:4711@laptop" {
		t.Fatalf("owner = %q, want sid:4711@laptop", got.Servers[0].Owner)
	}
	// The host in the marker is not this machine's, so the identity is not
	// interrogated and is reported alive — the conservative direction.
	if !got.Servers[0].OwnerAlive {
		t.Fatal("an identity from another host must be reported alive")
	}
}

// TestListServersUnclaimedOmitsBothOwnerFields guards the fix made while
// implementing this handler: devserver.OwnerAlive("") itself reports true
// (its "opaque, cannot interrogate" branch), so computing OwnerAlive
// unconditionally from an empty Owner would defeat owner_alive's
// omitempty and leak "owner_alive":true for a server nobody has claimed.
// Asserted on the raw JSON, not the decoded struct, because decoding
// through pigtail.ServerView would hide exactly this bug: an absent field
// and a present-but-false field decode to the same Go zero value.
func TestListServersUnclaimedOmitsBothOwnerFields(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir() // no owner marker written anywhere: unclaimed
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: dir, Ports: map[string]int{"dev": 3030}},
	}}
	r := newServersTestRouter(t, st, 6<<30)
	rec := doJSON(t, r, "GET", "/servers", nil)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var servers []map[string]json.RawMessage
	if err := json.Unmarshal(raw["servers"], &servers); err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("want 1 server, got %d: %s", len(servers), rec.Body.String())
	}
	if _, present := servers[0]["owner"]; present {
		t.Errorf(`"owner" must be absent for an unclaimed server, got: %s`, rec.Body.String())
	}
	if _, present := servers[0]["owner_alive"]; present {
		t.Errorf(`"owner_alive" must be absent for an unclaimed server, got: %s`, rec.Body.String())
	}
}

// newStopIdleTestRouter wires POST /servers/stop-idle (plus its siblings)
// against a monitor with two workspaces: ws1:dev (port 3030, idle 30m) and
// ws2:dev (port 3040, idle 1m) — the same fixture shape as rammon's own
// twoWorkspaces(), so the idle-over-threshold/spared split is easy to reason
// about.
func newStopIdleTestRouter(t *testing.T, cfg rammon.Config) (*chi.Mux, *rammon.RAMMonitor) {
	t.Helper()
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
	}}
	mon := rammon.New(nil, st,
		rammon.WithConfig(cfg),
		testPathsExist,
		rammon.WithServersFunc(func(ws pigtail.Workspace) []rammon.ServerProc {
			out := make([]rammon.ServerProc, 0, len(ws.Ports))
			for kind, port := range ws.Ports {
				out = append(out, rammon.ServerProc{Name: kind, Port: port})
			}
			return out
		}),
		rammon.WithPortPIDs(func(port int) (bool, []int, error) {
			switch port {
			case 3030:
				return true, []int{100}, nil
			case 3040:
				return true, []int{200}, nil
			default:
				return false, nil, nil
			}
		}),
		rammon.WithIdleFunc(func(ws pigtail.Workspace, _ string) time.Duration {
			if ws.ID == "ws1" {
				return 30 * time.Minute
			}
			return 1 * time.Minute
		}),
		rammon.WithKiller(func([]int) {}),
	)
	svc := &Services{RAMMonitor: mon}
	r := chi.NewRouter()
	RegisterServerRoutes(r, svc, testResponder{})
	return r, mon
}

// TestStopIdleServersDefaultGrace: an empty body falls back to the daemon's
// configured idle-purge grace; ws1 (30m) is stopped, ws2 (1m) is kept and
// says why.
func TestStopIdleServersDefaultGrace(t *testing.T) {
	t.Parallel()
	cfg := rammon.Defaults()
	cfg.IdlePurgeGrace = 10 * time.Minute
	r, _ := newStopIdleTestRouter(t, cfg)

	rec := doJSON(t, r, "POST", "/servers/stop-idle", map[string]int{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out pigtail.StopIdleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if len(out.Servers) != 2 {
		t.Fatalf("servers=%d, want 2: %s", len(out.Servers), rec.Body.String())
	}
	var stopped, kept int
	for _, s := range out.Servers {
		if s.Stopped {
			stopped++
			if s.WorkspaceID != "ws1" {
				t.Errorf("stopped the wrong server: %+v", s)
			}
		} else {
			kept++
			if s.Reason == "" {
				t.Errorf("a kept server must say why: %+v", s)
			}
		}
	}
	if stopped != 1 || kept != 1 {
		t.Fatalf("stopped=%d kept=%d, want 1,1: %s", stopped, kept, rec.Body.String())
	}
}

// TestStopIdleServersExplicitGraceOverridesConfigured: an explicit
// grace_seconds shorter than the configured grace stops both servers.
func TestStopIdleServersExplicitGraceOverridesConfigured(t *testing.T) {
	t.Parallel()
	cfg := rammon.Defaults() // IdlePurgeGrace defaults to 0 (disabled)
	r, _ := newStopIdleTestRouter(t, cfg)

	rec := doJSON(t, r, "POST", "/servers/stop-idle", map[string]int{"grace_seconds": 30})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out pigtail.StopIdleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	stopped := 0
	for _, s := range out.Servers {
		if s.Stopped {
			stopped++
		}
	}
	if stopped != 2 {
		t.Fatalf("stopped=%d, want 2 (both past a 30s grace): %s", stopped, rec.Body.String())
	}
}

// TestStopIdleServersSparesPinned: a pinned idle-over-threshold server is
// spared and reported with reason "pinned", never stopped.
func TestStopIdleServersSparesPinned(t *testing.T) {
	t.Parallel()
	cfg := rammon.Defaults()
	cfg.IdlePurgeGrace = 10 * time.Minute
	r, mon := newStopIdleTestRouter(t, cfg)
	if !mon.Pin("ws1", "dev", time.Hour) {
		t.Fatal("Pin ws1:dev returned false, want true")
	}

	rec := doJSON(t, r, "POST", "/servers/stop-idle", map[string]int{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out pigtail.StopIdleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, s := range out.Servers {
		if s.WorkspaceID == "ws1" && (s.Stopped || s.Reason != "pinned") {
			t.Fatalf("pinned server misreported: %+v", s)
		}
	}
}

// TestStopIdleServersNegativeGrace: a negative grace_seconds is refused.
func TestStopIdleServersNegativeGrace(t *testing.T) {
	t.Parallel()
	r, _ := newStopIdleTestRouter(t, rammon.Defaults())
	rec := doJSON(t, r, "POST", "/servers/stop-idle", map[string]int{"grace_seconds": -5})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestStopIdleServersInvalidJSON: malformed JSON is refused.
func TestStopIdleServersInvalidJSON(t *testing.T) {
	t.Parallel()
	r, _ := newStopIdleTestRouter(t, rammon.Defaults())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/servers/stop-idle", strings.NewReader("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestStopIdleServersNoGraceAndPurgeDisabled: no grace_seconds given and the
// daemon's idle-purge grace is disabled (0) — this must refuse rather than
// silently doing nothing or stopping everything.
func TestStopIdleServersNoGraceAndPurgeDisabled(t *testing.T) {
	t.Parallel()
	r, _ := newStopIdleTestRouter(t, rammon.Defaults()) // IdlePurgeGrace == 0
	rec := doJSON(t, r, "POST", "/servers/stop-idle", map[string]int{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestStopIdleServersNilMonitor: no RAM monitor wired → 200 with no servers,
// never an error.
func TestStopIdleServersNilMonitor(t *testing.T) {
	t.Parallel()
	svc := &Services{RAMMonitor: nil}
	r := chi.NewRouter()
	RegisterServerRoutes(r, svc, testResponder{})

	rec := doJSON(t, r, "POST", "/servers/stop-idle", map[string]int{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if string(raw["servers"]) != "[]" {
		t.Errorf("servers = %s, want []", raw["servers"])
	}
}

// newDeathTestRouter wires the /servers routes against a monitor whose only
// server (ws1:dev on 3030) is alive while *live is true. Sweep is the exported
// entry point that folds the activity picture forward, so a test can make a
// server appear and vanish and observe what the daemon concluded.
func newDeathTestRouter(t *testing.T) (*chi.Mux, *rammon.RAMMonitor, *bool) {
	t.Helper()
	live := true
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}}
	cfg := rammon.Defaults()
	cfg.FloorBytes = 1         // never under the floor: no eviction path
	cfg.BatteryEnabled = false // no power sampling in a unit test
	mon := rammon.New(events.New(), st,
		rammon.WithConfig(cfg),
		testPathsExist,
		rammon.WithServersFunc(func(ws pigtail.Workspace) []rammon.ServerProc {
			out := make([]rammon.ServerProc, 0, len(ws.Ports))
			for kind, port := range ws.Ports {
				out = append(out, rammon.ServerProc{Name: kind, Port: port})
			}
			return out
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

// TestMarkServerStoppedSuppressesTheDeath is the end-to-end reason the route
// exists: a CLI stop announced through it must keep the following sweep from
// reporting the disappearance as a death.
func TestMarkServerStoppedSuppressesTheDeath(t *testing.T) {
	t.Parallel()
	r, mon, live := newDeathTestRouter(t)

	mon.Sweep(context.Background()) // ws1:dev seen alive
	rec := doJSON(t, r, "POST", "/servers/stopped", map[string]string{
		"workspace_id": "ws1", "server": "dev",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 must carry no body, got %q", rec.Body.String())
	}
	*live = false
	mon.Sweep(context.Background())

	if deaths := mon.RecentDeaths(time.Now()); len(deaths) != 0 {
		t.Fatalf("an announced stop was still reported as a death: %+v", deaths)
	}
}

// TestUnannouncedStopIsStillADeath is the control for the test above: without
// the notification, the same disappearance IS reported. Without it, the test
// above would pass even if the handler did nothing at all.
func TestUnannouncedStopIsStillADeath(t *testing.T) {
	t.Parallel()
	_, mon, live := newDeathTestRouter(t)

	mon.Sweep(context.Background())
	*live = false
	mon.Sweep(context.Background())

	deaths := mon.RecentDeaths(time.Now())
	if len(deaths) != 1 || deaths[0].WorkspaceID != "ws1" || deaths[0].Port != 3030 {
		t.Fatalf("deaths = %+v, want one for ws1:dev on :3030", deaths)
	}
}

// TestListServersExposesRecentDeaths: GET /servers is the only place any
// client (menu bar, CLI, TUI) can see what the death detector found — the
// field must actually reach the response, not just the monitor's internal
// ring buffer.
func TestListServersExposesRecentDeaths(t *testing.T) {
	t.Parallel()
	r, mon, live := newDeathTestRouter(t)

	mon.Sweep(context.Background()) // ws1:dev seen alive
	*live = false
	mon.Sweep(context.Background()) // vanishes unannounced → recorded as a death

	rec := doJSON(t, r, "GET", "/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.RecentDeaths) != 1 {
		t.Fatalf("recent_deaths = %+v, want exactly one", resp.RecentDeaths)
	}
	d := resp.RecentDeaths[0]
	if d.WorkspaceID != "ws1" || d.Server != "dev" || d.Port != 3030 {
		t.Errorf("death = %+v, want ws1/dev/3030", d)
	}
	if d.AgoSeconds < 0 {
		t.Errorf("ago_seconds = %d, want >= 0", d.AgoSeconds)
	}
}

// TestListServersAgoUsesMonitorClockNotWallClock: ago_seconds — and which
// deaths still fall inside RecentDeaths' 30-minute window — must be computed
// from the RAMMonitor's own injectable clock (RAMMonitor.Now, backed by
// WithClock), not time.Now() read directly in the handler. The fake clock
// here is pinned to a date far from the real wall clock (2030), so if the
// handler ever regresses to time.Now() the death's timestamp would land in
// the "future" relative to real time and ago_seconds would come out wildly
// negative instead of the expected 240 — this is deliberately not a subtle
// timing race, it fails loudly either way.
func TestListServersAgoUsesMonitorClockNotWallClock(t *testing.T) {
	t.Parallel()
	live := true
	fake := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}}
	cfg := rammon.Defaults()
	cfg.FloorBytes = 1         // never under the floor: no eviction path
	cfg.BatteryEnabled = false // no power sampling in a unit test
	mon := rammon.New(events.New(), st,
		rammon.WithConfig(cfg),
		testPathsExist,
		rammon.WithServersFunc(func(ws pigtail.Workspace) []rammon.ServerProc {
			out := make([]rammon.ServerProc, 0, len(ws.Ports))
			for kind, port := range ws.Ports {
				out = append(out, rammon.ServerProc{Name: kind, Port: port})
			}
			return out
		}),
		rammon.WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 && live {
				return true, []int{4242}, nil
			}
			return false, nil, nil
		}),
		rammon.WithKiller(func([]int) {}),
		rammon.WithClock(func() time.Time { return fake }),
	)
	svc := &Services{RAMMonitor: mon}
	r := chi.NewRouter()
	RegisterServerRoutes(r, svc, testResponder{})

	mon.Sweep(context.Background()) // ws1:dev seen alive at `fake`
	live = false
	fake = fake.Add(time.Minute)
	mon.Sweep(context.Background()) // dies, recorded At=fake (t0+1m)

	fake = fake.Add(4 * time.Minute) // 4 simulated minutes pass before the poll

	rec := doJSON(t, r, "GET", "/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.RecentDeaths) != 1 {
		t.Fatalf("recent_deaths = %+v, want exactly one", resp.RecentDeaths)
	}
	if got := resp.RecentDeaths[0].AgoSeconds; got != 240 {
		t.Fatalf("ago_seconds = %d, want 240 (computed from the monitor's injected clock, not wall time)", got)
	}
}

// TestListServersNoDeathsMeansEmptySlice: no death → an empty (never null,
// never populated with a placeholder) recent_deaths.
func TestListServersNoDeathsMeansEmptySlice(t *testing.T) {
	t.Parallel()
	r, mon, _ := newDeathTestRouter(t)
	mon.Sweep(context.Background()) // server stays alive throughout

	rec := doJSON(t, r, "GET", "/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.RecentDeaths) != 0 {
		t.Fatalf("recent_deaths = %+v, want none", resp.RecentDeaths)
	}
}

// TestMarkServerStoppedIsNotSwallowedByTheWildcardRoute: /servers/stopped is a
// static route and must be registered before /servers/{ws}/... or chi would
// bind "stopped" as a workspace parameter.
func TestMarkServerStoppedIsNotSwallowedByTheWildcardRoute(t *testing.T) {
	t.Parallel()
	r, _ := newStopIdleTestRouter(t, rammon.Defaults())
	rec := doJSON(t, r, "POST", "/servers/stopped", map[string]string{
		"workspace_id": "ws1", "server": "dev",
	})
	if rec.Code == http.StatusNotFound {
		t.Fatalf("POST /servers/stopped fell through to the {ws} routes: %s", rec.Body.String())
	}
}

// TestMarkServerStoppedRequiresBothFields: a body missing either field is a
// caller bug, not something to guess at.
func TestMarkServerStoppedRequiresBothFields(t *testing.T) {
	t.Parallel()
	r, _ := newStopIdleTestRouter(t, rammon.Defaults())
	for _, body := range []map[string]string{
		{"server": "dev"},
		{"workspace_id": "ws1"},
		{},
	} {
		rec := doJSON(t, r, "POST", "/servers/stopped", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body=%v → status=%d, want 400: %s", body, rec.Code, rec.Body.String())
		}
	}
}

// TestMarkServerStoppedInvalidJSON: malformed JSON is refused.
func TestMarkServerStoppedInvalidJSON(t *testing.T) {
	t.Parallel()
	r, _ := newStopIdleTestRouter(t, rammon.Defaults())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/servers/stopped", strings.NewReader("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestMarkServerStoppedNilMonitor: no RAM monitor wired → still 204, never an
// error. The notification is best-effort on both ends.
func TestMarkServerStoppedNilMonitor(t *testing.T) {
	t.Parallel()
	svc := &Services{RAMMonitor: nil}
	r := chi.NewRouter()
	RegisterServerRoutes(r, svc, testResponder{})

	rec := doJSON(t, r, "POST", "/servers/stopped", map[string]string{
		"workspace_id": "ws1", "server": "dev",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// TestListServersKeepsTheRSSWireKey: the figure is a footprint now, but
// Pigtail and older CLIs decode "rss_bytes" — the key must not move.
func TestListServersKeepsTheRSSWireKey(t *testing.T) {
	t.Parallel()
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}}
	rec := doJSON(t, newServersTestRouter(t, st, 6<<30), "GET", "/servers", nil)
	var raw struct {
		Servers []map[string]json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Servers) != 1 {
		t.Fatalf("servers = %s", rec.Body.String())
	}
	if _, ok := raw.Servers[0]["rss_bytes"]; !ok {
		t.Fatalf("rss_bytes missing from %s", rec.Body.String())
	}
	if _, ok := raw.Servers[0]["mem_bytes"]; ok {
		t.Fatalf("a mem_bytes key appeared: the wire must not change: %s", rec.Body.String())
	}
}

// regStore registers ports for the allocator the way the store does.
type regStore struct{ ids map[int][]string }

func (r regStore) UsedPorts(string) (map[int]bool, error) {
	out := map[int]bool{}
	for p := range r.ids {
		out[p] = true
	}
	return out, nil
}
func (r regStore) PortRegistrants(_ string, port int) ([]string, bool, error) {
	return r.ids[port], false, nil
}

// GET /servers carries the port conflicts rammon recorded, each with the one
// command that repairs it — ordered when another workspace must move first.
func TestListServersReportsPortConflicts(t *testing.T) {
	t.Parallel()
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws_efb2e9", Path: "/tmp/efb2e9", Ports: map[string]int{"dev": 3000}},
		{ID: "ws_392f9b", Path: "/tmp/392f9b", Ports: map[string]int{"dev": 3013}},
	}}
	mon := rammon.New(events.New(), st, testPathsExist,
		rammon.WithLogger(func(string, ...any) {}),
		rammon.WithServersFunc(func(ws pigtail.Workspace) []rammon.ServerProc {
			if ws.ID == "ws_efb2e9" {
				return []rammon.ServerProc{{Name: "dev", Kind: "dev", Port: 3013, DBPort: 3000}}
			}
			return []rammon.ServerProc{{Name: "dev", Kind: "dev", Port: 3013, DBPort: 3013}}
		}),
		rammon.WithPortPIDs(func(int) (bool, []int, error) { return true, []int{4242}, nil }),
		rammon.WithCwds(func([]int) (map[int]string, error) { return map[int]string{4242: "/tmp/efb2e9/apps/web"}, nil }),
	)
	svc := &Services{RAMMonitor: mon, Ports: ports.New(regStore{ids: map[int][]string{3013: {"ws_392f9b"}}},
		ports.WithBindProbe(func(int) bool { return true }))}
	r := chi.NewRouter()
	r.Get("/servers", ListServers(svc, testResponder{}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/servers", nil))
	var out pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	fixes := map[string]string{}
	for _, c := range out.PortConflicts {
		fixes[c.WorkspaceID+"/"+c.Reason] = c.Fix
	}
	want := "pigtail ports reassign ws_392f9b --kind dev && pigtail ports resync ws_efb2e9"
	if fixes["ws_efb2e9/env_diverges"] != want || fixes["ws_392f9b/foreign_listener"] != want || len(fixes) != 2 {
		t.Fatalf("port_conflicts %+v, want both entries to carry %q", out.PortConflicts, want)
	}
	for _, c := range out.PortConflicts {
		if c.Reason == "foreign_listener" && (c.HolderWorkspaceID != "ws_efb2e9" || c.HolderPath != "/tmp/efb2e9") {
			t.Fatalf("holder %+v, want ws_efb2e9 at /tmp/efb2e9", c)
		}
	}
}
