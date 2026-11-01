package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	rwOldPID = 4242
	rwFgPID  = 4343
	rwNewPID = 5151
	// rwRunWS is the run's workspace; the worker's environment names another
	// (spawnCallerWS): every pill must target the run's (#36).
	rwRunWS = "22222222-BBBB-4BBB-8BBB-222222222222"
)

// rwPillCall is one sidebar call of the worker: req nil = clear.
type rwPillCall struct {
	ws  string
	req *pigtail.CmuxRespawnRequest
}

func (p rwPillCall) String() string {
	if p.req == nil {
		return "clear"
	}
	if p.req.Wait != "" {
		return p.req.State + " " + p.req.Wait
	}
	return p.req.State
}

// rwFake is the worker's whole world: the old Claude's state, its tab, its
// shells, what appears after the kill. No real process is ever signalled.
type rwFake struct {
	t   *testing.T
	mu  sync.Mutex
	run respawnRun
	dir string

	fgPolls                     int // the foreground reads alive this many times
	fgCalls                     int
	fgAtKill                    int
	status                      func(f *rwFake) claudesession.Native // the old Claude's sessions/<pid>.json
	screen                      func(f *rwFake, call int) (string, error)
	screenCalls                 int
	shells                      []int
	claudeAlive                 bool
	exits                       bool // the old Claude exits on SIGTERM
	appears                     bool // a fresh Claude appears after the exit
	newEnv                      string
	newArgs                     string
	newSID                      string
	kills                       int
	killAt                      time.Time
	idleSince                   time.Time
	promptAtKill, handoffAtKill string
	sidAtKill                   string // <key>.sid at the kill: no SessionEnd hook runs in the fake
	bindCalls                   []string
	bindErr                     error
	attachCalls                 int
	attachErr                   error
	pills                       []rwPillCall
	pillErr                     error
	killsAtPill                 []int // f.kills at each sidebar call
	cleanup                     func()
	onProcs                     func(f *rwFake)
	// Shell relaunch: ShellAtPrompt answers « not at its prompt »
	// shellPolls times first; shellScreen is the tab after the kill; typed is
	// what rwSendText (quoted) and rwSendLine sent, in order.
	shellPolls  int
	lineErr     error // rwSendLine alone fails, after a \x15 that went through
	shellErr    error
	sendErr     error
	typed       []string
	shellScreen string
	// Native state "shell": the Bash-tool shells rwShellInfo lists,
	// or its listing error (shellErr above is the shell relaunch's probe).
	shellInfo    []claudesession.ShellInfo
	shellInfoErr error
	// What the daemon reports for a final state: notice, or none at all
	// (noticeNil: a daemon older than the notice, 204).
	notice    *pigtail.CmuxRespawnNotice
	noticeNil bool
	// Alerts a successful run resolved (subjectID|cause), and the error
	// rwResolveAlerts returns.
	resolved   []string
	resolveErr error
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "claudesession", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newRWFake(t *testing.T) *rwFake {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	cfg, cwd := filepath.Join(dir, "cfg"), filepath.Join(dir, "wt")
	for _, d := range []string{cfg, cwd} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The worker inherits its caller's pane variables: none may steer a pill.
	t.Setenv("CMUX_WORKSPACE_ID", spawnCallerWS)
	t.Setenv("CMUX_SURFACE_ID", "S-CALLER")
	f := &rwFake{t: t, dir: dir, claudeAlive: true, exits: true, appears: true, newSID: "sid-fresh",
		notice: &pigtail.CmuxRespawnNotice{Journal: "ok", Wait: "ok", Notice: "ok", Pill: "ok"},
		run: respawnRun{ID: "r-test", SubjectID: "sub_p", SubjectTitle: "perso one", Cell: "jp", ConfigDir: cfg, Cwd: cwd,
			ClaudePID: rwOldPID, SessionID: "sid-old", SurfaceID: "S1", SurfaceRef: "surface:7", WorkspaceID: rwRunWS,
			RelaunchFile: filepath.Join(dir, "k.sid"), Relaunch: relaunchLauncher, Mode: "plan", Prompt: "/example-skill resume sub_p", LaunchProto: 2,
			IdleTimeout: 2 * time.Second, ForegroundPID: rwFgPID}}
	idle := fixture(t, "screen-idle-empty.txt")
	f.status = func(*rwFake) claudesession.Native {
		return claudesession.Native{SessionID: "sid-old", Status: "idle", StatusUpdatedAt: time.Now().Add(-time.Hour).UnixMilli()}
	}
	f.screen = func(*rwFake, int) (string, error) { return idle, nil }
	f.newEnv = "CMUX_SURFACE_ID=S1 CLAUDE_CONFIG_DIR=" + cfg + " PWD=" + cwd
	f.newArgs = "/x/bin/claude --permission-mode plan " + f.run.Prompt

	saved := []any{rwNative, rwReadScreen, rwBashShells, rwPidAlive, rwKill, rwProcs, rwEnviron, rwSetBinding,
		rwWrapper, rwAttach, rwInterrupt, rwPoll, rwSettle, rwExitTimeout, rwSeenTimeout, rwSidTimeout, rwSidebar, rwPillRefresh,
		rwShellAtPrompt, rwSendText, rwSendLine, rwShellTimeout, rwShellInfo, rwResolveAlerts}
	t.Cleanup(func() {
		rwNative = saved[0].(func(string, int) (claudesession.Native, bool))
		rwReadScreen = saved[1].(func(string) (string, error))
		rwBashShells = saved[2].(func(int) ([]int, error))
		rwPidAlive = saved[3].(func(int) bool)
		rwKill = saved[4].(func(int) error)
		rwProcs = saved[5].(func() ([]claudesession.Proc, error))
		rwEnviron = saved[6].(func(int) (string, error))
		rwSetBinding = saved[7].(func(string, string, string, string, string, string) error)
		rwWrapper = saved[8].(func(io.Writer) string)
		rwAttach = saved[9].(func(context.Context, *pigtail.Client, string, string) error)
		rwInterrupt = saved[10].(func(func()) func())
		rwPoll, rwSettle, rwExitTimeout, rwSeenTimeout, rwSidTimeout = saved[11].(time.Duration), saved[12].(time.Duration),
			saved[13].(time.Duration), saved[14].(time.Duration), saved[15].(time.Duration)
		rwSidebar = saved[16].(func(context.Context, *pigtail.Client, string, *pigtail.CmuxRespawnRequest) (*pigtail.CmuxRespawnNotice, error))
		rwPillRefresh = saved[17].(time.Duration)
		rwShellAtPrompt = saved[18].(func(string) (bool, error))
		rwSendText = saved[19].(func(string, string) error)
		rwSendLine = saved[20].(func(string, string) error)
		rwShellTimeout = saved[21].(time.Duration)
		rwShellInfo = saved[22].(func(int) ([]claudesession.ShellInfo, error))
		rwResolveAlerts = saved[23].(func(context.Context, *pigtail.Client, string, string) error)
	})
	savedStandby := rwInStandby
	t.Cleanup(func() { rwInStandby = savedStandby })
	rwInStandby = func(func(string) string, string) bool { return false } // never the real marker dir
	rwPoll, rwSettle, rwExitTimeout, rwSeenTimeout, rwSidTimeout = time.Millisecond, 0, 30*time.Millisecond, 30*time.Millisecond, 30*time.Millisecond
	rwShellTimeout = 30 * time.Millisecond

	rwNative = func(cfg string, pid int) (claudesession.Native, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if cfg != f.run.ConfigDir {
			return claudesession.Native{}, false
		}
		switch pid {
		case rwOldPID:
			if !f.claudeAlive {
				return claudesession.Native{}, false
			}
			return f.status(f), true
		case rwNewPID:
			if f.freshUp() {
				return claudesession.Native{SessionID: f.newSID, Status: "busy"}, true
			}
		}
		return claudesession.Native{}, false
	}
	rwReadScreen = func(surface string) (string, error) {
		f.mu.Lock()
		f.screenCalls++
		n := f.screenCalls
		f.mu.Unlock()
		if surface != "S1" {
			t.Errorf("screen read on %q, want the surface UUID S1", surface)
		}
		return f.screen(f, n)
	}
	rwBashShells = func(int) ([]int, error) { return f.shells, nil }
	rwShellInfo = func(int) ([]claudesession.ShellInfo, error) { return f.shellInfo, f.shellInfoErr }
	rwPidAlive = func(pid int) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch pid {
		case rwOldPID:
			return f.claudeAlive
		case rwFgPID:
			f.fgCalls++
			return f.fgCalls <= f.fgPolls
		}
		return false
	}
	rwKill = func(pid int) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if pid != rwOldPID {
			t.Errorf("SIGTERM to pid %d, want the old Claude %d", pid, rwOldPID)
		}
		f.kills++
		f.killAt = time.Now()
		f.fgAtKill = f.fgCalls
		if f.run.Relaunch != relaunchShell {
			p, _ := os.ReadFile(f.promptPath())
			h, _ := os.ReadFile(f.handoffPath())
			s, _ := os.ReadFile(f.run.RelaunchFile)
			f.promptAtKill, f.handoffAtKill, f.sidAtKill = string(p), string(h), string(s)
		}
		if f.exits {
			f.claudeAlive = false
			if f.appears && f.run.Relaunch != relaunchShell { // the launcher consumes the files (_c_run removes all three before claude)
				_ = os.Remove(f.promptPath())
				_ = os.Remove(f.handoffPath())
				_ = os.Remove(f.run.RelaunchFile)
			}
		}
		return nil
	}
	rwProcs = func() ([]claudesession.Proc, error) {
		if f.onProcs != nil {
			f.onProcs(f)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var ps []claudesession.Proc
		if f.claudeAlive {
			ps = append(ps, claudesession.Proc{PID: rwOldPID, Args: "/x/bin/claude --permission-mode plan"})
		}
		ps = append(ps, claudesession.Proc{PID: 9000, Args: "/x/bin/claude"}) // another tab's session
		if f.freshUp() {
			ps = append(ps, claudesession.Proc{PID: rwNewPID, Args: f.newArgs})
		}
		return ps, nil
	}
	rwEnviron = func(pid int) (string, error) {
		switch pid {
		case rwNewPID:
			return f.newEnv, nil
		case 9000:
			return "CMUX_SURFACE_ID=OTHER", nil
		}
		return "CMUX_SURFACE_ID=S1", nil
	}
	rwSetBinding = func(ref, cwd, sid, cfg, wrapper, mode string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bindCalls = append(f.bindCalls, strings.Join([]string{ref, cwd, sid, cfg, wrapper, mode}, "|"))
		return f.bindErr
	}
	rwSidebar = func(_ context.Context, _ *pigtail.Client, ws string, req *pigtail.CmuxRespawnRequest) (*pigtail.CmuxRespawnNotice, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var cp *pigtail.CmuxRespawnRequest
		if req != nil {
			c := *req
			cp = &c
		}
		f.pills = append(f.pills, rwPillCall{ws, cp})
		f.killsAtPill = append(f.killsAtPill, f.kills)
		if f.pillErr != nil || req == nil || req.State == pigtail.CmuxRespawnWaiting || f.noticeNil {
			return nil, f.pillErr
		}
		n := *f.notice
		return &n, nil
	}
	rwResolveAlerts = func(_ context.Context, _ *pigtail.Client, subjectID, cause string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.resolved = append(f.resolved, subjectID+"|"+cause)
		return f.resolveErr
	}
	rwWrapper = func(io.Writer) string { return "" }
	rwAttach = func(context.Context, *pigtail.Client, string, string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.attachCalls++
		return f.attachErr
	}
	rwInterrupt = func(cleanup func()) func() {
		f.mu.Lock()
		f.cleanup = cleanup
		f.mu.Unlock()
		return func() {}
	}
	rwShellAtPrompt = func(surface string) (bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if surface != "S1" {
			t.Errorf("shell probed on %q, want S1", surface)
		}
		if f.claudeAlive || f.shellErr != nil {
			return false, f.shellErr
		}
		if f.shellPolls > 0 {
			f.shellPolls--
			return false, nil
		}
		return true, nil
	}
	rwSendText = func(surface, text string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.typed = append(f.typed, fmt.Sprintf("%q", text))
		return f.sendErr
	}
	rwSendLine = func(surface, line string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sendErr != nil || f.lineErr != nil {
			return errors.Join(f.sendErr, f.lineErr) // one non-nil error: its own message, errors.Is intact
		}
		f.typed = append(f.typed, line)
		return nil
	}
	return f
}

// freshUp: the fresh Claude exists — after the kill, and in shell mode only
// once the worker typed both the line clear and the source line. Under f.mu.
func (f *rwFake) freshUp() bool {
	return f.kills > 0 && f.appears && (f.run.Relaunch != relaunchShell || len(f.typed) == 2)
}

func (f *rwFake) promptPath() string {
	return strings.TrimSuffix(f.run.RelaunchFile, ".sid") + ".prompt"
}
func (f *rwFake) handoffPath() string {
	return strings.TrimSuffix(f.run.RelaunchFile, ".sid") + ".handoff"
}

func (f *rwFake) work() spawnreg.Entry {
	return runRespawnWorker(context.Background(), nil, spawnreg.Open(spawnreg.DefaultPath()), f.run, io.Discard)
}

// pillSeq is the worker's sidebar calls, in order ("waiting draft", "clear",
// "abandoned", …).
func (f *rwFake) pillSeq() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.pills {
		out = append(out, p.String())
	}
	return strings.Join(out, ", ")
}

// assertPillTargets: every pill names the run's workspace and surface, the
// worker's pid while waiting — never the caller's pane variables (#36).
func (f *rwFake) assertPillTargets(t *testing.T) {
	t.Helper()
	for _, p := range f.pills {
		if p.ws != rwRunWS {
			t.Fatalf("pill %s posted on workspace %s, want the run's %s (#36)", p, p.ws, rwRunWS)
		}
		if p.req == nil {
			continue
		}
		if p.req.SurfaceID != "S1" {
			t.Fatalf("pill %s names surface %q, want the run's S1 (#36)", p, p.req.SurfaceID)
		}
		if p.req.State == pigtail.CmuxRespawnWaiting && p.req.PID != os.Getpid() {
			t.Fatalf("waiting pill names pid %d, want the worker's %d", p.req.PID, os.Getpid())
		}
	}
}

func (f *rwFake) assertPills(t *testing.T, want string) {
	t.Helper()
	if got := f.pillSeq(); got != want {
		t.Fatalf("pills = [%s], want [%s]", got, want)
	}
	f.assertPillTargets(t)
}

func (f *rwFake) assertFilesGone(t *testing.T) {
	t.Helper()
	if fileExists(f.promptPath()) || fileExists(f.handoffPath()) || fileExists(f.run.RelaunchFile) {
		t.Fatal("launcher files left behind")
	}
}

func TestRespawnWorkerHappyPath(t *testing.T) {
	f := newRWFake(t)
	e := f.work()
	if e.Result != respawnDone || e.PID != rwNewPID || e.NewSessionID != "sid-fresh" || e.Detail != "" {
		t.Fatalf("final = %+v", e)
	}
	if f.kills != 1 {
		t.Fatalf("kills = %d", f.kills)
	}
	if f.promptAtKill != f.run.Prompt+"\n" {
		t.Fatalf("prompt file = %q", f.promptAtKill)
	}
	if want := fmt.Sprintf("fresh jp sid-old %d plan\n", os.Getpid()); f.handoffAtKill != want {
		t.Fatalf("handoff = %q, want %q", f.handoffAtKill, want)
	}
	if want := "S1|" + f.run.Cwd + "|sid-fresh|" + f.run.ConfigDir + "||plan"; len(f.bindCalls) != 1 || f.bindCalls[0] != want {
		t.Fatalf("binding = %v, want %q", f.bindCalls, want)
	}
	if f.attachCalls != 1 {
		t.Fatalf("attach calls = %d", f.attachCalls)
	}
	f.assertPills(t, "") // a respawn that went through leaves no pill
	es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if len(es) != 2 || es[0].Result != respawnArmed || es[1].Result != respawnDone || es[1].OldSessionID != "sid-old" ||
		es[1].WorkerPID != os.Getpid() || es[1].SurfaceRef != "surface:7" || es[1].Cell != "jp" || es[1].Kind != spawnreg.KindRespawn {
		t.Fatalf("registry = %+v", es)
	}
	assertRelaunchRecorded(t, relaunchLauncher) // a launcher run says relaunch=launcher
}

// Incident 20261004T002742.681-8620: SIGTERM closes Claude without its
// SessionEnd hooks, so session-relaunch.sh never writes <key>.sid — and the
// launcher, with no .sid, never reads the handoff. pigtail writes it itself,
// in the hook's format (the id, then a newline), before the kill.
func TestRespawnWorkerWritesSidWithoutSessionEnd(t *testing.T) {
	f := newRWFake(t)
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	if f.sidAtKill != "sid-old\n" {
		t.Fatalf("<key>.sid at the kill = %q, want %q", f.sidAtKill, "sid-old\n")
	}
	if fi, err := os.Stat(f.run.RelaunchFile); err == nil {
		t.Fatalf("<key>.sid survived the launcher (mode %v)", fi.Mode())
	}
}

// The .sid pigtail writes follows the id after /clear, like the handoff.
func TestRespawnWorkerWritesSidAfterClear(t *testing.T) {
	f := newRWFake(t)
	f.status = func(*rwFake) claudesession.Native {
		return claudesession.Native{SessionID: "sid-after-clear", Status: "idle", StatusUpdatedAt: time.Now().Add(-time.Hour).UnixMilli()}
	}
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	if f.sidAtKill != "sid-after-clear\n" {
		t.Fatalf("<key>.sid at the kill = %q", f.sidAtKill)
	}
}

// withdraw removes the .sid only while it still holds the id pigtail wrote:
// a SessionEnd hook (or a new Claude) may have rewritten it since.
func TestRespawnWorkerWithdrawSid(t *testing.T) {
	read := func(t *testing.T, p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("<key>.sid: %v", err)
		}
		return string(b)
	}
	t.Run("ours is removed", func(t *testing.T) {
		f := newRWFake(t)
		w := &rwWorker{run: f.run, log: io.Discard}
		if err := w.writeLauncherFiles("sid-old"); err != nil {
			t.Fatal(err)
		}
		if got := read(t, f.run.RelaunchFile); got != "sid-old\n" {
			t.Fatalf("<key>.sid = %q", got)
		}
		if fi, _ := os.Stat(f.run.RelaunchFile); fi.Mode().Perm() != 0o600 {
			t.Fatalf("<key>.sid mode = %v, want 0600", fi.Mode().Perm())
		}
		w.withdraw()
		f.assertFilesGone(t)
	})
	t.Run("rewritten by another is kept", func(t *testing.T) {
		f := newRWFake(t)
		w := &rwWorker{run: f.run, log: io.Discard}
		if err := w.writeLauncherFiles("sid-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.run.RelaunchFile, []byte("sid-other\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		w.withdraw()
		if got := read(t, f.run.RelaunchFile); got != "sid-other\n" {
			t.Fatalf("<key>.sid = %q, want the other session's id kept", got)
		}
		if fileExists(f.promptPath()) || fileExists(f.handoffPath()) {
			t.Fatal("prompt or handoff left behind")
		}
	})
	t.Run("never written is kept", func(t *testing.T) {
		f := newRWFake(t)
		if err := os.WriteFile(f.run.RelaunchFile, []byte("sid-old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		w := &rwWorker{run: f.run, log: io.Discard}
		w.withdraw()
		if got := read(t, f.run.RelaunchFile); got != "sid-old\n" {
			t.Fatalf("<key>.sid = %q, want the hook's file kept", got)
		}
	})
}

// A re-check that withdraws before the kill takes the .sid back too.
func TestRespawnWorkerRecheckWithdrawsSid(t *testing.T) {
	f := newRWFake(t)
	idle, draft := fixture(t, "screen-idle-empty.txt"), fixture(t, "screen-draft.txt")
	once, sidGone := false, false
	f.screen = func(f *rwFake, _ int) (string, error) {
		if fileExists(f.handoffPath()) && !once {
			once = true
			return draft, nil
		}
		if once && f.kills == 0 && !fileExists(f.run.RelaunchFile) {
			sidGone = true
		}
		return idle, nil
	}
	if e := f.work(); e.Result != respawnDone || f.kills != 1 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	if !once || !sidGone {
		t.Fatalf("draft at the re-check %v, .sid withdrawn before the next check %v", once, sidGone)
	}
	if f.sidAtKill != "sid-old\n" {
		t.Fatalf("<key>.sid at the kill = %q", f.sidAtKill)
	}
}

func TestRespawnWorkerWaitsForForeground(t *testing.T) {
	f := newRWFake(t)
	f.fgPolls = 3
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	if f.fgAtKill <= 3 {
		t.Fatalf("killed while the foreground was alive (fg checks at kill %d)", f.fgAtKill)
	}
	f.assertPills(t, "") // the agent's own turn ending: no pill
}

func TestRespawnWorkerBusyThenIdle(t *testing.T) {
	f := newRWFake(t)
	rwSettle = 30 * time.Millisecond
	calls := 0
	f.status = func(f *rwFake) claudesession.Native {
		calls++
		if calls <= 3 {
			return claudesession.Native{SessionID: "sid-old", Status: "busy", StatusUpdatedAt: time.Now().UnixMilli()}
		}
		if f.idleSince.IsZero() {
			// Millisecond-truncated: the worker only sees StatusUpdatedAt in
			// ms, so its settle runs from this instant, not from time.Now().
			f.idleSince = time.UnixMilli(time.Now().UnixMilli())
		}
		return claudesession.Native{SessionID: "sid-old", Status: "idle", StatusUpdatedAt: f.idleSince.UnixMilli()}
	}
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	if d := f.killAt.Sub(f.idleSince); d < rwSettle {
		t.Fatalf("killed %s after the session went idle, want ≥ %s", d, rwSettle)
	}
	f.assertPills(t, "") // busy: no pill
}

func TestRespawnWorkerDraftBlocks(t *testing.T) {
	f := newRWFake(t)
	f.run.IdleTimeout = 50 * time.Millisecond
	draft := fixture(t, "screen-draft.txt")
	f.screen = func(*rwFake, int) (string, error) { return draft, nil }
	e := f.work()
	if e.Result != respawnAbandoned || f.kills != 0 || !strings.Contains(e.Detail, "draft in the input") || !strings.Contains(e.Detail, "the session was not touched") {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	f.assertFilesGone(t)
	f.assertPills(t, "waiting draft, abandoned")
}

// Review Focus 1: a draft typed after the first idle read, before the kill.
func TestRespawnWorkerDraftAppearsBeforeKill(t *testing.T) {
	f := newRWFake(t)
	idle, draft := fixture(t, "screen-idle-empty.txt"), fixture(t, "screen-draft.txt")
	once, withdrawn := false, false
	f.screen = func(f *rwFake, _ int) (string, error) {
		if fileExists(f.handoffPath()) && !once {
			once = true
			return draft, nil
		}
		if once && !fileExists(f.handoffPath()) && f.kills == 0 {
			withdrawn = true
		}
		return idle, nil
	}
	if e := f.work(); e.Result != respawnDone || f.kills != 1 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	if !once || !withdrawn {
		t.Fatalf("draft seen at the re-check %v, handoff withdrawn before the next check %v", once, withdrawn)
	}
}

// Review Focus 2: busy again between the handoff write and the kill.
func TestRespawnWorkerBusyAgain(t *testing.T) {
	f := newRWFake(t)
	once, withdrawn := false, false
	f.status = func(f *rwFake) claudesession.Native {
		if fileExists(f.handoffPath()) && !once {
			once = true
			return claudesession.Native{SessionID: "sid-old", Status: "busy", StatusUpdatedAt: time.Now().UnixMilli()}
		}
		if once && !fileExists(f.handoffPath()) && f.kills == 0 {
			withdrawn = true
		}
		return claudesession.Native{SessionID: "sid-old", Status: "idle", StatusUpdatedAt: time.Now().Add(-time.Hour).UnixMilli()}
	}
	if e := f.work(); e.Result != respawnDone || f.kills != 1 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	if !once || !withdrawn {
		t.Fatalf("busy at the re-check %v, handoff withdrawn %v", once, withdrawn)
	}
}

func TestRespawnWorkerNoKillRows(t *testing.T) {
	cases := []struct {
		name, key, pill string
		setup           func(t *testing.T, f *rwFake)
	}{
		{"background agent", "background work", "background", func(t *testing.T, f *rwFake) {
			bg := fixture(t, "screen-bg-agent.txt")
			f.screen = func(*rwFake, int) (string, error) { return bg, nil }
		}},
		{"bash shell running", "Bash shell pid [200]", "background", func(_ *testing.T, f *rwFake) { f.shells = []int{200} }},
		{"shell listing fails", "process listing failed", "background", func(_ *testing.T, f *rwFake) {
			rwBashShells = func(int) ([]int, error) { return nil, errors.New("ps: boom") }
		}},
		{"no input box", "no input box", "screen", func(t *testing.T, f *rwFake) {
			sc := fixture(t, "screen-no-input.txt")
			f.screen = func(*rwFake, int) (string, error) { return sc, nil }
		}},
		{"screen unreadable", "screen unreadable", "screen", func(t *testing.T, f *rwFake) {
			noBox := fixture(t, "screen-no-input.txt")
			f.screen = func(_ *rwFake, n int) (string, error) {
				switch n % 3 { // two different errors and no box: one key
				case 0:
					return noBox, nil
				case 1:
					return "", errors.New("cmux read-screen: socket closed")
				}
				return "", errors.New("cmux read-screen: timeout")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRWFake(t)
			f.run.IdleTimeout = 50 * time.Millisecond
			c.setup(t, f)
			e := f.work()
			if e.Result != respawnAbandoned || f.kills != 0 || !strings.Contains(e.Detail, c.key) {
				t.Fatalf("final = %+v, kills %d", e, f.kills)
			}
			f.assertFilesGone(t)
			f.assertPills(t, "waiting "+c.pill+", abandoned") // one key, one pill, however the reads vary
		})
	}
}

// Review Focus 5: /clear changed the session id since the plan read it.
func TestRespawnWorkerSidChanged(t *testing.T) {
	f := newRWFake(t)
	f.status = func(*rwFake) claudesession.Native {
		return claudesession.Native{SessionID: "sid-after-clear", Status: "idle", StatusUpdatedAt: time.Now().Add(-time.Hour).UnixMilli()}
	}
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	if want := fmt.Sprintf("fresh jp sid-after-clear %d plan\n", os.Getpid()); f.handoffAtKill != want {
		t.Fatalf("handoff = %q, want %q", f.handoffAtKill, want)
	}
}

func TestRespawnWorkerFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *rwFake)
		want  []string
	}{
		{"does not exit", func(f *rwFake) { f.exits = false }, []string{"did not exit within", "it still runs; nothing relaunched"}},
		{"not seen", func(f *rwFake) { f.appears = false }, []string{"no fresh Claude in surface:7", "c jp --resume sid-old", "the tab shows:"}},
		{"wrong config", func(f *rwFake) { f.newEnv = "CMUX_SURFACE_ID=S1 CLAUDE_CONFIG_DIR=/elsewhere PWD=" + f.run.Cwd },
			[]string{"relaunched under the wrong account", "the tab shows:"}},
		{"wrong cwd", func(f *rwFake) {
			f.newEnv = "CMUX_SURFACE_ID=S1 CLAUDE_CONFIG_DIR=" + f.run.ConfigDir + " PWD=/elsewhere"
		},
			[]string{"wrong directory", "the tab shows:"}},
		{"not fresh", func(f *rwFake) { f.newArgs = "/x/bin/claude --resume sid-old --permission-mode plan" },
			[]string{"relaunched but not fresh", "the tab shows:"}},
		{"resumed with the prompt", func(f *rwFake) { f.newArgs = "/x/bin/claude --permission-mode plan --resume sid-old " + f.run.Prompt },
			[]string{"relaunched but not fresh"}},
		{"no prompt", func(f *rwFake) { f.newArgs = "/x/bin/claude --permission-mode plan" },
			[]string{"relaunched but not fresh"}},
		{"other mode", func(f *rwFake) { f.newArgs = "/x/bin/claude --permission-mode auto " + f.run.Prompt },
			[]string{"relaunched but not fresh"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRWFake(t)
			c.setup(f)
			e := f.work()
			if e.Result != respawnFailed {
				t.Fatalf("final = %+v", e)
			}
			for _, w := range c.want {
				if !strings.Contains(e.Detail, w) {
					t.Errorf("detail %q lacks %q", e.Detail, w)
				}
			}
			f.assertFilesGone(t)
			f.assertPills(t, "failed")
			if len(f.bindCalls) != 0 || f.attachCalls != 0 {
				t.Fatal("a failed respawn rebound")
			}
		})
	}
}

func TestRespawnWorkerBindingFails(t *testing.T) {
	f := newRWFake(t)
	f.bindErr = errors.New("cmux: boom")
	e := f.work()
	if e.Result != respawnDone || !strings.Contains(e.Detail, "resume binding not rewritten: cmux: boom — fix: cmux surface resume set") {
		t.Fatalf("final = %+v", e)
	}
	f.assertPills(t, "") // respawned: the warning lives in the record (--status)
}

// #37: a workspace bound elsewhere — say so, suggest no attach without a way out.
func TestRespawnWorkerAttachConflict(t *testing.T) {
	f := newRWFake(t)
	f.attachErr = &pigtail.APIError{Code: pigtail.ErrConflict, Message: "bound to sub_x"}
	e := f.work()
	const want = "workspace already bound to another subject — not rebound"
	if e.Result != respawnDone || !strings.Contains(e.Detail, want) || strings.Contains(e.Detail, "subject attach") {
		t.Fatalf("final = %+v", e)
	}
	f.assertPills(t, "")
}

func TestRespawnWorkerBoundSkipsAttach(t *testing.T) {
	f := newRWFake(t)
	f.run.Bound = true
	if e := f.work(); e.Result != respawnDone || f.attachCalls != 0 {
		t.Fatalf("final = %+v, attach calls %d", e, f.attachCalls)
	}
}

func TestRespawnWorkerSessionEndedOnItsOwn(t *testing.T) {
	f := newRWFake(t)
	f.claudeAlive = false
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "ended on its own") || f.kills != 0 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	if fileExists(f.promptPath()) || fileExists(f.handoffPath()) {
		t.Fatal("files written for a session that is gone")
	}
	f.assertPills(t, "failed")
}

// Review Focus 4: the worker SIGTERMed while it waits.
func TestRespawnWorkerInterrupted(t *testing.T) {
	f := newRWFake(t)
	draft := fixture(t, "screen-draft.txt")
	f.screen = func(f *rwFake, n int) (string, error) {
		if n == 2 {
			f.cleanup() // what the signal handler runs
		}
		return draft, nil
	}
	e := f.work()
	if e.Result != respawnAbandoned || !strings.Contains(e.Detail, "worker interrupted — the session was not touched") || f.kills != 0 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	f.assertFilesGone(t)
	f.assertPills(t, "waiting draft, abandoned")
	es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if len(es) != 2 || es[1].Result != respawnAbandoned {
		t.Fatalf("registry = %+v", es)
	}
}

func TestRespawnWorkerInterruptedAfterKill(t *testing.T) {
	f := newRWFake(t)
	f.onProcs = func(f *rwFake) {
		f.mu.Lock()
		killed, c := f.kills > 0, f.cleanup
		f.mu.Unlock()
		if killed {
			f.onProcs = nil
			c()
		}
	}
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "worker interrupted after closing the session — look at surface:7") {
		t.Fatalf("final = %+v", e)
	}
	es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if last := es[len(es)-1]; last.Result != respawnFailed || len(es) != 2 {
		t.Fatalf("registry = %+v, want armed then failed only", es)
	}
	f.assertPills(t, "failed")
}

func TestRespawnWorkerUTF8Prompt(t *testing.T) {
	f := newRWFake(t)
	f.run.Prompt = "/example-skill resume sub_é"
	f.newArgs = "/x/bin/claude --permission-mode plan " + f.run.Prompt
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	if f.promptAtKill != "/example-skill resume sub_\xc3\xa9\n" {
		t.Fatalf("prompt file bytes = %q", f.promptAtKill)
	}
}

// --- the --worker mode, end to end through cobra ---

func writeRunFile(t *testing.T, run respawnRun) string {
	t.Helper()
	p := respawnRunPath(run.ID)
	if err := writeRespawnRun(p, run); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRespawnWorkerModeNeedsNoOtherFlag(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	t.Setenv("LC_ALL", "C")
	f := newRWFake(t)
	f.run.Bound = true
	path := writeRunFile(t, f.run)
	if _, _, err := runRespawnCLI(t, s, "--worker", path); err != nil {
		t.Fatal(err)
	}
	es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if len(es) != 2 || es[0].Result != respawnArmed || es[0].ID != f.run.ID || es[1].Result != respawnDone {
		t.Fatalf("registry = %+v", es)
	}
	// TestWorkerPinsUTF8Locale: started with LC_ALL=C, the worker pins UTF-8.
	if got := os.Getenv("LC_ALL"); got != workerLocale {
		t.Fatalf("LC_ALL = %q, want %q", got, workerLocale)
	}
}

func TestRespawnWorkerRefusesExtraFlags(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := newRWFake(t)
	path := writeRunFile(t, f.run)
	_, _, err := runRespawnCLI(t, s, "--worker", path, "--subject", "sub_p")
	if err == nil || !strings.Contains(err.Error(), "--worker runs alone (got --subject)") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = runRespawnCLI(t, s, "--worker", path, "--model", "sonnet")
	if err == nil || !strings.Contains(err.Error(), "--worker runs alone (got --model)") {
		t.Fatalf("err = %v", err)
	}
	if es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries(); len(es) != 0 {
		t.Fatal("a refused worker wrote the registry")
	}
}

// TestRespawnWorkerCrossScopeAttach: the worker's own client carries
// --cross-scope from the run file, and rwAttach is the real one.
func TestRespawnWorkerCrossScopeAttach(t *testing.T) {
	for _, cross := range []bool{true, false} {
		withCells(t, func(_, perso string) string { return perso })
		s, st := realSubjectDaemon(t)
		t.Setenv("LC_ALL", "")
		f := newRWFake(t)
		rwAttach = func(ctx context.Context, c *pigtail.Client, subjectID, wsID string) error {
			_, err := c.AttachCmuxWorkspace(ctx, subjectID, pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: wsID, Source: pigtail.CmuxSourcePtnew})
			return err
		}
		f.run.SubjectID, f.run.CrossScope = "sub_t", cross
		path := writeRunFile(t, f.run)
		_, _, err := runRespawnCLI(t, s, "--worker", path)
		if err != nil {
			t.Fatalf("cross=%v: %v", cross, err)
		}
		b, berr := st.GetCmuxBinding(rwRunWS)
		es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
		last := es[len(es)-1]
		if cross {
			if berr != nil || b.SubjectID != "sub_t" || last.Detail != "" {
				t.Fatalf("cross: binding %+v %v, detail %q", b, berr, last.Detail)
			}
			continue
		}
		if berr == nil || !strings.Contains(last.Detail, "pigtail subject attach sub_t --workspace "+rwRunWS+" --source ptnew --cross-scope") {
			t.Fatalf("no cross: binding %+v %v, detail %q", b, berr, last.Detail)
		}
	}
}

// TestPsShowsUTF8Argv: under the pinned locale, the real ps listing shows a
// non-ASCII argv byte for byte (macOS ps escapes it under LC_ALL=C).
func TestPsShowsUTF8Argv(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the escaping is macOS ps's; cmux runs on macOS only")
	}
	if os.Getenv("PIGTAIL_HELPER") == "sleep" {
		// Outlives the parent's wait below even on a loaded machine; the
		// parent's Cleanup kills it, so the length costs nothing.
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	t.Setenv("LC_ALL", "C")
	pinWorkerLocale()
	child := exec.Command(os.Args[0], "-test.run=^TestPsShowsUTF8Argv$", "claude", "sub_é")
	child.Env = append(os.Environ(), "PIGTAIL_HELPER=sleep")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	var args string
	// A bound, not a pace: the loop returns at the first listing that shows
	// the child, so a quiet machine pays nothing; a loaded one (full-suite
	// -race run, `ps -A` slowed by hundreds of processes) gets room.
	waitFor(t, 60*time.Second, func() bool {
		procs, err := claudesession.ClaudeProcesses()
		if err != nil {
			return false
		}
		for _, p := range procs {
			if p.PID == child.Process.Pid {
				args = p.Args
				return true
			}
		}
		return false
	})
	if !strings.HasSuffix(args, " claude sub_\xc3\xa9") {
		t.Fatalf("ps argv = %q, want it to end with the exact bytes of « claude sub_é »", args)
	}
}

// the Claude listing before the kill fails — nothing is touched.
func TestRespawnWorkerProcsListingFails(t *testing.T) {
	f := newRWFake(t)
	rwProcs = func() ([]claudesession.Proc, error) { return nil, errors.New("ps: boom") }
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "nothing touched") || f.kills != 0 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	f.assertFilesGone(t)
}

// a SIGTERM to the worker landing while it is about to write the launcher
// files: the handler withdraws, and the main loop must not write them after.
func TestRespawnWorkerInterruptedBeforeWrite(t *testing.T) {
	f := newRWFake(t)
	f.onProcs = func(f *rwFake) { // step 3 lists the Claude pids right before the write
		f.onProcs = nil
		f.cleanup()
	}
	e := f.work()
	if e.Result != respawnAbandoned || f.kills != 0 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	f.assertFilesGone(t)
}

// / #36: the pills of a held respawn go to the run's workspace and
// surface, with the worker's pid — the caller's CMUX_* variables, inherited by
// the worker, steer none of them.
func TestRespawnWorkerPillOnRunWorkspaceNotCallerEnv(t *testing.T) {
	f := newRWFake(t)
	f.run.IdleTimeout = 50 * time.Millisecond
	draft := fixture(t, "screen-draft.txt")
	f.screen = func(*rwFake, int) (string, error) { return draft, nil }
	if e := f.work(); e.Result != respawnAbandoned {
		t.Fatalf("final = %+v", e)
	}
	if len(f.pills) == 0 {
		t.Fatal("no pill posted")
	}
	f.assertPillTargets(t)
}

// The draft's pill is withdrawn before the kill; a successful respawn leaves
// nothing behind.
func TestRespawnWorkerDraftPillClearedOnRespawn(t *testing.T) {
	f := newRWFake(t)
	idle, draft := fixture(t, "screen-idle-empty.txt"), fixture(t, "screen-draft.txt")
	f.screen = func(_ *rwFake, n int) (string, error) {
		if n <= 3 {
			return draft, nil
		}
		return idle, nil
	}
	if e := f.work(); e.Result != respawnDone || f.kills != 1 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	f.assertPills(t, "waiting draft, clear")
	if f.killsAtPill[1] != 0 {
		t.Fatal("the pill was cleared after the kill, want before")
	}
}

// The pill follows the wait's key: a new key replaces it, the same key does
// not re-post it.
func TestRespawnWorkerPillFollowsTheWaitKey(t *testing.T) {
	f := newRWFake(t)
	idle, draft, bg := fixture(t, "screen-idle-empty.txt"), fixture(t, "screen-draft.txt"), fixture(t, "screen-bg-agent.txt")
	f.screen = func(_ *rwFake, n int) (string, error) {
		switch {
		case n <= 3:
			return draft, nil
		case n <= 6:
			return bg, nil
		}
		return idle, nil
	}
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	f.assertPills(t, "waiting draft, waiting background, clear")
}

// An unchanged waiting pill is re-posted every rwPillRefresh: a daemon that
// restarted meanwhile shows it again.
func TestRespawnWorkerWaitingPillRefreshed(t *testing.T) {
	f := newRWFake(t)
	rwPillRefresh = 10 * time.Millisecond
	f.run.IdleTimeout = 80 * time.Millisecond
	draft := fixture(t, "screen-draft.txt")
	f.screen = func(*rwFake, int) (string, error) { return draft, nil }
	if e := f.work(); e.Result != respawnAbandoned {
		t.Fatalf("final = %+v", e)
	}
	seq := strings.Split(f.pillSeq(), ", ")
	if len(seq) < 3 || seq[len(seq)-1] != "abandoned" {
		t.Fatalf("pills = %q, want several waiting draft then abandoned", seq)
	}
	for _, p := range seq[:len(seq)-1] {
		if p != "waiting draft" {
			t.Fatalf("pills = %q", seq)
		}
	}
	f.assertPillTargets(t)
}

// A pill the daemon refuses (an older daemon's 404, no loop) never fails the
// run: one ⚠ line in its record, however often the pill is re-posted.
func TestRespawnWorkerPillPostFailsShowsInDetail(t *testing.T) {
	f := newRWFake(t)
	rwPillRefresh = 5 * time.Millisecond
	f.run.IdleTimeout = 50 * time.Millisecond
	f.pillErr = errors.New("404 page not found\n")
	draft := fixture(t, "screen-draft.txt")
	f.screen = func(*rwFake, int) (string, error) { return draft, nil }
	e := f.work()
	if e.Result != respawnAbandoned || !strings.Contains(e.Detail, "draft in the input") {
		t.Fatalf("final = %+v", e)
	}
	const warn = "⚠ sidebar pill not posted (404 page not found)"
	if n := strings.Count(e.Detail, warn); n != 1 || strings.Contains(e.Detail, "\n") {
		t.Fatalf("detail = %q, want %q once on one line", e.Detail, warn)
	}
	if len(f.pills) < 3 {
		t.Fatalf("pills = %d, want the refreshes too", len(f.pills))
	}
	// A respawn that went through keeps the warning in its record.
	g := newRWFake(t)
	g.pillErr = errors.New("503 no loop")
	g.screen = func(_ *rwFake, n int) (string, error) {
		if n <= 2 {
			return draft, nil
		}
		return fixture(t, "screen-idle-empty.txt"), nil
	}
	if e := g.work(); e.Result != respawnDone || strings.Count(e.Detail, "⚠ sidebar pill not posted (503 no loop)") != 1 {
		t.Fatalf("respawned: final = %+v", e)
	}
}

// r1·9: a session Claude Code parked as a background job keeps its pid but
// no longer hosts the conversation: never closed, the run gives up at once.
func TestRespawnWorkerParkedSessionAbandons(t *testing.T) {
	f := newRWFake(t)
	f.run.IdleTimeout = time.Minute // at once, not at the timeout
	f.status = func(*rwFake) claudesession.Native {
		return claudesession.Native{SessionID: "sid-old", Status: "idle", ParkedJobID: "job-7", StatusUpdatedAt: time.Now().Add(-time.Hour).UnixMilli()}
	}
	start := time.Now()
	e := f.work()
	if e.Result != respawnAbandoned || f.kills != 0 || !strings.Contains(e.Detail, "session parked as a background job — nothing closed") {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("waited for the timeout")
	}
	f.assertFilesGone(t)
	f.assertPills(t, "abandoned")
}

// A, a respawn armed before /standby gives up — nothing closed, the
// launcher files withdrawn — when the session is in Standby (an unreadable
// marker included: the seam then answers true), even when the Standby comes
// between the first idle read and the re-check right before the kill.
func TestRespawnWorkerAbandonsInStandby(t *testing.T) {
	const why = "standby (D16): respawn abandoned — the session was not touched"
	// The marker predicate itself, over a directory under a plain file:
	// unreadable, so in Standby.
	unreadable := func(getenv func(string) string, sid string) bool {
		return inStandby(getenv, sid)
	}
	for _, c := range []struct {
		name    string
		standby func(n int, getenv func(string) string, sid string) bool // n: the 1-based read of the predicate
		env     bool                                                     // PIGTAIL_STANDBY_DIR under a file
	}{
		{"in standby", func(int, func(string) string, string) bool { return true }, false},
		{"marker unreadable", func(_ int, g func(string) string, sid string) bool { return unreadable(g, sid) }, true},
		{"standby before the re-check", func(n int, _ func(string) string, _ string) bool { return n >= 2 }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRWFake(t)
			f.run.IdleTimeout = time.Minute // at once, not at the timeout
			if c.env {
				file := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PIGTAIL_STANDBY_DIR", filepath.Join(file, "standby"))
			}
			var mu sync.Mutex
			reads := 0
			var sids []string
			rwInStandby = func(getenv func(string) string, sid string) bool {
				mu.Lock()
				defer mu.Unlock()
				reads++
				sids = append(sids, sid)
				return c.standby(reads, getenv, sid)
			}
			start := time.Now()
			e := f.work()
			if e.Result != respawnAbandoned || f.kills != 0 || !strings.Contains(e.Detail, why) {
				t.Fatalf("final = %+v, kills %d", e, f.kills)
			}
			if time.Since(start) > 10*time.Second {
				t.Fatal("waited for the timeout")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(sids) == 0 || sids[0] != "sid-old" {
				t.Fatalf("predicate read for %v, want the current session id sid-old", sids)
			}
			f.assertFilesGone(t)
		})
	}
	// Positive control: out of Standby the respawn closes the old Claude.
	f := newRWFake(t)
	called := false
	rwInStandby = func(func(string) string, string) bool { called = true; return false }
	if e := f.work(); f.kills == 0 || !called {
		t.Fatalf("out of Standby: final = %+v, kills %d, predicate read %v", e, f.kills, called)
	}
}

// (d): native status "shell" (a background shell outlives the turn) — the
// abandon names each shell, never "busy".
func TestRespawnWorkerShellStatusNamesShells(t *testing.T) {
	f := newRWFake(t)
	f.run.IdleTimeout = 30 * time.Millisecond
	f.status = func(*rwFake) claudesession.Native {
		return claudesession.Native{SessionID: "sid-old", Status: "shell", StatusUpdatedAt: time.Now().Add(-time.Hour).UnixMilli()}
	}
	f.shellInfo = []claudesession.ShellInfo{{PID: 200, Age: 12 * time.Minute, Command: "npm run dev"}}
	e := f.work()
	if e.Result != respawnAbandoned || f.kills != 0 ||
		!strings.Contains(e.Detail, "background shell: `npm run dev` (12m)") || strings.Contains(e.Detail, "busy") {
		t.Fatalf("final = %+v", e)
	}
	f.assertPills(t, "abandoned") // shell keeps no waiting pill (key "")
	f.shellInfo = nil
	g := newRWFake(t)
	g.run.IdleTimeout = 30 * time.Millisecond
	g.status = f.status
	if e := g.work(); !strings.Contains(e.Detail, "background shell (none listed)") {
		t.Fatalf("no shell listed: final = %+v", e)
	}
	// CA4: a failed listing says so — never "none listed", never "busy".
	h := newRWFake(t)
	h.run.IdleTimeout = 30 * time.Millisecond
	h.status = f.status
	h.shellInfoErr = errors.New("ps: boom")
	if e := h.work(); !strings.Contains(e.Detail, "background shell (process listing failed)") ||
		strings.Contains(e.Detail, "none listed") || strings.Contains(e.Detail, "busy") {
		t.Fatalf("listing failed: final = %+v", e)
	}
}

func TestDescribeShellsWithholdsSecrets(t *testing.T) {
	t.Parallel()
	got := describeShells([]claudesession.ShellInfo{
		{Age: 45 * time.Second, Command: "curl -H 'Authorization: Bearer abcdefgh12345' https://x"},
		{Age: 90 * time.Minute, Command: "sleep 600"},
		{Age: time.Minute, Command: "a"}, {Age: time.Minute, Command: "b"}, {Age: time.Minute, Command: "c"},
	})
	want := "`(command withheld)` (45s), `sleep 600` (1h30m), `a` (1m), +2 more"
	if got != want {
		t.Fatalf("describeShells = %q, want %q", got, want)
	}
}

// Zone already regressed (2026-10-03: a draft
// recalled from history was read as "screen unreadable" and the respawn gave
// up at 10 min instead of waiting for the draft). With the "shell" branch in
// ready(): an idle session whose input holds a history-labelled draft waits on
// the draft (pill "waiting draft", never "screen"), and respawns once it is gone;
// left there, the abandon names the draft.
func TestRespawnWorkerHistoryDraftWaitsThenRespawns(t *testing.T) {
	idle, draft := fixture(t, "screen-idle-empty.txt"), fixture(t, "screen-draft-history.txt")
	f := newRWFake(t)
	f.screen = func(_ *rwFake, n int) (string, error) {
		if n <= 3 {
			return draft, nil
		}
		return idle, nil
	}
	if e := f.work(); e.Result != respawnDone || f.kills != 1 {
		t.Fatalf("final = %+v, kills %d", e, f.kills)
	}
	f.assertPills(t, "waiting draft, clear")
	g := newRWFake(t)
	g.run.IdleTimeout = 50 * time.Millisecond
	g.screen = func(*rwFake, int) (string, error) { return draft, nil }
	e := g.work()
	if e.Result != respawnAbandoned || g.kills != 0 || !strings.Contains(e.Detail, "draft in the input") ||
		strings.Contains(e.Detail, "screen unreadable") {
		t.Fatalf("draft left: final = %+v", e)
	}
	g.assertPills(t, "waiting draft, abandoned")
}

// the final state carries its run; the report says everyone was told.
func TestRespawnWorkerFinalCarriesTheRun(t *testing.T) {
	f := newRWFake(t)
	f.run.IdleTimeout = 30 * time.Millisecond
	draft := fixture(t, "screen-draft.txt")
	f.screen = func(*rwFake, int) (string, error) { return draft, nil }
	e := f.work()
	last := f.pills[len(f.pills)-1].req
	if last == nil || last.State != respawnAbandoned || last.RunID != "r-test" || last.SubjectID != "sub_p" ||
		last.SessionID != "sid-old" || !strings.Contains(last.Detail, "draft in the input") || strings.Contains(last.Detail, "⚠") {
		t.Fatalf("final pill = %+v", last)
	}
	if strings.Contains(e.Detail, "⚠") {
		t.Fatalf("all told, yet detail = %q", e.Detail)
	}
}

// Review Focus 1: /clear changed the session id since the arming.
func TestRespawnWorkerFinalTargetsCurrentSession(t *testing.T) {
	f := newRWFake(t)
	f.run.IdleTimeout = 30 * time.Millisecond
	f.status = func(*rwFake) claudesession.Native {
		return claudesession.Native{SessionID: "sid-cleared", Status: "busy", StatusUpdatedAt: time.Now().UnixMilli()}
	}
	f.work()
	if got := f.pills[len(f.pills)-1].req.SessionID; got != "sid-cleared" {
		t.Fatalf("final pill session = %q, want the current sid-cleared", got)
	}
}

func TestRespawnWorkerNoticeIncompleteShowsInDetail(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(f *rwFake)
		want  string
	}{
		"skipped": {func(f *rwFake) {
			f.notice = &pigtail.CmuxRespawnNotice{Journal: "ok", Wait: "skipped: session sid-old is gone", Notice: "skipped: session sid-old is gone"}
		}, "⚠ notice incomplete (wait skipped: session sid-old is gone; notice skipped: session sid-old is gone)"},
		"older daemon": {func(f *rwFake) { f.noticeNil = true }, "⚠ the daemon did not report a notice"},
		"unreachable":  {func(f *rwFake) { f.pillErr = errors.New("dial unix: no such file") }, "⚠ notice fate unknown (dial unix: no such file)"},
		// CA8b: no sidebar loop — the session was told; only the pill is missing.
		"no sidebar loop": {func(f *rwFake) {
			f.notice = &pigtail.CmuxRespawnNotice{Journal: "ok", Wait: "ok", Notice: "ok", Pill: "skipped: this daemon runs no cmux sidebar loop"}
		}, "⚠ sidebar pill not posted (skipped: this daemon runs no cmux sidebar loop)"},
	} {
		f := newRWFake(t)
		f.run.IdleTimeout = 30 * time.Millisecond
		draft := fixture(t, "screen-draft.txt")
		f.screen = func(*rwFake, int) (string, error) { return draft, nil }
		tc.setup(f)
		e := f.work()
		if !strings.Contains(e.Detail, tc.want) || strings.Contains(e.Detail, "\n") {
			t.Errorf("%s: detail = %q, want %q", name, e.Detail, tc.want)
		}
		if name == "no sidebar loop" && (strings.Contains(e.Detail, "notice fate unknown") || strings.Contains(e.Detail, "notice incomplete")) {
			t.Errorf("%s: the session was told, yet detail = %q", name, e.Detail)
		}
	}
}

// a respawn that succeeds closes its subject's alerts.
func TestRespawnWorkerDoneResolvesAlerts(t *testing.T) {
	f := newRWFake(t)
	if e := f.work(); e.Result != respawnDone || strings.Join(f.resolved, ",") != "sub_p|respawn r-test" || strings.Contains(e.Detail, "⚠") {
		t.Fatalf("final = %+v, resolved %q", e, f.resolved)
	}
	g := newRWFake(t)
	g.resolveErr = errors.New("dial unix: no such file")
	if e := g.work(); e.Result != respawnDone || !strings.Contains(e.Detail, "⚠ alerts not resolved (dial unix: no such file) — pigtail alerts list") {
		t.Fatalf("resolve failure hidden: %+v", e)
	}
	h := newRWFake(t)
	h.run.SubjectID = ""
	if h.work(); len(h.resolved) != 0 {
		t.Fatalf("no subject, yet resolved %q", h.resolved)
	}
	a := newRWFake(t) // an abandon resolves nothing
	a.run.IdleTimeout = 30 * time.Millisecond
	draft := fixture(t, "screen-draft.txt")
	a.screen = func(*rwFake, int) (string, error) { return draft, nil }
	if a.work(); len(a.resolved) != 0 {
		t.Fatalf("abandon resolved %q", a.resolved)
	}
}

// Account rotation: a run whose fresh session
// goes to another cell. The old Claude answers only under the run's cell
// (ip, cfg), the fresh one only under the fresh cell (mp, cfg-h1): a usage
// reading the wrong dir sees nothing.
func newRWFreshFake(t *testing.T) *rwFake {
	t.Helper()
	testutil.SerialOnly(t) // swaps rwNative
	f := newRWFake(t)
	fresh := filepath.Join(f.dir, "cfg-h1")
	if err := os.Mkdir(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	f.run.Cell, f.run.FreshCell, f.run.FreshConfigDir = "ip", "mp", fresh
	f.newEnv = "CMUX_SURFACE_ID=S1 CLAUDE_CONFIG_DIR=" + fresh + " PWD=" + f.run.Cwd
	rwNative = func(cfg string, pid int) (claudesession.Native, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case pid == rwOldPID && cfg == f.run.ConfigDir && f.claudeAlive:
			return f.status(f), true
		case pid == rwNewPID && cfg == f.run.FreshConfigDir && f.freshUp():
			return claudesession.Native{SessionID: f.newSID, Status: "busy"}, true
		}
		return claudesession.Native{}, false
	}
	return f
}

func TestRespawnWorkerFreshCellHandoffLine(t *testing.T) {
	f := newRWFreshFake(t)
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	if want := fmt.Sprintf("fresh mp sid-old %d plan\n", os.Getpid()); f.handoffAtKill != want {
		t.Fatalf("handoff = %q, want %q", f.handoffAtKill, want)
	}
}

func TestRespawnWorkerFreshCellVerifiesFreshAccount(t *testing.T) {
	f := newRWFreshFake(t)
	f.newEnv = "CMUX_SURFACE_ID=S1 CLAUDE_CONFIG_DIR=" + f.run.ConfigDir + " PWD=" + f.run.Cwd // a fresh Claude under ip
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "relaunched under the wrong account") || !strings.Contains(e.Detail, "not "+f.run.FreshConfigDir) {
		t.Fatalf("final = %+v", e)
	}
}

func TestRespawnWorkerFreshCellNewSidAndBinding(t *testing.T) {
	f := newRWFreshFake(t)
	e := f.work()
	if e.Result != respawnDone || e.NewSessionID != "sid-fresh" {
		t.Fatalf("final = %+v, want the new id read under mp", e)
	}
	if want := "S1|" + f.run.Cwd + "|sid-fresh|" + f.run.FreshConfigDir + "||plan"; len(f.bindCalls) != 1 || f.bindCalls[0] != want {
		t.Fatalf("binding = %v, want %q", f.bindCalls, want)
	}
}

func TestRespawnWorkerBindingFixUsesFreshDir(t *testing.T) {
	f := newRWFreshFake(t)
	f.bindErr = errors.New("cmux: boom")
	e := f.work()
	if e.Result != respawnDone || !strings.Contains(e.Detail, "CLAUDE_CONFIG_DIR="+f.run.FreshConfigDir+" ") {
		t.Fatalf("final = %+v, want the fix under mp's dir", e)
	}
}

// :333 (ready), :569 (id before the kill) and :309 (current id) read the old
// session under the run's cell, ip.
func TestRespawnWorkerOldSessionReadUnderCell(t *testing.T) {
	f := newRWFreshFake(t)
	f.status = func(*rwFake) claudesession.Native {
		return claudesession.Native{SessionID: "sid-cleared", Status: "idle", StatusUpdatedAt: time.Now().Add(-time.Hour).UnixMilli()}
	}
	if e := f.work(); e.Result != respawnDone {
		t.Fatalf("final = %+v (ready() must read ip's native status)", e)
	}
	if !strings.Contains(f.handoffAtKill, " sid-cleared ") {
		t.Fatalf("handoff = %q, want the id read under ip", f.handoffAtKill)
	}
	g := newRWFreshFake(t)
	g.run.IdleTimeout = 30 * time.Millisecond
	g.status = func(*rwFake) claudesession.Native {
		return claudesession.Native{SessionID: "sid-cleared", Status: "busy", StatusUpdatedAt: time.Now().UnixMilli()}
	}
	g.work()
	if got := g.pills[len(g.pills)-1].req.SessionID; got != "sid-cleared" {
		t.Fatalf("final pill session = %q, want sid-cleared read under ip", got)
	}
}

func TestRespawnWorkerResumeLineOldCell(t *testing.T) {
	f := newRWFreshFake(t)
	f.appears = false
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "c ip --resume sid-old") {
		t.Fatalf("final = %+v, want the resume line on the old cell", e)
	}
}

func TestRespawnWorkerEntryCells(t *testing.T) {
	f := newRWFreshFake(t)
	f.work()
	es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if len(es) != 2 {
		t.Fatalf("registry = %+v", es)
	}
	for _, e := range es {
		if e.Cell != "ip" || e.FreshCell != "mp" {
			t.Fatalf("entry cell %q fresh_cell %q, want ip and mp", e.Cell, e.FreshCell)
		}
	}
	// --status filters on cell (ip, perso) and shows fresh_cell.
	withCellRows(t, map[string]cellRow{"ip": {Scope: "perso", Account: "i"}, "mp": {Scope: "perso", Account: "m", Rank: 1}}, "ip")
	var out, errw strings.Builder
	if err := printRespawnStatus(&out, &errw, "", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"cell":"ip"`) || !strings.Contains(out.String(), `"fresh_cell":"mp"`) {
		t.Fatalf("status json = %s", out.String())
	}
	out.Reset()
	if err := printRespawnStatus(&out, &errw, "", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ip→mp") {
		t.Fatalf("status text = %s", out.String())
	}
	// A work caller does not see it (cell ip is perso).
	withCellRows(t, map[string]cellRow{"ip": {Scope: "perso", Account: "i"}, "mt": {Scope: "globex", Account: "m"}}, "mt")
	out.Reset()
	errw.Reset()
	if err := printRespawnStatus(&out, &errw, "", true); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("work status = %v %s", err, out.String())
	}
}

// A run file written before fresh_cell: the fresh session stays on cell.
func TestRespawnWorkerFreshCellLegacyRun(t *testing.T) {
	f := newRWFake(t)
	p := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(p, []byte(`{"id":"r1","cell":"jp","config_dir":"/c/jp","claude_pid":42,"surface_id":"S1","relaunch":"launcher","relaunch_file":"/x/k.sid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := readRespawnRun(p)
	if err != nil {
		t.Fatal(err)
	}
	if id, dir := run.fresh(); id != "jp" || dir != "/c/jp" {
		t.Fatalf("fresh() = %s %s, want jp /c/jp", id, dir)
	}
	if e := f.work(); e.Result != respawnDone || !strings.HasPrefix(f.handoffAtKill, "fresh jp ") {
		t.Fatalf("legacy run: %+v handoff %q", e, f.handoffAtKill)
	}
}

func TestRespawnWorkerHandoffCarriesModel(t *testing.T) {
	f := newRWFake(t)
	f.run.Model = "sonnet"
	f.newArgs = "/x/bin/claude --permission-mode plan --model sonnet " + f.run.Prompt
	e := f.work()
	if e.Result != respawnDone {
		t.Fatalf("final = %+v", e)
	}
	if want := fmt.Sprintf("fresh jp sid-old %d plan sonnet\n", os.Getpid()); f.handoffAtKill != want {
		t.Fatalf("handoff = %q, want %q", f.handoffAtKill, want)
	}
	es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if len(es) != 2 || es[0].Model != "sonnet" || es[1].Model != "sonnet" {
		t.Fatalf("registry = %+v", es)
	}
}

func TestRespawnWorkerModelMissingFromArgv(t *testing.T) {
	f := newRWFake(t)
	f.run.Model = "sonnet"
	e := f.work()
	if e.Result != respawnFailed || !strings.Contains(e.Detail, "not on model sonnet") {
		t.Fatalf("final = %+v", e)
	}
}
