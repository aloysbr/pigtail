package rammon

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/proccpu"
)

// rampCPUSampler adds step CPU-seconds to pid 100 at every Sample.
type rampCPUSampler struct {
	cpu  *float64
	step float64
}

func (r rampCPUSampler) Sample() (map[int]proccpu.ProcStat, error) {
	*r.cpu += r.step
	return map[int]proccpu.ProcStat{100: {PID: 100, PPID: 1, CPUSeconds: *r.cpu}}, nil
}

func TestDefaultsIdleWindowAndThreshold(t *testing.T) {
	d := Defaults()
	if d.IdleWindow != 5*time.Minute {
		t.Fatalf("IdleWindow = %s, want 5m", d.IdleWindow)
	}
	if d.IdleCPUThresholdPercent != 2.5 { // T measured on 2026-10-02
		t.Fatalf("IdleCPUThresholdPercent = %v, want 2.5", d.IdleCPUThresholdPercent)
	}
}

// Regression of 2026-10-01 end to end with the shipped defaults: 1.25 % of a
// core per sweep used to reset IDLE every sweep.
func TestRestingServerAccumulatesIdle(t *testing.T) {
	var killed [][]int
	cpu := 0.0
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, Defaults(), &killed,
		WithCPUSampler(rampCPUSampler{cpu: &cpu, step: 0.2}))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	for i := 0; i <= 40; i++ {
		now = base.Add(time.Duration(16*i) * time.Second)
		if !m.observeActivity(now) {
			t.Fatalf("observeActivity failed at sweep %d", i)
		}
	}
	if got := m.activity.idleFor("ws1:dev", now); got < 5*time.Minute {
		t.Fatalf("idle of a resting server after 640 s = %s, want >= 5m", got)
	}
}

// D6 end to end: a server reloaded with ~0 CPU (next dev in service, 0.333 %)
// is in use because a navigation burst appeared on its port.
func TestReloadWithoutCPUKeepsTheServerActive(t *testing.T) {
	var killed [][]int
	peers := []devserver.Conn{{PID: 100, Remote: "127.0.0.1:50001"}}
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) {
			return map[int][]devserver.Conn{
				3030:  peers,
				50001: {{PID: 999, Remote: "127.0.0.1:3030"}}, // the browser's half: other port, ignored
			}, nil
		}))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	for i := 0; i <= 25; i++ { // fill quietly, one tab open (0..400 s)
		now = base.Add(time.Duration(16*i) * time.Second)
		m.observeActivity(now)
	}
	peers = nil // reload: navigationBurst new connections at once
	for _, e := range endpoints(50077, navigationBurst) {
		peers = append(peers, devserver.Conn{PID: 100, Remote: e})
	}
	for i := 26; i <= 40; i++ {
		now = base.Add(time.Duration(16*i) * time.Second)
		m.observeActivity(now)
	}
	if got := m.activity.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle 224 s after a reload = %s, want 0", got)
	}
}

// A connection held by another process on the server's port number is not the
// server's (a client whose ephemeral port collides): only the listener's PIDs count.
func TestConnectionsOfOtherPIDsAreIgnored(t *testing.T) {
	var killed [][]int
	n := 0
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) {
			n++ // a full burst every sweep, but held by another process
			var cs []devserver.Conn
			for _, e := range endpoints(50000+100*n, navigationBurst) {
				cs = append(cs, devserver.Conn{PID: 4242, Remote: e})
			}
			return map[int][]devserver.Conn{3030: cs}, nil
		}))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	for i := 0; i <= 40; i++ {
		now = base.Add(time.Duration(16*i) * time.Second)
		m.observeActivity(now)
	}
	if got := m.activity.idleFor("ws1:dev", now); got < 5*time.Minute {
		t.Fatalf("idle = %s: another pid's connections were counted", got)
	}
}

// A page load whose connections closed before the sweep, seen only as
// TIME_WAIT on the port. Seen at two sweeps (they live 30 s), each counts once:
// a burst, not two.
func TestTimeWaitBurstIsActivityCountedOnce(t *testing.T) {
	var killed [][]int
	var tw []string
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) { return map[int][]devserver.Conn{}, nil }),
		WithTimeWait(func() (map[int][]string, error) { return map[int][]string{3030: tw}, nil }))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	for i := 0; i <= 25; i++ { // fill quietly (0..400 s)
		now = base.Add(time.Duration(16*i) * time.Second)
		m.observeActivity(now)
	}
	tw = nil
	for _, e := range endpoints(50001, navigationBurst) {
		tw = append(tw, "127.0.0.1:3030>"+e)
	}
	for i := 26; i <= 27; i++ { // the same TIME_WAITs at two sweeps
		now = base.Add(time.Duration(16*i) * time.Second)
		m.observeActivity(now)
	}
	tw = nil
	if got := m.activity.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle right after a page load = %s, want 0", got)
	}
	total := 0
	for _, s := range m.activity.entriesSnapshot()["ws1:dev"].samples {
		total += s.newConns
	}
	if total != navigationBurst {
		t.Fatalf("new connections in the window = %d, want %d (each TIME_WAIT seen twice, counted once)", total, navigationBurst)
	}
}

// A sweep whose TIME_WAIT inventory failed is "peers unknown" as a whole: a
// still-alive TIME_WAIT must not be counted again when the inventory returns.
func TestFailedTimeWaitInventoryKeepsTheBaseline(t *testing.T) {
	var killed [][]int
	fail := false
	var tw []string
	for _, e := range endpoints(50001, navigationBurst) {
		tw = append(tw, "127.0.0.1:3030>"+e)
	}
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) { return map[int][]devserver.Conn{}, nil }),
		WithTimeWait(func() (map[int][]string, error) {
			if fail {
				return nil, errors.New("netstat: killed")
			}
			return map[int][]string{3030: tw}, nil
		}))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	for i := 0; i <= 25; i++ { // the TIME_WAIT is part of the baseline from the start
		now = base.Add(time.Duration(16*i) * time.Second)
		m.observeActivity(now)
	}
	fail = true
	now = now.Add(16 * time.Second)
	m.observeActivity(now)
	fail = false
	now = now.Add(16 * time.Second)
	m.observeActivity(now)
	if got := m.activity.idleFor("ws1:dev", now); got == 0 {
		t.Fatal("a TIME_WAIT already in the baseline was counted as new after a failed inventory")
	}
}

// D6 end to end: an open tab polling the server (one new short connection per
// sweep, seen as TIME_WAIT) does not keep it active.
func TestPollingTabAgesEndToEnd(t *testing.T) {
	var killed [][]int
	n := 0
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) {
			return map[int][]devserver.Conn{3030: {{PID: 100, Remote: "127.0.0.1:50001"}}}, nil // the tab's websocket
		}),
		WithTimeWait(func() (map[int][]string, error) {
			n++
			return map[int][]string{3030: {"127.0.0.1:3030>127.0.0.1:" + strconv.Itoa(53000+n)}}, nil
		}))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	for i := 0; i <= 40; i++ {
		now = base.Add(time.Duration(16*i) * time.Second)
		m.observeActivity(now)
	}
	if got := m.activity.idleFor("ws1:dev", now); got < 5*time.Minute {
		t.Fatalf("idle of a polled server after 640 s = %s, want >= 5m (D6)", got)
	}
}

// A failed connection inventory degrades to CPU-only for that sweep; it never
// fails the sweep (which would skip eviction) and never reads as "no clients".
func TestObserveActivitySurvivesAFailedConnectionInventory(t *testing.T) {
	var killed [][]int
	m := newTestMonitorRealIdle(&fakeStore{workspaces: oneWorkspace()}, fakeMem{avail: 32 << 30}, withNetSignal(Defaults()), &killed,
		WithAccepted(func() (map[int][]devserver.Conn, error) { return nil, errors.New("lsof: EMFILE") }))
	if !m.observeActivity(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("a failed connection inventory must not fail the sweep")
	}
}
