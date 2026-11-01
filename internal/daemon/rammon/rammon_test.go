package rammon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/proccpu"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeMem returns a fixed available figure and an empty proc snapshot. When
// err is set, available() returns it (to exercise the sampler-error fail-safe).
type fakeMem struct {
	avail uint64
	err   error
}

func (f fakeMem) available() (uint64, error)      { return f.avail, f.err }
func (f fakeMem) procs() (map[int]memStat, error) { return map[int]memStat{}, nil }

// fakeStore returns a fixed workspace list.
type fakeStore struct {
	workspaces []pigtail.Workspace
}

func (f *fakeStore) ListWorkspaces(pigtail.WorkspaceState) ([]pigtail.Workspace, error) {
	return f.workspaces, nil
}

func twoWorkspaces() []pigtail.Workspace {
	return []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
	}
}

// newTestMonitor wires a RAMMonitor with everything faked: instant waits,
// silent log, a recording killer. portPIDs maps 3030→pid 100, 3040→pid 200.
// idleFor reports 30m for ws1, 1m for everything else.
// extra options are appended last so they win over the defaults below.
// testPathsExist declares the fixtures' workspaces as present on disk. They
// live at paths like /tmp/ws1 that no test ever creates, so the production
// stat would read every one of them as a removed worktree and skip it.
var testPathsExist = WithPathExists(func(string) bool { return true })

func newTestMonitor(st storeIface, mem memSampler, cfg Config, killed *[][]int, extra ...Option) *RAMMonitor {
	opts := []Option{
		WithMemSampler(mem),
		WithConfig(cfg),
		WithLogger(func(string, ...any) {}),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithPortPIDs(func(port int) (bool, []int, error) {
			switch port {
			case 3030:
				return true, []int{100}, nil
			case 3040:
				return true, []int{200}, nil
			default:
				return false, nil, nil
			}
		}),
		WithIdleFunc(func(ws pigtail.Workspace, _ string) time.Duration {
			if ws.ID == "ws1" {
				return 30 * time.Minute
			}
			return 1 * time.Minute
		}),
		WithKiller(func(pids []int) { *killed = append(*killed, pids) }),
	}
	opts = append(opts, extra...)
	return New(events.New(), st, append([]Option{testPathsExist}, opts...)...)
}

// constCPUSampler always reports the same cumulative CPU, so every delta is
// zero and every server reads as quiet.
type constCPUSampler struct{ procs map[int]proccpu.ProcStat }

func (c constCPUSampler) Sample() (map[int]proccpu.ProcStat, error) { return c.procs, nil }

// flakyCPUSampler reports a fixed CPU snapshot, or an error while fail is set
// — standing in for `ps` (darwin) or /proc (linux) being unreadable for a
// sweep. Flipping fail lets one test show both the guarded and the unguarded
// behaviour of the very same monitor.
type flakyCPUSampler struct {
	procs map[int]proccpu.ProcStat
	fail  bool
}

func (f *flakyCPUSampler) Sample() (map[int]proccpu.ProcStat, error) {
	if f.fail {
		return nil, errors.New("ps: cannot sample")
	}
	return f.procs, nil
}

// TestSweepNeverEvictsWhenTheCPUSamplerFails pins the single guard standing
// between a failed CPU sample and a machine-wide massacre. Idleness is now
// derived from the CPU consumed between sweeps; a sampler error yields no
// deltas at all, which — unguarded — reads as "every server is perfectly
// quiet" and hands every eviction path (RAM floor, battery, idle-purge,
// server cap) the whole fleet at once. observeActivity must return false and
// Sweep must act on nothing.
//
// The recovery half is the control: the same monitor, same pressure, sampler
// working, evicts immediately. Without it the test would also pass on a
// monitor that never evicts anything.
func TestSweepNeverEvictsWhenTheCPUSamplerFails(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 1}
	var killed [][]int
	cpu := &flakyCPUSampler{
		procs: map[int]proccpu.ProcStat{100: {PID: 100, PPID: 1, CPUSeconds: 1}},
		fail:  true,
	}
	m := newTestMonitor(st, fakeMem{avail: 1}, cfg, &killed, WithCPUSampler(cpu))

	if m.observeActivity(time.Now()) {
		t.Fatal("observeActivity must report failure when the CPU sampler errors")
	}
	m.Sweep(context.Background())
	if len(killed) != 0 {
		t.Fatalf("a failed CPU sample must never evict anything, killed=%v", killed)
	}

	cpu.fail = false
	m.Sweep(context.Background())
	if len(killed) == 0 {
		t.Fatal("control: with the sampler working, the same pressure must evict — otherwise the assertion above proves nothing")
	}
}

// TestCPUSamplerFailureIsNotReadAsAVolleyOfDeaths is the other half of the
// same guard. Death detection lives inside observeActivity and works by
// difference: a key the tracker knows, whose port is now free, died. A failed
// sample gives no picture of what is alive, so treating that sweep as
// evidence would declare every tracked server dead at once — a flood of ✝
// lines that teaches the user to ignore the one that matters.
//
// Again with its control: once the sampler recovers, the genuinely vanished
// server IS reported.
func TestCPUSamplerFailureIsNotReadAsAVolleyOfDeaths(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}}
	cfg := Defaults()
	cfg.Enabled = true
	var killed [][]int
	cpu := &flakyCPUSampler{procs: map[int]proccpu.ProcStat{100: {PID: 100, PPID: 1, CPUSeconds: 1}}}
	listening := true
	m := newTestMonitor(st, fakeMem{avail: 32 << 30}, cfg, &killed,
		WithCPUSampler(cpu),
		WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 && listening {
				return true, []int{100}, nil
			}
			return false, nil, nil
		}))

	now := time.Now()
	if !m.observeActivity(now) {
		t.Fatal("the first observation should succeed")
	}

	// The server vanishes, and the very next sample fails.
	listening = false
	cpu.fail = true
	m.observeActivity(now.Add(time.Minute))
	if deaths := m.RecentDeaths(now.Add(time.Minute)); len(deaths) != 0 {
		t.Fatalf("a failed CPU sample must not be read as a death, got %v", deaths)
	}

	cpu.fail = false
	m.observeActivity(now.Add(2 * time.Minute))
	if deaths := m.RecentDeaths(now.Add(2 * time.Minute)); len(deaths) != 1 {
		t.Fatalf("control: once the sampler recovers the real death must be reported, got %v", deaths)
	}
}

// TestServerRestartedWhileTheDaemonRunsResetsItsIdle is the running-daemon half
// of the process-identity guard PR #60 introduced for restored entries only.
// lastCPU is a cumulative counter, so a server restarted between two sweeps
// reports a figure far below the one recorded for its predecessor. That
// negative delta reads exactly like "went quiet": the entry keeps the
// lastActive of the process that died, and idle goes on growing on a server
// that started seconds ago — until the idle purge or the server cap evicts it.
func TestServerRestartedWhileTheDaemonRunsResetsItsIdle(t *testing.T) {
	st := &fakeStore{workspaces: oneWorkspace()}
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	// One-sweep window: this test is about a restart resetting the idle counter, not the averaging;
	// a window equal to the interval reproduces per-sweep judgement.
	cfg.IdleWindow = cfg.Interval
	var killed [][]int
	// The first process has burnt 500 CPU-seconds; its replacement starts from
	// scratch, which is what makes the delta negative.
	cpu := constCPUSampler{procs: map[int]proccpu.ProcStat{
		100: {PID: 100, PPID: 1, CPUSeconds: 500},
		999: {PID: 999, PPID: 1, CPUSeconds: 0.3},
	}}
	pids := []int{100}
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, cfg, &killed,
		WithCPUSampler(cpu),
		WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 {
				return true, pids, nil
			}
			return false, nil, nil
		}))

	base := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	now := base
	m.observeActivity(now) // first sighting
	for i := 0; i < 4; i++ {
		now = now.Add(cfg.Interval)
		m.observeActivity(now) // quiet: idle accumulates
	}
	if got, want := m.activity.idleFor("ws1:dev", now), 4*cfg.Interval; got != want {
		t.Fatalf("idle before the restart = %s, want %s", got, want)
	}

	// Restarted inside one sweep interval: same port, new process.
	pids = []int{999}
	now = now.Add(cfg.Interval)
	m.observeActivity(now)

	if got := m.activity.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle after a restart under new PIDs = %s, want 0 — the entry must restart on a clean base", got)
	}
	// The rebased counter must not read as a burst of activity either: the next
	// quiet sweep starts idle accumulating again from zero.
	now = now.Add(cfg.Interval)
	m.observeActivity(now)
	if got, want := m.activity.idleFor("ws1:dev", now), cfg.Interval; got != want {
		t.Fatalf("idle one quiet sweep after the restart = %s, want %s", got, want)
	}
}

// newTestMonitorRealIdle is a newTestMonitor variant that leaves idleFor at
// its real (tracker-backed) implementation instead of overriding it with
// WithIdleFunc, so M1 regression tests exercise the actual CPU-activity path.
// portPIDs maps 3030→pid 100. extra options (e.g. WithClock) are appended
// last so they win over the defaults below.
func newTestMonitorRealIdle(st storeIface, mem memSampler, cfg Config, killed *[][]int, extra ...Option) *RAMMonitor {
	opts := []Option{
		WithMemSampler(mem),
		WithConfig(cfg),
		WithLogger(func(string, ...any) {}),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithCPUSampler(constCPUSampler{procs: map[int]proccpu.ProcStat{
			100: {PID: 100, PPID: 1, CPUSeconds: 42},
		}}),
		WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 {
				return true, []int{100}, nil
			}
			return false, nil, nil
		}),
		WithKiller(func(pids []int) { *killed = append(*killed, pids) }),
	}
	opts = append(opts, extra...)
	return New(events.New(), st, append([]Option{testPathsExist}, opts...)...)
}

// TestSnapshotIdleIgnoresStaleLogFile is a regression test: a worktree that
// once ran a background dev server keeps a .pigtail-dev.log whose mtime is
// days old. The previous idle heuristic read that mtime and evicted a
// freshly-started server on sight (reproduced in real use on 2026-07-27).
//
// The fixture is now a *leftover*: dev-server logs moved out of the worktree
// into pigtail's state dir, so a file with this name can only be one an old
// version left behind. Which is exactly the case worth pinning — idleness must
// come from CPU activity, never from a file lying around in the worktree.
func TestSnapshotIdleIgnoresStaleLogFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".pigtail-dev.log")
	if err := os.WriteFile(logPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-18 * 24 * time.Hour)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}

	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: dir, Ports: map[string]int{"dev": 3030}},
	}}
	var killed [][]int
	cfg := Defaults()
	cfg.IdlePurgeGrace = time.Hour
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, cfg, &killed)

	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("a freshly-started server was evicted on a stale log file: killed=%v", killed)
	}
}

// TestSnapshotCarriesOwner: the daemon reads the owner marker during its
// sweep so GET /servers can show who claims a server. Without it, an
// identity that is derived rather than typed by hand would be invisible.
func TestSnapshotCarriesOwner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	// The marker lives in pigtail's state dir, keyed by the worktree path —
	// the daemon resolves it from ws.Path alone, with no file in the worktree.
	if _, err := devserver.EnsureStateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devserver.OwnerPath(dir, "dev"),
		[]byte("sid:4711@laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: dir, Ports: map[string]int{"dev": 3030}},
	}}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, Defaults(), &killed)

	servers, _, _ := m.Inspect()
	if len(servers) != 1 {
		t.Fatalf("want 1 server, got %d", len(servers))
	}
	if servers[0].Owner != "sid:4711@laptop" {
		t.Fatalf("Owner = %q, want sid:4711@laptop", servers[0].Owner)
	}
}

// TestIdlePurgeEvictsQuietServerWithoutLogFile is the other half of M1: a
// cmux sidecar writes no .pigtail-*.log, so the old heuristic read idle=0
// forever and the server was never evictable — while still counting against
// MaxServers. The clock is injected (WithClock) rather than relying on
// time.Now(): the scripted observeActivity calls below use synthetic
// timestamps, and without a matching fake clock, Sweep's own now (real wall
// time) would sit only microseconds past `base`, not 45s past it — making the
// idle-for-30s-grace assertion depend on how fast the test happens to run.
func TestIdlePurgeEvictsQuietServerWithoutLogFile(t *testing.T) {
	dir := t.TempDir() // deliberately no .pigtail-dev.log
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: dir, Ports: map[string]int{"dev": 3030}},
	}}
	var killed [][]int
	cfg := Defaults()
	cfg.Interval = 15 * time.Second
	// One-sweep window: this test is about purging a server without a log file, not the averaging;
	// a window equal to the interval reproduces per-sweep judgement.
	cfg.IdleWindow = cfg.Interval
	cfg.IdlePurgeGrace = 30 * time.Second

	base := time.Now()
	clock := base
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, cfg, &killed,
		WithClock(func() time.Time { return clock }))

	// Three quiet sweeps: first sighting, then 2 × 15s of accumulated idle.
	for i := 0; i < 3; i++ {
		clock = base.Add(time.Duration(i) * cfg.Interval)
		m.observeActivity(clock)
	}
	// Sweep runs one more interval after the last scripted observation, well
	// past the 30s grace measured from the pinned first-sighting timestamp.
	clock = base.Add(3 * cfg.Interval)
	m.Sweep(context.Background())

	if len(killed) == 0 {
		t.Fatal("a quiet log-less server was never evictable")
	}
}

// TestSweepEvictsMostIdle: under the floor, the most-idle server (ws1, idle
// 30m) is evicted, killing its PIDs.
func TestSweepEvictsMostIdle(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 1}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1}, cfg, &killed)

	m.Sweep(context.Background())

	if len(killed) != 1 {
		t.Fatalf("killed %d times, want 1", len(killed))
	}
	if len(killed[0]) != 1 || killed[0][0] != 100 {
		t.Errorf("killed pids %v, want [100]", killed[0])
	}
}

// TestSweepWarnsWhenAllActive: every server is within the grace window
// (idle < grace) so nothing is safely evictable — warn, do not kill.
func TestSweepWarnsWhenAllActive(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
	}}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 1}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1}, cfg, &killed)

	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("killed %d times, want 0 (all active)", len(killed))
	}
}

// TestConfirmWindows: with ConfirmWindows=2 the first under-floor sweep must
// not evict; only the second confirmed sweep does.
func TestConfirmWindows(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 2}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1}, cfg, &killed)

	m.Sweep(context.Background())
	if len(killed) != 0 {
		t.Fatalf("first sweep killed %d, want 0 (not yet confirmed)", len(killed))
	}

	m.Sweep(context.Background())
	if len(killed) != 1 {
		t.Fatalf("second sweep killed %d, want 1 (confirmed)", len(killed))
	}
	if len(killed[0]) != 1 || killed[0][0] != 100 {
		t.Errorf("killed pids %v, want [100]", killed[0])
	}
}

// TestServerNameFlowsToIdleFor proves the REAL server name (cfg.Servers key),
// not the port-kind key, is what reaches idleFor and the eviction path. Here
// the configured server is "web" on port 3030; idleFor only reports the server
// idle when called with server=="web".
func TestServerNameFlowsToIdleFor(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 1}
	var killed [][]int
	var sawServer string
	m := New(events.New(), st, testPathsExist,
		WithMemSampler(fakeMem{avail: 1}),
		WithConfig(cfg),
		WithLogger(func(string, ...any) {}),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithServersFunc(func(pigtail.Workspace) []ServerProc {
			return []ServerProc{{Name: "web", Port: 3030}}
		}),
		WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 {
				return true, []int{100}, nil
			}
			return false, nil, nil
		}),
		WithIdleFunc(func(_ pigtail.Workspace, server string) time.Duration {
			sawServer = server
			if server == "web" {
				return 30 * time.Minute
			}
			return 0
		}),
		WithKiller(func(pids []int) { killed = append(killed, pids) }),
	)

	m.Sweep(context.Background())

	if sawServer != "web" {
		t.Fatalf("idleFor saw server %q, want %q", sawServer, "web")
	}
	if len(killed) != 1 || len(killed[0]) != 1 || killed[0][0] != 100 {
		t.Fatalf("killed %v, want [[100]] (real server name must flow through)", killed)
	}
}

// TestSweepSamplerErrorFailSafe: a sampler error must never evict and must not
// advance the under-floor counter.
func TestSweepSamplerErrorFailSafe(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 1}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1, err: errors.New("boom")}, cfg, &killed)

	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("killed %d times on sampler error, want 0", len(killed))
	}
	m.mu.Lock()
	runs := m.underRuns
	m.mu.Unlock()
	if runs != 0 {
		t.Errorf("underRuns = %d after sampler error, want 0", runs)
	}
}

// TestUnderRunsResetAboveFloor: an above-floor sweep resets the confirmation
// counter, so a single subsequent under-floor sweep (with ConfirmWindows=2)
// does not evict.
func TestUnderRunsResetAboveFloor(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 100, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 2}
	var killed [][]int
	low := fakeMem{avail: 1}
	high := fakeMem{avail: 1000}
	m := newTestMonitor(st, low, cfg, &killed)

	// 1st under-floor sweep: primes underRuns to 1 (not yet confirmed).
	m.Sweep(context.Background())
	if len(killed) != 0 {
		t.Fatalf("after first under-floor sweep killed %d, want 0", len(killed))
	}

	// Above-floor sweep resets the counter.
	m.mem = high
	m.Sweep(context.Background())

	// Single under-floor sweep again: counter was reset, so still not confirmed.
	m.mem = low
	m.Sweep(context.Background())
	if len(killed) != 0 {
		t.Fatalf("after reset + single under-floor sweep killed %d, want 0 (counter reset)", len(killed))
	}
}

// TestWarnRateLimited: while pressure persists and all servers are active, the
// "not evicting" warning is logged only once across repeated sweeps.
func TestWarnRateLimited(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
	}}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute, Interval: time.Second, ConfirmWindows: 1}
	var killed [][]int
	var logs []string
	m := New(events.New(), st, testPathsExist,
		WithMemSampler(fakeMem{avail: 1}),
		WithConfig(cfg),
		WithLogger(func(format string, args ...any) { logs = append(logs, format) }),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3040 {
				return true, []int{200}, nil
			}
			return false, nil, nil
		}),
		WithIdleFunc(func(pigtail.Workspace, string) time.Duration { return 1 * time.Minute }),
		WithKiller(func(pids []int) { killed = append(killed, pids) }),
	)

	m.Sweep(context.Background())
	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("killed %d, want 0 (all active)", len(killed))
	}
	var warns int
	for _, l := range logs {
		if strings.Contains(l, "not evicting") {
			warns++
		}
	}
	if warns != 1 {
		t.Errorf("warn logged %d times, want 1 (rate-limited)", warns)
	}
}

func TestDefaultsBatteryFields(t *testing.T) {
	d := Defaults()
	if !d.BatteryEnabled {
		t.Errorf("BatteryEnabled = %v, want true", d.BatteryEnabled)
	}
	if d.BatteryGrace != 10*time.Minute {
		t.Errorf("BatteryGrace = %v, want 10m", d.BatteryGrace)
	}
}

// fakePower returns a scripted sequence of OnBattery results, one per call.
type fakePower struct {
	seq []bool
	i   int
}

func (f *fakePower) OnBattery() (bool, error) {
	v := f.seq[f.i]
	if f.i < len(f.seq)-1 {
		f.i++
	}
	return v, nil
}

// TestSampleOnBatteryDebounce: a single-sweep blip (mains→battery→mains) must
// not flip the debounced flag with ConfirmWindows=2; two agreeing battery
// samples do flip it.
func TestSampleOnBatteryDebounce(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Defaults()
	cfg.Interval = time.Second
	cfg.ConfirmWindows = 2
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, cfg, &killed)
	// Script: battery, mains, battery, battery.
	m.power = &fakePower{seq: []bool{true, false, true, true}}

	if got := m.sampleOnBattery(); got { // 1st battery sample — not yet confirmed
		t.Fatalf("after 1 battery sample, onBattery=%v, want false (unconfirmed)", got)
	}
	if got := m.sampleOnBattery(); got { // mains blip resets the counter
		t.Fatalf("after mains blip, onBattery=%v, want false", got)
	}
	if got := m.sampleOnBattery(); got { // 1st confirming battery sample again
		t.Fatalf("after 1st confirming battery, onBattery=%v, want false", got)
	}
	if got := m.sampleOnBattery(); !got { // 2nd consecutive battery → flip
		t.Fatalf("after 2 consecutive battery samples, onBattery=%v, want true", got)
	}
}

// TestSampleOnBatteryDisabled: BatteryEnabled=false always reports mains.
func TestSampleOnBatteryDisabled(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Defaults()
	cfg.BatteryEnabled = false
	cfg.ConfirmWindows = 1
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, cfg, &killed)
	m.power = &fakePower{seq: []bool{true, true, true}}
	if m.sampleOnBattery() {
		t.Fatalf("BatteryEnabled=false must report mains")
	}
}

// TestSweepBatteryEvictsIgnoringFloor: on battery with abundant RAM (well
// above floor), the most-idle server is still evicted (eviction is always silent).
func TestSweepBatteryEvictsIgnoringFloor(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()} // ws1 idle 30m → pid 100
	cfg := Defaults()
	cfg.Interval = time.Second
	cfg.ConfirmWindows = 1
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, cfg, &killed) // 1 TiB avail
	m.power = &fakePower{seq: []bool{true}}                        // on battery
	m.onBattery = true                                             // already confirmed

	m.Sweep(context.Background())

	if len(killed) != 1 || killed[0][0] != 100 {
		t.Fatalf("battery sweep killed %v, want [[100]]", killed)
	}
}

// TestSweepMainsUnchangedWithAbundantRAM: on mains with abundant RAM, nothing
// is evicted (the existing behaviour) even though servers are idle.
func TestSweepMainsUnchangedWithAbundantRAM(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Defaults()
	cfg.Interval = time.Second
	cfg.ConfirmWindows = 1
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, cfg, &killed)
	m.power = &fakePower{seq: []bool{false}} // on mains

	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("mains sweep with abundant RAM killed %v, want none", killed)
	}
}

func TestSnoozeWindow(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Defaults()
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, cfg, &killed)

	now := time.Now()
	s := ServerStat{WorkspaceID: "ws1", Server: "dev"}

	if m.isSnoozed(s, now) {
		t.Fatalf("fresh server should not be snoozed")
	}
	m.snooze(s, now.Add(10*time.Minute))
	if !m.isSnoozed(s, now) {
		t.Fatalf("just-snoozed server should be snoozed")
	}
	if !m.isSnoozed(s, now.Add(5*time.Minute)) {
		t.Fatalf("server within the snooze window should stay snoozed")
	}
	if m.isSnoozed(s, now.Add(11*time.Minute)) {
		t.Fatalf("server past snoozeUntil should no longer be snoozed")
	}
}

// TestReplugKeepsPins: plugging back in clears nothing (D9 for durable pins;
// technical choice T5 for timed ones — idle purge, the cap and stop --idle
// act on mains too, so a replug clear would drop a protection the user asked
// for in the very mode those evictions run).
func TestReplugKeepsPins(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Defaults()
	cfg.ConfirmWindows = 1
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, cfg, &killed)
	m.onBattery = true
	timed := ServerStat{WorkspaceID: "ws1", Server: "dev"}
	durable := ServerStat{WorkspaceID: "ws2", Server: "dev"}
	m.snooze(timed, time.Now().Add(10*time.Minute))
	m.snoozeDurable(durable)

	m.power = &fakePower{seq: []bool{false}} // mains; ConfirmWindows=1 → flips immediately
	if m.sampleOnBattery() {
		t.Fatalf("expected mains after replug")
	}
	if !m.isSnoozed(timed, time.Now()) {
		t.Fatal("replug cleared a timed pin")
	}
	if !m.isSnoozed(durable, time.Now()) {
		t.Fatal("replug cleared a durable pin")
	}
}

// TestBatterySkipsPinned: a pinned (snoozed) idle server is not evicted on
// battery.
func TestBatterySkipsPinned(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()} // ws1 idle 30m → pid 100
	cfg := Defaults()
	cfg.ConfirmWindows = 1
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, cfg, &killed)
	m.power = &fakePower{seq: []bool{true}}
	m.onBattery = true
	m.snooze(ServerStat{WorkspaceID: "ws1", Server: "dev"}, time.Now().Add(time.Hour))

	m.Sweep(context.Background())
	if len(killed) != 0 {
		t.Fatalf("pinned server must not be killed, killed=%v", killed)
	}
}

// fakeBackend is a minimal test double for backend.Backend. Only DisplayRef
// returns a value; all other methods are no-ops. Reused by Phase 1 Task P1.6.
type fakeBackend struct{ ref string }

func (f *fakeBackend) Kind() string { return "fake" }
func (f *fakeBackend) Create(_ context.Context, _ *pigtail.Project, _ backend.CreateIntent) (*pigtail.Workspace, error) {
	return nil, nil
}
func (f *fakeBackend) Snapshot(_ context.Context, _ *pigtail.Workspace, _ string) (backend.SnapshotID, error) {
	return "", nil
}
func (f *fakeBackend) Diff(_ context.Context, _ *pigtail.Workspace, _, _ *backend.SnapshotID) (backend.Diff, error) {
	return backend.Diff{}, nil
}
func (f *fakeBackend) Status(_ context.Context, _ *pigtail.Workspace) (backend.Status, error) {
	return backend.Status{}, nil
}
func (f *fakeBackend) Cleanup(_ context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	return backend.CleanupOutcome{}, nil
}
func (f *fakeBackend) Move(_ context.Context, _ *pigtail.Workspace, _ string) error { return nil }
func (f *fakeBackend) RenameBranch(_ context.Context, _ *pigtail.Workspace, _ string) error {
	return nil
}
func (f *fakeBackend) DisplayRef(_ *pigtail.Workspace) string { return f.ref }
func (f *fakeBackend) SupportsDelivery() bool                 { return false }

// TestWithBackendWiresField: WithBackend option must set m.backend to the
// supplied instance (phase-0 wiring, field is not read yet).
func TestWithBackendWiresField(t *testing.T) {
	st := &fakeStore{}
	be := &fakeBackend{ref: "feat/x"}
	m := New(nil, st, testPathsExist, WithBackend(be))
	if m.backend != be {
		t.Fatalf("WithBackend did not set m.backend")
	}
}

// TestPurgeIdleEvictsOverThreshold: with avail ABOVE the floor (mains path
// returns None) and IdlePurgeGrace=10m, only the idle-over-threshold server
// (ws1, 30m) is purged. ws2 (1m) is spared.
func TestPurgeIdleEvictsOverThreshold(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
		Interval: time.Second, ConfirmWindows: 1, IdlePurgeGrace: 10 * time.Minute}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, cfg, &killed) // avail >> floor

	m.Sweep(context.Background())

	if len(killed) != 1 || len(killed[0]) != 1 || killed[0][0] != 100 {
		t.Fatalf("idle purge killed %v, want [[100]]", killed)
	}
}

// TestPurgeIdleSparesPinned: a pinned idle-over-threshold server is spared.
func TestPurgeIdleSparesPinned(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
		Interval: time.Second, ConfirmWindows: 1, IdlePurgeGrace: 10 * time.Minute}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, cfg, &killed)

	if !m.Pin("ws1", "dev", time.Hour) {
		t.Fatal("Pin ws1:dev returned false, want true")
	}
	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("pinned server purged: killed %v, want none", killed)
	}
}

// TestPurgeIdleDisabledWhenGraceZero: IdlePurgeGrace=0 purges nothing.
func TestPurgeIdleDisabledWhenGraceZero(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
		Interval: time.Second, ConfirmWindows: 1, IdlePurgeGrace: 0}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, cfg, &killed)

	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("grace=0 purged %v, want none", killed)
	}
}

// TestCapEvictsMostIdle: 2 servers, cap=1, avail above floor (mains None).
// ws1 (30m, >= capMinIdle) is evicted; ws2 (1m, < capMinIdle) is spared.
func TestCapEvictsMostIdle(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
		Interval: time.Second, ConfirmWindows: 1, MaxServers: 1}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, cfg, &killed) // avail >> floor

	m.Sweep(context.Background())

	if len(killed) != 1 || len(killed[0]) != 1 || killed[0][0] != 100 {
		t.Fatalf("cap killed %v, want [[100]]", killed)
	}
}

// TestCapSparesPinned: cap=1, but the only cap-eligible server (ws1, 30m) is
// pinned → nothing is evicted (ws2 at 1m is below capMinIdle).
func TestCapSparesPinned(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
		Interval: time.Second, ConfirmWindows: 1, MaxServers: 1}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, cfg, &killed)

	if !m.Pin("ws1", "dev", time.Hour) {
		t.Fatal("Pin ws1:dev returned false, want true")
	}
	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("pinned server evicted by cap: killed %v, want none", killed)
	}
}

// TestCapDisabledWhenZero: MaxServers=0 evicts nothing even over any count.
func TestCapDisabledWhenZero(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
		Interval: time.Second, ConfirmWindows: 1, MaxServers: 0}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, cfg, &killed)

	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("cap=0 evicted %v, want none", killed)
	}
}

// TestCapWarningLatchedOncePerEpisode: cap=1 with ws1(30m) pinned and ws2(1m)
// fresh → the cap can never be reached (pinned spared, ws2 below capMinIdle),
// so nothing is evicted and the "cap not fully enforced" warning must fire
// exactly ONCE across repeated over-cap sweeps (latched), not every sweep.
func TestCapWarningLatchedOncePerEpisode(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
		Interval: time.Second, ConfirmWindows: 1, MaxServers: 1}
	var logs []string
	var killed [][]int
	m := New(events.New(), st, testPathsExist,
		WithMemSampler(fakeMem{avail: 1 << 30}),
		WithConfig(cfg),
		WithLogger(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithPortPIDs(func(port int) (bool, []int, error) {
			switch port {
			case 3030:
				return true, []int{100}, nil
			case 3040:
				return true, []int{200}, nil
			default:
				return false, nil, nil
			}
		}),
		WithIdleFunc(func(ws pigtail.Workspace, _ string) time.Duration {
			if ws.ID == "ws1" {
				return 30 * time.Minute
			}
			return 1 * time.Minute
		}),
		WithKiller(func(pids []int) { killed = append(killed, pids) }),
	)
	if !m.Pin("ws1", "dev", time.Hour) {
		t.Fatal("Pin ws1:dev returned false, want true")
	}

	m.Sweep(context.Background())
	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("pinned + fresh servers: killed %v, want none", killed)
	}
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "cap not fully enforced") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("warning logged %d times across 2 over-cap sweeps, want 1 (latched)", n)
	}
}

// TestStopIdleStopsQuietAndReportsKept: with grace=10m, ws1 (idle 30m) is
// stopped and ws2 (idle 1m) is kept, and the kept server must say why — the
// whole point of --idle is naming what it spared, not just what it killed.
func TestStopIdleStopsQuietAndReportsKept(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	var killed [][]int
	// newTestMonitor's idleFor: 30m for ws1, 1m for everything else.
	m := newTestMonitor(st, fakeMem{avail: 32 << 30}, Defaults(), &killed)

	got := m.StopIdle(10 * time.Minute)

	if len(got) != 2 {
		t.Fatalf("StopIdle must report every server, got %d", len(got))
	}
	var stopped, kept int
	for _, r := range got {
		if r.Stopped {
			stopped++
			if r.WorkspaceID != "ws1" {
				t.Fatalf("stopped the wrong server: %+v", r)
			}
		} else {
			kept++
			if r.Reason == "" {
				t.Fatalf("a kept server must say why: %+v", r)
			}
		}
	}
	if stopped != 1 || kept != 1 {
		t.Fatalf("stopped=%d kept=%d, want 1 and 1", stopped, kept)
	}
	if len(killed) != 1 {
		t.Fatalf("killed %d process groups, want 1", len(killed))
	}
}

// TestStopIdleSparesPinnedServers: a pinned idle-over-threshold server is
// never stopped by --idle, and is reported as kept for "pinned".
func TestStopIdleSparesPinnedServers(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 32 << 30}, Defaults(), &killed)
	m.Pin("ws1", "dev", time.Hour)

	got := m.StopIdle(10 * time.Minute)

	if len(killed) != 0 {
		t.Fatal("a pinned server must never be stopped by --idle")
	}
	for _, r := range got {
		if r.WorkspaceID == "ws1" && (r.Stopped || r.Reason != "pinned") {
			t.Fatalf("pinned server misreported: %+v", r)
		}
	}
}

func TestBatteryStatusAndToggle(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()} // ws1 idle 30m, ws2 idle 1m
	cfg := Defaults()
	cfg.BatteryGrace = 10 * time.Minute
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, cfg, &killed)
	m.onBattery = true

	v := m.BatteryStatus()
	if !v.OnBattery || !v.Enabled {
		t.Fatalf("status: OnBattery=%v Enabled=%v, want true,true", v.OnBattery, v.Enabled)
	}
	if v.GraceSeconds != 600 {
		t.Fatalf("GraceSeconds=%d, want 600", v.GraceSeconds)
	}
	if len(v.Servers) != 2 {
		t.Fatalf("want 2 servers, got %d", len(v.Servers))
	}
	// ws1 idle 30m ≥ grace 10m → EvictInSeconds 0; ws2 idle 1m → 9m left = 540s.
	byWS := map[string]BatteryServerView{}
	for _, s := range v.Servers {
		byWS[s.WorkspaceID] = s
	}
	if byWS["ws1"].EvictInSeconds != 0 {
		t.Errorf("ws1 EvictInSeconds=%d, want 0", byWS["ws1"].EvictInSeconds)
	}
	if byWS["ws2"].EvictInSeconds != 540 {
		t.Errorf("ws2 EvictInSeconds=%d, want 540", byWS["ws2"].EvictInSeconds)
	}

	m.SetBatteryEnabled(false)
	if m.BatteryStatus().Enabled {
		t.Fatalf("SetBatteryEnabled(false) should disable")
	}
	m.SetBatteryEnabled(true)
	if !m.BatteryStatus().Enabled {
		t.Fatalf("SetBatteryEnabled(true) should re-enable")
	}
}

// fakeMemWithProcs is fakeMem with a process table, so a snapshot charges the
// server's tree a known amount.
type fakeMemWithProcs struct {
	avail uint64
	table map[int]memStat
}

func (f fakeMemWithProcs) available() (uint64, error)      { return f.avail, nil }
func (f fakeMemWithProcs) procs() (map[int]memStat, error) { return f.table, nil }

// TestPurgeIdleLogsMemNotRSS: the eviction log carries the figure the
// decision used — the footprint — and must not call it RSS any more.
func TestPurgeIdleLogsMemNotRSS(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
		Interval: time.Second, ConfirmWindows: 1, IdlePurgeGrace: 10 * time.Minute}
	mem := fakeMemWithProcs{avail: 1 << 30, table: map[int]memStat{
		100: {PID: 100, PPID: 1, MemBytes: 300 << 20}, // ws1:3030, idle 30m → purged
	}}
	var killed [][]int
	var logs []string
	m := newTestMonitor(st, mem, cfg, &killed,
		WithLogger(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }))

	m.Sweep(context.Background())

	var line string
	for _, l := range logs {
		if strings.Contains(l, "idle-purging") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no idle-purging line in %q (killed %v)", logs, killed)
	}
	if !strings.Contains(line, "mem=300MiB") || strings.Contains(line, "rss=") {
		t.Fatalf("purge log = %q, want mem=300MiB and no rss=", line)
	}
}
