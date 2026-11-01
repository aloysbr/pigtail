package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget/sessbudgettest"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// budgetFake is a session budget behind the spawn's two seams: the real
// Controller (so the real Decide) in the state of a named fixture. It records
// every call, in order, with the registry lock and the tab listing.
type budgetFake struct {
	t   *testing.T
	ctl *sessbudget.Controller
	clk *sessbudgettest.Clock
	fx  sessbudgettest.Fixture

	mu         sync.Mutex
	events     []string // "ask", "lock", "tabs", "release:<id>", "launched:<id>"
	asks       int
	locks      int
	leaseCalls int // asks + reports
	lastLease  string
}

func installBudgetFake(t *testing.T, fx sessbudgettest.Fixture) *budgetFake {
	t.Helper()
	testutil.SerialOnly(t) // swaps spawnBudgetAsk
	clk := sessbudgettest.NewClock()
	b := &budgetFake{t: t, clk: clk, fx: fx, ctl: sessbudget.NewController(sessbudgettest.Config(), clk.Now, t.Logf)}
	for _, s := range fx.Samples(clk) {
		b.ctl.Observe(s)
	}
	savedAsk, savedReport, savedLock, savedTabs := spawnBudgetAsk, spawnLeaseReport, spawnRegistryLock, spawnListTabs
	t.Cleanup(func() {
		spawnBudgetAsk, spawnLeaseReport, spawnRegistryLock, spawnListTabs = savedAsk, savedReport, savedLock, savedTabs
	})
	spawnBudgetAsk = func(context.Context, *pigtail.Client) (pigtail.SessionLeaseAnswer, error) {
		a := b.ctl.TryAdmit(sessbudget.KindAgent)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.events = append(b.events, "ask")
		b.asks++
		b.leaseCalls++
		b.lastLease = a.LeaseID
		return pigtail.SessionLeaseAnswer{Admitted: a.Admitted, Busy: a.Busy, Budget: a.Budget, Reason: a.Reason, LeaseID: a.LeaseID}, nil
	}
	spawnLeaseReport = func(_ *pigtail.Client, id string, launched bool) {
		b.mu.Lock()
		kind := "release"
		if launched {
			kind = "launched"
		}
		b.events = append(b.events, kind+":"+id)
		b.leaseCalls++
		b.mu.Unlock()
		if launched {
			b.ctl.MarkLaunched(id)
		} else {
			b.ctl.ReleaseLease(id)
		}
	}
	spawnRegistryLock = func(reg *spawnreg.Registry, w io.Writer, d time.Duration) (func(), error) {
		b.mu.Lock()
		b.events = append(b.events, "lock")
		b.locks++
		b.mu.Unlock()
		return savedLock(reg, w, d)
	}
	spawnListTabs = func() ([]spawnreg.Tab, error) {
		b.mu.Lock()
		b.events = append(b.events, "tabs")
		b.mu.Unlock()
		return savedTabs()
	}
	return b
}

func (b *budgetFake) log() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.events, ",")
}

// reports are the release/launched calls, in order.
func (b *budgetFake) reports() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.events {
		if strings.HasPrefix(e, "release:") || strings.HasPrefix(e, "launched:") {
			out = append(out, e)
		}
	}
	return out
}

func (b *budgetFake) busy() int { return b.ctl.Check().Busy }

// afterGap moves the clock past the gap and observes a later sample, so the
// next ask meets row 6 instead of the gap.
func (b *budgetFake) afterGap() {
	b.clk.Advance(sessbudgettest.MinGap)
	b.ctl.Observe(b.fx.AdvanceSample(b.clk))
}

// --- the two pure tables ---

func TestSpawnBudgetNeedTable(t *testing.T) {
	t.Parallel()
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, c := range []struct {
		name  string
		dry   bool
		relay string
		env   func(string) string
		want  spawnNeed
	}{
		{"dry-run", true, "", env(), spawnNeedNone},
		{"dry-run with a relay", true, "WS", env(), spawnNeedNone},
		{"real relay", false, "WS", env(), spawnNeedNone},
		{"gate off", false, "", env("PIGTAIL_SESSION_GATE", "0"), spawnNeedNone},
		{"plain spawn", false, "", env(), spawnNeedAsk},
		{"relay that fell back (RelayFrom empty)", false, "", env("PIGTAIL_SESSION_GATE", "1"), spawnNeedAsk},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := spawnBudgetNeed(c.dry, c.relay, c.env); got != c.want {
				t.Fatalf("need = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSpawnLeaseEndTable(t *testing.T) {
	t.Parallel()
	errX := errors.New("boom")
	for _, c := range []struct {
		name  string
		lease string
		err   error
		r     spawnResult
		want  leaseEnd
	}{
		{"no lease_id", "", nil, spawnResult{TabCreated: true, State: "launched"}, leaseNone},
		{"error, no tab", "L", errX, spawnResult{}, leaseRelease},
		{"launched", "L", nil, spawnResult{TabCreated: true, State: "launched"}, leaseReport},
		{"error with a tab (seen wrong, or a tab that may exist)", "L", errX, spawnResult{TabCreated: true}, leaseReport},
		{"pending, watcher armed", "L", nil, spawnResult{TabCreated: true, State: "pending"}, leaseWatcher},
		{"pending, watcher not armed", "L", nil, spawnResult{TabCreated: true, State: "pending", ArmWarning: "not armed"}, leaseNone},
		{"pending, bind Append failed, watcher armed", "L", errX, spawnResult{TabCreated: true, State: "pending"}, leaseWatcher},
		{"pending, bind Append failed, watcher not armed", "L", errX, spawnResult{TabCreated: true, State: "pending", ArmWarning: "not armed"}, leaseNone},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := spawnLeaseEnd(c.lease, c.err, c.r); got != c.want {
				t.Fatalf("end = %v, want %v", got, c.want)
			}
		})
	}
}

// --- refusal ---

func refusedLine(busy, budget int, reason string) string {
	return "Error: session spawn: refusé · " + reason + " (" + itoa(busy) + "/" + itoa(budget) + " sessions occupées) — réessaie plus tard"
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestSpawnOverBudgetRefusedExit3NothingRegistered(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor.WithBusy(sessbudgettest.Ceiling))
	start := time.Now()
	_, errOut, err := e.spawn(t)
	if exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	if want := refusedLine(sessbudgettest.Ceiling, sessbudgettest.Ceiling, "budget plein"); "Error: "+err.Error() != strings.TrimPrefix(want, "") {
		t.Fatalf("error %q, want %q (stderr %q)", "Error: "+err.Error(), want, errOut)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("refusal took %v", took)
	}
	if es := registryEntries(t); len(es) != 0 {
		t.Fatalf("registry = %+v, want nothing", es)
	}
	if e.f.consumed != 0 || len(e.f.created) != 0 {
		t.Fatalf("stamp consumed %d, tabs created %d", e.f.consumed, len(e.f.created))
	}
	if b.locks != 0 || e.f.tabsCalls != 0 {
		t.Fatalf("lock taken %d times, tabs listed %d times: %s", b.locks, e.f.tabsCalls, b.log())
	}
	if b.log() != "ask" {
		t.Fatalf("calls = %s, want the one ask", b.log())
	}
}

func TestSpawnRefusedJSON(t *testing.T) {
	e := newSpawnEnv(t)
	installBudgetFake(t, sessbudgettest.AboveFloor.WithBusy(sessbudgettest.Ceiling))
	out, _, err := e.spawn(t, "--json")
	if exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	var v map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); jerr != nil {
		t.Fatalf("stdout %q: %v", out, jerr)
	}
	if v["refused"] != true || v["busy"] != float64(sessbudgettest.Ceiling) || v["budget"] != float64(sessbudgettest.Ceiling) || v["reason"] != "budget plein" || len(v) != 4 {
		t.Fatalf("json = %v", v)
	}
}

func TestSpawnRefusedByGapShowsReason(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	e.f.launchesAs(persoCellDir(t), e.cwd)
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("first spawn: %v\n%s", err, errOut)
	}
	b.clk.Advance(15 * time.Second)
	e.f.mu.Lock()
	e.f.procs = e.f.procs[:1] // the first spawn's Claude is gone: the subject may be spawned again
	e.f.mu.Unlock()
	e.f.stamp = "auto"
	_, _, err := e.spawn(t)
	want := refusedLine(sessbudgettest.AboveFloor.Busy+1, sessbudgettest.Ceiling, "démarrage progressif, 25 s")
	if exitCode(err) != 3 || err == nil || "Error: "+err.Error() != want {
		t.Fatalf("second spawn: exit %d err %v, want %q", exitCode(err), err, want)
	}
}

func TestSpawnConcurrentSecondRefusedBeforeLock(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	e.f.launchesAs(persoCellDir(t), e.cwd)
	inLock, release := make(chan struct{}), make(chan struct{})
	counting := spawnRegistryLock
	var first atomic.Bool
	spawnRegistryLock = func(reg *spawnreg.Registry, w io.Writer, d time.Duration) (func(), error) {
		if first.CompareAndSwap(false, true) { // A holds here; a B that wrongly gets here passes through
			close(inLock)
			<-release
		}
		return counting(reg, w, d)
	}
	var errA, errB error
	withStubDaemon(t, e.s, func() {
		run := func(sub string) error {
			cmd := sessionCmd()
			silenceUsageOnRunE(cmd)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"spawn", "--subject", sub, "--prompt", spawnPrompt, "--cwd", e.cwd})
			return cmd.Execute()
		}
		done := make(chan struct{})
		go func() { defer close(done); errA = run("sub_p") }()
		select {
		case <-inLock:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("A never reached the lock")
		}
		errB = run("sub_p")
		close(release)
		<-done
	})
	if exitCode(errB) != 3 {
		t.Fatalf("B: exit %d (%v), want 3 without the lock", exitCode(errB), errB)
	}
	if errA != nil {
		t.Fatalf("A: %v", errA)
	}
	if b.asks != 2 {
		t.Fatalf("asks = %d, want one per spawn", b.asks)
	}
}

func TestSpawnAskAfterPlanBeforeLock(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	e.f.launchesAs(persoCellDir(t), e.cwd)
	planned := ""
	spawnGroupOf = func(string) (string, error) { // the last read of planSpawn
		planned = b.log()
		return e.f.groupRef, nil
	}
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if planned != "" {
		t.Fatalf("budget called during the plan: %s", planned)
	}
	log := strings.Split(b.log(), ",")
	if log[0] != "ask" || log[1] != "lock" {
		t.Fatalf("calls = %v, want ask then lock", log)
	}
	for i, ev := range log {
		if ev == "ask" && i != 0 {
			t.Fatalf("an ask after the lock (inside it or fn): %v", log)
		}
	}
}

func TestSpawnClosedGateRelayFallbackExit3(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	_ = spawnreg.Open(spawnreg.DefaultPath()).Append(spawnreg.Entry{TS: time.Now().Add(-time.Hour), SubjectID: "sub_other", WorkspaceID: "B1", Result: "launched"})
	f.addProc(7500, "/x/bin/claude", "CMUX_WORKSPACE_ID=B1")
	b := installBudgetFake(t, sessbudgettest.RedFull)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--relay")
	if exitCode(err) != 3 || b.asks != 1 {
		t.Fatalf("exit %d (%v), asks %d: a relay that fell back is a plain spawn and asks", exitCode(err), err, b.asks)
	}
	if want := refusedLine(sessbudgettest.Floor, sessbudgettest.Floor, sessbudgettest.RedCause); "Error: "+err.Error() != want {
		t.Fatalf("error %q, want %q", err.Error(), want)
	}
	if es := registryEntries(t); len(es) != 1 || len(f.created) != 0 { // the seeded entry alone
		t.Fatalf("registry %+v, created %d", es, len(f.created))
	}
}

// --- #256 ordering ---

func TestSpawnMarginRefusalAsksNothing(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	saturateJ(t, dirs)
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	cwd := mk(t, filepath.Join(dev, "repo"))
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd)
	if exitCode(err) != 3 || err == nil || strings.Contains(err.Error(), "sessions occupées") {
		t.Fatalf("exit %d (%v), want the margin's exit 3 and line", exitCode(err), err)
	}
	if b.leaseCalls != 0 {
		t.Fatalf("budget called after a margin refusal: %s", b.log())
	}
}

func TestSpawnMarginSwitchThenBudgetRefused(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor.WithBusy(sessbudgettest.Ceiling))
	cwd := mk(t, filepath.Join(dev, "repo"))
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd)
	want := refusedLine(sessbudgettest.Ceiling, sessbudgettest.Ceiling, "budget plein")
	if exitCode(err) != 3 || err == nil || "Error: "+err.Error() != want {
		t.Fatalf("exit %d (%v), want %q", exitCode(err), err, want)
	}
	if m := marginEntries(t); len(m) != 1 || m[0].Result != "switched" {
		t.Fatalf("margin entries = %+v, want the switch kept", m)
	}
	if es := spawnEntries(t); len(es) != 0 {
		t.Fatalf("launch entries = %+v, want none", es)
	}
	if b.log() != "ask" {
		t.Fatalf("calls = %s", b.log())
	}
}

// --- giving the place back ---

func assertOneRelease(t *testing.T, b *budgetFake, before int) {
	t.Helper()
	got := b.reports()
	if len(got) != 1 || got[0] != "release:"+b.lastLease {
		t.Fatalf("reports = %v, want exactly one release of %s (%s)", got, b.lastLease, b.log())
	}
	if b.busy() != before {
		t.Fatalf("busy = %d after the release, want %d", b.busy(), before)
	}
}

func TestSpawnLockFailureReleasesPlace(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	spawnRegistryLock = func(*spawnreg.Registry, io.Writer, time.Duration) (func(), error) {
		return nil, errors.New("lock broken")
	}
	if _, _, err := e.spawn(t); err == nil {
		t.Fatal("spawn succeeded")
	}
	assertOneRelease(t, b, sessbudgettest.AboveFloor.Busy)
}

func TestSpawnPendingErrorReleasesPlace(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	stubPidAlive(t, aliveOwner(true))
	spawnGroupOf = func(string) (string, error) { // between the plan and the reservation
		seedLaunch(t, seedReserve("ln_l", "sub_p", seedOwner, time.Second), seedRef("ln_l", "sub_p", spawnOtherWS, "workspace:9", 0))
		return e.f.groupRef, nil
	}
	_, _, err := e.spawn(t)
	if err == nil || !strings.Contains(err.Error(), "launch in progress") {
		t.Fatalf("err = %v, want launch in progress", err)
	}
	assertOneRelease(t, b, sessbudgettest.AboveFloor.Busy)
}

func TestSpawnCheckFailureReleasesPlace(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, e spawnEnv)
	}{
		{"subject already live", func(t *testing.T, e spawnEnv) {
			spawnGroupOf = func(string) (string, error) {
				seedLaunch(t, seedReserve("ln_A", "sub_p", os.Getpid(), time.Second),
					seedRef("ln_A", "sub_p", spawnOtherWS, "workspace:9", time.Second),
					spawnreg.Entry{TS: time.Now().UTC(), ID: "ln_A", SubjectID: "sub_p", WorkspaceID: spawnOtherWS, WorkspaceRef: "workspace:9", Result: spawnreg.ResultLaunched})
				e.f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
				return e.f.groupRef, nil
			}
		}},
		{"burst", func(t *testing.T, e spawnEnv) {
			spawnGroupOf = func(string) (string, error) {
				appendLaunches(t, spawnBurstMax, time.Second, func(x *spawnreg.Entry) { x.SubjectID = "sub_other" })
				return e.f.groupRef, nil
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			b := installBudgetFake(t, sessbudgettest.AboveFloor)
			c.setup(t, e)
			if _, _, err := e.spawn(t); err == nil {
				t.Fatal("spawn succeeded")
			}
			assertOneRelease(t, b, sessbudgettest.AboveFloor.Busy)
			if b.afterGap(); b.ctl.Check().Reason != "" {
				t.Fatalf("the gap was not restored: %+v", b.ctl.Check())
			}
		})
	}
}

func TestSpawnPreCreateFailureReleasesPlace(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(e spawnEnv)
	}{
		{"consume stamp", func(spawnEnv) { spawnConsumeStamp = func(string) error { return errors.New("record vanished") } }},
		{"claude pids", func(spawnEnv) {
			calls := 0
			claudeProcs = func() ([]claudesession.Proc, error) {
				calls++
				if calls >= 3 { // plan, Check under the lock, then executeSpawn
					return nil, errors.New("ps failed")
				}
				return []claudesession.Proc{{PID: 4242, Args: "/x/bin/claude --settings /t/cmux-claude-settings.a"}}, nil
			}
		}},
		{"createWorktree", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.setup == nil {
				withCells(t, func(_, perso string) string { return perso })
				root := t.TempDir()
				main := gitMain(t, root)
				s, _, _ := worktreeDaemon(t, root, nil)
				installSpawnFake(t)
				b := installBudgetFake(t, sessbudgettest.AboveFloor)
				spawnCreateWorktree = func(context.Context, *pigtail.Client, pigtail.NewWorkspaceRequest) (*pigtail.Workspace, error) {
					return nil, &url.Error{Op: "Post", URL: "http://pigtail/workspaces/new", Err: context.Canceled}
				}
				if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt); err == nil {
					t.Fatal("spawn succeeded")
				}
				assertOneRelease(t, b, sessbudgettest.AboveFloor.Busy)
				return
			}
			e := newSpawnEnv(t)
			b := installBudgetFake(t, sessbudgettest.AboveFloor)
			c.setup(e)
			if _, _, err := e.spawn(t); err == nil {
				t.Fatal("spawn succeeded")
			}
			assertOneRelease(t, b, sessbudgettest.AboveFloor.Busy)
		})
	}
}

func TestSpawnCmuxNothingReleasesPlace(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *spawnFake)
	}{
		{"create fails", func(f *spawnFake) { f.launchErr = errors.New("cmux timeout") }},
		{"cmux unreachable", func(f *spawnFake) {
			f.tabsErr = func(call int) error {
				if call >= 2 { // the first listing is the reservation's before-diff
					return nil
				}
				return errors.New("cmux gone")
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			b := installBudgetFake(t, sessbudgettest.AboveFloor)
			c.setup(e.f)
			if _, _, err := e.spawn(t); err == nil {
				t.Fatal("spawn succeeded")
			}
			assertOneRelease(t, b, sessbudgettest.AboveFloor.Busy)
			for _, r := range b.reports() {
				if strings.HasPrefix(r, "launched:") {
					t.Fatalf("reported launched: %v", b.reports())
				}
			}
		})
	}
}

// --- keeping the place ---

func TestSpawnAdmittedLaunchesAtOnce(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	e.f.launchesAs(persoCellDir(t), e.cwd)
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if len(e.f.created) != 1 || !strings.Contains(e.f.created[0].Command, "PIGTAIL_GATE_PASSED=spawn C_CWD=") {
		t.Fatalf("created %d, command %v", len(e.f.created), e.f.created)
	}
	if got := b.reports(); len(got) != 1 || got[0] != "launched:"+b.lastLease {
		t.Fatalf("reports = %v, want one launched report and no give-back", got)
	}
}

func TestSpawnReportsLaunchedAfterVerify(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	var atTick []string
	busyAtTick := -1
	e.f.launch = func(cmux.WorkspaceSpec) {
		// The tab exists, Claude is not seen yet: the sampler ticks. The lease
		// must still count (nothing reported before verifySpawn returns).
		b.ctl.Observe(b.fx.AdvanceSample(b.clk))
		busyAtTick = b.busy()
		atTick = b.reports()
		time.AfterFunc(40*time.Millisecond, func() {
			e.f.addProc(5000, "/x/bin/claude --settings /t/cmux-claude-settings.b /example-skill reprends",
				"CMUX_WORKSPACE_ID="+spawnNewWS+" CLAUDE_CONFIG_DIR="+persoCellDir(t)+" PWD="+e.cwd)
		})
	}
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if len(atTick) != 0 || busyAtTick != sessbudgettest.AboveFloor.Busy+1 {
		t.Fatalf("at the tick: reports %v, busy %d, want none and %d", atTick, busyAtTick, sessbudgettest.AboveFloor.Busy+1)
	}
	log := b.log()
	if got := b.reports(); len(got) != 1 || got[0] != "launched:"+b.lastLease || !strings.HasSuffix(log, got[0]) {
		t.Fatalf("calls = %s, want a single launched report, last", log)
	}
}

func TestSpawnSeenWrongReportsLaunched(t *testing.T) {
	for _, c := range []struct {
		name   string
		launch func(f *spawnFake, cfg, cwd string)
	}{
		{"wrong account", func(f *spawnFake, _, cwd string) { f.launchesAs("/elsewhere", cwd) }},
		{"wrong directory", func(f *spawnFake, cfg, _ string) { f.launchesAs(cfg, "/Users/x/Developer") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			b := installBudgetFake(t, sessbudgettest.AboveFloor)
			c.launch(e.f, persoCellDir(t), e.cwd)
			if _, _, err := e.spawn(t); err == nil {
				t.Fatal("spawn succeeded")
			}
			if got := b.reports(); len(got) != 1 || got[0] != "launched:"+b.lastLease {
				t.Fatalf("reports = %v, want one launched report and no give-back", got)
			}
		})
	}
}

func TestSpawnAmbiguousTabReportsLaunched(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	e.f.launchErr = nil
	e.f.extraTabs = []spawnreg.Tab{{ID: "44444444-DDDD-4DDD-8DDD-444444444444", Ref: "workspace:43"}}
	spawnNewWorkspace = func(cmux.WorkspaceSpec) (string, error) {
		e.f.mu.Lock()
		e.f.created = append(e.f.created, cmux.WorkspaceSpec{})
		e.f.mu.Unlock()
		return "", errors.New("cmux named no tab") // the diff finds two new tabs
	}
	if _, _, err := e.spawn(t); err == nil {
		t.Fatal("spawn succeeded")
	}
	if got := b.reports(); len(got) != 1 || got[0] != "launched:"+b.lastLease {
		t.Fatalf("reports = %v, want one launched report and no give-back", got)
	}
}

func TestSpawnPendingHandsLeaseToWatcher(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	if _, errOut, err := e.spawn(t); err != nil { // nothing starts in the fake tab: pending
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if len(e.f.armed) != 1 || e.f.armed[0].LeaseID != b.lastLease || b.lastLease == "" {
		t.Fatalf("armed runs = %+v, want the lease %q", e.f.armed, b.lastLease)
	}
	if got := b.reports(); len(got) != 0 {
		t.Fatalf("the CLI reported %v, the watcher's job", got)
	}
}

func TestSpawnWatchNotArmedLeavesLeaseToExpiry(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	e.f.armErr = errors.New("cannot start the watcher")
	if _, _, err := e.spawn(t); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := b.reports(); len(got) != 0 {
		t.Fatalf("reports = %v, want none: the lease runs to PendingLeaseMax", got)
	}
}

func TestSpawnWatcherReportsLease(t *testing.T) {
	type call struct {
		id       string
		launched bool
		entries  int
	}
	var calls []call
	saved := spawnLeaseReport
	t.Cleanup(func() { spawnLeaseReport = saved })
	record := func(_ *pigtail.Client, id string, launched bool) {
		calls = append(calls, call{id, launched, len(registryEntries(t))})
	}

	t.Run("confirmed", func(t *testing.T) {
		calls = nil
		f, reg, run := watchFixture(t, time.Second)
		spawnLeaseReport = record
		run.LeaseID = "lease_1"
		f.claudeIn(spawnNewWS, run.ConfigDir, run.Cwd)
		if err := watchSpawn(nil, reg, run); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0].id != "lease_1" || !calls[0].launched || lastEntry(t).Result != spawnreg.ResultLaunched ||
			calls[0].entries != len(registryEntries(t)) {
			t.Fatalf("calls = %+v, want one launched report after the launched entry", calls)
		}
	})
	t.Run("deadline passed", func(t *testing.T) {
		calls = nil
		_, reg, run := watchFixture(t, spawnreg.ConfirmTimeout-100*time.Millisecond)
		spawnLeaseReport = record
		run.LeaseID = "lease_2"
		if err := watchSpawn(nil, reg, run); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0].id != "lease_2" || !calls[0].launched || lastEntry(t).Result != spawnreg.ResultFailed ||
			calls[0].entries != len(registryEntries(t)) {
			t.Fatalf("calls = %+v, want one launched report after the failed result", calls)
		}
	})
	t.Run("no lease", func(t *testing.T) {
		calls = nil
		f, reg, run := watchFixture(t, time.Second)
		spawnLeaseReport = record
		f.claudeIn(spawnNewWS, run.ConfigDir, run.Cwd)
		if err := watchSpawn(nil, reg, run); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 0 {
			t.Fatalf("calls = %+v, want none without a LeaseID", calls)
		}
	})
	t.Run("a hanging report changes nothing", func(t *testing.T) {
		f, reg, run := watchFixture(t, time.Second)
		spawnLeaseReport = func(*pigtail.Client, string, bool) {} // errors are swallowed inside the real seam
		run.LeaseID = "lease_3"
		f.claudeIn(spawnNewWS, run.ConfigDir, run.Cwd)
		if err := watchSpawn(nil, reg, run); err != nil || lastEntry(t).Result != spawnreg.ResultLaunched {
			t.Fatalf("watch = %v, last %+v", err, lastEntry(t))
		}
	})
}

func TestSpawnRelayFallbackKeepsLease(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := t.TempDir()
	f.launchesAs(persoCellDir(t), cwd)
	_ = spawnreg.Open(spawnreg.DefaultPath()).Append(spawnreg.Entry{TS: time.Now().Add(-time.Hour), SubjectID: "sub_other", WorkspaceID: "B1", Result: "launched"})
	f.addProc(7500, "/x/bin/claude", "CMUX_WORKSPACE_ID=B1")
	b := installBudgetFake(t, sessbudgettest.AboveFloor)
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--relay"); err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if b.asks != 1 || len(f.created) != 1 || !strings.Contains(f.created[0].Command, "PIGTAIL_GATE_PASSED=spawn C_CWD=") {
		t.Fatalf("asks %d, created %+v", b.asks, f.created)
	}
	if got := b.reports(); len(got) != 1 || got[0] != "launched:"+b.lastLease {
		t.Fatalf("reports = %v, want only the launched report", got)
	}
}

// --- exemptions and fail-open (regression guards) ---

func TestSpawnDryRunMakesNoCall(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.RedFull.WithBusy(sessbudgettest.Ceiling))
	if _, errOut, err := e.spawn(t, "--dry-run"); err != nil {
		t.Fatalf("dry-run with the gate closed: %v\n%s", err, errOut)
	}
	if b.leaseCalls != 0 {
		t.Fatalf("budget called: %s", b.log())
	}
}

func TestSpawnClosedGateRealRelayLaunchesNoAsk(t *testing.T) {
	s, f := relayFixture(t)
	b := installBudgetFake(t, sessbudgettest.RedFull)
	cwd := t.TempDir()
	f.stamp = "auto"
	f.launchesAs(persoCellDir(t), cwd)
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--relay"); err != nil {
		t.Fatalf("relay refused: %v\n%s", err, errOut)
	}
	if b.leaseCalls != 0 {
		t.Fatalf("a real relay called the budget: %s", b.log())
	}
	if len(f.created) != 1 || !strings.Contains(f.created[0].Command, "PIGTAIL_GATE_PASSED=relay C_CWD=") {
		t.Fatalf("created %+v, want the relay prefix", f.created)
	}
}

func TestSpawnHonoursSessionGateOff(t *testing.T) {
	e := newSpawnEnv(t)
	b := installBudgetFake(t, sessbudgettest.RedFull.WithBusy(sessbudgettest.Ceiling))
	t.Setenv("PIGTAIL_SESSION_GATE", "0")
	e.f.launchesAs(persoCellDir(t), e.cwd)
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("spawn with the gate off: %v\n%s", err, errOut)
	}
	if b.leaseCalls != 0 || len(e.f.created) != 1 {
		t.Fatalf("calls %s, created %d", b.log(), len(e.f.created))
	}
}

func TestSpawnGateDownFailsOpen(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		e := newSpawnEnv(t)
		reported := 0
		spawnLeaseReport = func(*pigtail.Client, string, bool) { reported++ }
		spawnBudgetAsk = func(context.Context, *pigtail.Client) (pigtail.SessionLeaseAnswer, error) {
			return pigtail.SessionLeaseAnswer{}, errors.New("connection refused")
		}
		e.f.launchesAs(persoCellDir(t), e.cwd)
		if _, errOut, err := e.spawn(t); err != nil || len(e.f.created) != 1 || reported != 0 {
			t.Fatalf("spawn: %v created %d reports %d\n%s", err, len(e.f.created), reported, errOut)
		}
	})
	t.Run("hangs", func(t *testing.T) {
		hang := make(chan struct{})
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemonWith(t, func(r chi.Router, _ *store.Store) {
			r.Post("/session-budget/leases", func(http.ResponseWriter, *http.Request) { <-hang })
		})
		t.Cleanup(func() { close(hang) }) // after the server's own cleanup is registered: runs first
		f := installSpawnFake(t)
		reported := 0
		spawnLeaseReport = func(*pigtail.Client, string, bool) { reported++ }
		spawnBudgetAsk = realSpawnBudgetAsk // the real client, bounded by spawnBudgetTimeout
		cwd := t.TempDir()
		f.launchesAs(persoCellDir(t), cwd)
		start := time.Now()
		_, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
		if err != nil || len(f.created) != 1 || reported != 0 {
			t.Fatalf("spawn: %v created %d reports %d\n%s", err, len(f.created), reported, errOut)
		}
		if took := time.Since(start); took > spawnBudgetTimeout+5*time.Second {
			t.Fatalf("a hanging daemon held the spawn %v", took)
		}
	})
}

func TestSpawnReleaseFailureIgnored(t *testing.T) {
	// A report that fails (the real seam swallows the error) never changes the
	// outcome nor the exit code: here the daemon answers 500 to the release.
	withCells(t, func(_, perso string) string { return perso })
	var releases int
	s, _ := realSubjectDaemonWith(t, func(r chi.Router, _ *store.Store) {
		r.Post("/session-budget/leases/release", func(w http.ResponseWriter, _ *http.Request) {
			releases++
			http.Error(w, "boom", http.StatusInternalServerError)
		})
	})
	f := installSpawnFake(t)
	spawnBudgetAsk = func(context.Context, *pigtail.Client) (pigtail.SessionLeaseAnswer, error) {
		return pigtail.SessionLeaseAnswer{Admitted: true, LeaseID: "lease_x"}, nil
	}
	spawnLeaseReport = realSpawnLeaseReport
	f.launchErr = errors.New("cmux timeout") // a pre-create failure: the lease is given back
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cmux_create_failed") || exitCode(err) == 3 {
		t.Fatalf("err = %v (exit %d), want the launch's own failure", err, exitCode(err))
	}
	if releases != 1 {
		t.Fatalf("release route called %d times", releases)
	}
}

func TestRespawnNeverGated(t *testing.T) {
	t.Parallel()
	// planRespawn and its margin path never reach the budget: no respawn file
	// references the seams.
	for _, name := range []string{"session_respawn.go", "session_respawn_shell.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"spawnBudgetAsk", "spawnLeaseReport", "AdmitSessionNow", "ReleaseSessionLease"} {
			if bytes.Contains(b, []byte(banned)) {
				t.Fatalf("%s calls %s: a respawn is never gated", name, banned)
			}
		}
	}
}

func TestRelaySpawnLineCarriesGatePassedRelay(t *testing.T) {
	s, f := relayFixture(t)
	b := installBudgetFake(t, sessbudgettest.RedFull)
	cwd := t.TempDir()
	f.stamp = "auto"
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--relay", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("relay dry-run: %v", err)
	}
	if !strings.Contains(out, "PIGTAIL_GATE_PASSED=relay C_CWD=") {
		t.Fatalf("the relay line lacks the prefix:\n%s", out)
	}
	if b.leaseCalls != 0 {
		t.Fatalf("budget called: %s", b.log())
	}
}

// The default ask talks to POST /session-budget/leases as kind agent, no_wait.
func TestSpawnBudgetAskDefaultSendsNoWaitAgent(t *testing.T) {
	var got pigtail.SessionLeaseRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/session-budget/leases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeData(w, http.StatusOK, pigtail.SessionLeaseAnswer{Admitted: true, LeaseID: "lease_d"})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	withStubDaemon(t, s, func() {
		c, err := newScopedClient()
		if err != nil {
			t.Fatal(err)
		}
		a, err := realSpawnBudgetAsk(context.Background(), c)
		if err != nil || a.LeaseID != "lease_d" {
			t.Fatalf("answer %+v err %v", a, err)
		}
	})
	if got.Kind != "agent" || !got.NoWait {
		t.Fatalf("request = %+v, want kind agent, no_wait", got)
	}
}
