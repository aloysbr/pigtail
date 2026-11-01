package cmuxcheck

import (
	"bytes"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/inputprobe"
)

const newTabID = "S-NEW"

// tabs simulates the resume's world: the dead claude (pid 70 on S-N), the
// new tab and the claude the typed line starts there (pid 71).
type tabs struct {
	t          *testing.T
	w          *world
	events     []string
	alive      map[int]bool
	signals    []syscall.Signal
	verdict    inputprobe.Verdict
	killErr    error
	ignoreTERM bool // the old claude survives SIGTERM
	newTabErr  error
	noSpawn    bool // the typed line starts nothing
	neverIdle  bool // the new tab's shell never reaches its prompt
	closeErr   error
	newTabCwd  string
	focusNew   bool   // the new tab takes cmux's keyboard focus
	onProbe    func() // runs inside the resume's re-probe

	newTabWS  string
	wsOfCalls int
	typed     []string
}

func newTabs(t *testing.T) (*tabs, Row) {
	w := newWorld()
	w.add(t, 70, "S-N", "/cfg/a", "sid-n", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	tb := &tabs{t: t, w: w, alive: map[int]bool{70: true}, verdict: inputprobe.VerdictDead}
	before, now := photos(t)
	now = withNewPanel(now, "S-N", "sid-n", "/w/n", "/cfg/a")
	rows, _ := probeRun(t, w, before, now, probeOpts("S-N"), inputprobe.VerdictDead)
	r := row(t, rows, "S-N")
	if len(r.Actions) != 1 || r.Actions[0] != ActResume {
		t.Fatalf("fixture: row = %+v, want a planned resume", r)
	}
	return tb, r
}

func (tb *tabs) repairer() *Repairer {
	d := tb.w.deps()
	d.ProbeInput = func(tg inputprobe.Target) inputprobe.Result {
		if want := "/dev/ttys070"; tg.TTY != want {
			tb.t.Errorf("re-probe target tty %q, want %q: the resume must read the tty before probing", tg.TTY, want)
		}
		if tb.onProbe != nil {
			tb.onProbe()
		}
		return inputprobe.Result{Verdict: tb.verdict}
	}
	return &Repairer{D: d, PidAlive: func(pid int) bool { return tb.alive[pid] },
		Send: func(surface, text string) error {
			tb.events = append(tb.events, "send "+surface)
			tb.typed = append(tb.typed, text)
			if strings.HasPrefix(text, " export ") && !tb.noSpawn {
				tb.w.idleSurface[surface] = false
				tb.alive[71] = true
				tb.w.procs = append(tb.w.procs, claudesession.Proc{PID: 71, Args: "claude --resume sid-n"})
				tb.w.env[71] = "claude CMUX_SURFACE_ID=" + surface + " CLAUDE_CONFIG_DIR=/cfg/a"
				tb.w.native[71] = claudesession.Native{PID: 71, SessionID: "sid-n", Status: "idle", Kind: "interactive"}
			}
			return nil
		},
		Kill: func(pid int, sig syscall.Signal) error {
			tb.events = append(tb.events, "kill")
			tb.signals = append(tb.signals, sig)
			if tb.killErr != nil {
				return tb.killErr
			}
			if !tb.ignoreTERM {
				tb.alive[pid] = false
				var keep []claudesession.Proc
				for _, p := range tb.w.procs {
					if p.PID != pid {
						keep = append(keep, p)
					}
				}
				tb.w.procs = keep
			}
			return nil
		},
		NewTab: func(ws, cwd string) (string, error) {
			tb.events = append(tb.events, "new-tab")
			tb.newTabWS, tb.newTabCwd = ws, cwd
			if tb.newTabErr != nil {
				return "", tb.newTabErr
			}
			tb.w.idleSurface[newTabID] = !tb.neverIdle
			if tb.focusNew {
				tb.w.focused = newTabID
			}
			return newTabID, nil
		},
		CloseTab: func(surface, ws string) error {
			tb.events = append(tb.events, "close "+surface)
			return tb.closeErr
		},
		WorkspaceOf: func(string) (string, error) { tb.wsOfCalls++; return "W-TREE", nil },
		Sleep:       func(time.Duration) {}, Poll: time.Millisecond, ExitTimeout: 5 * time.Millisecond,
		SeenTimeout: 5 * time.Millisecond, SeenSettle: time.Millisecond, ShellTimeout: 5 * time.Millisecond, CmuxPID: simCmuxPID}
}

func (tb *tabs) has(e string) bool {
	for _, x := range tb.events {
		if x == e {
			return true
		}
	}
	return false
}

func (tb *tabs) nothingDone(t *testing.T, r Row) {
	t.Helper()
	if tb.has("kill") || tb.has("new-tab") || len(tb.typed) != 0 {
		t.Fatalf("events %v typed %q: want nothing stopped, nothing opened (row %+v)", tb.events, tb.typed, r)
	}
}

func TestResumeStopsOldClaudeBeforeOpeningTab(t *testing.T) {
	tb, r := newTabs(t)
	tb.repairer().Fix(&r)
	want := []string{"kill", "new-tab", "send " + newTabID, "close S-N"}
	if strings.Join(tb.events, "|") != strings.Join(want, "|") {
		t.Fatalf("events %v, want %v", tb.events, want)
	}
	if r.Remaining || r.Result != "repaired" || r.PID != 71 || len(r.Done) != 1 {
		t.Fatalf("row = %+v", r)
	}
}

func TestResumeTypesLineWithCellConfigDir(t *testing.T) {
	tb, r := newTabs(t)
	tb.repairer().Fix(&r)
	if len(tb.typed) != 1 || !strings.Contains(tb.typed[0], "CLAUDE_CONFIG_DIR=/cfg/a") || !strings.Contains(tb.typed[0], "--resume sid-n") {
		t.Fatalf("typed %q", tb.typed)
	}
}

func TestResumeOpensTabInBindingCwd(t *testing.T) {
	tb, r := newTabs(t)
	tb.repairer().Fix(&r)
	if tb.newTabCwd != "/w/n" {
		t.Fatalf("cwd %q", tb.newTabCwd)
	}
	tb2, r2 := newTabs(t)
	r2.cwd = ""
	tb2.repairer().Fix(&r2)
	if r2.Blocked == "" || !r2.Remaining {
		t.Fatalf("row = %+v, want blocked", r2)
	}
	tb2.nothingDone(t, r2)
}

func TestResumeBlockedWhenCheckpointDiffersFromLive(t *testing.T) {
	tb, r := newTabs(t)
	n := tb.w.native[70]
	n.SessionID = "sid-other"
	tb.w.native[70] = n
	tb.repairer().Fix(&r)
	if r.Blocked != "binding checkpoint differs from the live session" {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
}

func TestResumeSeenUsesNewTabUUID(t *testing.T) {
	tb, r := newTabs(t)
	tb.repairer().Fix(&r)
	if !tb.has("send "+newTabID) || tb.has("send S-N") || r.PID != 71 {
		t.Fatalf("events %v row %+v: the line goes to the new tab, the new claude is seen there", tb.events, r)
	}
}

func TestResumeClosesDeadTabAfterNewClaudeSeen(t *testing.T) {
	tb, r := newTabs(t)
	tb.repairer().Fix(&r)
	if tb.events[len(tb.events)-1] != "close S-N" {
		t.Fatalf("events %v: the dead tab closes last", tb.events)
	}
}

func TestResumeKeepsDeadTabWhenNewClaudeNotSeen(t *testing.T) {
	tb, r := newTabs(t)
	tb.noSpawn = true
	line := r.Line
	tb.repairer().Fix(&r)
	if tb.has("close S-N") {
		t.Fatalf("events %v: the dead tab must stay", tb.events)
	}
	want := "new claude not seen in the new tab, old tab kept: resume by hand with `" + line + "`"
	if !r.Remaining || r.Result != want {
		t.Fatalf("result %q, want %q", r.Result, want)
	}
}

func TestResumeBlockedOnBypassBinding(t *testing.T) {
	tb, r := newTabs(t)
	r.bypass = true
	tb.repairer().Fix(&r)
	if r.Blocked != "binding runs bypassPermissions: relaunch it by hand (D8)" {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
}

func TestResumeBlockedWhenTranscriptAbsent(t *testing.T) {
	tb, r := newTabs(t)
	tb.w.noTranscript["/cfg/a|sid-n"] = true
	tb.repairer().Fix(&r)
	if !strings.Contains(r.Blocked, "no transcript of sid-n") {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
}

func TestResumeBlockedWhenAccountDiffersFromLive(t *testing.T) {
	tb, r := newTabs(t)
	tb.w.env[70] = strings.Replace(tb.w.env[70], "/cfg/a", "/cfg/b", 1)
	tb.repairer().Fix(&r)
	if r.Blocked != "binding account differs from the live session's: resume by hand" {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
}

func TestResumeShellNeverIdleTypesNothing(t *testing.T) {
	tb, r := newTabs(t)
	tb.neverIdle = true
	tb.repairer().Fix(&r)
	if len(tb.typed) != 0 || tb.has("close S-N") || !r.Remaining {
		t.Fatalf("events %v typed %q row %+v", tb.events, tb.typed, r)
	}
}

func TestResumeRefusedWhenInputBack(t *testing.T) {
	tb, r := newTabs(t)
	tb.verdict = inputprobe.VerdictAlive
	tb.repairer().Fix(&r)
	if r.Result != "input back, nothing done" || len(r.Done) != 0 {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
	tb2, r2 := newTabs(t)
	tb2.verdict = inputprobe.VerdictUnknown
	tb2.repairer().Fix(&r2)
	tb2.nothingDone(t, r2)
	if r2.Result != "input probe inconclusive, nothing done" || !r2.Remaining {
		t.Fatalf("unknown: row = %+v, want the inconclusive line, still remaining", r2)
	}
}

func TestResumeRefusedWhenBusy(t *testing.T) {
	tb, r := newTabs(t)
	n := tb.w.native[70]
	n.Status = "busy"
	tb.w.native[70] = n
	tb.repairer().Fix(&r)
	if !strings.HasPrefix(r.Blocked, "busy:") {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
}

func TestResumeNeverKillsWhenPidChanged(t *testing.T) {
	tb, r := newTabs(t)
	r.PID = 99 // the row saw another process
	tb.repairer().Fix(&r)
	if !r.Remaining {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
}

func TestResumeKillFailureOpensNothing(t *testing.T) {
	for name, f := range map[string]func(*tabs){
		"kill error":    func(tb *tabs) { tb.killErr = errors.New("EPERM") },
		"survives TERM": func(tb *tabs) { tb.ignoreTERM = true },
	} {
		tb, r := newTabs(t)
		f(tb)
		tb.repairer().Fix(&r)
		line := r.Line
		if tb.has("new-tab") || len(tb.typed) != 0 || !r.Remaining {
			t.Fatalf("%s: events %v row %+v", name, tb.events, r)
		}
		// never two claudes on one transcript: stop the old one first
		if want := StopFirstMessage(70, line); r.Result != want {
			t.Fatalf("%s: result %q, want %q", name, r.Result, want)
		}
		for _, s := range tb.signals {
			if s != syscall.SIGTERM {
				t.Fatalf("%s: signal %v: SIGTERM only, never SIGKILL", name, s)
			}
		}
	}
}

func TestResumeCloseFailureIsInfoNotFailure(t *testing.T) {
	tb, r := newTabs(t)
	tb.closeErr = errors.New("cmux: cannot close the last surface")
	tb.repairer().Fix(&r)
	if r.Remaining || r.Result != "repaired" || strings.Join(r.Info, "|") != "old tab not closed|close: cmux: cannot close the last surface" {
		t.Fatalf("row = %+v", r)
	}
}

func TestResumeNeverSendsSIGSTOP(t *testing.T) {
	tb, r := newTabs(t)
	tb.repairer().Fix(&r)
	if len(tb.signals) != 1 || tb.signals[0] != syscall.SIGTERM {
		t.Fatalf("signals %v, want [SIGTERM]", tb.signals)
	}
}

func TestResumeOnlyForNamedSurface(t *testing.T) {
	tb, r := newTabs(t)
	r.named = false
	tb.repairer().Fix(&r)
	if !r.Remaining || len(r.Done) != 0 || r.Blocked != "resume refused: only for a surface named by --surface" {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
}

func TestResumeWorkspaceFromPhotoPanel(t *testing.T) {
	tb, r := newTabs(t)
	tb.repairer().Fix(&r)
	if tb.wsOfCalls != 0 || tb.newTabWS != "W1" {
		t.Fatalf("WorkspaceOf calls %d, ws %q: want the photo's W1", tb.wsOfCalls, tb.newTabWS)
	}
	tb2, r2 := newTabs(t)
	r2.workspace = ""
	tb2.repairer().Fix(&r2)
	if tb2.wsOfCalls != 1 || tb2.newTabWS != "W-TREE" {
		t.Fatalf("WorkspaceOf calls %d, ws %q: want the seam's", tb2.wsOfCalls, tb2.newTabWS)
	}
}

// The daemon's pilot is never resumed. The real guard is the plan-time one
// (inputDead plans no resume on the pilot); Run sets Repairer.Pilot only
// under --unattended, which names no surface, so this resume-time guard is
// defence in depth: a row that reached Fix with a planned resume on the
// pilot is still blocked before any stop.
func TestResumeBlockedOnPilot(t *testing.T) {
	tb, r := newTabs(t)
	rp := tb.repairer()
	rp.Pilot = "S-N"
	rp.Fix(&r)
	if r.Blocked != "pilot session: never resumed by the check, resume by hand" || !r.Remaining {
		t.Fatalf("row = %+v", r)
	}
	tb.nothingDone(t, r)
}

// The default Kill seam sends SIGTERM and nothing else: any other signal is
// refused before the system call (a pid no process holds, so a broken guard
// would still signal nothing).
func TestDefaultKillRefusesAllButSIGTERM(t *testing.T) {
	const nobody = 2147480000
	for _, sig := range []syscall.Signal{syscall.SIGKILL, syscall.SIGSTOP, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT} {
		if err := killTERMOnly(nobody, sig); err == nil || !strings.Contains(err.Error(), "SIGTERM only") {
			t.Fatalf("signal %v: err %v, want refused", sig, err)
		}
	}
	if funcName(DefaultRepairer(Deps{}).Kill) != funcName(killTERMOnly) {
		t.Fatal("DefaultRepairer's Kill is not killTERMOnly")
	}
}

func funcName(f any) string { return runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name() }

// Bloquant 1 (round 3): the new tab holds the keyboard focus, so the gate
// refuses to type there — after the SIGTERM, with the tab open. The row
// gives the hand-resume line, never a « busy » block.
func TestResumeBusyGateOnNewTabGivesNotSeenLine(t *testing.T) {
	tb, r := newTabs(t)
	tb.focusNew = true
	line := r.Line
	tb.repairer().Fix(&r)
	if !tb.has("kill") || !tb.has("new-tab") || tb.has("close S-N") || len(tb.typed) != 0 {
		t.Fatalf("events %v typed %q", tb.events, tb.typed)
	}
	if r.Blocked != "" || !r.Remaining || r.Result != HandResumeMessage(line) || r.PID != 0 {
		t.Fatalf("row = %+v, want the not-seen line, no block, no live pid", r)
	}
}

// The report shows what an ended resume did, never a bare « ok ».
func TestResumeInputBackShowsInReport(t *testing.T) {
	tb, r := newTabs(t)
	tb.verdict = inputprobe.VerdictAlive
	tb.repairer().Fix(&r)
	var buf bytes.Buffer
	Report{Rows: []Row{r}}.Write(&buf)
	if !strings.Contains(buf.String(), "input back, nothing done") {
		t.Fatalf("report:\n%s", buf.String())
	}
}

// Live is read again right before the SIGTERM: a claude replaced on the
// surface meanwhile (here during the re-probe) is never signalled.
func TestResumeRereadsLiveBeforeKill(t *testing.T) {
	tb, r := newTabs(t)
	tb.onProbe = func() {
		tb.w.procs = []claudesession.Proc{{PID: 72, Args: "claude"}}
		tb.w.env[72] = tb.w.env[70]
		tb.w.native[72] = tb.w.native[70]
		tb.alive[72] = true
	}
	tb.repairer().Fix(&r)
	if !r.Remaining || len(tb.signals) != 0 {
		t.Fatalf("signals %v row %+v: want nothing signalled", tb.signals, r)
	}
	tb.nothingDone(t, r)
}

// NewTab failing: the old claude is stopped, no tab opened — the line says so.
func TestResumeNewTabFailureSaysNoTabOpened(t *testing.T) {
	tb, r := newTabs(t)
	tb.newTabErr = errors.New("cmux: no such workspace")
	line := r.Line
	tb.repairer().Fix(&r)
	if want := TabNotOpenedMessage(line); r.Result != want || !r.Remaining {
		t.Fatalf("result %q, want %q", r.Result, want)
	}
	if !strings.Contains(strings.Join(r.Info, "|"), "cmux: no such workspace") {
		t.Fatalf("info %q: want the cause", r.Info)
	}
}

// A missing NewTab or CloseTab seam refuses before any signal.
func TestResumeNilSeamsRefuseBeforeSignal(t *testing.T) {
	for name, f := range map[string]func(*Repairer){
		"NewTab":   func(rp *Repairer) { rp.NewTab = nil },
		"CloseTab": func(rp *Repairer) { rp.CloseTab = nil },
	} {
		tb, r := newTabs(t)
		rp := tb.repairer()
		f(rp)
		rp.Fix(&r)
		if !r.Remaining || len(tb.signals) != 0 {
			t.Fatalf("%s nil: signals %v row %+v", name, tb.signals, r)
		}
		tb.nothingDone(t, r)
	}
}

// The re-probe's target carries the tty read now by TTYOf (revue du plan,
// tour 2): an empty tty would make every --fix --surface inconclusive.
func TestResumeReprobeTargetCarriesTTY(t *testing.T) {
	tb, r := newTabs(t)
	probed := 0
	rp := tb.repairer()
	inner := rp.D.ProbeInput
	rp.D.ProbeInput = func(tg inputprobe.Target) inputprobe.Result {
		probed++
		if tg.TTY == "" || tg.TTY != "/dev/ttys070" {
			t.Fatalf("re-probe target tty %q, want the tty of the fake TTYOf", tg.TTY)
		}
		return inner(tg)
	}
	rp.Fix(&r)
	if probed != 1 || len(tb.signals) != 1 || tb.signals[0] != syscall.SIGTERM {
		t.Fatalf("probed %d, signals %v: want one re-probe then the SIGTERM", probed, tb.signals)
	}
}

func TestResumeUnknownWhenTTYOfFails(t *testing.T) {
	tb, r := newTabs(t)
	tb.w.ttyOfErr = map[int]error{70: errors.New("ps failed")}
	tb.repairer().Fix(&r)
	tb.nothingDone(t, r)
	if r.Result != "input probe inconclusive, nothing done" || !r.Remaining {
		t.Fatalf("row = %+v, want the inconclusive line", r)
	}
}

func TestResumeUnknownWhenClaudeNotForeground(t *testing.T) {
	tb, r := newTabs(t)
	tb.w.notForeground = map[int]string{70: "stopped or traced"}
	tb.repairer().Fix(&r)
	tb.nothingDone(t, r)
	if len(tb.signals) != 0 || r.Result != "input probe inconclusive, nothing done" {
		t.Fatalf("signals %v row = %+v: nothing may be stopped", tb.signals, r)
	}
}
