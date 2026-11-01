package rammon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// errProbe stands in for an lsof failure: the port state is undecidable, not
// known-free.
var errProbe = errors.New("probe failed")

// oneWorkspace is the single-server fixture the death tests build on: ws1:dev
// on port 3030, the same shape newTestMonitorRealIdle's default portPIDs and
// CPU sampler already understand.
func oneWorkspace() []pigtail.Workspace {
	return []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}
}

// deathFixture wires a monitor over oneWorkspace() whose port 3030 is busy
// while *live is true and free once it flips. Returns the monitor and the
// liveness switch, so a test reads as "observe, kill, observe".
func deathFixture(t *testing.T, cfg Config) (*RAMMonitor, *bool) {
	t.Helper()
	live := true
	st := &fakeStore{workspaces: oneWorkspace()}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, cfg, &killed)
	m.power = &fakePower{seq: []bool{false}} // mains: keep the power path out of it
	m.portPIDs = func(port int) (bool, []int, error) {
		if port == 3030 && live {
			return true, []int{100}, nil
		}
		return false, nil, nil
	}
	return m, &live
}

// TestDeathRecordedWhenServerVanishes is the core of the residual class: a
// server seen alive at sweep N-1 and gone at sweep N, with nobody announcing a
// stop, is a death — the case that used to be indistinguishable from "never
// started" because both loops just skipped the free port.
func TestDeathRecordedWhenServerVanishes(t *testing.T) {
	live := true
	st := &fakeStore{workspaces: oneWorkspace()}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, Defaults(), &killed)
	m.portPIDs = func(port int) (bool, []int, error) {
		if port == 3030 && live {
			return true, []int{100}, nil
		}
		return false, nil, nil
	}

	base := time.Now()
	m.observeActivity(base)
	live = false
	m.observeActivity(base.Add(15 * time.Second))

	deaths := m.RecentDeaths(base.Add(20 * time.Second))
	if len(deaths) != 1 || deaths[0].Server != "dev" {
		t.Fatalf("deaths = %+v, want one for ws1:dev", deaths)
	}
	if deaths[0].WorkspaceID != "ws1" || deaths[0].Port != 3030 {
		t.Errorf("death = %+v, want ws1:dev on :3030", deaths[0])
	}
	if !deaths[0].At.Equal(base.Add(15 * time.Second)) {
		t.Errorf("death.At = %s, want the sweep that noticed it (%s)",
			deaths[0].At, base.Add(15*time.Second))
	}
}

// TestDeathIsAnnouncedOnTheBus: a death is not only recorded, it is published,
// so the TUI and the menu bar can react to it as it happens.
func TestDeathIsAnnouncedOnTheBus(t *testing.T) {
	m, live := deathFixture(t, Defaults())
	sub := m.bus.Subscribe(string(pigtail.EventServerDied))
	defer sub.Close()

	base := time.Now()
	m.observeActivity(base)
	*live = false
	m.observeActivity(base.Add(15 * time.Second))

	select {
	case e := <-sub.C:
		if e.Type != pigtail.EventServerDied {
			t.Fatalf("event type = %s, want %s", e.Type, pigtail.EventServerDied)
		}
		if e.Data["workspace_id"] != "ws1" || e.Data["server"] != "dev" || e.Data["port"] != 3030 {
			t.Errorf("event payload = %+v, want ws1:dev on :3030", e.Data)
		}
		if _, ok := e.Data["at_unix"]; !ok {
			t.Errorf("event payload = %+v, want an at_unix field", e.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no resources.server_died event was published")
	}
}

// TestIntentionalStopIsNotADeath: the same disappearance, but announced first.
func TestIntentionalStopIsNotADeath(t *testing.T) {
	m, live := deathFixture(t, Defaults())

	base := time.Now()
	m.observeActivity(base)
	m.MarkIntentionalStop("ws1", "dev")
	*live = false
	m.observeActivity(base.Add(15 * time.Second))

	if len(m.RecentDeaths(base.Add(20*time.Second))) != 0 {
		t.Fatal("a deliberate stop must not be reported as a death")
	}
}

// TestIntentionalStopMarkSurvivesASlowShutdown: Next.js can take seconds to
// release its port after SIGTERM, so the mark must still be valid two sweeps
// later — otherwise every clean stop of a slow server becomes a false death.
func TestIntentionalStopMarkSurvivesASlowShutdown(t *testing.T) {
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	m, live := deathFixture(t, cfg)

	base := time.Now()
	m.observeActivity(base)
	m.MarkIntentionalStop("ws1", "dev")
	// Sweep at +15s: SIGTERM sent, port still held.
	m.observeActivity(base.Add(15 * time.Second))
	// Sweep at +30s: the port is finally free, two sweeps after the mark.
	*live = false
	m.observeActivity(base.Add(30 * time.Second))

	if len(m.RecentDeaths(base.Add(35*time.Second))) != 0 {
		t.Fatal("the intentional-stop mark expired too early")
	}
}

// TestIntentionalStopMarkExpires: the mark covers one shutdown, not the rest
// of the server's life. A server that dies long after a stop that never
// completed must still be reported.
func TestIntentionalStopMarkExpires(t *testing.T) {
	cfg := Defaults()
	cfg.Interval = 15 * time.Second // TTL = max(2×15s, 60s) = 60s
	m, live := deathFixture(t, cfg)

	base := time.Now()
	m.observeActivity(base)
	m.MarkIntentionalStop("ws1", "dev")
	// The stop never took: the server is still there well past the mark's TTL.
	m.observeActivity(base.Add(90 * time.Second))
	// Then it dies on its own.
	*live = false
	late := base.Add(105 * time.Second)
	m.observeActivity(late)

	if len(m.RecentDeaths(late)) != 1 {
		t.Fatal("an unrelated later death must still be reported")
	}
}

// TestRecentDeathsDropsOldEntries: the ring is bounded in time as well as in
// size, so a server that flaps cannot make it grow without end.
func TestRecentDeathsDropsOldEntries(t *testing.T) {
	m, live := deathFixture(t, Defaults())

	base := time.Now()
	m.observeActivity(base)
	*live = false
	m.observeActivity(base.Add(15 * time.Second))

	if len(m.RecentDeaths(base.Add(20*time.Second))) != 1 {
		t.Fatal("the death was not recorded at all")
	}
	if len(m.RecentDeaths(base.Add(31*time.Minute))) != 0 {
		t.Fatal("deaths older than the window must fall out")
	}
}

// TestRecentDeathsIsBoundedInSize: beyond maxDeaths entries inside the window,
// the oldest fall out — the ring never grows without limit.
func TestRecentDeathsIsBoundedInSize(t *testing.T) {
	m, _ := deathFixture(t, Defaults())
	base := time.Now()
	for i := 0; i < maxDeaths+10; i++ {
		m.recordDeath(Death{WorkspaceID: "ws1", Server: "dev", Port: 3030, At: base})
	}
	if got := len(m.RecentDeaths(base)); got != maxDeaths {
		t.Fatalf("kept %d deaths, want the ring capped at %d", got, maxDeaths)
	}
}

// TestStopAllMarksEveryStopIntentional: the menu bar's "Stop all" stops every
// server at once; if it did not mark them, the very next sweep would report a
// volley of false deaths and destroy any trust in the signal.
func TestStopAllMarksEveryStopIntentional(t *testing.T) {
	live := true
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
		{ID: "ws3", Path: "/tmp/ws3", Ports: map[string]int{"dev": 3050}},
	}}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, Defaults(), &killed)
	m.portPIDs = func(port int) (bool, []int, error) {
		if !live {
			return false, nil, nil
		}
		switch port {
		case 3030, 3040, 3050:
			return true, []int{port / 10}, nil
		}
		return false, nil, nil
	}

	base := time.Now()
	m.observeActivity(base)
	if stopped := m.StopAll(); len(stopped) != 3 {
		t.Fatalf("StopAll stopped %d servers, want 3", len(stopped))
	}
	live = false
	m.observeActivity(base.Add(15 * time.Second))

	if deaths := m.RecentDeaths(base.Add(20 * time.Second)); len(deaths) != 0 {
		t.Fatalf("Stop all produced %d false deaths: %+v", len(deaths), deaths)
	}
}

// TestStopServerMarksTheStopIntentional: the per-server menu-bar stop is
// deliberate too.
func TestStopServerMarksTheStopIntentional(t *testing.T) {
	m, live := deathFixture(t, Defaults())

	base := time.Now()
	m.observeActivity(base)
	if !m.StopServer("ws1", "dev") {
		t.Fatal("StopServer returned false, want true")
	}
	*live = false
	m.observeActivity(base.Add(15 * time.Second))

	if deaths := m.RecentDeaths(base.Add(20 * time.Second)); len(deaths) != 0 {
		t.Fatalf("StopServer produced a false death: %+v", deaths)
	}
}

// TestStopIdleMarksTheStopsItMakes: `pigtail stop --idle` stops servers on
// purpose; its victims must not come back as deaths.
func TestStopIdleMarksTheStopsItMakes(t *testing.T) {
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	// One-sweep window: this test is about stop --idle marking its stops, not the averaging;
	// a window equal to the interval reproduces per-sweep judgement.
	cfg.IdleWindow = cfg.Interval
	m, live := deathFixture(t, cfg)

	base := time.Now()
	clock := base
	m.now = func() time.Time { return clock }
	// Three quiet sweeps so the server is measurably idle.
	for i := 0; i < 3; i++ {
		clock = base.Add(time.Duration(i) * cfg.Interval)
		m.observeActivity(clock)
	}
	clock = base.Add(3 * cfg.Interval)
	res := m.StopIdle(30 * time.Second)
	if len(res) != 1 || !res[0].Stopped {
		t.Fatalf("StopIdle result = %+v, want ws1:dev stopped", res)
	}
	*live = false
	clock = base.Add(4 * cfg.Interval)
	m.observeActivity(clock)

	if deaths := m.RecentDeaths(clock); len(deaths) != 0 {
		t.Fatalf("stop --idle produced a false death: %+v", deaths)
	}
}

// TestIdlePurgeMarksItsEvictions: the always-on idle purge is a daemon stop
// path like any other — it must mark, or every purge becomes a false death.
func TestIdlePurgeMarksItsEvictions(t *testing.T) {
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	// One-sweep window: this test is about the purge marking its eviction, not the averaging;
	// a window equal to the interval reproduces per-sweep judgement.
	cfg.IdleWindow = cfg.Interval
	cfg.IdlePurgeGrace = 30 * time.Second
	m, live := deathFixture(t, cfg)

	base := time.Now()
	clock := base
	m.now = func() time.Time { return clock }
	for i := 0; i < 3; i++ {
		clock = base.Add(time.Duration(i) * cfg.Interval)
		m.observeActivity(clock)
	}
	clock = base.Add(3 * cfg.Interval)
	m.purgeIdle(context.Background(), 32<<30, m.snapshot())
	*live = false
	clock = base.Add(4 * cfg.Interval)
	m.observeActivity(clock)

	if deaths := m.RecentDeaths(clock); len(deaths) != 0 {
		t.Fatalf("the idle purge produced a false death: %+v", deaths)
	}
}

// TestCapServersMarksItsEvictions: same for the server-count cap.
func TestCapServersMarksItsEvictions(t *testing.T) {
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	// One-sweep window: this test is about the cap marking its eviction, not the averaging;
	// a window equal to the interval reproduces per-sweep judgement.
	cfg.IdleWindow = cfg.Interval
	cfg.MaxServers = 1
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
	}}
	var killed [][]int
	gone := map[int]bool{}
	base := time.Now()
	clock := base
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, cfg, &killed,
		WithClock(func() time.Time { return clock }))
	m.portPIDs = func(port int) (bool, []int, error) {
		if gone[port] {
			return false, nil, nil
		}
		switch port {
		case 3030, 3040:
			return true, []int{port / 10}, nil
		}
		return false, nil, nil
	}
	// Enough quiet sweeps to clear capMinIdle (5 min).
	for i := 0; i < 25; i++ {
		clock = base.Add(time.Duration(i) * cfg.Interval)
		m.observeActivity(clock)
	}
	clock = base.Add(25 * cfg.Interval)
	m.capServers(context.Background(), 32<<30, m.snapshot())
	if len(killed) != 1 {
		t.Fatalf("the cap evicted %d servers, want exactly 1 (fixture no longer exercises capServers)", len(killed))
	}
	// Whichever of the two the cap picked is the one that now disappears
	// (pids are port/10 in this fixture).
	gone[killed[0][0]*10] = true
	clock = base.Add(26 * cfg.Interval)
	m.observeActivity(clock)

	if deaths := m.RecentDeaths(clock); len(deaths) != 0 {
		t.Fatalf("the server cap produced a false death: %+v", deaths)
	}
}

// TestSweepEvictionMarksItsVictim: the mains memory-pressure eviction path.
func TestSweepEvictionMarksItsVictim(t *testing.T) {
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	// One-sweep window: this test is about the floor eviction marking its victim, not the averaging;
	// a window equal to the interval reproduces per-sweep judgement.
	cfg.IdleWindow = cfg.Interval
	cfg.ConfirmWindows = 1
	cfg.FloorBytes = 1 << 40 // always under the floor
	cfg.Grace = time.Second
	m, live := deathFixture(t, cfg)

	base := time.Now()
	clock := base
	m.now = func() time.Time { return clock }
	for i := 0; i < 3; i++ {
		clock = base.Add(time.Duration(i) * cfg.Interval)
		m.observeActivity(clock)
	}
	clock = base.Add(3 * cfg.Interval)
	m.Sweep(context.Background())
	*live = false
	clock = base.Add(4 * cfg.Interval)
	m.observeActivity(clock)

	if deaths := m.RecentDeaths(clock); len(deaths) != 0 {
		t.Fatalf("the memory-pressure eviction produced a false death: %+v", deaths)
	}
}

// TestBatteryEvictionMarksItsVictim: the battery path evicts silently and on
// its own schedule — it must mark too, or unplugging the laptop would turn
// every power-saving eviction into a reported death.
func TestBatteryEvictionMarksItsVictim(t *testing.T) {
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	// One-sweep window: this test is about the battery eviction marking its victim, not the averaging;
	// a window equal to the interval reproduces per-sweep judgement.
	cfg.IdleWindow = cfg.Interval
	cfg.ConfirmWindows = 1
	cfg.BatteryGrace = 30 * time.Second
	m, live := deathFixture(t, cfg)
	m.power = &fakePower{seq: []bool{true}} // on battery

	base := time.Now()
	clock := base
	m.now = func() time.Time { return clock }
	for i := 0; i < 3; i++ {
		clock = base.Add(time.Duration(i) * cfg.Interval)
		m.observeActivity(clock)
	}
	clock = base.Add(3 * cfg.Interval)
	m.Sweep(context.Background())
	*live = false
	clock = base.Add(4 * cfg.Interval)
	m.observeActivity(clock)

	if deaths := m.RecentDeaths(clock); len(deaths) != 0 {
		t.Fatalf("the battery eviction produced a false death: %+v", deaths)
	}
}

// TestUnknownPortStateIsNotADeath: `lsof` failing is not evidence a server
// died. Reporting on a probe error would flood the signal with noise the
// moment the machine is under load — exactly what would make it useless.
func TestUnknownPortStateIsNotADeath(t *testing.T) {
	probeErr := false
	st := &fakeStore{workspaces: oneWorkspace()}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, Defaults(), &killed)
	m.portPIDs = func(port int) (bool, []int, error) {
		if probeErr {
			return false, nil, errProbe
		}
		if port == 3030 {
			return true, []int{100}, nil
		}
		return false, nil, nil
	}

	base := time.Now()
	m.observeActivity(base)
	probeErr = true
	m.observeActivity(base.Add(15 * time.Second))

	if deaths := m.RecentDeaths(base.Add(20 * time.Second)); len(deaths) != 0 {
		t.Fatalf("a port-probe error was reported as a death: %+v", deaths)
	}
	// And the server is still tracked, so a real death is still detectable.
	if keys := m.activity.keys(); len(keys) != 1 {
		t.Fatalf("tracker keys = %v, want the undecided server kept", keys)
	}
}

// TestVanishedWorkspaceIsNotADeath: a workspace the daemon no longer knows
// about (cleaned up, removed) takes its servers with it. That is bookkeeping,
// not a server dying on its own.
func TestVanishedWorkspaceIsNotADeath(t *testing.T) {
	st := &fakeStore{workspaces: oneWorkspace()}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, Defaults(), &killed)

	base := time.Now()
	m.observeActivity(base)
	st.workspaces = nil
	m.observeActivity(base.Add(15 * time.Second))

	if deaths := m.RecentDeaths(base.Add(20 * time.Second)); len(deaths) != 0 {
		t.Fatalf("a removed workspace was reported as a death: %+v", deaths)
	}
	if keys := m.activity.keys(); len(keys) != 0 {
		t.Fatalf("tracker keys = %v, want the gone workspace forgotten", keys)
	}
}

// TestRestartUnderNewPIDsIsNotADeath: the process-identity guard rebases an
// entry whose listeners changed, which must stay a bookkeeping operation — the
// server never stopped listening, so nothing died. Its control is in the same
// test: once that same restarted server really vanishes, it is still reported.
func TestRestartUnderNewPIDsIsNotADeath(t *testing.T) {
	st := &fakeStore{workspaces: oneWorkspace()}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, Defaults(), &killed)
	pids := []int{100}
	live := true
	m.portPIDs = func(port int) (bool, []int, error) {
		if port == 3030 && live {
			return true, pids, nil
		}
		return false, nil, nil
	}

	base := time.Now()
	m.observeActivity(base)
	pids = []int{999} // restarted between the two sweeps
	m.observeActivity(base.Add(15 * time.Second))

	if deaths := m.RecentDeaths(base.Add(20 * time.Second)); len(deaths) != 0 {
		t.Fatalf("a restart was reported as a death: %+v", deaths)
	}

	live = false
	m.observeActivity(base.Add(30 * time.Second))
	deaths := m.RecentDeaths(base.Add(35 * time.Second))
	if len(deaths) != 1 || deaths[0].Server != "dev" || deaths[0].Port != 3030 {
		t.Fatalf("control: the real death after the restart must still be reported, got %+v", deaths)
	}
}

// TestIntentionalStopTTLIsAtLeastAMinute pins the rule: 60s or two sweep
// intervals, whichever is longer.
func TestIntentionalStopTTLIsAtLeastAMinute(t *testing.T) {
	cases := []struct {
		interval time.Duration
		want     time.Duration
	}{
		{15 * time.Second, time.Minute},      // 2×15s = 30s → floored at 60s
		{30 * time.Second, time.Minute},      // 2×30s = 60s → not longer than 60s
		{45 * time.Second, 90 * time.Second}, // 2×45s wins
	}
	for _, tc := range cases {
		cfg := Defaults()
		cfg.Interval = tc.interval
		m, _ := deathFixture(t, cfg)
		if got := m.intentionalStopTTL(); got != tc.want {
			t.Errorf("interval=%s → TTL=%s, want %s", tc.interval, got, tc.want)
		}
	}
}

// TestLastSweepAt: the time of the last observation that judged deaths, so a
// consumer can tell "gone, not judged yet" from "gone, judged a stop".
func TestLastSweepAt(t *testing.T) {
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	m, _ := deathFixture(t, cfg)
	if !m.LastSweepAt().IsZero() {
		t.Fatalf("swept before any sweep: %v", m.LastSweepAt())
	}
	at := time.Now().Add(time.Minute)
	if !m.observeActivity(at) {
		t.Fatal("fixture observation failed")
	}
	if got := m.LastSweepAt(); !got.Equal(at) {
		t.Fatalf("LastSweepAt %v, want %v", got, at)
	}
}
