package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newPinTestRouter builds a chi router with all /servers routes wired to a
// RAMMonitor whose seams are injected: ws1:dev on port 3030 (pid 4242), ws2:dev
// on port 3040 (pid 4243). `killed` records each kill call.
func newPinTestRouter(t *testing.T, killed *[][]int) *chi.Mux {
	t.Helper()
	st := &fakeWSStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
	}}
	mon := rammon.New(events.New(), st,
		rammon.WithConfig(rammon.Config{FloorBytes: 6 << 30}),
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
				return true, []int{4242}, nil
			case 3040:
				return true, []int{4243}, nil
			}
			return false, nil, nil
		}),
		rammon.WithIdleFunc(func(pigtail.Workspace, string) time.Duration {
			return 5 * time.Minute
		}),
		rammon.WithKiller(func(pids []int) { *killed = append(*killed, pids) }),
	)
	svc := &Services{RAMMonitor: mon}
	r := chi.NewRouter()
	RegisterServerRoutes(r, svc, testResponder{})
	return r
}

// TestPinServerOK: valid pin request must return 200 with pinned_until > 0.
func TestPinServerOK(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	rec := doJSON(t, r, "POST", "/servers/ws1/dev/pin?duration=1h", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	pinnedUntil, _ := out["pinned_until"].(float64)
	if pinnedUntil <= 0 {
		t.Errorf("pinned_until = %v, want > 0", pinnedUntil)
	}
}

// TestPinServerBadDuration: an invalid duration must return 400.
func TestPinServerBadDuration(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	rec := doJSON(t, r, "POST", "/servers/ws1/dev/pin?duration=2h", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestPinServerMissingDuration: an absent duration must return 400.
func TestPinServerMissingDuration(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	rec := doJSON(t, r, "POST", "/servers/ws1/dev/pin", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestPinServerNotFound: an unknown ws/server must return 404.
func TestPinServerNotFound(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	rec := doJSON(t, r, "POST", "/servers/ws-nope/dev/pin?duration=30m", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestUnpinServerOK: unpin is idempotent and always returns 200.
func TestUnpinServerOK(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	// Pin first so unpin has something to clear.
	doJSON(t, r, "POST", "/servers/ws1/dev/pin?duration=1h", nil)

	rec := doJSON(t, r, "DELETE", "/servers/ws1/dev/pin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Idempotent: unpin again must still return 200.
	rec = doJSON(t, r, "DELETE", "/servers/ws1/dev/pin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("idempotent unpin status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestStopServerOK: stop a known running server must return 200 and record a kill.
func TestStopServerOK(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	rec := doJSON(t, r, "POST", "/servers/ws1/dev/stop", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(killed) != 1 {
		t.Fatalf("expected 1 kill, got %d", len(killed))
	}
}

// TestStopServerNotFound: stop of an unknown server must return 404.
func TestStopServerNotFound(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	rec := doJSON(t, r, "POST", "/servers/ws-nope/ghost/stop", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	if len(killed) != 0 {
		t.Fatalf("unexpected kill on 404 path")
	}
}

// TestStopAllServersOK: stop-all must return 200 with both servers in the
// stopped list and record 2 kill calls.
func TestStopAllServersOK(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	rec := doJSON(t, r, "POST", "/servers/stop-all", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Stopped []map[string]string `json:"stopped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Stopped) != 2 {
		t.Fatalf("stopped count = %d, want 2: %s", len(out.Stopped), rec.Body.String())
	}
	if len(killed) != 2 {
		t.Fatalf("kill calls = %d, want 2", len(killed))
	}
}

// TestListServersIncludesBranchAndPin: after pinning ws1:dev, GET /servers
// should include a non-zero pinned_until for that server.
func TestListServersIncludesBranchAndPin(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	// Pin ws1:dev for 1h.
	rec := doJSON(t, r, "POST", "/servers/ws1/dev/pin?duration=1h", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pin status = %d", rec.Code)
	}

	// List servers and verify pinned_until > 0 for ws1:dev.
	rec = doJSON(t, r, "GET", "/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, s := range resp.Servers {
		if s.WorkspaceID == "ws1" && s.Server == "dev" {
			found = true
			if s.PinnedUnixSeconds <= 0 {
				t.Errorf("ws1:dev pinned_until = %d, want > 0 after pin", s.PinnedUnixSeconds)
			}
		}
	}
	if !found {
		t.Fatalf("ws1:dev not found in servers list")
	}
}

// A durable pin answers pinned_durable and no expiry, and GET /servers reports
// it the same way.
func TestPinServerUntilUnpinned(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)

	rec := doJSON(t, r, "POST", "/servers/ws1/dev/pin?duration="+pigtail.PinUntilUnpinned, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["pinned_durable"] != true {
		t.Fatalf("pin response = %v, want pinned_durable true", out)
	}
	if v, _ := out["pinned_until"].(float64); v != 0 {
		t.Fatalf("pin response pinned_until = %v, want 0 for a durable pin", v)
	}

	rec = doJSON(t, r, "GET", "/servers", nil)
	var resp pigtail.ServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, s := range resp.Servers {
		if s.WorkspaceID == "ws1" && (!s.PinnedDurable || s.PinnedUnixSeconds != 0) {
			t.Fatalf("ws1:dev = durable %v until %d, want durable true, no until", s.PinnedDurable, s.PinnedUnixSeconds)
		}
		if s.WorkspaceID == "ws2" && s.PinnedDurable {
			t.Fatal("ws2:dev reported durably pinned")
		}
	}
	if strings.Contains(rec.Body.String(), `"pinned_until":0`) {
		t.Fatalf("pinned_until must stay omitted when zero: %s", rec.Body.String())
	}
}

// A durable pin on a server that is not running is a 404, like a timed one.
func TestPinServerUntilUnpinnedUnknownServer(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)
	rec := doJSON(t, r, "POST", "/servers/ws-nope/dev/pin?duration="+pigtail.PinUntilUnpinned, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// The 400 message names the new value, so a caller learns it exists.
func TestPinServerBadDurationNamesUntilUnpinned(t *testing.T) {
	t.Parallel()
	var killed [][]int
	r := newPinTestRouter(t, &killed)
	rec := doJSON(t, r, "POST", "/servers/ws1/dev/pin?duration=forever", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), pigtail.PinUntilUnpinned) {
		t.Fatalf("status = %d body = %s, want 400 naming %s", rec.Code, rec.Body.String(), pigtail.PinUntilUnpinned)
	}
}
