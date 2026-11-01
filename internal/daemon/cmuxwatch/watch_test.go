package cmuxwatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The flag value is validated at start: an unknown one refuses to start,
// never falls back silently.
func TestParseMode(t *testing.T) {
	for _, ok := range []string{"off", "report", "pilot", "fix"} {
		if m, err := ParseMode(ok); err != nil || m != ok {
			t.Errorf("ParseMode(%q) = %q, %v", ok, m, err)
		}
	}
	for _, bad := range []string{"", "Fix", "repair", "on", "report "} {
		if _, err := ParseMode(bad); err == nil {
			t.Errorf("ParseMode(%q) accepted", bad)
		}
	}
	if DefaultMode != ModePilot {
		t.Fatalf("default = %q, want pilot (D3, decided 2026-10-09)", DefaultMode)
	}
}

type fake struct {
	pid  int
	err  error
	runs [][]string // the args of each run (Args: mode, then the surfaces)
	exit int
	logs []string
	// sidebarOff: the cmux sidebar loop is switched off (É19).
	sidebarOff bool
	// busyLeft / busyErr: what BusyLeft answers after a pass (D6).
	busyLeft  []string
	busyErr   error
	busyCalls int
	// notices: "<pid>|<failure>" per Notice call (D2); secondExit: the code
	// Run returns from the second run on (0 = exit).
	notices    []string
	secondExit int
}

// logged counts the log lines that contain sub.
func (f *fake) logged(sub string) int {
	n := 0
	for _, l := range f.logs {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// newW builds a watcher whose last-cmux-pid file holds last ("" = no file:
// pigtaild's first start, É1). New reads it once, so it is written first.
func newW(t *testing.T, mode string, f *fake, last string) (*Watcher, string) {
	state := filepath.Join(t.TempDir(), "last-pid")
	if last != "" {
		if err := os.WriteFile(state, []byte(last), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w := New(Config{Mode: mode, Poll: time.Second, Settle: 90 * time.Second, Retries: 3, StatePath: state, SecondPass: 5 * time.Minute}, Deps{
		Sentinel: func() (int, error) { return f.pid, f.err },
		Run: func(_ context.Context, args []string) (int, error) {
			f.runs = append(f.runs, args)
			if len(f.runs) > 1 && f.secondExit != 0 {
				return f.secondExit, nil
			}
			return f.exit, nil
		},
		Notice:    func(pid int, failure string) { f.notices = append(f.notices, fmt.Sprintf("%d|%s", pid, failure)) },
		Args:      func(mode string, s []string) []string { return append([]string{mode}, s...) },
		Logf:      func(format string, a ...any) { f.logs = append(f.logs, fmt.Sprintf(format, a...)) },
		SidebarOn: func() bool { return !f.sidebarOff },
		BusyLeft:  func(int, bool) ([]string, error) { f.busyCalls++; return f.busyLeft, f.busyErr },
	})
	return w, state
}

var t0 = time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)

func TestWatcherRunsOnceAfterSettle(t *testing.T) {
	f := &fake{pid: 100}
	w, state := newW(t, "report", f, "99") // the daemon saw cmux 99 before
	ctx := context.Background()
	w.Step(ctx, t0)
	w.Step(ctx, t0.Add(60*time.Second))
	if len(f.runs) != 0 {
		t.Fatal("ran before the settle delay")
	}
	w.Step(ctx, t0.Add(91*time.Second))
	w.Step(ctx, t0.Add(200*time.Second))
	if len(f.runs) != 1 || f.runs[0][0] != "report" {
		t.Fatalf("runs = %v, want one report run", f.runs)
	}
	if len(f.notices) != 0 {
		t.Fatalf("notices = %v, want none in report mode", f.notices)
	}
	if b, _ := os.ReadFile(state); string(b) != "100" {
		t.Fatalf("state = %q, want 100 (a daemon restart must not re-run)", b)
	}
}

func TestWatcherSamePidAcrossDaemonRestart(t *testing.T) {
	f := &fake{pid: 100}
	w, _ := newW(t, "fix", f, "100")
	w.Step(context.Background(), t0)
	w.Step(context.Background(), t0.Add(10*time.Minute))
	if len(f.runs) != 0 {
		t.Fatalf("runs = %v", f.runs)
	}
}

func TestWatcherFixModeAndRetryOnExit2(t *testing.T) {
	f := &fake{pid: 5, exit: 2}
	w, _ := newW(t, "fix", f, "4") // not a first start (É1)
	for i := 0; i < 10; i++ {
		w.Step(context.Background(), t0.Add(time.Duration(i)*100*time.Second))
	}
	if len(f.runs) != 3 || f.runs[0][0] != "fix" {
		t.Fatalf("runs = %v, want 3 fix attempts (exit 2 = could not look)", f.runs)
	}
}

// É1 (review round 5): pigtaild's very first start, no last-cmux-pid file —
// the running cmux is adopted, recorded, never checked; the next cmux
// launch is.
func TestWatcherFirstStartAdoptsPid(t *testing.T) {
	f := &fake{pid: 100}
	w, state := newW(t, "report", f, "") // no state file
	for i := 0; i < 5; i++ {
		w.Step(context.Background(), t0.Add(time.Duration(i)*100*time.Second))
	}
	if len(f.runs) != 0 {
		t.Fatalf("runs = %v, want none on a first start", f.runs)
	}
	if b, _ := os.ReadFile(state); string(b) != "100" {
		t.Fatalf("state = %q, want the running cmux pid adopted", b)
	}
	f.pid = 101 // a real relaunch afterwards
	w.Step(context.Background(), t0.Add(time.Hour))
	w.Step(context.Background(), t0.Add(time.Hour+91*time.Second))
	if len(f.runs) != 1 {
		t.Fatalf("runs = %v, want one run after the next launch", f.runs)
	}
}

// É19 (D13, D14 decided 2026-10-08): the defects show only as a sidebar pill; with the
// sidebar loop off, a check that left a defect (exit 1) says so in the log —
// once per launch, never on a clean check, never while the sidebar is on.
// D17: a report whose only rows are reported only exits 0 — no pill with the
// sidebar on either, so no line here (the "clean" case).
func TestWatcherSidebarOffLogged(t *testing.T) {
	const want = "cmuxwatch: sidebar off — no pill for the defects: pigtail cmux check --last"
	count := func(logs []string) int {
		n := 0
		for _, l := range logs {
			if strings.Contains(l, want) {
				n++
			}
		}
		return n
	}
	for _, c := range []struct {
		name string
		off  bool
		exit int
		n    int
	}{
		{"off, defect", true, 1, 1},
		{"off, clean or reported only (D17)", true, 0, 0},
		{"on, defect", false, 1, 0},
	} {
		f := &fake{pid: 100, exit: c.exit, sidebarOff: c.off}
		w, _ := newW(t, "report", f, "99")
		w.Step(context.Background(), t0)
		w.Step(context.Background(), t0.Add(91*time.Second))
		w.Step(context.Background(), t0.Add(300*time.Second))
		if got := count(f.logs); got != c.n || len(f.runs) != 1 {
			t.Errorf("%s: %d sidebar-off lines (want %d), %d runs; logs %q", c.name, got, c.n, len(f.runs), f.logs)
		}
	}
}

func TestWatcherOffAndCmuxDown(t *testing.T) {
	f := &fake{pid: 5}
	w, _ := newW(t, "off", f, "4")
	w.Step(context.Background(), t0.Add(time.Hour))
	f2 := &fake{err: errors.New("no sentinel")}
	w2, _ := newW(t, "report", f2, "4")
	w2.Step(context.Background(), t0.Add(time.Hour))
	if len(f.runs)+len(f2.runs) != 0 {
		t.Fatal("off, or cmux down, must not run")
	}
}

// A check that never finished — killed by a signal (exit < 0) or cut by the
// daemon's context — looked at nothing: the launch is never recorded as
// checked. A killed run is retried like exit 2; a cancelled context (pigtaild
// stopping) records nothing, so the next daemon start checks this launch.
func TestWatcherKilledOrCancelledNotRecorded(t *testing.T) {
	f := &fake{pid: 100, exit: -1}
	w, state := newW(t, "report", f, "99")
	w.Step(context.Background(), t0)
	w.Step(context.Background(), t0.Add(91*time.Second))
	if b, _ := os.ReadFile(state); string(b) != "99" {
		t.Fatalf("killed check (exit -1): state = %q, want 99 (not recorded)", b)
	}
	w.Step(context.Background(), t0.Add(182*time.Second))
	if len(f.runs) != 2 {
		t.Fatalf("killed check: runs = %v, want a retry", f.runs)
	}
	if !strings.Contains(strings.Join(f.logs, "\n"), "not recorded as checked") {
		t.Fatalf("killed check: logs %q; want the failure said", f.logs)
	}

	g := &fake{pid: 100, exit: 0}
	w2, state2 := newW(t, "report", g, "99")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w2.Step(ctx, t0)
	w2.Step(ctx, t0.Add(91*time.Second))
	if b, _ := os.ReadFile(state2); string(b) != "99" {
		t.Fatalf("cancelled context: state = %q, want 99 (not recorded)", b)
	}
}

// The log names the command each attempt runs, right before it runs (N2 pass,
// 2026-10-08: the log said "check (report) attempt 1: exit 1" and nothing of
// the argv). No Command wired: the attempt still runs, nothing named.
func TestWatcherLogsTheCommand(t *testing.T) {
	for mode, want := range map[string]string{"report": "pigtail report", "pilot": "pigtail pilot", "fix": "pigtail fix"} {
		f := &fake{pid: 100, exit: 1}
		w, _ := newW(t, mode, f, "99")
		w.Step(context.Background(), t0)
		w.Step(context.Background(), t0.Add(90*time.Second))
		run, exit := -1, -1
		for i, l := range f.logs {
			if strings.Contains(l, "attempt 1: running "+want) && strings.HasSuffix(l, want) {
				run = i
			}
			if strings.Contains(l, "attempt 1: exit 1") {
				exit = i
			}
		}
		if run < 0 || exit < 0 || run > exit {
			t.Errorf("%s: logs %q; want \"attempt 1: running %s\" before the exit line", mode, f.logs, want)
		}
	}
}

// D3: pilot is the default and parses.
func TestParseModePilot(t *testing.T) {
	if m, err := ParseMode("pilot"); err != nil || m != ModePilot || DefaultMode != ModePilot {
		t.Fatalf("ParseMode(pilot) = %q, %v; DefaultMode = %q", m, err, DefaultMode)
	}
}

// D6: rows left busy after the first pass → one pass on them SecondPass
// later, with their surfaces; never a third.
func TestWatcherSecondPassOnBusyRows(t *testing.T) {
	f := &fake{pid: 100, busyLeft: []string{"S-A"}}
	w, _ := newW(t, ModePilot, f, "99")
	ctx := context.Background()
	w.Step(ctx, t0)
	w.Step(ctx, t0.Add(90*time.Second))
	if len(f.runs) != 1 || strings.Join(f.runs[0], " ") != "pilot" {
		t.Fatalf("first pass runs %v", f.runs)
	}
	w.Step(ctx, t0.Add(90*time.Second+4*time.Minute))
	if len(f.runs) != 1 {
		t.Fatalf("second pass too early: %v", f.runs)
	}
	w.Step(ctx, t0.Add(90*time.Second+5*time.Minute))
	if len(f.runs) != 2 || strings.Join(f.runs[1], " ") != "pilot S-A" {
		t.Fatalf("second pass runs %v", f.runs)
	}
	w.Step(ctx, t0.Add(30*time.Minute))
	if len(f.runs) != 2 {
		t.Fatalf("a third pass ran: %v", f.runs)
	}
	if f.logged("1 rows left busy, second pass in 5m0s") != 1 || f.logged("→ second pass (pilot)") != 1 {
		t.Fatalf("log %v", f.logs)
	}
}

// T-g: a new cmux pid before the second pass drops it, logged.
func TestWatcherSecondPassDroppedOnRelaunch(t *testing.T) {
	f := &fake{pid: 100, busyLeft: []string{"S-A"}}
	w, _ := newW(t, ModePilot, f, "99")
	ctx := context.Background()
	w.Step(ctx, t0)
	w.Step(ctx, t0.Add(90*time.Second))
	f.pid = 101
	w.Step(ctx, t0.Add(2*time.Minute))
	if f.logged("second pass for cmux pid 100 dropped — cmux pid now 101") != 1 {
		t.Fatalf("log %v", f.logs)
	}
}

// Revue 1 #2: a sentinel read error (or pid 0) between the passes keeps the
// second pass — never "dropped — cmux pid now 0".
func TestWatcherSecondPassSurvivesSentinelError(t *testing.T) {
	f := &fake{pid: 100, busyLeft: []string{"S-A"}}
	w, _ := newW(t, ModePilot, f, "99")
	ctx := context.Background()
	w.Step(ctx, t0)
	w.Step(ctx, t0.Add(90*time.Second))
	f.err = errors.New("sentinel unreadable")
	w.Step(ctx, t0.Add(3*time.Minute))
	f.pid, f.err = 0, nil
	w.Step(ctx, t0.Add(4*time.Minute))
	f.pid = 100
	w.Step(ctx, t0.Add(90*time.Second+5*time.Minute))
	if len(f.runs) != 2 || f.logged("dropped") != 0 {
		t.Fatalf("runs %v, log %v", f.runs, f.logs)
	}
}

// T-g, revue 1 #5: report mode never plans a second pass, even with busy
// rows (BusyLeft not even read); pilot mode with nothing busy neither;
// BusyLeft unreadable → logged, none.
func TestWatcherNoSecondPass(t *testing.T) {
	ctx := context.Background()
	f := &fake{pid: 100, busyLeft: []string{"S-A"}}
	w, _ := newW(t, ModeReport, f, "99")
	w.Step(ctx, t0)
	w.Step(ctx, t0.Add(90*time.Second))
	w.Step(ctx, t0.Add(10*time.Minute))
	if len(f.runs) != 1 || f.busyCalls != 0 {
		t.Fatalf("report mode: runs %v, BusyLeft read %d times", f.runs, f.busyCalls)
	}
	f2 := &fake{pid: 100}
	w2, _ := newW(t, ModePilot, f2, "99")
	w2.Step(ctx, t0)
	w2.Step(ctx, t0.Add(90*time.Second))
	w2.Step(ctx, t0.Add(10*time.Minute))
	if len(f2.runs) != 1 {
		t.Fatalf("nothing busy: runs %v", f2.runs)
	}
	f3 := &fake{pid: 100, busyErr: errors.New("last.json: unexpected EOF")}
	w3, _ := newW(t, ModePilot, f3, "99")
	w3.Step(ctx, t0)
	w3.Step(ctx, t0.Add(90*time.Second))
	if f3.logged("rows left busy unreadable") != 1 {
		t.Fatalf("log %v", f3.logs)
	}
}

// D2: one notice per launch, after its last pass — the first when no second
// pass is planned, else the second only.
func TestWatcherNoticeAfterFinalPass(t *testing.T) {
	f := &fake{pid: 100}
	w, _ := newW(t, "fix", f, "99")
	ctx := context.Background()
	w.Step(ctx, t0)
	w.Step(ctx, t0.Add(91*time.Second))
	if len(f.notices) != 1 || f.notices[0] != "100|" {
		t.Fatalf("notices = %v, want one after the only pass", f.notices)
	}

	g := &fake{pid: 100, busyLeft: []string{"S1"}, exit: 1}
	w2, _ := newW(t, "fix", g, "99")
	w2.Step(ctx, t0)
	w2.Step(ctx, t0.Add(91*time.Second))
	if len(g.notices) != 0 {
		t.Fatalf("notices = %v, want none before the second pass", g.notices)
	}
	w2.Step(ctx, t0.Add(91*time.Second+5*time.Minute))
	if len(g.notices) != 1 || g.notices[0] != "100|" {
		t.Fatalf("notices = %v, want one after the second pass", g.notices)
	}
}

// Review Focus 5: a launch the check gave up on is said, never silent.
func TestWatcherNoticeOnGiveUp(t *testing.T) {
	f := &fake{pid: 100, exit: 2}
	w, _ := newW(t, "fix", f, "99")
	ctx := context.Background()
	w.Step(ctx, t0)
	for i := 1; i <= 3; i++ {
		w.Step(ctx, t0.Add(time.Duration(i)*91*time.Second))
	}
	if len(f.notices) != 1 || !strings.HasPrefix(f.notices[0], "100|abandon après 3 essais") {
		t.Fatalf("notices = %v, want the give-up", f.notices)
	}
}

// A killed second pass (exit < 0): the notice says so (FailSecondPass).
func TestWatcherNoticeOnSecondPassFailed(t *testing.T) {
	f := &fake{pid: 100, busyLeft: []string{"S1"}, exit: 1, secondExit: -1}
	w, _ := newW(t, "fix", f, "99")
	ctx := context.Background()
	w.Step(ctx, t0)
	w.Step(ctx, t0.Add(91*time.Second))
	w.Step(ctx, t0.Add(91*time.Second+5*time.Minute))
	if len(f.notices) != 1 || f.notices[0] != "100|"+FailSecondPass {
		t.Fatalf("notices = %v, want the second-pass failure", f.notices)
	}
}
