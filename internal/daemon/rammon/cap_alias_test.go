package rammon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// aliasStat builds a confirmed, long-idle stat — the state that makes a server
// eligible for over-cap eviction — for one label on a port.
func aliasStat(server string, port int, idle time.Duration, rss uint64) ServerStat {
	return ServerStat{
		WorkspaceID: "ws1", Server: server, Port: port, PIDs: []int{100},
		MemBytes: rss, IdleFor: idle, IdleConfirmed: true,
	}
}

// TestDistinctProcessesCollapsesAliases: two declarations on one port are one
// process wearing two labels, and must count once.
func TestDistinctProcessesCollapsesAliases(t *testing.T) {
	stats := []ServerStat{
		aliasStat("dev", 3030, time.Hour, 100),
		aliasStat("web", 3030, time.Hour, 100),
		aliasStat("storybook", 6030, time.Hour, 200),
		aliasStat("ui-styleguide", 6030, time.Hour, 200),
	}
	got := distinctProcesses(stats)
	if len(got) != 2 {
		t.Fatalf("distinctProcesses over 4 aliases on 2 ports = %d entries, want 2", len(got))
	}
	seen := map[int]bool{}
	for _, s := range got {
		if seen[s.Port] {
			t.Fatalf("port %d returned twice: %+v", s.Port, got)
		}
		seen[s.Port] = true
	}
}

// TestDistinctProcessesKeepsTheLeastIdleReading: the labels of one process do
// not always carry the same reading — a name the traffic never used, or one
// this daemon has not observed yet, reads as more idle than the process is.
// The representative must take the liveliest reading, and stay unconfirmed if
// any label is, so an alias can never be the reason a live process is killed.
func TestDistinctProcessesKeepsTheLeastIdleReading(t *testing.T) {
	quiet := aliasStat("web", 3030, time.Hour, 100)
	busy := aliasStat("dev", 3030, 10*time.Second, 100)
	busy.IdleConfirmed = false

	got := distinctProcesses([]ServerStat{quiet, busy})
	if len(got) != 1 {
		t.Fatalf("distinctProcesses = %d entries, want 1", len(got))
	}
	if got[0].IdleFor != 10*time.Second {
		t.Fatalf("representative IdleFor = %s, want 10s (the least-idle label)", got[0].IdleFor)
	}
	if got[0].IdleConfirmed {
		t.Fatal("representative is confirmed, want unconfirmed (one label is unconfirmed)")
	}
}

// TestDistinctProcessesUnionsPinning: pinning one label protects the process,
// so the representative must carry the furthest expiry of the group.
func TestDistinctProcessesUnionsPinning(t *testing.T) {
	until := time.Now().Add(time.Hour)
	pinned := aliasStat("web", 3030, time.Hour, 100)
	pinned.PinnedUntil = until
	got := distinctProcesses([]ServerStat{aliasStat("dev", 3030, time.Hour, 100), pinned})
	if len(got) != 1 {
		t.Fatalf("distinctProcesses = %d entries, want 1", len(got))
	}
	if !got[0].PinnedUntil.Equal(until) {
		t.Fatalf("representative PinnedUntil = %v, want %v (pin unioned across labels)", got[0].PinnedUntil, until)
	}
}

// aliasMonitor wires a monitor over ONE workspace running ONE server on port
// 3030, declared under two names — the monorepo shape: `dev` and `web` both bound
// to the dev port. Every server is long-idle and confirmed, so only the cap's
// arithmetic decides whether anything dies.
func aliasMonitor(t *testing.T, max int, killed *[][]int, logs *[]string) *RAMMonitor {
	t.Helper()
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
	}}
	return New(events.New(), st, testPathsExist,
		WithMemSampler(fakeMem{avail: 1 << 30}),
		WithConfig(Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
			Interval: time.Second, ConfirmWindows: 1, MaxServers: max}),
		WithLogger(func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) }),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithServersFunc(func(pigtail.Workspace) []ServerProc {
			return []ServerProc{{Name: "dev", Port: 3030}, {Name: "web", Port: 3030}}
		}),
		WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 {
				return true, []int{100}, nil
			}
			return false, nil, nil
		}),
		WithIdleFunc(func(pigtail.Workspace, string) time.Duration { return 30 * time.Minute }),
		WithKiller(func(pids []int) { *killed = append(*killed, pids) }),
	)
}

// TestCapCountsProcessesNotLabels is the regression this fix exists for.
//
// One process, two declared names, cap=1: the machine is exactly at the cap,
// and nothing may be evicted. Before the fix the cap counted the two labels,
// found 2 > 1, and killed the only server running — a repo declaring aliases
// spent two slots per server, so the cap tripped while the machine sat well
// under it. Observed in the field as dev servers dying every few minutes with
// `over-cap evicting` in the daemon log and 17 counted entries for 10 live
// processes.
func TestCapCountsProcessesNotLabels(t *testing.T) {
	var killed [][]int
	var logs []string
	m := aliasMonitor(t, 1, &killed, &logs)

	m.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("one process under two names with cap=1: killed %v, want none", killed)
	}
	for _, l := range logs {
		if strings.Contains(l, "over-cap") || strings.Contains(l, "cap not fully enforced") {
			t.Fatalf("cap complained while at the cap: %q", l)
		}
	}
}

// TestCapStillEvictsWhenGenuinelyOverTheCap: collapsing labels must not become
// a blanket amnesty. Same two-name process, cap=0 — genuinely over — still dies.
func TestCapStillEvictsWhenGenuinelyOverTheCap(t *testing.T) {
	var killed [][]int
	var logs []string
	// cap=0 disables the cap entirely, so use a second workspace instead:
	// two processes (4 labels), cap=1 → exactly one process must die.
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws1", Path: "/tmp/ws1", Ports: map[string]int{"dev": 3030}},
		{ID: "ws2", Path: "/tmp/ws2", Ports: map[string]int{"dev": 3040}},
	}}
	m := New(events.New(), st, testPathsExist,
		WithMemSampler(fakeMem{avail: 1 << 30}),
		WithConfig(Config{Enabled: true, FloorBytes: 1 << 20, Grace: 10 * time.Minute,
			Interval: time.Second, ConfirmWindows: 1, MaxServers: 1}),
		WithLogger(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }),
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
			return 10 * time.Minute
		}),
		WithKiller(func(pids []int) { killed = append(killed, pids) }),
	)

	m.Sweep(context.Background())

	if len(killed) != 1 {
		t.Fatalf("two processes (4 labels) with cap=1: killed %v, want exactly 1", killed)
	}
	if len(killed[0]) != 1 || killed[0][0] != 100 {
		t.Fatalf("killed %v, want the most-idle process (pid 100)", killed[0])
	}
}

// TestDistinctProcessesUnionsDurablePin: a durable pin on one label protects
// the process as a whole, same as a timed one.
func TestDistinctProcessesUnionsDurablePin(t *testing.T) {
	pinned := aliasStat("web", 3030, time.Hour, 100)
	pinned.PinDurable = true
	got := distinctProcesses([]ServerStat{aliasStat("dev", 3030, time.Minute, 100), pinned})
	if len(got) != 1 || !got[0].PinDurable {
		t.Fatalf("representative = %+v, want PinDurable unioned across labels", got)
	}
}
