package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	spawnCallerWS = "11111111-AAAA-4AAA-8AAA-111111111111"
	spawnNewWS    = "22222222-BBBB-4BBB-8BBB-222222222222"
	spawnOtherWS  = "33333333-CCCC-4CCC-8CCC-333333333333"
)

// spawnFake replaces every outside call spawn makes.
type spawnFake struct {
	procs         []claudesession.Proc
	envs          map[int]string
	created       []cmux.WorkspaceSpec
	trustMarked   []string // "<workspace id>|<pid>" asked of the daemon (D14)
	trustErr      error    // the daemon refuses the trust-pending pill
	groupRef      string
	caller        claudesession.Proc
	cellMode      string
	mainCk        bool
	launch        func(spec cmux.WorkspaceSpec) // what appears once the workspace exists
	screen        string
	stamp         string              // current mode the caller's hook recorded; "" = no record
	badEnv        map[int]bool        // pids whose environment cannot be read
	consumed      int                 // real spawns that consumed the record
	preTrust      claudeconfig.Result // what the pre-trust seam answers
	preTrustCalls []string            // "<config dir>|<cwd>" asked of the pre-trust seam
	order         []string            // "pretrust", "launch": the order of the two outside calls
	launchErr     error               // cmux new-workspace fails
	renamed       []string            // "<workspace id>|<title>" asked of cmux (caller rename)
	renameErr     error               // cmux refuses the rename
	boundErr      error               // spawnCallerBound fails
	// cmux tabs (spawnListTabs): the caller's, plus workspace:42 once created.
	tabsErr   func(call int) error // the listing fails at this call (1-based)
	tabsCalls int
	extraTabs []spawnreg.Tab  // tabs that appear with the create, besides workspace:42
	mu        sync.Mutex      // guards the fake's shared state (concurrent runSpawn)
	armed     []spawnWatchRun // runs handed to the stubbed spawnArmWatch
	armErr    error           // the stubbed spawnArmWatch fails
}

func installSpawnFake(t *testing.T) *spawnFake {
	t.Helper()
	f := &spawnFake{envs: map[int]string{}, badEnv: map[int]bool{}, groupRef: "workspace_group:3", cellMode: "auto", stamp: "auto",
		caller: claudesession.Proc{PID: 4242, Args: "/x/bin/claude --settings /t/cmux-claude-settings.a"}}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", t.TempDir()) // never read the real maintenance state
	t.Setenv(cmux.EnvWorkspaceID, spawnCallerWS)
	stubCmuxRefs(t, map[string]string{"workspace:42": spawnNewWS})
	// No test reaches a real session budget: the ask fails open (daemon
	// down), a report goes nowhere. Budget tests install their own fake.
	savedAsk, savedReport := spawnBudgetAsk, spawnLeaseReport
	t.Cleanup(func() { spawnBudgetAsk, spawnLeaseReport = savedAsk, savedReport })
	spawnBudgetAsk = func(context.Context, *pigtail.Client) (pigtail.SessionLeaseAnswer, error) {
		return pigtail.SessionLeaseAnswer{}, errors.New("no session budget in this test")
	}
	spawnLeaseReport = func(*pigtail.Client, string, bool) {}
	savedWatch := []any{spawnArmWatch, spawnConfirmTimeout, spawnBaseBudget, spawnWatchArgv}
	t.Cleanup(func() {
		spawnArmWatch = savedWatch[0].(func(*spawnreg.Registry, spawnWatchRun, io.Writer) (int, error))
		spawnConfirmTimeout = savedWatch[1].(time.Duration)
		spawnBaseBudget = savedWatch[2].(time.Duration)
		spawnWatchArgv = savedWatch[3].(func(string, string) []string)
	})
	// Never the real detached watcher: os.Executable() is the test binary.
	spawnArmWatch = func(_ *spawnreg.Registry, run spawnWatchRun, _ io.Writer) (int, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.armed = append(f.armed, run)
		return 999, f.armErr
	}
	spawnWatchArgv = func(_, runPath string) []string {
		t.Errorf("the real watcher argv was built for %s: a test reached armSpawnWatch", runPath)
		return []string{"-test.run=^$"}
	}
	saved := []any{claudeProcs, processEnviron, spawnCallerWSRef, spawnGroupOf, spawnCallerClaude,
		spawnCellDefaultMode, spawnMainCheckout, spawnNewWorkspace, spawnMarkTrust, spawnReadScreen,
		spawnSurfacesIn, spawnVerifyTimeout, spawnVerifyInterval, spawnCallerSession, spawnPermissionStamp, spawnConsumeStamp, spawnTrustWindow,
		spawnCallerBound, spawnRenameWorkspace, spawnCreateWorktree, spawnCreatePhase, spawnLockNotice,
		spawnMainCheckoutRoot, spawnLinkedWorktrees, spawnListTabs, spawnRegistryLock}
	t.Cleanup(func() {
		claudeProcs = saved[0].(func() ([]claudesession.Proc, error))
		processEnviron = saved[1].(func(int) (string, error))
		spawnCallerWSRef = saved[2].(func() (string, error))
		spawnGroupOf = saved[3].(func(string) (string, error))
		spawnCallerClaude = saved[4].(func() (claudesession.Proc, bool, error))
		spawnCellDefaultMode = saved[5].(func(string) (string, error))
		spawnMainCheckout = saved[6].(func(string) (bool, error))
		spawnNewWorkspace = saved[7].(func(cmux.WorkspaceSpec) (string, error))
		spawnMarkTrust = saved[8].(func(context.Context, *pigtail.Client, string, int) error)
		spawnReadScreen = saved[9].(func(string) (string, error))
		spawnSurfacesIn = saved[10].(func(string) ([]string, error))
		spawnVerifyTimeout = saved[11].(time.Duration)
		spawnVerifyInterval = saved[12].(time.Duration)
		spawnCallerSession = saved[13].(func(string, int) (string, bool))
		spawnPermissionStamp = saved[14].(func(string) (string, error))
		spawnConsumeStamp = saved[15].(func(string) error)
		spawnTrustWindow = saved[16].(time.Duration)
		spawnCallerBound = saved[17].(func(context.Context, *pigtail.Client, string) (bool, error))
		spawnRenameWorkspace = saved[18].(func(string, string) error)
		spawnCreateWorktree = saved[19].(func(context.Context, *pigtail.Client, pigtail.NewWorkspaceRequest) (*pigtail.Workspace, error))
		spawnCreatePhase = saved[20].(func(context.Context, *pigtail.Client, string, pigtail.SubjectPhase) error)
		spawnLockNotice = saved[21].(time.Duration)
		spawnMainCheckoutRoot = saved[22].(func(string) (string, bool, bool, error))
		spawnLinkedWorktrees = saved[23].(func(string) ([]string, error))
		spawnListTabs = saved[24].(func() ([]spawnreg.Tab, error))
		spawnRegistryLock = saved[25].(func(*spawnreg.Registry, io.Writer, time.Duration) (func(), error))
	})
	claudeProcs = func() ([]claudesession.Proc, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]claudesession.Proc(nil), f.procs...), nil
	}
	processEnviron = func(pid int) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.badEnv[pid] {
			return "", errors.New("ps: operation not permitted")
		}
		return f.envs[pid], nil
	}
	spawnCallerSession = func(string, int) (string, bool) { return "sess-caller", true }
	spawnConsumeStamp = func(string) error { f.mu.Lock(); defer f.mu.Unlock(); f.consumed++; f.stamp = ""; return nil }
	spawnPermissionStamp = func(string) (string, error) {
		if f.stamp == "" {
			return "", errors.New("no record of the session's current permission mode — install the permission-stamp hook")
		}
		return f.stamp, nil
	}
	spawnCallerWSRef = func() (string, error) { return "workspace:7", nil }
	spawnGroupOf = func(string) (string, error) { return f.groupRef, nil }
	spawnCallerClaude = func() (claudesession.Proc, bool, error) { return f.caller, f.caller.PID != 0, nil }
	spawnCellDefaultMode = func(string) (string, error) { return f.cellMode, nil }
	spawnMainCheckout = func(string) (bool, error) { return f.mainCk, nil }
	spawnNewWorkspace = func(s cmux.WorkspaceSpec) (string, error) {
		f.mu.Lock()
		f.order = append(f.order, "launch")
		if f.launchErr != nil {
			f.mu.Unlock()
			return "", f.launchErr
		}
		f.created = append(f.created, s)
		launch := f.launch
		f.mu.Unlock()
		if launch != nil {
			launch(s)
		}
		return "workspace:42", nil
	}
	spawnListTabs = func() ([]spawnreg.Tab, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tabsCalls++
		if f.tabsErr != nil {
			if err := f.tabsErr(f.tabsCalls); err != nil {
				return nil, err
			}
		}
		tabs := []spawnreg.Tab{{ID: spawnCallerWS, Ref: "workspace:7"}}
		if len(f.created) > 0 {
			tabs = append(tabs, spawnreg.Tab{ID: spawnNewWS, Ref: "workspace:42"})
			tabs = append(tabs, f.extraTabs...)
		}
		return tabs, nil
	}
	spawnMarkTrust = func(_ context.Context, _ *pigtail.Client, ws string, pid int) error {
		f.trustMarked = append(f.trustMarked, ws+"|"+strconv.Itoa(pid))
		return f.trustErr
	}
	realBound := spawnCallerBound // the real lookup, against the test daemon
	spawnCallerBound = func(ctx context.Context, c *pigtail.Client, ws string) (bool, error) {
		if f.boundErr != nil {
			return false, f.boundErr
		}
		return realBound(ctx, c, ws)
	}
	spawnRenameWorkspace = func(ws, title string) error {
		f.renamed = append(f.renamed, ws+"|"+title)
		return f.renameErr
	}
	spawnReadScreen = func(string) (string, error) { return f.screen, nil }
	spawnSurfacesIn = func(string) ([]string, error) { return []string{"surface:99"}, nil }
	spawnVerifyTimeout, spawnVerifyInterval = 300*time.Millisecond, 10*time.Millisecond
	spawnTrustWindow = 50 * time.Millisecond
	savedPreTrust := spawnPreTrust
	t.Cleanup(func() { spawnPreTrust = savedPreTrust })
	spawnPreTrust = func(_ context.Context, _ *pigtail.Client, dir, cwd string) claudeconfig.Result {
		f.preTrustCalls = append(f.preTrustCalls, dir+"|"+cwd)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.order = append(f.order, "pretrust")
		return f.preTrust
	}
	savedWSAt := spawnWorkspaceAt
	t.Cleanup(func() { spawnWorkspaceAt = savedWSAt })
	spawnWorkspaceAt = func(context.Context, *pigtail.Client, string) (*pigtail.Workspace, error) { return nil, nil }
	// The caller itself is a live Claude in its own workspace, on the cell of this test's env.
	f.addProc(4242, f.caller.Args, "CMUX_WORKSPACE_ID="+spawnCallerWS+" CLAUDE_CONFIG_DIR="+os.Getenv("CLAUDE_CONFIG_DIR"))
	return f
}

func (f *spawnFake) addProc(pid int, args, env string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs = append(f.procs, claudesession.Proc{PID: pid, Args: args})
	f.envs[pid] = env
}

// launchesAs makes the created workspace start a Claude with this environment.
func (f *spawnFake) launchesAs(configDir, cwd string) {
	f.launch = func(cmux.WorkspaceSpec) {
		f.addProc(5000, "/x/bin/claude --settings /t/cmux-claude-settings.b /example-skill reprends",
			"CMUX_WORKSPACE_ID="+spawnNewWS+" CLAUDE_CONFIG_DIR="+configDir+" PWD="+cwd)
	}
}

func runSpawnCLI(t *testing.T, s *stubDaemon, args ...string) (string, string, error) {
	t.Helper()
	var o, e bytes.Buffer
	var err error
	withStubDaemon(t, s, func() {
		cmd := sessionCmd()
		silenceUsageOnRunE(cmd) // as newRootCmd does: a refusal prints no usage
		cmd.SetOut(&o)
		cmd.SetErr(&e)
		cmd.SetArgs(append([]string{"spawn"}, args...))
		err = cmd.Execute()
	})
	return o.String(), e.String(), err
}

func persoCellDir(t *testing.T) string { t.Helper(); return os.Getenv("CLAUDE_CONFIG_DIR") }

const spawnPrompt = "/example-skill resume sub_p"

func TestSpawnRefusals(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name    string
		setup   func(t *testing.T, f *spawnFake)
		args    []string
		wantErr string
	}{
		{"not in cmux", func(t *testing.T, _ *spawnFake) { t.Setenv(cmux.EnvWorkspaceID, "") },
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "not in a cmux pane"},
		{"no cell", func(t *testing.T, f *spawnFake) {
			d := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", d)
			f.envs[4242] = "CMUX_WORKSPACE_ID=" + spawnCallerWS + " CLAUDE_CONFIG_DIR=" + d
		}, []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "no Claude cell"},
		{"env differs from the caller's", func(t *testing.T, _ *spawnFake) {
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "work"))
		}, []string{"--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", cwd}, "the cell is the session's"},
		{"no Claude ancestor", func(_ *testing.T, f *spawnFake) { f.caller = claudesession.Proc{} },
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "no Claude session above"},
		{"caller env unreadable", func(_ *testing.T, f *spawnFake) { f.badEnv[4242] = true },
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "cannot read the environment"},
		{"no permission record", func(_ *testing.T, f *spawnFake) { f.stamp = "" },
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "current permission mode unknown"},
		{"currently bypassed", func(_ *testing.T, f *spawnFake) { f.stamp = "bypassPermissions" },
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "bypassPermissions now"},
		{"unreadable process env", func(_ *testing.T, f *spawnFake) {
			f.addProc(8000, "/x/bin/claude", "")
			f.badEnv[8000] = true
		}, []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "another live session on the subject cannot be ruled out"},
		{"other scope", nil,
			[]string{"--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", cwd}, "--cross-scope"},
		{"no worktree and no cwd flag", nil, // no comma: subtest names end up in the cell table path (YAML flow map)
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt}, "pigtail new --subject sub_p"},
		{"cwd missing", nil,
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", filepath.Join(cwd, "nope")}, "not a directory"},
		{"cwd with a space", func(t *testing.T, _ *spawnFake) { _ = os.Mkdir(filepath.Join(cwd, "a b"), 0o700) },
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", filepath.Join(cwd, "a b")}, "whitespace"},
		{"shared main checkout", func(_ *testing.T, f *spawnFake) { f.mainCk = true },
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "main checkout"},
		{"caller bypass", func(_ *testing.T, f *spawnFake) {
			f.caller.Args = "/x/bin/claude --dangerously-skip-permissions"
		}, []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "bypassPermissions"},
		{"cell default bypass", func(_ *testing.T, f *spawnFake) { f.cellMode = "bypassPermissions" },
			[]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, "bypassPermissions (cell default)"},
		{"multi-line prompt", nil,
			[]string{"--subject", "sub_p", "--prompt", "a\nb", "--cwd", cwd}, "control character"},
		{"empty prompt", nil,
			[]string{"--subject", "sub_p", "--prompt", " ", "--cwd", cwd}, "--prompt is empty"},
		{"prompt too long", nil,
			[]string{"--subject", "sub_p", "--prompt", strings.Repeat("x", 501), "--cwd", cwd}, "longer than 500"},
		{"prompt starting with a dash", nil,
			[]string{"--subject", "sub_p", "--prompt", "-p x", "--cwd", cwd}, "starts with '-'"},
	}
	// R15/R16 name --new-worktree (Task 6): a second expected substring, by case
	// name, so the positional case literals above stay as they are.
	wantAlso := map[string]string{
		"no worktree and no cwd flag": "--new-worktree", // R15, keeps "pigtail new --subject sub_p"
		"shared main checkout":        "--new-worktree", // R16, keeps "main checkout"
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			s, _ := realSubjectDaemon(t)
			f := installSpawnFake(t)
			if c.setup != nil {
				c.setup(t, f)
			}
			_, _, err := runSpawnCLI(t, s, c.args...)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			if w := wantAlso[c.name]; w != "" && !strings.Contains(err.Error(), w) {
				t.Fatalf("err = %v, want also %q", err, w)
			}
			if !strings.HasPrefix(err.Error(), "session spawn: ") {
				t.Fatalf("error not prefixed: %v", err)
			}
			if strings.Contains(err.Error(), "caps") {
				t.Fatalf("refusal still names the caps: %v", err)
			}
			if !strings.Contains(err.Error(), " — ") {
				t.Fatalf("refusal names no next step (\" — <what to do>\"): %v", err)
			}
			if len(f.created) != 0 {
				t.Fatalf("a refusal created a workspace: %+v", f.created)
			}
			if len(f.renamed) != 0 {
				t.Fatalf("a refusal renamed the caller: %q", f.renamed)
			}
		})
	}
}

func TestSpawnCrossScopeAccepted(t *testing.T) {
	fourCells(t, "ha") // master perso caller; wa = same account m in the subject's scope, dir on disk
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--cross-scope", "--dry-run")
	if err != nil || !strings.Contains(out, "sub_t") || !strings.Contains(out, "cell      wa (") {
		t.Fatalf("cross-scope dry-run = %q %v", out, err)
	}
}

func TestSpawnWorkCellCannotSeePerso(t *testing.T) {
	withCells(t, func(work, _ string) string { return work })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("work → perso = %v, want not found", err)
	}
}

func TestSpawnLiveCaps(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)

	// The caller's own workspace is bound to the subject and live: not "another" session.
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnCallerWS, SubjectID: "sub_p", Source: "subject-work", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run"); err != nil {
		t.Fatalf("caller's own live workspace blocked the spawn: %v", err)
	}

	// Another bound workspace with a live Claude: refused.
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnOtherWS, SubjectID: "sub_p", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err == nil || !strings.Contains(err.Error(), "already has a live session") {
		t.Fatalf("second live session = %v", err)
	}
	if exitCode(err) != 1 {
		t.Fatalf("live session on the subject exits %d, want 1", exitCode(err))
	}
}

func TestSpawnDryRunShowsLoweredModeAndUnreadable(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.stamp = "plan" // shift-tab to plan after an auto launch
	f.addProc(8000, "/x/bin/claude", "")
	f.badEnv[8000] = true
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode      plan (launch auto, cell default; current plan)", "--permission-mode", "⚠ 1 Claude process(es) have an unreadable environment (pid [8000])"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run lacks %q:\n%s", want, out)
		}
	}
}

func TestSpawnDryRunCreatesNothing(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nothing created", "sub_p", "cell      p", cwd, "mode      auto (launch auto, cell default; current auto)", "prompt    " + spawnPrompt, "workspace_group:3 (right under the caller workspace:7)", "new-workspace", "C_CWD=", "--permission-mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run lacks %q:\n%s", want, out)
		}
	}
	if len(f.created) != 0 || len(f.trustMarked) != 0 {
		t.Fatal("dry-run created or marked a trust pill")
	}
	if es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries(); len(es) != 0 {
		t.Fatal("dry-run wrote the registry")
	}
}

func TestSpawnLaunches(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)

	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %d workspaces, want 1", len(f.created))
	}
	spec := f.created[0]
	if spec.GroupRef != "workspace_group:3" || spec.CallerRef != "workspace:7" || spec.Cwd != cwd || !slices.Contains(cmux.NewWorkspaceArgs(spec), "afterCurrent") {
		t.Fatalf("placement = %+v", spec)
	}
	if spec.Env[0] != "CLAUDE_CONFIG_DIR="+persoCellDir(t) {
		t.Fatalf("env = %v", spec.Env)
	}
	if !strings.Contains(spec.Command, "c p ") || !strings.Contains(spec.Command, "C_CWD=") {
		t.Fatalf("command = %s", spec.Command)
	}
	if !strings.Contains(out, "✓ fresh session on sub_p") || !strings.Contains(out, "workspace:42") || !strings.Contains(out, "pid 5000") {
		t.Fatalf("stdout = %q", out)
	}
	if b, err := st.GetCmuxBinding(spawnNewWS); err != nil || b.SubjectID != "sub_p" || b.Source != "ptnew" {
		t.Fatalf("binding = %+v %v", b, err)
	}
	if len(f.trustMarked) != 0 {
		t.Fatalf("trusted folder: trust pill asked %v", f.trustMarked)
	}
	es := spawnEntries(t)
	if len(es) != 1 || es[0].Result != "launched" || es[0].WorkspaceID != spawnNewWS || es[0].Cell != "p" || es[0].Prompt != spawnPrompt || es[0].Mode != "auto" {
		t.Fatalf("registry = %+v", es)
	}
}

func TestSpawnUngroupedCaller(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.groupRef = ""
	f.launchesAs(persoCellDir(t), cwd)
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd); err != nil {
		t.Fatal(err)
	}
	if f.created[0].GroupRef != "" {
		t.Fatalf("ungrouped caller got group flags: %+v", f.created[0])
	}
}

func TestSpawnLaunchFailures(t *testing.T) {
	cases := []struct {
		name    string
		launch  func(f *spawnFake, cfg, cwd string)
		wantErr string
	}{
		{"wrong account", func(f *spawnFake, _, cwd string) { f.launchesAs("/elsewhere", cwd) }, "CLAUDE_CONFIG_DIR"},
		{"launcher ignored C_CWD", func(f *spawnFake, cfg, _ string) { f.launchesAs(cfg, "/Users/x/Developer") }, "ignored C_CWD"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cwd := t.TempDir()
			withCells(t, func(_, perso string) string { return perso })
			s, st := realSubjectDaemon(t)
			f := installSpawnFake(t)
			c.launch(f, persoCellDir(t), cwd)
			f.screen = "pigtail session spawn: the launcher in this shell is too old"
			_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) || !strings.Contains(err.Error(), "workspace:42") {
				t.Fatalf("err = %v, want %q and the workspace ref", err, c.wantErr)
			}
			b, bErr := st.GetCmuxBinding(spawnNewWS)
			if bErr != nil || b.SubjectID != "sub_p" || b.Source != "ptnew" {
				t.Fatalf("an unverified launch must stay bound ptnew: %+v %v", b, bErr)
			}
			if !strings.Contains(err.Error(), "left open, bound to sub_p") {
				t.Fatalf("err = %v, want it to say the workspace is bound", err)
			}
			if len(f.renamed) != 1 || !strings.Contains(err.Error(), `calling tab workspace:7 renamed "↗ perso one"`) {
				t.Fatalf("unverified spawn: caller renamed %q, err %v", f.renamed, err)
			}
			es := registryEntries(t)
			assertNoUnverified(t, es)
			kinds := entryKinds(es)
			last := es[len(es)-1]
			if kinds != "launch-reserve,launch-ref,launch-result" || last.Result != spawnreg.ResultFailed || last.ID != es[0].ID ||
				last.SubjectID != "sub_p" || last.WorkspaceID != spawnNewWS || last.WorkspaceRef != "workspace:42" {
				t.Fatalf("registry = %+v, want reserve, launch-ref, launch-result failed carrying the identity", es)
			}
		})
	}
}

// AC5 (ex-TestSpawnUnverifiedReportsBinding): nothing starts within 30 s —
// the launch is pending, not failed: exit 0, and the report of the old
// unverified error (binding, warnings, caller title) in --json.
func TestSpawnPendingReportsBinding(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t) // nothing starts in the new workspace
	f.screen = "pigtail session spawn: the launcher in this shell is too old"
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--json")
	if err != nil {
		t.Fatalf("err = %v, want a pending launch (exit 0)", err)
	}
	var v struct {
		State        string   `json:"state"`
		LaunchID     string   `json:"launch_id"`
		WorkspaceRef string   `json:"workspace_ref"`
		WorkspaceID  string   `json:"workspace_id"`
		SubjectID    string   `json:"subject_id"`
		Bound        *bool    `json:"bound"`
		Warnings     []string `json:"warnings"`
		CallerTitle  string   `json:"caller_title"`
	}
	if jErr := json.Unmarshal([]byte(out), &v); jErr != nil {
		t.Fatalf("decode %q: %v", out, jErr)
	}
	if v.State != "pending" || !strings.HasPrefix(v.LaunchID, "ln_") || v.WorkspaceRef != "workspace:42" || v.WorkspaceID != spawnNewWS ||
		v.SubjectID != "sub_p" || v.Bound == nil || !*v.Bound || v.Warnings == nil || v.CallerTitle != "↗ perso one" {
		t.Fatalf("json = %+v\n%s", v, out)
	}
	f.stamp = "auto"
	text, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err == nil || !strings.Contains(err.Error(), "launch in progress in workspace:42") {
		t.Fatalf("a second spawn while pending = %v (%s), want refused", err, text)
	}
}

// The text form of the pending result: bound, the tab's screen, the caller renamed.
func TestSpawnNothingStartsIsPending(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.screen = "pigtail session spawn: the launcher in this shell is too old"
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatalf("err = %v, want exit 0", err)
	}
	for _, want := range []string{"launch pending in workspace:42 — left open, bound to sub_p", "too old", `calling tab workspace:7 renamed "↗ perso one"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if b, bErr := st.GetCmuxBinding(spawnNewWS); bErr != nil || b.SubjectID != "sub_p" || b.Source != "ptnew" {
		t.Fatalf("a pending launch must stay bound ptnew: %+v %v", b, bErr)
	}
	if len(f.renamed) != 1 {
		t.Fatalf("caller renamed %q, want once", f.renamed)
	}
	es := registryEntries(t)
	assertNoUnverified(t, es)
	if entryKinds(es) != "launch-reserve,launch-ref" {
		t.Fatalf("registry kinds = %s, want reserve then launch-ref", entryKinds(es))
	}
	if len(f.armed) != 1 || f.armed[0].ID != es[0].ID {
		t.Fatalf("watcher armed %+v, want once with the reserve's id", f.armed)
	}
}

// AC6 (ex-TestSpawnUnverifiedBindConflict): pending and the bind refused — the
// result says so and gives the --replace command, on stderr and in warnings.
func TestSpawnPendingBindConflict(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	installSpawnFake(t)
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnNewWS, SubjectID: "sub_t", Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	want := "pigtail subject attach sub_p --workspace " + spawnNewWS + " --source ptnew --replace"
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil || !strings.Contains(out, "not bound to sub_p (CONFLICT: ") || !strings.Contains(errOut, want) {
		t.Fatalf("err = %v\nstdout %s\nstderr %s", err, out, errOut)
	}
	cwd2 := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s2, st2 := realSubjectDaemon(t)
	installSpawnFake(t)
	if err := st2.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnNewWS, SubjectID: "sub_t", Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	js, _, err := runSpawnCLI(t, s2, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd2, "--json")
	var v struct {
		Bound    *bool    `json:"bound"`
		Warnings []string `json:"warnings"`
	}
	if err != nil || json.Unmarshal([]byte(js), &v) != nil || v.Bound == nil || *v.Bound || !strings.Contains(strings.Join(v.Warnings, "\n"), want) {
		t.Fatalf("json = %+v (%v)\n%s", v, err, js)
	}
}

// Verified launch, bind refused by a conflict: the exact warning line, with --replace.
const bindConflictLine = "⚠ not bound to the subject (CONFLICT: cmux workspace already bound to another subject (attach --replace to move it)) — it is bound to another subject — to replace it: pigtail subject attach sub_p --workspace " + spawnNewWS + " --source ptnew --replace"

// The verified-launch warning keeps today's exact wording for a non-conflict
// cause, and names the replace for a conflict.
func TestBindHint(t *testing.T) {
	t.Parallel()
	p := spawnPlan{Subject: pigtail.Subject{ID: "sub_p", Scope: "perso"}, Cell: claudeprofile.Cell{Scope: "perso"}, CallerCell: claudeprofile.Cell{Scope: "perso"}}
	internal := &pigtail.APIError{Code: pigtail.ErrInternal, Message: "boom"}
	got := fmt.Sprintf("⚠ not bound to the subject (%v) — %s", internal, bindHint(p, spawnNewWS, internal))
	old := fmt.Sprintf("⚠ not bound to the subject (%v) — by hand: pigtail subject attach %s --workspace %s --source ptnew%s", internal, "sub_p", spawnNewWS, "")
	if got != old {
		t.Fatalf("non-conflict line changed:\n got %q\nwant %q", got, old)
	}
	conflict := &pigtail.APIError{Code: pigtail.ErrConflict, Message: "cmux workspace already bound to another subject (attach --replace to move it)"}
	if h := bindHint(p, spawnNewWS, conflict); h != "it is bound to another subject — to replace it: pigtail subject attach sub_p --workspace "+spawnNewWS+" --source ptnew --replace" {
		t.Fatalf("conflict hint = %q", h)
	}
	p.CallerCell.Scope = "globex"
	if h := bindHint(p, spawnNewWS, conflict); !strings.HasSuffix(h, " --replace --cross-scope") {
		t.Fatalf("cross-scope hint = %q", h)
	}
}

func TestSpawnBindConflictIsAWarning(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	// The new workspace id is already bound elsewhere: attach answers CONFLICT.
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnNewWS, SubjectID: "sub_t", Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatalf("a binding failure must not fail a running session: %v", err)
	}
	if !strings.Contains(errOut, bindConflictLine+"\n") {
		t.Fatalf("stderr = %q, want the line %q", errOut, bindConflictLine)
	}
	if !strings.Contains(out, "✓") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestSpawnJSON(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		WorkspaceRef string `json:"workspace_ref"`
		WorkspaceID  string `json:"workspace_id"`
		Cell         string `json:"cell"`
		Cwd          string `json:"cwd"`
		PID          int    `json:"pid"`
		Mode         string `json:"permission_mode"`
		Prompt       string `json:"prompt"`
		Bound        bool   `json:"bound"`
		CallerTitle  string `json:"caller_title"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if v.WorkspaceRef != "workspace:42" || v.WorkspaceID != spawnNewWS || v.Cell != "p" || v.PID != 5000 || !v.Bound || v.Mode != "auto" || v.Prompt != spawnPrompt || v.CallerTitle != "↗ perso one" {
		t.Fatalf("json = %+v", v)
	}
	if strings.Contains(out, "notified") {
		t.Fatalf("json still names notified: %s", out)
	}
}

func TestSpawnConsumesThePermissionRecord(t *testing.T) {
	cwd := t.TempDir()
	fourCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run"); err != nil || f.consumed != 0 {
		t.Fatalf("dry-run = %v, consumed %d (a dry-run must leave the record)", err, f.consumed)
	}
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd); err != nil || f.consumed != 1 {
		t.Fatalf("real spawn = %v, consumed %d", err, f.consumed)
	}
	// A second real spawn with no new record (a call that escaped the hook) is refused.
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", cwd, "--cross-scope")
	if err == nil || !strings.Contains(err.Error(), "current permission mode unknown") {
		t.Fatalf("second spawn without a record = %v", err)
	}
}

func TestSpawnDryRunJSON(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.stamp = "plan"
	f.addProc(8000, "/x/bin/claude", "")
	f.badEnv[8000] = true
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		DryRun      bool     `json:"dry_run"`
		SubjectID   string   `json:"subject_id"`
		Cell        string   `json:"cell"`
		ConfigDir   string   `json:"config_dir"`
		Cwd         string   `json:"cwd"`
		Mode        string   `json:"permission_mode"`
		LaunchMode  string   `json:"launch_mode"`
		Origin      string   `json:"launch_mode_origin"`
		Current     string   `json:"current_mode"`
		Prompt      string   `json:"prompt"`
		GroupRef    string   `json:"group_ref"`
		CallerRef   string   `json:"caller_workspace_ref"`
		Live        int      `json:"live_spawned"`
		Recent      int      `json:"launches_recent"`
		BurstMax    int      `json:"launches_max"`
		WindowS     int      `json:"launches_window_s"`
		RelayFrom   *string  `json:"relay_from"`
		Argv        []string `json:"cmux_argv"`
		Command     string   `json:"command"`
		Unreadable  []int    `json:"unreadable_pids"`
		Warnings    []string `json:"warnings"`
		CallerTitle string   `json:"caller_title"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("dry-run --json is not one JSON object: %v\n%s", err, out)
	}
	if !v.DryRun || v.SubjectID != "sub_p" || v.Cell != "p" || v.ConfigDir != persoCellDir(t) || v.Cwd != cwd ||
		v.Mode != "plan" || v.LaunchMode != "auto" || v.Origin != "cell default" || v.Current != "plan" ||
		v.Prompt != spawnPrompt || v.GroupRef != "workspace_group:3" || v.CallerRef != "workspace:7" ||
		v.Recent != 0 || v.BurstMax != 10 || v.WindowS != 600 || v.RelayFrom == nil || *v.RelayFrom != "" || len(v.Argv) == 0 || v.Argv[0] != "new-workspace" || !strings.Contains(v.Command, "C_CWD=") ||
		len(v.Unreadable) != 1 || v.Unreadable[0] != 8000 || len(v.Warnings) != 1 || v.CallerTitle != "↗ perso one" ||
		!strings.Contains(strings.Join(v.Argv, " "), "--group-placement afterCurrent --group-reference workspace:7") {
		t.Fatalf("dry-run json = %+v", v)
	}
	for _, gone := range []string{`"max_live"`, `"live_counted"`, `"alive"`, `"alive_ceiling"`, `"live_breakdown"`, `"live_starting"`} {
		if strings.Contains(out, gone) {
			t.Errorf("dry-run json still carries %s", gone)
		}
	}
	if len(f.created) != 0 || f.consumed != 0 {
		t.Fatal("dry-run --json created a workspace or consumed the record")
	}
}

func TestSpawnJSONRefusal(t *testing.T) {
	for _, extra := range [][]string{{"--dry-run", "--json"}, {"--json"}} {
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installSpawnFake(t)
		f.stamp = ""
		args := append([]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir()}, extra...)
		out, _, err := runSpawnCLI(t, s, args...)
		if err == nil || !strings.Contains(err.Error(), "current permission mode unknown") {
			t.Fatalf("%v: err = %v, want the refusal", extra, err)
		}
		var v struct {
			Error  string `json:"error"`
			DryRun bool   `json:"dry_run"`
		}
		if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
			t.Fatalf("%v: refusal stdout is not a JSON object: %v\n%q", extra, jerr, out)
		}
		if v.Error != err.Error() || v.DryRun != (extra[0] == "--dry-run") {
			t.Fatalf("%v: json = %+v, err = %v", extra, v, err)
		}
		if len(f.created) != 0 {
			t.Fatal("a refusal created a workspace")
		}
	}
}

// TestSpawnCarriesPromptInEnv: the workspace cmux creates gets the prompt and
// the cwd as --env values, and a --command that is pure ASCII.
func TestSpawnCarriesPromptInEnv(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "fraîches")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	const prompt = "/example-skill resume sub_p — café"
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", prompt, "--cwd", cwd); err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	spec := f.created[0]
	for i := 0; i < len(spec.Command); i++ {
		if spec.Command[i] >= 0x80 {
			t.Fatalf("non-ASCII byte in --command: %q", spec.Command)
		}
	}
	env := strings.Join(spec.Env, "\n")
	if !strings.Contains(env, "\nPIGTAIL_SPAWN_PROMPT="+prompt) || !strings.Contains(env, "\nPIGTAIL_SPAWN_CWD="+cwd) {
		t.Fatalf("env = %q", spec.Env)
	}
}

// A2 / D14: on the trust dialog, spawn asks the daemon for the pill on the
// new workspace (never the caller's), warns, posts no notification.
// updateGolden rewrites the golden files of this package (go test -update).
var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden")

// TestSpawnArgvGolden pins the cmux argv and env a real spawn hands to
// new-workspace, byte for byte (plan lot 2, task 1: the safety net of the
// launch line's move into internal/sessionlaunch). The non-ASCII prompt only
// travels by --env (sub_x000000000000039). Temp paths become <CONFIG_DIR>
// and <CWD> before the comparison.
func TestSpawnArgvGolden(t *testing.T) {
	const prompt = "/example-skill resume sub_x — écrit « é »"
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", prompt, "--cwd", cwd); err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if len(f.created) != 1 {
		t.Fatalf("created = %d workspaces, want 1", len(f.created))
	}
	spec := f.created[0]
	mask := strings.NewReplacer(persoCellDir(t), "<CONFIG_DIR>", cwd, "<CWD>")
	var b strings.Builder
	b.WriteString("# argv\n")
	for _, a := range cmux.NewWorkspaceArgs(spec) {
		b.WriteString(mask.Replace(a) + "\n")
	}
	b.WriteString("# env\n")
	for _, e := range spec.Env {
		b.WriteString(mask.Replace(e) + "\n")
	}
	got := b.String()
	path := filepath.Join("testdata", "spawn-argv.golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to write it): %v", err)
	}
	if got != string(want) {
		t.Fatalf("spawn argv/env drifted from %s:\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

func TestSpawnMarksTrustOnTheNewWorkspace(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	f.screen = trustScreen
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd); err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if len(f.trustMarked) != 1 || f.trustMarked[0] != spawnNewWS+"|5000" {
		t.Fatalf("trust marked = %v, want the new workspace and its pid", f.trustMarked)
	}
}

// A22: the daemon refuses (or is unreachable): a ⚠ naming the workspace on
// stderr and in --json warnings, exit 0 (text: D21·C10).
func TestSpawnTrustPendingRefused(t *testing.T) {
	const want = `⚠ the trust-folder pill could not be posted on workspace:42 (`
	for _, asJSON := range []bool{false, true} {
		cwd := t.TempDir()
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installSpawnFake(t)
		f.launchesAs(persoCellDir(t), cwd)
		f.screen = trustScreen
		f.trustErr = errors.New("404 not found")
		args := []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}
		if asJSON {
			args = append(args, "--json")
		}
		out, errOut, err := runSpawnCLI(t, s, args...)
		if err != nil {
			t.Fatalf("json=%v: must not fail the spawn: %v", asJSON, err)
		}
		if !strings.Contains(errOut, want) || !strings.Contains(errOut, "accept it there by hand") {
			t.Fatalf("json=%v: stderr = %q", asJSON, errOut)
		}
		if asJSON && (!strings.Contains(out, want) || strings.Contains(out, `"notified"`)) {
			t.Fatalf("json = %q", out)
		}
	}
}

// R1 (verify N2 round 1): an older daemon answers 404 with a body ending in
// a newline; the C10 line stays one line, on stderr and in --json warnings.
func TestSpawnTrustPendingErrorOnOneLine(t *testing.T) {
	const want = "⚠ the trust-folder pill could not be posted on workspace:42 (pigtail: HTTP 404: 404 page not found) — the sidebar will not show it: accept it there by hand"
	for _, asJSON := range []bool{false, true} {
		cwd := t.TempDir()
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installSpawnFake(t)
		f.launchesAs(persoCellDir(t), cwd)
		f.screen = trustScreen
		f.trustErr = errors.New("pigtail: HTTP 404: 404 page not found\n")
		args := []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}
		if asJSON {
			args = append(args, "--json")
		}
		out, errOut, err := runSpawnCLI(t, s, args...)
		if err != nil {
			t.Fatalf("json=%v: %v", asJSON, err)
		}
		if !strings.Contains(errOut, want+"\n") {
			t.Fatalf("json=%v: stderr = %q, want the line %q", asJSON, errOut, want)
		}
		if asJSON {
			var v struct {
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Warnings) != 2 || v.Warnings[1] != want {
				t.Fatalf("json warnings = %q %v", v.Warnings, err)
			}
		}
	}
}

// A21: a trusted folder: no pill asked, no notification, output unchanged.
func TestSpawnTrustedFolderNoPill(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	f.screen = "> /example-skill resume sub_p\n  ? for shortcuts"
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil || len(f.trustMarked) != 0 || !strings.HasPrefix(out, "✓ fresh session on sub_p") {
		t.Fatalf("err=%v marked=%v out=%q", err, f.trustMarked, out)
	}
}

const trustScreen = `
 Do you trust the files in this folder?

 /Users/x/Developer/w

 ❯ 1. Yes, proceed
   2. No, exit
`

func TestTrustDialogShown(t *testing.T) {
	t.Parallel()
	for _, sc := range []string{trustScreen, "Quick safety check: Is this a project you created or one you trust?", "❯ 1. Yes, I trust this folder"} {
		if !trustDialogShown(sc) {
			t.Errorf("not detected: %q", sc)
		}
	}
	for _, sc := range []string{"", "> /example-skill resume sub_p\n  ? for shortcuts"} {
		if trustDialogShown(sc) {
			t.Errorf("false positive: %q", sc)
		}
	}
}

// TestSpawnWarnsOnTrustDialog: a folder trust could not be pre-accepted for
// leaves the new session on Claude's trust dialog. spawn never answers the
// dialog (pre-trust happens before launch, from an inherited grant), but says
// so loudly: stderr, --json warnings and the sidebar pill the daemon shows
// (D14); exit 0.
func TestSpawnWarnsOnTrustDialog(t *testing.T) {
	const want = `⚠ the new session waits on "trust this folder" in workspace:42 — accept it there`
	for _, asJSON := range []bool{false, true} {
		cwd := t.TempDir()
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installSpawnFake(t)
		f.launchesAs(persoCellDir(t), cwd)
		f.screen = trustScreen
		args := []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}
		if asJSON {
			args = append(args, "--json")
		}
		out, errOut, err := runSpawnCLI(t, s, args...)
		if err != nil {
			t.Fatalf("json=%v: a waiting trust dialog must not fail the spawn: %v", asJSON, err)
		}
		if !strings.Contains(errOut, want) {
			t.Fatalf("json=%v: stderr = %q, want %q", asJSON, errOut, want)
		}
		if len(f.trustMarked) != 1 {
			t.Fatalf("json=%v: trust pill asked %v, want once", asJSON, f.trustMarked)
		}
		if asJSON {
			var v struct {
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Warnings) != 1 || v.Warnings[0] != want {
				t.Fatalf("json warnings = %+v %v (%q)", v, err, out)
			}
		}
	}
}

func TestSpawnNoTrustDialogNoWarning(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	f.screen = "> /example-skill resume sub_p"
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--json")
	if err != nil || strings.Contains(errOut, "trust") || !strings.Contains(out, `"warnings":[]`) {
		t.Fatalf("no dialog: err=%v stderr=%q out=%q", err, errOut, out)
	}
}

// TestTrustWatchStopsOnReadyFooter: a trusted folder shows Claude's idle
// footer at once; the watch must end there, not after the whole window.
func TestTrustWatchStopsOnReadyFooter(t *testing.T) {
	for _, footer := range []string{"⏵⏵ auto mode on (shift+tab to cycle)", "? for shortcuts", "esc to interrupt"} {
		f := installSpawnFake(t)
		f.screen = "> /example-skill resume sub_p\n  " + footer
		spawnTrustWindow = 2 * time.Second
		start := time.Now()
		if waitsOnTrust("surface:99") {
			t.Fatalf("%q: detected a trust dialog", footer)
		}
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Fatalf("%q: watch ran %s, want an early stop on the ready footer", footer, d)
		}
	}
}

// D1 (sub_x000000000000040): the help says where a spawned session is born.
func TestSpawnHelpSaysBornUnderCaller(t *testing.T) {
	t.Parallel()
	long := sessionSpawnCmd().Long
	if !strings.Contains(long, "right under the caller, in the caller's group") {
		t.Fatalf("help does not say the session is born right under the caller:\n%s", long)
	}
	if strings.Contains(long, "at the bottom of the caller's group") {
		t.Fatalf("help still says the bottom of the caller's group:\n%s", long)
	}
	if strings.Contains(long, "next to the caller") {
		t.Fatalf("help still says next to the caller:\n%s", long)
	}
}

// The per-subject guard is not the cap: an idle session on the same subject
// still refuses the spawn.
func TestSpawnSubjectGuardCountsIdle(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnOtherWS, SubjectID: "sub_p", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "already has a live session") {
		t.Fatalf("idle session on the same subject = %v", err)
	}
	if exitCode(err) != 1 {
		t.Fatalf("live session on the subject exits %d, want 1", exitCode(err))
	}
}

// AC1, AC2 (D1, D3): an unbound caller is renamed after the subject it launched, and the spawn says so.
func TestSpawnRenamesUnboundCaller(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if len(f.renamed) != 1 || f.renamed[0] != spawnCallerWS+"|↗ perso one" {
		t.Fatalf("renamed = %q", f.renamed)
	}
	if !strings.Contains(out, `  caller: workspace:7 renamed "↗ perso one"`) {
		t.Fatalf("stdout = %q", out)
	}
}

// AC1: --name names the new tab, never the caller.
func TestSpawnCallerTitleIgnoresName(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--name", "Custom tab"); err != nil {
		t.Fatal(err)
	}
	if f.created[0].Name != "Custom tab" || len(f.renamed) != 1 || f.renamed[0] != spawnCallerWS+"|↗ perso one" {
		t.Fatalf("new tab %q, caller renamed %q", f.created[0].Name, f.renamed)
	}
}

// AC3: a bound caller (a chain's spawned tab, a subject's tab) keeps the title cmuxsync gives it.
func TestSpawnLeavesBoundCallerAlone(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnCallerWS, SubjectID: "sub_t", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.renamed) != 0 || !strings.Contains(out, `"caller_title":""`) {
		t.Fatalf("bound caller renamed: %q\n%s", f.renamed, out)
	}
}

// AC4: cmux refuses the rename — a warning, the spawn stands.
func TestSpawnCallerRenameFailureIsAWarning(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	f.renameErr = errors.New("cmux: socket closed")
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--json")
	if err != nil {
		t.Fatalf("a rename failure failed the spawn: %v", err)
	}
	want := `⚠ calling workspace not renamed to "↗ perso one" (cmux: socket closed) — the spawn itself succeeded`
	if !strings.Contains(errOut, want) || !strings.Contains(out, `"caller_title":""`) || !strings.Contains(out, "calling workspace not renamed") {
		t.Fatalf("stderr = %q\nstdout = %s", errOut, out)
	}
}

// AC4 / Review focus 1: the lookup fails (not a 404) — the caller may be bound: no rename, a warning.
func TestSpawnCallerLookupFailureIsAWarning(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	f.boundErr = errors.New("daemon: 500 internal")
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--json")
	if err != nil {
		t.Fatal(err)
	}
	const want = "⚠ calling workspace not renamed: cannot tell whether it is bound (daemon: 500 internal)"
	if len(f.renamed) != 0 || !strings.Contains(errOut, want) {
		t.Fatalf("renamed %q, stderr %q", f.renamed, errOut)
	}
	var v struct {
		CallerTitle *string  `json:"caller_title"`
		Warnings    []string `json:"warnings"`
	}
	if jErr := json.Unmarshal([]byte(out), &v); jErr != nil || v.CallerTitle == nil || *v.CallerTitle != "" || !slices.Contains(v.Warnings, want) {
		t.Fatalf("json = %+v (%v)\n%s", v, jErr, out) // D3: the warning is in --json too
	}
}

// AC1: a hand-typed short title on several lines gives one clean line.
func TestSpawnCallerTitleIsOneLine(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	sub, _ := st.GetSubject("sub_p")
	sub.ShortTitle = "Titres\n  onglets   spawn"
	if err := st.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd); err != nil {
		t.Fatal(err)
	}
	if len(f.renamed) != 1 || f.renamed[0] != spawnCallerWS+"|↗ Titres onglets spawn" {
		t.Fatalf("renamed = %q", f.renamed)
	}
}

// AC8 (D5): two spawns from the same unbound tab — the last subject launched gives the title.
func TestSpawnLastSpawnTitlesTheCaller(t *testing.T) {
	cwd := t.TempDir()
	dirs, _, _ := fourCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd); err != nil {
		t.Fatal(err)
	}
	f.stamp = "auto" // the first spawn consumed the permission record
	f.launch = func(cmux.WorkspaceSpec) {
		f.addProc(5001, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnNewWS+" CLAUDE_CONFIG_DIR="+dirs["wa"]+" PWD="+cwd)
	}
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", cwd, "--cross-scope"); err != nil {
		t.Fatalf("second spawn: %v\n%s", err, errOut)
	}
	want := []string{spawnCallerWS + "|↗ perso one", spawnCallerWS + "|↗ work one"}
	if strings.Join(f.renamed, ",") != strings.Join(want, ",") {
		t.Fatalf("renamed = %q, want %q", f.renamed, want)
	}
}

// AC9, AC12 (D6): the dry-run announces the rename and makes it not.
func TestSpawnDryRunAnnouncesCallerRename(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.renamed) != 0 || !strings.Contains(out, `  caller    would rename workspace:7 to "↗ perso one"`) {
		t.Fatalf("renamed %q\n%s", f.renamed, out)
	}
}

// AC9: a bound caller — the dry-run announces nothing, in JSON or in text.
func TestSpawnDryRunBoundCallerAnnouncesNothing(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnCallerWS, SubjectID: "sub_t", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run", "--json")
	if err != nil || !strings.Contains(out, `"caller_title":""`) {
		t.Fatalf("err %v\n%s", err, out)
	}
	out, _, err = runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err != nil || strings.Contains(out, "caller    ") || strings.Contains(out, "would rename") || strings.Contains(out, "would not be renamed") {
		t.Fatalf("text dry-run: err %v\n%s", err, out)
	}
	if len(f.renamed) != 0 {
		t.Fatalf("a dry-run renamed the caller: %q", f.renamed)
	}
}

// AC9: the lookup fails — the dry-run says it cannot tell.
func TestSpawnDryRunCallerLookupFailure(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.boundErr = errors.New("daemon: 500 internal")
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err != nil || !strings.Contains(out, "⚠ calling workspace would not be renamed: cannot tell whether it is bound (daemon: 500 internal)") {
		t.Fatalf("err %v\n%s", err, out)
	}
}

// AC11 (D7): a work session in a tab bound to a perso subject leaves that tab alone, dry-run included.
func TestSpawnCallerBoundElsewhereIsLeftAlone(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(work, _ string) string { return work })
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(os.Getenv("CLAUDE_CONFIG_DIR"), cwd)
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnCallerWS, SubjectID: "sub_p", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run")
	if err != nil || strings.Contains(out, "would rename") {
		t.Fatalf("dry-run: %v\n%s", err, out)
	}
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", cwd); err != nil {
		t.Fatalf("spawn: %v\n%s", err, errOut)
	}
	if len(f.renamed) != 0 {
		t.Fatalf("a tab bound to a perso subject was renamed: %q", f.renamed)
	}
}

func TestSpawnPreTrustsBeforeLaunch(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	f.screen = "> /example-skill resume sub_p\n  ? for shortcuts"
	f.preTrust = claudeconfig.Result{Outcome: claudeconfig.Written, Key: "/r/repo", Ancestor: "/r"}
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if want := persoCellDir(t) + "|" + cwd; len(f.preTrustCalls) != 1 || f.preTrustCalls[0] != want {
		t.Fatalf("pre-trust calls %v, want [%s]", f.preTrustCalls, want)
	}
	if len(f.order) < 2 || f.order[0] != "pretrust" || f.order[1] != "launch" {
		t.Fatalf("order %v, want pretrust before launch", f.order)
	}
	if !strings.Contains(out, "trust: pre-accepted /r/repo in cell p (inherited from /r)") || strings.Contains(errOut, "trust") {
		t.Fatalf("out=%q err=%q", out, errOut)
	}
}

func TestSpawnPreTrustFailureWarnsAndLaunches(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		cwd := t.TempDir()
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installSpawnFake(t)
		f.launchesAs(persoCellDir(t), cwd)
		f.screen = "> /example-skill resume sub_p\n  ? for shortcuts"
		f.preTrust = claudeconfig.Result{Outcome: claudeconfig.Failed, Reason: "lock held"}
		args := []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}
		if asJSON {
			args = append(args, "--json")
		}
		out, errOut, err := runSpawnCLI(t, s, args...)
		const want = `⚠ folder trust not pre-accepted (lock held) — the session may stop on "trust this folder"`
		if err != nil || len(f.created) != 1 || !strings.Contains(errOut, want) {
			t.Fatalf("json=%v err=%v created=%d stderr=%q", asJSON, err, len(f.created), errOut)
		}
		if asJSON {
			var v struct {
				Warnings []string            `json:"warnings"`
				PreTrust claudeconfig.Result `json:"pretrust"`
			}
			if json.Unmarshal([]byte(out), &v) != nil || len(v.Warnings) != 1 || v.Warnings[0] != want || v.PreTrust.Outcome != claudeconfig.Failed {
				t.Fatalf("json %q", out)
			}
		}
	}
}

func TestSpawnTrustWarningNamesWhyNotPreTrusted(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	f.screen = trustScreen
	f.preTrust = claudeconfig.Result{Outcome: claudeconfig.NoTrustedAncestor, Reason: "no directory from /a up to /b is trusted in /c"}
	_, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	want := `⚠ the new session waits on "trust this folder" in workspace:42 — accept it there (not pre-accepted: no directory from /a up to /b is trusted in /c)`
	if err != nil || !strings.Contains(errOut, want) {
		t.Fatalf("err=%v stderr=%q", err, errOut)
	}
}

// A write that happened is reported even when the launch then fails.
func TestSpawnReportsPreTrustWhenLaunchFails(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.preTrust = claudeconfig.Result{Outcome: claudeconfig.Written, Key: "/r/repo", Ancestor: "/r"}
	f.launchErr = errors.New("cmux down")
	_, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err == nil || !strings.Contains(errOut, "trust: pre-accepted /r/repo in cell p (inherited from /r) before the launch failed — the entry stays") {
		t.Fatalf("err=%v stderr=%q", err, errOut)
	}
}

// --json: the pre-trust result is in the success object, and in the error
// object when the launch fails after a write.
func TestSpawnJSONCarriesPreTrust(t *testing.T) {
	for _, fail := range []bool{false, true} {
		cwd := t.TempDir()
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installSpawnFake(t)
		f.launchesAs(persoCellDir(t), cwd)
		f.screen = "> /example-skill resume sub_p\n  ? for shortcuts"
		f.preTrust = claudeconfig.Result{Outcome: claudeconfig.Written, Key: "/r/repo", Ancestor: "/r"}
		if fail {
			f.launchErr = errors.New("cmux down")
		}
		out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--json")
		if (err != nil) != fail {
			t.Fatalf("fail=%v: err=%v", fail, err)
		}
		var v struct {
			Error    string              `json:"error"`
			PreTrust claudeconfig.Result `json:"pretrust"`
		}
		if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
			t.Fatalf("fail=%v: not one JSON object: %q", fail, out)
		}
		if v.PreTrust.Outcome != claudeconfig.Written || v.PreTrust.Key != "/r/repo" || v.PreTrust.Ancestor != "/r" || (v.Error != "") != fail {
			t.Fatalf("fail=%v: %+v", fail, v)
		}
	}
}

// --dry-run evaluates no pre-trust: it writes nothing anywhere. pflag keeps
// the last value of a repeated flag (a past correction on the respawn guard):
// "--dry-run=false --dry-run" is a dry run, "--dry-run --dry-run=false" is not.
func TestSpawnDryRunDoesNotPreTrust(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		dry   bool
	}{
		{[]string{"--dry-run"}, true},
		{[]string{"--dry-run=false", "--dry-run"}, true},
		{[]string{"--dry-run", "--dry-run=false"}, false},
	} {
		cwd := t.TempDir()
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installSpawnFake(t)
		f.launchesAs(persoCellDir(t), cwd)
		f.screen = "> /example-skill resume sub_p\n  ? for shortcuts"
		f.preTrust = claudeconfig.Result{Outcome: claudeconfig.Written, Key: "/r/repo", Ancestor: "/r"}
		args := append([]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd}, tc.flags...)
		if _, _, err := runSpawnCLI(t, s, args...); err != nil {
			t.Fatalf("%v: %v", tc.flags, err)
		}
		if tc.dry && (len(f.preTrustCalls) != 0 || len(f.created) != 0) {
			t.Fatalf("%v is a dry run: pre-trust calls %v, created %d", tc.flags, f.preTrustCalls, len(f.created))
		}
		if !tc.dry && len(f.preTrustCalls) != 1 {
			t.Fatalf("%v is a real spawn: pre-trust calls %v", tc.flags, f.preTrustCalls)
		}
	}
}

// Pre-trust wrote, yet Claude still shows the dialog: the warning says so.
func TestSpawnTrustWarningAfterWrite(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	f.screen = trustScreen
	f.preTrust = claudeconfig.Result{Outcome: claudeconfig.Written, Key: "/r/repo", Ancestor: "/r"}
	_, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	want := `⚠ the new session waits on "trust this folder" in workspace:42 — accept it there (pre-accepted /r/repo but Claude still asks)`
	if err != nil || !strings.Contains(errOut, want) || len(f.trustMarked) != 1 {
		t.Fatalf("err=%v marked=%v stderr=%q", err, f.trustMarked, errOut)
	}
}

// Review Focus 1: a cell still written work and a globex cell both spawn
// on a globex subject as their own scope — no --cross-scope asked, none
// suggested when the binding fails — and neither sees a perso subject.
func TestSpawnFromWorkCellOnGlobexSubject(t *testing.T) {
	scopes := map[string]string{"t": "globex", "s": "globex", "p": "perso"}
	for _, cell := range []string{"t", "s"} {
		t.Run(cell+" dry-run", func(t *testing.T) {
			withCellScopes(t, scopes, cell)
			s, _ := realSubjectDaemon(t)
			installSpawnFake(t)
			if out, _, err := runSpawnCLI(t, s, "--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"); err != nil {
				t.Fatalf("dry-run on sub_t from cell %s: %v\n%s", cell, err, out)
			}
		})
		t.Run(cell+" bind conflict", func(t *testing.T) {
			cwd := t.TempDir()
			dirs := withCellScopes(t, scopes, cell)
			s, st := realSubjectDaemon(t)
			f := installSpawnFake(t)
			f.launchesAs(dirs[cell], cwd)
			if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnNewWS, SubjectID: "sub_p", Source: "attach", BoundAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			_, errOut, err := runSpawnCLI(t, s, "--subject", "sub_t", "--prompt", spawnPrompt, "--cwd", cwd)
			if err != nil {
				t.Fatalf("a binding failure must not fail a running session: %v", err)
			}
			if !strings.Contains(errOut, "pigtail subject attach sub_t --workspace "+spawnNewWS) || strings.Contains(errOut, "--cross-scope") {
				t.Fatalf("cell %s: warning = %q, want an attach hint without --cross-scope", cell, errOut)
			}
		})
	}
	t.Run("t perso subject", func(t *testing.T) {
		withCellScopes(t, scopes, "t")
		s, _ := realSubjectDaemon(t)
		installSpawnFake(t)
		if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("work cell on sub_p: %v, want not found", err)
		}
	})
}

func TestSpawnHelpNamesTheNewFlags(t *testing.T) {
	var o bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&o)
	cmd.SetArgs([]string{"spawn", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	h := o.String()
	for _, want := range []string{"--cell string", "--new-worktree", "--branch string", "subjectless", "cwd_home", "at least 7 minutes", "moved to the background, not killed", "never re-run the spawn"} { // T15's caller rule (verify 2026-10-08: a Bash timeout backgrounds the call)
		if !strings.Contains(h, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if strings.Contains(h, "`") {
		t.Errorf("help renders a backtick (cobra value-name trap)")
	}
	if strings.Contains(h, "--max-live") {
		t.Errorf("help still shows --max-live")
	}
	// D1: --subject is optional for a subjectless spawn — its usage must not say plain "(required)".
	if strings.Contains(h, "subject id (required)") {
		t.Errorf("--subject usage still says it is always required")
	}
}

// stubSpawnWorkspaceAt stubs spawnWorkspaceAt with a workspace at path whose hooks
// read h (nil = unknown); installSpawnFake's cleanup restores it.
func stubSpawnWorkspaceAt(id string, h *pigtail.HooksResult) {
	spawnWorkspaceAt = func(_ context.Context, _ *pigtail.Client, path string) (*pigtail.Workspace, error) {
		return &pigtail.Workspace{ID: id, Path: path, Hooks: h}, nil
	}
}

// TestSpawnRefusesAWorktreeWhoseHooksFailed (criterion 2, spawn): a definitive
// refusal naming the hook, the log, and both ways out — dry run included.
func TestSpawnRefusesAWorktreeWhoseHooksFailed(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	wtDir := t.TempDir()
	stubSpawnWorkspaceAt("ws_bad", &pigtail.HooksResult{
		Status: pigtail.HooksFailed, Hook: "husky", Error: "hook husky failed: exit status 3", Log: "/s/hooks.log"})
	for _, extra := range [][]string{nil, {"--dry-run"}} {
		args := append([]string{"--subject", "sub_p", "--cwd", wtDir, "--prompt", spawnPrompt}, extra...)
		_, _, err := runSpawnCLI(t, s, args...)
		if err == nil || !strings.Contains(err.Error(), "husky") || !strings.Contains(err.Error(), "/s/hooks.log") ||
			!strings.Contains(err.Error(), "pigtail hooks run ws_bad") || !strings.Contains(err.Error(), "pigtail clean ws_bad") || len(f.created) != 0 {
			t.Fatalf("%v: err = %v, created %d; want a refusal naming husky, the log, pigtail hooks run and pigtail clean", extra, err, len(f.created))
		}
		if _, ok := retryAfter(err); ok {
			t.Fatal("a failed hook is not worth a retry")
		}
	}
}

// TestSpawnRefusesRunningHooksAsRetryable: hooks still running → a long retry.
func TestSpawnRefusesRunningHooksAsRetryable(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	wtDir := t.TempDir()
	stubSpawnWorkspaceAt("ws_run", &pigtail.HooksResult{Status: pigtail.HooksRunning, Log: "/s/hooks.log"})
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cwd", wtDir, "--prompt", spawnPrompt)
	if err == nil || !strings.Contains(err.Error(), "still running") || !strings.Contains(err.Error(), "/s/hooks.log") || len(f.created) != 0 {
		t.Fatalf("err = %v, created %d; want a refusal saying still running, with the log", err, len(f.created))
	}
	if d, ok := retryAfter(err); !ok || d != retryLong {
		t.Fatalf("retryAfter = %v, %v; want retryLong", d, ok)
	}
}

// TestSpawnUnknownHooksDoNotBlock (criterion 3): a workspace from before
// migration 0023 (Hooks nil), or a directory pigtail does not manage, passes.
func TestSpawnUnknownHooksDoNotBlock(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := t.TempDir()
	stubSpawnWorkspaceAt("ws_old", nil)
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run"); err != nil {
		t.Fatalf("unknown hooks: %v", err)
	}
	spawnWorkspaceAt = func(context.Context, *pigtail.Client, string) (*pigtail.Workspace, error) { return nil, nil }
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run"); err != nil {
		t.Fatalf("no workspace: %v", err)
	}
}

// TestSpawnRefusesFromInsideAFailedWorktree: --cwd a subdirectory of the
// worktree, and the daemon's path not symlink-resolved (t.TempDir() is under
// /var, git answers /private/var on macOS): the refusal must still hold.
func TestSpawnRefusesFromInsideAFailedWorktree(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	base := t.TempDir() // NOT EvalSymlinks'd: the workspace path keeps /var
	repo, wt := filepath.Join(base, "repo"), filepath.Join(base, "repo-wt", "feat")
	for _, a := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
		{"-C", repo, "worktree", "add", "-q", "-b", "feat", wt},
	} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	sub := filepath.Join(wt, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s, _ := realSubjectDaemonWith(t, func(r chi.Router, _ *store.Store) {
		r.Get("/workspaces", func(w http.ResponseWriter, _ *http.Request) {
			writeData(w, http.StatusOK, []pigtail.Workspace{{ID: "ws_bad", Path: wt, Hooks: &pigtail.HooksResult{
				Status: pigtail.HooksFailed, Hook: "husky", Error: "hook husky failed: exit status 3", Log: "/s/hooks.log"}}})
		})
	})
	realAt := spawnWorkspaceAt
	f := installSpawnFake(t)
	spawnWorkspaceAt = realAt // the real lookup, against the test daemon (installSpawnFake's cleanup restores it)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cwd", sub, "--prompt", spawnPrompt, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "husky") || !strings.Contains(err.Error(), "pigtail hooks run ws_bad") || len(f.created) != 0 {
		t.Fatalf("err = %v, created %d; want the husky refusal from a subdirectory of ws_bad", err, len(f.created))
	}
}

// registryEntries reads the test's spawn registry without its launch margin
// rows (Kind margin, read by marginEntries): a trace every real spawn writes
// before its reservation (unmeasured here: no usage reading), never a record
// of a launch.
func registryEntries(t *testing.T) []spawnreg.Entry {
	t.Helper()
	all, err := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if err != nil {
		t.Fatal(err)
	}
	var es []spawnreg.Entry
	for _, e := range all {
		if e.Kind != spawnreg.KindMargin {
			es = append(es, e)
		}
	}
	return es
}

// spawnEntries keeps the spawn entries (Kind ""): one per launched session.
func spawnEntries(t *testing.T) []spawnreg.Entry {
	t.Helper()
	var out []spawnreg.Entry
	for _, e := range registryEntries(t) {
		if e.Kind == "" {
			out = append(out, e)
		}
	}
	return out
}

func entryKinds(es []spawnreg.Entry) string {
	var k []string
	for _, e := range es {
		if e.Kind == "" {
			k = append(k, e.Result)
		} else {
			k = append(k, e.Kind)
		}
	}
	return strings.Join(k, ",")
}

func assertNoUnverified(t *testing.T, es []spawnreg.Entry) {
	t.Helper()
	for _, e := range es {
		if e.Result == "unverified" {
			t.Fatalf("an unverified entry is still written: %+v", es)
		}
	}
}

// --- T2 (sub_x000000000000015): the scoped reservation on the CLI path ---

func stubPidAlive(t *testing.T, alive func(int) bool) {
	t.Helper()
	testutil.SerialOnly(t) // swaps pidAlive
	prev := pidAlive
	pidAlive = alive
	t.Cleanup(func() { pidAlive = prev })
}

// seedLaunch appends records to the test's registry.
func seedLaunch(t *testing.T, es ...spawnreg.Entry) {
	t.Helper()
	reg := spawnreg.Open(spawnreg.DefaultPath())
	for _, e := range es {
		if err := reg.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

func seedReserve(id, sub string, owner int, age time.Duration) spawnreg.Entry {
	return spawnreg.Entry{TS: time.Now().UTC().Add(-age), Kind: spawnreg.KindLaunchReserve, ID: id, SubjectID: sub, OwnerPID: owner, Result: spawnreg.ResultPending}
}

func seedRef(id, sub, ws, ref string, age time.Duration) spawnreg.Entry {
	return spawnreg.Entry{TS: time.Now().UTC().Add(-age), Kind: spawnreg.KindLaunchRef, ID: id, SubjectID: sub, WorkspaceID: ws, WorkspaceRef: ref, Result: spawnreg.ResultPending}
}

const seedOwner = 777 // the fake owner pid of a seeded reserve

func aliveOwner(alive bool) func(int) bool {
	return func(pid int) bool {
		if pid == seedOwner {
			return alive
		}
		return claudesession.PidAlive(pid)
	}
}

type spawnEnv struct {
	s   *stubDaemon
	st  *store.Store
	f   *spawnFake
	cwd string
}

func newSpawnEnv(t *testing.T) spawnEnv {
	t.Helper()
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	return spawnEnv{s: s, st: st, f: installSpawnFake(t), cwd: cwd}
}

func (e spawnEnv) spawn(t *testing.T, extra ...string) (string, string, error) {
	t.Helper()
	return runSpawnCLI(t, e.s, append([]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", e.cwd}, extra...)...)
}

// A --new-worktree create can take 15 min before the tab exists: its reserve
// (owner alive, under the cap) stays in progress past PendingWindow.
func TestSpawnNewWorktreeSlowStaysPending(t *testing.T) {
	e := newSpawnEnv(t)
	stubPidAlive(t, aliveOwner(true))
	seedLaunch(t, seedReserve("ln_slow", "sub_p", seedOwner, 10*time.Minute))
	e.f.launchesAs(persoCellDir(t), e.cwd)
	_, _, err := e.spawn(t)
	if err == nil || !strings.Contains(err.Error(), "launch in progress") || !strings.Contains(err.Error(), "a tab not created yet") {
		t.Fatalf("second spawn during a slow create = %v, want refused (accepts the second spawn)", err)
	}
	if len(e.f.created) != 0 {
		t.Fatal("a tab was created")
	}
	if es := registryEntries(t); len(es) != 1 {
		t.Fatalf("registry = %+v, want the seeded reserve alone (nothing failed)", es)
	}
}

func TestSpawnReserveOwnerDeadAccepted(t *testing.T) {
	e := newSpawnEnv(t)
	stubPidAlive(t, aliveOwner(false))
	seedLaunch(t, seedReserve("ln_dead", "sub_p", seedOwner, time.Minute))
	e.f.launchesAs(persoCellDir(t), e.cwd)
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("owner dead before the tab: spawn = %v\n%s", err, errOut)
	}
}

func TestSpawnReservePastCapAccepted(t *testing.T) {
	e := newSpawnEnv(t)
	stubPidAlive(t, aliveOwner(true))
	seedLaunch(t, seedReserve("ln_cap", "sub_p", seedOwner, spawnreg.ReserveCap+time.Minute))
	e.f.launchesAs(persoCellDir(t), e.cwd)
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("reserve past the cap: spawn = %v\n%s", err, errOut)
	}
}

// b2: a relay from the very tab whose launch is still in progress is not
// refused by its own launch, at plan and under the lock.
func TestSpawnPendingExcludesCallerTab(t *testing.T) {
	t.Run("own tab", func(t *testing.T) {
		e := newSpawnEnv(t)
		stubPidAlive(t, aliveOwner(true))
		seedLaunch(t, seedReserve("ln_own", "sub_p", seedOwner, 2*time.Minute), seedRef("ln_own", "sub_p", spawnCallerWS, "workspace:7", time.Minute))
		e.f.launchesAs(persoCellDir(t), e.cwd)
		if _, errOut, err := e.spawn(t); err != nil {
			t.Fatalf("relay from the caller's own pending tab = %v\n%s", err, errOut)
		}
	})
	t.Run("another tab", func(t *testing.T) {
		e := newSpawnEnv(t)
		stubPidAlive(t, aliveOwner(true))
		seedLaunch(t, seedReserve("ln_x", "sub_p", seedOwner, 2*time.Minute), seedRef("ln_x", "sub_p", spawnOtherWS, "workspace:9", time.Minute))
		e.f.launchesAs(persoCellDir(t), e.cwd)
		if _, _, err := e.spawn(t); err == nil || !strings.Contains(err.Error(), "launch in progress in workspace:9") {
			t.Fatalf("another tab's pending launch = %v, want refused", err)
		}
	})
}

func TestPlanSpawnRefusesWhileLaunchPending(t *testing.T) {
	base := func() []spawnreg.Entry {
		return []spawnreg.Entry{seedReserve("ln_a", "sub_p", seedOwner, 3*time.Minute), seedRef("ln_a", "sub_p", spawnOtherWS, "workspace:7", 2*time.Minute)}
	}
	t.Run("pending", func(t *testing.T) {
		e := newSpawnEnv(t)
		stubPidAlive(t, aliveOwner(true))
		seedLaunch(t, base()...)
		_, _, err := e.spawn(t)
		if err == nil || !strings.Contains(err.Error(), "launch in progress in workspace:7") {
			t.Fatalf("err = %v, expected refusal", err)
		}
		if len(e.f.created) != 0 {
			t.Fatal("spawnNewWorkspace called")
		}
	})
	for name, end := range map[string]spawnreg.Entry{
		"failed":   {TS: time.Now().UTC(), Kind: spawnreg.KindLaunchResult, ID: "ln_a", SubjectID: "sub_p", WorkspaceID: spawnOtherWS, WorkspaceRef: "workspace:7", Result: spawnreg.ResultFailed},
		"launched": {TS: time.Now().UTC(), ID: "ln_a", SubjectID: "sub_p", WorkspaceID: spawnOtherWS, WorkspaceRef: "workspace:7", Result: spawnreg.ResultLaunched},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSpawnEnv(t)
			stubPidAlive(t, aliveOwner(true))
			seedLaunch(t, append(base(), end)...)
			e.f.launchesAs(persoCellDir(t), e.cwd)
			if _, errOut, err := e.spawn(t); err != nil {
				t.Fatalf("after a %s end: spawn = %v\n%s", name, err, errOut)
			}
		})
	}
}

func TestRunSpawnExitsGoThroughReservation(t *testing.T) {
	rows := []struct {
		name  string
		setup func(f *spawnFake)
		want  string
	}{
		{"consume fails", func(f *spawnFake) {
			spawnConsumeStamp = func(string) error { return errors.New("record vanished") }
		}, "record vanished"},
		{"create fails without a new tab", func(f *spawnFake) { f.launchErr = errors.New("cmux timeout") }, "cmux_create_failed"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			saved := spawnConsumeStamp
			r.setup(e.f)
			_, _, err := e.spawn(t)
			if err == nil {
				t.Fatal("spawn succeeded")
			}
			es := registryEntries(t)
			if len(es) != 2 {
				t.Fatalf("registry = %+v after %v, want reserve then failed", es, err)
			}
			last := es[len(es)-1]
			if es[0].Kind != spawnreg.KindLaunchReserve || last.Kind != spawnreg.KindLaunchResult ||
				last.Result != spawnreg.ResultFailed || last.ID != es[0].ID || !strings.Contains(last.Detail, r.want) {
				t.Fatalf("registry = %+v, want reserve then failed %q on the same id", es, r.want)
			}
			spawnConsumeStamp, e.f.launchErr, e.f.stamp = saved, nil, "auto"
			e.f.launchesAs(persoCellDir(t), e.cwd)
			if _, errOut, err := e.spawn(t); err != nil {
				t.Fatalf("next spawn after the failure = %v\n%s", err, errOut)
			}
		})
	}
}

// Replaces TestSpawnUnresolvedRefBindsNothing: cmux names workspace:42 but it
// cannot be resolved; the tab diff finds it, the launch goes on bound to it.
func TestResolveFailureAfterCreateKeepsLaunchOpen(t *testing.T) {
	e := newSpawnEnv(t)
	stubCmuxRefs(t, map[string]string{}) // workspace:42 resolves to nothing
	e.f.launchesAs(persoCellDir(t), e.cwd)
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("spawn = %v\n%s", err, errOut)
	}
	es := registryEntries(t)
	var ref *spawnreg.Entry
	for i := range es {
		if es[i].Kind == spawnreg.KindLaunchRef {
			ref = &es[i]
		}
		if es[i].Result == spawnreg.ResultFailed {
			t.Fatalf("failed written: %+v", es)
		}
	}
	if ref == nil || ref.WorkspaceID != spawnNewWS || ref.WorkspaceRef != "workspace:42" {
		t.Fatalf("registry = %+v, want a launch-ref on the diffed tab", es)
	}
	if b, err := e.st.GetCmuxBinding(spawnNewWS); err != nil || b.SubjectID != "sub_p" {
		t.Fatalf("binding = %+v %v", b, err)
	}
}

func TestVerifiedPathClosesReservedID(t *testing.T) {
	e := newSpawnEnv(t)
	e.f.launchesAs(persoCellDir(t), e.cwd)
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("spawn = %v\n%s", err, errOut)
	}
	es := registryEntries(t)
	if entryKinds(es) != "launch-reserve,launch-ref,launched" {
		t.Fatalf("registry kinds = %s", entryKinds(es))
	}
	for _, x := range es {
		if x.ID != es[0].ID {
			t.Fatalf("a record without the reserve's id: %+v", es)
		}
	}
	if es[2].PID != 5000 || es[2].WorkspaceID != spawnNewWS || es[2].Cell != "p" {
		t.Fatalf("launched entry = %+v", es[2])
	}
	if _, ok := spawnreg.Pending(es, "sub_p", time.Now(), spawnreg.PendingOpts{}); ok {
		t.Fatal("still pending after the launched entry")
	}
}

// Guard: a subjectless launch not yet confirmed counts in the burst.
func TestSpawnSubjectlessCountsInBurst(t *testing.T) {
	_, dev, _ := fourCells(t, "wa")
	s, _ := realSubjectDaemonWith(t, nil)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "home", "x"))
	appendLaunches(t, 9, time.Minute, nil)
	if _, _, err := runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt); err != nil {
		t.Fatalf("10th launch = %v, want it to run (pending, unconfirmed)", err)
	}
	f.stamp = "auto" // a fresh permission record for the second call
	_, _, err := runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt)
	if err == nil || !strings.Contains(err.Error(), "reads as a runaway") {
		t.Fatalf("11th launch = %v, want the burst refusal", err)
	}
}

// The race of the Check: A launches between B's plan and B's reservation; the
// live set re-read under the lock sees A's Claude.
func TestReserveRefusesLaunchedBetweenPlanAndReserve(t *testing.T) {
	t.Run("live re-read", func(t *testing.T) {
		e := newSpawnEnv(t)
		spawnGroupOf = func(string) (string, error) {
			seedLaunch(t, seedReserve("ln_A", "sub_p", os.Getpid(), time.Second),
				seedRef("ln_A", "sub_p", spawnOtherWS, "workspace:9", time.Second),
				spawnreg.Entry{TS: time.Now().UTC(), ID: "ln_A", SubjectID: "sub_p", WorkspaceID: spawnOtherWS, WorkspaceRef: "workspace:9", Result: spawnreg.ResultLaunched})
			e.f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
			return e.f.groupRef, nil
		}
		_, _, err := e.spawn(t)
		if err == nil || !strings.Contains(err.Error(), "already has a live session") {
			t.Fatalf("B = %v, want refused under the lock", err)
		}
		for _, x := range registryEntries(t) {
			if x.ID != "ln_A" {
				t.Fatalf("B reserved: %+v", x)
			}
		}
		if len(e.f.created) != 0 {
			t.Fatal("B created a tab")
		}
	})
	t.Run("processes unreadable under the lock", func(t *testing.T) {
		e := newSpawnEnv(t)
		spawnGroupOf = func(string) (string, error) {
			claudeProcs = func() ([]claudesession.Proc, error) { return nil, errors.New("ps failed") }
			return e.f.groupRef, nil
		}
		_, _, err := e.spawn(t)
		if err == nil || !strings.Contains(err.Error(), "list Claude processes") || exitCode(err) != exitTempFail {
			t.Fatalf("err = %v (exit %d), want a retryable refusal", err, exitCode(err))
		}
		if es := registryEntries(t); len(es) != 0 {
			t.Fatalf("written: %+v", es)
		}
	})
}

// Remark b: « launch in progress » exits 1 — wait for the confirmation, never loop.
func TestSpawnLaunchInProgressExitsOne(t *testing.T) {
	t.Run("plan", func(t *testing.T) {
		e := newSpawnEnv(t)
		stubPidAlive(t, aliveOwner(true))
		seedLaunch(t, seedReserve("ln_p", "sub_p", seedOwner, time.Minute), seedRef("ln_p", "sub_p", spawnOtherWS, "workspace:9", 30*time.Second))
		_, _, err := e.spawn(t)
		if _, retry := retryAfter(err); err == nil || retry || exitCode(err) != 1 {
			t.Fatalf("err = %v (exit %d), expected refusal exit 1", err, exitCode(err))
		}
	})
	t.Run("lock", func(t *testing.T) {
		e := newSpawnEnv(t)
		stubPidAlive(t, aliveOwner(true))
		spawnGroupOf = func(string) (string, error) {
			seedLaunch(t, seedReserve("ln_l", "sub_p", seedOwner, time.Second), seedRef("ln_l", "sub_p", spawnOtherWS, "workspace:9", 0))
			return e.f.groupRef, nil
		}
		_, _, err := e.spawn(t)
		if _, retry := retryAfter(err); err == nil || !strings.Contains(err.Error(), "launch in progress") || retry || exitCode(err) != 1 {
			t.Fatalf("err = %v (exit %d), expected refusal exit 1", err, exitCode(err))
		}
		if len(e.f.created) != 0 {
			t.Fatal("created a tab")
		}
	})
}

// Guard: the retryable refusals keep exit 75.
func TestSpawnRetryableRefusalsStill75(t *testing.T) {
	rows := []struct {
		name    string
		setup   func(f *spawnFake)
		want    string
		created int
		failed  bool
	}{
		{"unreadable environment", func(f *spawnFake) { f.addProc(8000, "/x/bin/claude", ""); f.badEnv[8000] = true }, "unreadable environment", 0, false},
		{"lock not taken", func(*spawnFake) {
			spawnRegistryLock = func(*spawnreg.Registry, io.Writer, time.Duration) (func(), error) {
				return nil, errors.New("flock: interrupted")
			}
		}, "flock: interrupted", 0, false},
		{"tabs unlisted before the create", func(f *spawnFake) {
			f.tabsErr = func(n int) error {
				if n == 1 {
					return errors.New("socket gone")
				}
				return nil
			}
		}, "cmux_unreachable", 0, true},
		{"create fails without a new tab", func(f *spawnFake) { f.launchErr = errors.New("cmux timeout") }, "cmux new-workspace failed", 0, true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			r.setup(e.f)
			_, _, err := e.spawn(t)
			if err == nil || !strings.Contains(err.Error(), r.want) || exitCode(err) != exitTempFail {
				t.Fatalf("err = %v (exit %d), want %q exit 75", err, exitCode(err), r.want)
			}
			if len(e.f.created) != r.created {
				t.Fatalf("created %d", len(e.f.created))
			}
			failed := false
			for _, x := range registryEntries(t) {
				failed = failed || x.Result == spawnreg.ResultFailed
			}
			if failed != r.failed {
				t.Fatalf("failed written = %v, want %v", failed, r.failed)
			}
		})
	}
}

// Écart 2: a launch failed with a tab keeps its identity; a Claude that later
// shows in that tab still blocks a second spawn of the subject.
func TestFailedLaunchTabStaysDuplicateCandidate(t *testing.T) {
	e := newSpawnEnv(t)
	if err := e.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnNewWS, SubjectID: "sub_t", Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	e.f.launchesAs("/elsewhere", e.cwd) // wrong account
	if _, _, err := e.spawn(t); err == nil || !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR") {
		t.Fatalf("first spawn = %v, want the wrong-account failure", err)
	}
	es := registryEntries(t)
	last := es[len(es)-1]
	if last.Kind != spawnreg.KindLaunchResult || last.SubjectID != "sub_p" || last.WorkspaceID != spawnNewWS || last.WorkspaceRef != "workspace:42" {
		t.Fatalf("failed record = %+v, want it to carry the identity", last)
	}
	e.f.stamp = "auto"
	if _, _, err := e.spawn(t); err == nil || !strings.Contains(err.Error(), "already has a live session in workspace") {
		t.Fatalf("second spawn = %v, want the live-session refusal", err)
	}
}

type fakeTabLister struct {
	windows []cmux.Window
	ws      map[string][]cmux.WorkspaceInfo
}

func (l *fakeTabLister) ListWindows() ([]cmux.Window, error) { return l.windows, nil }
func (l *fakeTabLister) ListWorkspaces(id string) ([]cmux.WorkspaceInfo, error) {
	return l.ws[id], nil
}

func TestSpawnListTabsUppercasesIDs(t *testing.T) {
	t.Parallel()
	old := "aaaaaaaa-1111-4111-8111-111111111111"
	nw := "bbbbbbbb-2222-4222-8222-222222222222"
	l := &fakeTabLister{windows: []cmux.Window{{ID: "W1"}, {ID: "W2"}}, ws: map[string][]cmux.WorkspaceInfo{"W1": {{ID: old, Ref: "workspace:1"}}}}
	tabs, err := listCmuxTabsWith(l)
	if err != nil || len(tabs) != 1 || tabs[0].ID != strings.ToUpper(old) || tabs[0].Ref != "workspace:1" {
		t.Fatalf("tabs = %+v %v", tabs, err)
	}
	reg := spawnreg.Open(filepath.Join(t.TempDir(), "spawns.jsonl"))
	var got spawnreg.Tab
	_, err = reg.WithReservation(spawnreg.ReserveRequest{Tabs: func() ([]spawnreg.Tab, error) { return listCmuxTabsWith(l) }}, func(ln *spawnreg.Launch) error {
		var cerr error
		got, cerr = ln.Create(func() (spawnreg.Tab, error) {
			l.ws["W2"] = []cmux.WorkspaceInfo{{ID: nw, Ref: "workspace:2"}}
			return spawnreg.Tab{}, errors.New("no id")
		})
		return cerr
	})
	if err != nil || got.ID != strings.ToUpper(nw) || got.Ref != "workspace:2" {
		t.Fatalf("diffed tab = %+v %v, want the new one in upper case", got, err)
	}
}

func TestSpawnHelpDescribesPending(t *testing.T) {
	t.Parallel()
	long := sessionSpawnCmd().Long
	i := strings.Index(long, "Refuses, creating nothing")
	if i < 0 || !strings.Contains(long[i:], "launch in progress") || strings.Contains(long, "queues on the registry lock") {
		t.Fatalf("help:\n%s", long)
	}
}

// Two CLI callers on the same subject, both planned on an empty registry:
// one reservation, one tab, left pending; the other is refused as launch in progress.
func TestReserveTwoCLICallersRace(t *testing.T) {
	e := newSpawnEnv(t)
	spawnConsumeStamp = func(string) error { e.f.mu.Lock(); defer e.f.mu.Unlock(); e.f.consumed++; return nil } // both pass the permission check
	var mu sync.Mutex
	planned := 0
	both := make(chan struct{})
	spawnGroupOf = func(string) (string, error) {
		mu.Lock()
		planned++
		if planned == 2 {
			close(both)
		}
		mu.Unlock()
		select {
		case <-both:
		case <-time.After(10 * time.Second):
			return "", errors.New("barrier: the other caller never planned")
		}
		return e.f.groupRef, nil
	}
	errs := make([]error, 2)
	withStubDaemon(t, e.s, func() {
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				cmd := sessionCmd()
				silenceUsageOnRunE(cmd)
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs([]string{"spawn", "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", e.cwd})
				errs[i] = cmd.Execute()
			}(i)
		}
		wg.Wait()
	})
	reserves := 0
	for _, x := range registryEntries(t) {
		if x.Kind == spawnreg.KindLaunchReserve {
			reserves++
		}
	}
	if reserves != 1 || len(e.f.created) != 1 {
		t.Fatalf("reserves = %d, created = %d, want one of each (errs %v)", reserves, len(e.f.created), errs)
	}
	var inProgress, pending int
	for _, err := range errs {
		switch {
		case err != nil && strings.Contains(err.Error(), "launch in progress") && exitCode(err) == 1:
			inProgress++
		case err == nil:
			pending++ // nothing starts in the fake tab: pending, exit 0
		}
	}
	if inProgress != 1 || pending != 1 || len(e.f.armed) != 1 {
		t.Fatalf("errs = %v, armed %d, want one launch in progress (exit 1), one pending (exit 0) and the watcher armed once", errs, len(e.f.armed))
	}
}

// --- T3 (sub_x000000000000015): a slow launch is pending, the watcher confirms it ---

// slowClaude: the new tab's Claude appears after `after`, past the verify timeout.
func (f *spawnFake) slowClaude(configDir, cwd string, after time.Duration) {
	f.launch = func(cmux.WorkspaceSpec) {
		time.AfterFunc(after, func() {
			f.addProc(5000, "/x/bin/claude --settings /t/cmux-claude-settings.b /example-skill reprends",
				"CMUX_WORKSPACE_ID="+spawnNewWS+" CLAUDE_CONFIG_DIR="+configDir+" PWD="+cwd)
		})
	}
}

type spawnJSON struct {
	State        string   `json:"state"`
	LaunchID     string   `json:"launch_id"`
	WorkspaceRef string   `json:"workspace_ref"`
	WorkspaceID  string   `json:"workspace_id"`
	SubjectID    string   `json:"subject_id"`
	Bound        *bool    `json:"bound"`
	Warnings     []string `json:"warnings"`
	CallerTitle  *string  `json:"caller_title"`
	PID          int      `json:"pid"`
}

func decodeSpawn(t *testing.T, out string) spawnJSON {
	t.Helper()
	var v spawnJSON
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return v
}

func slowPendingSpawn(t *testing.T) (spawnEnv, spawnJSON) {
	t.Helper()
	testutil.SerialOnly(t) // swaps spawnVerifyTimeout
	e := newSpawnEnv(t)
	spawnVerifyTimeout = 50 * time.Millisecond
	e.f.slowClaude(persoCellDir(t), e.cwd, 150*time.Millisecond)
	out, errOut, err := e.spawn(t, "--json")
	if err != nil {
		t.Fatalf("spawn = %v, want a pending launch (exit 0)\n%s\n%s", err, out, errOut)
	}
	return e, decodeSpawn(t, out)
}

func TestExecuteSpawnSlowClaudeIsPending(t *testing.T) {
	e, v := slowPendingSpawn(t)
	es := registryEntries(t)
	if v.State != "pending" || v.LaunchID != es[0].ID || es[0].Kind != spawnreg.KindLaunchReserve {
		t.Fatalf("result = %+v, registry %+v: want pending with the reserve's id", v, es)
	}
	if b, err := e.st.GetCmuxBinding(spawnNewWS); err != nil || b.SubjectID != "sub_p" {
		t.Fatalf("the pending tab must stay bound: %+v %v", b, err)
	}
}

func TestNotSeenKeepsReservedID(t *testing.T) {
	e, _ := slowPendingSpawn(t)
	es := registryEntries(t)
	if entryKinds(es) != "launch-reserve,launch-ref" {
		t.Fatalf("registry kinds = %s, want one reserve and its launch-ref", entryKinds(es))
	}
	if es[1].ID != es[0].ID || es[1].WorkspaceRef != "workspace:42" {
		t.Fatalf("launch-ref = %+v", es[1])
	}
	if len(e.f.armed) != 1 {
		t.Fatalf("watcher armed %d times, want once (by the Handoff)", len(e.f.armed))
	}
	run := e.f.armed[0]
	if run.ID != es[0].ID || run.SubjectID != "sub_p" || run.WorkspaceID != spawnNewWS || run.WorkspaceRef != "workspace:42" ||
		run.Cell != "p" || run.ConfigDir != persoCellDir(t) || run.Cwd != e.cwd || run.Prompt != spawnPrompt || run.Mode != "auto" || run.RelayFrom != "" {
		t.Fatalf("armed run = %+v", run)
	}
	if !slices.Contains(run.Before, 4242) {
		t.Fatalf("run.Before = %v, want the PIDs listed before the create", run.Before)
	}
	if want := es[1].TS.Add(spawnConfirmTimeout); run.Deadline.Sub(want).Abs() > time.Millisecond {
		t.Fatalf("deadline = %s, want the launch-ref's TS + %s = %s", run.Deadline, spawnConfirmTimeout, want)
	}
}

func TestResolveFailureArmsWatcher(t *testing.T) {
	e := newSpawnEnv(t)
	stubCmuxRefs(t, map[string]string{}) // workspace:42 resolves to nothing
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("spawn = %v\n%s", err, errOut)
	}
	es := registryEntries(t)
	if len(e.f.armed) != 1 || e.f.armed[0].ID != es[0].ID || e.f.armed[0].WorkspaceID != spawnNewWS {
		t.Fatalf("armed = %+v, want the reserve's id on the diffed tab %s", e.f.armed, spawnNewWS)
	}
}

func TestPrintSpawnResultPending(t *testing.T) {
	t.Parallel()
	p := spawnPlan{Subject: pigtail.Subject{ID: "sub_p", Title: "perso one"}, CallerWSRef: "workspace:7"}
	r := spawnResult{State: "pending", LaunchID: "ln_abc", WorkspaceRef: "workspace:42", WorkspaceID: spawnNewWS, Bound: true, Tail: "too old"}
	var b bytes.Buffer
	if err := printSpawnResult(&b, p, r, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "launch pending in workspace:42 — left open, bound to sub_p") || !strings.Contains(b.String(), "too old") {
		t.Fatalf("text = %s", b.String())
	}
	b.Reset()
	if err := printSpawnResult(&b, p, r, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"state":"pending"`) || !strings.Contains(b.String(), `"launch_id":"ln_abc"`) {
		t.Fatalf("json = %s", b.String())
	}
}

func TestSpawnPendingRelayNothingWarns(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t) // nothing starts
	_ = spawnreg.Open(spawnreg.DefaultPath()).Append(spawnreg.Entry{TS: time.Now().Add(-time.Hour), SubjectID: "sub_other", WorkspaceID: "B1", Result: "launched"})
	f.addProc(7500, "/x/bin/claude", "CMUX_WORKSPACE_ID=B1")
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--relay", "--json")
	if err != nil {
		t.Fatalf("err = %v, want pending (exit 0)", err)
	}
	v := decodeSpawn(t, out)
	if v.State != "pending" || !strings.Contains(strings.Join(v.Warnings, "\n"), "--relay passed nothing") {
		t.Fatalf("json = %+v", v)
	}
}

// --relay: no watcher, the CLI waits for the confirmation up to the deadline.
func TestExecuteSpawnRelayWaitsPastVerifyTimeout(t *testing.T) {
	s, f := relayFixture(t)
	cwd := t.TempDir()
	spawnVerifyTimeout = 50 * time.Millisecond
	f.slowClaude(persoCellDir(t), cwd, 150*time.Millisecond)
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--relay")
	if err != nil {
		t.Fatalf("relay = %v\n%s", err, errOut)
	}
	if n := strings.Count(errOut, "still launching… waiting for Claude in workspace:42"); n != 1 {
		t.Fatalf("%d « still launching » lines, want 1:\n%s", n, errOut)
	}
	if !strings.Contains(out, "relay: workspace:42 takes over from this session — end this one now\n") {
		t.Fatalf("result lacks the relay line:\n%s", out)
	}
	if len(f.armed) != 0 {
		t.Fatalf("a watcher was armed on a --relay launch: %+v", f.armed)
	}
	es := registryEntries(t)
	last := es[len(es)-1]
	if last.Kind != "" || last.Result != spawnreg.ResultLaunched || last.RelayFrom != spawnCallerWS || last.PID != 5000 {
		t.Fatalf("last record = %+v, want launched with relay_from", last)
	}
}

func TestExecuteSpawnRelayFailsAtDeadline(t *testing.T) {
	s, f := relayFixture(t)
	spawnVerifyTimeout = 20 * time.Millisecond
	spawnConfirmTimeout = 200 * time.Millisecond
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--relay")
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d), want a failure, exit 1", err, exitCode(err))
	}
	if strings.Contains(out, "relay:") {
		t.Fatalf("a relay line on a failed launch:\n%s", out)
	}
	if len(f.armed) != 0 {
		t.Fatalf("a watcher was armed on a --relay launch: %+v", f.armed)
	}
	var reserve, last spawnreg.Entry
	for _, x := range registryEntries(t) {
		if x.Kind == spawnreg.KindLaunchReserve {
			reserve = x
		}
		last = x
	}
	if last.Kind != spawnreg.KindLaunchResult || last.Result != spawnreg.ResultFailed || last.ID != reserve.ID || last.WorkspaceID != spawnNewWS {
		t.Fatalf("last record = %+v, want failed with the reserve's id and the tab", last)
	}
}

// Guard: a pending result never tells the caller to end.
func TestRelayPendingNeverPrintsRelayLine(t *testing.T) {
	t.Parallel()
	p := spawnPlan{Subject: pigtail.Subject{ID: "sub_p"}, RelayFrom: spawnCallerWS}
	var b bytes.Buffer
	if err := printSpawnResult(&b, p, spawnResult{State: "pending", WorkspaceRef: "workspace:42"}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "end this one now") {
		t.Fatalf("pending printed the relay line:\n%s", b.String())
	}
}

// The --relay wait runs past the base budget: the daemon calls after it keep a live ctx.
func TestRunSpawnRelayTrustPillAfterBaseBudget(t *testing.T) {
	s, f := relayFixture(t)
	cwd := t.TempDir()
	spawnBaseBudget = 400 * time.Millisecond
	spawnVerifyTimeout = 20 * time.Millisecond
	f.screen = "Do you trust the files in this folder?"
	f.slowClaude(persoCellDir(t), cwd, 900*time.Millisecond)
	var ctxErr error
	marked := false
	spawnMarkTrust = func(ctx context.Context, _ *pigtail.Client, _ string, _ int) error {
		marked, ctxErr = true, ctx.Err()
		return ctx.Err()
	}
	if _, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--relay"); err != nil {
		t.Fatalf("relay = %v\n%s", err, errOut)
	}
	if !marked || ctxErr != nil {
		t.Fatalf("trust pill marked=%v ctx err=%v, want posted with a live ctx", marked, ctxErr)
	}
}

func TestSpawnHelpRelayBashTimeout(t *testing.T) {
	t.Parallel()
	long := strings.Join(strings.Fields(sessionSpawnCmd().Long), " ")
	want := "With --relay (with or without --new-worktree), the confirmation wait adds up to 5 minutes (about 7 in all for the relay alone): run the spawn in the background, or use the maximum Bash timeout, 600000 ms; past it the call moves to the background and is not killed, as above."
	if !strings.Contains(long, want) {
		t.Fatalf("help lacks the --relay Bash paragraph:\n%s", long)
	}
	for _, m := range regexp.MustCompile(`\d+`).FindAllString(long, -1) {
		if n, _ := strconv.Atoi(m); n > 600000 {
			t.Fatalf("help names %d, above the 600000 ms Bash maximum", n)
		}
	}
}

func TestSpawnWatcherArmFailureWarns(t *testing.T) {
	e := newSpawnEnv(t)
	e.f.armErr = errors.New("worker did not arm")
	out, errOut, err := e.spawn(t, "--json")
	if err != nil {
		t.Fatalf("err = %v, want pending (exit 0)", err)
	}
	v := decodeSpawn(t, out)
	if v.State != "pending" || !strings.Contains(strings.Join(v.Warnings, "\n"), "worker did not arm") || !strings.Contains(errOut, "worker did not arm") {
		t.Fatalf("json = %+v\nstderr %s", v, errOut)
	}
	e2 := newSpawnEnv(t)
	e2.f.armErr = errors.New("worker did not arm")
	text, _, err := e2.spawn(t)
	if err != nil || !strings.Contains(text, "worker did not arm") {
		t.Fatalf("text = %v\n%s, want the arming failure in the result itself", err, text)
	}
}

// Guard: the fixture alone keeps every runSpawn test off the real watcher.
func TestSpawnFixtureNeverExecsRealWatcher(t *testing.T) {
	e := newSpawnEnv(t) // nothing starts: the Handoff arms
	if _, errOut, err := e.spawn(t); err != nil {
		t.Fatalf("spawn = %v\n%s", err, errOut)
	}
	if len(e.f.armed) != 1 {
		t.Fatalf("the fixture's spawnArmWatch got %d runs, want 1", len(e.f.armed))
	}
}

// --- T4: the burst counts launches, never their follow-up records ---

// launch-ref, launch-armed and launch-result never count; a reserve and its
// launched entry (same id) are one launch; a failed launch with a tab is one.
func TestSpawnBurstIgnoresPendingFollowups(t *testing.T) {
	stubPidAlive(t, aliveOwner(true))
	now := time.Now().UTC()
	at := func(ago time.Duration) time.Time { return now.Add(-ago) }
	es := []spawnreg.Entry{
		{TS: at(5 * time.Minute), Kind: spawnreg.KindLaunchReserve, ID: "ln_a", SubjectID: "sub_p", OwnerPID: seedOwner, Result: spawnreg.ResultPending},
		{TS: at(4 * time.Minute), Kind: spawnreg.KindLaunchRef, ID: "ln_a", SubjectID: "sub_p", WorkspaceID: spawnNewWS, WorkspaceRef: "workspace:42", Result: spawnreg.ResultPending},
		{TS: at(4 * time.Minute), Kind: spawnreg.KindLaunchArmed, ID: "ln_a", SubjectID: "sub_p", WorkspaceID: spawnNewWS, WorkspaceRef: "workspace:42", Result: spawnreg.ResultArmed},
		{TS: at(3 * time.Minute), ID: "ln_a", SubjectID: "sub_p", WorkspaceID: spawnNewWS, WorkspaceRef: "workspace:42", Result: spawnreg.ResultLaunched, PID: 5000},
		{TS: at(3 * time.Minute), Kind: spawnreg.KindLaunchReserve, ID: "ln_b", SubjectID: "sub_q", OwnerPID: seedOwner, Result: spawnreg.ResultPending},
		{TS: at(2 * time.Minute), Kind: spawnreg.KindLaunchRef, ID: "ln_b", SubjectID: "sub_q", WorkspaceID: spawnOtherWS, WorkspaceRef: "workspace:43", Result: spawnreg.ResultPending},
		{TS: at(2 * time.Minute), Kind: spawnreg.KindLaunchArmed, ID: "ln_b", SubjectID: "sub_q", WorkspaceID: spawnOtherWS, WorkspaceRef: "workspace:43", Result: spawnreg.ResultArmed},
		{TS: at(time.Minute), Kind: spawnreg.KindLaunchResult, ID: "ln_b", SubjectID: "sub_q", WorkspaceID: spawnOtherWS, WorkspaceRef: "workspace:43", Result: spawnreg.ResultFailed, Detail: "no Claude process appeared"},
	}
	b := burstAt(es, now)
	if b.Recent != 2 || !b.Oldest.Equal(es[0].TS) {
		t.Fatalf("burst = %+v, want 2 launches from the first reserve (%s)", b, es[0].TS)
	}
}

// After the create, a tab that cannot be told apart (several new tabs, or the
// listing failing after the create) is exit 1, never retried — a retry could
// open a second tab —, binds nothing, and closes the launch failed with its cause.
func TestSpawnAmbiguousTabExitsOneNothingBound(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *spawnFake)
		cause string
	}{
		{"several new tabs", func(f *spawnFake) {
			f.extraTabs = []spawnreg.Tab{{ID: spawnOtherWS, Ref: "workspace:43"}}
		}, "several new tabs, binding not guessed"},
		{"listing fails after the create", func(f *spawnFake) {
			f.tabsErr = func(call int) error {
				if call >= 2 {
					return errors.New("cmux: socket closed")
				}
				return nil
			}
		}, "tab state unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			stubCmuxRefs(t, map[string]string{}) // workspace:42 resolves to nothing: the diff decides
			c.setup(e.f)
			_, _, err := e.spawn(t)
			if _, retry := retryAfter(err); err == nil || exitCode(err) != 1 || retry {
				t.Fatalf("err = %v (exit %d), want exit 1, not retryable", err, exitCode(err))
			}
			if !strings.Contains(err.Error(), "look at cmux; nothing bound") || !strings.Contains(err.Error(), c.cause) {
				t.Fatalf("err = %v, want « %s » and « look at cmux; nothing bound »", err, c.cause)
			}
			for _, ws := range []string{spawnNewWS, spawnOtherWS} {
				if b, bErr := e.st.GetCmuxBinding(ws); bErr == nil && b != nil {
					t.Fatalf("%s bound: %+v", ws, b)
				}
			}
			if len(e.f.armed) != 0 {
				t.Fatalf("a watcher was armed: %+v", e.f.armed)
			}
			es := registryEntries(t)
			last := es[len(es)-1]
			if last.Kind != spawnreg.KindLaunchResult || last.Result != spawnreg.ResultFailed || last.ID != es[0].ID || !strings.Contains(last.Detail, c.cause) {
				t.Fatalf("registry = %+v, want the launch closed failed with its cause", es)
			}
		})
	}
}
