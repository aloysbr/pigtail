package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cliexec"
	"github.com/aloysbr/pigtail/internal/heavycmd"
	"github.com/aloysbr/pigtail/internal/testrun"
)

// The resolution and rendering tests moved to internal/testrun along with the
// code they cover. What is left here is the wiring this file still owns.

func TestTestCmdHasExplainFlags(t *testing.T) {
	t.Parallel()
	cmd := testCmd()
	for _, name := range []string{"explain", "json"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s is missing: an unarbitrated run must be explainable", name)
		}
	}
}

// The repo's own binary is preferred over npx, which would otherwise offer to
// install one — a prompt in the middle of a test run.
func TestRunnerCommandPrefersTheLocalBinary(t *testing.T) {
	t.Parallel()
	for _, runner := range []testrun.Runner{testrun.RunnerVitest, testrun.RunnerJest} {
		t.Run(string(runner), func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "node_modules", ".bin")
			if err := os.MkdirAll(bin, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, string(runner)), []byte("#!/bin/sh\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, args := runnerCommand(runner, root, []string{"--run"})
			if want := filepath.Join(bin, string(runner)); got != want {
				t.Errorf("runnerCommand = %q, want the repo's own binary %q", got, want)
			}
			if len(args) != 1 || args[0] != "--run" {
				t.Errorf("args = %v, want the caller's args untouched", args)
			}
		})
	}
}

func TestRunnerCommandFallsBackToNpx(t *testing.T) {
	t.Parallel()
	got, args := runnerCommand(testrun.RunnerJest, t.TempDir(), []string{"--run"})
	if got != "npx" {
		t.Errorf("runnerCommand = %q, want npx", got)
	}
	if len(args) != 2 || args[0] != "jest" {
		t.Errorf("args = %v, want jest prepended", args)
	}
}

// An empty runner must still launch something: RunnerAuto reaching this far
// would otherwise exec a binary named "", which fails with a baffling message.
func TestRunnerCommandDefaultsToVitest(t *testing.T) {
	t.Parallel()
	got, args := runnerCommand(testrun.RunnerAuto, t.TempDir(), nil)
	if got != "npx" || len(args) != 1 || args[0] != "vitest" {
		t.Errorf("runnerCommand(auto) = %q %v, want npx vitest", got, args)
	}
}

// --runner is refused on a typo rather than silently falling back to auto:
// quietly running the other runner is the silence this command exists to end.
func TestParseRunner(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]testrun.Runner{
		"": testrun.RunnerAuto, "auto": testrun.RunnerAuto, "AUTO": testrun.RunnerAuto,
		" jest ": testrun.RunnerJest, "vitest": testrun.RunnerVitest,
	} {
		got, err := parseRunner(in)
		if err != nil || got != want {
			t.Errorf("parseRunner(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	if _, err := parseRunner("jest2"); err == nil {
		t.Error("parseRunner accepted an unknown runner")
	}
}

// An unarbitrated plan must not install anything or write a wrapper: the whole
// point is that the suite still runs, untouched.
func TestPrepareRunWritesNothingWhenUnarbitrated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	plan := testrun.Plan{Runner: testrun.RunnerVitest, NodeModules: dir}.
		WithSkip(testrun.SkipNoConfig, "no config", "")

	cfg, cleanup, got := prepareRun(plan)
	cleanup()
	if cfg != "" {
		t.Errorf("config path = %q, want empty", cfg)
	}
	if got.Arbitrated() {
		t.Error("plan must stay unarbitrated")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote %d entries into the repo, want none", len(entries))
	}
}

// A write failure adds a named skip and still lets the suite run, rather than
// aborting: the arbiter must never be the reason tests do not run.
func TestPrepareRunDegradesWhenItCannotWrite(t *testing.T) {
	t.Parallel()
	// A file where the cache directory should go makes MkdirAll fail.
	root := t.TempDir()
	blocked := filepath.Join(root, "node_modules")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := testrun.Plan{
		Runner: testrun.RunnerVitest, Layout: testrun.LayoutSingle, Grain: testrun.GrainFile,
		NodeModules: blocked, BaseConfig: filepath.Join(root, "vitest.config.ts"), Budget: 9, Ceiling: 9,
	}

	cfg, cleanup, got := prepareRun(plan)
	cleanup()
	if cfg != "" {
		t.Errorf("config path = %q, want empty on failure", cfg)
	}
	if got.Arbitrated() {
		t.Fatal("a failed install must leave a skip behind, not a silent success")
	}
	if got.Skips[0].Code != testrun.SkipInstallFailed {
		t.Errorf("skip = %q, want %q", got.Skips[0].Code, testrun.SkipInstallFailed)
	}
	// And the status line must say it out loud.
	if !strings.Contains(testrun.StatusLine(got), "NOT arbitrated") {
		t.Errorf("status line hides the failure: %s", testrun.StatusLine(got))
	}
}

// planFixture points the process at an empty directory with no daemon and
// admission off: runTests then resolves a plan and reaches its runner call.
func planFixture(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv(envAdmit, "0")
	t.Setenv(envAdmitLease, "")
}

func TestPigtailTestWiresGuard(t *testing.T) {
	planFixture(t)
	oldRun := runWith
	t.Cleanup(func() { runWith = oldRun })
	var got []cliexec.Opts
	runWith = func(_ *exec.Cmd, o cliexec.Opts) (int, error) { got = append(got, o); return 0, nil }

	seamAgent(t, true, func() func() bool { return func() bool { return false } })
	if code, err := runTestsCode(testrun.RunnerAuto, nil, nil); err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	o := got[0]
	if !o.Group || o.MaxRun != heavycmd.RunCeiling(heavycmd.ClassVitest) || o.Orphaned == nil || o.OnStop == nil {
		t.Errorf("agent: %+v, want Group, the vitest ceiling, Orphaned and OnStop", o)
	}

	got = nil
	seamAgent(t, false, func() func() bool { return func() bool { return true } })
	if _, err := runTestsCode(testrun.RunnerAuto, nil, nil); err != nil {
		t.Fatal(err)
	}
	if o := got[0]; o.Group || o.Orphaned != nil || o.OnStop != nil || o.MaxRun != 0 {
		t.Errorf("not agent: %+v, want the zero Opts", o)
	}
}

func TestPigtailTestSkipsWhenOrphanedBeforeStart(t *testing.T) {
	c := startAdmissionDaemon(t, 2)
	t.Chdir(t.TempDir())
	t.Setenv(envAdmitLease, "")
	oldRun := runWith
	t.Cleanup(func() { runWith = oldRun })
	runWith = func(*exec.Cmd, cliexec.Opts) (int, error) {
		t.Error("the runner was started")
		return 0, nil
	}
	oldPrep := prepareRunFn
	t.Cleanup(func() { prepareRunFn = oldPrep })
	cleanups := 0
	prepareRunFn = func(p testrun.Plan) (string, func(), testrun.Plan) {
		cfg, raw, plan := oldPrep(p)
		return cfg, func() { cleanups++; raw() }, plan
	}
	seamAgent(t, true, func() func() bool { return func() bool { return true } })
	sink := &journalSink{}
	code, err := runTestsCode(testrun.RunnerAuto, nil, sink.send)
	if err != nil || code != 129 {
		t.Fatalf("code=%d err=%v, want 129", code, err)
	}
	if len(sink.entries) != 1 || sink.entries[0].Kind != "admit-orphan-skipped" {
		t.Fatalf("journal = %+v", sink.entries)
	}
	if cleanups != 1 {
		t.Errorf("the generated config's cleanup ran %d times, want once", cleanups)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(c.Status().Leases) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the lease was not released")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
