package rammon

import (
	"context"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// aliasPinMonitor wires the monorepo shape on two workspaces: each runs ONE
// process declared under two names, `dev` and `web`, on the same port. ws1 is
// pid 100 on 3030, idle 30m; ws2 is pid 200 on 3040, idle 1m (never a victim:
// under capMinIdle, BatteryGrace, Grace and any purge grace used here). Every
// reading is confirmed, so only the pin decides whether pid 100 dies.
// `dev` comes first, so it is distinctProcesses' representative; `web` is not.
func aliasPinMonitor(t *testing.T, cfg Config, avail uint64, killed *[][]int) *RAMMonitor {
	t.Helper()
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
	}}
	return New(events.New(), st, testPathsExist,
		WithMemSampler(fakeMem{avail: avail}),
		WithConfig(cfg),
		WithLogger(func(string, ...any) {}),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithServersFunc(func(ws pigtail.Workspace) []ServerProc {
			p := ws.Ports["dev"]
			return []ServerProc{{Name: "dev", Port: p}, {Name: "web", Port: p}}
		}),
		WithPortPIDs(func(port int) (bool, []int, error) {
			switch port {
			case 3030:
				return true, []int{100}, nil
			case 3040:
				return true, []int{200}, nil
			}
			return false, nil, nil
		}),
		WithIdleFunc(func(ws pigtail.Workspace, _ string) time.Duration {
			if ws.ID == "ws1" {
				return 30 * time.Minute
			}
			return time.Minute
		}),
		WithKiller(func(pids []int) { *killed = append(*killed, pids) }),
	)
}

// aliasPins is the matrix every path is checked against: the pin on the
// representative label (non-regression) and on the other label (the defect),
// timed and durable.
var aliasPins = []struct {
	name  string
	label string
	pin   func(m *RAMMonitor, label string)
}{
	{"durable on dev (representative)", "dev", func(m *RAMMonitor, l string) {
		m.snoozeDurable(ServerStat{WorkspaceID: "ws1", Server: l})
	}},
	{"durable on web (other label)", "web", func(m *RAMMonitor, l string) {
		m.snoozeDurable(ServerStat{WorkspaceID: "ws1", Server: l})
	}},
	{"timed on dev (representative)", "dev", func(m *RAMMonitor, l string) {
		m.snooze(ServerStat{WorkspaceID: "ws1", Server: l}, time.Now().Add(time.Hour))
	}},
	{"timed on web (other label)", "web", func(m *RAMMonitor, l string) {
		m.snooze(ServerStat{WorkspaceID: "ws1", Server: l}, time.Now().Add(time.Hour))
	}},
}

func killedPID(killed [][]int, pid int) bool {
	for _, k := range killed {
		for _, p := range k {
			if p == pid {
				return true
			}
		}
	}
	return false
}

// A pin on any name of a process protects the process from the cap.
func TestAliasPinSparesTheProcessFromTheCap(t *testing.T) {
	for _, tc := range aliasPins {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
				Interval: time.Second, ConfirmWindows: 1, MaxServers: 1}
			var killed [][]int
			m := aliasPinMonitor(t, cfg, 1<<40, &killed)
			m.power = &fakePower{seq: []bool{false}}
			tc.pin(m, tc.label)
			m.Sweep(context.Background())
			if killedPID(killed, 100) {
				t.Fatalf("pin on ws1:%s did not protect its process from the cap: killed %v", tc.label, killed)
			}
		})
	}
}

// ... from the idle purge.
func TestAliasPinSparesTheProcessFromTheIdlePurge(t *testing.T) {
	for _, tc := range aliasPins {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
				Interval: time.Second, ConfirmWindows: 1, IdlePurgeGrace: 10 * time.Minute}
			var killed [][]int
			m := aliasPinMonitor(t, cfg, 1<<40, &killed)
			m.power = &fakePower{seq: []bool{false}}
			tc.pin(m, tc.label)
			m.Sweep(context.Background())
			if killedPID(killed, 100) {
				t.Fatalf("pin on ws1:%s did not protect its process from the idle purge: killed %v", tc.label, killed)
			}
		})
	}
}

// ... from battery eviction (above the floor: the floor is not involved).
func TestAliasPinSparesTheProcessFromBatteryEviction(t *testing.T) {
	for _, tc := range aliasPins {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.ConfirmWindows = 1
			var killed [][]int
			m := aliasPinMonitor(t, cfg, 1<<40, &killed)
			m.power = &fakePower{seq: []bool{true}}
			m.onBattery = true
			tc.pin(m, tc.label)
			m.Sweep(context.Background())
			if killedPID(killed, 100) {
				t.Fatalf("pin on ws1:%s did not protect its process from battery eviction: killed %v", tc.label, killed)
			}
		})
	}
}

// ... from `stop --idle`, which reports both labels as kept for "pinned".
func TestAliasPinSparesTheProcessFromStopIdle(t *testing.T) {
	for _, tc := range aliasPins {
		t.Run(tc.name, func(t *testing.T) {
			var killed [][]int
			m := aliasPinMonitor(t, Defaults(), 1<<40, &killed)
			tc.pin(m, tc.label)
			for _, r := range m.StopIdle(10 * time.Minute) {
				if r.WorkspaceID == "ws1" && (r.Stopped || r.Reason != "pinned") {
					t.Fatalf("pin on ws1:%s: ws1:%s = %+v, want kept with reason pinned", tc.label, r.Server, r)
				}
			}
			if killedPID(killed, 100) {
				t.Fatalf("pin on ws1:%s: stop --idle killed %v", tc.label, killed)
			}
		})
	}
}

// The RAM floor keeps priority (D9/D11): a pin on either label does not save
// the process under the floor, on mains or on battery.
func TestAliasPinDoesNotSpareTheProcessFromTheFloor(t *testing.T) {
	for _, battery := range []bool{false, true} {
		for _, tc := range aliasPins {
			name := tc.name + map[bool]string{false: " / mains", true: " / battery"}[battery]
			t.Run(name, func(t *testing.T) {
				cfg := Defaults() // floor 6 GiB, grace 10m
				cfg.ConfirmWindows = 1
				var killed [][]int
				m := aliasPinMonitor(t, cfg, 1, &killed)
				m.power = &fakePower{seq: []bool{battery}}
				m.onBattery = battery
				tc.pin(m, tc.label)
				m.Sweep(context.Background())
				if len(killed) != 1 || !killedPID(killed, 100) {
					t.Fatalf("under the floor with ws1:%s pinned: killed %v, want [[100]]", tc.label, killed)
				}
			})
		}
	}
}
