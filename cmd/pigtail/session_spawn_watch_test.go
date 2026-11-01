package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/relaunchfile"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- the hidden --watch flag, through the real cobra command ---

func stubSpawnWatcher(t *testing.T) *[]string {
	t.Helper()
	testutil.SerialOnly(t) // swaps runSpawnWatcher
	saved := runSpawnWatcher
	t.Cleanup(func() { runSpawnWatcher = saved })
	var got []string
	runSpawnWatcher = func(_ *pigtail.Client, runPath string) error { got = append(got, runPath); return nil }
	return &got
}

func TestSpawnWatchRunsThroughRealCobraCommand(t *testing.T) {
	got := stubSpawnWatcher(t)
	cmd := sessionSpawnCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--watch", "/state/ln_x.json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("session spawn --watch = %v", err)
	}
	if len(*got) != 1 || (*got)[0] != "/state/ln_x.json" {
		t.Fatalf("watcher called with %v", *got)
	}
}

func TestSpawnWatchArgvExecutesViaRoot(t *testing.T) {
	got := stubSpawnWatcher(t)
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(spawnWatchArgv("/x/pigtail", "/state/ln_y.json"))
	if err := root.Execute(); err != nil {
		t.Fatalf("root %v = %v", spawnWatchArgv("/x/pigtail", "/state/ln_y.json"), err)
	}
	if len(*got) != 1 || (*got)[0] != "/state/ln_y.json" {
		t.Fatalf("watcher called with %v", *got)
	}
}

func TestSpawnWatchRefusesOtherFlags(t *testing.T) {
	got := stubSpawnWatcher(t)
	cmd := sessionSpawnCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--watch", "x", "--prompt", "y"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--watch runs alone") {
		t.Fatalf("err = %v", err)
	}
	if len(*got) != 0 {
		t.Fatal("the watcher ran")
	}
}

// Guard: without --watch, --prompt stays required.
func TestSpawnPromptStillRequired(t *testing.T) {
	t.Parallel()
	cmd := sessionSpawnCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--subject", "sub_p"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `required flag(s) "prompt" not set`) {
		t.Fatalf("err = %v", err)
	}
}

// --- the watcher itself ---

// watchFixture: the spawn fakes (process table), a seeded reserve and
// launch-ref, and the run the CLI would arm.
func watchFixture(t *testing.T, refAge time.Duration) (*spawnFake, *spawnreg.Registry, spawnWatchRun) {
	t.Helper()
	withCells(t, func(_, perso string) string { return perso })
	f := installSpawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	ref := seedRef("ln_w", "sub_p", spawnNewWS, "workspace:42", refAge)
	seedLaunch(t, seedReserve("ln_w", "sub_p", seedOwner, refAge+time.Minute), ref)
	run := spawnWatchRun{ID: "ln_w", SubjectID: "sub_p", WorkspaceID: spawnNewWS, WorkspaceRef: "workspace:42", Cell: "p",
		ConfigDir: persoCellDir(t), Cwd: "/w/p", Prompt: spawnPrompt, Mode: "auto", Before: []int{4242},
		Deadline: spawnWatchDeadline(ref.TS)}
	return f, reg, run
}

func (f *spawnFake) claudeIn(ws, configDir, cwd string) {
	f.addProc(5000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+ws+" CLAUDE_CONFIG_DIR="+configDir+" PWD="+cwd)
}

func lastEntry(t *testing.T) spawnreg.Entry {
	t.Helper()
	es := registryEntries(t)
	return es[len(es)-1]
}

func TestSpawnWatcherConfirmsLaunched(t *testing.T) {
	f, reg, run := watchFixture(t, time.Second)
	polls := 0
	claudeProcs = func() ([]claudesession.Proc, error) {
		polls++
		if polls == 2 {
			f.claudeIn(spawnNewWS, run.ConfigDir, run.Cwd)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]claudesession.Proc(nil), f.procs...), nil
	}
	if err := watchSpawn(nil, reg, run); err != nil {
		t.Fatal(err)
	}
	e := lastEntry(t)
	if e.Kind != "" || e.Result != spawnreg.ResultLaunched || e.ID != "ln_w" || e.PID != 5000 ||
		e.WorkspaceID != spawnNewWS || e.WorkspaceRef != "workspace:42" || e.SubjectID != "sub_p" {
		t.Fatalf("launched entry = %+v", e)
	}
	es := registryEntries(t)
	if es[len(es)-2].Kind != spawnreg.KindLaunchArmed || es[len(es)-2].ID != "ln_w" {
		t.Fatalf("no armed record before the result: %+v", es)
	}
}

func TestSpawnWatcherDeadlineFromLaunchRef(t *testing.T) {
	ref := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	if got := spawnWatchDeadline(ref); !got.Equal(ref.Add(spawnreg.ConfirmTimeout)) {
		t.Fatalf("deadline = %s, want the launch-ref + %s", got, spawnreg.ConfirmTimeout)
	}
	// A reserve 12 min old (slow worktree), a fresh launch-ref: arming does not close it failed.
	withCells(t, func(_, perso string) string { return perso })
	f := installSpawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	r := seedRef("ln_w", "sub_p", spawnNewWS, "workspace:42", 0)
	seedLaunch(t, seedReserve("ln_w", "sub_p", seedOwner, 12*time.Minute), r)
	run := spawnWatchRun{ID: "ln_w", SubjectID: "sub_p", WorkspaceID: spawnNewWS, WorkspaceRef: "workspace:42", Cell: "p",
		ConfigDir: persoCellDir(t), Cwd: "/w/p", Deadline: spawnWatchDeadline(r.TS)}
	f.claudeIn(spawnNewWS, run.ConfigDir, run.Cwd)
	if err := watchSpawn(nil, reg, run); err != nil {
		t.Fatal(err)
	}
	if e := lastEntry(t); e.Result != spawnreg.ResultLaunched {
		t.Fatalf("last = %+v, want launched", e)
	}
}

func TestSpawnWatcherFailsAfterDeadline(t *testing.T) {
	_, reg, run := watchFixture(t, spawnreg.ConfirmTimeout-100*time.Millisecond)
	if err := watchSpawn(nil, reg, run); err != nil {
		t.Fatal(err)
	}
	e := lastEntry(t)
	if e.Kind != spawnreg.KindLaunchResult || e.Result != spawnreg.ResultFailed || e.ID != "ln_w" || !strings.Contains(e.Detail, "no Claude process appeared") {
		t.Fatalf("last = %+v, want failed « no Claude process appeared »", e)
	}
	es := registryEntries(t)
	if _, ok := spawnreg.Pending(es, "sub_p", time.Now(), spawnreg.PendingOpts{Alive: aliveOwner(true)}); ok {
		t.Fatal("still pending after the watcher's failure: a new spawn would be refused")
	}
	p := spawnPlan{Subject: pigtail.Subject{ID: "sub_p"}, CallerWS: spawnCallerWS}
	if err := spawnGuards(es, map[string][]int{}, nil, p, time.Now().UTC()); err != nil {
		t.Fatalf("a new plan after the failure = %v, want accepted", err)
	}
}

func TestSpawnWatcherFailedKeepsIdentity(t *testing.T) {
	e := newSpawnEnv(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	ref := seedRef("ln_w", "sub_p", spawnNewWS, "workspace:42", 6*time.Minute)
	seedLaunch(t, seedReserve("ln_w", "sub_p", seedOwner, 7*time.Minute), ref)
	run := spawnWatchRun{ID: "ln_w", SubjectID: "sub_p", WorkspaceID: spawnNewWS, WorkspaceRef: "workspace:42", Cell: "p",
		ConfigDir: persoCellDir(t), Cwd: e.cwd, Deadline: spawnWatchDeadline(ref.TS)} // passed: failed at once
	if err := watchSpawn(nil, reg, run); err != nil {
		t.Fatal(err)
	}
	es := registryEntries(t)
	for _, x := range es[2:] { // armed, then failed
		if x.SubjectID != "sub_p" || x.WorkspaceID != spawnNewWS || x.WorkspaceRef != "workspace:42" || x.ID != "ln_w" {
			t.Fatalf("watcher record without the identity: %+v", x)
		}
	}
	if last := es[len(es)-1]; last.Kind != spawnreg.KindLaunchResult || last.Result != spawnreg.ResultFailed {
		t.Fatalf("last = %+v, want failed", last)
	}
	now := time.Now().UTC()
	if n := spawnreg.Launches(es, now.Add(-spawnBurstWindow), now, spawnreg.PendingOpts{Alive: aliveOwner(false)}); len(n) != 1 {
		t.Fatalf("burst counts %d launches, want the failed one with a tab counted once", len(n))
	}
	e.f.claudeIn(spawnNewWS, persoCellDir(t), e.cwd) // a late Claude in the tab
	if _, _, err := e.spawn(t); err == nil || !strings.Contains(err.Error(), "already has a live session") {
		t.Fatalf("second spawn = %v, want the live-session refusal", err)
	}
}

func TestSpawnWatcherLaunchedEntryFields(t *testing.T) {
	for _, relay := range []string{spawnCallerWS, ""} {
		t.Run("relay="+relay, func(t *testing.T) {
			f, reg, run := watchFixture(t, time.Second)
			run.RelayFrom, run.Mode = relay, "plan"
			f.claudeIn(spawnNewWS, run.ConfigDir, run.Cwd)
			if err := watchSpawn(nil, reg, run); err != nil {
				t.Fatal(err)
			}
			e := lastEntry(t)
			if e.RelayFrom != relay || e.Prompt != spawnPrompt || e.Mode != "plan" || e.Cell != "p" || e.Cwd != "/w/p" {
				t.Fatalf("launched entry = %+v", e)
			}
		})
	}
}

func TestSpawnWatchRunCwdFromCreatedWorktree(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	root := t.TempDir()
	main := gitMain(t, root)
	actual := mk(t, filepath.Join(root, "feat-perso-one-2"))
	s, _, _ := worktreeDaemon(t, root, nil)
	f := installSpawnFake(t) // nothing starts within the verify: pending
	spawnCreateWorktree = func(ctx context.Context, c *pigtail.Client, req pigtail.NewWorkspaceRequest) (*pigtail.Workspace, error) {
		ws, err := c.New(ctx, req)
		if err != nil {
			return nil, err
		}
		ws.Path = actual // the daemon's path, not the predicted one
		return ws, nil
	}
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt); err != nil {
		t.Fatalf("spawn = %v\n%s", err, errOut)
	}
	if len(f.armed) != 1 || f.armed[0].Cwd != actual {
		t.Fatalf("armed = %+v, want Cwd %s", f.armed, actual)
	}
	run := f.armed[0]
	run.Deadline = time.Now().Add(2 * time.Second)
	f.claudeIn(spawnNewWS, run.ConfigDir, actual)
	if err := watchSpawn(nil, spawnreg.Open(spawnreg.DefaultPath()), run); err != nil {
		t.Fatal(err)
	}
	if e := lastEntry(t); e.Result != spawnreg.ResultLaunched {
		t.Fatalf("watcher = %+v, want launched in the created worktree", e)
	}
}

// --- arming: the detached handshake ---

// TestHelperSpawnWatch is the watcher armSpawnWatch starts in the arm tests:
// "spawnwatch-arm" reads the run back, echoes it and appends the armed
// record; "spawnwatch-exit" exits at once.
func TestHelperSpawnWatch(t *testing.T) {
	t.Parallel()
	mode := os.Getenv("PIGTAIL_HELPER")
	if mode != "spawnwatch-arm" && mode != "spawnwatch-exit" {
		return
	}
	if mode == "spawnwatch-exit" {
		os.Exit(0)
	}
	runPath := flag.Arg(0)
	run, err := readSpawnWatchRun(runPath)
	if err != nil {
		os.Exit(4)
	}
	b, _ := json.Marshal(run)
	_ = relaunchfile.WriteAtomic(runPath+".echo", b)
	_ = spawnreg.Open(spawnreg.DefaultPath()).Append(spawnreg.Entry{Kind: spawnreg.KindLaunchArmed, ID: run.ID, SubjectID: run.SubjectID,
		WorkspaceID: run.WorkspaceID, WorkspaceRef: run.WorkspaceRef, Result: spawnreg.ResultArmed, WorkerPID: os.Getpid()})
	os.Exit(0)
}

func helperSpawnWatch(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("PIGTAIL_HELPER", mode)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	saved := spawnWatchArgv
	t.Cleanup(func() { spawnWatchArgv = saved })
	spawnWatchArgv = func(_, runPath string) []string { return []string{"-test.run=^TestHelperSpawnWatch$", runPath} }
}

func TestArmSpawnWatchHandshake(t *testing.T) {
	helperSpawnWatch(t, "spawnwatch-arm")
	run := spawnWatchRun{ID: "ln_hs", SubjectID: "sub_p", WorkspaceID: spawnNewWS, WorkspaceRef: "workspace:42", Cell: "p",
		ConfigDir: "/cfg/p", Cwd: "/w/é", Prompt: "/example-skill é", Mode: "auto", Before: []int{1, 2}, Deadline: time.Now().Add(time.Minute).UTC().Round(0)}
	reg := spawnreg.Open(spawnreg.DefaultPath())
	pid, err := armSpawnWatch(reg, run, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runPath := spawnWatchRunPath(run.ID)
	if fi, err := os.Stat(runPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("run file %v mode %v, want 0600", err, fi)
	}
	var echo spawnWatchRun
	if b, err := os.ReadFile(runPath + ".echo"); err != nil || json.Unmarshal(b, &echo) != nil {
		t.Fatalf("echo: %v", err)
	}
	if !reflect.DeepEqual(echo, run) {
		t.Fatalf("run round trip:\n got %+v\nwant %+v", echo, run)
	}
	es, _ := reg.Entries()
	if len(es) != 1 || es[0].Kind != spawnreg.KindLaunchArmed || es[0].ID != run.ID || es[0].WorkerPID != pid {
		t.Fatalf("registry = %+v (pid %d)", es, pid)
	}
}

func TestArmSpawnWatchFailsWhenWorkerExits(t *testing.T) {
	helperSpawnWatch(t, "spawnwatch-exit")
	run := spawnWatchRun{ID: "ln_ex", SubjectID: "sub_p", WorkspaceID: spawnNewWS, Deadline: time.Now().Add(time.Minute)}
	if _, err := armSpawnWatch(spawnreg.Open(spawnreg.DefaultPath()), run, io.Discard); err == nil || !strings.Contains(err.Error(), "exited before arming") {
		t.Fatalf("err = %v", err)
	}
}

// An inherited root flag (--socket) is not « another flag »: --watch still runs.
func TestSpawnWatchAllowsInheritedFlags(t *testing.T) {
	got := stubSpawnWatcher(t)
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--socket", "/tmp/x.sock", "session", "spawn", "--watch", "/state/ln_z.json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--socket with --watch = %v", err)
	}
	if len(*got) != 1 || (*got)[0] != "/state/ln_z.json" {
		t.Fatalf("watcher called with %v", *got)
	}
}
