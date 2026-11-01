package rammon

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/powersrc"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/proccpu"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// storeIface is the slice of *store.Store the monitor needs. Narrow so tests
// can fake it.
type storeIface interface {
	ListWorkspaces(pigtail.WorkspaceState) ([]pigtail.Workspace, error)
}

// ServerProc identifies one configured dev server on a workspace.
type ServerProc struct {
	Name   string // server name (cfg.Servers key) — also the log-file token
	Kind   string // port kind (cfg.Servers[name].PortKind)
	Port   int    // effective port: .env.local's, else the registry's
	DBPort int    // the registry's port for Kind, 0 when none
}

// Config tunes the RAM-pressure monitor.
type Config struct {
	Enabled        bool
	FloorBytes     uint64        // evict when available drops below this
	Grace          time.Duration // a server must be idle at least this long to be evictable
	Interval       time.Duration // sweep pacing
	ConfirmWindows int           // consecutive under-floor sweeps before acting

	// Battery* tune power-aware eviction. On battery, idle servers are evicted
	// regardless of the RAM floor (see decide()), gated by Enabled && BatteryEnabled.
	BatteryEnabled bool          // power-aware eviction active (default true)
	BatteryGrace   time.Duration // idle-for threshold on battery (default 10m)

	// IdlePurgeGrace drives the always-on idle purge: any running server idle
	// at least this long is evicted every sweep, regardless of power source or
	// the RAM floor. Zero disables it (default). Runtime-settable via
	// SetIdlePurgeGrace; reverts to the flag default on daemon restart.
	IdlePurgeGrace time.Duration

	// MaxServers caps the number of live dev servers. When more than
	// MaxServers are running, the most-idle ones (idle >= capMinIdle,
	// pinned excluded) are evicted every sweep down to the cap. Zero
	// disables the cap (default). Counts cmux-hosted servers too.
	MaxServers int

	// IdleCPUThresholdPercent is the share of one core a server's process
	// subtree must average over IdleWindow to count as in use. Rule D8: by
	// default this is the only signal; a server to keep at rest is pinned.
	IdleCPUThresholdPercent float64

	// IdleWindow is how far back the signals reach. Zero means
	// defaultIdleWindow. A server this daemon first saw is never called idle
	// on less than one full window.
	IdleWindow time.Duration

	// NetSignal turns on the network signal: a navigation burst (>=
	// navigationBurst new connections on a server's port in one sweep) counts
	// as use, which costs one lsof and one netstat/ss on every sweep where a
	// dev server is live. Off — the peer sets are dropped — by
	// default — rule D8: pigtail does not guess use from the network. Measured
	// on 2026-10-02, one untouched browser tab that polls its server reached
	// the burst size in some sweeps, and two such tabs on one server did at
	// almost any relative timing. A server to keep at rest is pinned instead.
	NetSignal bool
}

// Defaults are the out-of-the-box monitor settings: 6 GiB floor, 10-minute
// idle grace, sweep every 15s, confirm over 2 windows. On battery: power-aware
// eviction on, 10-minute idle grace. Idle = under 2.5 % of one core over a
// 5-minute window (rule D8: CPU only; the network signal is opt-in). 2.5 is twice, rounded up
// to 0.5, the highest resting 5-minute mean measured on 2026-10-02 (1.0 %, a
// scratch vite); real resting servers sat at 0.04-0.29 %.
func Defaults() Config {
	return Config{
		Enabled:        true,
		FloorBytes:     6 << 30,
		Grace:          10 * time.Minute,
		Interval:       15 * time.Second,
		ConfirmWindows: 2,

		BatteryEnabled: true,
		BatteryGrace:   10 * time.Minute,

		IdleCPUThresholdPercent: 2.5,
		IdleWindow:              defaultIdleWindow,
	}
}

// RAMMonitor sweeps all known workspaces and, when free memory drops below the
// floor across ConfirmWindows consecutive sweeps, evicts the single most-idle
// dev server (never one inside the grace window). If everything is active it
// only warns. Mirrors the watchdog's seam/option style.
type RAMMonitor struct {
	bus   *events.Bus
	store storeIface
	mem   memSampler
	cfg   Config
	logf  func(format string, args ...any)

	// portPIDs reports whether a port has listeners and their PIDs. One
	// fork per call: kept for the per-port seam the tests inject, and as
	// the fallback when the bulk path is unavailable.
	portPIDs func(port int) (bool, []int, error)
	// listeners returns every TCP listener on the machine in one call, as
	// port -> holding PIDs. When non-nil it replaces the portPIDs loop for
	// the two places that probe *every* configured port at once
	// (observeActivity and snapshot); nil falls back to that loop.
	//
	// Installing a test portPIDs via WithPortPIDs sets this back to nil, on
	// purpose: a test that scripts a per-port answer must not have the
	// monitor shell out to the machine's real lsof behind its back. That is
	// what keeps every existing WithPortPIDs test untouched by the bulk path.
	listeners func() (map[int][]int, error)
	// accepted returns every established TCP connection by local port. nil, or
	// an error, means no connection evidence this sweep: servers are judged on
	// CPU alone, and their peer baseline is kept.
	accepted func() (map[int][]devserver.Conn, error)
	// timeWait returns the TIME_WAIT connections by port (closed since the
	// last sweep or the one before). Same failure rule as accepted.
	timeWait func() (map[int][]string, error)
	// idleFor reports how long a server has been idle.
	idleFor func(ws pigtail.Workspace, server string) time.Duration
	// serversFor enumerates the real configured dev servers of a workspace,
	// keyed by server name (the log-file token), not by port kind.
	serversFor func(ws pigtail.Workspace) []ServerProc
	// pathExists reports whether a workspace's worktree is still on disk.
	// Injected so tests can describe workspaces that never touch the disk.
	pathExists func(path string) bool
	// kill terminates a confirmed victim's PIDs. Injected for testability.
	kill func(pids []int)
	// wait sleeps for d unless ctx is done. Injected for instant tests.
	wait func(ctx context.Context, d time.Duration)
	// power samples the machine's power source. Injected for testability.
	power powersrc.Source
	// backend resolves a workspace's work-ref label (DisplayRef) without
	// reading BackendData here (architecture.md §8). Used by evictBattery.
	backend backend.Backend
	// cpu samples per-process CPU accounting once per sweep.
	cpu proccpu.Sampler
	// activity derives per-server idleness from CPU consumed between sweeps.
	activity *activityTracker
	// now returns the current time. Injected so tests can script exact
	// timestamps instead of racing the real clock.
	now func() time.Time

	// cwds maps listener pids to their working directory, to keep only the
	// listeners running inside a workspace. nil = attribution off.
	cwds func(pids []int) (map[int]string, error)
	// conflicts is what the last snapshot recorded (under conflictsMu).
	conflictsMu sync.Mutex
	conflicts   []PortConflict

	mu          sync.Mutex
	underRuns   int                  // consecutive under-floor sweeps
	warned      bool                 // suppress repeated warn logs while pressure persists
	capWarned   bool                 // one-shot latch for the over-cap "not fully enforced" warning
	onBattery   bool                 // debounced power state
	powerRuns   int                  // consecutive same-direction power samples (debounce)
	pins        map[string]pinEntry  // workspace:server → pin; protects from every eviction but the RAM floor (mains and battery, D9/D11)
	intentional map[string]time.Time // workspace:server → time until which a disappearance reads as a deliberate stop
	deaths      []Death              // bounded ring of servers that vanished unannounced
	lastSweep   time.Time            // when the last observation judged deaths (LastSweepAt)

	// netErrs is the last error logged per connection inventory ("lsof",
	// "time-wait"), so a durable failure — a Linux box without ss — logs once,
	// not at every sweep. Cleared when that inventory succeeds again. Only the
	// sweep touches it (observeActivity), hence no lock.
	netErrs map[string]string

	// statePath is where the tracker and the death ring survive a restart.
	// Empty disables persistence.
	statePath string
	// saveMu serializes whole saves — the read of the state and the write of
	// the file. Without it a sweep save that read the pins before an Unpin
	// could land after the Unpin's own save and resurrect the pin on disk
	// until the next sweep. Held across I/O on purpose: saves are rare and
	// small, and m.mu is never held while waiting on it.
	saveMu sync.Mutex
	// writeState writes the state file; a seam so a test can park a save
	// between its read and its write.
	writeState func(path string, s persistedState) error
	// deathsFromRestore allows a restored-but-never-observed key to be reported
	// as a death when its port is free. Only true when the state file was young
	// enough to describe a daemon redeploy rather than a machine reboot.
	deathsFromRestore bool
}

// Option customises a RAMMonitor (daemon wiring and tests).
type Option func(*RAMMonitor)

// New constructs a RAMMonitor with platform defaults. Tests override the seams.
func New(bus *events.Bus, st storeIface, opts ...Option) *RAMMonitor {
	m := &RAMMonitor{
		bus:        bus,
		store:      st,
		mem:        newPlatformMemSampler(),
		power:      powersrc.New(),
		cfg:        Defaults(),
		logf:       log.Printf,
		portPIDs:   devserver.IsPortBusy,
		listeners:  devserver.ListListeners,
		accepted:   devserver.ListAccepted,
		timeWait:   devserver.ListTimeWait,
		kill:       devserver.KillPIDs,
		serversFor: defaultServersFor,
		pathExists: defaultPathExists,
		writeState: writeStateFile,
		cpu:        proccpu.NewSampler(),
		activity:   newActivityTracker(),
		now:        time.Now,
		wait: func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		},
	}
	// idleFor is left nil above so WithIdleFunc can win; the tracker-backed
	// default is installed only when no option supplied one.
	for _, o := range opts {
		o(m)
	}
	if m.idleFor == nil {
		m.idleFor = func(ws pigtail.Workspace, server string) time.Duration {
			return m.activity.idleFor(activityKey(ws.ID, server), m.now())
		}
	}
	return m
}

// WithMemSampler overrides the memory sampler.
func WithMemSampler(s memSampler) Option { return func(m *RAMMonitor) { m.mem = s } }

// WithConfig overrides the monitor config.
func WithConfig(c Config) Option { return func(m *RAMMonitor) { m.cfg = c } }

// WithLogger overrides the log sink.
func WithLogger(f func(string, ...any)) Option { return func(m *RAMMonitor) { m.logf = f } }

// WithWaiter overrides the inter-sweep sleep (tests pass a no-op).
func WithWaiter(f func(context.Context, time.Duration)) Option {
	return func(m *RAMMonitor) { m.wait = f }
}

// WithPortPIDs overrides port→listener-PID discovery.
//
// It also disables the bulk listener inventory. A test that scripts what
// each port answers has described the whole machine as far as it is
// concerned, and leaving the bulk path installed would send the monitor to
// fork the real lsof for the sweep's two full passes — reading the
// developer's actual listeners into the test's assertions. Supplying a bulk
// seam is WithListeners' job, and the option applied last wins. It also
// disables the connection inventory, for the same reason.
func WithPortPIDs(f func(int) (bool, []int, error)) Option {
	return func(m *RAMMonitor) { m.portPIDs, m.listeners, m.accepted, m.timeWait = f, nil, nil, nil }
}

// WithListeners overrides the bulk listener inventory, and disables the
// connection inventory (a scripted machine must not be completed by the real
// lsof). Passing nil forces the per-port portPIDs loop.
func WithListeners(f func() (map[int][]int, error)) Option {
	return func(m *RAMMonitor) { m.listeners, m.accepted, m.timeWait = f, nil, nil }
}

// WithAccepted overrides the connection inventory. Apply it after
// WithPortPIDs / WithListeners, which clear it.
func WithAccepted(f func() (map[int][]devserver.Conn, error)) Option {
	return func(m *RAMMonitor) { m.accepted = f }
}

// WithTimeWait overrides the TIME_WAIT inventory. Apply it after
// WithPortPIDs / WithListeners, which clear it.
func WithTimeWait(f func() (map[int][]string, error)) Option {
	return func(m *RAMMonitor) { m.timeWait = f }
}

// WithIdleFunc overrides idle measurement.
func WithIdleFunc(f func(pigtail.Workspace, string) time.Duration) Option {
	return func(m *RAMMonitor) { m.idleFor = f }
}

// WithServersFunc overrides how a workspace's configured dev servers are
// enumerated (real server names vs. port-kind fallback).
func WithServersFunc(f func(pigtail.Workspace) []ServerProc) Option {
	return func(m *RAMMonitor) { m.serversFor = f }
}

// WithCwds turns on attribution by cwd: a listener counts for a workspace
// only when its process runs inside the workspace's path. nil (the default
// of New) keeps every listener, as before; the daemon wires
// devserver.ProcessCwds.
func WithCwds(f func(pids []int) (map[int]string, error)) Option {
	return func(m *RAMMonitor) { m.cwds = f }
}

// WithPathExists overrides the on-disk existence check for a workspace path.
func WithPathExists(f func(string) bool) Option {
	return func(m *RAMMonitor) { m.pathExists = f }
}

// WithKiller overrides the kill action.
func WithKiller(f func([]int)) Option { return func(m *RAMMonitor) { m.kill = f } }

// WithPowerSource overrides the power-source sampler (tests inject a fake).
func WithPowerSource(s powersrc.Source) Option { return func(m *RAMMonitor) { m.power = s } }

// WithBackend injects the backend used to resolve work-ref labels.
func WithBackend(b backend.Backend) Option { return func(m *RAMMonitor) { m.backend = b } }

// WithCPUSampler overrides the process CPU sampler (tests inject a scripted one).
func WithCPUSampler(s proccpu.Sampler) Option { return func(m *RAMMonitor) { m.cpu = s } }

// WithClock overrides the monitor's clock (tests script exact timestamps
// instead of racing the real clock).
func WithClock(f func() time.Time) Option { return func(m *RAMMonitor) { m.now = f } }

// WithStatePath sets where the activity tracker and death ring are persisted
// across daemon restarts. Empty (the default) disables persistence entirely,
// which is the pre-existing behaviour: every counter starts blank.
func WithStatePath(p string) Option { return func(m *RAMMonitor) { m.statePath = p } }

// LoadState seeds the tracker and the death ring from the previous daemon's
// state file. Best-effort by design: any failure leaves a blank slate, which
// costs exactly the behaviour of before this file existed. Call once, before
// the first sweep.
func (m *RAMMonitor) LoadState() {
	if m.statePath == "" {
		return
	}
	s, err := readStateFile(m.statePath)
	if err != nil {
		// A missing file is the normal first start, not worth a line.
		if !os.IsNotExist(err) {
			m.logf("rammon: ignoring unusable state file: %v", err)
		}
		return
	}

	entries := make(map[string]activityEntry, len(s.Entries))
	for _, e := range s.Entries {
		samples := make([]activitySample, 0, len(e.Samples))
		for _, p := range e.Samples {
			samples = append(samples, activitySample{at: p.At, cpu: p.CPU, newConns: p.NewConns})
		}
		// A file from before the window existed: seed the one point it does
		// know, so the first observation measures the restart gap.
		if len(samples) == 0 && !s.SavedAt.IsZero() {
			samples = append(samples, activitySample{at: s.SavedAt, cpu: e.LastCPU})
		}
		entries[e.Key] = activityEntry{
			lastCPU: e.LastCPU, lastActive: e.LastActive, port: e.Port, pids: e.PIDs,
			samples: samples, peers: e.Peers, peersKnown: e.PeersKnown, fresh: e.Fresh,
		}
	}
	m.activity.restore(entries)

	// Only a state file young enough to describe a redeploy may turn a freed
	// port into a reported death; see deathRestoreWindow.
	fresh := m.now().Sub(s.SavedAt) < deathRestoreWindow
	m.mu.Lock()
	m.deathsFromRestore = fresh
	if fresh {
		m.deaths = append(m.deaths, s.Deaths...)
	}
	// Pins are restored whatever the file's age — unlike deaths, "until I
	// unpin" has no age. A timed pin past its expiry, or an entry with no key,
	// is dropped rather than guessed at.
	now := m.now()
	for _, p := range s.Pins {
		if p.Key == "" || (!p.Durable && !now.Before(p.Until)) {
			continue
		}
		if m.pins == nil {
			m.pins = map[string]pinEntry{}
		}
		m.pins[p.Key] = pinEntry{until: p.Until, durable: p.Durable}
	}
	restoredPins := len(m.pins)
	m.mu.Unlock()

	m.logf("rammon: restored %d server(s), %d pin(s) from %s (deaths reportable: %v)",
		len(entries), restoredPins, m.statePath, fresh)
}

// SaveState writes the tracker and the death ring out. Called after every
// sweep, so a restart at any moment finds a picture at most one interval old.
func (m *RAMMonitor) SaveState() error {
	if m.statePath == "" {
		return nil
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	snap := m.activity.entriesSnapshot()
	entries := make([]persistedEntry, 0, len(snap))
	for k, e := range snap {
		var samples []persistedSample
		for _, p := range e.samples {
			samples = append(samples, persistedSample{At: p.at, CPU: p.cpu, NewConns: p.newConns})
		}
		entries = append(entries, persistedEntry{
			Key: k, LastCPU: e.lastCPU, LastActive: e.lastActive, Port: e.port, PIDs: e.pids,
			Samples: samples, Peers: e.peers, PeersKnown: e.peersKnown, Fresh: e.fresh,
		})
	}
	m.mu.Lock()
	deaths := make([]Death, len(m.deaths))
	copy(deaths, m.deaths)
	now := m.now()
	var pins []persistedPin
	for k, e := range m.pins {
		if !e.durable && !now.Before(e.until) {
			continue // expired: nothing to carry
		}
		pins = append(pins, persistedPin{Key: k, Until: e.until, Durable: e.durable})
	}
	m.mu.Unlock()

	return m.writeState(m.statePath, persistedState{
		Version: stateVersion,
		SavedAt: now,
		Entries: entries,
		Deaths:  deaths,
		Pins:    pins,
	})
}

// defaultPathExists reports whether a workspace's worktree is still on disk.
//
// Deliberately conservative: only a definite "not there" (ENOENT) counts as
// gone. A path that cannot be read for any other reason — permissions, an
// unmounted volume, a transient I/O error — is treated as present, because the
// cost of being wrong is asymmetric. Skipping a live workspace hides its
// servers from the monitor; probing a dead one only wastes two port probes.
func defaultPathExists(path string) bool {
	if path == "" {
		return true // nothing to check
	}
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// liveWorkspaces lists the workspaces whose worktree is still on disk.
//
// A removed worktree keeps its row, and with it its recorded ports. Worse,
// defaultServersFor resolves the *container's* pigtail.yaml by walking up from
// the (now absent) path, so it still returns a full set of servers — meaning
// every deleted worktree kept costing two port probes per pass, forever. One
// stat per workspace replaces them: microseconds against ~100ms of lsof.
//
// The rows themselves are left alone. Collecting them is `POST /workspaces/gc`,
// a deliberate, user-triggered act; a background sweep must not delete records
// on the strength of a stat.
func (m *RAMMonitor) liveWorkspaces() ([]pigtail.Workspace, error) {
	wss, err := m.store.ListWorkspaces("")
	if err != nil {
		return nil, err
	}
	out := wss[:0:0]
	for _, ws := range wss {
		if m.pathExists(ws.Path) {
			out = append(out, ws)
		}
	}
	return out, nil
}

// defaultServersFor resolves a workspace's real dev servers from its project
// config, keyed by server name (the log-file token). When no config is
// resolvable (e.g. a test workspace with no on-disk pigtail.yaml), it falls
// back to treating port-kind keys as server names — correct for the default
// config where each server's name equals its port kind.
func defaultServersFor(ws pigtail.Workspace) []ServerProc {
	ctx, err := devserver.ResolveContextFromPath(ws.Path)
	var cfg *pigtail.ProjectConfig
	if err == nil && ctx != nil {
		cfg = ctx.Config
	}
	ports := devserver.WorkspacePorts(ws.Path, cfg, ws.Ports)
	if cfg == nil || len(cfg.Servers) == 0 {
		out := make([]ServerProc, 0, len(ports))
		for kind, port := range ports {
			out = append(out, ServerProc{Name: kind, Kind: kind, Port: port, DBPort: ws.Ports[kind]})
		}
		return out
	}
	var out []ServerProc
	for name, spec := range cfg.Servers {
		port := ports[spec.PortKind]
		if port == 0 {
			continue
		}
		out = append(out, ServerProc{Name: name, Kind: spec.PortKind, Port: port, DBPort: ws.Ports[spec.PortKind]})
	}
	return out
}

// sampleOnBattery samples the power source and debounces transitions over
// ConfirmWindows consecutive agreeing samples, so a brief unplug/replug does
// not flip the mode. Returns the debounced on-battery state. Always false when
// battery mode is disabled. A sampling error is treated as mains (fail-safe).
func (m *RAMMonitor) sampleOnBattery() bool {
	m.mu.Lock()
	enabled := m.cfg.BatteryEnabled
	m.mu.Unlock()
	if !enabled {
		return false
	}
	sample := powersrc.SampleOnBattery(m.power) // shells out — must NOT hold m.mu
	m.mu.Lock()
	defer m.mu.Unlock()
	if sample == m.onBattery {
		m.powerRuns = 0 // already in this state; nothing to confirm
		return m.onBattery
	}
	m.powerRuns++
	if m.powerRuns >= m.cfg.ConfirmWindows {
		m.onBattery = sample
		m.powerRuns = 0
		// No pin is cleared here any more. A pin protects from idle purge,
		// the cap and stop --idle, which all run on mains: clearing at replug
		// dropped a protection the user asked for, in the very mode those
		// evictions act. Timed pins end on their own; durable ones on Unpin.
	}
	return m.onBattery
}

// Run sweeps until ctx is cancelled. No-op when disabled.
func (m *RAMMonitor) Run(ctx context.Context) {
	if !m.cfg.Enabled {
		return
	}
	for {
		m.wait(ctx, m.cfg.Interval)
		if ctx.Err() != nil {
			// Deliberately no save on the way out. The state is at most one
			// interval old already, which the restore path is built to accept,
			// so a shutdown write buys almost no freshness — and it lands on a
			// path where the directory underneath may already be going away.
			return
		}
		m.Sweep(ctx)
		m.persist()
	}
}

// persist writes the state out, logging rather than propagating a failure: a
// monitor that stopped sweeping because it could not write a cache file would
// trade a small loss of continuity for a large one.
func (m *RAMMonitor) persist() {
	if err := m.SaveState(); err != nil {
		m.logf("rammon: save state: %v", err)
	}
}

// Sweep runs one pressure check. Above the floor it resets the confirmation
// counter. Below the floor it counts the run; once ConfirmWindows consecutive
// under-floor sweeps accumulate, it asks decide() for a victim and either
// evicts the most-idle server or warns (once) that nothing is safely evictable.
func (m *RAMMonitor) Sweep(ctx context.Context) {
	avail, err := m.mem.available()
	if err != nil {
		// Fail-safe: a sampler error must never trigger an eviction.
		m.logf("rammon: available: %v", err)
		return
	}

	if !m.observeActivity(m.now()) {
		return // no fresh activity picture — never evict on uncertainty
	}

	// Always-on idle purge: independent of power source and the RAM floor.
	// No-op when IdlePurgeGrace == 0. Runs before the battery/mains paths,
	// which remain unchanged.
	// One probe pass per sweep. purgeIdle and capServers each used to call
	// snapshot() themselves, and the battery/mains paths a third time, so a
	// single sweep shelled out to lsof over the same ports three or four times
	// for one decision. They now share this slice.
	//
	// Re-snapshotting did buy one thing: each stage saw the servers the
	// previous stage had just killed as gone. withoutServers reproduces that
	// exactly, without the probes. What is deliberately not reproduced is
	// sub-second freshness about the outside world — a server that dies on its
	// own mid-sweep is picked up on the next one, which is what the interval
	// means in the first place.
	stats := m.snapshot()
	stats = withoutServers(stats, m.purgeIdle(ctx, avail, stats))
	stats = withoutServers(stats, m.capServers(ctx, avail, stats))

	onBattery := m.sampleOnBattery()

	if onBattery {
		// Battery eviction first: it only ever takes an unpinned server past
		// BatteryGrace, so a pin yields to the floor below only when no
		// unpinned victim exists — pins are the last resort.
		now := time.Now()
		eligible := stats[:0:0]
		for _, s := range stats {
			if !m.processPinned(s, stats, now) {
				eligible = append(eligible, s)
			}
		}
		d := decide(eligible, avail, m.cfg.FloorBytes, m.cfg.Grace, m.cfg.BatteryGrace, true)
		if d.Action == ActionEvict {
			m.evictBattery(ctx, *d.Victim, avail)
			// avail was read before this kill: judging the floor *under* it
			// would double-kill on one stale reading, so the floor waits for
			// the next sweep. Above the floor the reading stays valid — a kill
			// only raises available memory — so the episode is over: reset
			// the counter exactly as floorStep would have. Otherwise an
			// under-floor sweep before and one after would read as two
			// consecutive ones and kill a pinned server on a broken streak.
			if avail >= m.cfg.FloorBytes {
				m.mu.Lock()
				m.underRuns, m.warned = 0, false
				m.mu.Unlock()
			}
			return
		}
	}
	// D11: the RAM floor applies on battery as on mains, pinned servers
	// included (D9) — same decide, same confirmation, same grace.
	m.floorStep(stats, avail)
}

// floorStep is the RAM-floor eviction: above the floor it resets the
// confirmation counter; under it for ConfirmWindows consecutive sweeps it
// evicts the most-idle server past Grace — pinned or not — or warns once that
// everything is active. It reads the unfiltered snapshot on purpose: under
// the floor the machine comes first (D9, D11).
func (m *RAMMonitor) floorStep(stats []ServerStat, avail uint64) {
	if avail >= m.cfg.FloorBytes {
		m.mu.Lock()
		m.underRuns, m.warned = 0, false
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	m.underRuns++
	confirmed := m.underRuns >= m.cfg.ConfirmWindows
	m.mu.Unlock()
	if !confirmed {
		return
	}

	d := decide(stats, avail, m.cfg.FloorBytes, m.cfg.Grace, m.cfg.BatteryGrace, false)
	switch d.Action {
	case ActionEvict:
		m.logf("rammon: evicting %s:%s port=%d mem=%dMiB idle=%s avail=%dMiB",
			d.Victim.WorkspaceID, d.Victim.Server, d.Victim.Port, d.Victim.MemBytes>>20,
			d.Victim.IdleFor.Round(time.Second), avail>>20)
		m.MarkIntentionalStop(d.Victim.WorkspaceID, d.Victim.Server)
		m.kill(d.Victim.PIDs)
		m.publish(*d.Victim, avail)
		m.mu.Lock()
		m.underRuns = 0
		m.mu.Unlock()
	case ActionWarn:
		m.mu.Lock()
		first := !m.warned
		m.warned = true
		m.mu.Unlock()
		if first {
			m.logf("rammon: memory pressure (avail=%dMiB < floor=%dMiB) but all %d servers are active — not evicting",
				avail>>20, m.cfg.FloorBytes>>20, len(stats))
		}
	}
}

// evictBattery performs a battery-mode eviction. The victim was already
// filtered against the pin/snooze set in Sweep, so this always kills —
// silently, with no prompt and no banner (user's choice). The disappearance is
// surfaced by the menu-bar app, not by a notification.
func (m *RAMMonitor) evictBattery(_ context.Context, victim ServerStat, avail uint64) {
	m.logf("rammon: battery-evicting %s:%s port=%d mem=%dMiB idle=%s (silent)",
		victim.WorkspaceID, victim.Server, victim.Port, victim.MemBytes>>20,
		victim.IdleFor.Round(time.Second))
	m.MarkIntentionalStop(victim.WorkspaceID, victim.Server)
	m.kill(victim.PIDs)
	m.publish(victim, avail)
}

// IdlePurgeGrace reports the current always-on idle-purge threshold (0 = off).
func (m *RAMMonitor) IdlePurgeGrace() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.IdlePurgeGrace
}

// SetIdlePurgeGrace sets the always-on idle-purge threshold for the running
// daemon session (in-memory; reverts to the flag default on restart). A
// non-positive value disables the purge.
func (m *RAMMonitor) SetIdlePurgeGrace(d time.Duration) {
	m.mu.Lock()
	m.cfg.IdlePurgeGrace = d
	m.mu.Unlock()
}

// purgeIdle evicts every running server idle at least IdlePurgeGrace, skipping
// pinned (snoozed) servers. Silent — like battery eviction, the disappearance
// is surfaced by the menu-bar app, not by a notification. No-op when the
// threshold is non-positive.
// It reads the sweep's snapshot rather than taking its own, and returns the
// servers it killed so the caller can keep the slice honest for the next stage.
func (m *RAMMonitor) purgeIdle(_ context.Context, avail uint64, stats []ServerStat) []ServerStat {
	m.mu.Lock()
	grace := m.cfg.IdlePurgeGrace
	m.mu.Unlock()
	if grace <= 0 {
		return nil
	}
	now := time.Now()
	var killed []ServerStat
	for _, s := range idlePurgeVictims(stats, grace) {
		if m.processPinned(s, stats, now) {
			continue
		}
		m.logf("rammon: idle-purging %s:%s port=%d mem=%dMiB idle=%s (silent)",
			s.WorkspaceID, s.Server, s.Port, s.MemBytes>>20,
			s.IdleFor.Round(time.Second))
		m.MarkIntentionalStop(s.WorkspaceID, s.Server)
		m.kill(s.PIDs)
		m.publish(s, avail)
		killed = append(killed, s)
	}
	return killed
}

// capMinIdle is the minimum idle time before the server-count cap will evict a
// server, so a server touched in the last few minutes is never killed mid-use
// when the cap trips.
const capMinIdle = 5 * time.Minute

// capServers enforces Config.MaxServers: when more than MaxServers dev servers
// are live, it evicts the most-idle ones (idle >= capMinIdle) down to the cap,
// skipping pinned servers (which still count toward the cap). Silent, like
// idle-purge and battery eviction. No-op when MaxServers <= 0.
// It reads the sweep's snapshot rather than taking its own, and returns the
// servers it evicted.
func (m *RAMMonitor) capServers(_ context.Context, avail uint64, stats []ServerStat) []ServerStat {
	m.mu.Lock()
	max := m.cfg.MaxServers
	m.mu.Unlock()
	if max <= 0 {
		return nil
	}
	// The cap counts running processes, not the names a pigtail.yaml gives
	// them: a project declaring two servers on one port_kind (monorepo's `dev`
	// and `web`) yields two stats for one process, and counting both spends
	// two slots on one server. See distinctProcesses.
	procs := distinctProcesses(stats)
	if len(procs) <= max {
		m.mu.Lock()
		m.capWarned = false
		m.mu.Unlock()
		return nil
	}
	now := time.Now()
	// Pinned servers count toward the cap but are never evicted.
	var evictable []ServerStat
	for _, s := range procs {
		if !m.processPinned(s, stats, now) {
			evictable = append(evictable, s)
		}
	}
	want := len(procs) - max
	victims := overCapVictims(evictable, want, capMinIdle)
	for _, s := range victims {
		m.logf("rammon: over-cap evicting %s:%s port=%d mem=%dMiB idle=%s (max=%d, silent)",
			s.WorkspaceID, s.Server, s.Port, s.MemBytes>>20,
			s.IdleFor.Round(time.Second), max)
		m.MarkIntentionalStop(s.WorkspaceID, s.Server)
		m.kill(s.PIDs)
		m.publish(s, avail)
	}
	if len(victims) < want {
		m.mu.Lock()
		first := !m.capWarned
		m.capWarned = true
		m.mu.Unlock()
		if first {
			m.logf("rammon: %d servers over cap=%d but only %d idle-enough to evict — cap not fully enforced",
				len(procs), max, len(victims))
		}
	} else {
		m.mu.Lock()
		m.capWarned = false
		m.mu.Unlock()
	}
	// Killing a representative kills the process, so every label of that
	// process is gone — report them all, or the twins linger in the sweep's
	// slice as servers that are still alive.
	return labelsOf(stats, victims)
}

// labelsOf returns every stat sharing a port with one of the given servers —
// the full set of names under which the killed processes were listed.
func labelsOf(stats, killed []ServerStat) []ServerStat {
	if len(killed) == 0 {
		return killed
	}
	ports := make(map[int]bool, len(killed))
	for _, s := range killed {
		ports[s.Port] = true
	}
	out := stats[:0:0]
	for _, s := range stats {
		if ports[s.Port] {
			out = append(out, s)
		}
	}
	return out
}

// withoutServers returns stats minus the given servers, matched on
// (workspace, server) — the same identity the snooze set uses.
func withoutServers(stats, remove []ServerStat) []ServerStat {
	if len(remove) == 0 {
		return stats
	}
	gone := make(map[string]bool, len(remove))
	for _, s := range remove {
		gone[s.WorkspaceID+":"+s.Server] = true
	}
	out := stats[:0:0]
	for _, s := range stats {
		if !gone[s.WorkspaceID+":"+s.Server] {
			out = append(out, s)
		}
	}
	return out
}

// StopServer kills a running server's PIDs immediately. Returns false if no
// such running server is found.
func (m *RAMMonitor) StopServer(workspaceID, server string) bool {
	for _, s := range m.snapshot() {
		if s.WorkspaceID == workspaceID && s.Server == server {
			m.logf("rammon: stopping %s:%s port=%d (menu-bar)", workspaceID, server, s.Port)
			m.MarkIntentionalStop(workspaceID, server)
			m.kill(s.PIDs)
			return true
		}
	}
	return false
}

// StopAll kills every running server's PIDs and returns the (ws, server) pairs
// stopped. Every stop is marked deliberate: the menu bar's "Stop all" would
// otherwise produce a volley of false deaths on the very next sweep.
func (m *RAMMonitor) StopAll() [][2]string {
	var stopped [][2]string
	for _, s := range m.snapshot() {
		m.MarkIntentionalStop(s.WorkspaceID, s.Server)
		m.kill(s.PIDs)
		stopped = append(stopped, [2]string{s.WorkspaceID, s.Server})
	}
	return stopped
}

// StopIdleResult is one server's outcome in a --idle sweep. Every live server
// appears, stopped or not: the point of the command is to say what it spared
// and why, unlike the silent evictions the daemon performs on its own.
type StopIdleResult struct {
	WorkspaceID string
	Server      string
	Port        int
	IdleSeconds int
	Stopped     bool
	Reason      string // why it was kept: "active" or "pinned"; empty when stopped
}

// StopIdle stops every unpinned server idle at least grace, and reports the
// full picture including the ones it left alone. This is the deliberate
// inverse of the daemon's own silent idle-purge/battery/cap evictions: it
// names every server it spared and why, instead of only what it killed.
func (m *RAMMonitor) StopIdle(grace time.Duration) []StopIdleResult {
	now := m.now()
	stats := m.snapshot()
	victims := map[string]bool{}
	for _, s := range idlePurgeVictims(stats, grace) {
		victims[m.snoozeKey(s)] = true
	}
	out := make([]StopIdleResult, 0, len(stats))
	for _, s := range stats {
		r := StopIdleResult{
			WorkspaceID: s.WorkspaceID,
			Server:      s.Server,
			Port:        s.Port,
			IdleSeconds: int(s.IdleFor.Seconds()),
		}
		switch {
		case !victims[m.snoozeKey(s)]:
			r.Reason = "active"
		case m.processPinned(s, stats, now):
			r.Reason = "pinned"
		default:
			m.logf("rammon: --idle stopping %s:%s port=%d idle=%s",
				s.WorkspaceID, s.Server, s.Port, s.IdleFor.Round(time.Second))
			m.MarkIntentionalStop(s.WorkspaceID, s.Server)
			m.kill(s.PIDs)
			r.Stopped = true
		}
		out = append(out, r)
	}
	return out
}

// Inspect returns the current running-server snapshot plus system headroom.
// Used by the read-only /servers endpoint; never evicts.
func (m *RAMMonitor) Inspect() (servers []ServerStat, availableBytes, floorBytes uint64) {
	avail, err := m.mem.available()
	if err != nil {
		m.logf("rammon: inspect available: %v", err)
	}
	return m.snapshot(), avail, m.cfg.FloorBytes
}

// Now returns the monitor's own clock (overridable via WithClock). Callers
// that ask the monitor to make a time-bounded decision — e.g. RecentDeaths,
// which uses "now" to decide what still falls inside the reporting window —
// must read the same clock the monitor itself decided against, not
// time.Now(): a caller-side wall clock would silently diverge from an
// injected test clock and make the decision untestable.
func (m *RAMMonitor) Now() time.Time { return m.now() }

// BatteryView is the battery-mode status for the /battery endpoint.
type BatteryView struct {
	OnBattery    bool                `json:"on_battery"`
	Enabled      bool                `json:"enabled"`
	GraceSeconds int                 `json:"grace_seconds"`
	Servers      []BatteryServerView `json:"servers"`
}

// BatteryServerView is one running server's battery-eviction status.
type BatteryServerView struct {
	WorkspaceID    string `json:"workspace_id"`
	Server         string `json:"server"`
	Port           int    `json:"port"`
	IdleSeconds    int    `json:"idle_seconds"`
	EvictInSeconds int    `json:"evict_in_seconds"`
	Spared         bool   `json:"spared"`
}

// BatteryStatus reports the current power state, mode config, and per-server
// idle countdown to battery eviction. Never evicts (read-only).
func (m *RAMMonitor) BatteryStatus() BatteryView {
	m.mu.Lock()
	onBattery := m.onBattery
	enabled := m.cfg.BatteryEnabled
	m.mu.Unlock()
	now := time.Now()
	v := BatteryView{
		OnBattery:    onBattery,
		Enabled:      enabled,
		GraceSeconds: int(m.cfg.BatteryGrace.Seconds()),
		Servers:      []BatteryServerView{},
	}
	servers := m.snapshot()
	for _, s := range servers {
		evictIn := m.cfg.BatteryGrace - s.IdleFor
		if evictIn < 0 {
			evictIn = 0
		}
		v.Servers = append(v.Servers, BatteryServerView{
			WorkspaceID:    s.WorkspaceID,
			Server:         s.Server,
			Port:           s.Port,
			IdleSeconds:    int(s.IdleFor.Seconds()),
			EvictInSeconds: int(evictIn.Seconds()),
			Spared:         m.processPinned(s, servers, now),
		})
	}
	return v
}

// SetBatteryEnabled flips battery mode for the running daemon session
// (in-memory; reverts to the flag default on restart).
func (m *RAMMonitor) SetBatteryEnabled(on bool) {
	m.mu.Lock()
	m.cfg.BatteryEnabled = on
	m.mu.Unlock()
}

// activityKey identifies one server across sweeps.
func activityKey(workspaceID, server string) string { return workspaceID + ":" + server }

// observeActivity samples process CPU once and folds it into the activity
// tracker for every live server, then forgets servers that are gone. Returns
// false when sampling failed: the caller must skip eviction for that sweep
// rather than act on a stale or empty picture.
//
// It is also where death detection lives, because it is the only place that
// holds both pictures at once: the keys seen at the previous sweep (the
// tracker) and the ones alive now. A key that was there, is gone, and carries
// no deliberate-stop mark is a server that died on its own.
func (m *RAMMonitor) observeActivity(now time.Time) bool {
	procs, err := m.cpu.Sample()
	if err != nil {
		m.logf("rammon: sample process CPU: %v", err)
		return false
	}
	m.mu.Lock()
	window, threshold, netSignal := m.cfg.IdleWindow, m.cfg.IdleCPUThresholdPercent, m.cfg.NetSignal
	m.mu.Unlock()

	wss, err := m.liveWorkspaces()
	if err != nil {
		m.logf("rammon: list workspaces: %v", err)
		return false
	}
	m.prunePins(wss)
	live := map[string]bool{}     // seen listening this sweep
	expected := map[string]bool{} // configured on a workspace the daemon still knows
	unknown := map[string]bool{}  // probed, but the answer is not "free"

	// Same shape as snapshot(): enumerate the pass's (workspace, server)
	// pairs first so every port it needs is answered in one probe, instead
	// of one fork per port in series. This was the second of the sweep's two
	// full inventories — at 15s pacing and ~0.12s per fork over 102 ports,
	// the two together cost ~25s, so the sweep never slept and competed with
	// every incoming request.
	type pair struct {
		key  string
		port int
		path string
	}
	var pairs []pair
	var portList []int
	for _, ws := range wss {
		for _, srv := range m.serversFor(ws) {
			key := activityKey(ws.ID, srv.Name)
			expected[key] = true
			pairs = append(pairs, pair{key: key, port: srv.Port, path: ws.Path})
			portList = append(portList, srv.Port)
		}
	}
	probes := m.probePorts(portList)
	// A port held only by another worktree's process is not alive for this
	// workspace; a failed cwd probe leaves every pair undecided.
	cwds, cwdErr := m.cwdsOf(probes)

	var conns map[int][]devserver.Conn
	var tw map[int][]string
	connsKnown := false
	if netSignal && anyLive(probes) {
		conns, tw, connsKnown = m.connectionInventory()
	}

	for _, p := range pairs {
		key := p.key
		pr, _ := m.attribute(probes[p.port], p.path, cwds, cwdErr)
		switch {
		case pr.err != nil:
			// An lsof failure is not evidence of a free port. Leave the
			// entry alone: undecided, neither alive nor dead. With the bulk
			// path this is all-or-nothing — one failed inventory marks every
			// port of the pass undecided, which is exactly the intent.
			unknown[key] = true
			continue
		case !pr.busy:
			continue
		case len(pr.pids) == 0:
			// Listening, but no PID to attribute CPU to — alive enough
			// not to be a death, too vague to fold into the tracker.
			//
			// Unreachable through the bulk path, which derives busy from
			// len(pids) > 0 and so can never report one without the other.
			// Kept, not deleted: the per-port seam still feeds this branch —
			// in every test that injects portPIDs, and in the fallback a
			// rollback of the bulk path would restore — and devserver's own
			// probe can legitimately answer busy with an empty pid set when
			// lsof prints a line it cannot read as an integer.
			unknown[key] = true
			continue
		}
		var cpu float64
		for _, pid := range pr.pids {
			cpu += proccpu.SubtreeCPU(procs, pid)
		}
		live[key] = true
		// The listener set travels with the CPU figure: a cumulative
		// counter only means anything next to the identity of what
		// produced it. observe compares the two and rebases the entry when
		// they disagree — a restart, whether it happened while the daemon
		// was down or between two of its sweeps. The peer set travels with
		// it for the same reason: connections belong to a process.
		m.activity.observeWith(key, observation{
			port: p.port, pids: pr.pids, cpu: cpu,
			peers: peersFor(conns[p.port], tw[p.port], pr.pids), peersKnown: connsKnown,
			netOff: !netSignal,
		}, now, window, threshold)
	}
	// The port is free again, so it is only knowable from the last
	// observation — read it before dropping the entry.
	ports := map[string]int{}
	var vanished []string
	for _, key := range m.activity.keys() {
		if live[key] || unknown[key] {
			continue
		}
		port := m.activity.portFor(key)
		restored := !m.activity.confirmed(key)
		m.activity.forget(key)
		if !expected[key] {
			continue // workspace or server config went away: bookkeeping, not a death
		}
		if restored && !m.deathsFromRestore {
			// Never observed by this daemon, and the state file was too old to
			// tell a redeploy from a reboot. Bookkeeping, not a death.
			continue
		}
		ports[key] = port
		vanished = append(vanished, key)
	}
	// Names sharing a port are one process: stops kill by port, so one mark on
	// any of its names covers them all, and an unannounced end is one death.
	// Every mark in the group is consumed — no short-circuit — so a twin's mark
	// cannot outlive the shutdown and swallow a later, real death.
	for _, group := range groupByPort(vanished, func(k string) int { return ports[k] }) {
		intentional := false
		for _, key := range group {
			if m.consumeIntentionalStop(key, now) {
				intentional = true
			}
		}
		if intentional {
			continue
		}
		wsID, _ := splitActivityKey(group[0])
		servers := make([]string, len(group))
		for i, key := range group {
			_, servers[i] = splitActivityKey(key)
		}
		m.noteDeath(Death{WorkspaceID: wsID, Server: servers[0], Servers: servers, Port: ports[group[0]], At: now})
	}
	m.purgeIntentionalStops(now)
	m.mu.Lock()
	m.lastSweep = now
	m.mu.Unlock()
	return true
}

// portProbe is one port's answer, mirroring devserver.IsPortBusy's triple so
// the per-port and bulk paths are interchangeable at the call site.
type portProbe struct {
	busy bool
	pids []int
	err  error
}

// probePorts answers a whole list of ports at once.
//
// This is the fix for the route that rebuilt its state by interrogating the
// machine: snapshot() and observeActivity each walk every live workspace's
// every configured port, and each used to fork one lsof per port, in series,
// inside whatever called them — a GET /servers, or the 15-second sweep.
// Measured 2026-09-21 on 51 live workspaces / 102 port entries: GET /servers
// took 26.6 / 8.9 / 7.4 s, while one bulk inventory takes 0.10 s.
//
// It returns PIDs, not a boolean. snapshot() attributes subtree memory with
// sumTree(procs, pids) and observeActivity attributes CPU the same way, so a
// remedy that answered only "busy" would silently empty the memory column.
//
// A failed bulk probe marks *every* port undecided rather than free. That is
// the same rule the per-port path already holds (devserver.portProbeResult)
// and it is the one that matters here: observeActivity records a death for a
// server whose port went quiet, so an lsof that could not run must never
// read as an empty machine.
func (m *RAMMonitor) probePorts(ports []int) map[int]portProbe {
	out := make(map[int]portProbe, len(ports))
	if m.listeners != nil {
		inv, err := m.listeners()
		for _, port := range ports {
			if err != nil {
				out[port] = portProbe{err: err}
				continue
			}
			pids := inv[port]
			out[port] = portProbe{busy: len(pids) > 0, pids: pids}
		}
		if err != nil {
			m.logf("rammon: bulk listener probe failed, %d port(s) undecided this pass: %v",
				len(ports), err)
		}
		return out
	}
	for _, port := range ports {
		busy, pids, err := m.portPIDs(port)
		out[port] = portProbe{busy: busy, pids: pids, err: err}
	}
	return out
}

// snapshot enumerates every (workspace, busy port) pair into a ServerStat,
// attributing subtree memory and idleness.
func (m *RAMMonitor) snapshot() []ServerStat {
	wss, err := m.liveWorkspaces()
	if err != nil {
		m.logf("rammon: list workspaces: %v", err)
		return nil
	}
	var procs map[int]memStat
	if snap, err := m.mem.procs(); err == nil {
		procs = snap
	}
	// Collect the (workspace, server) pairs first, so every port this pass
	// needs is known before a single probe is made and the whole set can be
	// answered in one call.
	type pair struct {
		ws  pigtail.Workspace
		srv ServerProc
	}
	var pairs []pair
	var ports []int
	for _, ws := range wss {
		for _, srv := range m.serversFor(ws) {
			pairs = append(pairs, pair{ws: ws, srv: srv})
			ports = append(ports, srv.Port)
		}
	}
	probes := m.probePorts(ports)
	cwds, cwdErr := m.cwdsOf(probes)
	if cwdErr != nil {
		m.logf("rammon: cwd probe failed, %d pair(s) undecided: %v", len(pairs), cwdErr)
	}
	var conflicts []PortConflict
	// .env.local and the registry disagree: recorded once per (workspace,
	// kind) — twins on one port are one divergence — whether or not the
	// server runs.
	diverged := map[[2]string]bool{}
	for _, p := range pairs {
		srv := p.srv
		if srv.DBPort == 0 || srv.Port == srv.DBPort {
			continue
		}
		k := [2]string{p.ws.ID, srv.Kind}
		if diverged[k] {
			continue
		}
		diverged[k] = true
		conflicts = append(conflicts, PortConflict{
			WorkspaceID: p.ws.ID, Path: p.ws.Path, Server: srv.Name, Kind: srv.Kind, Port: srv.Port,
			RegisteredPort: srv.DBPort, Reason: ConflictEnvDiverges,
		})
	}

	// Left as a nil slice when nothing is listening, exactly as before: the
	// zero value is what every caller has always been handed for an idle
	// machine.
	var out []ServerStat
	for _, p := range pairs {
		ws, srv := p.ws, p.srv
		pr, foreign := m.attribute(probes[srv.Port], ws.Path, cwds, cwdErr)
		if !pr.busy && len(foreign) > 0 {
			// Held, but from another worktree: never listed, stopped or
			// evicted under this workspace — recorded as a conflict.
			conflicts = append(conflicts, foreignConflict(ws, srv, foreign, wss))
			continue
		}
		// Unchanged reading of the two non-answers: an undecided probe and a
		// free port both mean "no ServerStat", they never mean "dead".
		if pr.err != nil || !pr.busy {
			continue
		}
		var mem uint64
		if procs != nil {
			mem = sumTree(procs, pr.pids)
		}
		var branch string
		if m.backend != nil {
			branch = m.backend.DisplayRef(&ws)
		}
		out = append(out, ServerStat{
			WorkspaceID: ws.ID,
			Server:      srv.Name,
			Branch:      branch,
			Port:        srv.Port,
			PIDs:        pr.pids,
			MemBytes:    mem,
			IdleFor:     m.idleFor(ws, srv.Name),
			Owner:       devserver.ReadOwner(ws.Path, srv.Name),
			// Confirmed unless the entry came from the previous daemon's
			// state file and this one has not looked yet. An unknown key —
			// nothing restored, or an injected idle seam — is confirmed, so
			// every path that never persisted anything is untouched.
			IdleConfirmed: m.activity.confirmed(activityKey(ws.ID, srv.Name)),
		})
	}
	for i := range out {
		out[i].PinnedUntil, out[i].PinDurable = m.pinState(out[i])
	}
	m.setConflicts(conflicts)
	return out
}

// publish emits a resources.server_evicted event for SSE consumers (TUI).
func (m *RAMMonitor) publish(s ServerStat, avail uint64) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(pigtail.Event{
		Type: pigtail.EventServerEvicted,
		Data: map[string]interface{}{
			"workspace_id":    s.WorkspaceID,
			"server":          s.Server,
			"port":            s.Port,
			"pids":            s.PIDs,
			"rss_bytes":       s.MemBytes, // key kept for wire compatibility; the value is the footprint (see ServerStat.MemBytes)
			"idle_seconds":    int(s.IdleFor.Seconds()),
			"available_bytes": avail,
		},
	})
}

// peersFor builds a server's peer set: the client endpoints of the open
// connections held by its own listener PIDs (a connection held by another
// process on the same local port number is a client whose ephemeral port
// collides, not the server's), plus its TIME_WAIT connections as "tw:" keys.
// TIME_WAIT has no owner, so it is matched by port alone.
func peersFor(conns []devserver.Conn, timeWait []string, pids []int) []string {
	out := make([]string, 0, len(conns)+len(timeWait))
	for _, k := range timeWait {
		out = append(out, "tw:"+k)
	}
	for _, c := range conns {
		for _, p := range pids {
			if c.PID == p {
				out = append(out, c.Remote)
				break
			}
		}
	}
	return out
}

// NetSignal reports whether the opt-in network signal is on.
func (m *RAMMonitor) NetSignal() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.NetSignal
}

// anyLive reports whether one probed port has a listener to attribute
// connections to. Without one the connection inventories have nothing to
// say, and are not run.
func anyLive(probes map[int]portProbe) bool {
	for _, pr := range probes {
		if pr.err == nil && pr.busy && len(pr.pids) > 0 {
			return true
		}
	}
	return false
}

// connectionInventory runs the network signal's two inventories (only when
// Config.NetSignal is on). A failure degrades the sweep to CPU-only judgement
// — never a skipped sweep, never "no clients" — and keeps the peer baselines.
// Both inventories or neither: a peer set built from one of them would drop
// the other's keys, and their return would read as new connections.
func (m *RAMMonitor) connectionInventory() (map[int][]devserver.Conn, map[int][]string, bool) {
	if m.accepted == nil {
		return nil, nil, false
	}
	conns, err := m.accepted()
	if m.netFailed("lsof", "rammon: connection inventory failed, CPU-only until it recovers: %v", err) {
		return nil, nil, false
	}
	if m.timeWait == nil {
		return conns, nil, true
	}
	tw, err := m.timeWait()
	if m.netFailed("time-wait", "rammon: time-wait inventory failed, CPU-only until it recovers: %v", err) {
		return nil, nil, false
	}
	return conns, tw, true
}

// netFailed records the outcome of one inventory and reports whether it
// failed. A failure is logged only when its message differs from the last one
// logged for that inventory: a durable failure logs once, a new kind of error
// or a failure after a recovery logs again.
func (m *RAMMonitor) netFailed(source, format string, err error) bool {
	if err == nil {
		delete(m.netErrs, source)
		return false
	}
	if m.netErrs == nil {
		m.netErrs = map[string]string{}
	}
	if msg := err.Error(); m.netErrs[source] != msg {
		m.netErrs[source] = msg
		m.logf(format, err)
	}
	return true
}
