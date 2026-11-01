package rammon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// D9: the RAM floor keeps priority over every pin. On mains, under the floor,
// a durably pinned idle server is still the victim — exactly as a timed pin
// already is (the floor path never filtered pins). This test exists so a
// future "pinned means never killed" shortcut fails loudly.
func TestFloorEvictsADurablyPinnedServer(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()} // ws1:dev idle 30m → pid 100
	// BatteryEnabled false (zero value) keeps the power source out of it.
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 1}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1}, cfg, &killed)

	if !m.PinUntilUnpinned("ws1", "dev") {
		t.Fatal("PinUntilUnpinned ws1:dev = false, want true (server is running)")
	}
	m.Sweep(context.Background())

	if len(killed) != 1 || len(killed[0]) != 1 || killed[0][0] != 100 {
		t.Fatalf("under the floor a durable pin must not protect: killed %v, want [[100]]", killed)
	}
}

// Every other eviction path spares a durable pin: idle purge, cap, battery,
// stop --idle.
func TestDurablePinSparesNonFloorEvictions(t *testing.T) {
	t.Run("idle purge", func(t *testing.T) {
		cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
			Interval: time.Second, ConfirmWindows: 1, IdlePurgeGrace: 10 * time.Minute}
		var killed [][]int
		m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 30}, cfg, &killed)
		m.PinUntilUnpinned("ws1", "dev")
		m.Sweep(context.Background())
		if len(killed) != 0 {
			t.Fatalf("durably pinned server purged: %v", killed)
		}
	})
	t.Run("cap", func(t *testing.T) {
		cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
			Interval: time.Second, ConfirmWindows: 1, MaxServers: 1}
		var killed [][]int
		m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 30}, cfg, &killed)
		m.PinUntilUnpinned("ws1", "dev") // ws1 is the only one idle past capMinIdle
		m.Sweep(context.Background())
		if len(killed) != 0 {
			t.Fatalf("durably pinned server evicted by the cap: %v", killed)
		}
	})
	t.Run("battery", func(t *testing.T) {
		cfg := Defaults()
		cfg.ConfirmWindows = 1
		var killed [][]int
		m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 40}, cfg, &killed)
		m.power = &fakePower{seq: []bool{true}}
		m.onBattery = true
		m.PinUntilUnpinned("ws1", "dev")
		m.Sweep(context.Background())
		if len(killed) != 0 {
			t.Fatalf("durably pinned server battery-evicted: %v", killed)
		}
	})
	t.Run("stop --idle", func(t *testing.T) {
		var killed [][]int
		m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 40}, Defaults(), &killed)
		m.PinUntilUnpinned("ws1", "dev")
		for _, r := range m.StopIdle(10 * time.Minute) {
			if r.WorkspaceID == "ws1" && (r.Stopped || r.Reason != "pinned") {
				t.Fatalf("ws1:dev = %+v, want kept with reason pinned", r)
			}
		}
	})
}

// A durable pin does not expire, however far the clock moves.
func TestDurablePinNeverExpires(t *testing.T) {
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 40}, Defaults(), &[][]int{})
	s := ServerStat{WorkspaceID: "ws1", Server: "dev"}
	m.snoozeDurable(s)
	if !m.isSnoozed(s, time.Now().Add(10*365*24*time.Hour)) {
		t.Fatal("durable pin expired")
	}
	until, durable := m.pinState(s)
	if !durable || !until.IsZero() {
		t.Fatalf("pinState = (%v, %v), want (zero, true)", until, durable)
	}
}

// Last choice wins: a timed pin replaces a durable one, and Unpin clears both.
func TestTimedPinReplacesDurable(t *testing.T) {
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 40}, Defaults(), &[][]int{})
	s := ServerStat{WorkspaceID: "ws1", Server: "dev"}
	m.PinUntilUnpinned("ws1", "dev")
	if !m.Pin("ws1", "dev", time.Hour) {
		t.Fatal("Pin 1h = false")
	}
	until, durable := m.pinState(s)
	if durable || until.IsZero() {
		t.Fatalf("after a 1h pin: pinState = (%v, %v), want (expiry, false)", until, durable)
	}
	m.PinUntilUnpinned("ws1", "dev")
	m.Unpin("ws1", "dev")
	if m.isSnoozed(s, time.Now()) {
		t.Fatal("Unpin left a durable pin behind")
	}
}

// A durable pin on a server that is not running is refused, like a timed one:
// the route answers 404 rather than pinning a typo.
func TestPinUntilUnpinnedUnknownServer(t *testing.T) {
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 40}, Defaults(), &[][]int{})
	if m.PinUntilUnpinned("ws-nope", "dev") {
		t.Fatal("PinUntilUnpinned of an unknown server = true, want false")
	}
}

// snapshot stamps the durable flag so /servers can report it.
func TestSnapshotCarriesDurablePin(t *testing.T) {
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 40}, Defaults(), &[][]int{})
	m.PinUntilUnpinned("ws1", "dev")
	for _, s := range m.snapshot() {
		if s.WorkspaceID == "ws1" && (!s.PinDurable || !s.PinnedUntil.IsZero()) {
			t.Fatalf("ws1:dev stat = durable %v until %v, want durable true, zero until", s.PinDurable, s.PinnedUntil)
		}
		if s.WorkspaceID == "ws2" && s.PinDurable {
			t.Fatal("ws2:dev reported durably pinned")
		}
	}
}

// D9's core promise: a durable pin survives a daemon restart.
func TestDurablePinSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	first := restartedMonitor(t, path, start, nil)
	if !first.PinUntilUnpinned("ws1", "dev") {
		t.Fatal("PinUntilUnpinned = false")
	}
	// No SaveState call here on purpose: the pin itself must have saved.

	second := restartedMonitor(t, path, start.Add(time.Hour), nil)
	if _, durable := second.pinState(ServerStat{WorkspaceID: "ws1", Server: "dev"}); !durable {
		t.Fatal("durable pin lost across a restart")
	}
}

// A timed pin survives too, with its expiry; an expired one does not come back.
func TestTimedPinSurvivesRestartUntilItExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	first := restartedMonitor(t, path, start, nil)
	first.snooze(ServerStat{WorkspaceID: "ws1", Server: "dev"}, start.Add(time.Hour))
	if err := first.SaveState(); err != nil {
		t.Fatal(err)
	}

	soon := restartedMonitor(t, path, start.Add(10*time.Minute), nil)
	if until, _ := soon.pinState(ServerStat{WorkspaceID: "ws1", Server: "dev"}); !until.Equal(start.Add(time.Hour)) {
		t.Fatalf("restored expiry = %v, want %v", until, start.Add(time.Hour))
	}
	late := restartedMonitor(t, path, start.Add(2*time.Hour), nil)
	if until, durable := late.pinState(ServerStat{WorkspaceID: "ws1", Server: "dev"}); durable || !until.IsZero() {
		t.Fatalf("an expired timed pin was resurrected: (%v, %v)", until, durable)
	}
}

// Review focus 2: restarted while the pinned server is down, the pin waits
// for it — the identity is workspace:server, not a live port.
func TestDurablePinSurvivesRestartWhileServerIsDown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	first := restartedMonitor(t, path, start, nil)
	first.PinUntilUnpinned("ws1", "dev")

	allFree := func(int) (bool, []int, error) { return false, nil, nil }
	second := restartedMonitor(t, path, start.Add(time.Minute), allFree)
	second.power = &fakePower{seq: []bool{false}} // mains: keep the real power source out of it
	second.Sweep(context.Background())            // ws1 still exists, its server is down
	if _, durable := second.pinState(ServerStat{WorkspaceID: "ws1", Server: "dev"}); !durable {
		t.Fatal("a sweep with the server down dropped its durable pin")
	}
}

// Review focus 3: a pin whose workspace is gone (row deleted, or worktree
// removed from disk) is pruned at the next sweep, and the prune is saved.
func TestPinOfAGoneWorkspaceIsPruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var killed [][]int
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()[1:]}, fakeMem{avail: 1 << 40}, Defaults(), &killed,
		WithCPUSampler(fixedCPU{secs: 10}), WithStatePath(path))
	m.snoozeDurable(ServerStat{WorkspaceID: "ws1", Server: "dev"}) // ws1 no longer in the store
	m.snoozeDurable(ServerStat{WorkspaceID: "ws2", Server: "dev"})
	m.power = &fakePower{seq: []bool{false}} // mains: keep the real power source out of it

	m.Sweep(context.Background())

	if _, durable := m.pinState(ServerStat{WorkspaceID: "ws1", Server: "dev"}); durable {
		t.Fatal("pin of a gone workspace kept")
	}
	if _, durable := m.pinState(ServerStat{WorkspaceID: "ws2", Server: "dev"}); !durable {
		t.Fatal("pin of a live workspace pruned")
	}
}

// Review focus 4: an old state file (no pins) loads with none; a keyless or
// expired entry is skipped, never resurrected or crashed on.
func TestLoadStateWithoutPinsKeepsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(path, []byte(`{"version":1,"saved_at":"2026-10-02T11:59:00Z","entries":[],"deaths":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := restartedMonitor(t, path, now, nil)
	m.mu.Lock()
	n := len(m.pins)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d pins from a file without pins", n)
	}
}

func TestLoadStateSkipsExpiredAndKeylessPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := writeStateFile(path, persistedState{Version: stateVersion, SavedAt: now, Pins: []persistedPin{
		{Key: "", Durable: true},
		{Key: "ws1:dev", Until: now.Add(-time.Minute)},
		{Key: "ws2:dev", Durable: true},
	}}); err != nil {
		t.Fatal(err)
	}
	m := restartedMonitor(t, path, now, nil)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pins) != 1 || !m.pins["ws2:dev"].durable {
		t.Fatalf("restored pins = %+v, want only ws2:dev durable", m.pins)
	}
}

// Unpin is saved at once too: a restart right after it must not resurrect
// the pin.
func TestUnpinIsSavedImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	first := restartedMonitor(t, path, start, nil)
	first.PinUntilUnpinned("ws1", "dev")
	first.Unpin("ws1", "dev")

	second := restartedMonitor(t, path, start.Add(time.Second), nil)
	if second.isSnoozed(ServerStat{WorkspaceID: "ws1", Server: "dev"}, start.Add(time.Second)) {
		t.Fatal("an unpinned server came back pinned after a restart")
	}
}

// T6 reverse race: a sweep save that read the pins before an Unpin must not
// land after the Unpin's own save and resurrect the pin on disk.
func TestUnpinIsNotUndoneByAnInFlightSweepSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := restartedMonitor(t, path, start, nil)
	if !m.PinUntilUnpinned("ws1", "dev") {
		t.Fatal("PinUntilUnpinned = false")
	}

	entered, release, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls int32
	m.writeState = func(p string, s persistedState) error {
		switch atomic.AddInt32(&calls, 1) {
		case 1:
			close(entered) // the sweep's save has read the pins (pin present)
			<-release
		case 2:
			// The Unpin's save. Without serialization it gets here while
			// the sweep's save is parked, and lands first.
			err := writeStateFile(p, s)
			close(secondDone)
			return err
		}
		return writeStateFile(p, s)
	}
	saved := make(chan error, 1)
	go func() { saved <- m.SaveState() }()
	<-entered

	unpinned := make(chan struct{})
	go func() { m.Unpin("ws1", "dev"); close(unpinned) }()
	for m.isSnoozed(ServerStat{WorkspaceID: "ws1", Server: "dev"}, start) {
		runtime.Gosched() // wait until the Unpin has cleared memory
	}
	// Without the mutex the Unpin's save completes here (secondDone), before
	// the stale one is released: deterministic red. With it, the Unpin's save
	// is blocked behind the parked one, so this times out and the test goes on.
	select {
	case <-secondDone:
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	<-unpinned

	second := restartedMonitor(t, path, start.Add(time.Second), nil)
	if second.isSnoozed(ServerStat{WorkspaceID: "ws1", Server: "dev"}, start.Add(time.Second)) {
		t.Fatal("a stale sweep save resurrected an unpinned server across a restart")
	}
}

// batteryUnderFloor: on battery (confirmed), 1 byte available, ws1:dev idle
// 30m (pid 100), ws2:dev idle 1m (pid 200). Defaults: floor 6 GiB, grace and
// battery grace 10m.
func batteryUnderFloor(t *testing.T, confirm int, killed *[][]int) *RAMMonitor {
	t.Helper()
	cfg := Defaults()
	cfg.ConfirmWindows = confirm
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1}, cfg, killed)
	m.power = &fakePower{seq: []bool{true}}
	m.onBattery = true
	return m
}

// D11: under the floor on battery, a pinned server is evictable — timed or
// durable. Red on base: the battery branch filters pins out and never checks
// the floor (the timed sub-test compiles on 34fb3eb and fails there on
// behaviour).
func TestBatteryFloorEvictsAPinnedServer(t *testing.T) {
	pins := map[string]func(m *RAMMonitor){
		"timed": func(m *RAMMonitor) {
			m.snooze(ServerStat{WorkspaceID: "ws1", Server: "dev"}, time.Now().Add(time.Hour))
		},
		"durable": func(m *RAMMonitor) { m.PinUntilUnpinned("ws1", "dev") },
	}
	for name, pin := range pins {
		t.Run(name, func(t *testing.T) {
			var killed [][]int
			m := batteryUnderFloor(t, 1, &killed)
			pin(m)
			m.Sweep(context.Background())
			if len(killed) != 1 || len(killed[0]) != 1 || killed[0][0] != 100 {
				t.Fatalf("battery + under floor + pinned ws1: killed %v, want [[100]]", killed)
			}
		})
	}
}

// Same confirmation as on mains: one under-floor sweep is not enough with
// ConfirmWindows=2.
func TestBatteryFloorWaitsForConfirmWindows(t *testing.T) {
	var killed [][]int
	m := batteryUnderFloor(t, 2, &killed)
	m.PinUntilUnpinned("ws1", "dev")
	m.Sweep(context.Background())
	if len(killed) != 0 {
		t.Fatalf("first under-floor sweep killed %v, want none (ConfirmWindows=2)", killed)
	}
	m.Sweep(context.Background())
	if len(killed) != 1 || killed[0][0] != 100 {
		t.Fatalf("second under-floor sweep killed %v, want [[100]]", killed)
	}
}

// Order guard: when an unpinned server is a battery victim, the battery
// eviction takes it and the floor does not also act on the same stale
// reading — exactly one kill per sweep. (Green on base too: a non-regression
// guard for the battery path.)
func TestBatteryEvictionRunsBeforeTheFloorAndAlone(t *testing.T) {
	var killed [][]int
	m := batteryUnderFloor(t, 1, &killed)
	m.Sweep(context.Background())
	if len(killed) != 1 || killed[0][0] != 100 {
		t.Fatalf("battery + under floor, nothing pinned: killed %v, want exactly [[100]]", killed)
	}
}

// varMem is a memory sampler whose figure the test moves between sweeps.
type varMem struct{ avail uint64 }

func (v *varMem) available() (uint64, error)      { return v.avail, nil }
func (v *varMem) procs() (map[int]memStat, error) { return map[int]memStat{}, nil }

// A: under the floor, only a pinned server is idle → counter 1 (of 2).
// B: above the floor, an unpinned server is a battery victim → evicted; the
// reading is valid, so the under-floor streak is broken.
// C: under the floor again → counter 1, not 2: the pinned server survives.
// Red on the plan before this fix: B returned without resetting, so C made 2.
func TestBatteryEvictionAboveTheFloorBreaksTheFloorStreak(t *testing.T) {
	cfg := Defaults() // floor 6 GiB, grace and battery grace 10m
	cfg.ConfirmWindows = 2
	mem := &varMem{}
	ws2Idle := time.Minute
	var killed [][]int
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, mem, cfg, &killed,
		WithIdleFunc(func(ws pigtail.Workspace, _ string) time.Duration {
			if ws.ID == "ws1" {
				return 30 * time.Minute // pid 100, pinned below
			}
			return ws2Idle // pid 200
		}))
	m.power = &fakePower{seq: []bool{true}}
	m.onBattery = true
	m.PinUntilUnpinned("ws1", "dev")

	mem.avail = 1 // A
	m.Sweep(context.Background())

	mem.avail, ws2Idle = 1<<40, 30*time.Minute // B
	m.Sweep(context.Background())
	if len(killed) != 1 || killed[0][0] != 200 {
		t.Fatalf("after B killed %v, want [[200]] (battery eviction of the unpinned server)", killed)
	}

	mem.avail, ws2Idle = 1, time.Minute // C
	m.Sweep(context.Background())
	if len(killed) != 1 {
		t.Fatalf("after C killed %v: the pinned server died on a broken under-floor streak", killed)
	}
}

// The battery path itself is unchanged above the floor: a pin still protects
// there (TestBatterySkipsPinned covers the timed pin; this covers durable).
func TestBatteryAboveFloorStillSparesPinned(t *testing.T) {
	cfg := Defaults()
	cfg.ConfirmWindows = 1
	var killed [][]int
	m := newTestMonitor(&fakeStore{workspaces: twoWorkspaces()}, fakeMem{avail: 1 << 40}, cfg, &killed)
	m.power = &fakePower{seq: []bool{true}}
	m.onBattery = true
	m.PinUntilUnpinned("ws1", "dev")
	m.Sweep(context.Background())
	if len(killed) != 0 {
		t.Fatalf("battery above the floor killed a pinned server: %v", killed)
	}
}
