package livepass

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Tests of scripts/live-pass.sh against the doubles of stubs_test.go.

func contains(t *testing.T, what, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%s does not contain %q:\n%s", what, sub, s)
	}
}

func notContains(t *testing.T, what, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Fatalf("%s contains %q:\n%s", what, sub, s)
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("mode 000 does not stop root")
	}
}

// --- up -------------------------------------------------------------------

func TestUpOpensInBackgroundAndCreatesWandK(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	opens := r.callLines("open")
	if len(opens) != 1 || argsOf("open", opens[0]) != "-g -a cmux NIGHTLY" {
		t.Fatalf("open calls: %q, want one `open -g -a cmux NIGHTLY`", opens)
	}
	if n := r.countCalls("nightly", "new-window"); n != 2 {
		t.Fatalf("new-window calls: %d, want 2", n)
	}
	st := r.state()
	if st["W"] != winW || st["K"] != winK || st["NIGHTLY_LAUNCHED"] != "1" || st["NIGHTLY_VERSION"] != versionA {
		t.Fatalf("state: %v", st)
	}
}

// callerCmuxLeaks checks calls.log for C9: every NIGHTLY CLI call carries
// exactly CMUX_SOCKET_PATH=<nightly socket>, every main CLI call no CMUX_*,
// and no trap was called. A calls.log without a call of either CLI is an
// error (C11 c9-env): an empty journal proves nothing.
func callerCmuxLeaks(calls, nightlySock string) error {
	var nightly, main int
	var errs []string
	sc := bufio.NewScanner(strings.NewReader(calls))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "TRAP"):
			errs = append(errs, "trap called: "+l)
		case strings.HasPrefix(l, "nightly "):
			nightly++
			env := envOf(l)
			if len(env) != 1 || env[0] != "CMUX_SOCKET_PATH="+nightlySock {
				errs = append(errs, fmt.Sprintf("nightly CLI env %q: %s", env, l))
			}
		case strings.HasPrefix(l, "main "):
			main++
			if env := envOf(l); len(env) != 0 {
				errs = append(errs, fmt.Sprintf("main CLI env %q: %s", env, l))
			}
		}
	}
	if nightly == 0 {
		return errors.New("calls.log has no call of the nightly CLI")
	}
	if main == 0 {
		return errors.New("calls.log has no call of the main CLI")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

func TestCmuxCallsGetNoCallerCmuxEnv(t *testing.T) {
	t.Parallel()
	if err := callerCmuxLeaks("", "/x"); err == nil {
		t.Fatal("callerCmuxLeaks on an empty calls.log must fail")
	}
	r := newRig(t)
	r.seedLedger() // mac-idle is called: its env is checked too
	r.mustRun(0, "up")
	r.mustRun(0, "status")
	r.mustRun(0, "down")
	calls := r.calls()
	if err := callerCmuxLeaks(calls, r.nightlySocket()); err != nil {
		t.Fatal(err)
	}
	// Positive control: the double does journal a CMUX_* variable it is given.
	contains(t, "calls.log", calls, "CMUX_SOCKET_PATH="+r.nightlySocket())
	// The main CLI called is the one of LIVEPASS_MAIN_APP (journal name main).
	if len(r.callLines("main")) == 0 {
		t.Fatal("main CLI of LIVEPASS_MAIN_APP never called")
	}
	mf := r.callLines("mac-idle")
	if len(mf) == 0 {
		t.Fatal("mac-idle never called")
	}
	for _, l := range mf {
		contains(t, "mac-idle env (keeps the caller's, C9)", l, "CMUX_SURFACE_ID=80914656-BF88-4780-9982-CA1665755494")
	}
}

func TestUpRefusesWithoutNightly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.extraEnv = []string{"LIVEPASS_NIGHTLY_APP=" + r.path("missing.app")}
	_, e := r.mustRun(2, "up")
	contains(t, "stderr", e, "live-pass: cmux NIGHTLY not found")
	if len(r.callLines("open")) != 0 {
		t.Fatal("open called")
	}
}

func TestUpWaitsForAnAnsweringSocket(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.touch("n.sock") // a stale socket file: present, not answering
	r.socketDelay = 3
	r.mustRun(0, "up")
	if n := r.countCalls("nightly", "list-windows"); n != 4 {
		t.Fatalf("list-windows calls: %d, want 4", n)
	}
	r.mustRun(0, "down")

	r2 := newRig(t)
	r2.touch("socket-dead")
	before := r2.ledger()
	r2.socketWait = 1
	_, e := r2.mustRun(3, "up")
	contains(t, "stderr", e, "live-pass: nightly socket did not answer")
	if alive(r2.nightlyPid()) {
		t.Fatal("the fake NIGHTLY launched by up survived its failure")
	}
	if r2.stateDir() != "" || len(r2.passDirs()) != 0 {
		t.Fatal("pass state left behind")
	}
	if r2.ledger() != before {
		t.Fatal("ledger changed by a failed up")
	}
}

func TestUpRefusesLongSocketPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	base := r.path("root") + "/"
	long := base + strings.Repeat("x", 95-len(base))
	if len(long) != 95 {
		t.Fatalf("long root is %d bytes", len(long))
	}
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatal(err)
	}
	r.extraEnv = []string{"LIVEPASS_ROOT=" + long}
	_, e := r.mustRun(2, "up")
	contains(t, "stderr", e, "socket path too long")
}

func TestUpRefusesSecondPass(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	_, e := r.mustRun(2, "up")
	contains(t, "stderr", e, "a live pass is already up")
	r.mustRun(0, "down")
}

func TestUpRefusesWhenNightlyFrontmostBeforeUp(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedLedger()
	pid := r.startNightly()
	r.setFront("cmux NIGHTLY")
	_, e := r.mustRun(2, "up")
	contains(t, "stderr", e, "live-pass: cmux NIGHTLY is frontmost before up — the user is using it; retry once it is in the background")
	if len(r.callLines("open")) != 0 || len(r.callLines("mac-idle")) != 0 {
		t.Fatal("open or mac-idle called")
	}
	if len(r.passDirs()) != 0 {
		t.Fatalf("pass dirs: %v", r.passDirs())
	}
	if r.ledger() != "" {
		t.Fatalf("ledger written: %q", r.ledger())
	}
	if !alive(pid) {
		t.Fatal("the user's NIGHTLY was stopped")
	}
	// Control: the same NIGHTLY in the background is reused.
	r.setFront("Arc")
	r.mustRun(0, "up")
	if st := r.state(); st["NIGHTLY_LAUNCHED"] != "0" {
		t.Fatalf("state: %v", st)
	}
	r.mustRun(0, "down")
	if !alive(pid) {
		t.Fatal("down stopped a reused NIGHTLY")
	}
}

func TestUpReturnsWithBackgroundChildren(t *testing.T) {
	t.Parallel()
	// Positive control: the launcher does catch a child holding the pipes.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.sh")
	if err := os.WriteFile(bad, []byte("sleep 5 &\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := runBash(bad, []string{"PATH=/usr/bin:/bin"}); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("runBash did not see a held pipe: %v", err)
	}
	r := newRig(t)
	r.mustRun(0, "up")
	st := r.state()
	sp, _ := strconv.Atoi(st["SAMPLER_PID"])
	np, _ := strconv.Atoi(st["NIGHTLY_PID"])
	if !alive(sp) || !alive(np) {
		t.Fatalf("sampler %d alive=%v, nightly %d alive=%v", sp, alive(sp), np, alive(np))
	}
	r.mustRun(0, "down")
}

// blindCase is one C11 row fed with a blind or unreadable instrument (D4).
type blindCase struct {
	id   string
	name string
	run  func(t *testing.T)
}

func blindInstrumentCases() []blindCase {
	return []blindCase{
		{"nightly-running", "lsappinfo blind", func(t *testing.T) {
			r := newRig(t)
			r.touch("lsappinfo-blind")
			_, e := r.mustRun(2, "up")
			contains(t, "stderr", e, "cannot tell whether cmux NIGHTLY runs")
			if len(r.callLines("open")) != 0 {
				t.Fatal("open called on a blind lsappinfo")
			}
		}},
		{"nightly-pid", "pid garbage", func(t *testing.T) {
			r := newRig(t)
			r.touch("pid-garbage")
			r.socketWait = 1
			_, e := r.mustRun(3, "up")
			contains(t, "stderr", e, "cmux NIGHTLY launched but its pid was not read")
			if r.stateDir() != "" {
				t.Fatal("lp.current created")
			}
		}},
		{"nightly-pid", "pid junk", func(t *testing.T) {
			r := newRig(t)
			r.write("pid-garbage", 0o644, "junk")
			r.socketWait = 1
			_, e := r.mustRun(3, "up")
			contains(t, "stderr", e, "cmux NIGHTLY launched but its pid was not read")
			if r.stateDir() != "" {
				t.Fatal("lp.current created")
			}
		}},
		{"focus-now", "ioreg empty", func(t *testing.T) {
			r := newRig(t)
			r.touch("ioreg-empty")
			o, _ := r.mustRun(1, "front")
			contains(t, "stdout", o, "not measured: lock state unreadable")
		}},
		{"pass-lock", "root mode 000", func(t *testing.T) {
			skipIfRoot(t)
			r := newRig(t)
			if err := os.Chmod(r.path("root"), 0); err != nil {
				t.Fatal(err)
			}
			_, e := r.mustRun(2, "up")
			contains(t, "stderr", e, "LIVEPASS_ROOT is not a readable directory")
		}},
		{"ledger", "ledger mode 000", func(t *testing.T) {
			skipIfRoot(t)
			r := newRig(t)
			if err := os.Chmod(r.ledgerPath(), 0); err != nil {
				t.Fatal(err)
			}
			o, _ := r.mustRun(0, "up")
			contains(t, "stdout", o, "D2: clean passes 0/3 (not measured: ledger unreadable), mac-idle required")
			if r.countCalls("mac-idle", "") == 0 {
				t.Fatal("mac-idle not called")
			}
			o, e := r.mustRun(0, "down")
			notContains(t, "stdout", o, "D2: pass recorded") // never claimed when the write failed
			contains(t, "stderr", e, "cannot write the D2 ledger "+r.ledgerPath()+": pass not recorded")
		}},
		{"ledger", "plist without version", func(t *testing.T) {
			r := newRig(t)
			r.setVersion("")
			o, _ := r.mustRun(0, "up")
			contains(t, "stdout", o, "0/3 (not measured: nightly version unreadable)")
			r.mustRun(0, "down")
			contains(t, "ledger", r.ledger(), " unknown")
			f := strings.Fields(strings.Split(strings.TrimSpace(r.ledger()), "\n")[3])
			if f[2] != "unknown" {
				t.Fatalf("version recorded %q, want unknown", f[2])
			}
		}},
		{"mac-idle", "mac-idle absent", func(t *testing.T) {
			r := newRig(t)
			r.seedLedger()
			r.extraEnv = []string{"LIVEPASS_MAC_FREE=/nonexistent/mac-idle"}
			_, e := r.mustRun(4, "up")
			contains(t, "stderr", e, "the Mac is busy (mac-idle not found: /nonexistent/mac-idle)")
		}},
		{"stopped", "nightly pid unknown", func(t *testing.T) {
			r := newRig(t)
			r.mustRun(0, "up")
			pid := r.nightlyPid()
			r.setState("NIGHTLY_PID", "")
			_, e := r.mustRun(5, "down")
			contains(t, "stderr", e, "cmux NIGHTLY pid unknown — not proven stopped")
			if r.stateDir() == "" {
				t.Fatal("state not kept")
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}},
	}
}

func TestBlindInstrumentsNeverReadAsAbsence(t *testing.T) {
	t.Parallel()
	for _, c := range blindInstrumentCases() {
		t.Run(c.id+"/"+c.name, c.run)
	}
}

func TestUpStopsWhenMacBusy(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedLedger()
	r.setMacFree(1)
	_, e := r.mustRun(4, "up")
	contains(t, "stderr", e, "live-pass: the Mac is busy (mac-idle 1)")
	if len(r.callLines("open")) != 0 {
		t.Fatal("open called")
	}
	if len(r.passDirs()) != 0 {
		t.Fatal("pass dir left")
	}
}

func TestUpRequiresMacFreeBeforeThreeCleanPasses(t *testing.T) {
	t.Parallel()
	for n := 0; n < 3; n++ {
		r := newRig(t)
		r.seedLedger(cleanLines(n)...)
		o, _ := r.mustRun(0, "up")
		contains(t, "stdout", o, fmt.Sprintf("D2: clean passes %d/3, mac-idle required", n))
		if r.countCalls("mac-idle", "") != 1 {
			t.Fatalf("%d clean: mac-idle calls %d", n, r.countCalls("mac-idle", ""))
		}
		r.mustRun(0, "down")
	}
	r := newRig(t)
	lines := append(cleanLines(2), ledgerLines("dirty", versionA, 1)...)
	lines = append(lines, cleanLines(2)...)
	r.seedLedger(lines...)
	o, _ := r.mustRun(0, "up")
	contains(t, "stdout", o, "D2: clean passes 2/3, mac-idle required")
	r.mustRun(0, "down")
}

func TestUpRequiresMacFreeAfterNightlyUpgrade(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setVersion("0.64.21-nightly.B")
	o, _ := r.mustRun(0, "up")
	contains(t, "stdout", o, "D2: clean passes 0/3, mac-idle required")
	if r.countCalls("mac-idle", "") != 1 {
		t.Fatal("mac-idle not called after an upgrade")
	}
	r.mustRun(0, "down")

	r2 := newRig(t)
	r2.setVersion("0.64.21-nightly.B")
	r2.seedLedger(append(cleanLines(3), ledgerLines("clean", "0.64.21-nightly.B", 1)...)...)
	o, _ = r2.mustRun(0, "up")
	contains(t, "stdout", o, "D2: clean passes 1/3, mac-idle required")
	r2.mustRun(0, "down")

	r3 := newRig(t) // back to A after B lines: only the tail of A counts
	r3.seedLedger(append(append(cleanLines(3), ledgerLines("clean", "0.64.21-nightly.B", 2)...), cleanLines(1)...)...)
	o, _ = r3.mustRun(0, "up")
	contains(t, "stdout", o, "D2: clean passes 1/3, mac-idle required")
	r3.mustRun(0, "down")
}

func TestUpSkipsMacFreeAfterThreeCleanPasses(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	o, _ := r.mustRun(0, "up")
	contains(t, "stdout", o, "D2: clean passes 3/3, mac-idle optional")
	if len(r.callLines("mac-idle")) != 0 {
		t.Fatal("mac-idle called after 3 clean passes")
	}
	r.mustRun(0, "down")
	r.setMacFree(0)
	r.mustRun(0, "up", "--mac-idle")
	r.mustRun(0, "down")
	if r.countCalls("mac-idle", "") != 2 { // the plain call and its --release
		t.Fatalf("--mac-idle: calls %q", r.callLines("mac-idle"))
	}
	r.setMacFree(1)
	r.mustRun(4, "up", "--mac-idle")
}

func TestUpFailsWhenNightlyTakesFocus(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	wrapOpenSetsFront(r)
	_, e := r.mustRun(6, "up")
	contains(t, "stderr", e, "run: scripts/live-pass.sh down")
	if r.stateDir() == "" {
		t.Fatal("state not kept on exit 6")
	}
	r.setFront("Arc")
	r.mustRun(0, "down")
	if v, why := r.lastVerdict(); v != "dirty" || why != "nightly frontmost at up" {
		t.Fatalf("verdict %q %q", v, why)
	}
}

// wrapOpenSetsFront makes the fake open put cmux NIGHTLY frontmost.
func wrapOpenSetsFront(r *rig) {
	r.write("bin/open", 0o755, strings.Replace(fakeOpen, "exit 0\n", "printf 'cmux NIGHTLY' > \"$RIG/front\"\nexit 0\n", 1))
}

func TestUpTransientFrontmostIsNotNightly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.write("bin/open", 0o755, strings.Replace(fakeOpen, "exit 0\n", "printf 'NotificationCenter' > \"$RIG/front\"\nexit 0\n", 1))
	r.mustRun(0, "up")
	r.mustRun(0, "down")
}

func TestSamplerSeesNightlyFrontmost(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.setFront("cmux NIGHTLY")
	waitFor(t, "a sampler line in focus.log", 10*time.Second, func() bool {
		return strings.Contains(r.passFile("focus.log"), "sampler")
	})
	r.setFront("Arc")
	r.mustRun(0, "down")
	if v, why := r.lastVerdict(); v != "dirty" || why != "nightly frontmost (sampler)" {
		t.Fatalf("verdict %q %q", v, why)
	}
	o, _ := r.mustRun(0, "up")
	contains(t, "stdout", o, "D2: clean passes 0/3")
	r.mustRun(0, "down")
}

func TestUpReusesRunningNightly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pid := r.startNightly()
	r.mustRun(0, "up")
	if len(r.callLines("open")) != 0 {
		t.Fatal("open called for a running NIGHTLY")
	}
	if st := r.state(); st["NIGHTLY_LAUNCHED"] != "0" || st["NIGHTLY_PID"] != strconv.Itoa(pid) {
		t.Fatalf("state %v", st)
	}
	r.mustRun(0, "down")
	if !alive(pid) {
		t.Fatal("down stopped a reused NIGHTLY")
	}

	r2 := newRig(t)
	pid2 := r2.startNightly()
	r2.touch("pid-garbage")
	r2.mustRun(0, "up")
	if len(r2.callLines("open")) != 0 {
		t.Fatal("open called")
	}
	if st := r2.state(); st["NIGHTLY_LAUNCHED"] != "0" || st["NIGHTLY_PID"] != "" {
		t.Fatalf("state %v", st)
	}
	r2.mustRun(0, "down")
	if !alive(pid2) {
		t.Fatal("down stopped a reused NIGHTLY")
	}
}

func TestUpWaitsForNightlyPid(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.pidDelay = 3
	r.mustRun(0, "up")
	calls := r.calls()
	openAt := strings.Index(calls, "open -g")
	after := calls[openAt:]
	if n := strings.Count(after, "lsappinfo find bundleid=com.cmuxterm.app.nightly"); n < 4 {
		t.Fatalf("find after open: %d, want ≥ 4", n)
	}
	st := r.state()
	if st["NIGHTLY_PID"] != strconv.Itoa(r.nightlyPid()) || st["NIGHTLY_LAUNCHED"] != "1" {
		t.Fatalf("state %v (fake pid %d)", st, r.nightlyPid())
	}
	pidAt := strings.Index(after, "lsappinfo info -only pid")
	lwAt := strings.Index(after, "nightly list-windows")
	if pidAt < 0 || lwAt < 0 || lwAt < pidAt {
		t.Fatalf("list-windows (%d) before the pid read (%d)", lwAt, pidAt)
	}
	r.mustRun(0, "down")
}

// macOS 27 prints `info` as a block: the pid is a `pid = N` token inside it.
func TestUpReadsMacOS27PidBlock(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.touch("lsappinfo-v27")
	r.mustRun(0, "up")
	st := r.state()
	if st["NIGHTLY_PID"] != strconv.Itoa(r.nightlyPid()) || st["NIGHTLY_LAUNCHED"] != "1" {
		t.Fatalf("state %v (fake pid %d)", st, r.nightlyPid())
	}
	r.mustRun(0, "down")
}

func TestUpMacOS27PidUnreadable(t *testing.T) {
	t.Parallel()
	for _, garbage := range []string{"", "junk"} {
		r := newRig(t)
		r.touch("lsappinfo-v27")
		r.write("pid-garbage", 0o644, garbage)
		r.socketWait = 1
		_, e := r.mustRun(3, "up")
		contains(t, "stderr", e, "its pid was not read within 1 s")
	}
}

func TestUpNightlyPidUnreadable(t *testing.T) {
	t.Parallel()
	for _, garbage := range []string{"", "junk"} {
		r := newRig(t)
		r.write("pid-garbage", 0o644, garbage)
		before := r.ledger()
		r.socketWait = 1
		_, e := r.mustRun(3, "up")
		contains(t, "stderr", e, "cmux NIGHTLY launched but its pid was not read within 1 s — not proven stopped, quit it by hand")
		if r.stateDir() != "" || len(r.passDirs()) != 0 {
			t.Fatal("pass state left")
		}
		if r.ledger() != before {
			t.Fatal("ledger changed")
		}
	}
}

// --- down -----------------------------------------------------------------

func TestDownSignalsNeverOsascript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	st := r.state()
	sp, _ := strconv.Atoi(st["SAMPLER_PID"])
	r.mustRun(0, "down")
	waitFor(t, "fake NIGHTLY dead", 5*time.Second, func() bool { return !alive(r.nightlyPid()) })
	if r.countCalls("nightly", "close-window --window "+winW) != 1 || r.countCalls("nightly", "close-window --window "+winK) != 1 {
		t.Fatalf("close-window calls: %q", r.callLines("nightly"))
	}
	if len(r.callLines("osascript")) != 0 {
		t.Fatal("osascript called")
	}
	waitFor(t, "sampler dead", 5*time.Second, func() bool { return !alive(sp) })
	if len(r.passDirs()) != 0 || r.stateDir() != "" {
		t.Fatal("state left")
	}
	if _, err := os.Lstat(r.path("root/lp.current")); err == nil {
		t.Fatal("lp.current left")
	}
}

func TestDownRecordsCleanOnlyAfterStatus(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.waitReadable(r.focusMin)
	r.mustRun(0, "down")
	if v, why := r.lastVerdict(); v != "unmeasured" || why != "no status" {
		t.Fatalf("verdict %q %q", v, why)
	}
	r.mustRun(0, "up")
	r.mustRun(0, "status")
	r.waitReadable(r.focusMin)
	r.mustRun(0, "down")
	if v, _ := r.lastVerdict(); v != "clean" {
		t.Fatalf("verdict %q", v)
	}
}

func TestDownRecordsDirtyAfterIsolationFail(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.stopWait = 1
	r.mustRun(0, "up")
	r.setState("DAEMON_PID", strconv.Itoa(r.startStubborn()))
	r.mustRun(5, "down")
	if v, why := r.lastVerdict(); v != "dirty" || why != "isolation broken" {
		t.Fatalf("verdict %q %q", v, why)
	}

	r2 := newRig(t)
	r2.stopWait = 1
	r2.touch("nightly-stubborn")
	r2.mustRun(0, "up")
	before := r2.ledger()
	r2.mustRun(5, "down")
	if r2.ledger() != before {
		t.Fatal("a NIGHTLY survivor recorded the pass")
	}
	_ = syscall.Kill(r2.nightlyPid(), syscall.SIGKILL)
	waitFor(t, "stubborn NIGHTLY dead", 5*time.Second, func() bool { return !alive(r2.nightlyPid()) })
	r2.mustRun(0, "down")
	if v, why := r2.lastVerdict(); v != "dirty" || why != "isolation broken" {
		t.Fatalf("verdict %q %q", v, why)
	}
}

func TestDownReleasesMacFreeItAsked(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedLedger()
	r.mustRun(0, "up")
	if r.state()["MAC_FREE_ASKED"] != "1" {
		t.Fatal("MAC_FREE_ASKED not 1")
	}
	r.mustRun(0, "down")
	if r.countCalls("mac-idle", "--release") != 1 {
		t.Fatalf("releases: %q", r.callLines("mac-idle"))
	}
	r2 := newRig(t)
	r2.mustRun(0, "up")
	r2.mustRun(0, "down")
	if len(r2.callLines("mac-idle")) != 0 {
		t.Fatalf("mac-idle calls: %q", r2.callLines("mac-idle"))
	}
}

func TestMacFreeOnlyAtUp(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedLedger()
	r.mustRun(0, "up")
	r.setMacFree(1)
	r.mustRun(0, "status")
	r.waitReadable(r.focusMin)
	r.mustRun(0, "down")
	mf := r.callLines("mac-idle")
	if len(mf) != 2 || argsOf("mac-idle", mf[0]) != "" || argsOf("mac-idle", mf[1]) != "--release" {
		t.Fatalf("mac-idle calls: %q", mf)
	}
	if v, _ := r.lastVerdict(); v != "clean" {
		t.Fatalf("verdict %q", v)
	}
}

func TestStateWrittenBeforeFocusCheck(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedLedger()
	wrapOpenSetsFront(r)
	r.mustRun(6, "up")
	st := r.state()
	if st == nil || st["NIGHTLY_PID"] == "" || st["NIGHTLY_LAUNCHED"] != "1" || st["MAC_FREE_ASKED"] != "1" || st["W"] == "" || st["K"] == "" {
		t.Fatalf("state %v", st)
	}
	r.setFront("Arc")
	r.mustRun(0, "down")
	waitFor(t, "fake NIGHTLY dead", 5*time.Second, func() bool { return !alive(r.nightlyPid()) })
	if r.countCalls("mac-idle", "--release") != 1 {
		t.Fatal("mac-idle --release not called")
	}

	r2 := newRig(t)
	r2.touch("socket-dead")
	r2.socketWait = 1
	r2.mustRun(3, "up")
	waitFor(t, "fake NIGHTLY dead after exit 3", 5*time.Second, func() bool { return !alive(r2.nightlyPid()) })
}

func TestFocusUnreadableSampleIsSkipped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ front, reason string }{
		{"", "empty"}, {"@null", "unrecognized"}, {"@garbage", "unrecognized"},
	} {
		t.Run(tc.reason+tc.front, func(t *testing.T) {
			r := newRig(t)
			r.mustRun(0, "up")
			r.mustRun(0, "status")
			r.setFront(tc.front)
			r.waitSamplerTicks(1) // a sample in flight before the switch lands first
			readable := lineCount(r.passFile("focus-readable.log"))
			r.waitSamplerTicks(2)
			if now := lineCount(r.passFile("focus-readable.log")); now != readable {
				t.Fatalf("an unreadable sample was counted: %d → %d", readable, now)
			}
			contains(t, "focus-unmeasured.log", r.passFile("focus-unmeasured.log"), "sampler "+tc.reason)
			r.setFront("Arc")
			r.waitReadable(r.focusMin)
			r.mustRun(0, "down")
			if v, _ := r.lastVerdict(); v != "clean" {
				t.Fatalf("verdict %q", v)
			}
		})
	}
}

func TestFocusUnreadableThenNightlyIsDirty(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.setFront("")
	r.waitSamplerTicks(2)
	r.setFront("cmux NIGHTLY")
	waitFor(t, "sampler sees NIGHTLY", 10*time.Second, func() bool {
		return strings.Contains(r.passFile("focus.log"), "sampler")
	})
	r.setFront("Arc")
	r.mustRun(0, "down")
	if v, why := r.lastVerdict(); v != "dirty" || why != "nightly frontmost (sampler)" {
		t.Fatalf("verdict %q %q", v, why)
	}
}

func TestPassCleanWithLockedSamplesSkipped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.touch("locked")
	r.waitSamplerTicks(1)
	r.setFront("loginwindow")
	r.remove("locked")
	r.waitSamplerTicks(2)
	r.setFront("Arc")
	r.mustRun(0, "status")
	r.waitReadable(r.focusMin)
	um := r.passFile("focus-unmeasured.log")
	contains(t, "focus-unmeasured.log", um, "loginwindow")
	r.mustRun(0, "down")
	if v, _ := r.lastVerdict(); v != "clean" {
		t.Fatalf("verdict %q", v)
	}
}

func TestPassUnmeasuredBelowReadableQuota(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.focusMin = 10
	r.mustRun(0, "up")
	r.touch("locked")
	r.mustRun(0, "status")
	r.waitSamplerTicks(2)
	r.mustRun(0, "down")
	v, why := r.lastVerdict()
	if v != "unmeasured" || !regexp.MustCompile(`^readable focus samples \d+/10$`).MatchString(why) {
		t.Fatalf("verdict %q %q", v, why)
	}
	r.remove("locked")
	r.mustRun(0, "up")
	r.waitReadable(r.focusMin)
	r.mustRun(0, "down")
	if v, why := r.lastVerdict(); v != "unmeasured" || why != "no status" {
		t.Fatalf("verdict %q %q", v, why)
	}
	// The ledger was neither counted nor reset: still 3 clean passes.
	o, _ := r.mustRun(0, "up")
	contains(t, "stdout", o, "D2: clean passes 3/3")
	r.mustRun(0, "down")
	// The script's own default quota is 60 (D5).
	r.focusMin = 0
	env := []string{}
	for _, kv := range r.env() {
		if !strings.HasPrefix(kv, "LIVEPASS_FOCUS_MIN=") {
			env = append(env, kv)
		}
	}
	o, _, code, err := runBash(scriptPath(t), env, "help")
	if err != nil || code != 0 {
		t.Fatalf("help: %v %d", err, code)
	}
	contains(t, "help", o, "readable focus quota, LIVEPASS_FOCUS_MIN")
	contains(t, "help", o, "60 readable focus samples")
}

func TestFocusUnmeasuredAtUp(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setFront("")
	o, _ := r.mustRun(0, "up")
	contains(t, "stdout", o, "focus: not measured (empty)")
	if n := lineCount(r.passFile("focus-readable.log")); n != 0 {
		t.Fatalf("an unreadable up sample was counted (%d)", n)
	}
	r.setFront("Arc")
	r.mustRun(0, "status")
	r.waitReadable(r.focusMin)
	r.mustRun(0, "down")
	if v, _ := r.lastVerdict(); v != "clean" {
		t.Fatalf("verdict %q", v)
	}
}

func TestLedgerDefaultPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.defaultLedger = true
	r.extraEnv = []string{"XDG_STATE_HOME=" + r.path("xdg")}
	r.mustRun(0, "up")
	r.mustRun(0, "status")
	r.mustRun(0, "down")
	if _, err := os.Stat(r.path("xdg/pigtail-live-pass/ledger")); err != nil {
		t.Fatal(err)
	}
	r.extraEnv = nil
	r.mustRun(0, "up")
	r.mustRun(0, "down")
	if _, err := os.Stat(r.path("home/.local/state/pigtail-live-pass/ledger")); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerSkipsUnmeasured(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lines []string
		want  string
	}{
		{[]string{"clean", "clean", "unmeasured", "clean"}, "D2: clean passes 3/3, mac-idle optional"},
		{[]string{"clean", "unmeasured", "unmeasured"}, "D2: clean passes 1/3, mac-idle required"},
		{[]string{"dirty", "unmeasured", "clean"}, "D2: clean passes 1/3, mac-idle required"},
	} {
		r := newRig(t)
		var lines []string
		for _, v := range tc.lines {
			lines = append(lines, ledgerLines(v, versionA, 1)...)
		}
		r.seedLedger(lines...)
		o, _ := r.mustRun(0, "up")
		contains(t, "stdout", o, tc.want)
		r.mustRun(0, "down")
	}
}

func TestDownEscalatesStubbornDaemon(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.stopWait = 1
	r.mustRun(0, "up")
	pid := r.startStubborn()
	r.setState("DAEMON_PID", strconv.Itoa(pid))
	_, e := r.mustRun(5, "down")
	contains(t, "stderr", e, fmt.Sprintf("live-pass: branch daemon ignored SIGTERM, killed (pid %d)", pid))
	waitFor(t, "stubborn daemon dead", 5*time.Second, func() bool { return !alive(pid) })
	if r.countCalls("nightly", "close-window") != 2 {
		t.Fatal("W/K not closed")
	}
	waitFor(t, "NIGHTLY dead", 5*time.Second, func() bool { return !alive(r.nightlyPid()) })
	if len(r.passDirs()) != 0 {
		t.Fatal("state kept")
	}
}

func TestDownReportsNightlySurvivor(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.stopWait = 1
	r.touch("nightly-stubborn")
	r.mustRun(0, "up")
	pid := r.nightlyPid()
	_, e := r.mustRun(5, "down")
	contains(t, "stderr", e, fmt.Sprintf("live-pass: cmux NIGHTLY survived SIGTERM (pid %d)", pid))
	contains(t, "stderr", e, fmt.Sprintf("run: kill %d, then scripts/live-pass.sh down", pid))
	if !alive(pid) {
		t.Fatal("stubborn NIGHTLY dead")
	}
	if st := r.state(); st == nil || st["NIGHTLY_PID"] != strconv.Itoa(pid) {
		t.Fatalf("state %v", st)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	waitFor(t, "NIGHTLY dead", 5*time.Second, func() bool { return !alive(pid) })
	r.mustRun(0, "down")
}

func TestDownWithoutPass(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, e := r.mustRun(2, "down")
	contains(t, "stderr", e, "live-pass: no live pass is up")
}

// --- C7/C9 guard ----------------------------------------------------------

var (
	toolNames   = []string{"open", "osascript", "lsappinfo", "ioreg", "mac-idle", "cmux"}
	defaultLine = regexp.MustCompile(`^\s*: "\$\{LIVEPASS_[A-Z_]+:=.*\}"\s*$`)
)

// bareToolCalls lists the script lines that invoke a driven tool bare, at a
// command position outside quotes (a quoted "$LIVEPASS_X" word is the allowed
// form), skipping comments, heredoc bodies and the `: "${LIVEPASS_…:=…}"`
// defaults. An empty script, or one with no "$LIVEPASS_OPEN" at all, is an
// error (C11 c7-guard): a blind detector never reads as « no bare call ».
func bareToolCalls(script string) ([]string, error) {
	if strings.TrimSpace(script) == "" || !strings.Contains(script, `"$LIVEPASS_OPEN"`) {
		return nil, errors.New("script read empty or detector blind")
	}
	var bad []string
	heredoc := ""
	for _, line := range strings.Split(script, "\n") {
		if heredoc != "" {
			if strings.TrimSpace(line) == heredoc {
				heredoc = ""
			}
			continue
		}
		if m := heredocStart.FindStringSubmatch(line); m != nil {
			heredoc = m[1]
		}
		if defaultLine.MatchString(line) {
			continue
		}
		for _, w := range commandWords(line) {
			for _, tool := range toolNames {
				if w == tool {
					bad = append(bad, line)
				}
			}
		}
	}
	return bad, nil
}

var heredocStart = regexp.MustCompile(`<<-?\s*'?"?([A-Z_]+)'?"?`)

var shellKeywords = map[string]bool{"if": true, "then": true, "do": true, "else": true, "elif": true,
	"while": true, "until": true, "!": true, "exec": true, "command": true, "time": true, "{": true}

// commandWords returns the unquoted words at a command position of one line:
// its start, after ; | & ( ) newline, and inside $( … ) — also within double
// quotes. Assignments and keywords before the command are skipped.
func commandWords(line string) []string {
	type ctx byte // 't' unquoted (command level), 'd' double quotes
	stack := []ctx{'t'}
	var out []string
	atCmd := true
	i := 0
	for i < len(line) {
		c := line[i]
		top := stack[len(stack)-1]
		if top == 'd' {
			switch {
			case c == '\\':
				i += 2
			case c == '"':
				stack = stack[:len(stack)-1]
				i++
			case c == '$' && i+1 < len(line) && line[i+1] == '(':
				stack = append(stack, 't')
				atCmd = true
				i += 2
			default:
				i++
			}
			continue
		}
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return out
		case c == '\'':
			j := strings.IndexByte(line[i+1:], '\'')
			if j < 0 {
				return out
			}
			i += j + 2
			atCmd = false
		case c == '"':
			stack = append(stack, 'd')
			atCmd = false
			i++
		case c == '$' && i+1 < len(line) && line[i+1] == '(':
			stack = append(stack, 't')
			atCmd = true
			i += 2
		case c == ')':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			atCmd = true
			i++
		case c == ';' || c == '|' || c == '&' || c == '(':
			atCmd = true
			i++
		default:
			j := i
			for j < len(line) && !strings.ContainsRune(" \t;|&()\"'", rune(line[j])) {
				j++
			}
			w := line[i:j]
			if atCmd {
				if shellKeywords[w] || (strings.Contains(w, "=") && !strings.HasPrefix(w, "-")) {
					// still at a command position
				} else {
					out = append(out, w)
					atCmd = false
				}
			}
			if j == i {
				j++
			}
			i = j
		}
	}
	return out
}

func TestScriptCallsToolsOnlyThroughVariables(t *testing.T) {
	t.Parallel()
	if _, err := bareToolCalls(""); err == nil {
		t.Fatal("bareToolCalls on an empty script must fail")
	}
	if _, err := bareToolCalls("echo hi\n"); err == nil {
		t.Fatal("bareToolCalls on a script without \"$LIVEPASS_OPEN\" must fail")
	}
	bad, err := bareToolCalls("x() { \"$LIVEPASS_OPEN\" -g; }\nopen -g -a x\n")
	if err != nil || len(bad) != 1 || bad[0] != "open -g -a x" {
		t.Fatalf("detector missed a planted bare open: %q %v", bad, err)
	}
	b, err := os.ReadFile(scriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	bad, err = bareToolCalls(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("bare tool calls:\n%s", strings.Join(bad, "\n"))
	}
	notContains(t, "script", script, "osascript")
	for _, line := range strings.Split(script, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") {
			continue
		}
		if strings.Contains(line, "$CMUX_BUNDLED_CLI_PATH") || strings.Contains(line, "${CMUX_BUNDLED_CLI_PATH") {
			t.Errorf("CMUX_BUNDLED_CLI_PATH read: %s", line)
		}
	}
	// $LIVEPASS_NIGHTLY_CLI / $LIVEPASS_MAIN_CLI only inside nightly_cli and
	// main_cli (and their defaults); CMUX_BUNDLED_CLI_PATH only exported.
	fn := ""
	for _, line := range strings.Split(script, "\n") {
		if m := regexp.MustCompile(`^([a-z_]+)\(\) \{`).FindStringSubmatch(line); m != nil {
			fn = m[1]
		}
		if defaultLine.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, "$LIVEPASS_NIGHTLY_CLI") && fn != "nightly_cli" && !strings.Contains(line, "CMUX_BUNDLED_CLI_PATH=") {
			t.Errorf("$LIVEPASS_NIGHTLY_CLI outside nightly_cli (%s): %s", fn, line)
		}
		if strings.Contains(line, "$LIVEPASS_MAIN_CLI") && fn != "main_cli" {
			t.Errorf("$LIVEPASS_MAIN_CLI outside main_cli (%s): %s", fn, line)
		}
	}
}

func TestFrontSubcommand(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	o, _ := r.mustRun(0, "front")
	if strings.TrimSpace(o) != "Arc" {
		t.Fatalf("front: %q", o)
	}
	r.setFront("")
	o, _ = r.mustRun(1, "front")
	contains(t, "front", o, "not measured: empty")
	r.setFront("Arc")
	r.touch("locked")
	o, _ = r.mustRun(1, "front")
	contains(t, "front", o, "not measured: screen locked")
	r.remove("locked")
	r.touch("ioreg-empty")
	o, _ = r.mustRun(1, "front")
	contains(t, "front", o, "not measured: lock state unreadable")
}

func TestFrontSubcommandMacOS27(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.touch("lsappinfo-v27")
	o, _ := r.mustRun(0, "front")
	if strings.TrimSpace(o) != "Arc" {
		t.Fatalf("front: %q", o)
	}
	for _, f := range []string{"@null", "@garbage"} {
		r.setFront(f)
		o, _ = r.mustRun(1, "front")
		contains(t, "front", o, "not measured: unrecognized")
	}
}

func TestScriptSyntax(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("bash", "-n", scriptPath(t)).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

// --- Task 3: branch daemon, env, tab ---------------------------------------

func passFlags(r *rig, st map[string]string) []string {
	dir := r.stateDir()
	return []string{"-socket", dir + "/pigtaild.sock", "-db", dir + "/pigtail.db", "-http=", "-outputs-http=",
		"-cmux-socket", r.nightlySocket(), "-cmux-sidebar", "-cmux-folders",
		"-cmux-sidebar-window", st["W"], "-cmux-sidebar-window", st["K"],
		"-refresh-main=false", "-watchdog=false", "-ram-budget=false", "-cmux-check-after-relaunch=off", "-orphan-reaper=off", "-tmp-sweep=off"}
}

func TestDaemonStartPassesIsolationFlags(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pd := r.writePigtaild()
	r.mustRun(0, "up")
	r.mustRun(0, "daemon", "start", "--pigtaild", pd, "--", "-cmux-folder-sort=false")
	st := r.state()
	want := strings.Join(append(passFlags(r, st), "-cmux-folder-sort=false"), " ")
	runs := r.callLines("pigtaild-run")
	if len(runs) != 1 || strings.TrimPrefix(runs[0], "pigtaild-run ") != want {
		t.Fatalf("pigtaild argv:\n%q\nwant\n%q", runs, want)
	}
	if st["DAEMON_PID"] == "" {
		t.Fatal("DAEMON_PID not written")
	}
	var cmuxVars []string
	xdg, sockSeen := "", false
	for _, kv := range r.daemonEnv(st["DAEMON_PID"]) {
		switch {
		case strings.HasPrefix(kv, "CMUX_"):
			cmuxVars = append(cmuxVars, kv)
		case strings.HasPrefix(kv, "XDG_STATE_HOME="):
			xdg = strings.TrimPrefix(kv, "XDG_STATE_HOME=")
		case strings.HasPrefix(kv, "PIGTAIL_SOCKET="):
			sockSeen = true
		}
	}
	if len(cmuxVars) != 1 || cmuxVars[0] != "CMUX_BUNDLED_CLI_PATH="+r.nightlyCLI() {
		t.Fatalf("daemon CMUX_* env: %q", cmuxVars)
	}
	if xdg != r.stateDir()+"/xdg-state" {
		t.Fatalf("XDG_STATE_HOME=%q", xdg)
	}
	if sockSeen {
		t.Fatal("PIGTAIL_SOCKET inherited by the branch daemon")
	}
	r.mustRun(0, "down")
}

func TestDaemonStartRefusesOldPigtaild(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pd := r.writePigtaild()
	r.mustRun(0, "up")
	r.touch("old-pigtaild")
	_, e := r.mustRun(2, "daemon", "start", "--pigtaild", pd)
	contains(t, "stderr", e, "live-pass: pigtaild refused the pass flags (-check-args): flag provided but not defined: -cmux-socket — a pigtaild without -cmux-socket would drive the main cmux")
	if len(r.callLines("pigtaild-run")) != 0 {
		t.Fatal("a daemon was started")
	}
	// Positive control: with an up-to-date double, -check-args gets exactly
	// the argv of the real start that follows.
	r.remove("old-pigtaild")
	r.mustRun(0, "daemon", "start", "--pigtaild", pd, "--", "-cmux-folder-sort=false")
	checks := r.callLines("pigtaild-check")
	runs := r.callLines("pigtaild-run")
	if len(runs) != 1 || len(checks) != 2 || strings.TrimPrefix(checks[1], "pigtaild-check ") != strings.TrimPrefix(runs[0], "pigtaild-run ") {
		t.Fatalf("check %q vs run %q", checks, runs)
	}
	r.mustRun(0, "down")
}

func TestDaemonStartNeedsCheckArgsConfirmation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pd := r.writePigtaild()
	r.mustRun(0, "up")
	r.touch("silent-check-args")
	_, e := r.mustRun(2, "daemon", "start", "--pigtaild", pd)
	contains(t, "stderr", e, "live-pass: pigtaild -check-args did not confirm the pass flags")
	if len(r.callLines("pigtaild-run")) != 0 {
		t.Fatal("a daemon was started")
	}
	r.mustRun(0, "down")
}

func TestDaemonStartReportsDeathAtBirth(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pd := r.writePigtaild()
	r.mustRun(0, "up")
	r.touch("die-at-birth")
	_, e := r.mustRun(3, "daemon", "start", "--pigtaild", pd)
	contains(t, "stderr", e, "fatal: open store: database is locked (fake)")
	if st := r.state(); st["DAEMON_PID"] != "" {
		t.Fatalf("DAEMON_PID %q left", st["DAEMON_PID"])
	}
	r.mustRun(0, "down")
}

func TestDaemonStartRefusesSecondDaemon(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pd := r.writePigtaild()
	r.mustRun(0, "up")
	r.mustRun(0, "daemon", "start", "--pigtaild", pd)
	pid := r.state()["DAEMON_PID"]
	_, e := r.mustRun(2, "daemon", "start", "--pigtaild", pd)
	contains(t, "stderr", e, "branch daemon already running (pid "+pid+")")
	r.mustRun(0, "down")
}

func TestDaemonStopLeavesNoProcess(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pd := r.writePigtaild()
	r.mustRun(0, "up")
	r.mustRun(0, "daemon", "start", "--pigtaild", pd, "--", "-cmux-folder-sort=false")
	pid, _ := strconv.Atoi(r.state()["DAEMON_PID"])
	r.mustRun(0, "daemon", "stop")
	waitFor(t, "branch daemon dead", 5*time.Second, func() bool { return !alive(pid) })
	if st := r.state(); st["DAEMON_PID"] != "" {
		t.Fatalf("DAEMON_PID %q", st["DAEMON_PID"])
	}
	r.mustRun(0, "daemon", "start", "--pigtaild", pd, "--", "-cmux-folder-sort=true")
	runs := r.callLines("pigtaild-run")
	if len(runs) != 2 || !strings.HasSuffix(runs[1], "-cmux-folder-sort=true") {
		t.Fatalf("runs %q", runs)
	}
	pid2, _ := strconv.Atoi(r.state()["DAEMON_PID"])
	if pid2 == pid || !alive(pid2) {
		t.Fatalf("restart: pid %d → %d", pid, pid2)
	}
	r.mustRun(0, "down")
	waitFor(t, "restarted daemon dead after down", 5*time.Second, func() bool { return !alive(pid2) })
}

func TestEnvPrintsQuotedExports(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	o, _ := r.mustRun(0, "env")
	out, err := exec.Command("bash", "-c", o+`
printf '%s\n' "$CMUX_BUNDLED_CLI_PATH" "$PIGTAIL_SOCKET" "$CMUX_SOCKET_PATH" "$LIVEPASS_W" "$LIVEPASS_K" "${XDG_STATE_HOME-unset}"`).Output()
	if err != nil {
		t.Fatalf("eval env: %v\n%s", err, o)
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := []string{r.nightlyCLI(), r.stateDir() + "/pigtaild.sock", r.nightlySocket(), winW, winK, "unset"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("env:\n%q\nwant\n%q", got, want)
	}
	notContains(t, "env output", o, "XDG_STATE_HOME")
	r.mustRun(0, "down")
}

func TestTabCarriesEnvAndClaudeConfig(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.extraEnv = []string{"CLAUDE_CONFIG_DIR=/x/cell"}
	r.mustRun(0, "up")
	o, _ := r.mustRun(0, "tab", "W", "--claude")
	contains(t, "tab stdout", o, "OK workspace:")
	tabs := r.callLines("nightly new-workspace")
	if len(tabs) != 1 {
		t.Fatalf("new-workspace calls %q", r.callLines("nightly"))
	}
	args := argsOf("nightly", tabs[0])
	for _, want := range []string{"--window " + winW, "--focus false", "--env PIGTAIL_SOCKET=" + r.stateDir() + "/pigtaild.sock",
		"--env CMUX_SOCKET_PATH=" + r.nightlySocket(), "--env CMUX_BUNDLED_CLI_PATH=" + r.nightlyCLI(),
		"--env CLAUDE_CONFIG_DIR=/x/cell", "--command claude"} {
		contains(t, "new-workspace argv", args, want)
	}
	for _, bad := range []string{"CMUX_SURFACE_ID", "CMUX_WORKSPACE_ID", "CMUX_SOCKET="} {
		notContains(t, "new-workspace argv", args, bad)
	}
	if env := envOf(tabs[0]); len(env) != 1 || env[0] != "CMUX_SOCKET_PATH="+r.nightlySocket() {
		t.Fatalf("new-workspace env %q", env)
	}
	for _, c := range args {
		if c > 0x7e {
			t.Fatalf("non-ASCII argv: %q", args)
		}
	}
	r.mustRun(0, "tab", "K")
	tabs = r.callLines("nightly new-workspace")
	if len(tabs) != 2 || strings.Contains(argsOf("nightly", tabs[1]), "--command") || !strings.Contains(tabs[1], "--window "+winK) {
		t.Fatalf("tab K: %q", tabs)
	}
	r.mustRun(0, "tab", "W", "--claude-no-skills")
	tabs = r.callLines("nightly new-workspace")
	if len(tabs) != 3 || !strings.Contains(argsOf("nightly", tabs[2]), "--command claude --disable-slash-commands") {
		t.Fatalf("tab --claude-no-skills: %q", tabs)
	}
	_, e := r.mustRun(2, "tab", "X")
	contains(t, "stderr", e, "live-pass:")
	r.mustRun(0, "down")
}

// --- Task 4: status proves the isolation (C5, C11) --------------------------

var statusIDs = []string{"pass-ids", "live-log", "main-listing", "branch-aim", "branch-log", "focus-now", "sampler", "main-key"}

// statusLines maps each check id of a status output to its line, and returns
// the last line.
func statusLines(out string) (map[string]string, string) {
	m := map[string]string{}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, l := range lines {
		for _, id := range statusIDs {
			if strings.HasPrefix(l, id+": ") {
				m[id] = l
			}
		}
	}
	return m, lines[len(lines)-1]
}

// statusLineIDs are the ids of every `<id>: ok|FAIL|not measured` line.
func statusLineIDs(out string) []string {
	var ids []string
	re := regexp.MustCompile(`^([a-z-]+): (ok$|FAIL |not measured: )`)
	for _, l := range strings.Split(out, "\n") {
		if m := re.FindStringSubmatch(l); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// appendPassLog adds a line to the branch daemon's log.
func (r *rig) appendPassLog(line string) {
	r.t.Helper()
	f, err := os.OpenFile(filepath.Join(r.stateDir(), "pigtaild.log"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintln(f, line); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) waitSamplerReadable() {
	r.t.Helper()
	waitFor(r.t, "a readable sampler sample", 10*time.Second, func() bool {
		return strings.Contains(r.passFile("focus-readable.log"), " sampler ")
	})
}

// greenRig is the TestStatusGreen scenario: every proof established.
func greenRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	pd := r.writePigtaild()
	r.setFront("cmux")
	r.mustRun(0, "up")
	r.mustRun(0, "daemon", "start", "--pigtaild", pd)
	r.appendLiveLog("2026/10/20 10:00:01 cmuxsync: claude_code Running → Idle on " + mainWs1)
	r.appendPassLog("2026/10/20 10:00:02 cmuxsync: filed " + wsW + " in Inbox")
	r.waitSamplerReadable()
	return r
}

func TestStatusGreen(t *testing.T) {
	t.Parallel()
	r := greenRig(t)
	o, e := r.mustRun(0, "status")
	ids := statusLineIDs(o)
	if strings.Join(ids, " ") != strings.Join(statusIDs, " ") {
		t.Fatalf("status lines %q, want %q\n%s", ids, statusIDs, o)
	}
	m, last := statusLines(o)
	for _, id := range statusIDs {
		if m[id] != id+": ok" {
			t.Errorf("%s: %q\nstderr: %s", id, m[id], e)
		}
	}
	if last != "isolation: proven" {
		t.Fatalf("last line %q", last)
	}
	st := r.state()
	for _, want := range []string{versionA, winW, winK, "pid " + st["DAEMON_PID"], r.stateDir() + "/pigtaild.log", "D2: clean passes 3/3"} {
		contains(t, "status stdout", o, want)
	}
	r.mustRun(0, "down")
}

func TestStatusFlagsLiveDaemonTouchingPass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		id   func(r *rig) string
		tab  bool
	}{
		{"workspace of W", func(*rig) string { return wsW }, false},
		{"lower case", func(*rig) string { return strings.ToLower(wsW) }, false},
		{"tab K workspace", func(*rig) string { return "CCCCCCCC-0000-4000-8000-000000000001" }, true},
		{"window W", func(*rig) string { return winW }, false},
		{"NIGHTLY default window", func(*rig) string { return winDef }, false},
		{"workspace of NIGHTLY default window", func(*rig) string { return wsDef }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.appendLiveLog("before up: " + wsW + " " + winW) // before the offset: never counted
			r.mustRun(0, "up")
			if tc.tab {
				r.mustRun(0, "tab", "K")
			}
			r.appendLiveLog("2026/10/20 10:00:01 cmuxsync: claude_code Running → Idle on " + tc.id(r))
			_, e := r.mustRun(7, "status")
			contains(t, "stderr", strings.ToUpper(e), strings.ToUpper("live pigtaild wrote about a pass workspace or window ("+tc.id(r)+")"))
			r.mustRun(0, "down")
		})
	}
	// Control: the same lines before up only → no FAIL.
	r := newRig(t)
	r.appendLiveLog("before up: " + wsW + " " + winW)
	r.mustRun(0, "up")
	r.appendLiveLog("later: " + mainWs1)
	r.mustRun(0, "status")
	r.mustRun(0, "down")
}

func TestStatusIsolationFailMakesPassDirty(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.appendLiveLog("cmuxsync: claude_code patch: list-status " + wsW)
	r.mustRun(7, "status")
	r.mustRun(0, "down")
	if v, why := r.lastVerdict(); v != "dirty" || why != "isolation broken" {
		t.Fatalf("verdict %q %q", v, why)
	}
	o, _ := r.mustRun(0, "up")
	contains(t, "stdout", o, "D2: clean passes 0/3")
	r.mustRun(0, "down")
}

func notProven(t *testing.T, o, id, wantLine string) {
	t.Helper()
	m, last := statusLines(o)
	if m[id] != wantLine {
		t.Fatalf("%s line %q, want %q\n%s", id, m[id], wantLine, o)
	}
	if !strings.HasPrefix(last, "isolation: partial (not measured:") || !strings.Contains(last, id) {
		t.Fatalf("last line %q", last)
	}
}

func TestStatusNotMeasuredWhenLiveLogStill(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	o, _ := r.mustRun(0, "status")
	notProven(t, o, "live-log", "live-log: not measured: live pigtaild log did not move since up")
	r.mustRun(0, "down")
}

func TestStatusNotMeasuredWhenLiveLogNamesNoWorkspace(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.appendLiveLog("2026/10/20 10:00:01 rammon: tick")
	o, _ := r.mustRun(0, "status")
	notProven(t, o, "live-log", "live-log: not measured: live pigtaild log names no workspace of the user since up")
	r.mustRun(0, "down")
}

func TestStatusNotMeasuredFocus(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.setFront("")
	o, _ := r.mustRun(0, "status")
	notProven(t, o, "focus-now", "focus-now: not measured: focus (empty)")
	r.setFront("Arc")
	r.mustRun(0, "down")
}

func TestStatusWithoutBranchDaemonIsPartial(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	o, _ := r.mustRun(0, "status")
	notProven(t, o, "branch-aim", "branch-aim: not measured: branch daemon not started")
	notProven(t, o, "branch-log", "branch-log: not measured: branch daemon not started")
	r.waitReadable(r.focusMin)
	r.mustRun(0, "down")
	if v, _ := r.lastVerdict(); v == "dirty" {
		t.Fatal("a status without branch daemon made the pass dirty")
	}
	// Control: a started daemon without the aim line is a FAIL.
	r2 := newRig(t)
	pd := r2.writePigtaild()
	r2.touch("no-aim-line")
	r2.mustRun(0, "up")
	r2.mustRun(0, "daemon", "start", "--pigtaild", pd)
	o, _ = r2.mustRun(7, "status")
	if m, _ := statusLines(o); !strings.HasPrefix(m["branch-aim"], "branch-aim: FAIL ") {
		t.Fatalf("branch-aim %q", m["branch-aim"])
	}
	r2.mustRun(0, "down")
}

func TestStatusBranchLogNeedsPassId(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pd := r.writePigtaild()
	r.mustRun(0, "up")
	r.mustRun(0, "daemon", "start", "--pigtaild", pd)
	o, _ := r.mustRun(0, "status")
	notProven(t, o, "branch-log", "branch-log: not measured: branch pigtaild log names no pass workspace")
	r.appendPassLog("cmuxsync: filed " + wsK)
	o, _ = r.mustRun(0, "status")
	if m, _ := statusLines(o); m["branch-log"] != "branch-log: ok" {
		t.Fatalf("branch-log %q", m["branch-log"])
	}
	r.mustRun(0, "down")
}

func TestStatusPassIdsNotMeasured(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.touch("pass-ws-empty-" + winW)
	r.mustRun(0, "up")
	r.appendLiveLog("cmuxsync: claude_code Running → Idle on " + mainWs1)
	o, _ := r.mustRun(0, "status")
	notProven(t, o, "pass-ids", "pass-ids: not measured: nightly listed no workspace of W")
	m, _ := statusLines(o)
	if !strings.HasPrefix(m["live-log"], "live-log: not measured: ") {
		t.Fatalf("live-log %q", m["live-log"])
	}
	r.mustRun(0, "down")
}

func TestStatusSamplerNotMeasured(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setFront("")
	r.mustRun(0, "up")
	sp, _ := strconv.Atoi(r.state()["SAMPLER_PID"])
	o, _ := r.mustRun(0, "status")
	notProven(t, o, "sampler", "sampler: not measured: sampler (no readable sample)")
	_ = syscall.Kill(sp, syscall.SIGKILL)
	waitFor(t, "sampler dead", 5*time.Second, func() bool { return !alive(sp) })
	o, _ = r.mustRun(0, "status")
	notProven(t, o, "sampler", "sampler: not measured: sampler (dead)")
	r.mustRun(0, "down")
}

func TestStatusMainKeyNotMeasuredWithoutKeyField(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setFront("cmux")
	r.mustRun(0, "up")
	r.setMainWindows(`[{"id" : "` + mainWin + `", "index" : 0}]`)
	o, _ := r.mustRun(0, "status")
	notProven(t, o, "main-key", "main-key: not measured: main key (listing has no key field)")
	r.mustRun(0, "down")
}

func TestUpRefusesEmptyMainWorkspaces(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.touch("main-ws-empty")
	_, e := r.mustRun(3, "up")
	contains(t, "stderr", e, "main cmux listed no workspace of the user")
	if len(r.callLines("open")) != 0 || r.nightlyPid() != 0 {
		t.Fatal("NIGHTLY launched")
	}
	if r.stateDir() != "" || len(r.passDirs()) != 0 {
		t.Fatal("pass state left")
	}
}

func TestStatusFlagsBranchDaemonTouchingMain(t *testing.T) {
	t.Parallel()
	for _, id := range []string{mainWs2, mainWin} {
		r := newRig(t)
		pd := r.writePigtaild()
		r.mustRun(0, "up")
		r.mustRun(0, "daemon", "start", "--pigtaild", pd)
		r.appendPassLog("cmuxsync: filed " + id)
		_, e := r.mustRun(7, "status")
		contains(t, "stderr", e, "branch daemon wrote about a main workspace or window ("+id+")")
		r.mustRun(0, "down")
	}
}

func TestStatusFailsWithoutAimLine(t *testing.T) {
	t.Parallel()
	sock := ""
	for _, line := range []string{
		"",
		"2026/10/20 10:00:00 cmux: every call aimed at socket /tmp/other.sock (-cmux-socket)",
		"2026/10/20 10:00:00 cmux: every call aimed at socket {sock}2 (-cmux-socket)",
		"2026/10/20 10:00:00 cmux: every call aimed at socket {sock} (-cmux-socket) extra",
	} {
		r := newRig(t)
		sock = r.nightlySocket()
		pd := r.writePigtaild()
		r.touch("no-aim-line")
		r.mustRun(0, "up")
		r.mustRun(0, "daemon", "start", "--pigtaild", pd)
		if line != "" {
			r.appendPassLog(strings.ReplaceAll(line, "{sock}", sock))
		}
		_, e := r.mustRun(7, "status")
		contains(t, "stderr", e, `branch daemon log has no "cmux: every call aimed at socket `+sock+` (-cmux-socket)" line`)
		// Positive control: the exact line, after log.Printf's timestamp, passes.
		r.appendPassLog("2026/10/20 10:00:03 cmux: every call aimed at socket " + sock + " (-cmux-socket)")
		o, _, _ := r.run("status")
		if m, _ := statusLines(o); m["branch-aim"] != "branch-aim: ok" {
			t.Fatalf("exact aim line: %q", m["branch-aim"])
		}
		r.mustRun(0, "down")
	}
}

func TestStatusFlagsPassWindowInMainListing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.setMainWindows(`[{"id" : "` + mainWin + `", "key" : true}, {"id" : "` + winW + `", "key" : false}]`)
	_, e := r.mustRun(7, "status")
	contains(t, "stderr", e, "main cmux lists a pass window ("+winW+")")
	r.mustRun(0, "down")
}

func TestStatusFailsOnMainListingError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.touch("main-list-fails")
	_, e := r.mustRun(7, "status")
	contains(t, "stderr", e, "main cmux listing failed")
	r.remove("main-list-fails")
	r.mustRun(0, "down")
}

func TestStatusFailsOnEmptyMainListing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.setMainWindows(`{"windows":[]}`)
	_, e := r.mustRun(7, "status")
	contains(t, "stderr", e, "main cmux listing has no window of the user")
	r.mustRun(0, "down")
}

func TestStatusFlagsMainKeyLost(t *testing.T) {
	t.Parallel()
	two := func(aKey, bKey bool) string {
		return fmt.Sprintf(`[{"id" : "%s", "key" : %v}, {"id" : "%s", "key" : %v}]`, mainWin, aKey, mainWin2, bKey)
	}
	r := newRig(t)
	r.setMainWindows(two(true, false))
	r.setFront("cmux")
	r.mustRun(0, "up")
	r.setMainWindows(two(false, true)) // the user moved to their other window
	o, _ := r.mustRun(0, "status")
	if m, _ := statusLines(o); m["main-key"] != "main-key: ok" {
		t.Fatalf("main-key %q", m["main-key"])
	}
	if r.state()["ISOLATION_BROKEN"] == "1" {
		t.Fatal("ISOLATION_BROKEN set")
	}
	r.setMainWindows(two(false, false))
	_, e := r.mustRun(7, "status")
	contains(t, "stderr", e, "no main cmux window is key while cmux is frontmost")
	r.setFront("Arc")
	o, _, _ = r.run("status")
	if m, _ := statusLines(o); m["main-key"] != "main-key: not measured: main key (cmux not frontmost)" {
		t.Fatalf("main-key %q", m["main-key"])
	}
	r.mustRun(0, "down")

	r2 := newRig(t) // a window switch alone never makes the pass dirty
	r2.setMainWindows(two(true, false))
	r2.setFront("cmux")
	r2.mustRun(0, "up")
	r2.setMainWindows(two(false, true))
	r2.mustRun(0, "status")
	r2.mustRun(0, "down")
	if v, _ := r2.lastVerdict(); v == "dirty" {
		t.Fatal("a main window switch made the pass dirty")
	}
}

func TestStatusFlagsNightlyFrontmost(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.setFront("cmux NIGHTLY")
	r.mustRun(7, "status")
	r.setFront("Arc")
	r.mustRun(0, "down")

	r2 := newRig(t)
	r2.mustRun(0, "up")
	r2.setFront("cmux NIGHTLY")
	waitFor(t, "sampler sees NIGHTLY", 10*time.Second, func() bool {
		return strings.Contains(r2.passFile("focus.log"), "sampler")
	})
	r2.setFront("Arc")
	_, e := r2.mustRun(7, "status")
	if !regexp.MustCompile(`nightly was frontmost at \S+ \(sampler\)`).MatchString(e) {
		t.Fatalf("stderr %q", e)
	}
	r2.mustRun(0, "down")
}

func TestStatusReportsEveryFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	r.appendLiveLog("cmuxsync: on " + wsW)
	r.touch("main-list-fails")
	_, e := r.mustRun(7, "status")
	if n := strings.Count(e, "live-pass: "); n != 2 {
		t.Fatalf("stderr lines %d:\n%s", n, e)
	}
	r.remove("main-list-fails")
	r.mustRun(0, "down")
}

func TestHelpListsSubcommands(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	o, _ := r.mustRun(0, "help")
	for _, want := range []string{"up [--mac-idle]", "daemon start", "daemon stop", "env", "tab", "status", "front", "down",
		"mac-idle, at the start only"} {
		contains(t, "help", o, want)
	}
}

// d4Checks is plan C11 — D4 (the user): every absence or negative check of the
// harness, in the plan's order; an absence check added later enters here
// with its line. Each case feeds the check an empty or unreadable input and
// requires it not to answer ok.
var d4Checks = []string{
	"pass-ids", "live-log", "main-listing", "branch-aim", "branch-log", "focus-now", "sampler", "main-key",
	"front-before-up", "up-focus", "nightly-running", "nightly-pid", "main-ids", "pass-lock", "mac-idle",
	"ledger", "check-args", "stopped", "verdict", "c7-guard", "c9-env",
}

// statusNotOk runs status after setup and requires line id not ok and the
// last line not « isolation: proven ».
func statusNotOk(t *testing.T, r *rig, id string) {
	t.Helper()
	o, _, _ := r.run("status")
	m, last := statusLines(o)
	if m[id] == "" || m[id] == id+": ok" || last == "isolation: proven" {
		t.Fatalf("%s: %q, last %q\n%s", id, m[id], last, o)
	}
}

func d4Cases() map[string][]blindCase {
	up := func(t *testing.T) *rig {
		r := newRig(t)
		r.mustRun(0, "up")
		return r
	}
	withDaemon := func(t *testing.T, knob string) *rig {
		r := newRig(t)
		pd := r.writePigtaild()
		if knob != "" {
			r.touch(knob)
		}
		r.mustRun(0, "up")
		r.mustRun(0, "daemon", "start", "--pigtaild", pd)
		return r
	}
	focus := func(front, file string) func(t *testing.T) {
		return func(t *testing.T) {
			r := up(t)
			r.setFront(front)
			if file != "" {
				r.touch(file)
			}
			statusNotOk(t, r, "focus-now")
		}
	}
	cases := map[string][]blindCase{
		"pass-ids": {{"pass-ids", "empty listing", func(t *testing.T) {
			r := newRig(t)
			r.touch("pass-ws-empty-" + winK)
			r.mustRun(0, "up")
			statusNotOk(t, r, "pass-ids")
		}}},
		"live-log": {
			{"live-log", "file absent", func(t *testing.T) {
				r := up(t)
				r.remove("live.log")
				statusNotOk(t, r, "live-log")
			}},
			{"live-log", "still", func(t *testing.T) { statusNotOk(t, up(t), "live-log") }},
			{"live-log", "grown without an id of the user", func(t *testing.T) {
				r := up(t)
				r.appendLiveLog("rammon: tick")
				statusNotOk(t, r, "live-log")
			}},
		},
		"main-listing": {
			{"main-listing", "exit 1", func(t *testing.T) {
				r := up(t)
				r.touch("main-list-fails")
				statusNotOk(t, r, "main-listing")
			}},
			{"main-listing", "no window", func(t *testing.T) {
				r := up(t)
				r.setMainWindows(`{"windows":[]}`)
				statusNotOk(t, r, "main-listing")
			}},
		},
		"branch-aim": {
			{"branch-aim", "no daemon", func(t *testing.T) { statusNotOk(t, up(t), "branch-aim") }},
			{"branch-aim", "empty daemon log", func(t *testing.T) { statusNotOk(t, withDaemon(t, "no-aim-line"), "branch-aim") }},
		},
		"branch-log": {{"branch-log", "no pass id", func(t *testing.T) { statusNotOk(t, withDaemon(t, ""), "branch-log") }}},
		"focus-now": {
			{"focus-now", "empty", focus("", "")},
			{"focus-now", "null", focus("@null", "")},
			{"focus-now", "garbage", focus("@garbage", "")},
			{"focus-now", "loginwindow", focus("loginwindow", "")},
			{"focus-now", "locked", focus("Arc", "locked")},
			{"focus-now", "ioreg empty", focus("Arc", "ioreg-empty")},
		},
		"sampler": {
			{"sampler", "dead", func(t *testing.T) {
				r := up(t)
				sp, _ := strconv.Atoi(r.state()["SAMPLER_PID"])
				_ = syscall.Kill(sp, syscall.SIGKILL)
				waitFor(t, "sampler dead", 5*time.Second, func() bool { return !alive(sp) })
				statusNotOk(t, r, "sampler")
			}},
			{"sampler", "no sampler line", func(t *testing.T) {
				r := newRig(t)
				r.setFront("")
				r.mustRun(0, "up")
				statusNotOk(t, r, "sampler")
			}},
		},
		"main-key": {
			{"main-key", "no key field", func(t *testing.T) {
				r := newRig(t)
				r.setFront("cmux")
				r.mustRun(0, "up")
				r.setMainWindows(`[{"id" : "` + mainWin + `"}]`)
				statusNotOk(t, r, "main-key")
			}},
			{"main-key", "focus unreadable", func(t *testing.T) {
				r := up(t)
				r.setFront("")
				statusNotOk(t, r, "main-key")
			}},
		},
		"front-before-up": {{"front-before-up", "front empty", func(t *testing.T) {
			r := newRig(t)
			r.setFront("")
			o, _ := r.mustRun(0, "up")
			contains(t, "stdout", o, "focus: not measured (empty)")
			notContains(t, "stdout", o, "not frontmost")
			if n := lineCount(r.passFile("focus-readable.log")); n != 0 {
				t.Fatalf("readable samples %d", n)
			}
		}}},
		"up-focus": {{"up-focus", "front empty after W/K", func(t *testing.T) {
			r := newRig(t)
			r.write("bin/open", 0o755, strings.Replace(fakeOpen, "exit 0\n", ": > \"$RIG/front\"\nexit 0\n", 1))
			o, _ := r.mustRun(0, "up")
			contains(t, "stdout", o, "focus: not measured (empty)")
			if n := lineCount(r.passFile("focus-readable.log")); n != 1 { // the sample before up only
				t.Fatalf("readable samples %d", n)
			}
		}}},
		"main-ids": {{"main-ids", "no workspace", func(t *testing.T) {
			r := newRig(t)
			r.touch("main-ws-empty")
			r.mustRun(3, "up")
			if r.stateDir() != "" {
				t.Fatal("lp.current created")
			}
		}}},
		"check-args": {{"check-args", "silent", func(t *testing.T) {
			r := newRig(t)
			pd := r.writePigtaild()
			r.touch("silent-check-args")
			r.mustRun(0, "up")
			_, e := r.mustRun(2, "daemon", "start", "--pigtaild", pd)
			contains(t, "stderr", e, "pigtaild -check-args did not confirm the pass flags")
		}}},
		"verdict": {{"verdict", "zero readable sample with a status", func(t *testing.T) {
			r := newRig(t)
			r.setFront("")
			r.mustRun(0, "up")
			r.mustRun(0, "status")
			r.mustRun(0, "down")
			if v, why := r.lastVerdict(); v != "unmeasured" || why != "readable focus samples 0/3" {
				t.Fatalf("verdict %q %q", v, why)
			}
		}}},
		"c7-guard": {{"c7-guard", "empty script", func(t *testing.T) {
			if _, err := bareToolCalls(""); err == nil || !strings.Contains(err.Error(), "script read empty or detector blind") {
				t.Fatalf("bareToolCalls(\"\") = %v", err)
			}
		}}},
		"c9-env": {{"c9-env", "empty calls.log", func(t *testing.T) {
			if err := callerCmuxLeaks("", "/x"); err == nil {
				t.Fatal("callerCmuxLeaks(\"\") = nil")
			}
		}}},
	}
	for _, c := range blindInstrumentCases() {
		if c.id != "focus-now" { // the front subcommand case; status cases above
			cases[c.id] = append(cases[c.id], c)
		}
	}
	return cases
}

func TestNoAbsenceCheckPassesOnEmptyInput(t *testing.T) {
	t.Parallel()
	cases := d4Cases()
	for _, id := range d4Checks {
		if len(cases[id]) == 0 {
			t.Errorf("C11 check %s has no empty-input case", id)
		}
	}
	for id := range cases {
		found := false
		for _, d := range d4Checks {
			found = found || d == id
		}
		if !found {
			t.Errorf("case for %s, which is not in d4Checks (plan C11)", id)
		}
	}
	// Completeness: the lines of a full status are exactly the 8 status ids
	// of d4Checks — a new status line without its C11 row fails here.
	r := greenRig(t)
	o, _ := r.mustRun(0, "status")
	if got := strings.Join(statusLineIDs(o), " "); got != strings.Join(d4Checks[:8], " ") {
		t.Fatalf("status lines %q, d4Checks status ids %q", got, d4Checks[:8])
	}
	r.mustRun(0, "down")
	for _, id := range d4Checks {
		for _, c := range cases[id] {
			t.Run(id+"/"+c.name, c.run)
		}
	}
}

// TestUpStoresPassIDs: right after up, the state names W, K, NIGHTLY's own
// window and their workspaces (verify round 1 of PR #228: PASS_IDS was read
// unset under set -u, and up stored the workspaces only).
func TestUpStoresPassIDs(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustRun(0, "up")
	ids := " " + r.state()["PASS_IDS"] + " "
	for _, want := range []string{winW, winK, winDef, wsW, wsK, wsDef} {
		if !strings.Contains(ids, " "+want+" ") {
			t.Errorf("PASS_IDS after up %q lacks %s", ids, want)
		}
	}
	r.mustRun(0, "down")
}

// TestBashErrorGuardSeesScriptErrors: the guard of rig.run matches what
// bash prints, and not the script's own messages.
func TestBashErrorGuardSeesScriptErrors(t *testing.T) {
	t.Parallel()
	for _, e := range []string{
		"scripts/live-pass.sh: line 641: PASS_IDS: unbound variable\n",
		"/x/scripts/live-pass.sh: line 12: syntax error near unexpected token `fi'\n",
	} {
		if bashErrorLine.FindString(e) == "" {
			t.Errorf("guard blind to %q", e)
		}
	}
	for _, e := range []string{"live-pass: main cmux listing failed\n", ""} {
		if m := bashErrorLine.FindString(e); m != "" {
			t.Errorf("guard matched %q in %q", m, e)
		}
	}
}

// TestStatusDaemonLine fixes the branch daemon line of status, started or
// not (verify round 1 of PR #228: the pid was printed twice, "pid 2006920069").
func TestStatusDaemonLine(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pd := r.writePigtaild()
	r.mustRun(0, "up")
	o, _ := r.mustRun(0, "status")
	contains(t, "status", o, "\nbranch daemon not started, log "+r.stateDir()+"/pigtaild.log\n")
	r.mustRun(0, "daemon", "start", "--pigtaild", pd)
	o, _, _ = r.run("status")
	contains(t, "status", o, "\nbranch daemon pid "+r.state()["DAEMON_PID"]+", log "+r.stateDir()+"/pigtaild.log\n")
	r.mustRun(0, "down")
}

// TestUnknownCommandIsNamed: an unknown command, or daemon subcommand, is
// named on stderr before the usage, exit 2 (verify round 1 of PR #228).
func TestUnknownCommandIsNamed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"bogus"}, "live-pass: unknown command 'bogus'\n"},
		{[]string{"daemon", "bogus"}, "live-pass: unknown command 'daemon bogus'\n"},
		{[]string{"daemon"}, "live-pass: unknown command 'daemon'\n"},
	} {
		_, e := r.mustRun(2, tc.args...)
		if !strings.HasPrefix(e, tc.want) {
			t.Errorf("%v: stderr %q, want it to start with %q", tc.args, e, tc.want)
		}
		contains(t, "stderr", e, "usage: scripts/live-pass.sh")
	}
}
