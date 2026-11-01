package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cliexec"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/heavycmd"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	// exitTempFail is EX_TEMPFAIL (sysexits.h): "try again later". What
	// `pigtail admit` exits with when its wait budget runs out, so a caller —
	// an agent above all — can tell "the machine is full" from a failure of
	// the command itself.
	exitTempFail = 75

	// envAdmitLease is set in the admitted command's environment. A nested
	// `pigtail admit` or `pigtail test` that sees it runs directly: the
	// outer lease already covers it — unless the outer one is a generic
	// envelope and the inner command a specific class (see admit).
	envAdmitLease = "PIGTAIL_ADMIT_LEASE"
	// envAdmitClass is the class of that lease, set beside it.
	envAdmitClass = "PIGTAIL_ADMIT_CLASS"
	// envAdmit=0 is the client-side kill switch.
	envAdmit = "PIGTAIL_ADMIT"
	// envAdmitVerbose=1 also reports immediate grants, which are otherwise
	// silent so as not to pollute output an agent parses.
	envAdmitVerbose = "PIGTAIL_ADMIT_VERBOSE"

	// admitProgressEvery paces the "still queued" lines on stderr.
	admitProgressEvery = 15 * time.Second
)

// A client queued behind a daemon that restarts reconnects instead of running
// unadmitted: every admitReconnectEvery, for at most admitReconnectWindow from
// the first cut. Variables so tests need not wait for real seconds.
var (
	admitReconnectEvery  = 2 * time.Second
	admitReconnectWindow = 90 * time.Second
)

type admitOptions struct {
	class string
	// workers is how many workers the command will start when its cost
	// scales with that; 0 for everything else. costPrior is what the client
	// computed for that shape.
	workers   int
	costPrior uint64
	label     string
	costMB    int
	// maxWait bounds the queue wait. Zero waits indefinitely.
	maxWait time.Duration
	// maxWaitSet reports that the *user* asked for this budget, which
	// overrides the interactive rule below.
	maxWaitSet bool
	onTimeout  string
	// maxRun replaces the class's run ceiling (agent mode only); maxRunSet
	// reports --max-run was given, and 0 then lifts the ceiling.
	maxRun    time.Duration
	maxRunSet bool
	// journal records the stops (nil: no journal).
	journal journalSender
}

// interactive reports whether a human is watching: stdin and stderr are both
// terminals. An agent's Bash tool gives neither.
//
// A variable so tests can script it.
var interactive = func() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stderr)
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// resolveWait applies the interactive rule: at a terminal, and unless the
// user set --max-wait, the command waits for as long as it takes and says so
// every fifteen seconds. Exiting 75 under a human who is watching would just
// mean re-typing the command; for an agent it is the signal that stops the
// retry loop that filled the machine in the first place.
func resolveWait(opts admitOptions) time.Duration {
	if opts.maxWaitSet || !interactive() {
		return opts.maxWait
	}
	return 0
}

func admitCmd() *cobra.Command {
	opts := admitOptions{}
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "admit [flags] -- command [args…]",
		Annotations: scopedWrite(), // the journal of a stop; see runAdmitted
		Short:       "Run a heavy command once the machine has the memory for it",
		Long: `Run a heavy command — tsc, eslint, jest, vitest, nx, a storybook build —
once the daemon's memory admission controller lets it start.

The command waits in a machine-wide queue while available memory, after
what the commands already running are still expected to take, would drop
under the daemon's floor, while the kernel reports memory pressure, while
the machine is swapping, or while the ceiling of concurrent heavy commands
is reached. Worktrees are served fairly. With nothing else admitted, the
command always starts: under pressure heavy commands are serialised, never
blocked.

It then runs exactly as if invoked directly — same directory, stdio,
signals and exit code. The daemon learns what each command costs from the
peak memory of its process tree.

Exit codes: the command's own; 75 (EX_TEMPFAIL) when --max-wait runs out
first, unless --on-timeout=run. At an interactive terminal the wait is
unlimited by default — exiting 75 under a human who is watching would only
mean retyping the command — and --max-wait restores a budget. Without a
reachable daemon, or with PIGTAIL_ADMIT=0, the command runs unadmitted.

Agent mode (the process has no controlling terminal: an agent's Bash tool,
launchd, cron) adds a lifecycle: the command runs in its own process group,
signals reach the whole group, it is stopped when its session ends or when
it outlives its class's ceiling (--max-run).
Exit 124: stopped at the ceiling. Exit 129: the session was already gone,
the command never started.

A command run under an admitted one is not admitted again, except under a
generic envelope (pigtail admit -- git push): a specific class inside it takes
its own lease, so a pre-push make check still queues one at a time.

The command must come after a literal "--".`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if opts.onTimeout != "fail" && opts.onTimeout != "run" {
				return fmt.Errorf("--on-timeout must be fail or run, not %q", opts.onTimeout)
			}
			opts.maxWaitSet = c.Flags().Changed("max-wait")
			opts.maxRunSet = c.Flags().Changed("max-run")
			if opts.maxRun < 0 {
				return fmt.Errorf("--max-run must not be negative")
			}
			sc, scerr := newScopedClient()
			opts.journal = journalFrom(sc, scerr, crossScope)
			os.Exit(runAdmitted(args, opts))
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.class, "class", "", "command class (tsc, eslint, jest, vitest, vitest-browser, nx, storybook-build, build, go-test, generic); inferred from the command when empty")
	cmd.Flags().StringVar(&opts.label, "label", "", "label shown in pigtail slots (default: the command line)")
	cmd.Flags().IntVar(&opts.costMB, "cost-mb", 0, "expected peak memory in MiB, overriding what the daemon learned")
	cmd.Flags().DurationVar(&opts.maxWait, "max-wait", 10*time.Minute,
		"give up waiting after this long (ignored at an interactive terminal, where the wait is unlimited unless this flag is given; 0 = wait forever)")
	cmd.Flags().DurationVar(&opts.maxRun, "max-run", 0,
		"agent mode: stop the command's tree after this long instead of its class's ceiling (0 = no ceiling)")
	addCrossScopeFlag(cmd, &crossScope)
	cmd.Flags().StringVar(&opts.onTimeout, "on-timeout", "fail", "when --max-wait runs out: fail (exit 75) or run anyway")
	return cmd
}

// runAdmitted admits argv, runs it, and returns the exit code to leave with.
func runAdmitted(argv []string, opts admitOptions) int {
	cwd, _ := os.Getwd()
	if opts.class == "" {
		c, _ := heavycmd.Classify(argv, cwd)
		opts.class = string(c)
	}
	if opts.class == "" {
		opts.class = string(heavycmd.ClassGeneric) // exported below: a nested admit reads it
	}
	if opts.label == "" {
		opts.label = truncateLabel(strings.Join(argv, " "))
	}
	// Read the mode and arm the orphan probe first: before the queue (a
	// session that dies while waiting is caught) and before any signal.Notify.
	agent, nested, watch := guardMode()
	lease, abort := admit(os.Stderr, cwd, opts)
	if abort {
		return exitTempFail
	}
	if watch != nil && watch() {
		lease.Release()
		return skipOrphaned(os.Stderr, "admit", opts.journal, opts.class, opts.label, cwd)
	}

	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is the caller's own command
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), childAdmitEnv(lease, opts.class)...)
	if s := socketPath(); s != "" {
		cmd.Env = append(cmd.Env, "PIGTAIL_SOCKET="+s)
	}
	cmd.Env = append(cmd.Env, testLockEnv(lease, opts.class, os.Environ())...)
	guard := childGuard(agent, nested, opts.class, opts.maxRun, opts.maxRunSet, watch)
	if guard.Group {
		guard.OnStop = newOnStop(os.Stderr, "admit", opts.journal, opts.class, opts.label, cwd, guard.MaxRun)
	}
	code, err := cliexec.RunWith(cmd, guard)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigtail admit: %s: %v\n", argv[0], err)
		code = startFailureExit(err)
	}
	lease.Finish(code) // reports the exit to the history, then releases
	return code
}

// startFailureExit is the shell's exit code for a command that could not
// start: 127 when it was not found, 126 otherwise (not executable, …).
func startFailureExit(err error) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return 127
	}
	return 126
}

// admit takes a lease for one heavy command, or decides to run without one.
// It returns the lease (nil to run unadmitted) and abort=true when the
// caller must exit with exitTempFail instead of running at all.
//
// Every failure other than the wait budget running out fails open: the
// admission controller must never be the reason a command does not run.
func admit(stderr io.Writer, cwd string, opts admitOptions) (*pigtail.AdmissionLease, bool) {
	if os.Getenv(envAdmit) == "0" {
		return nil, false
	}
	class := opts.class
	if class == "" {
		class = string(heavycmd.ClassGeneric)
	}
	// Nested: the outer lease covers this command, unless it is only a
	// generic envelope (`pigtail admit -- git push`) around a specific class
	// (the pre-push hook's go-test): that one still queues for its own class,
	// naming the envelope so the daemon does not make it wait on its parent.
	parent, covered := nestedAdmission(class)
	if covered {
		return nil, false
	}
	group, repo := admissionGroup(cwd)
	req := pigtail.AdmissionRequest{
		Group: group, Class: class, Label: opts.label, Repo: repo, PID: os.Getpid(), ParentLease: parent,
		// Who asks, for the daemon's focus priority: the cwd is the caller's
		// (the plan's for pigtail test), not this process's.
		SessionID: os.Getenv("CLAUDE_CODE_SESSION_ID"), CmuxWorkspaceID: os.Getenv("CMUX_WORKSPACE_ID"), Cwd: cwd,
	}
	if opts.costMB > 0 {
		req.CostHintBytes = uint64(opts.costMB) << 20 //nolint:gosec // G115: positive, checked just above
	}
	if opts.workers > 0 {
		// The worker count keys what the daemon learns, and sizes the prior
		// it starts from: a run whose cost is one browser per worker cannot
		// be described by a class-wide constant.
		req.Workers = opts.workers
		req.CostPriorBytes = opts.costPrior
	}

	wait := resolveWait(opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if wait > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), wait)
		defer cancel()
	}
	var last pigtail.AdmissionUpdate
	var lastPrint time.Time
	onQueued := func(u pigtail.AdmissionUpdate) {
		last = u
		if lastPrint.IsZero() || time.Since(lastPrint) >= admitProgressEvery {
			fmt.Fprintln(stderr, "pigtail admit: "+describeQueued(class, u))
			lastPrint = time.Now()
		}
	}
	lease, err := newClient().AcquireAdmission(ctx, req, onQueued)
	// A stream that breaks while queued (at least one "queued" line was read)
	// is a daemon that went away, most likely restarting: the leases it had
	// granted come back with it (leases_persist.go; queued waiters are not
	// persisted, they come back through this very reconnect), so ask again
	// rather than run unadmitted. Only transport-level errors are retried; an
	// answer from the daemon (an APIError) ends the reconnect at once.
	// The window starts at the first cut and never restarts. A reconnection is
	// a new Enqueue: the client goes to the back of the line. Known gap: a
	// daemon dying before it wrote its first line leaves last.State empty, so
	// that stays "running unadmitted", as before.
	var cutAt time.Time
	for err != nil && last.State != "" && !errors.Is(err, context.DeadlineExceeded) && !isAdmissionDisabled(err) && isTransportErr(err) {
		if cutAt.IsZero() {
			cutAt = time.Now()
			fmt.Fprintf(stderr, "pigtail admit: daemon gone while queued — reconnecting (up to %ds)\n", int(admitReconnectWindow.Seconds()))
		}
		if time.Since(cutAt) >= admitReconnectWindow {
			fmt.Fprintf(stderr, "pigtail admit: daemon still gone after %ds — running unadmitted\n", int(admitReconnectWindow.Seconds()))
			return nil, false
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(admitReconnectEvery):
			lease, err = newClient().AcquireAdmission(ctx, req, onQueued)
		}
	}
	switch {
	case err == nil:
		if !lastPrint.IsZero() {
			fmt.Fprintf(stderr, "pigtail admit: admitted after %s\n", (time.Duration(lease.Grant.WaitedMS) * time.Millisecond).Round(time.Second))
		} else if os.Getenv(envAdmitVerbose) == "1" {
			fmt.Fprintf(stderr, "pigtail admit: admitted at once (%s, ~%s)\n", class, fmtBytes(lease.Grant.CostBytes))
		}
		return lease, false
	case errors.Is(err, context.DeadlineExceeded):
		if opts.onTimeout == "run" {
			fmt.Fprintf(stderr, "pigtail admit: still queued after %s — running anyway (--on-timeout=run)\n", wait)
			return nil, false
		}
		fmt.Fprintf(stderr, "pigtail admit: gave up after %s in the queue — %s (%s).\n"+
			"  Exit 75 means \"try again later\", not that the command failed: re-run it later, in the background,\n"+
			"  or on fewer files — never in a tight retry loop.\n",
			wait, giveUpCause(class, last.Reason), describeQueued(class, last))
		return nil, true
	}
	if isAdmissionDisabled(err) {
		return nil, false // switched off on purpose: nothing to report
	}
	fmt.Fprintf(stderr, "pigtail admit: admission unavailable (%v) — running unadmitted\n", err)
	return nil, false
}

// isTransportErr reports whether err is the daemon being unreachable or the
// stream breaking (connection refused or reset, a missing socket, EOF, a
// network error, no first line in time) — the only failures a reconnect can
// cure. Any answer the daemon gave (an *APIError, an HTTP error) is not one.
func isTransportErr(err error) bool {
	var apiErr *pigtail.APIError
	if err == nil || errors.As(err, &apiErr) {
		return false
	}
	var netErr net.Error
	return errors.Is(err, pigtail.ErrAdmissionUnresponsive) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENOENT) ||
		errors.As(err, &netErr)
}

// isAdmissionDisabled reports whether the daemon answered that admission is
// switched off.
func isAdmissionDisabled(err error) bool {
	var apiErr *pigtail.APIError
	return errors.As(err, &apiErr) && apiErr.Code == pigtail.ErrAdmissionDisabled
}

// describeQueued renders one queued update for stderr.
func describeQueued(class string, u pigtail.AdmissionUpdate) string {
	if u.State == "" {
		return "queued, no position reported yet"
	}
	s := fmt.Sprintf("queued — %s (~%s), position %d/%d, %d heavy command(s) running",
		class, fmtBytes(u.NeedBytes), u.Position, u.Queued, u.Running)
	switch u.Reason {
	case "memory":
		s += fmt.Sprintf(", memory: %s free after reservations < %s floor + %s needed",
			fmtBytes(u.EffectiveBytes), fmtBytes(u.FloorBytes), fmtBytes(u.NeedBytes))
	case "pressure":
		s += ", the kernel reports memory pressure"
	case "swapout":
		s += ", the machine is swapping"
	case "compress":
		s += ", waiting on compressor traffic"
	case "compress_calm":
		s += ", waiting for sustained compressor calm (heavy class: a long wait does not lift it)"
	case "disk":
		s += fmt.Sprintf(", disk: %s free < %s floor (nothing starts until space is freed)",
			fmtGB(u.DiskFreeBytes), fmtGB(u.DiskFloorBytes))
	case "max_jobs":
		s += ", at the concurrent heavy-command ceiling"
	case "class_limit":
		s += fmt.Sprintf(", waiting for a %s turn", class)
	case "":
		s += ", waiting behind the head of the queue"
	}
	return s
}

// giveUpCause names, for the give-up message, what held the queue at the
// last update: a class cap or a full ceiling is not a memory shortage.
func giveUpCause(class, reason string) string {
	switch reason {
	case "memory", "pressure", "swapout", "compress":
		return "the machine is short of memory"
	case "disk":
		return "the disk stayed under its free-space floor"
	case "class_limit":
		return fmt.Sprintf("another %s command held the queue", class)
	case "max_jobs":
		return "the heavy-command ceiling stayed full"
	default:
		return "the queue ahead did not move"
	}
}

// admissionGroup returns the fairness group (the worktree) and the repo key
// learned costs are filed under (the project, shared by its worktrees).
//
// Outside a pigtail project it falls back to the nearest directory holding a
// .git entry — read from the filesystem, never by running git (see the
// architecture checklist in CLAUDE.md).
func admissionGroup(cwd string) (group, repo string) {
	if ctx, err := devserver.ResolveContext(cwd); err == nil {
		return ctx.WorktreeName, ctx.ContainerDir
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Base(dir), dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd, cwd
		}
		dir = parent
	}
}

// nestedAdmission reads the envelope an inherited lease puts around this
// command: covered=true when that lease already covers it (the command runs
// without a lease of its own), else the lease to name as parent ("" if none).
// PIGTAIL_ADMIT=0 is not an envelope.
func nestedAdmission(class string) (parent string, covered bool) {
	outer := os.Getenv(envAdmitLease)
	if outer == "" || os.Getenv(envAdmit) == "0" {
		return "", false
	}
	if outer == "none" || os.Getenv(envAdmitClass) != string(heavycmd.ClassGeneric) || class == string(heavycmd.ClassGeneric) {
		return "", true
	}
	return outer, false
}

// childAdmitEnv is the admission environment of the command admit just ran.
// A nested skip adds nothing: the variables it inherited still name the
// envelope's lease and class, and rewriting them to "none" would make a
// grandchild read "covered" as "not admitted" and run without a cap.
func childAdmitEnv(lease *pigtail.AdmissionLease, class string) []string {
	if _, covered := nestedAdmission(class); covered && lease == nil {
		return nil
	}
	return admittedEnv(lease, class)
}

// admittedEnv is what an admitted command inherits: its lease (or "none")
// and the class it was admitted as, which a nested `pigtail admit` reads.
func admittedEnv(lease *pigtail.AdmissionLease, class string) []string {
	return []string{envAdmitLease + "=" + leaseID(lease), envAdmitClass + "=" + class}
}

func leaseID(l *pigtail.AdmissionLease) string {
	if l == nil {
		return "none" // ran unadmitted: nested commands inherit that decision
	}
	return l.Grant.LeaseID
}

func truncateLabel(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// fmtGB renders a disk size as whole GB (1 GB = 2^30 bytes, like the
// -admit-disk-floor-gb flag and the disk alert).
func fmtGB(b uint64) string { return fmt.Sprintf("%d GB", b>>30) }

func fmtBytes(b uint64) string {
	const gib = 1 << 30
	if b >= gib {
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	}
	return fmt.Sprintf("%d MiB", b>>20)
}
