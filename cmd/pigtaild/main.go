// Command pigtaild is the pigtail background daemon.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/agent/tmux"
	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/buildinfo"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon"
	"github.com/aloysbr/pigtail/internal/daemon/clamshell"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxwatch"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/orphans"
	"github.com/aloysbr/pigtail/internal/daemon/powersrc"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/daemon/thermal"
	"github.com/aloysbr/pigtail/internal/daemon/tmpsweep"
	"github.com/aloysbr/pigtail/internal/dbtool"
	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Version of this binary. Stamped at build time by the Makefile
// (-ldflags -X main.Version); "dev" is an unstamped `go build`.
var Version = "dev"

func main() {
	// One-shot modes first, before any flag parsing or side effect: they
	// read the arguments and exit, the daemon never starts.
	if handled, code := versionMode(os.Args[1:], os.Stdout); handled {
		os.Exit(code)
	}
	if len(os.Args) > 1 && (os.Args[1] == "-check-args" || os.Args[1] == "--check-args") {
		os.Exit(checkArgs(os.Args[2:], os.Stdout, os.Stderr))
	}
	if handled, code := dbToolMode(os.Args[1:], dbtool.IO{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, IsTTY: isStdinTTY, Now: time.Now,
	}, loadDaemonScopeTable); handled {
		os.Exit(code)
	}
	flag.CommandLine.Usage = usage(flag.CommandLine)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// versionMode handles `pigtaild -version` (or --version) as the first
// argument: it prints the build identity line and reports handled, exit 0.
// Any other argument list is left to the real start.
func versionMode(args []string, out io.Writer) (handled bool, code int) {
	if len(args) == 0 || (args[0] != "-version" && args[0] != "--version") {
		return false, 0
	}
	_, _ = fmt.Fprintln(out, buildinfo.Line("pigtaild", Version))
	return true, 0
}

// usage wraps the flag set's help so it names the one-shot modes, which are
// read before the flags are parsed and so never appear among them.
func usage(fs *flag.FlagSet) func() {
	return func() {
		w := fs.Output()
		_, _ = fmt.Fprintf(w, "Usage of %s:\n", fs.Name())
		_, _ = fmt.Fprintf(w, "  %s -version\n\tprint the build identity (version, branch, commit, dirty) and exit\n", fs.Name())
		_, _ = fmt.Fprintf(w, "  %s -check-args <flags…>\n\tvalidate a start's flags as a real start would (unknown flag, invalid value), start nothing; exit 0 if they are accepted, 2 otherwise\n", fs.Name())
		_, _ = fmt.Fprintf(w, "  %s -db-tool list|restore <flags…>\n\tpigtaild's backup tool for `pigtail db` (backups, daemon stopped); run it through `pigtail db`\n", fs.Name())
		_, _ = fmt.Fprintf(w, "  %s [flags]\n\tstart the daemon\n", fs.Name())
		fs.PrintDefaults()
	}
}

func run() error {
	stripCmuxEnv()
	f := registerFlags(flag.CommandLine)
	flag.Parse()
	cfg, err := f.config()
	if err != nil {
		return err
	}
	aimCmuxSocket(*f.cmuxSocketFlag)
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}

	// One daemon per database: taken before the store opens and before any
	// socket is bound, so a second start never touches the first one's socket.
	releaseLock, err := daemon.AcquireInstanceLock(cfg.DBPath)
	if err != nil {
		return err
	}
	defer releaseLock()

	// An invalid scopes.yaml stops the start, before the store and the
	// socket: the fallback would be the builtin, whose one scope is the
	// master — every caller would see everything.
	tbl, tblErr := loadDaemonScopeTable()
	if tblErr != nil {
		return fmt.Errorf("ERROR scope table: %w — fix %s and restart", tblErr, filepath.Join(setup.ConfigDir(), scope.TableFile))
	}
	scope.Use(tbl)
	cfg.Spaces = loadDaemonSpaceTable(tbl, log.Printf)
	if runtime.GOOS == "darwin" {
		// pigtail lid: sleep control with the lid closed (sudo -n pmset, allowed
		// by the rule `pigtail lid setup` installs), its battery guard and its
		// lid-closed heat guard (notifyutil, ioreg: no root).
		cfg.LidRunner = lid.ExecRunner{}
		cfg.LidBattery = powersrc.ReadLevel
		cfg.LidThermal = thermal.Read
		cfg.LidClosed = clamshell.Closed
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()
	if stored, err := st.StoredScopes(); err == nil {
		for _, sc := range stored {
			if !tbl.Has(scope.Scope(sc)) {
				log.Printf("WARN scope %q is stored in %s but not declared in the scope table — only %s sees its rows", sc, cfg.DBPath, tbl.Master())
			}
		}
	}

	bus := events.New()

	// Port ranges come from each project's pigtail.yaml; the allocator
	// itself is stateless wrt ranges.
	allocator := daemon.NewAllocator(st)
	cfg.Allocator = allocator
	backend := gitbackend.New(allocator)
	agentImpl := tmux.New()

	// settings.yaml: read once at start; a bad file gives the defaults and one log line.
	us, uerr := usersettings.Load(setup.ConfigDir())
	if uerr != nil {
		log.Printf("WARN %v — using the default settings", uerr)
	}
	cfg.UserSettings = &us
	maintenance.HumanName = us.Human.Name
	maintenance.FreezeAlsoRefuses = us.Maintenance.FreezeAlsoRefuses
	cmuxcheck.HumanName = us.Human.Name
	waittype.Configure(us.WaitCommands()) // the user's own commands: none by default

	srv, err := daemon.New(cfg, st, bus, backend, agentImpl)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutdown signal received")
		cancel()
	}()

	fmt.Printf("[pigtaild %s] socket=%s http=%s db=%s\n", Version, cfg.SocketPath, cfg.HTTPAddr, cfg.DBPath)
	logFootprintSelfCheck(sysmem.SelfCheckLine, log.Printf)
	if err := srv.Run(ctx); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

// daemonFlags holds every pigtaild flag registered on one flag set. The real
// start (run) and `pigtaild -check-args` (checkArgs) both go through
// registerFlags then config: one flag set, one set of validations, so a start
// the check accepts is a start run accepts (plan 2026-10-18-install-guard, D2).
type daemonFlags struct {
	socketFlag           *string
	httpFlag             *string
	outputsHTTPFlag      *string
	outputsHTTPSet       func() bool
	dbFlag               *string
	watchdogFlag         *bool
	ramFlag              *bool
	ramFloorFlag         *int
	ramGraceFlag         *int
	ramIntervalFlag      *int
	ramConfirmFlag       *int
	batteryModeFlag      *bool
	batteryGraceFlag     *int
	idlePurgeGraceFlag   *int
	idleSessionPauseFlag *int
	idleSessionCloseFlag *int // the old name, an alias (T-b)
	idlePauseSet         func() (pause, close bool)
	sessionSampleFlag    *int
	sessionFloorFlag     *int
	sessionCeilingFlag   *int
	sessionWindowFlag    *time.Duration
	sessionMinGapFlag    *time.Duration
	sessionCompressFlag  *int
	sessionMinAvailFlag  *int
	idleCPUThresholdFlag *float64
	idleNetSignalFlag    *bool
	maxServersFlag       *int
	cpuSlotsFlag         *int
	cpuSlotMaxHoldFlag   *time.Duration
	admitMaxJobsFlag     *int
	admitFloorFlag       *int
	admitDiskFloorFlag   *int
	diskAlertFlag        *int
	admitPressureFlag    *int
	admitSwapoutFlag     *int
	admitCompressFlag    *int
	admitMaxStarveFlag   *int

	admitCompressCalmFlag, admitCompressCalmStarveFlag *int

	admitMaxHoldFlag        *int
	cmuxSidebarFlag         *bool
	cmuxClaudeCodeFixFlag   *bool
	cmuxBirthGraceFlag      *time.Duration
	cmuxFoldersFlag         *bool
	mainRefreshFlag         *bool
	mainRefreshIntervalFlag *int
	usageRefreshFlag        *bool
	roleWatchFlag           *bool
	usageRefreshSecondsFlag *int
	usageStateDirFlag       *string
	staticRangeFlag         *string
	dbOffsiteFlag           *string
	dbOffsiteFallbackFlag   *string
	gateCmdFlag             *string
	sdkHostFlag             *string
	sdkNodeFlag             *string
	sdkLaunchFlag           *bool
	cmuxSocketFlag          *string
	orphanReaperFlag        *string
	orphanReapAfterFlag     *time.Duration
	tmpSweepFlag            *string

	admitClassLimits   stringList
	cmuxSidebarWindows stringList

	cmuxCheckMode          func() (string, error)
	cmuxCheckCtrlC         func() (string, error)
	cmuxInputProbe         func() (bool, error)
	cmuxFolderSortDisabled func() bool
}

// registerFlags registers every pigtaild flag on fs.
func registerFlags(fs *flag.FlagSet) *daemonFlags {
	f := &daemonFlags{}
	f.socketFlag = fs.String("socket", "", "Unix socket path")
	f.httpFlag = fs.String("http", "127.0.0.1:7800", "HTTP loopback address ('' to disable)")
	f.outputsHTTPFlag = fs.String("outputs-http", "", "outputs server loopback address (default "+daemon.DefaultOutputsAddr+" when -http is on, off when -http is ''; '' to disable)")
	f.outputsHTTPSet = func() (set bool) {
		fs.Visit(func(fl *flag.Flag) {
			if fl.Name == "outputs-http" {
				set = true
			}
		})
		return set
	}
	f.dbFlag = fs.String("db", "", "SQLite database path")
	f.watchdogFlag = fs.Bool("watchdog", true, "kill runaway dev servers (per-project tuning in pigtail.yaml)")
	f.ramFlag = fs.Bool("ram-budget", true, "evict idle dev servers under system memory pressure")
	f.ramFloorFlag = fs.Int("ram-floor-mb", 6144, "evict when system available RAM drops below this (MiB)")
	f.ramGraceFlag = fs.Int("ram-grace-seconds", 600, "only evict servers idle at least this long")
	f.ramIntervalFlag = fs.Int("ram-interval-seconds", 15, "RAM pressure sweep cadence")
	f.ramConfirmFlag = fs.Int("ram-confirm-windows", 2, "consecutive under-floor sweeps before evicting")
	f.batteryModeFlag = fs.Bool("battery-mode", true, "on battery, evict idle dev servers regardless of RAM")
	f.batteryGraceFlag = fs.Int("battery-grace-seconds", 600, "on battery, only evict servers idle at least this long")
	f.idlePurgeGraceFlag = fs.Int("idle-purge-grace-seconds", 0, "always evict servers idle at least this long, regardless of power/RAM (0 = off)")
	f.idleSessionPauseFlag = fs.Int("idle-session-pause-seconds", 3600, "stop a Claude session idle at least this long, one per relay sweep; its tab offers to relaunch it (0 = off)")
	f.idleSessionCloseFlag = fs.Int("idle-session-close-seconds", 3600, "the old name of -idle-session-pause-seconds")
	f.idlePauseSet = func() (pause, close bool) {
		fs.Visit(func(fl *flag.Flag) {
			switch fl.Name {
			case "idle-session-pause-seconds":
				pause = true
			case "idle-session-close-seconds":
				close = true
			}
		})
		return pause, close
	}
	f.sessionSampleFlag = fs.Int("session-sample-seconds", daemon.DefaultSessionSampleSeconds,
		"cadence of the session sampler (live and busy Claude sessions, memory and CPU per session, kept in session-samples.jsonl; 0 = off)")
	sb := sessbudget.DefaultConfig()
	f.sessionFloorFlag = fs.Int("session-floor", sb.Floor,
		"session budget under memory pressure; agent spawns and wakes are always admitted under it")
	f.sessionCeilingFlag = fs.Int("session-ceiling", sb.Ceiling,
		"session budget when memory is green (0 = session gate off: every launch admitted; needs -session-sample-seconds > 0)")
	f.sessionWindowFlag = fs.Duration("session-window", sb.Window,
		"span the session budget judges memory pressure over")
	f.sessionMinGapFlag = fs.Duration("session-min-gap", sb.MinGap,
		"minimum time between two agent spawns or wakes admitted above the floor")
	f.sessionCompressFlag = fs.Int("session-compress-kb-per-sec", int(sb.CompressBytesPerSec)>>10,
		"mean compressor traffic (KiB/s) over which the session budget drops to the floor")
	f.sessionMinAvailFlag = fs.Int("session-min-available", 0,
		"available memory (MiB) under which the session budget drops to the floor (0 = the admission floor, -admit-floor-mb)")
	f.idleCPUThresholdFlag = fs.Float64("idle-cpu-threshold-percent", rammon.Defaults().IdleCPUThresholdPercent,
		"percent of one core a dev server's process subtree must average over 5 minutes to count as in use (pin a server to keep it at rest)")
	f.idleNetSignalFlag = fs.Bool("idle-net-signal", rammon.Defaults().NetSignal,
		"also count a page load or reload (a burst of new connections in one sweep) as use; runs lsof and netstat/ss on every sweep where a dev server is live, and an open tab polling its server can trip it")
	f.maxServersFlag = fs.Int("max-servers", 0, "cap on live dev servers; evict the most-idle beyond this (0 = off)")
	f.cpuSlotsFlag = fs.Int("cpu-slots", -1,
		"CPU slots shared between all test runs on this machine (-1 = platform default, 0 = disable the arbiter)")
	f.cpuSlotMaxHoldFlag = fs.Duration("cpu-slot-max-hold", 10*time.Minute,
		"revoke the accounting of a CPU slot held longer than this (nothing is killed, the client is not told)")
	f.admitMaxJobsFlag = fs.Int("admit-max-jobs", -1,
		"heavy commands (tsc, eslint, jest, vitest, nx, storybook build) admitted at once (-1 = platform default: min(perf cores, RAM/10 GiB), 0 = disable admission)")
	f.admitFloorFlag = fs.Int("admit-floor-mb", 6144,
		"available memory every admission must leave behind (MiB)")
	f.admitDiskFloorFlag = fs.Int("admit-disk-floor-gb", daemon.DefaultAdmitDiskFloorGB,
		"free disk space (GiB) under which no heavy command is admitted, even on an empty machine (0 = off)")
	f.diskAlertFlag = fs.Int("disk-alert-gb", 40,
		"free disk space (GiB) under which pigtaild raises the low-disk alert (journal disk-low, event disk.low, GET /alerts); it closes 10 GiB above (0 or -1 = alert off: nothing is raised, and one left open by a previous run is closed at start)")
	f.admitPressureFlag = fs.Int("admit-pressure-level", 2,
		"brake admissions while the kernel memory-pressure level is at or above this — tighter swap gate and more headroom, not a stop (2 = warn, 0 = off)")
	f.admitSwapoutFlag = fs.Int("admit-swapout-kb-per-sec", 4096,
		"hold heavy commands while the machine swaps out faster than this (KiB/s, 0 = off)")
	f.admitCompressFlag = fs.Int("admit-compress-kb-per-sec", 16384,
		"hold heavy commands while the memory compressor works faster than this — compressions + decompressions (KiB/s, 0 = off)")
	f.admitCompressCalmFlag = fs.Int("admit-compress-calm-seconds", 60,
		"the heavy classes (build, vitest-browser) start only once compressor traffic has also stayed under -admit-compress-kb-per-sec over this many seconds, and a long wait does not lift it (0 = off: they are gated like any other class)")
	f.admitCompressCalmStarveFlag = fs.Int("admit-compress-calm-max-starve-seconds", 0,
		"release a heavy-class command held by that rule once it has waited this long (0 = never, the default)")
	f.admitMaxStarveFlag = fs.Int("admit-max-starve-seconds", 180,
		"a queued command waiting longer than this owes nothing but the memory floor: the job ceiling and the pressure brake stop applying to it (0 = off)")
	f.admitMaxHoldFlag = fs.Int("admit-max-hold-seconds", 1800,
		"revoke the accounting of an admission held longer than this (nothing is killed, the command is not told)")
	fs.Var(&f.admitClassLimits, "admit-class-limit",
		"cap concurrent admissions of one class, as <class>:<n>; repeatable (default vitest-browser:1, go-test:2, build:1). <class>:0 turns that cap off")
	f.cmuxSidebarFlag = fs.Bool("cmux-sidebar", false,
		"drive the cmux sidebar: attention pills, and title + description of workspaces bound to a subject (POST /cmux/sidebar flips it until restart)")
	fs.Var(&f.cmuxSidebarWindows, "cmux-sidebar-window",
		"restrict the cmux sidebar loop's writes to this cmux window (UUID); repeatable. Default: every window")
	f.cmuxClaudeCodeFixFlag = fs.Bool("cmux-claude-code-fix", true,
		"temporary patch, while cmux lacks manaflow-ai/cmux#15666/#15603: when every Claude session of a workspace is idle (native file, ≥ 10 s) and cmux's own claude_code row still reads Running, put it back to Idle. =false switches the patch off, not the sidebar loop")
	f.cmuxBirthGraceFlag = fs.Duration("cmux-birth-grace", 30*time.Minute,
		"how long an unbound workspace stays where it was born before « Inbox » takes it; shortened to check on a throwaway window")
	f.cmuxFoldersFlag = fs.Bool("cmux-folders", false,
		"arrange the cmux sidebar in folders, one per space then « Inbox », while the cmux window is not key; an unbound workspace stays where it was born until declared off-topic or --cmux-birth-grace ends; acts only while the sidebar loop is on (--cmux-sidebar)")
	f.mainRefreshFlag = fs.Bool("refresh-main", true,
		"periodically fast-forward each project's main worktree onto its base (never over a dirty or repurposed checkout)")
	f.mainRefreshIntervalFlag = fs.Int("refresh-main-interval-seconds", 600, "main-worktree refresh sweep cadence")
	f.usageRefreshFlag = fs.Bool("usage-refresh", true,
		"periodically refresh the Claude usage readings older than 20 min at the default cadence, to keep them under 30 min (claude -p /usage, one account at a time, no tokens), for the account rotation and Staffing's weekly budget")
	f.roleWatchFlag = fs.Bool("role-watch", true,
		"judge the declared roles (pigtail role add) every minute and journal one alert per missed deadline; off: pigtail role status answers watch: false")
	f.usageRefreshSecondsFlag = fs.Int("usage-refresh-interval-seconds", 600, "background usage refresh sweep cadence")
	f.usageStateDirFlag = fs.String("usage-state-dir", "",
		"directory of the usage state: usage-history.jsonl, usage-refresh.json and the refresh lock (default: pigtail's state dir, next to spawns.jsonl)")
	f.staticRangeFlag = fs.String("static-port-range", "8080-8099", "daemon-global port range for static servers (min-max)")
	f.dbOffsiteFlag = fs.String("db-offsite-dir", "",
		"copy the newest database backup here after each rotation (off by default). "+
			"Either a local directory that must already exist, or an rsync-style "+
			"[user@]host:/path, which needs no mount and survives a reboot")
	f.dbOffsiteFallbackFlag = fs.String("db-offsite-fallback-dest", "",
		"prove the redundant route to the same destination stays alive, by exercising this pinned alias every 6h (off by default). "+
			"Typically the same [user@]host:/path as -db-offsite-dir, through an ~/.ssh/config Host that is not allowed to fall back to the LAN.")
	f.cmuxCheckMode = cmuxCheckModeFlag(fs)
	f.cmuxCheckCtrlC = cmuxCheckCtrlCFlag(fs)
	f.cmuxInputProbe = cmuxInputProbeFlag(fs)
	f.cmuxFolderSortDisabled = cmuxFolderSortDisabledFlag(fs)
	f.cmuxSocketFlag = fs.String("cmux-socket", "",
		"aim every cmux call at this cmux socket (a second instance, e.g. cmux NIGHTLY for a live pass); absolute, ≤ 103 bytes")
	f.orphanReaperFlag = fs.String("orphan-reaper", string(orphans.ModeOn),
		"stop the process tree of a Claude Code Bash-tool shell left orphaned (its claude dead) for -orphan-reap-after: on, observe (journal orphan-tree-would-reap, never signal) or off")
	f.orphanReapAfterFlag = fs.Duration("orphan-reap-after", orphans.DefaultAfter,
		"how long the orphan reaper must see a shell orphaned before stopping its tree (counted from its first sight by this daemon)")
	f.tmpSweepFlag = fs.String("tmp-sweep", string(tmpsweep.ModeOn),
		"delete the temporary directories pigtail's own tests left behind (known test prefixes, idle for 2 h, in the temp dir and /tmp): on, observe (journal tmp-would-sweep, never delete) or off")
	f.gateCmdFlag = fs.String("gate-cmd", "", "'gate' command that GET /gates runs as '<cmd> list --json' (empty = off)")
	f.sdkHostFlag = fs.String("sdk-host", "", "directory of clients/sdkhost (the SDK session host); with -sdk-node it turns the SDK sessions on, read-only")
	f.sdkNodeFlag = fs.String("sdk-node", "", "node binary (22+) that runs the SDK session host; absolute")
	f.sdkLaunchFlag = fs.Bool("sdk-launch", false, "let POST /subjects/{id}/sdk-sessions launch SDK sessions (needs -sdk-host and -sdk-node; off by default)")
	return f
}

// sdkConfig is the SDK sessions' option: nil when neither -sdk-host nor
// -sdk-node is set; -sdk-host and -sdk-node go together, and -sdk-launch needs
// both (refused at start rather than silently ignored).
func (f *daemonFlags) sdkConfig() (*daemon.SDKConfig, error) {
	host, node := *f.sdkHostFlag, *f.sdkNodeFlag
	switch {
	case host == "" && node == "":
		if *f.sdkLaunchFlag {
			return nil, fmt.Errorf("-sdk-launch needs -sdk-host and -sdk-node")
		}
		return nil, nil
	case host == "":
		return nil, fmt.Errorf("-sdk-node needs -sdk-host")
	case node == "":
		return nil, fmt.Errorf("-sdk-host needs -sdk-node")
	}
	return &daemon.SDKConfig{Host: host, Node: node, Launch: *f.sdkLaunchFlag}, nil
}

// sessionBudget builds and validates the session budget's configuration
// (sessbudget.ValidateConfig: the gate on with the sampler off is refused).
func (f *daemonFlags) sessionBudget() (sessbudget.Config, error) {
	minAvail := *f.sessionMinAvailFlag
	if minAvail == 0 {
		minAvail = *f.admitFloorFlag
	}
	if minAvail < 0 || *f.sessionCompressFlag < 0 {
		return sessbudget.Config{}, fmt.Errorf("session-min-available and session-compress-kb-per-sec: must be >= 0")
	}
	cfg := sessbudget.Config{
		Floor: *f.sessionFloorFlag, Ceiling: *f.sessionCeilingFlag,
		Window: *f.sessionWindowFlag, MinGap: *f.sessionMinGapFlag,
		CompressBytesPerSec: float64(*f.sessionCompressFlag) * 1024,
		MinAvailable:        uint64(minAvail) << 20,
		SamplePeriod:        time.Duration(*f.sessionSampleFlag) * time.Second,
	}
	if err := sessbudget.ValidateConfig(cfg); err != nil {
		return sessbudget.Config{}, err
	}
	return cfg, nil
}

// config validates the parsed flags and builds the daemon configuration:
// everything between the parse and the start, without any I/O (the socket and
// database defaults are computed, their directory is not created).
// idleSessionPause reads -idle-session-pause-seconds, or the old
// -idle-session-close-seconds when only that one is set; both set to
// different values refuse to start (T-b).
func (f *daemonFlags) idleSessionPause() (int, error) {
	pauseSet, closeSet := f.idlePauseSet()
	v := *f.idleSessionPauseFlag
	if closeSet && !pauseSet {
		v = *f.idleSessionCloseFlag
	}
	if closeSet && pauseSet && *f.idleSessionCloseFlag != *f.idleSessionPauseFlag {
		return 0, fmt.Errorf("idle-session-pause-seconds and idle-session-close-seconds (its old name) disagree: set only idle-session-pause-seconds")
	}
	if v < 0 {
		return 0, fmt.Errorf("idle-session-pause-seconds: must be >= 0 (0 = off)")
	}
	return v, nil
}

func (f *daemonFlags) config() (daemon.Config, error) {
	staticRange, err := parsePortRange(*f.staticRangeFlag)
	if err != nil {
		return daemon.Config{}, fmt.Errorf("static-port-range: %w", err)
	}
	idlePause, err := f.idleSessionPause()
	if err != nil {
		return daemon.Config{}, err
	}
	if *f.sessionSampleFlag < 0 {
		return daemon.Config{}, fmt.Errorf("session-sample-seconds: must be >= 0 (0 = off)")
	}
	sessionBudget, err := f.sessionBudget()
	if err != nil {
		return daemon.Config{}, err
	}
	cmuxMode, err := f.cmuxCheckMode()
	if err != nil {
		return daemon.Config{}, err // an unknown value refuses to start (TestParseMode)
	}
	cmuxCtrlC, err := f.cmuxCheckCtrlC()
	if err != nil {
		return daemon.Config{}, err // an unknown value refuses to start (TestCmuxCheckCtrlCFlag)
	}
	cmuxProbe, err := f.cmuxInputProbe()
	if err != nil {
		return daemon.Config{}, err // an unknown value refuses to start (TestCmuxInputProbeFlag)
	}
	if err := validCmuxSocket(*f.cmuxSocketFlag); err != nil {
		return daemon.Config{}, err // TestCmuxSocketFlagRejects
	}
	orphanMode, err := orphans.ParseMode(*f.orphanReaperFlag)
	if err != nil {
		return daemon.Config{}, fmt.Errorf("orphan-reaper: %w", err) // TestOrphanReaperFlags
	}
	if *f.orphanReapAfterFlag <= 0 {
		return daemon.Config{}, fmt.Errorf("orphan-reap-after: must be > 0 (-orphan-reaper=off turns the reaper off)")
	}
	tmpSweepMode, err := tmpsweep.ParseMode(*f.tmpSweepFlag)
	if err != nil {
		return daemon.Config{}, fmt.Errorf("tmp-sweep: %w", err) // TestTmpSweepFlags
	}
	outputsAddr := daemon.ResolveOutputsAddr(*f.httpFlag, *f.outputsHTTPFlag, f.outputsHTTPSet())
	if err := daemon.ValidateOutputsAddr(outputsAddr, *f.httpFlag); err != nil {
		return daemon.Config{}, err
	}

	sdkCfg, err := f.sdkConfig()
	if err != nil {
		return daemon.Config{}, err
	}
	cfg := daemon.Config{
		SocketPath:       *f.socketFlag,
		HTTPAddr:         *f.httpFlag,
		OutputsAddr:      outputsAddr,
		DBPath:           *f.dbFlag,
		WatchdogDisabled: !*f.watchdogFlag,
		OrphanReaper:     orphanMode,
		OrphanReapAfter:  *f.orphanReapAfterFlag,
		TmpSweep:         tmpSweepMode,
		StaticPortRange:  staticRange,
		DBOffsiteDir:     *f.dbOffsiteFlag,

		DBOffsiteFallbackDest: *f.dbOffsiteFallbackFlag,
		GateCmd:               *f.gateCmdFlag,
		SDK:                   sdkCfg,

		RAMBudgetDisabled:  !*f.ramFlag,
		RAMFloorMB:         *f.ramFloorFlag,
		RAMGraceSeconds:    *f.ramGraceFlag,
		RAMIntervalSeconds: *f.ramIntervalFlag,
		RAMConfirmWindows:  *f.ramConfirmFlag,

		BatteryDisabled:     !*f.batteryModeFlag,
		BatteryGraceSeconds: *f.batteryGraceFlag,

		IdlePurgeGraceSeconds:   *f.idlePurgeGraceFlag,
		IdleSessionPauseSeconds: idlePause,
		SessionSampleSeconds:    *f.sessionSampleFlag,
		SessionBudget:           sessionBudget,
		MaxServers:              *f.maxServersFlag,
		IdleCPUThresholdPercent: *f.idleCPUThresholdFlag,
		IdleNetSignal:           *f.idleNetSignalFlag,

		CPUSlots:       *f.cpuSlotsFlag,
		CPUSlotMaxHold: *f.cpuSlotMaxHoldFlag,

		AdmitMaxJobs:          *f.admitMaxJobsFlag,
		AdmitFloorMB:          *f.admitFloorFlag,
		AdmitDiskFloorGB:      *f.admitDiskFloorFlag,
		DiskAlertGB:           diskAlertConfig(*f.diskAlertFlag),
		AdmitPressureLevel:    offIfZero(*f.admitPressureFlag),
		AdmitSwapoutKBPerSec:  offIfZero(*f.admitSwapoutFlag),
		AdmitCompressKBPerSec: offIfZero(*f.admitCompressFlag),
		AdmitMaxStarve:        time.Duration(offIfZero(*f.admitMaxStarveFlag)) * time.Second,

		AdmitCompressCalmWindow:    time.Duration(offIfZero(*f.admitCompressCalmFlag)) * time.Second,
		AdmitCompressCalmMaxStarve: time.Duration(*f.admitCompressCalmStarveFlag) * time.Second,

		AdmitMaxHold:     time.Duration(*f.admitMaxHoldFlag) * time.Second,
		AdmitClassLimits: f.admitClassLimits,

		MainRefreshDisabled:        !*f.mainRefreshFlag,
		MainRefreshIntervalSeconds: *f.mainRefreshIntervalFlag,

		RoleWatchDisabled:           !*f.roleWatchFlag,
		UsageRefreshDisabled:        !*f.usageRefreshFlag,
		UsageRefreshIntervalSeconds: *f.usageRefreshSecondsFlag,
		UsageStateDir:               *f.usageStateDirFlag,

		CmuxSidebar:        *f.cmuxSidebarFlag,
		CmuxSidebarWindows: f.cmuxSidebarWindows,

		CmuxClaudeCodeFixDisabled: !*f.cmuxClaudeCodeFixFlag,
		CmuxFolders:               *f.cmuxFoldersFlag,
		CmuxFolderSortDisabled:    f.cmuxFolderSortDisabled(), // the inversion is in the helper (TestCmuxFolderSortFlag)
		CmuxBirthGrace:            *f.cmuxBirthGraceFlag,

		CmuxCheckMode:  cmuxMode,
		CmuxCheckCtrlC: cmuxCtrlC,
		CmuxInputProbe: cmuxProbe,
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = defaultSocketPath()
	}
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(filepath.Dir(cfg.SocketPath), "pigtail.db")
	}
	if cfg.UsageStateDir == "" {
		cfg.UsageStateDir = filepath.Dir(spawnreg.DefaultPath()) // TestUsageStateDirDefault
	}
	return cfg, nil
}

// checkArgs is `pigtaild -check-args <flags…>`: the real start's flags and
// validations on args, nothing started, nothing created. 0 and an ok line on
// stdout when a start would accept them; 2 and the reason — naming the flag —
// on stderr otherwise. post-merge runs it with the plist's ProgramArguments on
// the new binary before replacing the installed one.
func checkArgs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerFlags(fs)
	err := fs.Parse(args)
	if err == nil {
		_, err = f.config()
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pigtaild -check-args: %v\n", err)
		return 2
	}
	_, _ = fmt.Fprintln(stdout, "pigtaild -check-args: ok")
	return 0
}

// maxSocketPath is the longest Unix socket path macOS binds (104 bytes with
// the NUL).
const maxSocketPath = 103

// validCmuxSocket checks a -cmux-socket value: "" (not aimed), or an absolute
// path of at most maxSocketPath bytes — refused at start, never left to a
// cmux call failing later.
func validCmuxSocket(p string) error {
	if p == "" {
		return nil
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("cmux-socket: %q is not an absolute path", p)
	}
	if len(p) > maxSocketPath {
		return fmt.Errorf("cmux-socket: %q is %d bytes, more than %d", p, len(p), maxSocketPath)
	}
	return nil
}

// aimCmuxSocket aims every cmux call of the daemon at socket p (-cmux-socket):
// the sidebar's calls inherit CMUX_SOCKET_PATH from the process env, the wake's
// get it from cmux.SocketPath. Run after stripCmuxEnv; nothing when p is "".
func aimCmuxSocket(p string) {
	if p == "" {
		return
	}
	_ = os.Setenv(cmux.EnvSocketPath, p)
	cmux.SetSocketPath(p)
	log.Printf("cmux: every call aimed at socket %s (-cmux-socket)", p)
}

// stripCmuxEnv removes every CMUX_* variable from pigtaild's environment
// except the bundled CLI path, which the daemon reads itself (cmux.cliPath),
// and CMUX_SOCKET_PATH, which aimCmuxSocket sets back afterwards when
// -cmux-socket aims the daemon (the caller's own value never survives):
// a pigtaild started from a cmux pane would otherwise hand CMUX_SURFACE_ID to
// every cmux call it makes, and cmux reads it before an explicit target. Its
// value is kept as is; absent, it stays absent (empty would mean « no cmux »).
func stripCmuxEnv() {
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CMUX_") && k != cmux.EnvBundledCLI {
			_ = os.Unsetenv(k)
		}
	}
}

// offIfZero maps a flag where 0 means "gate off" onto daemon.Config, where 0
// means "default" and a negative value means "off".
func offIfZero(v int) int {
	if v == 0 {
		return -1
	}
	return v
}

// logFootprintSelfCheck logs, once per start, how this daemon's own memory
// reads through the private footprint reader against ps RSS (decision D7). A
// macOS update that changes proc_pid_rusage would otherwise go unnoticed: the
// reader would keep returning numbers, and every memory figure — eviction,
// admission, `pigtail servers` — would quietly be wrong. Seams for the test.
func logFootprintSelfCheck(line func() (string, bool), logf func(string, ...any)) {
	if l, ok := line(); ok {
		logf("%s", l)
	}
}

// parsePortRange parses "min-max" into a pigtail.PortRange.
func parsePortRange(s string) (pigtail.PortRange, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return pigtail.PortRange{}, fmt.Errorf("want min-max, got %q", s)
	}
	lo, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return pigtail.PortRange{}, fmt.Errorf("min: %w", err)
	}
	hi, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return pigtail.PortRange{}, fmt.Errorf("max: %w", err)
	}
	if lo <= 0 || hi < lo {
		return pigtail.PortRange{}, fmt.Errorf("invalid range %d-%d", lo, hi)
	}
	return pigtail.PortRange{lo, hi}, nil
}

func defaultSocketPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pigtail", "pigtail.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pigtail", "pigtail.sock")
}

// stringList collects a repeatable string flag, in the order given.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// loadDaemonScopeTable reads scopes.yaml from the pigtail config dir — the
// CLI's (cmd/pigtail/scopetable.go) and the cell table's dir, never the
// socket's: --socket must not split the daemon's policy from the CLI's. An
// invalid file: the deny-all table and the error (scope.Load).
func loadDaemonScopeTable() (*scope.Table, error) {
	t, _, err := scope.Load(setup.ConfigDir())
	return t, err
}

// cmuxCheckModeFlag registers --cmux-check-after-relaunch on fs and returns
// its validated value, to read after Parse (TestCmuxCheckModeFlag).
func cmuxCheckModeFlag(fs *flag.FlagSet) func() (string, error) {
	v := fs.String("cmux-check-after-relaunch", cmuxwatch.DefaultMode,
		"after each cmux relaunch, run `pigtail cmux check`: off | report (inventory, sidebar pill) | pilot (default: repair the pilot only) | fix (every session); pilot and fix pass once more 5 min later on the rows left busy")
	return func() (string, error) { return cmuxwatch.ParseMode(*v) }
}

// cmuxCheckCtrlCFlag registers --cmux-check-ctrl-c on fs and returns its
// validated value (D8, T-a), to read after Parse (TestCmuxCheckCtrlCFlag).
func cmuxCheckCtrlCFlag(fs *flag.FlagSet) func() (string, error) {
	v := fs.String("cmux-check-ctrl-c", string(cmuxcheck.QuitRescue),
		"Ctrl+C policy of the post-relaunch check: rescue (default: never on an unreadable screen pigtaild found alone) or unread (after an acceptance)")
	return func() (string, error) {
		q, err := cmuxcheck.ParseQuitPolicy(*v)
		if err != nil {
			return "", fmt.Errorf("--cmux-check-ctrl-c: %q is not rescue or unread", *v)
		}
		return string(q), nil
	}
}

// cmuxFolderSortDisabledFlag registers --cmux-folder-sort on fs
// (sub_x000000000000030, D5) and returns, to read after Parse, the value of
// daemon.Config.CmuxFolderSortDisabled: the flag inverted (TestCmuxFolderSortFlag).
func cmuxFolderSortDisabledFlag(fs *flag.FlagSet) func() bool {
	v := fs.Bool("cmux-folder-sort", true,
		"with --cmux-folders: sort the folders and the sessions inside each one by gravity; =false files each session in its folder without sorting: an order set by hand stays, a session born right under a member of its folder (⌘N with cmux's afterCurrent; a spawn under its caller) stays there, pigtail's own adds go to the bottom of their folder, a new folder goes right before « Inbox » and « Standby » last, then they stay where they are moved")
	return func() bool { return !*v }
}

// loadDaemonSpaceTable reads spaces.yaml where the CLI does (setup.ConfigDir).
// An invalid file does not stop pigtaild, like the scope table: one log line
// and the built-in space table. If even that does not fit the scope table (a
// scopes.yaml without the built-in spaces' scopes), nil: every session goes
// to « Inbox » and no theme takes a space.
func loadDaemonSpaceTable(scopes *scope.Table, logf func(string, ...any)) *space.Table {
	t, err := space.LoadOrBuiltin(setup.ConfigDir(), scopes)
	if err == nil {
		return t
	}
	b, berr := space.Builtin(scopes)
	if berr != nil {
		logf("ERROR space table: %v — and the built-in one does not fit the scope table (%v): no space; fix the file and restart", err, berr)
		return nil
	}
	logf("ERROR space table: %v — using the built-in space table; fix the file and restart", err)
	return b
}

// cmuxInputProbeFlag registers --cmux-input-probe on fs
// (sub_x000000000000024, task 7) and returns, to read after Parse, the value
// of daemon.Config.CmuxInputProbe; an unknown value refuses to start
// (TestCmuxInputProbeFlag).
func cmuxInputProbeFlag(fs *flag.FlagSet) func() (bool, error) {
	v := fs.String("cmux-input-probe", "on",
		"dead-input probe of idle cmux sessions (a terminal status query on the session's own tty, never a keystroke), shown on the pigtail-chk pill: on (default) or off, the safety switch; acts only while the sidebar loop is on (--cmux-sidebar)")
	return func() (bool, error) {
		switch *v {
		case "on":
			return true, nil
		case "off":
			return false, nil
		}
		return false, fmt.Errorf("--cmux-input-probe: %q is not on or off", *v)
	}
}

// diskAlertConfig maps -disk-alert-gb to daemon.Config.DiskAlertGB, whose
// zero value means "no watcher": the flag's 0 and -1 both cut the alert (-1).
func diskAlertConfig(gb int) int {
	if gb <= 0 {
		return -1
	}
	return gb
}
