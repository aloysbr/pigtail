package rammon

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/proccpu"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sharedPortServers is the monorepo shape: one process per port, declared under
// two names. `dev` and `web` both bind the dev port.
func sharedPortServers(ws pigtail.Workspace) []ServerProc {
	p := ws.Ports["dev"]
	return []ServerProc{{Name: "dev", Port: p}, {Name: "web", Port: p}}
}

// sharedPortFixture wires a monitor over ws1 whose single process on :3030 is
// listed as both `dev` and `web`, busy while *live is true.
func sharedPortFixture(t *testing.T) (*RAMMonitor, *bool) {
	t.Helper()
	live := true
	st := &fakeStore{workspaces: oneWorkspace()}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, Defaults(), &killed,
		WithServersFunc(sharedPortServers))
	m.power = &fakePower{seq: []bool{false}}
	m.portPIDs = func(port int) (bool, []int, error) {
		if port == 3030 && live {
			return true, []int{100}, nil
		}
		return false, nil, nil
	}
	return m, &live
}

// intentionalMarks reads the mark map under the lock, for leftover checks.
func intentionalMarks(m *RAMMonitor) map[string]time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]time.Time, len(m.intentional))
	for k, v := range m.intentional {
		out[k] = v
	}
	return out
}

// TestSharedPortDeathIsReportedOnce: one process wearing two names is one
// death, not two. Before, `dev` and `web` each vanished on their own key and
// the log carried two "died on its own" lines for a single crash.
func TestSharedPortDeathIsReportedOnce(t *testing.T) {
	m, live := sharedPortFixture(t)
	sub := m.bus.Subscribe(string(pigtail.EventServerDied))
	defer sub.Close()

	base := time.Now()
	m.observeActivity(base)
	*live = false
	m.observeActivity(base.Add(15 * time.Second))

	deaths := m.RecentDeaths(base.Add(20 * time.Second))
	if len(deaths) != 1 {
		t.Fatalf("deaths = %+v, want exactly one for the shared port", deaths)
	}
	d := deaths[0]
	if !reflect.DeepEqual(d.Servers, []string{"dev", "web"}) {
		t.Errorf("death.Servers = %v, want [dev web]", d.Servers)
	}
	if d.Server != "dev" || d.WorkspaceID != "ws1" || d.Port != 3030 {
		t.Errorf("death = %+v, want ws1, Server=dev (first of Servers), :3030", d)
	}

	select {
	case e := <-sub.C:
		if got, ok := e.Data["servers"].([]string); !ok || !reflect.DeepEqual(got, []string{"dev", "web"}) {
			t.Errorf("event servers = %#v, want [dev web]", e.Data["servers"])
		}
		if e.Data["server"] != "dev" {
			t.Errorf("event server = %v, want dev (kept for older clients)", e.Data["server"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no resources.server_died event was published")
	}
	select {
	case e := <-sub.C:
		t.Fatalf("a second death event was published for the same process: %+v", e.Data)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestStopMarkOnOneNameCoversItsPortTwin: `pigtail stop --server dev` (or the
// menu bar, or any daemon eviction) marks one name; the process it kills was
// also listed as `web`. The twin must not come back as a death, and must not
// leave a mark behind that would swallow a later, real death.
func TestStopMarkOnOneNameCoversItsPortTwin(t *testing.T) {
	m, live := sharedPortFixture(t)

	base := time.Now()
	m.observeActivity(base)
	m.MarkIntentionalStop("ws1", "dev")
	*live = false
	m.observeActivity(base.Add(15 * time.Second))

	if deaths := m.RecentDeaths(base.Add(20 * time.Second)); len(deaths) != 0 {
		t.Fatalf("a stop marked on dev reported its port twin as a death: %+v", deaths)
	}
	if marks := intentionalMarks(m); len(marks) != 0 {
		t.Fatalf("leftover marks after the stop completed: %v", marks)
	}
}

// TestStopMarkBeforeTheTwinIsTrackedStillCovers: the mark can land before the
// tracker knows the port (a stop announced right after a daemon start), so
// MarkIntentionalStop cannot expand it. Grouping at detection time must still
// read the group's single mark as covering the whole process.
func TestStopMarkBeforeTheTwinIsTrackedStillCovers(t *testing.T) {
	m, live := sharedPortFixture(t)

	base := time.Now()
	m.MarkIntentionalStop("ws1", "web") // tracker empty: nothing to expand to
	m.observeActivity(base)
	*live = false
	m.observeActivity(base.Add(15 * time.Second))

	if deaths := m.RecentDeaths(base.Add(20 * time.Second)); len(deaths) != 0 {
		t.Fatalf("one mark in the group did not cover the process: %+v", deaths)
	}
	if marks := intentionalMarks(m); len(marks) != 0 {
		t.Fatalf("leftover marks after the stop completed: %v", marks)
	}
}

// TestNamesOnDifferentPortsDieSeparately: grouping is by port, not by
// workspace — two real processes are two deaths.
func TestNamesOnDifferentPortsDieSeparately(t *testing.T) {
	live := true
	st := &fakeStore{workspaces: oneWorkspace()}
	var killed [][]int
	m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, Defaults(), &killed,
		WithServersFunc(func(pigtail.Workspace) []ServerProc {
			return []ServerProc{{Name: "dev", Port: 3030}, {Name: "storybook", Port: 6030}}
		}))
	m.portPIDs = func(port int) (bool, []int, error) {
		if !live {
			return false, nil, nil
		}
		switch port {
		case 3030:
			return true, []int{100}, nil
		case 6030:
			return true, []int{200}, nil
		}
		return false, nil, nil
	}

	base := time.Now()
	m.observeActivity(base)
	live = false
	m.observeActivity(base.Add(15 * time.Second))

	deaths := m.RecentDeaths(base.Add(20 * time.Second))
	if len(deaths) != 2 {
		t.Fatalf("deaths = %+v, want two (two ports, two processes)", deaths)
	}
	for _, d := range deaths {
		if len(d.Servers) != 1 || d.Servers[0] != d.Server {
			t.Errorf("death %+v: want Servers = [Server]", d)
		}
	}
}

// TestEvictionPathsCoverPortTwins runs every daemon stop path against the
// shared-port shape. Each path marks the name it picked; the process dies
// under both names. Modelled on TestCapServersMarksItsEvictions.
func TestEvictionPathsCoverPortTwins(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(*Config)
		// battery switches the power source to battery for the sweep.
		battery bool
		act     func(m *RAMMonitor)
	}{
		{
			name: "memory pressure",
			cfg: func(c *Config) {
				c.ConfirmWindows = 1
				c.FloorBytes = 1 << 40
				c.Grace = time.Second
			},
			act: func(m *RAMMonitor) { m.Sweep(context.Background()) },
		},
		{
			name:    "battery",
			cfg:     func(c *Config) { c.ConfirmWindows = 1; c.BatteryGrace = 30 * time.Second },
			battery: true,
			act:     func(m *RAMMonitor) { m.Sweep(context.Background()) },
		},
		{
			name: "idle purge",
			cfg:  func(c *Config) { c.IdlePurgeGrace = 30 * time.Second },
			act:  func(m *RAMMonitor) { m.purgeIdle(context.Background(), 32<<30, m.snapshot()) },
		},
		{
			name: "server cap",
			cfg:  func(c *Config) { c.MaxServers = 1 },
			act:  func(m *RAMMonitor) { m.capServers(context.Background(), 32<<30, m.snapshot()) },
		},
		{
			name: "menu-bar stop on the second name",
			act: func(m *RAMMonitor) {
				if !m.StopServer("ws1", "web") {
					panic("StopServer found no ws1:web")
				}
			},
		},
		{
			name: "stop --idle",
			act:  func(m *RAMMonitor) { m.StopIdle(30 * time.Second) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Interval = 15 * time.Second
			// One-sweep window: this test is about every eviction path covering port twins, not the averaging;
			// a window equal to the interval reproduces per-sweep judgement.
			cfg.IdleWindow = cfg.Interval
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			st := &fakeStore{workspaces: []pigtail.Workspace{
				{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
				{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
			}}
			var killed [][]int
			gone := map[int]bool{}
			base := time.Now()
			clock := base
			m := newTestMonitorRealIdle(st, fakeMem{avail: 32 << 30}, cfg, &killed,
				WithClock(func() time.Time { return clock }),
				WithServersFunc(sharedPortServers),
				WithCPUSampler(constCPUSampler{procs: map[int]proccpu.ProcStat{}}))
			m.power = &fakePower{seq: []bool{tc.battery}}
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
			// Enough quiet sweeps to clear every grace, capMinIdle (5 min) included.
			for i := 0; i < 25; i++ {
				clock = base.Add(time.Duration(i) * cfg.Interval)
				m.observeActivity(clock)
			}
			clock = base.Add(25 * cfg.Interval)
			tc.act(m)
			if len(killed) == 0 {
				t.Fatal("the path killed nothing — the fixture no longer exercises it")
			}
			for _, pids := range killed {
				for _, pid := range pids {
					gone[pid*10] = true
				}
			}
			clock = base.Add(26 * cfg.Interval)
			m.observeActivity(clock)

			if deaths := m.RecentDeaths(clock); len(deaths) != 0 {
				t.Fatalf("a deliberate stop reported a port twin as a death: %+v", deaths)
			}
			if marks := intentionalMarks(m); len(marks) != 0 {
				t.Fatalf("leftover marks after the stop completed: %v", marks)
			}
		})
	}
}

// TestGroupByPort pins the pure grouping: one group per (workspace, port),
// names sorted inside a group, groups in a stable order, and an unknown port
// (0) never merging unrelated names.
func TestGroupByPort(t *testing.T) {
	ports := map[string]int{
		"ws1:dev": 3030, "ws1:web": 3030,
		"ws1:storybook": 6030, "ws1:ui-styleguide": 6030,
		"ws2:dev": 3030, // same number, another workspace: never merged
		"ws1:a":   0, "ws1:b": 0,
	}
	portFor := func(k string) int { return ports[k] }

	cases := []struct {
		name string
		keys []string
		want [][]string
	}{
		{"empty", nil, nil},
		{"single", []string{"ws1:dev"}, [][]string{{"ws1:dev"}}},
		{"two names one port", []string{"ws1:web", "ws1:dev"}, [][]string{{"ws1:dev", "ws1:web"}}},
		{
			"two ports",
			[]string{"ws1:ui-styleguide", "ws1:dev", "ws1:storybook", "ws1:web"},
			[][]string{{"ws1:dev", "ws1:web"}, {"ws1:storybook", "ws1:ui-styleguide"}},
		},
		{
			"same port number in two workspaces",
			[]string{"ws2:dev", "ws1:dev"},
			[][]string{{"ws1:dev"}, {"ws2:dev"}},
		},
		{"unknown port never merges", []string{"ws1:b", "ws1:a"}, [][]string{{"ws1:a"}, {"ws1:b"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := groupByPort(tc.keys, portFor); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("groupByPort(%v) = %v, want %v", tc.keys, got, tc.want)
			}
		})
	}
}
