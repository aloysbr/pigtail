package cmuxsync

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeServers stands in for rammon: what listens, and the recent deaths.
type fakeServers struct {
	snap  ServerSnapshot
	calls int
}

func (f *fakeServers) read() ServerSnapshot {
	f.calls++
	return f.snap
}

func withServers(h *harness) *fakeServers {
	f := &fakeServers{}
	h.r.deps.Servers = f.read
	return f
}

func srvPill(h *harness, ws string) string { return h.world.status[ws][KeyServers].Value }

// The slow tick reads the servers: a server owned by the workspace shows up
// within one slow pass, and is not re-read on the 1 s attention passes.
func TestRunnerServerPillOnSlowTick(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	f := withServers(h)
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.step(0)
	f.snap = ServerSnapshot{Up: []ServerObs{up("storybook", 6006, wsA)}}
	reads := f.calls
	for i := 0; i < 29; i++ {
		h.step(time.Second)
	}
	if f.calls != reads || srvPill(h, wsA) != "" {
		t.Fatalf("servers read on a fast pass: calls %d→%d, pill %q", reads, f.calls, srvPill(h, wsA))
	}
	h.step(time.Second)
	if srvPill(h, wsA) != "sb :6006 up" || srvPill(h, wsB) != "" {
		t.Fatalf("pills a=%q b=%q", srvPill(h, wsA), srvPill(h, wsB))
	}
	if p := h.world.status[wsA][KeyServers].Priority; p != 60 {
		t.Fatalf("priority %d", p)
	}
}

// A death announced on the bus shows on the very next pass, without waiting
// for the slow tick, and without listing cmux again.
func TestRunnerServerDeathShowsAtOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	f := withServers(h)
	h.world.add(winA, wsA, "a")
	f.snap = ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}
	h.step(0)
	if srvPill(h, wsA) != "dev :5173 up" {
		t.Fatalf("pill %q", srvPill(h, wsA))
	}
	h.step(2 * time.Second)

	f.snap = ServerSnapshot{Deaths: []ServerDeath{died(h.now.Add(time.Second), 5173, wsA, "dev")}}
	h.world.calls = nil
	h.r.ServersChanged()
	h.step(time.Second)
	if srvPill(h, wsA) != "dev :5173 dead" {
		t.Fatalf("death not shown at once: %q", srvPill(h, wsA))
	}
	for _, c := range h.world.calls {
		if strings.HasPrefix(c, "list-windows") || strings.HasPrefix(c, "workspace-list") {
			t.Fatalf("a server event re-listed cmux: %q", h.world.calls)
		}
	}
}

// pigtail stop: stopped on the next slow pass, then the line goes after 5 min.
func TestRunnerServerStoppedThenCleared(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	f := withServers(h)
	h.world.add(winA, wsA, "a")
	f.snap = ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}
	h.step(0)
	f.snap = ServerSnapshot{}
	h.step(30 * time.Second)
	if srvPill(h, wsA) != "dev :5173 stopped" {
		t.Fatalf("pill %q", srvPill(h, wsA))
	}
	for i := 0; i < 10; i++ {
		h.step(30 * time.Second)
	}
	if _, ok := h.world.status[wsA][KeyServers]; ok {
		t.Fatalf("stopped pill kept past 5 min: %q", srvPill(h, wsA))
	}
}

// The event and the state carry the servers; a change of server state alone
// publishes cmux.sidebar_changed.
func TestRunnerPublishesServers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	f := withServers(h)
	h.world.add(winA, wsA, "a")
	h.step(0)
	h.events = nil
	f.snap = ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}
	h.r.ServersChanged()
	h.step(time.Second)
	var got []pigtail.Event
	for _, e := range h.events {
		if e.Type == pigtail.EventCmuxSidebarChanged {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("events %+v", got)
	}
	srv, ok := got[0].Data["servers"].([]pigtail.CmuxSidebarServer)
	if !ok || len(srv) != 1 || srv[0] != (pigtail.CmuxSidebarServer{Name: "dev", Port: 5173, State: "up"}) {
		t.Fatalf("event servers %#v", got[0].Data["servers"])
	}
	st := h.r.State()
	if len(st.Workspaces) != 1 || len(st.Workspaces[0].Servers) != 1 || st.Workspaces[0].Servers[0].State != "up" {
		t.Fatalf("state %+v", st)
	}
}

// Without a server source (rammon absent) nothing is written under pigtail-srv,
// and a stale one persisted by cmux is cleared by the reconcile.
func TestRunnerNoServerSourceClearsStalePill(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.status[wsA][KeyServers] = cmux.StatusEntry{Key: KeyServers, Value: "▶ dev :5173 up"}
	h.step(0)
	if _, ok := h.world.status[wsA][KeyServers]; ok {
		t.Fatal("stale server pill kept")
	}
}

func TestRunnerServerPillShowsConflict(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	f := withServers(h)
	h.world.add(winA, wsA, "a")
	f.snap = ServerSnapshot{Conflicts: []ServerObs{up("dev", 3013, wsA)}}
	h.step(0)
	if srvPill(h, wsA) != "dev :3013 conflict" {
		t.Fatalf("pill %q", srvPill(h, wsA))
	}
	for _, c := range h.world.calls {
		if strings.Contains(c, "reorder") || strings.HasPrefix(c, "place ") {
			t.Fatalf("a reorder call: %q", c)
		}
	}
}
