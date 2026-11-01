package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	nodeclient "github.com/aloysbr/pigtail/clients/node"
	"github.com/aloysbr/pigtail/internal/cliexec"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/heavycmd"
	"github.com/aloysbr/pigtail/internal/testrun"
)

// runWith is cliexec.RunWith, a seam for the tests of the wiring.
var runWith = cliexec.RunWith

func testCmd() *cobra.Command {
	var explain, asJSON, crossScope bool
	var runner string

	cmd := &cobra.Command{
		Use:         "test [-- vitest args…]",
		Annotations: scopedWrite(), // the journal of a stop; see runTestsCode
		Short:       "Run vitest under pigtail's machine-wide CPU slot budget",
		Long: `Run vitest with two things added, and nothing written into this repo:

  · a setup file that takes one CPU slot per test file from the daemon, so
    concurrent runs in other worktrees share the machine instead of each
    claiming it whole;
  · a ceiling on the workers, so the run never pays the RAM of workers that
    could not run anyway. The ceiling is the lower of the daemon's budget and
    whatever this repo pinned itself — pigtail only ever lowers one.

The run as a whole also takes one lease from the daemon's memory admission
controller (see "pigtail admit"): it waits while the machine lacks the
memory for one more heavy command, and exits 75 if that lasts longer than
ten minutes. PIGTAIL_ADMIT=0 skips that wait.

Every run prints one line to stderr saying whether it was arbitrated, and
why not when it was not. "pigtail test --explain" prints the whole
resolution and runs nothing; "--explain --json" prints it for an agent.

Everything is generated under node_modules/.cache/pigtail: the generated
config is removed after the run, the small Node client next to it is left in
place (it is rewritten from scratch on every run). With no daemon reachable,
this is a plain vitest run.

Runs from wherever it's invoked, exactly like vitest itself: the config is
found by walking up from the current directory (never past the worktree
root), and vitest's own working directory is left untouched — so a
per-project config in a subdirectory of a monorepo wins over one further up,
the same as "cd there && npx vitest" would.

In agent mode (no controlling terminal) the run also holds its tree: its own
process group, stopped with its session or at its class's ceiling, exactly as
"pigtail admit" does (see its help).

Any flag meant for vitest itself (not pigtail) must come after a literal
"--", e.g. "pigtail test -- --coverage" — without it, pigtail's own flag
parser rejects it as an unknown flag.`,
		// Deliberately NOT DisableFlagParsing: that would also stop pflag
		// from parsing pigtail's own global flags (--socket, --http,
		// --token) whenever they appear before "test" on the command line —
		// cobra hands such a command the *entire* raw arg list rather than
		// just the tail, so e.g. `pigtail --socket X test -- --run` would
		// silently leave flagSocket empty and forward "--socket X" into
		// vitest's argv instead. pflag already treats everything after a
		// bare "--" as positional without any help, which is exactly the
		// vitest-passthrough behaviour this command wants.
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			want, err := parseRunner(runner)
			if err != nil {
				return err
			}
			if explain {
				return explainRun(want, asJSON)
			}
			sc, scerr := newScopedClient()
			return runTests(want, args, journalFrom(sc, scerr, crossScope))
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	cmd.Flags().BoolVar(&explain, "explain", false, "print how this run would be arbitrated, and run nothing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "with --explain, print the resolution as JSON")
	cmd.Flags().StringVar(&runner, "runner", "auto", "which runner to arbitrate: auto, vitest or jest")
	return cmd
}

// parseRunner reads --runner. "auto" lets the repo decide, and an unknown
// value is refused rather than silently treated as auto: a typo that quietly
// ran the other runner would be the kind of silence this command exists to end.
func parseRunner(name string) (testrun.Runner, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto":
		return testrun.RunnerAuto, nil
	case "vitest":
		return testrun.RunnerVitest, nil
	case "jest":
		return testrun.RunnerJest, nil
	default:
		return "", fmt.Errorf("unknown --runner %q: use auto, vitest or jest", name)
	}
}

// resolvePlan answers how the run at cwd would be arbitrated. Kept separate
// from both callers so --explain and a real run resolve through exactly the
// same code — an --explain that could disagree with the run it describes would
// be worse than no --explain at all.
func resolvePlan(runner testrun.Runner) (testrun.Plan, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return testrun.Plan{}, "", fmt.Errorf("cwd: %w", err)
	}
	root := cwd
	if ctx, cerr := devserver.ResolveContext(cwd); cerr == nil {
		root = ctx.WorktreeDir
	}
	req := testrun.Request{Cwd: cwd, Root: root, Budget: arbiterBudget(), Runner: runner}
	return testrun.Resolve(req), root, nil
}

// explainRun prints the resolution and writes nothing: no client installed, no
// wrapper generated, nothing touched inside the repo. That is what makes it
// safe to run against a client repo just to see what pigtail thinks of it.
func explainRun(runner testrun.Runner, asJSON bool) error {
	plan, _, err := resolvePlan(runner)
	if err != nil {
		return err
	}
	if asJSON {
		return testrun.ExplainJSON(os.Stdout, plan)
	}
	testrun.Explain(os.Stdout, plan)
	return nil
}

// runTests runs the plan and leaves with the runner's own exit code. The
// work is in runTestsCode, whose defers (the generated config, the lease) have
// run by the time the process exits.
func runTests(runner testrun.Runner, args []string, send journalSender) error {
	code, err := runTestsCode(runner, args, send)
	if err != nil {
		return err
	}
	if code != 0 {
		// The runner's exit code is the answer the caller wants; surfacing it
		// as a cobra error would turn a red suite into "pigtail failed".
		os.Exit(code)
	}
	return nil
}

func runTestsCode(runner testrun.Runner, args []string, send journalSender) (int, error) {
	// The mode is read, and the orphan probe armed, before anything waits and
	// before cliexec's signal.Notify.
	agent, nested, watch := guardMode()
	plan, root, err := resolvePlan(runner)
	if err != nil {
		return 0, err
	}
	cwd := plan.Cwd

	// A test run is one heavy command to the memory admission controller —
	// on top of, not instead of, the CPU slots its workers take per file:
	// the two arbiters answer different questions (docs/specs/
	// 2026-09-22-mem-admission-design.md §8). Skipped when an outer
	// `pigtail admit` already holds a lease for this run.
	opts := admitOptions{
		class: string(admissionClass(plan)), label: truncateLabel("pigtail test " + strings.Join(args, " ")),
		maxWait: 10 * time.Minute, onTimeout: "fail",
	}
	if plan.Class == string(heavycmd.ClassVitestBrowser) {
		opts.workers = plan.Workers
		opts.costPrior = heavycmd.BrowserRunCost(plan.Workers)
	}
	lease, abort := admit(os.Stderr, cwd, opts)
	if abort {
		return exitTempFail, nil
	}
	defer lease.Release()

	// The slot group is per worktree AND per runner: a repo's jest units and
	// its vitest Storybook suite are two runs sharing one machine, and max-min
	// fairness only works if the arbiter can tell them apart.
	group := string(plan.Runner)
	if ctx, cerr := devserver.ResolveContext(cwd); cerr == nil {
		group = ctx.WorktreeName + ":" + string(plan.Runner)
	}

	// cleanup is wrapped in sync.OnceFunc so it is safe to both defer (the
	// panic and "returned before Start" safety net) and call explicitly
	// right before os.Exit (which skips deferred calls, and is how vitest's
	// own exit code is preserved below) — whichever fires first runs the
	// real removal, the other is a no-op rather than a second os.Remove.
	cfgPath, rawCleanup, plan := prepareRunFn(plan)
	cleanup := sync.OnceFunc(rawCleanup)
	defer cleanup()

	// One line, always, arbitrated or not: a run that quietly went
	// unarbitrated is indistinguishable from one that was, and that silence
	// is the defect this command was failing at.
	fmt.Fprintln(os.Stderr, testrun.StatusLine(plan))

	runArgs := args
	if cfgPath != "" {
		runArgs = append([]string{"--config", cfgPath}, args...)
	}

	// cmd.Dir is the cwd this command was invoked from, not the worktree
	// root: pigtail test must behave exactly like vitest invoked at the same
	// place, adding only arbitration. Vitest/Vite default their own `root`
	// (what relative config paths like setupFiles resolve against) to
	// process.cwd() unless the config sets it explicitly — running from the
	// worktree root unconditionally would silently run a different `root`
	// than "cd here && npx vitest" would, in exactly the monorepo case this
	// command exists for.
	bin, binArgs := runnerCommand(plan.Runner, root, runArgs)
	cmd := exec.Command(bin, binArgs...) //nolint:gosec // args come from the user's own shell
	cmd.Dir = cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "PIGTAIL_CPU_SLOT_GROUP="+group)
	cmd.Env = append(cmd.Env, childAdmitEnv(lease, opts.class)...)
	if s := socketPath(); s != "" {
		cmd.Env = append(cmd.Env, "PIGTAIL_SOCKET="+s)
	}
	cmd.Env = append(cmd.Env, testLockEnv(lease, opts.class, os.Environ())...)

	// A session already gone before the runner starts: nothing to run for.
	if watch != nil && watch() {
		cleanup()
		lease.Release()
		return skipOrphaned(os.Stderr, "test", send, opts.class, opts.label, cwd), nil
	}

	guard := childGuard(agent, nested, opts.class, 0, false, watch)
	if guard.Group {
		guard.OnStop = newOnStop(os.Stderr, "test", send, opts.class, opts.label, cwd, guard.MaxRun)
	}
	// cliexec relays SIGINT/SIGTERM/SIGHUP to vitest and keeps this process
	// alive until it exits, so the generated config is cleaned up even on
	// Ctrl+C — Go's default disposition would kill pigtail mid-syscall,
	// before any defer ran, and leave it behind in node_modules/.cache.
	code, err := runWith(cmd, guard)
	if err != nil {
		lease.Finish(startFailureExit(err)) // the deferred Release is then a no-op
		return 0, fmt.Errorf("%s: %w", plan.Runner, err)
	}
	cleanup()
	lease.Finish(code)
	return code, nil
}

// prepareRunFn is a seam: tests count the cleanup calls.
var prepareRunFn = prepareRun

// prepareRun installs the Node client and writes the wrapper for an arbitrated
// plan, returning the config path, a cleanup, and the plan as it ended up —
// installing or rendering can still fail, and each failure adds its own named
// skip rather than aborting: the arbiter must never be the reason tests do not
// run.
func prepareRun(plan testrun.Plan) (string, func(), testrun.Plan) {
	noop := func() {}
	if !plan.Arbitrated() {
		return "", noop, plan
	}
	dir := filepath.Join(plan.NodeModules, ".cache", "pigtail")
	setup, err := nodeclient.Install(dir)
	if err != nil {
		return "", noop, plan.WithSkip(testrun.SkipInstallFailed,
			fmt.Sprintf("could not install the CPU slot client: %v", err),
			"check write access to "+dir)
	}
	// A workspace layout needs one wrapper per project plus a root one, so
	// the cleanup removes a list rather than a single file. Everything else
	// about the run is identical.
	if plan.Layout == testrun.LayoutWorkspace {
		cfgPath, written, werr := testrun.RenderWorkspaceConfig(dir, plan.BaseConfig, plan.Projects, setup.Vitest, plan.Budget)
		cleanup := func() {
			for _, p := range written {
				_ = os.Remove(p)
			}
		}
		if werr != nil {
			// Whatever was written before the failure still goes: a half
			// generated workspace left behind would be picked up by the next
			// run as if pigtail had meant it.
			cleanup()
			return "", noop, plan.WithSkip(testrun.SkipRenderFailed,
				fmt.Sprintf("could not write the generated workspace config: %v", werr),
				"check write access to "+dir)
		}
		return cfgPath, cleanup, plan
	}

	render := testrun.RenderVitestConfig
	setupFile := setup.Vitest
	if plan.Runner == testrun.RunnerJest {
		render, setupFile = testrun.RenderJestConfig, setup.Jest
	}
	if plan.Grain == testrun.GrainRun {
		// No per-file slot: this run is arbitrated as a whole by the memory
		// admission's class cap. The wrapper is still written — it carries
		// the ceiling and the write-back, both of which still apply.
		setupFile = ""
	}
	cfgPath, err := render(dir, plan.BaseConfig, setupFile, plan.Budget)
	if err != nil {
		return "", noop, plan.WithSkip(testrun.SkipRenderFailed,
			fmt.Sprintf("could not write the generated config: %v", err),
			"check write access to "+dir)
	}
	return cfgPath, func() { _ = os.Remove(cfgPath) }, plan
}

// arbiterBudget asks the daemon for the global budget. Any failure returns 0,
// which Resolve reads as "run unarbitrated", with a named skip.
func arbiterBudget() int {
	cx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := newClient().CPUSlots(cx)
	if err != nil {
		return 0
	}
	return resp.Budget
}

// runnerCommand prefers the repo's own binary over npx, which would otherwise
// offer to install one.
func runnerCommand(runner testrun.Runner, root string, args []string) (string, []string) {
	name := string(runner)
	if name == "" {
		name = string(testrun.RunnerVitest)
	}
	local := filepath.Join(root, "node_modules", ".bin", name)
	if _, err := os.Stat(local); err == nil {
		return local, args
	}
	return "npx", append([]string{name}, args...)
}

// admissionClass maps a plan onto the memory admission controller's class,
// so a jest run reserves what jest actually costs. Measured on
// big-monorepo the day this landed: one jest run peaks at 15.9 GiB,
// five times the class prior — which is exactly why the learned EWMA exists.
//
// The plan may already carry a class the runner alone does not imply: a
// browser-mode vitest run is charged as its own class, resolved from what the
// last run of that config recorded (see applyResolvedFacts).
func admissionClass(plan testrun.Plan) heavycmd.Class {
	if plan.Class != "" {
		return heavycmd.Class(plan.Class)
	}
	if plan.Runner == testrun.RunnerJest {
		return heavycmd.ClassJest
	}
	return heavycmd.ClassVitest
}
