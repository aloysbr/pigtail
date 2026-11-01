package rammon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/proccpu"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fixedCPU reports the same cumulative CPU for every process, so a sweep folds
// a known figure into the tracker without racing a real sampler.
type fixedCPU struct{ secs float64 }

func (f fixedCPU) Sample() (map[int]proccpu.ProcStat, error) {
	return map[int]proccpu.ProcStat{
		100: {PID: 100, CPUSeconds: f.secs},
		200: {PID: 200, CPUSeconds: f.secs},
		999: {PID: 999, CPUSeconds: f.secs},
	}, nil
}

// restartedMonitor is the crux of this whole change: a second daemon reading
// what the first one left behind.
func restartedMonitor(t *testing.T, path string, now time.Time, portPIDs func(int) (bool, []int, error)) *RAMMonitor {
	t.Helper()
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 32 << 30}, cfg, &[][]int{},
		WithClock(func() time.Time { return now }),
		WithCPUSampler(fixedCPU{secs: 10}),
		WithStatePath(path),
	)
	if portPIDs != nil {
		WithPortPIDs(portPIDs)(m)
	}
	// newTestMonitor injects a fixed idle seam; these tests are about the
	// tracker itself, so restore the production wiring that New() would have
	// installed had no option supplied one.
	m.idleFor = func(ws pigtail.Workspace, server string) time.Duration {
		return m.activity.idleFor(activityKey(ws.ID, server), m.now())
	}
	m.LoadState()
	return m
}

// A routine redeploy must stop resetting every idle counter to a few seconds —
// the symptom observed live on 2026-07-28.
func TestIdleSurvivesADaemonRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	first := restartedMonitor(t, path, start, nil)
	first.activity.restore(map[string]activityEntry{
		"ws1:dev": {lastCPU: 10, lastActive: start.Add(-2 * time.Hour), port: 3030, pids: []int{100}},
	})
	if err := first.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	second := restartedMonitor(t, path, start.Add(5*time.Second), nil)
	if got := second.activity.idleFor("ws1:dev", start.Add(5*time.Second)); got < 2*time.Hour {
		t.Fatalf("idle after restart = %s, want the ~2h it had before", got)
	}
	if second.activity.confirmed("ws1:dev") {
		t.Fatal("a freshly restored entry must not be confirmed before a sweep")
	}
}

// The eviction guarantee the silent-death design bought: a restart may not
// evict. Restored idle is reported, and cannot be acted on.
func TestRestoredIdleIsReportedButNotEvictable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	first := restartedMonitor(t, path, start, nil)
	first.activity.restore(map[string]activityEntry{
		"ws1:dev": {lastCPU: 10, lastActive: start.Add(-2 * time.Hour), port: 3030, pids: []int{100}},
	})
	if err := first.SaveState(); err != nil {
		t.Fatal(err)
	}

	second := restartedMonitor(t, path, start.Add(5*time.Second), nil)
	var found bool
	for _, s := range second.snapshot() {
		if s.WorkspaceID != "ws1" || s.Server != "dev" {
			continue
		}
		found = true
		if s.IdleFor < 2*time.Hour {
			t.Fatalf("IdleFor = %s, want the restored ~2h to be reported", s.IdleFor)
		}
		if s.IdleConfirmed {
			t.Fatal("IdleConfirmed must be false until this daemon observes the server")
		}
	}
	if !found {
		t.Fatal("ws1:dev missing from the snapshot")
	}
}

// A server restarted while the daemon was down comes back under new PIDs. Its
// cumulative CPU counter restarted with it, so the delta reads negative — which
// isActive cannot distinguish from "went quiet". Without the PID guard the
// restored 2h idle would survive and the freshly started server would be first
// in line for eviction.
func TestServerRestartedUnderNewPIDsLosesItsRestoredIdle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	first := restartedMonitor(t, path, start, nil)
	first.activity.restore(map[string]activityEntry{
		"ws1:dev": {lastCPU: 500, lastActive: start.Add(-2 * time.Hour), port: 3030, pids: []int{100}},
	})
	if err := first.SaveState(); err != nil {
		t.Fatal(err)
	}

	// Port 3030 is busy again, but with pid 999 — a different process.
	newPIDs := func(port int) (bool, []int, error) {
		switch port {
		case 3030:
			return true, []int{999}, nil
		case 3040:
			return true, []int{200}, nil
		}
		return false, nil, nil
	}
	now := start.Add(5 * time.Second)
	second := restartedMonitor(t, path, now, newPIDs)
	second.observeActivity(now)

	if got := second.activity.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle after a PID change = %s, want 0 (treated as a first sighting)", got)
	}
	if !second.activity.confirmed("ws1:dev") {
		t.Fatal("the entry was observed, so it must be confirmed")
	}
}

// The other half of the item: a server that dies around the restart used to be
// invisible, because nothing remembered it had ever existed.
func TestDeathAcrossARestartIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	first := restartedMonitor(t, path, start, nil)
	first.activity.restore(map[string]activityEntry{
		"ws2:dev": {lastCPU: 10, lastActive: start, port: 3040, pids: []int{200}},
	})
	if err := first.SaveState(); err != nil {
		t.Fatal(err)
	}

	// ws2's port is free now: it went away while no daemon was watching.
	gone := func(port int) (bool, []int, error) {
		if port == 3030 {
			return true, []int{100}, nil
		}
		return false, nil, nil
	}
	now := start.Add(5 * time.Second)
	second := restartedMonitor(t, path, now, gone)
	second.observeActivity(now)

	deaths := second.RecentDeaths(now)
	if len(deaths) != 1 || deaths[0].WorkspaceID != "ws2" || deaths[0].Port != 3040 {
		t.Fatalf("RecentDeaths = %+v, want one ws2:dev on :3040", deaths)
	}
}

// A machine reboot leaves every persisted port free too, but those servers did
// not die — they were never restarted. Reporting them would put a volley of
// false crosses in `pigtail servers` after every boot.
func TestStaleStateDoesNotReportDeaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	first := restartedMonitor(t, path, start, nil)
	first.activity.restore(map[string]activityEntry{
		"ws2:dev": {lastCPU: 10, lastActive: start, port: 3040, pids: []int{200}},
	})
	if err := first.SaveState(); err != nil {
		t.Fatal(err)
	}

	gone := func(port int) (bool, []int, error) {
		if port == 3030 {
			return true, []int{100}, nil
		}
		return false, nil, nil
	}
	// Well past deathRestoreWindow: this looks like a reboot, not a redeploy.
	now := start.Add(deathRestoreWindow + time.Minute)
	second := restartedMonitor(t, path, now, gone)
	second.observeActivity(now)

	if deaths := second.RecentDeaths(now); len(deaths) != 0 {
		t.Fatalf("RecentDeaths = %+v, want none from a stale state file", deaths)
	}
}

// The identity guard can only match on PIDs that were written down. They come
// from the sweep, so an entry the daemon observed itself must carry them into
// the state file — otherwise every restored entry mismatches on restore and the
// persisted idle is dropped on the first sweep, which is exactly the continuity
// this file exists to buy.
func TestObservedPIDsArePersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	m := restartedMonitor(t, path, start, nil) // default probe: 3030→100, 3040→200
	m.observeActivity(start)
	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	s, err := readStateFile(path)
	if err != nil {
		t.Fatalf("readStateFile: %v", err)
	}
	var found bool
	for _, e := range s.Entries {
		if e.Key != "ws1:dev" {
			continue
		}
		found = true
		if len(e.PIDs) != 1 || e.PIDs[0] != 100 {
			t.Fatalf("persisted PIDs = %v, want [100] — the guard has nothing to compare without them", e.PIDs)
		}
	}
	if !found {
		t.Fatal("ws1:dev missing from the persisted entries")
	}
}

// Same server, same process, across a daemon restart: the restored idle must
// survive the first observation. This is the round trip the previous test
// enables — with no PIDs recorded at observation time, the identity guard
// mismatched on every restored entry and reset the counter it had just read.
func TestRestoredIdleSurvivesAnObservationOfTheSameProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	first := restartedMonitor(t, path, start.Add(-2*time.Hour), nil)
	first.observeActivity(start.Add(-2 * time.Hour))
	if err := first.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	second := restartedMonitor(t, path, start, nil) // same probe: still pid 100
	second.observeActivity(start)

	if got := second.activity.idleFor("ws1:dev", start); got != 2*time.Hour {
		t.Fatalf("idle after observing the same process = %s, want the restored 2h", got)
	}
}

// Persistence off (no path) must behave exactly as before: no file, no restore.
func TestNoStatePathIsANoOp(t *testing.T) {
	m := restartedMonitor(t, "", time.Now(), nil)
	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState with no path must be a silent no-op, got %v", err)
	}
}

// A file written by the previous version has no samples. It loads (same
// stateVersion), keeps its restored idle, and gets one seeded sample at
// SavedAt so the restart gap itself is measured.
func TestLegacyStateFileSeedsOneSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"version":1,"saved_at":"2026-10-01T12:00:00Z","entries":[{"key":"ws1:dev","last_cpu":10,"last_active":"2026-10-01T10:00:00Z","port":3030,"pids":[100]}],"deaths":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	m := restartedMonitor(t, path, saved.Add(5*time.Second), nil)
	s := m.activity.entriesSnapshot()["ws1:dev"].samples
	if len(s) != 1 || !s[0].at.Equal(saved) || s[0].cpu != 10 {
		t.Fatalf("legacy entry samples = %+v, want one seed {SavedAt, LastCPU}", s)
	}
	m.observeActivity(saved.Add(5 * time.Second)) // fixedCPU: 10, quiet
	if got := m.activity.idleFor("ws1:dev", saved.Add(5*time.Second)); got < 2*time.Hour {
		t.Fatalf("restored idle after the upgrade = %s, want the ~2h it had", got)
	}
}

// A redeploy must continue the window: a burst a minute before the restart is
// still use a few seconds after it.
func TestRestartKeepsARecentBurstInTheWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cpu := 10.0
	first := restartedMonitor(t, path, start, nil)
	WithCPUSampler(rampCPUSampler{cpu: &cpu, step: 0})(first)
	for i := 0; i <= 30; i++ { // 0..480 s quiet: full window
		first.observeActivity(start.Add(time.Duration(16*i) * time.Second))
	}
	cpu += 9 // burst at 496 s
	first.observeActivity(start.Add(496 * time.Second))
	for i := 32; i <= 34; i++ { // quiet to 544 s
		first.observeActivity(start.Add(time.Duration(16*i) * time.Second))
	}
	if err := first.SaveState(); err != nil {
		t.Fatal(err)
	}
	now := start.Add(549 * time.Second)
	second := restartedMonitor(t, path, now, nil)
	WithCPUSampler(rampCPUSampler{cpu: &cpu, step: 0})(second)
	second.observeActivity(now)
	if got := second.activity.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle 53 s after a burst, across a redeploy = %s, want 0", got)
	}
}

// A tab reloaded while pigtaild restarts: the burst is seen against the
// persisted peer set.
func TestConnectionOpenedDuringARestartIsActivity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	remotes := endpoints(50001, navigationBurst)
	acc := WithAccepted(func() (map[int][]devserver.Conn, error) {
		var cs []devserver.Conn
		for _, r := range remotes {
			cs = append(cs, devserver.Conn{PID: 100, Remote: r})
		}
		return map[int][]devserver.Conn{3030: cs}, nil
	})
	first := restartedMonitor(t, path, start, nil)
	first.cfg.NetSignal = true // the network signal is opt-in (rule D8)
	acc(first)
	for i := 0; i <= 30; i++ {
		first.observeActivity(start.Add(time.Duration(16*i) * time.Second))
	}
	if err := first.SaveState(); err != nil {
		t.Fatal(err)
	}
	remotes = endpoints(51001, navigationBurst) // reloaded during the restart
	now := start.Add(485 * time.Second)
	second := restartedMonitor(t, path, now, nil)
	second.cfg.NetSignal = true
	acc(second)
	second.observeActivity(now)
	if got := second.activity.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle after a reload during the restart = %s, want 0", got)
	}
}

// A redeploy 37 s after a server was first seen: the fill continues.
func TestRestartMidFillKeepsFilling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := restartedMonitor(t, path, start, nil)
	first.observeActivity(start)
	first.observeActivity(start.Add(16 * time.Second))
	first.observeActivity(start.Add(32 * time.Second))
	if err := first.SaveState(); err != nil {
		t.Fatal(err)
	}
	now := start.Add(37 * time.Second)
	second := restartedMonitor(t, path, now, nil)
	second.observeActivity(now)
	if got := second.activity.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle 37 s after first sighting, across a redeploy = %s, want 0 (still filling)", got)
	}
}
