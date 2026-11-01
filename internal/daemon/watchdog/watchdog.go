package watchdog

import (
	"context"
	"log"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/proccpu"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Reason recorded on the workspace and published in events when a runaway
// dev server is killed.
const ReasonRunawayCPU = "runaway_cpu"

// DefaultSweepInterval paces sweeps when no project config says otherwise.
const DefaultSweepInterval = 30 * time.Second

// storeIface is the slice of *store.Store the watchdog needs. Narrow so
// tests can fake it.
type storeIface interface {
	ListWorkspaces(state pigtail.WorkspaceState) ([]pigtail.Workspace, error)
	RecordRunaway(id, reason string) error
}

// Target is one workspace dev server eligible for monitoring: the port
// pigtail allocated and the PIDs currently listening on it.
type Target struct {
	Workspace pigtail.Workspace
	Port      int
	PIDs      []int
	Config    *pigtail.WatchdogConfig
}

// Watchdog sweeps all known workspaces and kills dev servers whose process
// subtree sustains the configured %CPU across multiple sampling windows.
type Watchdog struct {
	bus     *events.Bus
	store   storeIface
	sampler proccpu.Sampler
	logf    func(format string, args ...any)

	// cfgFor resolves the effective watchdog config for a workspace
	// (defaults when the project config cannot be loaded).
	cfgFor func(ws pigtail.Workspace) *pigtail.WatchdogConfig
	// portPIDs reports whether a port has listeners and their PIDs.
	portPIDs func(port int) (bool, []int, error)
	// cwds maps listener pids to their working directory; only pids running
	// inside the workspace are targets. nil = off (every listener kept).
	cwds func(pids []int) (map[int]string, error)
	// portsFor resolves a workspace's ports (.env.local first, store fallback).
	portsFor func(ws pigtail.Workspace) map[string]int
	// kill terminates a confirmed runaway. Injected so the detection loop
	// is testable without real signals.
	kill func(t Target) error
	// wait sleeps for d unless ctx is done. Injected for instant tests.
	wait func(ctx context.Context, d time.Duration)
}

// Option customises a Watchdog (used by the daemon wiring and by tests).
type Option func(*Watchdog)

// WithSampler overrides the CPU sampler.
func WithSampler(s proccpu.Sampler) Option { return func(w *Watchdog) { w.sampler = s } }

// WithConfigResolver overrides how a workspace's watchdog config is found.
func WithConfigResolver(f func(ws pigtail.Workspace) *pigtail.WatchdogConfig) Option {
	return func(w *Watchdog) { w.cfgFor = f }
}

// WithPortPIDs overrides port→listener-PID discovery.
func WithPortPIDs(f func(port int) (bool, []int, error)) Option {
	return func(w *Watchdog) { w.portPIDs = f }
}

// WithCwds turns on attribution by cwd: a listener running outside the
// workspace is never a target. nil (the default) keeps every listener.
func WithCwds(f func(pids []int) (map[int]string, error)) Option {
	return func(w *Watchdog) { w.cwds = f }
}

// WithPortsFor overrides how a workspace's ports are resolved.
func WithPortsFor(f func(ws pigtail.Workspace) map[string]int) Option {
	return func(w *Watchdog) { w.portsFor = f }
}

// defaultPortsFor reads a workspace's ports through the resolver rammon uses
// (devserver.WorkspacePorts): .env.local first, the store as fallback.
func defaultPortsFor(ws pigtail.Workspace) map[string]int {
	var cfg *pigtail.ProjectConfig
	if ctx, err := devserver.ResolveContextFromPath(ws.Path); err == nil && ctx != nil {
		cfg = ctx.Config
	}
	return devserver.WorkspacePorts(ws.Path, cfg, ws.Ports)
}

// WithKiller overrides the kill action.
func WithKiller(f func(t Target) error) Option { return func(w *Watchdog) { w.kill = f } }

// WithWaiter overrides the inter-window sleep (tests pass a no-op).
func WithWaiter(f func(ctx context.Context, d time.Duration)) Option {
	return func(w *Watchdog) { w.wait = f }
}

// WithLogger overrides the log sink.
func WithLogger(f func(format string, args ...any)) Option {
	return func(w *Watchdog) { w.logf = f }
}

// New constructs a Watchdog with platform defaults. The daemon supplies the
// config resolver and killer via options; tests override everything.
func New(bus *events.Bus, st storeIface, opts ...Option) *Watchdog {
	w := &Watchdog{
		bus:     bus,
		store:   st,
		sampler: proccpu.NewSampler(),
		logf:    log.Printf,
		cfgFor:  func(pigtail.Workspace) *pigtail.WatchdogConfig { return pigtail.WatchdogDefaults() },
		portPIDs: func(int) (bool, []int, error) {
			return false, nil, nil
		},
		portsFor: defaultPortsFor,
		kill:     func(Target) error { return nil },
		wait: func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		},
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run sweeps until ctx is cancelled.
func (w *Watchdog) Run(ctx context.Context) {
	interval := DefaultSweepInterval
	for {
		w.wait(ctx, interval)
		if ctx.Err() != nil {
			return
		}
		if next := w.Sweep(ctx); next > 0 {
			interval = next
		}
	}
}

// Sweep runs one detection pass over every workspace and returns the pace
// for the next sweep (shortest sweep_interval among enabled configs, or
// DefaultSweepInterval when none).
func (w *Watchdog) Sweep(ctx context.Context) time.Duration {
	next := DefaultSweepInterval
	targets := w.collectTargets()
	if len(targets) == 0 {
		return next
	}
	for _, t := range targets {
		if d := time.Duration(t.Config.SweepInterval) * time.Second; d < next {
			next = d
		}
	}
	w.evaluate(ctx, targets)
	return next
}

// collectTargets finds every (workspace, busy port) pair with the watchdog
// enabled for its project.
func (w *Watchdog) collectTargets() []Target {
	workspaces, err := w.store.ListWorkspaces("")
	if err != nil {
		w.logf("watchdog: list workspaces: %v", err)
		return nil
	}
	var targets []Target
	for _, ws := range workspaces {
		cfg := w.cfgFor(ws)
		if !cfg.IsEnabled() {
			continue
		}
		for _, port := range w.portsFor(ws) {
			busy, pids, err := w.portPIDs(port)
			if err != nil || !busy {
				continue
			}
			if w.cwds != nil {
				own := devserver.ClassifyListeners(pids, ws.Path, w.cwds)
				if own.Unknown() {
					continue // undecided: never kill on a probe that did not happen
				}
				if len(own.Owned) == 0 {
					continue // held from another worktree: not ours to kill
				}
				pids = own.Owned
			}
			targets = append(targets, Target{Workspace: ws, Port: port, PIDs: pids, Config: cfg})
		}
	}
	return targets
}

// evaluate runs the multi-window decision over the targets and kills the
// confirmed runaways. A target must measure hot in every one of its
// ConfirmWindows windows; the first cold window clears it — so a short
// legitimate recompile burst never reaches the kill path.
func (w *Watchdog) evaluate(ctx context.Context, targets []Target) {
	live := targets
	for window := 1; len(live) > 0; window++ {
		if ctx.Err() != nil {
			return
		}
		if window > 1 {
			w.wait(ctx, maxSeconds(live, func(c *pigtail.WatchdogConfig) int { return c.ConfirmGap }))
			if ctx.Err() != nil {
				return
			}
		}
		interval := maxSeconds(live, func(c *pigtail.WatchdogConfig) int { return c.SampleInterval })
		hot, err := w.measureWindow(ctx, live, interval)
		if err != nil {
			w.logf("watchdog: sample: %v", err)
			return
		}

		var still []Target
		for i, t := range live {
			pct, isHot := hot[i], hot[i] >= float64(t.Config.CPUThreshold)
			if !isHot {
				continue // cleared — transient burst, drop it
			}
			if window == 1 {
				w.publish(t, "warned", pct)
			}
			if window >= t.Config.ConfirmWindows {
				w.logf("watchdog: runaway confirmed ws=%s port=%d pids=%v cpu=%.0f%%",
					t.Workspace.ID, t.Port, t.PIDs, pct)
				if err := w.kill(t); err != nil {
					w.logf("watchdog: kill ws=%s port=%d: %v", t.Workspace.ID, t.Port, err)
					continue
				}
				if err := w.store.RecordRunaway(t.Workspace.ID, ReasonRunawayCPU); err != nil {
					w.logf("watchdog: record runaway ws=%s: %v", t.Workspace.ID, err)
				}
				w.publish(t, "killed", pct)
				continue
			}
			still = append(still, t)
		}
		live = still
	}
}

// measureWindow samples all live targets' subtree CPU twice, interval apart,
// and returns each target's %CPU over the window (indexed like live).
func (w *Watchdog) measureWindow(ctx context.Context, live []Target, interval time.Duration) (map[int]float64, error) {
	s0, err := w.sampler.Sample()
	if err != nil {
		return nil, err
	}
	w.wait(ctx, interval)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s1, err := w.sampler.Sample()
	if err != nil {
		return nil, err
	}
	out := map[int]float64{}
	secs := interval.Seconds()
	for i, t := range live {
		var before, after float64
		for _, pid := range t.PIDs {
			before += proccpu.SubtreeCPU(s0, pid)
			after += proccpu.SubtreeCPU(s1, pid)
		}
		out[i] = (after - before) / secs * 100
	}
	return out, nil
}

// publish emits a resources.warning event for SSE consumers (TUI).
func (w *Watchdog) publish(t Target, action string, pct float64) {
	if w.bus == nil {
		return
	}
	w.bus.Publish(pigtail.Event{
		Type: pigtail.EventResourcesWarning,
		Data: map[string]interface{}{
			"workspace_id": t.Workspace.ID,
			"port":         t.Port,
			"pids":         t.PIDs,
			"cpu_percent":  pct,
			"action":       action,
			"reason":       ReasonRunawayCPU,
		},
	})
}

// maxSeconds returns the largest of f(config) across targets, as a Duration.
// Using the max keeps the shared window conservative when projects disagree.
func maxSeconds(targets []Target, f func(*pigtail.WatchdogConfig) int) time.Duration {
	m := 0
	for _, t := range targets {
		if v := f(t.Config); v > m {
			m = v
		}
	}
	return time.Duration(m) * time.Second
}
