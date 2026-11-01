package rammon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sweepCounter is probeCounter's bulk twin: it records how many times the
// machine was interrogated, rather than which ports were asked about. The
// unit of cost moved from "one fork per port" to "one fork per pass", so
// that is what the assertion has to be able to see.
type sweepCounter struct {
	mu   sync.Mutex
	n    int
	err  error
	inv  map[int][]int
	last time.Time
}

func (s *sweepCounter) list() (map[int][]int, error) {
	s.mu.Lock()
	s.n++
	s.last = time.Now()
	err, inv := s.err, s.inv
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *sweepCounter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// bigFixture builds n workspaces with two servers each, on distinct ports,
// plus the matching listener inventory and RSS table. Every server is alive.
func bigFixture(n int) (wss []pigtail.Workspace, inv map[int][]int, procs map[int]memStat, ports int) {
	inv = map[int][]int{}
	procs = map[int]memStat{}
	for i := 0; i < n; i++ {
		devPort, webPort := 4000+2*i, 4001+2*i
		devPID, webPID := 10000+2*i, 10001+2*i
		wss = append(wss, pigtail.Workspace{
			ID:    fmt.Sprintf("ws%03d", i),
			Path:  fmt.Sprintf("/tmp/ws%03d", i),
			Ports: map[string]int{"dev": devPort, "web": webPort},
		})
		inv[devPort] = []int{devPID}
		inv[webPort] = []int{webPID}
		// RSS deliberately keyed off the pid, so an attribution that lost the
		// pid set (a bulk path returning only a boolean) would show up as a
		// zero rather than as a missing row.
		procs[devPID] = memStat{PID: devPID, MemBytes: uint64(devPID) << 10}
		procs[webPID] = memStat{PID: webPID, MemBytes: uint64(webPID) << 10}
	}
	return wss, inv, procs, 2 * n
}

// fixtureMem serves the fixture's per-process RSS table.
type fixtureMem struct {
	avail uint64
	table map[int]memStat
}

func (f fixtureMem) available() (uint64, error)      { return f.avail, nil }
func (f fixtureMem) procs() (map[int]memStat, error) { return f.table, nil }

// twoServersEach enumerates the fixture's servers deterministically. The
// production defaultServersFor would iterate a map and stat every path.
func twoServersEach(ws pigtail.Workspace) []ServerProc {
	return []ServerProc{
		{Name: "dev", Port: ws.Ports["dev"]},
		{Name: "web", Port: ws.Ports["web"]},
	}
}

// TestSnapshotProbesTheMachineOncePerPass is the load-bearing number of this
// whole change, and it is deterministic: independent of the machine, of how
// many workspaces happen to exist, and of what else is running.
//
// GET /servers calls Inspect() → snapshot(), whose wss × serversFor loop
// forked one lsof per configured port, in series, inside the request.
// Measured 2026-09-21 on 51 live workspaces / 102 port entries: 26.6 / 8.9 /
// 7.4 s per request. 400 ports here; the assertion is that the machine is
// interrogated exactly once, and that the per-port fallback is not used at
// all — portPIDs is wired to fail the test if it is ever called.
func TestSnapshotProbesTheMachineOncePerPass(t *testing.T) {
	const workspaces = 200
	wss, inv, procs, ports := bigFixture(workspaces)
	sc := &sweepCounter{inv: inv}

	cfg := Defaults()
	cfg.Enabled = true

	m := New(events.New(), &fakeStore{workspaces: wss},
		testPathsExist,
		WithConfig(cfg),
		WithLogger(func(string, ...any) {}),
		WithMemSampler(fixtureMem{avail: 32 << 30, table: procs}),
		WithServersFunc(twoServersEach),
		WithIdleFunc(func(pigtail.Workspace, string) time.Duration { return time.Minute }),
		WithListeners(sc.list),
	)
	m.portPIDs = func(port int) (bool, []int, error) {
		t.Errorf("snapshot fell back to a per-port probe (:%d) with a bulk seam installed", port)
		return false, nil, nil
	}

	stats := m.snapshot()

	if got := sc.count(); got != 1 {
		t.Fatalf("snapshot interrogated the machine %d time(s) for %d ports, want exactly 1",
			got, ports)
	}
	if len(stats) != ports {
		t.Fatalf("snapshot returned %d servers, want %d", len(stats), ports)
	}
	// RSS is the half a boolean-only remedy would have silently dropped.
	for _, s := range stats {
		if len(s.PIDs) != 1 {
			t.Fatalf("%s:%s carries pids %v, want exactly one", s.WorkspaceID, s.Server, s.PIDs)
		}
		if want := uint64(s.PIDs[0]) << 10; s.MemBytes != want {
			t.Fatalf("%s:%s (pid %d) has RSS %d, want %d — the pid set was lost on the way",
				s.WorkspaceID, s.Server, s.PIDs[0], s.MemBytes, want)
		}
	}
}

// TestBulkProbeFailureNeverFabricatesAFreePort holds the safety half.
//
// A pass whose probe failed knows nothing about any port. It must produce no
// ServerStat — an entry with no pids would report a server with zero RSS —
// and, above all, no death: observeActivity records a death for a key that
// was alive last pass and is gone now, so reading a failed lsof as "the port
// is free" turns one unrunnable command into a volley of deaths for servers
// that never stopped. That exact failure already happened once with the
// per-port probe (see devserver.portProbeResult); the bulk path must not
// reintroduce it at 400 ports a time.
func TestBulkProbeFailureNeverFabricatesAFreePort(t *testing.T) {
	wss, inv, procs, _ := bigFixture(3)
	sc := &sweepCounter{inv: inv}

	cfg := Defaults()
	cfg.Enabled = true

	m := New(events.New(), &fakeStore{workspaces: wss},
		testPathsExist,
		WithConfig(cfg),
		WithLogger(func(string, ...any) {}),
		WithMemSampler(fixtureMem{avail: 32 << 30, table: procs}),
		WithServersFunc(twoServersEach),
		WithCPUSampler(fixedCPU{secs: 10}),
		WithListeners(sc.list),
	)
	// The per-port seam answers alive too, so this test is honest about
	// which path is failing: in T2 observeActivity still walks portPIDs, and
	// after T3 it goes through the bulk seam. Either way, the first pass must
	// see every server alive, and the second must see nothing but an error.
	m.portPIDs = func(port int) (bool, []int, error) {
		pids := inv[port]
		return len(pids) > 0, pids, nil
	}

	now := time.Now()

	// Pass 1: everything alive, the tracker learns every key.
	if !m.observeActivity(now) {
		t.Fatal("first pass failed to observe activity")
	}
	if got := len(m.snapshot()); got != 6 {
		t.Fatalf("first pass saw %d servers, want 6", got)
	}

	// Pass 2: the machine cannot be interrogated.
	boom := errors.New("lsof: EMFILE")
	sc.mu.Lock()
	sc.err = boom
	sc.mu.Unlock()
	m.portPIDs = func(int) (bool, []int, error) { return false, nil, boom }

	m.observeActivity(now.Add(time.Minute))

	if stats := m.snapshot(); len(stats) != 0 {
		t.Fatalf("a failed probe produced %d ServerStat(s): %+v", len(stats), stats)
	}
	if deaths := m.RecentDeaths(now.Add(2 * time.Minute)); len(deaths) != 0 {
		t.Fatalf("a failed probe recorded %d death(s) for servers that never stopped: %+v",
			len(deaths), deaths)
	}
}

// TestWithPortPIDsDisablesTheBulkPath pins the compatibility rule that lets
// every pre-existing WithPortPIDs test keep passing untouched. A test that
// scripts a per-port answer has described the whole machine as far as it is
// concerned; if the bulk seam stayed installed, the monitor would fork the
// real lsof and fold the developer's own listeners into the assertions.
func TestWithPortPIDsDisablesTheBulkPath(t *testing.T) {
	m := New(events.New(), &fakeStore{},
		WithPortPIDs(func(int) (bool, []int, error) { return false, nil, nil }),
	)
	if m.listeners != nil {
		t.Fatal("WithPortPIDs must clear the bulk listener seam")
	}
	if m.portPIDs == nil {
		t.Fatal("WithPortPIDs must install the per-port seam")
	}

	// And the default wiring does have one, otherwise the daemon would never
	// take the fast path at all.
	if New(events.New(), &fakeStore{}).listeners == nil {
		t.Fatal("the default monitor must carry a bulk listener seam")
	}
}

// TestProbePortsFallsBackPerPort covers the nil-seam branch: the shape the
// existing tests run in, and the shape a rollback would leave behind.
func TestProbePortsFallsBackPerPort(t *testing.T) {
	var asked []int
	m := New(events.New(), &fakeStore{},
		WithPortPIDs(func(port int) (bool, []int, error) {
			asked = append(asked, port)
			if port == 3030 {
				return true, []int{100}, nil
			}
			return false, nil, nil
		}),
	)
	got := m.probePorts([]int{3030, 3040})
	if len(asked) != 2 {
		t.Fatalf("asked %v, want one probe per port", asked)
	}
	if !got[3030].busy || len(got[3030].pids) != 1 || got[3030].pids[0] != 100 {
		t.Fatalf(":3030 = %+v, want busy with pid 100", got[3030])
	}
	if got[3040].busy || got[3040].err != nil {
		t.Fatalf(":3040 = %+v, want free with no error", got[3040])
	}
}

// TestSweepProbesTheMachineOncePerPass is TestSweepProbesEachPortOncePerPass
// carried to the bulk path, and the reason T3 exists at all.
//
// The sweep has two full passes by design — observeActivity samples CPU for
// the tracker, snapshot attributes RSS — and each used to inventory the
// machine one fork per port. At Defaults().Interval = 15s, with ~0.12s per
// fork over 102 port entries, the two passes cost ~25s: the sweep never
// slept, so the daemon ran permanently behind itself and competed with every
// incoming request. Fixing only snapshot() would have halved a number that
// still exceeded the interval.
//
// Two bulk probes per sweep is the floor with the current design. Anything
// above it is a stage inventorying the machine again.
func TestSweepProbesTheMachineOncePerPass(t *testing.T) {
	const workspaces = 200
	wss, inv, procs, ports := bigFixture(workspaces)
	sc := &sweepCounter{inv: inv}

	cfg := Defaults()
	cfg.Enabled = true
	cfg.FloorBytes = 1 << 30 // avail is far above: no mains eviction path
	// Both optional stages ON but tuned to evict nothing, so the count
	// reflects probing alone and not a stage that bailed out early.
	cfg.IdlePurgeGrace = time.Hour
	cfg.MaxServers = 10 * ports

	var killed [][]int
	m := New(events.New(), &fakeStore{workspaces: wss},
		testPathsExist,
		WithConfig(cfg),
		WithLogger(func(string, ...any) {}),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithMemSampler(fixtureMem{avail: 32 << 30, table: procs}),
		WithServersFunc(twoServersEach),
		WithIdleFunc(func(pigtail.Workspace, string) time.Duration { return time.Minute }),
		WithCPUSampler(fixedCPU{secs: 10}),
		WithKiller(func(pids []int) { killed = append(killed, pids) }),
		WithListeners(sc.list),
	)
	m.portPIDs = func(port int) (bool, []int, error) {
		t.Errorf("sweep fell back to a per-port probe (:%d) with a bulk seam installed", port)
		return false, nil, nil
	}

	m.Sweep(context.Background())

	const passes = 2
	if got := sc.count(); got != passes {
		t.Fatalf("sweep interrogated the machine %d time(s) for %d ports, want %d "+
			"(observeActivity + snapshot); a stage is inventorying again", got, ports, passes)
	}
	if len(killed) != 0 {
		t.Fatalf("the fixture was tuned to evict nothing, but %d eviction(s) fired: %v",
			len(killed), killed)
	}
}
