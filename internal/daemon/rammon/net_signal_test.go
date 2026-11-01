package rammon

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/devserver"
)

// withNetSignal turns the opt-in network signal on for the tests that are
// about it.
func withNetSignal(c Config) Config {
	c.NetSignal = true
	return c
}

// Rule D8: pigtail does not guess use from the network. With the shipped
// defaults a burst of new connections every sweep does not keep a resting
// server active, and neither connection inventory is ever run.
func TestDefaultIgnoresConnectionBursts(t *testing.T) {
	var killed [][]int
	accepted, timeWait, n := 0, 0, 0
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, Defaults(), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) {
			accepted++
			n++ // a full navigation burst at every sweep
			var cs []devserver.Conn
			for _, e := range endpoints(50000+100*n, 2*navigationBurst) {
				cs = append(cs, devserver.Conn{PID: 100, Remote: e})
			}
			return map[int][]devserver.Conn{3030: cs}, nil
		}),
		WithTimeWait(func() (map[int][]string, error) {
			timeWait++
			return map[int][]string{}, nil
		}))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	for i := 0; i <= 40; i++ {
		now = base.Add(time.Duration(16*i) * time.Second)
		if !m.observeActivity(now) {
			t.Fatalf("observeActivity failed at sweep %d", i)
		}
	}
	if got := m.activity.idleFor("ws1:dev", now); got < 5*time.Minute {
		t.Fatalf("idle with the default config = %s, want >= 5m: connections must not count unless -idle-net-signal is set", got)
	}
	if accepted != 0 || timeWait != 0 {
		t.Fatalf("inventories run with the network signal off: lsof %d, netstat/ss %d, want 0 and 0", accepted, timeWait)
	}
}

func TestDefaultsNetSignalIsOff(t *testing.T) {
	if Defaults().NetSignal {
		t.Fatal("Defaults().NetSignal = true, want false (rule D8)")
	}
}

// With the network signal on, a sweep with no live server has nothing to
// attribute connections to: the inventories are not run.
func TestNetSignalSkipsInventoriesWithoutALiveServer(t *testing.T) {
	var killed [][]int
	calls := 0
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithPortPIDs(func(int) (bool, []int, error) { return false, nil, nil }),
		WithAccepted(func() (map[int][]devserver.Conn, error) {
			calls++
			return map[int][]devserver.Conn{}, nil
		}),
		WithTimeWait(func() (map[int][]string, error) {
			calls++
			return map[int][]string{}, nil
		}))
	m.observeActivity(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if calls != 0 {
		t.Fatalf("%d inventory call(s) with no live server, want 0", calls)
	}
}

// A durable netstat/ss failure (a Linux box without ss) logs once per kind of
// error, not at every sweep; a new error, or a failure after a recovery, logs
// again.
func TestDurableInventoryFailureLogsOncePerError(t *testing.T) {
	var killed [][]int
	var lines []string
	twErr := errors.New("ss: executable file not found in $PATH")
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) { return map[int][]devserver.Conn{}, nil }),
		WithTimeWait(func() (map[int][]string, error) {
			if twErr != nil {
				return nil, twErr
			}
			return map[int][]string{}, nil
		}),
		WithLogger(func(format string, args ...any) {
			lines = append(lines, format)
		}))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sweep := 0
	run := func(n int) {
		for i := 0; i < n; i++ {
			m.observeActivity(base.Add(time.Duration(16*sweep) * time.Second))
			sweep++
		}
	}
	count := func() int {
		c := 0
		for _, l := range lines {
			if strings.Contains(l, "time-wait inventory failed") {
				c++
			}
		}
		return c
	}
	run(5)
	if got := count(); got != 1 {
		t.Fatalf("5 sweeps with the same failure logged %d line(s), want 1", got)
	}
	twErr = errors.New("ss: signal: killed")
	run(3)
	if got := count(); got != 2 {
		t.Fatalf("a different error logged %d line(s) in total, want 2", got)
	}
	twErr = nil
	run(2)
	twErr = errors.New("ss: signal: killed")
	run(2)
	if got := count(); got != 3 {
		t.Fatalf("a failure after a recovery logged %d line(s) in total, want 3", got)
	}
}

// With the network signal off, the peer set is dropped — not kept and
// rewritten as is — so turning the signal back on starts from a fresh
// baseline instead of reading every connection opened meanwhile as new.
func TestNetSignalOffDropsThePeerSet(t *testing.T) {
	var killed [][]int
	remotes := endpoints(50001, navigationBurst)
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) {
			var cs []devserver.Conn
			for _, r := range remotes {
				cs = append(cs, devserver.Conn{PID: 100, Remote: r})
			}
			return map[int][]devserver.Conn{3030: cs}, nil
		}))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	for i := 0; i <= 25; i++ { // signal on, the same connections throughout: full, quiet window
		now = base.Add(time.Duration(16*i) * time.Second)
		m.observeActivity(now)
	}
	m.cfg.NetSignal = false
	now = now.Add(16 * time.Second)
	m.observeActivity(now)
	if e := m.activity.entriesSnapshot()["ws1:dev"]; e.peersKnown || len(e.peers) != 0 {
		t.Fatalf("signal off: peers %v (known %v) kept, want dropped", e.peers, e.peersKnown)
	}
	m.cfg.NetSignal = true
	remotes = endpoints(51001, navigationBurst) // other connections by the time it is back on
	now = now.Add(16 * time.Second)
	m.observeActivity(now)
	if got := m.activity.idleFor("ws1:dev", now); got == 0 {
		t.Fatal("connections present when the signal came back on were read as new against a stale peer set")
	}
}
