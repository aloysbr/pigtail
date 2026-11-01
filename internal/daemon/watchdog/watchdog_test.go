package watchdog

import (
	"context"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/proccpu"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// scriptedSampler returns its snapshots in order; the last one repeats.
type scriptedSampler struct {
	snaps []map[int]proccpu.ProcStat
	calls int
}

func (s *scriptedSampler) Sample() (map[int]proccpu.ProcStat, error) {
	i := s.calls
	if i >= len(s.snaps) {
		i = len(s.snaps) - 1
	}
	s.calls++
	return s.snaps[i], nil
}

// fakeStore records RecordRunaway calls.
type fakeStore struct {
	workspaces []pigtail.Workspace
	recorded   []string
}

func (f *fakeStore) ListWorkspaces(pigtail.WorkspaceState) ([]pigtail.Workspace, error) {
	return f.workspaces, nil
}
func (f *fakeStore) RecordRunaway(id, _ string) error {
	f.recorded = append(f.recorded, id)
	return nil
}

// snap builds a snapshot with one server process tree: listener pid 100
// (child of 1) with child 200, at the given accumulated CPU seconds each.
func snap(listenerCPU, childCPU float64) map[int]proccpu.ProcStat {
	return map[int]proccpu.ProcStat{
		1:   {PID: 1, PPID: 0, CPUSeconds: 0},
		100: {PID: 100, PPID: 1, CPUSeconds: listenerCPU},
		200: {PID: 200, PPID: 100, CPUSeconds: childCPU},
	}
}

// newTestWatchdog wires a Watchdog with everything faked: instant waits,
// scripted CPU, one workspace on port 3030 with listener pid 100.
func newTestWatchdog(t *testing.T, st *fakeStore, smp proccpu.Sampler, cfg *pigtail.WatchdogConfig) (*Watchdog, *[]Target) {
	t.Helper()
	var killed []Target
	w := New(events.New(), st,
		WithSampler(smp),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithLogger(func(string, ...any) {}),
		WithConfigResolver(func(pigtail.Workspace) *pigtail.WatchdogConfig { return cfg }),
		WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3030 {
				return true, []int{100}, nil
			}
			return false, nil, nil
		}),
		WithKiller(func(tg Target) error {
			killed = append(killed, tg)
			return nil
		}),
	)
	return w, &killed
}

func testWorkspace() pigtail.Workspace {
	return pigtail.Workspace{
		ID:    "ws_test",
		Path:  "/tmp/proj/feat-x",
		Ports: map[string]int{"dev": 3030},
	}
}

func defaults() *pigtail.WatchdogConfig { return pigtail.WatchdogDefaults() }

// TestSweepKillsSustainedRunaway: a subtree pegging ~500% across both
// windows is killed, recorded, and announced on the bus.
func TestSweepKillsSustainedRunaway(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{testWorkspace()}}
	// Window 1: 0→30s CPU over 6s = 500%. Window 2: 30→60s = 500%.
	smp := &scriptedSampler{snaps: []map[int]proccpu.ProcStat{
		snap(0, 0), snap(20, 10), snap(20, 10), snap(40, 20),
	}}
	w, killed := newTestWatchdog(t, st, smp, defaults())
	sub := w.bus.Subscribe(string(pigtail.EventResourcesWarning))
	defer sub.Close()

	w.Sweep(context.Background())

	if len(*killed) != 1 {
		t.Fatalf("killed %d targets, want 1", len(*killed))
	}
	if (*killed)[0].Port != 3030 || (*killed)[0].Workspace.ID != "ws_test" {
		t.Errorf("killed wrong target: %+v", (*killed)[0])
	}
	if len(st.recorded) != 1 || st.recorded[0] != "ws_test" {
		t.Errorf("recorded: %v, want [ws_test]", st.recorded)
	}
	// Expect a "warned" then a "killed" event.
	actions := drainActions(sub)
	if len(actions) != 2 || actions[0] != "warned" || actions[1] != "killed" {
		t.Errorf("event actions: %v, want [warned killed]", actions)
	}
}

// TestSweepClearsTransientBurst: hot in window 1, idle in window 2 — the
// confirmation window clears it. This is the false-positive guard that
// protects a legitimate HMR recompile.
func TestSweepClearsTransientBurst(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{testWorkspace()}}
	// Window 1: 0→30 = 500%. Window 2: 30→30.06 = 1%.
	smp := &scriptedSampler{snaps: []map[int]proccpu.ProcStat{
		snap(0, 0), snap(20, 10), snap(20, 10), snap(20.05, 10.01),
	}}
	w, killed := newTestWatchdog(t, st, smp, defaults())
	sub := w.bus.Subscribe(string(pigtail.EventResourcesWarning))
	defer sub.Close()

	w.Sweep(context.Background())

	if len(*killed) != 0 {
		t.Fatalf("killed %d targets, want 0 (transient burst)", len(*killed))
	}
	if len(st.recorded) != 0 {
		t.Errorf("recorded: %v, want none", st.recorded)
	}
	actions := drainActions(sub)
	if len(actions) != 1 || actions[0] != "warned" {
		t.Errorf("event actions: %v, want [warned]", actions)
	}
}

// TestSweepIgnoresIdleServer: a server near 0% CPU is never measured hot.
func TestSweepIgnoresIdleServer(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{testWorkspace()}}
	smp := &scriptedSampler{snaps: []map[int]proccpu.ProcStat{
		snap(10, 5), snap(10.01, 5),
	}}
	w, killed := newTestWatchdog(t, st, smp, defaults())

	w.Sweep(context.Background())

	if len(*killed) != 0 || len(st.recorded) != 0 {
		t.Fatalf("idle server touched: killed=%d recorded=%v", len(*killed), st.recorded)
	}
}

// TestSweepSkipsDisabledProject: enabled:false opts the project out.
func TestSweepSkipsDisabledProject(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{testWorkspace()}}
	smp := &scriptedSampler{snaps: []map[int]proccpu.ProcStat{
		snap(0, 0), snap(100, 100),
	}}
	disabled := defaults()
	off := false
	disabled.Enabled = &off
	w, killed := newTestWatchdog(t, st, smp, disabled)

	w.Sweep(context.Background())

	if len(*killed) != 0 {
		t.Fatalf("disabled project was killed")
	}
	if smp.calls != 0 {
		t.Errorf("sampler called %d times for a disabled project, want 0", smp.calls)
	}
}

// TestSweepKillFailureSkipsRecord: a failed kill must not stamp the
// workspace as handled.
func TestSweepKillFailureSkipsRecord(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{testWorkspace()}}
	smp := &scriptedSampler{snaps: []map[int]proccpu.ProcStat{
		snap(0, 0), snap(20, 10), snap(20, 10), snap(40, 20),
	}}
	w, _ := newTestWatchdog(t, st, smp, defaults())
	w.kill = func(Target) error { return context.DeadlineExceeded }

	w.Sweep(context.Background())

	if len(st.recorded) != 0 {
		t.Errorf("recorded despite kill failure: %v", st.recorded)
	}
}

// TestRunStopsOnCancel: Run returns promptly once ctx is cancelled.
func TestRunStopsOnCancel(t *testing.T) {
	st := &fakeStore{}
	w := New(events.New(), st,
		WithSampler(&scriptedSampler{snaps: []map[int]proccpu.ProcStat{snap(0, 0)}}),
		WithLogger(func(string, ...any) {}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

// drainActions collects the "action" field of buffered events.
func drainActions(sub *events.Subscription) []string {
	var out []string
	for {
		select {
		case e := <-sub.C:
			if a, ok := e.Data["action"].(string); ok {
				out = append(out, a)
			}
		default:
			return out
		}
	}
}
