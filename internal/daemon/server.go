// Package daemon runs the pigtail background daemon: Unix socket + HTTP server,
// event bus, state store, and endpoint handlers.
package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/agent"
	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/buildinfo"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon/activity"
	"github.com/aloysbr/pigtail/internal/daemon/admission"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxsync"
	"github.com/aloysbr/pigtail/internal/daemon/cpuslots"
	"github.com/aloysbr/pigtail/internal/daemon/diskwatch"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/focuspri"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/orphans"
	"github.com/aloysbr/pigtail/internal/daemon/powersrc"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/staffing"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/daemon/thermal"
	"github.com/aloysbr/pigtail/internal/daemon/tmpsweep"
	"github.com/aloysbr/pigtail/internal/daemon/usagehistory"
	"github.com/aloysbr/pigtail/internal/daemon/watchdog"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/gates"
	"github.com/aloysbr/pigtail/internal/inputprobe"
	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/internal/mainfresh"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/staticsrv"
	"github.com/aloysbr/pigtail/internal/sysdisk"
	"github.com/aloysbr/pigtail/internal/websession"
	"github.com/aloysbr/pigtail/internal/wsmarker"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Version of the daemon binary, as served on /version. Stamped at build
// time by the Makefile (-ldflags -X); "dev" is an unstamped `go build`.
var Version = "dev"

// Config controls the daemon's listeners and storage locations.
type Config struct {
	// ScopeTableErr is the error of loading <config dir>/scopes.yaml (nil:
	// loaded, or absent); /health says so. pigtaild itself never sets it: it
	// refuses to start on an invalid file (cmd/pigtaild run).
	ScopeTableErr error

	// Spaces is the space table (<config dir>/spaces.yaml or the built-in
	// one), the sidebar folders a theme may name. nil = none declared.
	Spaces *space.Table

	// SocketPath is the Unix domain socket path. Default:
	// $XDG_CONFIG_HOME/pigtail/pigtail.sock or ~/.config/pigtail/pigtail.sock.
	SocketPath string

	// HTTPAddr is the HTTP loopback address (e.g. "127.0.0.1:7800").
	// Set to "" to disable HTTP.
	HTTPAddr string

	// OutputsAddr is the loopback address of the outputs server (the files
	// of the subject outputs registry, read-only; staticsrv.NewOutputsHandler),
	// an origin distinct from HTTPAddr. "" = off. pigtaild's -outputs-http
	// defaults it through ResolveOutputsAddr.
	OutputsAddr string

	// DBPath is the SQLite database path. Default: alongside the socket.
	DBPath string

	// TokenPath is the bearer-token file for HTTP. Default: alongside socket.
	TokenPath string

	// WebSessionPath is the web front's session secret (0600; deleting
	// it signs every browser out). Default: alongside the socket.
	WebSessionPath string

	// WatchdogDisabled turns off the runaway dev-server watchdog globally
	// (per-project opt-out lives in pigtail.yaml).
	WatchdogDisabled bool

	// OrphanReaper is what the orphan reaper does with the process tree of a
	// Claude Code Bash-tool shell left orphaned: "on" stops it, "observe"
	// only journals it. The zero value ("") and "off" run no reaper — the
	// daemon tests never kill; pigtaild's flag defaults to "on", so the
	// live-pass daemon (a real pigtaild) is started with -orphan-reaper=off
	// (scripts/live-pass.sh, TestLivePassTurnsOrphanReaperOff). OrphanReapAfter is how long a shell must be seen
	// orphaned first (<= 0: orphans.DefaultAfter).
	OrphanReaper    orphans.Mode
	OrphanReapAfter time.Duration

	// TmpSweep is what the tmp sweeper does with the temporary directories
	// pigtail's own tests left behind (internal/daemon/tmpsweep): "on"
	// deletes, "observe" only journals. The zero value is off, like
	// OrphanReaper: the daemons of the tests never sweep the real $TMPDIR; the
	// pigtaild flag -tmp-sweep defaults to "on".
	TmpSweep tmpsweep.Mode

	// RAMBudget* configure the system-memory-pressure monitor (daemon-global).
	// Zero values fall back to rammon.Defaults().
	RAMBudgetDisabled  bool
	RAMFloorMB         int
	RAMGraceSeconds    int
	RAMIntervalSeconds int
	RAMConfirmWindows  int

	// Battery* configure the power-aware eviction mode (daemon-global).
	// Zero values fall back to rammon.Defaults().
	BatteryDisabled     bool
	BatteryGraceSeconds int

	// LidRunner runs pmset for pigtail lid (sleep control with the lid
	// closed). nil = no lid manager, the /lid routes answer 501: every test
	// server. pigtaild sets lid.ExecRunner{} on darwin.
	LidRunner lid.Runner
	// LidBattery reads the battery level for the lid guard. nil = no battery
	// guard (every test server). pigtaild sets powersrc.ReadLevel on darwin.
	LidBattery func() (powersrc.Level, error)
	// LidThermal reads the thermal state for the lid-closed heat guard. nil =
	// no heat guard (every test server). pigtaild sets thermal.Read on darwin.
	LidThermal func() (thermal.State, error)
	// LidClosed reads whether the lid is closed, for the heat guard. nil with
	// LidThermal set = lid unknown, no heat off. pigtaild sets
	// clamshell.Closed on darwin.
	LidClosed func() (bool, error)

	// IdlePurgeGraceSeconds sets the always-on idle-purge threshold (seconds;
	// 0 = off, the default). Runtime-overridable via POST /purge.
	IdlePurgeGraceSeconds int

	// SessionSampleSeconds is the cadence of the session sampler (live and
	// busy Claude sessions, footprint and CPU per session, appended to
	// session-samples.jsonl). 0 = off; pigtaild's flag defaults to
	// DefaultSessionSampleSeconds.
	SessionSampleSeconds int
	// SessionSamplerWrap, when set, may replace the sampler's seams before it
	// is built. Test seam: keeps the tests off the real vm_stat and ps.
	SessionSamplerWrap func(*sessbudget.Deps)
	// SessionBudget configures the session budget controller (pigtaild's
	// -session-* flags). A zero Ceiling leaves the gate off: every ask is
	// admitted. Its SamplePeriod is set from SessionSampleSeconds.
	SessionBudget sessbudget.Config
	// IdleSessionPauseSeconds is how long a Claude session must have been
	// idle before pigtaild stops its process (relay_pause.go; 0 = off).
	IdleSessionPauseSeconds int

	// MaxServers caps the number of live dev servers (0 = off, the default).
	// Beyond it, the most-idle servers are evicted each sweep (see rammon).
	MaxServers int

	// IdleCPUThresholdPercent is the share of one core a dev server's process
	// subtree must average over rammon's idle window (5 min) to count as in
	// use (rule D8: activity is CPU; a server to keep at rest is pinned).
	// Zero falls back to rammon.Defaults().
	IdleCPUThresholdPercent float64

	// IdleNetSignal turns on rammon's opt-in network signal: a page load or
	// reload (a burst of new connections in one sweep) also counts as use, at
	// the cost of one lsof and one netstat/ss per sweep. Off by default.
	IdleNetSignal bool

	// StaticPortRange is the daemon-global port range static servers allocate
	// from. Zero value falls back to DefaultStaticPortRange.
	StaticPortRange pigtail.PortRange

	// DBOffsiteDir is where the newest database backup is copied after each
	// rotation, so the guarantee survives losing the disk rather than only a
	// bad write. Empty (the default) disables it.
	//
	// Two forms, and the shape picks the transport:
	//
	//   - a local directory, which must already exist — pigtail never creates
	//     it, because creating it under an unmounted volume would silently put
	//     the "offsite" copy on the local disk;
	//   - an rsync-style [user@]host:/path, which has no mount to keep alive:
	//     nothing to remount after a reboot and nothing to wake on first touch.
	//
	// See internal/daemon/store/offsite_remote.go for why the second exists.
	DBOffsiteDir string

	// DBOffsiteFallbackDest is a pinned SSH alias — typically the same
	// [user@]host:/path as DBOffsiteDir, but through a ~/.ssh/config Host
	// entry that is not allowed to fall back to the LAN — that the daemon
	// periodically exercises to prove the *redundant* route to the same
	// destination still works, independently of whichever path DBOffsiteDir
	// actually took. Empty (the default) disables the exercise. Ignored
	// entirely when DBOffsiteDir is itself empty: there is nothing to be
	// redundant with, and Run logs that once at startup instead of
	// declaring anything.
	DBOffsiteFallbackDest string

	// SDK is pigtaild's -sdk-host / -sdk-node / -sdk-launch; nil leaves the SDK
	// session routes at 503 SDK_OFF (sdk_wiring.go).
	SDK *SDKConfig

	// GateCmd is pigtaild's -gate-cmd: the `gate` command GET /gates
	// runs as `<cmd> list --json`. Empty = not configured (GET /gates says so).
	GateCmd string

	// MainRefreshDisabled turns off the periodic fast-forward of each
	// project's main worktree (see refreshmain.go). Off means the main
	// checkout only ever moves when `pigtail new` branches from it — which is
	// the drift this sweep exists to stop.
	MainRefreshDisabled bool

	// MainRefreshIntervalSeconds is that sweep's cadence. Zero falls back to
	// defaultMainRefreshInterval.
	MainRefreshIntervalSeconds int

	// UsageRefreshDisabled turns off the background refresh of the Claude
	// usage readings (usagerefresh_sweep.go): the account rotation then reads
	// them as they are, refreshed only while a session runs.
	UsageRefreshDisabled bool

	// UsageRefreshIntervalSeconds is that sweep's cadence. Zero falls back to
	// defaultUsageRefreshInterval.
	UsageRefreshIntervalSeconds int

	// RoleWatchDisabled turns off the role supervision sweep (roles_sweep.go;
	// -role-watch=false): roles are then never judged and `role status`
	// answers watch: false. RoleSweepInterval is its cadence (0: 1 min).
	RoleWatchDisabled bool
	RoleSweepInterval time.Duration

	// DeepHealthTimeout bounds the store probe of GET /health?deep=1
	// (0: 2 s).
	DeepHealthTimeout time.Duration

	// UsageStateDir holds the usage state: usage-history.jsonl, usage-refresh.json
	// and the refresh lock. pigtaild sets it (default: the spawn registry's
	// state dir); empty — every test Server — keeps no history and leaves
	// the sweep unarmed, writing nothing.
	UsageStateDir string

	// CPUSlots sizes the machine-wide CPU slot arbiter, which keeps N
	// concurrent test runs from each claiming the whole machine.
	//
	//   < 0  → the platform budget (cpuslots.DefaultBudget)
	//   = 0  → the arbiter is disabled; clients fail open (the zero value)
	//   > 0  → that many slots
	//
	// Zero disables rather than defaults on purpose: -cpu-slots=0 is the
	// daemon-side kill switch named in the design, and a kill switch that a
	// zero value silently overrode would not be one.
	CPUSlots int

	// CPUSlotMaxHold is how long a CPU slot may stay on the arbiter's books
	// before its accounting is revoked (nothing is killed, the client is not
	// told). Zero falls back to cpuslots.Defaults().
	CPUSlotMaxHold time.Duration

	// AdmitMaxJobs sizes the memory admission controller, which holds heavy
	// commands (tsc, eslint, jest, vitest, nx, storybook build) back while the
	// machine lacks the memory to run one more.
	//
	//   < 0  → the platform default (admission.DefaultMaxJobs)
	//   = 0  → the controller is disabled; clients fail open (the zero value)
	//   > 0  → at most that many concurrent heavy commands
	//
	// Zero disables for the same reason as CPUSlots: it is the daemon-side
	// kill switch, and a kill switch the zero value overrode would not be one.
	AdmitMaxJobs int

	// AdmitFloorMB is the available memory every admission must leave
	// behind. Zero falls back to admission.Defaults().
	AdmitFloorMB int

	// AdmitDiskFloorGB is the free disk space (GiB) under which no heavy
	// command is admitted, even on an empty machine (admission.ReasonDisk).
	//
	//   < 0  → the default, 20 GiB
	//   = 0  → the gate is off (the zero value, so an unconfigured Config
	//          never reads the disk)
	//   > 0  → that many GiB
	//
	// A sentinel of its own: AdmitFloorMB > 0 already means "else default".
	AdmitDiskFloorGB int

	// DiskAlertGB is the free disk space (GiB) under which pigtaild raises
	// the disk alert (journal disk-low, event disk.low, GET /alerts); it
	// closes 10 GiB above (internal/daemon/diskwatch).
	//
	//   = 0  → no watcher (the zero value, so an in-process test daemon
	//          never reads the disk nor writes the journal)
	//   < 0  → the alert is cut: the watcher only closes one a previous run
	//          left open, so an alert never outlives its loop
	//   > 0  → the threshold
	//
	// The pigtaild flag -disk-alert-gb defaults to 40 and maps 0 and -1 to -1.
	DiskAlertGB int

	// AdmitPressureLevel holds the queue while the kernel's pressure level is
	// at or above it (2 = warn, 4 = critical). Zero falls back to the default;
	// negative turns the gate off.
	AdmitPressureLevel int

	// AdmitSwapoutKBPerSec holds the queue while the machine swaps out faster
	// than this. Zero falls back to the default; negative turns the gate off.
	AdmitSwapoutKBPerSec int

	// AdmitCompressKBPerSec holds the queue while the memory compressor works
	// faster than this (compressions + decompressions). Zero falls back to
	// the default; negative turns the gate off.
	AdmitCompressKBPerSec int

	// AdmitClassLimits caps concurrent admissions per class, as
	// "<class>:<n>" pairs — for the classes whose scarce resource is not
	// memory (a browser-mode vitest run spawns one Chromium per worker).
	// Empty keeps the controller's own defaults; a pair with n<=0 turns that
	// class's cap off, which is how a default is disabled by hand.
	AdmitClassLimits []string

	// AdmitMaxStarve is how long a queued command may be held back before
	// only the memory floor still stands between it and a lease: past it, the
	// job ceiling and the pressure brake stop applying to it. Negative turns
	// the guarantee off, zero keeps the default (3 min). Without it, a queue
	// can sit behind one long-running lease for as long as that lease lasts —
	// measured on 2026-09-24, four jobs with 13 GiB free.
	AdmitMaxStarve time.Duration

	// AdmitCompressCalmWindow is how long the heavy classes (build,
	// vitest-browser) need compressor traffic under the gate before they
	// start. Zero keeps the default (60 s); negative turns the rule off.
	AdmitCompressCalmWindow time.Duration
	// AdmitCompressCalmMaxStarve releases a waiter from that rule after this
	// wait. Zero or less keeps the default: never (D1, 2026-10-28).
	AdmitCompressCalmMaxStarve time.Duration

	// AdmitMaxHold is how long a lease stays on the books before its
	// accounting is revoked. Zero falls back to admission.Defaults().
	AdmitMaxHold time.Duration

	// CmuxSidebar switches the cmux sidebar loop on at start (pills,
	// titles and descriptions of bound workspaces). Off by default; POST
	// /cmux/sidebar flips it until the next restart.
	CmuxSidebar bool

	// CmuxSidebarWindows restricts the loop's writes to these cmux windows
	// (UUIDs). Empty = every window. Meant for trying the loop on a
	// throwaway window without touching the others.
	CmuxSidebarWindows []string

	// CmuxCheckMode is pigtaild --cmux-check-after-relaunch: off, report,
	// pilot or fix (cmuxwatch). "" = off — a Config built without the flag.
	CmuxCheckMode string
	// CmuxInputProbe is pigtaild --cmux-input-probe (on by default, the
	// safety switch; sub_x000000000000024): the dead-input probe loop, run
	// only while the sidebar loop is on. false in a Config built without the
	// flag.
	CmuxInputProbe bool
	// CmuxCheckCtrlC is pigtaild --cmux-check-ctrl-c: rescue (the default,
	// "" too) or unread — the Ctrl+C policy of the daemon's check (D8, T-a).
	CmuxCheckCtrlC string

	// CmuxClaudeCodeFixDisabled switches off the temporary claude_code
	// patch (cmuxsync/claudecode_patch.go) without switching the loop off.
	// The patch is on by default; pigtaild -cmux-claude-code-fix=false.
	CmuxClaudeCodeFixDisabled bool
	// CmuxFolders switches the folder arrangement of the sidebar loop on
	// (pigtaild --cmux-folders, D2 of sub_x000000000000041); it acts only
	// while the loop is on.
	CmuxFolders bool
	// CmuxFolderSortDisabled is pigtaild --cmux-folder-sort=false
	// (sub_x000000000000030, D5): the folder arrangement files and never
	// sorts. The sort is on by default, like CmuxClaudeCodeFixDisabled.
	CmuxFolderSortDisabled bool
	// CmuxBirthGrace is how long an unbound workspace stays where it was born
	// (pigtaild --cmux-birth-grace, D31); zero = the default, 30 min.
	CmuxBirthGrace time.Duration

	// Allocator is the one port allocator of the daemon, shared with the
	// backend so a port allocated by Backend.Create stays pending until the
	// handler persisted the workspace and confirmed it. nil builds one on the
	// store (tests that never create a workspace).
	Allocator *ports.Allocator
	// ListListeners is the bulk listener inventory the daemon's own allocator
	// takes once per Allocate. nil = devserver.ListListeners. A test seam.
	ListListeners func() (map[int][]int, error)
	// PortBusy and ProcessCwds are the listener probes of the handlers
	// (rename --force, PATCH /workspaces/{id}/ports). nil =
	// devserver.IsPortBusy / devserver.ProcessCwds. Test seams.
	PortBusy    func(port int) (bool, []int, error)
	ProcessCwds func(pids []int) (map[int]string, error)

	// UserSettings is settings.yaml, read by pigtaild at start. Nil = the
	// neutral defaults (tests never read the real file).
	UserSettings *usersettings.Settings
	// LaunchOverrides edits the session launcher's deps once built (cmux,
	// ps, process env): the test seam, so no test reaches the real ones.
	LaunchOverrides func(*launch.Deps)
	// StaffingOverrides edits the staffing scheduler's deps once built: the
	// test seam (launch, margin, machine, tick, AfterTour).
	StaffingOverrides func(*staffing.Deps)
}

// DefaultStaticPortRange is used when Config.StaticPortRange is unset.
var DefaultStaticPortRange = pigtail.PortRange{8080, 8099}

// Server wires the store, event bus, and HTTP router together and
// listens on both the Unix socket and the loopback HTTP port.
type Server struct {
	cfg             Config
	hooks           *handlers.HookRunner // post-create hooks, on the daemon's lifetime
	hooksCancel     context.CancelFunc   // stops them when Run returns
	store           *store.Store
	bus             *events.Bus
	backend         backend.Backend
	agent           agent.Agent
	watcher         *activity.Watcher
	watchdog        *watchdog.Watchdog
	orphans         *orphans.Reaper    // nil unless Config.OrphanReaper is on or observe
	tmpSweep        *tmpsweep.Sweeper  // nil unless Config.TmpSweep is on or observe
	diskWatch       *diskwatch.Watcher // nil when Config.DiskAlertGB == 0
	ramMonitor      *rammon.RAMMonitor
	lid             *lid.Manager // nil unless Config.LidRunner is set
	cpuSlots        *cpuslots.Arbiter
	cpuSlotsMaxHold time.Duration
	admission       *admission.Controller
	sessionSampler  *sessbudget.Sampler // nil when the sampler is off
	sessionBudget   *sessbudget.Controller
	services        *handlers.Services    // the router's services, kept for wiring tests
	usageHistory    *usagehistory.History // nil: no UsageStateDir, or it could not be opened

	// roles is the role supervision sweep (nil: -role-watch=false), built in
	// New before the router; roleStale is its heartbeat check for
	// /health?deep=1 (nil when the watch is off).
	roles     *rolesSweep
	roleStale func() (string, bool)
	// ping is the store probe of /health?deep=1 (nil: Store.Ping); a test
	// fakes it, the store-side timeout is tested in the store package.
	ping         func(ctx context.Context) error
	mainFresh    *mainfresh.Registry
	allocator    *ports.Allocator
	staticMgr    *staticsrv.Manager
	offsite      *offsiteBackupper
	relay        *handlers.Relay
	cmuxSync     *cmuxsync.Runner
	inputTracker *inputprobe.Tracker // dead-input probe state; nil = --cmux-input-probe off
	inputNow     func() time.Time    // clock of the sidebar's dead-input read; nil = time.Now
	focusPri     *focuspri.Resolver  // admission's focus priority; nil without a store
	maintenance  *maintenance.Watch
	router       *chi.Mux
	token        string

	// Web front (web.go): the cookie's secret (nil with HTTP off), the
	// pending login codes, and the embedded front (a fake in tests).
	webSession *websession.Session
	webCodes   *websession.Codes
	webFS      fs.FS

	markerDir      string // workspace-subject markers (wsmarker), under daemonMaintenanceDir
	markerEvery    time.Duration
	markerTestHook func()
	markerLogf     func(string, ...any) // nil = log.Printf; tests collect the lines
	markerMu       sync.Mutex           // serialises syncWorkspaceMarkers: routes, tick and sidebar loop
	markerErr      string               // last marker error logged, "" after a pass that worked

	// streamsStop is closed when Run's http.Server enters Shutdown: every
	// open /events stream writes what is already queued, then ends cleanly
	// (Shutdown neither cancels in-flight requests nor closes active
	// connections, it would wait its 5 s cap on a permanent subscriber).
	streamsStop     chan struct{}
	streamsStopOnce sync.Once

	mu        sync.Mutex
	listeners []net.Listener

	// outputsBound is the address the outputs listener really bound, set
	// only after a successful Listen and cleared when Run stops ("" = off or
	// bind failed). Read through outputsBase, never from Config.OutputsAddr.
	outputsBound string
	// outputsH is the bound outputs server's handler, the source of
	// served_url (outputsURLs); nil while outputsBound is "".
	outputsH *staticsrv.OutputsHandler

	// launcher is the session launcher (POST /subjects/{id}/launch); nil
	// until it is wired. Its confirmations run on Run's context and are
	// drained with the other workers.
	launcher *launch.Launcher
	// staffing is the staffing scheduler (GET/POST /staffing); nil without
	// a store or a launcher. Its loop runs on Run's context.
	staffing *staffing.Scheduler
}

// New constructs a Server using the supplied config and dependencies.
// The backend and agent may be nil during early bring-up (e.g. tests that
// only exercise /health or /events) — handlers that need them will error.
func New(cfg Config, st *store.Store, bus *events.Bus, be backend.Backend, ag agent.Agent) (*Server, error) {
	if cfg.SocketPath == "" {
		cfg.SocketPath = defaultSocketPath()
	}
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(filepath.Dir(cfg.SocketPath), "pigtail.db")
	}
	if cfg.TokenPath == "" {
		cfg.TokenPath = filepath.Join(filepath.Dir(cfg.SocketPath), "token")
	}
	if cfg.WebSessionPath == "" {
		cfg.WebSessionPath = filepath.Join(filepath.Dir(cfg.SocketPath), websession.SessionFileName)
	}
	if err := ValidateOutputsAddr(cfg.OutputsAddr, cfg.HTTPAddr); err != nil {
		return nil, err
	}
	if cfg.StaticPortRange == (pigtail.PortRange{}) {
		cfg.StaticPortRange = DefaultStaticPortRange
	}
	alloc := cfg.Allocator
	if alloc == nil {
		list := cfg.ListListeners
		if list == nil {
			list = devserver.ListListeners
		}
		alloc = newAllocator(st, list)
	}
	s := &Server{
		cfg: cfg, store: st, bus: bus, backend: be, agent: ag,
		watcher:     activity.New(bus),
		mainFresh:   mainfresh.New(),
		allocator:   alloc,
		staticMgr:   staticsrv.New(),
		offsite:     newOffsiteBackupper(),
		streamsStop: make(chan struct{}),
	}
	s.watcher.SetPersister(func(sessionID string, state pigtail.AgentState) {
		_ = s.store.UpdateAgentSessionState(sessionID, state)
	})
	if !cfg.WatchdogDisabled {
		s.watchdog = watchdog.New(bus, st,
			watchdog.WithConfigResolver(watchdog.DefaultConfigResolver),
			watchdog.WithPortPIDs(watchdog.DefaultPortPIDs),
			watchdog.WithCwds(devserver.ProcessCwds),
			watchdog.WithKiller(s.killRunaway),
		)
	}
	if cfg.OrphanReaper == orphans.ModeOn || cfg.OrphanReaper == orphans.ModeObserve {
		s.orphans = orphans.New(cfg.OrphanReaper, cfg.OrphanReapAfter)
		s.orphans.Journal = func(e pigtail.JournalEntry) error {
			if err := e.Validate(); err != nil {
				return err
			}
			_, err := st.AddJournal(&e)
			return err
		}
	}
	if cfg.DiskAlertGB != 0 {
		// The floor in the alert is what admission enforces: 0 when admission
		// is off, so the notification never claims that tests wait.
		ac := admissionConfig(cfg)
		floor := ac.DiskFloorBytes
		if ac.MaxJobs <= 0 {
			floor = 0
		}
		threshold := uint64(0)
		if cfg.DiskAlertGB > 0 {
			threshold = uint64(cfg.DiskAlertGB) << 30
		}
		s.diskWatch = diskwatch.New(threshold, floor)
		s.diskWatch.Free = sysdisk.FreeMinDefault
		s.diskWatch.Journal = func(e pigtail.JournalEntry) error {
			if err := e.Validate(); err != nil {
				return err
			}
			_, err := st.AddJournal(&e)
			return err
		}
		s.diskWatch.Publish = func(e pigtail.Event) { bus.Publish(e) }
		s.diskWatch.Open = func() (bool, error) {
			e, err := diskwatch.OpenAlert(st)
			return e != nil, err
		}
	}
	if cfg.TmpSweep == tmpsweep.ModeOn || cfg.TmpSweep == tmpsweep.ModeObserve {
		s.tmpSweep = tmpsweep.New(cfg.TmpSweep)
		s.tmpSweep.Journal = func(e pigtail.JournalEntry) error {
			if err := e.Validate(); err != nil {
				return err
			}
			_, err := st.AddJournal(&e)
			return err
		}
	}
	// Always construct the RAM monitor so the read-only /servers introspection
	// endpoint works even when eviction is disabled. Eviction is gated by
	// rc.Enabled; a disabled monitor's Run loop returns early (no-op) but its
	// Inspect remains available.
	rc := rammon.Defaults()
	if cfg.RAMFloorMB > 0 {
		rc.FloorBytes = uint64(cfg.RAMFloorMB) << 20
	}
	if cfg.RAMGraceSeconds > 0 {
		rc.Grace = time.Duration(cfg.RAMGraceSeconds) * time.Second
	}
	if cfg.RAMIntervalSeconds > 0 {
		rc.Interval = time.Duration(cfg.RAMIntervalSeconds) * time.Second
	}
	if cfg.RAMConfirmWindows > 0 {
		rc.ConfirmWindows = cfg.RAMConfirmWindows
	}
	rc.Enabled = !cfg.RAMBudgetDisabled
	rc.BatteryEnabled = !cfg.BatteryDisabled
	if cfg.BatteryGraceSeconds > 0 {
		rc.BatteryGrace = time.Duration(cfg.BatteryGraceSeconds) * time.Second
	}
	if cfg.IdlePurgeGraceSeconds > 0 {
		rc.IdlePurgeGrace = time.Duration(cfg.IdlePurgeGraceSeconds) * time.Second
	}
	if cfg.MaxServers > 0 {
		rc.MaxServers = cfg.MaxServers
	}
	if cfg.IdleCPUThresholdPercent > 0 {
		rc.IdleCPUThresholdPercent = cfg.IdleCPUThresholdPercent
	}
	rc.NetSignal = cfg.IdleNetSignal
	s.ramMonitor = rammon.New(bus, st, rammon.WithConfig(rc), rammon.WithBackend(be),
		rammon.WithStatePath(rammon.StatePath(cfg.DBPath)), rammon.WithCwds(devserver.ProcessCwds))
	// Seed the activity tracker and the death ring from the previous daemon,
	// before the first sweep, so a redeploy no longer resets every idle counter
	// nor blinds death detection for one interval.
	s.ramMonitor.LoadState()
	// The CPU slot arbiter is always constructed so GET /cpu/slots answers
	// truthfully ("budget 0") rather than 404 when it is switched off; its
	// Run loop returns immediately at budget 0.
	cc := cpuslots.Defaults()
	if cfg.CPUSlots >= 0 {
		cc.Budget = cfg.CPUSlots
	}
	if cfg.CPUSlotMaxHold > 0 {
		cc.MaxHold = cfg.CPUSlotMaxHold
	}
	// Same history discipline as admission's: rotated here, once, at startup.
	cpuHistory := cpuslots.NewHistory(cpuslots.HistoryPath(cfg.DBPath))
	if err := cpuHistory.Rotate(cpuslots.HistoryMaxBytes); err != nil {
		log.Printf("cpuslots: rotating the history: %v", err)
	}
	s.cpuSlots = cpuslots.New(bus, cpuslots.WithConfig(cc), cpuslots.WithHistory(cpuHistory))
	s.cpuSlotsMaxHold = cc.MaxHold
	// Same reasoning for the admission controller: always constructed, so
	// GET /admission answers "max_jobs 0" rather than 404 when switched off.
	// The history is rotated here, once, rather than by its writer: only at
	// startup is nobody appending to it.
	history := admission.NewHistory(admission.HistoryPath(cfg.DBPath))
	if err := history.Rotate(admission.HistoryMaxBytes); err != nil {
		log.Printf("admission: rotating the history: %v", err)
	}
	s.admission = admission.New(bus,
		admission.WithConfig(admissionConfig(cfg)),
		admission.WithDiskReader(sysdisk.FreeMinDefault),
		admission.WithCostsPath(admission.CostsPath(cfg.DBPath)),
		admission.WithLeasesPath(admission.LeasesPath(cfg.DBPath)),
		admission.WithHistory(history))
	s.admission.LoadCosts()
	s.sessionSampler = newSessionSampler(cfg, st)
	// The session budget controller: always constructed, so GET
	// /session-budget answers enabled:false rather than 404 when the gate is
	// off. Samples reach it one way only, the sampler's OnSample, set here
	// before Server.Run starts the sampler's goroutine.
	bcfg := cfg.SessionBudget
	bcfg.SamplePeriod = time.Duration(cfg.SessionSampleSeconds) * time.Second
	s.sessionBudget = sessbudget.NewController(bcfg, time.Now, log.Printf)
	if s.sessionSampler != nil {
		s.sessionSampler.OnSample = s.sessionBudget.Observe
	}
	// The usage history (one sample per account and sweep pass) is opened
	// once, here, like the admission history: rotated and reloaded at start.
	if cfg.UsageStateDir != "" {
		h, err := usagehistory.Open(filepath.Join(cfg.UsageStateDir, usagehistory.FileName), time.Now())
		if err != nil {
			log.Printf("daemon: usage history: %v", err)
		}
		s.usageHistory = h
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o700); err != nil {
		return nil, fmt.Errorf("ensure config dir: %w", err)
	}
	if cfg.HTTPAddr != "" {
		tok, err := loadOrCreateToken(cfg.TokenPath)
		if err != nil {
			return nil, err
		}
		s.token = tok
	}
	sess, codes, front, err := newWebState(cfg)
	if err != nil {
		return nil, err
	}
	s.webSession, s.webCodes, s.webFS = sess, codes, front
	s.maintenance = maintenance.NewWatch(daemonMaintenanceDir(s.cfg, os.Getenv))
	// Focus D26: the store holds focus seals under a freeze, read on the
	// same Watch as FreezeMW; the routes and the sidebar loop go through it.
	if st != nil {
		st.SetFocusSealGate(func() bool { return maintenance.SealsHeld(s.maintenance) })
	}
	s.markerDir = filepath.Join(daemonMaintenanceDir(s.cfg, os.Getenv), wsmarker.DirName)
	logRuntimeClaim(daemonMaintenanceDir(s.cfg, os.Getenv), s.userSettings().Human.Name, claudesession.SessionAlive)
	s.markerEvery = 30 * time.Second
	if cfg.CmuxInputProbe {
		s.inputTracker = inputprobe.NewTracker(inputProbeEvery)
	}
	s.cmuxSync = s.newCmuxSync()
	if st != nil {
		s.focusPri = focuspri.New(s.focusPriDeps(), 0, 0)
	}
	s.lid = newLidManager(cfg, s.bus)
	hctx, cancel := context.WithCancel(context.Background())
	s.hooks, s.hooksCancel = handlers.NewHookRunner(hctx), cancel
	if st != nil && !cfg.RoleWatchDisabled {
		s.roles = newRolesSweep(st, newRolesCollector(st), cfg.RoleSweepInterval)
		s.roleStale = s.roles.Stale
	}
	s.router = s.buildRouter()
	return s, nil
}

// killRunaway is the watchdog's killer, wrapped so the RAM monitor's death
// detector learns the stop was deliberate. The watchdog stops the whole
// worktree (devserver.Stop with an empty server name), so every tracked
// server of that workspace is marked. Marking happens in-process: inside
// pigtaild the devserver notification seam stays a no-op, precisely so the
// daemon never calls itself over HTTP.
func (s *Server) killRunaway(t watchdog.Target) error {
	if s.ramMonitor != nil {
		s.ramMonitor.MarkIntentionalStopsForWorkspace(t.Workspace.ID)
	}
	return watchdog.DefaultKiller(t)
}

// Bus exposes the event bus so other daemon components can publish.
func (s *Server) Bus() *events.Bus { return s.bus }

// Store exposes the state store.
func (s *Server) Store() *store.Store { return s.store }

// Handler is the daemon's router, as Run serves it: an in-process caller
// (cmd/pigtaild's launchd-environment test) reaches the routes without a
// socket nor the rest of Run.
func (s *Server) Handler() http.Handler { return s.router }

// DefaultSessionSampleSeconds is the sampler's cadence when pigtaild is not
// told otherwise.
const DefaultSessionSampleSeconds = 30

// newSessionSampler builds the session sampler, or nil when it is off. It
// reads the peers and workspaces from the store; the memory sensor and the
// process table are the admission controller's own.
func newSessionSampler(cfg Config, st *store.Store) *sessbudget.Sampler {
	if cfg.SessionSampleSeconds <= 0 || st == nil {
		return nil
	}
	deps := sessbudget.DefaultDeps(
		func() ([]pigtail.PeerSession, error) { return st.ListPeers(false) },
		func(wsID string) string {
			ws, err := st.GetWorkspace(wsID)
			if err != nil || ws == nil {
				return ""
			}
			p, err := st.GetProject(ws.ProjectID)
			if err != nil || p == nil {
				return ""
			}
			return p.Name
		})
	if cfg.SessionSamplerWrap != nil {
		cfg.SessionSamplerWrap(&deps)
	}
	h := sessbudget.NewHistory(sessbudget.SamplesPath())
	return sessbudget.New(deps, h, time.Duration(cfg.SessionSampleSeconds)*time.Second)
}

// DefaultAdmitDiskFloorGB is the free disk space (GiB) under which admission
// holds every heavy command: the 2026-10-31 outage began at 1.4 GB free.
const DefaultAdmitDiskFloorGB = 20

// admissionConfig maps the daemon config onto the controller's, applying the
// zero/negative conventions documented on Config.
func admissionConfig(cfg Config) admission.Config {
	ac := admission.Defaults()
	switch {
	case cfg.AdmitMaxJobs == 0:
		ac.MaxJobs = 0
	case cfg.AdmitMaxJobs > 0:
		ac.MaxJobs = cfg.AdmitMaxJobs
	}
	if cfg.AdmitFloorMB > 0 {
		ac.FloorBytes = uint64(cfg.AdmitFloorMB) << 20
	}
	switch {
	case cfg.AdmitDiskFloorGB < 0:
		ac.DiskFloorBytes = DefaultAdmitDiskFloorGB << 30
	case cfg.AdmitDiskFloorGB > 0:
		ac.DiskFloorBytes = uint64(cfg.AdmitDiskFloorGB) << 30
	}
	switch {
	case cfg.AdmitPressureLevel < 0:
		ac.PressureLevel = 0
	case cfg.AdmitPressureLevel > 0:
		ac.PressureLevel = cfg.AdmitPressureLevel
	}
	switch {
	case cfg.AdmitSwapoutKBPerSec < 0:
		ac.SwapoutBytesPerSec = 0
	case cfg.AdmitSwapoutKBPerSec > 0:
		ac.SwapoutBytesPerSec = float64(cfg.AdmitSwapoutKBPerSec) * 1024
	}
	switch {
	case cfg.AdmitCompressKBPerSec < 0:
		ac.CompressBytesPerSec = 0
	case cfg.AdmitCompressKBPerSec > 0:
		ac.CompressBytesPerSec = float64(cfg.AdmitCompressKBPerSec) * 1024
	}
	if cfg.AdmitMaxHold > 0 {
		ac.MaxHold = cfg.AdmitMaxHold
	}
	switch {
	case cfg.AdmitMaxStarve < 0:
		ac.MaxStarve = 0
	case cfg.AdmitMaxStarve > 0:
		ac.MaxStarve = cfg.AdmitMaxStarve
	}
	switch {
	case cfg.AdmitCompressCalmWindow < 0:
		ac.CompressCalmWindow = 0
	case cfg.AdmitCompressCalmWindow > 0:
		ac.CompressCalmWindow = cfg.AdmitCompressCalmWindow
	}
	if cfg.AdmitCompressCalmMaxStarve > 0 {
		ac.CompressCalmMaxStarve = cfg.AdmitCompressCalmMaxStarve
	}
	if limits, ok := parseClassLimits(cfg.AdmitClassLimits); ok {
		ac.ClassLimits = limits
	}
	return ac
}

// parseClassLimits turns "<class>:<n>" pairs into the controller's map,
// starting from the defaults so that setting one class does not silently
// drop the others. A malformed pair is ignored rather than fatal: a typo in
// a launchd plist must not keep the daemon from starting. Reports false when
// nothing usable was given, so the caller keeps the defaults untouched.
func parseClassLimits(pairs []string) (map[string]int, bool) {
	if len(pairs) == 0 {
		return nil, false
	}
	out := map[string]int{}
	for k, v := range admission.Defaults().ClassLimits {
		out[k] = v
	}
	used := false
	for _, pair := range pairs {
		class, value, ok := strings.Cut(pair, ":")
		if !ok || class == "" {
			log.Printf("admission: ignoring malformed -admit-class-limit %q (want <class>:<n>)", pair)
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			log.Printf("admission: ignoring -admit-class-limit %q: %v", pair, err)
			continue
		}
		out[strings.TrimSpace(class)] = n
		used = true
	}
	return out, used
}

// cpuSlotMaxHold reports the arbiter's configured holding ceiling. Read by
// the wiring test; the arbiter itself never needs to be asked for it.
func (s *Server) cpuSlotMaxHold() time.Duration { return s.cpuSlotsMaxHold }

// workerDrainTimeout bounds how long Run waits, after cancellation, for the
// background goroutines it started to actually return.
//
// Waiting at all is the point of the bound's existence: runDBBackups' VACUUM
// INTO (backup.go) and the offsite copy (offsite.go) both write files, and a
// process that exits on top of either leaves exactly the half-written
// generation the backup exists to protect against. Run used to return the
// instant ctx was cancelled, while both were still mid-write.
//
// Waiting *without* a bound would be worse than not waiting: the fallback
// exercise's first pass runs its probe inline, and that probe can sit on an
// unreachable host for offsiteFallbackExerciseTimeout (30s,
// offsite_fallback.go). A launchd job that has not exited within its own
// grace period is SIGKILLed, which would trade a rare truncated backup for a
// systematic hard kill. Five seconds is the budget the HTTP shutdown just
// above already gets, and it is three orders of magnitude more than a local
// VACUUM INTO of this database takes.
const workerDrainTimeout = 5 * time.Second

// Run starts both listeners and blocks until ctx is cancelled and every
// goroutine it started has stopped (or workerDrainTimeout has elapsed).
func (s *Server) Run(ctx context.Context) error {
	// D6: no hook of this process runs yet, so a "running" record is one a
	// stopped daemon left behind — never a running forever that blocks spawn.
	if s.store != nil {
		if n, err := s.store.FailRunningHooks("interrupted: the daemon stopped before the hooks ended"); err != nil {
			log.Printf("hooks: reconcile running records: %v", err)
		} else if n > 0 {
			log.Printf("hooks: %d workspace(s) had hooks running when the daemon last stopped — marked failed", n)
		}
	}
	if err := os.RemoveAll(s.cfg.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	sockLn, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	if err := os.Chmod(s.cfg.SocketPath, 0o600); err != nil {
		_ = sockLn.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	s.addListener(sockLn)

	httpServer := &http.Server{Handler: s.router, ReadHeaderTimeout: 10 * time.Second}
	// Shutdown runs this as it starts, before waiting for connections: the
	// /events streams end instead of holding it.
	httpServer.RegisterOnShutdown(s.stopStreams)

	// Every goroutine Run starts goes through spawn, and nothing here starts
	// one any other way: the whole point is that the set is joinable, so the
	// drain at the bottom can be exhaustive rather than a list somebody has
	// to remember to extend. See workerDrainTimeout.
	var workers sync.WaitGroup
	spawn := func(fn func()) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			fn()
		}()
	}

	spawn(func() { _ = httpServer.Serve(sockLn) })

	webAddr := ""
	if s.cfg.HTTPAddr != "" {
		tcpLn, err := listenTCP(s.cfg.HTTPAddr)
		if err != nil {
			s.shutdownAll(httpServer)
			s.drainWorkers(&workers)
			return err
		}
		s.addListener(tcpLn)
		spawn(func() { _ = httpServer.Serve(tcpLn) })
		webAddr = tcpLn.Addr().String()
	}

	// Outputs server: a second origin serving the registered outputs only.
	// A failed bind (port taken, a second pigtaild) is one log line, never
	// fatal: the listener stays off and served_url stays empty.
	var outputsServer *http.Server
	if s.cfg.OutputsAddr != "" {
		outLn, err := net.Listen("tcp", s.cfg.OutputsAddr)
		if err != nil {
			log.Printf("outputs: listener %s off: %v", s.cfg.OutputsAddr, err)
		} else {
			s.addListener(outLn)
			bound := outLn.Addr().String()
			oh := s.outputsHandler(bound, webAddr)
			outputsServer = &http.Server{Handler: oh, ReadHeaderTimeout: 10 * time.Second}
			s.mu.Lock()
			s.outputsBound, s.outputsH = bound, oh
			s.mu.Unlock()
			spawn(func() { _ = outputsServer.Serve(outLn) })
		}
	}

	s.bus.Publish(pigtail.Event{
		Type: pigtail.EventDaemonStarted,
		Data: map[string]interface{}{"version": Version, "socket": s.cfg.SocketPath, "http": s.cfg.HTTPAddr},
	})

	// Recover activity watchers for sessions that were running when we
	// last shut down. tmux sessions with names matching ours survive
	// across daemon restarts.
	s.recoverWatchers()

	// Runaway dev-server watchdog: sweeps all workspaces and kills servers
	// stuck pegging the CPU. Stops with ctx like everything else.
	if s.watchdog != nil {
		spawn(func() { s.watchdog.Run(ctx) })
	}

	// Orphan reaper: stops the trees of Claude Code shells orphaned for
	// OrphanReapAfter (internal/daemon/orphans).
	if s.orphans != nil {
		spawn(func() { s.orphans.Run(ctx) })
	}

	// Tmp sweeper: deletes the temporary directories pigtail's own tests left
	// behind (internal/daemon/tmpsweep).
	if s.tmpSweep != nil {
		spawn(func() { s.tmpSweep.Run(ctx) })
	}

	// Disk watch: the low-disk alert (internal/daemon/diskwatch).
	if s.diskWatch != nil {
		spawn(func() { s.diskWatch.Run(ctx) })
	}

	// System-memory-pressure monitor: evicts idle dev servers when available
	// RAM drops below the floor. Stops with ctx like everything else.
	if s.ramMonitor != nil {
		spawn(func() { s.ramMonitor.Run(ctx) })
	}

	// CPU slot arbiter: revokes the accounting of slots held past the
	// ceiling. It never spawns and never kills — it only keeps its own books
	// honest. Stops with ctx like everything else; a no-op at budget 0.
	if s.cpuSlots != nil {
		spawn(func() { s.cpuSlots.Run(ctx) })
	}

	// Lid sleep control: deadline and battery guard. Executes only `sudo -n
	// pmset -a disablesleep 0`, the setup probe (`sudo -n -k pmset -a
	// disablesleep <current>`, under the manager lock) and `pmset -g`.
	if s.lid != nil {
		spawn(func() { s.runLid(ctx) })
	}

	// Memory admission controller: samples memory and re-evaluates its queue
	// on a clock, since memory frees up without any event. Like the CPU
	// arbiter it never spawns and never kills. A no-op when disabled.
	if s.admission != nil {
		spawn(func() { s.admission.Run(ctx) })
	}

	// Session sampler: one record per tick of the live and busy sessions.
	// Started here, after buildRouter (called from New) has wired OnSample's
	// consumers, so nothing writes that field while it runs. Stops with ctx.
	if s.sessionSampler != nil {
		spawn(func() { s.sessionSampler.Run(ctx) })
	}

	// Session-message relay: expires unread messages on a clock and tells
	// their senders. Never spawns, never kills.
	if s.relay != nil {
		spawn(func() { s.relay.Run(ctx) })
	}

	// Workspace-subject markers (wsmarker): rewritten at start and every 30 s, for the subject hook's fast path.
	spawn(func() { s.runWorkspaceMarkers(ctx) })

	// cmux sidebar loop: pills, titles and descriptions of the cmux
	// workspaces (internal/daemon/cmuxsync). Idle — not one cmux call —
	// while switched off; recovers its own panics and switches itself off
	// after repeated ones. Clears its pills on the way out.
	// Focus priority of memory admission: a background snapshot of the open
	// focus, the pinned workspaces and the caller mapping, so a request never
	// reads the store (internal/daemon/focuspri).
	if s.focusPri != nil {
		spawn(func() { s.focusPri.Run(ctx) })
	}
	// Session launcher: resumes the API launches left open, then its
	// confirmations live on ctx; the drain waits for them.
	if s.launcher != nil {
		s.launcher.Start(ctx)
		spawn(func() { <-ctx.Done(); s.launcher.Wait() })
	}
	// Staffing scheduler: after the launcher it launches through; a tour at
	// start, at each wait, launch or pause event and subject write, and every 60 s.
	s.startStaffing(ctx, spawn)
	if s.cmuxSync != nil {
		spawn(func() { s.cmuxSync.Run(ctx) })
		relay := s.watchServerEvents(s.cmuxSync.ServersChanged)
		spawn(func() { relay(ctx) })
		relayWaits := s.watchWaitEvents(s.cmuxSync.WaitsChanged)
		spawn(func() { relayWaits(ctx) })
	}

	// Post-relaunch check (cmuxwatch): `pigtail cmux check --scope all` once
	// per cmux launch, in a pigtail subprocess; its defects show as the
	// sidebar's pigtail-chk pill (D13, Task 9).
	startCmuxWatchFn(ctx, s.cfg.CmuxCheckMode, spawn, s.cmuxWatchDeps())

	// Dead-input probe (sub_x000000000000024): a terminal status query on
	// the session's own tty, never a keystroke; its verdicts light the pigtail-chk pill (cmuxsync DeadInputs).
	startInputProbeFn(ctx, s, spawn)

	// Declare the offsite destination(s) before either of the two goroutines
	// below starts, not after either one's first write. Moved here (task 8)
	// from inside runDBBackups: exerciseFallbackIfDue is now a second writer
	// of this same state file, and if it ran before declare() it could
	// persist a report with no DestDir — which Configured() reads as
	// "nothing configured anywhere" — only for the declare() that follows to
	// then wipe it out from under the exercise, since a changed DestDir
	// resets the whole record. Declaring first removes the window entirely.
	switch {
	case s.cfg.DBOffsiteDir != "" && s.offsite != nil && s.store != nil:
		s.offsite.declare(s.store.Path(), s.cfg.DBOffsiteDir, s.cfg.DBOffsiteFallbackDest)
		if fb := s.cfg.DBOffsiteFallbackDest; fb != "" {
			if _, _, parses := store.ParseRemoteDest(fb); !parses || fb == s.cfg.DBOffsiteDir {
				// The likeliest misconfiguration a person will actually
				// make: the flag's own help text calls this a "pinned
				// alias", but ParseRemoteDest — same as db-offsite-dir —
				// requires the full [user@]host:/path shape. Writing the
				// bare alias parses as neither a valid remote destination
				// nor (by construction) a match for db-offsite-dir, so
				// exerciseFallbackIfDue (offsite_fallback.go) silently
				// declines to ever run the exercise. Logged once at
				// startup, on the same model as the db-offsite-dir-less
				// case just below — this one used to log nothing at all.
				log.Printf("daemon: db-offsite-fallback-dest %q does not parse as a [user@]host:/path "+
					"destination distinct from db-offsite-dir; the fallback exercise will not run", fb)
			}
		}
	case s.cfg.DBOffsiteFallbackDest != "":
		// A fallback destination with no primary one has nothing to be
		// redundant with: there is no DestDir for declare() to attach it to,
		// and no primary backup for the exercise to ever be "the other
		// route" of. Logged once at startup and left at that — not fatal,
		// on the same model as every other offsite misconfiguration.
		log.Printf("daemon: db-offsite-fallback-dest is set but db-offsite-dir is not; " +
			"the fallback exercise has nothing to declare against and will not run")
	}

	// Rolling snapshots of the database — the only state here whose loss is
	// irreversible. Stops with ctx like everything else.
	spawn(func() { s.runDBBackups(ctx) })

	// Proves the redundant route to the same destination stays alive,
	// independently of whichever path the primary copy actually took. A
	// no-op unless both a primary destination and a fallback alias are
	// configured — see DBOffsiteFallbackDest's own doc comment.
	if s.cfg.DBOffsiteDir != "" && s.cfg.DBOffsiteFallbackDest != "" && s.offsite != nil && s.store != nil {
		spawn(func() { s.runOffsiteFallback(ctx) })
	}

	// Keeps BackendData["branch"] in step with git, so `pigtail list` stops
	// naming a branch the worktree left days ago. A no-op for a backend that
	// does not implement backend.RefResolver.
	spawn(func() { s.runBranchReconcile(ctx) })

	// Keeps each project's main worktree level with its base ref, so the
	// checkout agents are pointed at to read stops drifting behind the ones
	// pigtail creates from it. Strictly a fast-forward, never over a dirty or
	// repurposed checkout. A no-op for a backend that does not implement
	// backend.MainRefresher.
	spawn(func() { s.runMainRefresh(ctx) })

	// Keeps the Claude usage readings the account rotation and Staffing's
	// weekly budget read under 30 min, save a relaunch in flight or a failed
	// refresh: claude -p /usage from 20 min on, one
	// account at a time, under the same lock as pigtail usage --refresh. Runs
	// claude, never cmux.
	spawn(func() { s.runUsageRefresh(ctx) })

	// Judges the declared roles every minute and writes one journal alert per
	// missed deadline. Reads transcripts and the store; no cmux, no git.
	spawn(func() { s.runRoleWatch(ctx) })

	// Lifts the runtime claim at its 4 h deadline and journals the expiry;
	// reads and writes the lease file only, never cmux.
	spawn(func() { s.runRuntimeClaimSweep(ctx) })

	<-ctx.Done()
	s.bus.Publish(pigtail.Event{Type: pigtail.EventDaemonShuttingDown})
	s.watcher.StopAll()
	if s.staticMgr != nil {
		s.staticMgr.StopAll()
	}

	s.hooksCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	if outputsServer != nil {
		_ = outputsServer.Shutdown(shutdownCtx)
	}
	s.mu.Lock()
	s.outputsBound = ""
	s.mu.Unlock()
	s.shutdownAll(httpServer)
	s.drainWorkers(&workers)
	if !s.hooks.Wait(workerDrainTimeout) {
		log.Printf("hooks: some post-create hooks still stopping; the next start marks them failed")
	}
	return nil
}

// stopStreams ends every open /events stream (idempotent).
func (s *Server) stopStreams() { s.streamsStopOnce.Do(func() { close(s.streamsStop) }) }

// drainWorkers blocks until every goroutine Run started has returned — and,
// with them, the detached copy goroutines offsiteBackupper.run spawns, which
// answer to no context and are the one writer Run does not start itself —
// or until workerDrainTimeout elapses, whichever comes first.
//
// The timeout is not a formality that never fires: whichever goroutine is
// still running when it does was, by construction, the one worth not waiting
// for. It is named in the log rather than waited on, so "the daemon took five
// seconds to exit" is a sentence somebody can read instead of guess at.
func (s *Server) drainWorkers(workers *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		workers.Wait()
		if s.offsite != nil {
			s.offsite.waitForCopies()
		}
	}()

	t := time.NewTimer(workerDrainTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		log.Printf("daemon: a background goroutine was still running %s after shutdown; exiting without it", workerDrainTimeout)
	}
}

// recoverWatchers re-starts an activity watcher for every AgentSession
// in state "running" that exists in the store. Called once after Run().
func (s *Server) recoverWatchers() {
	workspaces, err := s.store.ListWorkspaces("")
	if err != nil {
		return
	}
	for _, ws := range workspaces {
		sessions, err := s.store.ListAgentSessionsByWorkspace(ws.ID)
		if err != nil {
			continue
		}
		for _, sess := range sessions {
			if sess.State == pigtail.AgentStateRunning {
				s.watcher.Start(sess)
			}
		}
	}
}

// DefaultOutputsAddr is the outputs server's address when -http is on and
// -outputs-http is not given.
const DefaultOutputsAddr = "127.0.0.1:7801"

// ResolveOutputsAddr is pigtaild's -outputs-http rule: an explicit value
// wins ("" turns the listener off); otherwise DefaultOutputsAddr when HTTP is
// on, off when it is off (a second daemon run with -http "" binds nothing).
func ResolveOutputsAddr(httpAddr, flagValue string, explicit bool) string {
	if explicit {
		return flagValue
	}
	if httpAddr == "" {
		return ""
	}
	return DefaultOutputsAddr
}

// ValidateOutputsAddr refuses an outputs address equal to the HTTP one or
// not on the loopback ("" = off, accepted).
func ValidateOutputsAddr(outputsAddr, httpAddr string) error {
	if outputsAddr == "" {
		return nil
	}
	if outputsAddr == httpAddr {
		return fmt.Errorf("outputs-http: %s is the -http address; the outputs server needs its own origin", outputsAddr)
	}
	host, _, err := net.SplitHostPort(outputsAddr)
	if err != nil {
		return fmt.Errorf("outputs-http: %q is not host:port: %w", outputsAddr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("outputs-http: %q is not a loopback address (127.0.0.1, ::1 or localhost)", outputsAddr)
	}
	return nil
}

// outputsBase is the outputs server's base URL ("http://" + the address
// really bound), "" while the listener is off or failed to bind.
func (s *Server) outputsBase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outputsBound == "" {
		return ""
	}
	return "http://" + s.outputsBound
}

// outputsURLs returns, for one request, the served_url of a path: the outputs
// server's base + ServedPath when its own index opens the path, "" otherwise.
// nil while the listener is off or its index cannot be read.
func (s *Server) outputsURLs() func(path string) string {
	s.mu.Lock()
	bound, h := s.outputsBound, s.outputsH
	s.mu.Unlock()
	if bound == "" || h == nil {
		return nil
	}
	opens, err := h.Opener()
	if err != nil {
		log.Printf("outputs: served_url off for this request: %v", err)
		return nil
	}
	return func(path string) string {
		if !opens(path) {
			return ""
		}
		return "http://" + bound + staticsrv.ServedPath(path)
	}
}

// outputsHandler builds the outputs server's handler for the bound address:
// Host must be that address or localhost on its port; a served page may be
// framed by the web front's origins only ('none' with HTTP off); no scope's
// launch dir is ever opened whole.
func (s *Server) outputsHandler(bound, webAddr string) *staticsrv.OutputsHandler {
	hosts := []string{bound}
	if _, port, err := net.SplitHostPort(bound); err == nil {
		hosts = append(hosts, "localhost:"+port)
	}
	var ancestors []string
	if webAddr != "" {
		ancestors = append(ancestors, "http://"+webAddr)
		if _, port, err := net.SplitHostPort(webAddr); err == nil {
			ancestors = append(ancestors, "http://localhost:"+port)
		}
	}
	var denied []string
	if t := scope.Active(); t != nil {
		for _, sc := range t.Names() {
			if d, ok := t.LaunchDir(sc); ok {
				denied = append(denied, d)
			}
		}
	}
	src := func() (string, []staticsrv.ServedEntry, error) {
		if s.store == nil {
			return "", nil, nil
		}
		return s.store.ServableOutputs()
	}
	return staticsrv.NewOutputsHandler(src, hosts, ancestors, denied)
}

func (s *Server) addListener(l net.Listener) {
	s.mu.Lock()
	s.listeners = append(s.listeners, l)
	s.mu.Unlock()
}

func (s *Server) shutdownAll(_ *http.Server) {
	s.mu.Lock()
	for _, l := range s.listeners {
		_ = l.Close()
	}
	s.listeners = nil
	s.mu.Unlock()
}

// daemonMaintenanceDir is where the daemon reads the maintenance state: the
// same rule as the CLI (maintenanceDir) and the shell hooks —
// $PIGTAIL_MAINTENANCE_DIR when set in the daemon's environment, else the
// socket's directory, like the inbox markers.
func daemonMaintenanceDir(cfg Config, getenv func(string) string) string {
	if d := getenv("PIGTAIL_MAINTENANCE_DIR"); d != "" {
		return d
	}
	return filepath.Dir(cfg.SocketPath)
}

// logRuntimeClaim says at start which session holds the installed runtime
// (D3): the lease file outlives the daemon, the log tells it was found.
func logRuntimeClaim(dir, human string, alive runtimeclaim.AliveFunc) {
	st, ok, err := runtimeclaim.Load(dir)
	switch {
	case err != nil:
		log.Printf("runtime claim %s unreadable: %v", filepath.Join(dir, runtimeclaim.FileName), err)
	case ok:
		who := "session " + maintenance.ShortID(st.Session)
		if st.Session == "" {
			if human == "" {
				human = usersettings.DefaultHumanName
			}
			who = human + " outside Claude Code"
		}
		log.Printf("runtime claim %s held by %s since %s (%s)", st.ID, who, st.Since.Format(time.RFC3339), st.Liveness(alive))
	}
}

// --- router & middleware --------------------------------------------------

func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(recoverMW)
	r.Use(requestIDMW)
	r.Use(apiVersionMW)
	r.Use(s.authMW)

	svc := &handlers.Services{
		HumanName: s.userSettings().Human.Name,
		Store:     s.store, Bus: s.bus, Backend: s.backend, Agent: s.agent, Watcher: s.watcher,
		Ports: s.allocator, StaticMgr: s.staticMgr, StaticPortRange: s.cfg.StaticPortRange,
		RAMMonitor:          s.ramMonitor,
		CPUSlots:            s.cpuSlots,
		Admission:           s.admission,
		SessionSampler:      s.sessionSampler,
		SessionBudget:       s.sessionBudget,
		MainFresh:           s.mainFresh,
		Offsite:             s.offsite,
		OffsiteDestDir:      s.cfg.DBOffsiteDir,
		OffsiteFallbackDest: s.cfg.DBOffsiteFallbackDest,
		Maintenance:         s.maintenance,
		RuntimeClaimDir:     daemonMaintenanceDir(s.cfg, os.Getenv),
		Alive:               claudesession.SessionAlive,
		Logf:                log.Printf,
		PortBusy:            s.cfg.PortBusy,
		ProcessCwds:         s.cfg.ProcessCwds,
		Spaces:              s.cfg.Spaces,
		Hooks:               s.hooks,
	}
	svc.SyncWorkspaceMarkers = s.syncWorkspaceMarkers
	svc.OutputsURLs = s.outputsURLs // read at each request: Run binds after this
	s.services = svc
	if s.cmuxSync != nil {
		svc.CmuxSidebar = s.cmuxSync
	}
	if s.focusPri != nil { // never a typed nil pointer in the interface
		svc.FocusPri = s.focusPri
	}
	if s.lid != nil { // never a typed nil pointer in the interface
		svc.Lid = s.lid
	}
	if s.cfg.GateCmd != "" {
		svc.Gates = &gates.Source{Cmd: s.cfg.GateCmd}
	}
	svc.UsageOverview = newUsageOverviewSource(s.cfg.UsageStateDir, s.usageHistory).Overview
	svc.CmuxCheckNotice = func() (pigtail.CmuxCheckNotice, error) {
		n, err := cmuxcheck.LoadNotice(cmuxcheck.StateDir())
		if errors.Is(err, fs.ErrNotExist) {
			return pigtail.CmuxCheckNotice{}, nil
		}
		if err != nil {
			return pigtail.CmuxCheckNotice{}, err
		}
		if pid, err := cmuxcheck.ReadSentinel(cmuxcheck.DefaultSentinel()); err != nil || pid != n.LaunchPID {
			return pigtail.CmuxCheckNotice{}, nil // cmux down or relaunched since: no notice
		}
		return pigtail.CmuxCheckNotice{LaunchPID: n.LaunchPID, At: n.At, Line: n.Line}, nil
	}
	resp := serverResponder{}

	r.Get("/health", s.handleHealth)
	r.Get("/version", s.handleVersion)
	r.Get("/backup", handlers.Backup(svc, resp))
	r.Post("/backup/offsite/run", handlers.RunOffsiteBackup(svc, resp))
	r.Get("/events", s.handleEvents)
	// Maintenance --freeze (D1, D10): creating a workspace is frozen, through
	// both Backend.Create paths; `pigtail run` (/workspaces/{id}/run) is not.
	fz := r.With(handlers.FreezeMW(svc, resp))
	fz.Post("/dispatch", handlers.Dispatch(svc, resp))
	r.Post("/workspaces", handlers.AdoptWorkspace(svc, resp))
	fz.Post("/workspaces/new", handlers.NewWorkspace(svc, resp))
	r.Get("/workspaces", handlers.ListWorkspaces(svc, resp))
	r.Get("/workspaces/{id}", handlers.GetWorkspace(svc, resp))
	r.Delete("/workspaces/{id}", handlers.CleanupWorkspace(svc, resp))
	r.Post("/workspaces/gc", handlers.GCWorkspaces(svc, resp))
	r.Post("/workspaces/{id}/rename", handlers.RenameWorkspace(svc, resp))
	r.Patch("/workspaces/{id}/ports", handlers.PatchWorkspacePorts(svc, resp))
	r.Post("/workspaces/{id}/run", handlers.RunAgent(svc, resp))
	r.Get("/workspaces/{id}/preflight", handlers.PreflightWorkspace(svc, resp))
	r.Post("/workspaces/{id}/hooks/run", handlers.RunWorkspaceHooks(svc, resp))
	r.Post("/projects", handlers.CreateProject(svc, resp))
	r.Get("/projects", handlers.ListProjects(svc, resp))
	r.Post("/agents/{id}/reply", handlers.ReplyAgent(svc, resp))
	s.wireLauncher(svc)
	s.wireSDK(svc)
	s.wireStaffing(svc)
	handlers.RegisterSubjectRoutes(r, svc, resp)
	handlers.RegisterStaticServerRoutes(r, svc, resp)
	handlers.RegisterServerRoutes(r, svc, resp)
	handlers.RegisterBatteryRoutes(r, svc, resp)
	handlers.RegisterLidRoutes(r, svc, resp)
	handlers.RegisterPurgeRoutes(r, svc, resp)
	handlers.RegisterCPUSlotRoutes(r, svc, resp)
	handlers.RegisterAdmissionRoutes(r, svc, resp)
	handlers.RegisterGateRoutes(r, svc, resp)
	handlers.RegisterUsageRoutes(r, svc, resp)
	handlers.RegisterAlertRoutes(r, svc, resp)
	if s.roles != nil {
		svc.RoleSnapshot = func() ([]pigtail.RoleStatus, bool) { return s.roles.Snapshot(), true }
	}
	handlers.RegisterRoleRoutes(r, svc, resp)
	handlers.RegisterCmuxCheckNoticeRoutes(r, svc, resp)
	handlers.RegisterSessionBudgetRoutes(r, svc, resp)
	r.Get("/board", handlers.Board(svc))
	s.RegisterWebRoutes(r)
	r.Get("/maintenance", handlers.GetMaintenance(svc, resp))
	handlers.RegisterRuntimeClaimRoutes(r, svc, resp)

	// Session-message relay. Inbox markers live next to the socket, so a
	// test daemon on its own socket never touches the real inbox.
	s.relay = handlers.NewRelay(svc, handlers.RelayConfig{
		IdlePause:        time.Duration(s.cfg.IdleSessionPauseSeconds) * time.Second,
		DoneCommand:      s.userSettings().Session.DoneCommand, // "" = never typed
		MarkerDir:        filepath.Join(filepath.Dir(s.cfg.SocketPath), "inbox"),
		WaitMarkerDir:    filepath.Join(filepath.Dir(s.cfg.SocketPath), "wait"),
		StandbyMarkerDir: filepath.Join(filepath.Dir(s.cfg.SocketPath), "standby"),
		DraftDir:         filepath.Join(filepath.Dir(s.cfg.SocketPath), "handoff-draft"), // the Stop hook's handoff drafts
		// Idle wake (lot 2): send/read-screen/identify bounded at 3 s (D34), the
		// sweep's listing at listingCallTimeout (D36), the send-time listing at
		// 3 s (D37), each aimed by UUID with no CMUX_* in its environment.
		Wake: &handlers.WakeDeps{
			ReadNative:         claudesession.ReadNative,
			SessionAlive:       claudesession.SessionAlive,
			PidAlive:           claudesession.PidAlive, // D38
			Frozen:             s.frozenSessions,
			CmuxSessions:       cmux.SessionsJSONListing,
			CmuxSessionsAtSend: cmux.SessionsJSONListingBounded,
			Focused:            cmux.FocusedSurfaceID,
			ReadScreen:         cmux.ReadScreenBounded,
			SendLine:           cmux.SendLineBounded,
			// T-f: a file read, never a cmux call — the wake holds while a
			// pigtail cmux check --fix types into panes.
			CheckRunning: func() bool {
				return cmuxcheck.Running(cmuxcheck.StateDir(), claudesession.PidAlive, time.Now())
			},
			// The session budget (Tâche 8): one non-waiting ask per wake, its
			// release, and the deferred gauge of the last pass. nil-safe, as
			// the controller is: a disabled gate grants every ask.
			BudgetAcquire:  s.sessionBudget.BudgetAcquire,
			BudgetRelease:  s.sessionBudget.BudgetRelease,
			BudgetDeferred: s.sessionBudget.SetDeferredWakes,
			// The idle pause (pause_wiring.go): SessionAlive above is its
			// liveness too.
			Background:     pauseBackground,
			ProcessEnviron: cmux.ProcessEnviron,
			Signal:         pauseSignal,
			WaitGone:       pauseWaitGone,
			// The auto-retry (retry_wiring.go): the API probe and the transcript reads.
			APIReachable:      retryAPIReachable,
			LastAPIError:      retryLastAPIError,
			HumanPromptsSince: retryHumanPromptsSince,
		},
	})
	// A session freed in a sample: one early wake pass (read by Observe, which
	// the sampler goroutine calls only after Run, so this write races nothing).
	s.sessionBudget.OnFreed = s.relay.KickWake
	svc.RespawnNotifier = s.relay // a final respawn tells its session (relay_respawn.go)
	handlers.RegisterRelayRoutes(r, s.relay, resp)

	return r
}

type ctxKey string

const (
	ctxRequestID ctxKey = "request_id"
	ctxIsSocket  ctxKey = "is_socket"
)

func requestIDMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := "req_" + hex.EncodeToString(b[:])
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func apiVersionMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Pigtail-API")
		if got != "" && got != pigtail.APIVersion {
			writeError(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				fmt.Sprintf("unsupported API version %q, want %q", got, pigtail.APIVersion), nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				writeError(w, r, http.StatusInternalServerError, pigtail.ErrInternal,
					fmt.Sprintf("panic: %v", rec),
					map[string]interface{}{"stack": string(stack)})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// authMW enforces bearer-token auth for HTTP loopback. Unix socket
// connections are trusted (filesystem permissions guard them).
func (s *Server) authMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if isSocketConn(r) || s.token == "" {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxIsSocket, true)))
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) &&
			subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(s.token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		// The web front's cookie path (web.go): a valid session cookie,
		// a GET on the read allowlist or a write on the exact write
		// allowlist, then Host/Origin/Sec-Fetch-Site (Origin required on a
		// write); the request then reads as the master scope, whatever it
		// named, with the cross-scope opt-in on a write only.
		if s.webCookieOK(r) {
			read := r.Method == http.MethodGet && webAPIAllowed(r)
			write := !read && webWriteAllowed(r)
			if read || write {
				if !s.webOriginOK(r, write) {
					writeError(w, r, http.StatusForbidden, pigtail.ErrUnauthorized, "web front: foreign or missing origin", nil)
					return
				}
				next.ServeHTTP(w, webAsMaster(r, write))
				return
			}
		}
		writeError(w, r, http.StatusUnauthorized, pigtail.ErrUnauthorized, "invalid or missing token", nil)
	})
}

// isSocketConn returns true if the request arrived on the Unix socket.
// We rely on net/http's RemoteAddr starting with "@" / empty for unix sockets.
func isSocketConn(r *http.Request) bool {
	if r.RemoteAddr == "" || r.RemoteAddr == "@" {
		return true
	}
	return false
}

// isPublicPath reports whether a path is served without bearer-token auth on
// the HTTP loopback. The board is a read-only, localhost-only dashboard that
// browsers open directly, so they cannot present the token. "/boardroom" and
// other non-board paths must NOT match — guard the boundary explicitly.
//
// The web front's browser paths ("/", "/assets/*", "/web/login") are
// public here too: their handlers check the Host and the cookie themselves
// (web.go). The login-code route is not: it stays socket-only.
func isPublicPath(path string) bool {
	if path == "/board" || strings.HasPrefix(path, "/board/") {
		return true
	}
	return path == "/" || path == "/web/login" || strings.HasPrefix(path, "/assets/")
}

// serverResponder adapts the package-level write helpers to handlers.Responder.
type serverResponder struct{}

func (serverResponder) JSON(w http.ResponseWriter, r *http.Request, status int, data interface{}) {
	writeJSON(w, r, status, data)
}
func (serverResponder) Error(w http.ResponseWriter, r *http.Request, status int, code pigtail.ErrorCode, msg string, details map[string]interface{}) {
	writeError(w, r, status, code, msg, details)
}

// --- response helpers -----------------------------------------------------

func writeJSON(w http.ResponseWriter, r *http.Request, status int, data interface{}) {
	resp := map[string]interface{}{
		"data": data,
		"meta": map[string]interface{}{"request_id": reqID(r)},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code pigtail.ErrorCode, msg string, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": pigtail.APIError{Code: code, Message: msg, Details: details},
		"meta":  map[string]interface{}{"request_id": reqID(r)},
	})
}

func reqID(r *http.Request) string {
	if v, ok := r.Context().Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

// --- handlers (minimal) ---------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// ?deep=1 is the watchdog's probe: it answers before StoredScopes (which
	// has no timeout) so a held store connection is a 503, never a hang.
	if r.URL.Query().Get("deep") == "1" {
		s.handleHealthDeep(w, r)
		return
	}
	tbl := scope.Active()
	var stored []string
	if v, err := s.store.StoredScopes(); err == nil {
		stored = v
	}
	// The caller's view (D19); an unreadable header is the default's view:
	// /health stays a liveness probe, it never answers 400.
	caller, err := scope.FromHeader(r.Header.Get(pigtail.HeaderScope))
	if err != nil {
		caller = tbl.Default()
	}
	sc := healthScopes(tbl, caller, s.cfg.ScopeTableErr, stored)
	// pid and http_addr: what `pigtail doctor` and setup check the running
	// daemon against (one daemon per database, the web UI's address).
	writeJSON(w, r, http.StatusOK, map[string]interface{}{
		"ok":             true,
		"daemon_version": Version,
		"pid":            os.Getpid(),
		"http_addr":      s.cfg.HTTPAddr,
		"scopes":         sc,
	})
}

// handleHealthDeep is GET /health?deep=1: the store answers a ping within
// DeepHealthTimeout and, when the role watch is on, its sweep has beaten
// lately. 200 {ok:true}, else 503 {ok:false, failing:[…]}. The relay does not
// beat: its tick includes slow cmux calls, a beat there could kick a healthy
// daemon.
func (s *Server) handleHealthDeep(w http.ResponseWriter, r *http.Request) {
	timeout := s.cfg.DeepHealthTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	ping := s.ping
	if ping == nil {
		ping = s.store.Ping
	}
	failing := []string{}
	if err := ping(ctx); err != nil {
		failing = append(failing, "store: "+err.Error())
	}
	if s.roleStale != nil {
		if msg, stale := s.roleStale(); stale {
			failing = append(failing, msg)
		}
	}
	if len(failing) > 0 {
		writeJSON(w, r, http.StatusServiceUnavailable, map[string]interface{}{"ok": false, "failing": failing})
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]interface{}{"ok": true})
}

// healthScopes is /health's "scopes": which table is active (source), its
// names, master and default, the stored scopes it does not declare (rows only
// the master sees), and the table file's error if it failed to load. A
// non-master caller reads its own scope only (D19): names = [caller], no
// master, default nor undeclared, and a generic error.
func healthScopes(tbl *scope.Table, caller scope.Scope, loadErr error, stored []string) map[string]interface{} {
	if !tbl.SeesAll(caller) {
		sc := map[string]interface{}{"source": tbl.Source(), "names": tbl.NameableBy(caller)}
		if loadErr != nil {
			sc["error"] = "the scope table file is invalid (details: master scope)"
		}
		return sc
	}
	und := []string{}
	for _, v := range stored {
		if !tbl.Has(scope.Scope(v)) {
			und = append(und, v)
		}
	}
	sc := map[string]interface{}{
		"source": tbl.Source(), "names": tbl.Names(), "master": tbl.Master(), "default": tbl.Default(),
		"undeclared": und,
	}
	if loadErr != nil {
		sc["error"] = loadErr.Error()
	}
	return sc
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	branch, commit, dirty := buildinfo.Fields()
	writeJSON(w, r, http.StatusOK, map[string]interface{}{
		"daemon_version": Version,
		"daemon_branch":  branch,
		"daemon_commit":  commit,
		"daemon_dirty":   dirty,
		"supported_apis": []string{pigtail.APIVersion},
	})
}

// --- helpers --------------------------------------------------------------

func defaultSocketPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pigtail", "pigtail.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pigtail", "pigtail.sock")
}

func loadOrCreateToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// syncWorkspaceMarkers rewrites the workspace-subject markers from the database, one caller at a time;
// a failure is logged once per distinct message (markerErr), not once per tick.
func (s *Server) syncWorkspaceMarkers() {
	s.markerMu.Lock()
	defer s.markerMu.Unlock()
	msg := ""
	if err := s.writeWorkspaceMarkers(); err != nil {
		msg = err.Error()
	}
	if msg != "" && msg != s.markerErr {
		logf := s.markerLogf
		if logf == nil {
			logf = log.Printf
		}
		logf("%s", msg)
	}
	s.markerErr = msg
}

// writeWorkspaceMarkers reads links and declarations, then rewrites the markers; the caller holds markerMu.
func (s *Server) writeWorkspaceMarkers() error {
	states := map[string]string{}
	bs, err := s.store.ListAllCmuxBindings()
	if err != nil {
		return fmt.Errorf("wsmarker: list links: %w", err)
	}
	for _, b := range bs {
		states[b.CmuxWorkspaceID] = pigtail.WorkspaceStateBound
	}
	ots, err := s.store.ListOffTopic()
	if err != nil {
		return fmt.Errorf("wsmarker: list off-topic: %w", err)
	}
	for _, o := range ots {
		if _, bound := states[o.CmuxWorkspaceID]; !bound {
			states[o.CmuxWorkspaceID] = pigtail.WorkspaceStateOffTopic
		}
	}
	if s.markerTestHook != nil { // TestWorkspaceMarkersConcurrent only
		s.markerTestHook()
	}
	if err := wsmarker.Sync(s.markerDir, states); err != nil {
		return fmt.Errorf("wsmarker: sync %s: %w", s.markerDir, err)
	}
	return nil
}

// runWorkspaceMarkers syncs once, then every markerEvery until ctx ends.
func (s *Server) runWorkspaceMarkers(ctx context.Context) {
	s.syncWorkspaceMarkers()
	t := time.NewTicker(s.markerEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.syncWorkspaceMarkers()
		}
	}
}

// userSettings is Config.UserSettings, else the neutral defaults.
func (s *Server) userSettings() usersettings.Settings {
	if s.cfg.UserSettings != nil {
		return *s.cfg.UserSettings
	}
	return usersettings.Defaults()
}
