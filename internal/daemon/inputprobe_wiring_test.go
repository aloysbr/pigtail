package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/inputprobe"
)

var probeNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// probeWorld: idle interactive claudes, one per surface (S-01…), each with
// a session id; busy names the sessions that are busy.
type probeWorld struct {
	n      int
	busy   map[string]bool
	alive  map[string]bool // sessions whose probe answers Alive (the others Dead)
	probed []string
	ttyOf  []int // pids TTYOf was asked about
	logs   []string
	mu     sync.Mutex
}

func sfOf(i int) string  { return fmt.Sprintf("S-%02d", i) }
func sidOf(i int) string { return fmt.Sprintf("sid-%02d", i) }

func (w *probeWorld) check() cmuxcheck.Deps {
	return cmuxcheck.Deps{
		Procs: func() ([]claudesession.Proc, error) {
			var out []claudesession.Proc
			for i := 1; i <= w.n; i++ {
				out = append(out, claudesession.Proc{PID: 100 + i, Args: "claude"})
			}
			return out, nil
		},
		Environ: func(pid int) (string, error) {
			return "CMUX_SURFACE_ID=" + sfOf(pid-100) + " CLAUDE_CONFIG_DIR=/cfg", nil
		},
		Native: func(_ string, pid int) (claudesession.Native, bool) {
			st := "idle"
			if w.busy[sidOf(pid-100)] {
				st = "busy"
			}
			return claudesession.Native{SessionID: sidOf(pid - 100), Status: st, Kind: "interactive",
				StatusUpdatedAt: probeNow.Add(-time.Hour).UnixMilli(), StartedAt: probeNow.Add(-2 * time.Hour).UnixMilli()}, true
		},
		Transcript: func(string, string) (string, bool) { return "", false },
		Screen:     func(string) (string, error) { return "", nil },
		Shells:     func(int) ([]int, error) { return nil, nil },
		Now:        func() time.Time { return probeNow },
		TTYListing: func() ([]byte, error) {
			var b strings.Builder
			for i := 1; i <= w.n; i++ {
				fmt.Fprintf(&b, "%d %d %d S+ ttys%03d\n", 100+i, 100+i, 100+i, i)
			}
			return []byte(b.String()), nil
		},
		TTYOf: func(pid int) (inputprobe.TTYInfo, error) {
			w.mu.Lock()
			w.ttyOf = append(w.ttyOf, pid)
			w.mu.Unlock()
			return inputprobe.TTYInfo{TTY: fmt.Sprintf("/dev/ttys%03d", pid-100), Foreground: true}, nil
		},
	}
}

func (w *probeWorld) loop() *inputProbeLoop {
	return &inputProbeLoop{
		tracker:      inputprobe.NewTracker(inputProbeEvery),
		max:          inputProbeMax,
		sidebarOn:    func() bool { return true },
		ping:         func() error { return nil },
		checkRunning: func() bool { return false },
		maintenance:  func() bool { return false },
		inStandby:    func(string) (bool, error) { return false, nil },
		photo:        func() (cmuxcheck.Photo, error) { return cmuxcheck.Photo{}, nil },
		check:        w.check(),
		probe: func(t inputprobe.Target) inputprobe.Result {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.probed = append(w.probed, t.SessionID)
			if w.alive[t.SessionID] {
				return inputprobe.Result{Verdict: inputprobe.VerdictAlive}
			}
			return inputprobe.Result{Verdict: inputprobe.VerdictDead}
		},
		now:  func() time.Time { return probeNow },
		logf: func(f string, a ...any) { w.logs = append(w.logs, fmt.Sprintf(f, a...)) },
	}
}

func TestPassNeverProbesBusySessionTargets(t *testing.T) {
	w := &probeWorld{n: 3, busy: map[string]bool{"sid-02": true}}
	w.loop().tick()
	if !reflect.DeepEqual(w.probed, []string{"sid-01", "sid-03"}) {
		t.Fatalf("probed %v: a busy session must never be probed", w.probed)
	}
}

func TestPassNeverProbesStandbySession(t *testing.T) {
	w := &probeWorld{n: 3}
	l := w.loop()
	l.inStandby = func(sid string) (bool, error) {
		switch sid {
		case "sid-01":
			return true, nil
		case "sid-03":
			return true, errors.New("standby unreadable")
		}
		return false, nil
	}
	l.tick()
	if !reflect.DeepEqual(w.probed, []string{"sid-02"}) {
		t.Fatalf("probed %v: a session in Standby (or unreadable) is never probed", w.probed)
	}
}

// TTYOf (one ps and a libproc walk per call) runs only for the sessions the
// tracker picks this tick, not for every candidate.
func TestInputProbeAsksTTYOfOnlyForPickedTargets(t *testing.T) {
	w := &probeWorld{n: 6}
	l := w.loop()
	l.tick()
	if len(w.probed) != inputProbeMax {
		t.Fatalf("probed %v, want %d", w.probed, inputProbeMax)
	}
	if len(w.ttyOf) != len(w.probed) {
		t.Fatalf("TTYOf asked about %v for %d probed targets", w.ttyOf, len(w.probed))
	}
	// A second tick finds nobody due: no probe, no TTYOf.
	w.ttyOf = nil
	l.now = func() time.Time { return probeNow.Add(time.Second) }
	l.tick()
	if len(w.ttyOf) > 2 {
		t.Fatalf("TTYOf asked about %v on a tick with at most the 2 never-probed due", w.ttyOf)
	}
}

func TestInputProbeTickProbesAndMarksDead(t *testing.T) {
	w := &probeWorld{n: 6}
	l := w.loop()
	if !l.tick() || len(w.probed) != inputProbeMax {
		t.Fatalf("probed %v, want %d per tick", w.probed, inputProbeMax)
	}
	if got := l.tracker.DeadSurfaces(l.now()); len(got) != 0 {
		t.Fatalf("dead %v after one Dead each: the pill needs two in a row", got)
	}
	// Another session answers, then the confirmations come due.
	l.tracker.Record(inputprobe.Target{Surface: "S-06", SessionID: "sid-06"}, inputprobe.Result{Verdict: inputprobe.VerdictAlive}, probeNow)
	l.now = func() time.Time { return probeNow.Add(inputprobe.ConfirmAfter + 5*time.Second) }
	l.tick()
	if got := l.tracker.DeadSurfaces(l.now()); !reflect.DeepEqual(got, []string{"S-01", "S-02", "S-03", "S-04"}) {
		t.Fatalf("dead %v", got)
	}
	for _, s := range w.logs {
		if strings.Contains(s, "sid-") {
			t.Fatalf("log %q names a session id", s)
		}
	}
}

func TestInputProbeForgetsEndedSession(t *testing.T) {
	w := &probeWorld{n: 3, alive: map[string]bool{"sid-01": true}}
	l := w.loop()
	l.tick()
	l.now = func() time.Time { return probeNow.Add(inputprobe.ConfirmAfter + 5*time.Second) }
	l.tick()
	if got := l.tracker.DeadSurfaces(l.now()); !reflect.DeepEqual(got, []string{"S-02", "S-03"}) {
		t.Fatalf("dead %v, want S-02 and S-03 confirmed", got)
	}
	w.n = 2 // sid-03's claude ended
	l.now = func() time.Time { return probeNow.Add(2 * time.Minute) }
	l.tick()
	if got := l.tracker.DeadSurfaces(l.now()); !reflect.DeepEqual(got, []string{"S-02"}) {
		t.Fatalf("dead %v: an ended session must be forgotten", got)
	}
}

func TestInputProbeLogsHeldDeadOnce(t *testing.T) {
	w := &probeWorld{n: 2} // nobody answers: the dead states are held
	l := w.loop()
	held := func() int {
		n := 0
		for _, s := range w.logs {
			if s == logDeadHeld {
				n++
			}
		}
		return n
	}
	l.tick()
	if held() != 0 {
		t.Fatal("a first Dead is no confirmed state: nothing to log")
	}
	at := probeNow
	for i := 0; i < 3; i++ {
		at = at.Add(inputprobe.ConfirmAfter + 5*time.Second)
		l.now = func() time.Time { return at }
		l.tick()
	}
	if held() != 1 {
		t.Fatalf("logs %q: want the held line exactly once", w.logs)
	}
	for _, s := range w.logs {
		if strings.Contains(s, "sid-") {
			t.Fatalf("log %q names a session id", s)
		}
	}
	// A live answer lifts the hold; a later hold is logged again.
	l.tracker.Record(inputprobe.Target{Surface: "S-09", SessionID: "sid-09"}, inputprobe.Result{Verdict: inputprobe.VerdictAlive}, at)
	l.tick()
	if l.heldLogged {
		t.Fatal("hold lifted: the flag must reset")
	}
}

func TestInputProbeSkippedWhenCheckRunning(t *testing.T) {
	w := &probeWorld{n: 2}
	l := w.loop()
	l.checkRunning = func() bool { return true }
	if l.tick() || len(w.probed) != 0 {
		t.Fatalf("probed %v while a cmux check --fix types", w.probed)
	}
}

func TestInputProbeSkippedWhenCmuxDown(t *testing.T) {
	w := &probeWorld{n: 2}
	l := w.loop()
	l.ping = func() error { return errors.New("no socket") }
	if l.tick() || len(w.probed) != 0 {
		t.Fatalf("probed %v with cmux down", w.probed)
	}
}

func TestInputProbeSkippedInMaintenance(t *testing.T) {
	w := &probeWorld{n: 2}
	l := w.loop()
	l.maintenance = func() bool { return true }
	pinged := false
	l.ping = func() error { pinged = true; return nil }
	if l.tick() || len(w.probed) != 0 || pinged {
		t.Fatalf("probed %v (ping %v) during a maintenance", w.probed, pinged)
	}
}

// TestInputProbeFollowsSidebarState goes through the production loop of a
// Server built by New: its sidebarOn reads the sidebar loop's live state, so
// --cmux-sidebar off at boot, or `pigtail cmux sidebar off` at run time,
// makes the tick call no cmux at all, and switching it on lets it probe.
// The goroutine itself runs whatever the boot flag (the sidebar can be
// switched on at run time, POST /cmux/sidebar); the per-tick gate is the
// plan's « sinon aucune boucle ».
func TestInputProbeFollowsSidebarState(t *testing.T) {
	for _, boot := range []bool{false, true} {
		s := newProbeServer(t, Config{CmuxSidebar: boot, CmuxInputProbe: true})
		w := &probeWorld{n: 2}
		l := s.inputProbeLoop() // production sidebarOn kept
		stub := w.loop()
		pinged := 0
		l.ping = func() error { pinged++; return nil }
		l.checkRunning, l.maintenance, l.inStandby = stub.checkRunning, stub.maintenance, stub.inStandby
		l.photo, l.check, l.probe, l.now, l.logf = stub.photo, stub.check, stub.probe, stub.now, stub.logf

		if !boot {
			if l.tick() || pinged != 0 || len(w.probed) != 0 {
				t.Fatalf("sidebar off at boot: probed %v, %d ping; want no cmux call", w.probed, pinged)
			}
			s.cmuxSync.SetEnabled(true)
		}
		if !l.tick() || pinged != 1 || len(w.probed) != 2 {
			t.Fatalf("sidebar on (boot %v): probed %v, %d ping; want both probed", boot, w.probed, pinged)
		}
		s.cmuxSync.SetEnabled(false)
		l.tracker = inputprobe.NewTracker(inputProbeEvery)
		if l.tick() || pinged != 1 || len(w.probed) != 2 {
			t.Fatalf("sidebar switched off (boot %v): probed %v, %d ping; want no cmux call", boot, w.probed, pinged)
		}
	}
}

func TestInputProbeNoProbeWhenPhotoUnreadable(t *testing.T) {
	w := &probeWorld{n: 2}
	l := w.loop()
	l.photo = func() (cmuxcheck.Photo, error) { return cmuxcheck.Photo{}, errors.New("no photo") }
	if l.tick() || len(w.probed) != 0 {
		t.Fatalf("probed %v with no photo", w.probed)
	}
	if len(w.logs) != 1 || !strings.Contains(w.logs[0], "photo unreadable") {
		t.Fatalf("logs %q: want the unreadable photo logged", w.logs)
	}
}

func newProbeServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	cfg.SocketPath = testSocketPath(t)
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestInputProbeOffFlagStartsNoLoop(t *testing.T) {
	s := newProbeServer(t, Config{CmuxSidebar: true})
	if s.inputTracker != nil {
		t.Fatal("--cmux-input-probe off: no tracker")
	}
	spawned := 0
	if startInputProbe(context.Background(), s, func(func()) { spawned++ }) || spawned != 0 {
		t.Fatal("probe loop started with --cmux-input-probe off")
	}
	if s.deadInputs() != nil {
		t.Fatal("no probe: no dead input")
	}
	on := newProbeServer(t, Config{CmuxSidebar: true, CmuxInputProbe: true})
	if !startInputProbe(context.Background(), on, func(func()) { spawned++ }) || spawned != 1 {
		t.Fatal("probe loop not started with the flag on, the sidebar loop and the relay")
	}
}

func TestInputProbeNoLoopWhenRelayNil(t *testing.T) {
	s := newProbeServer(t, Config{CmuxSidebar: true, CmuxInputProbe: true})
	s.relay = nil
	spawned := 0
	if startInputProbe(context.Background(), s, func(func()) { spawned++ }) || spawned != 0 {
		t.Fatal("probe loop started without the relay's Standby predicate")
	}
}

func TestServerStartsInputProbe(t *testing.T) {
	old := startInputProbeFn
	t.Cleanup(func() { startInputProbeFn = old })
	got := make(chan bool, 1)
	startInputProbeFn = func(_ context.Context, s *Server, _ func(func())) bool {
		got <- s.inputTracker != nil
		return false
	}
	srv := newProbeServer(t, Config{CmuxInputProbe: true})
	t.Cleanup(startDaemon(t, srv))
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("Server.Run reached startInputProbe without the tracker")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Run never called startInputProbe")
	}
}

func funcPtr(f any) uintptr { return reflect.ValueOf(f).Pointer() }

func TestInputProbeDepsUseTTYWriter(t *testing.T) {
	d := inputProbeDeps()
	if funcPtr(d.Write) != funcPtr(inputprobe.WriteTTY) {
		t.Fatal("Write must be inputprobe.WriteTTY (Query only, own tty only)")
	}
	if funcPtr(d.TTYOf) != funcPtr(inputprobe.TTYOf) {
		t.Fatal("TTYOf must be inputprobe.TTYOf")
	}
	if funcPtr(d.Counter) != funcPtr(inputprobe.DefaultCounter) {
		t.Fatal("Counter must be inputprobe.DefaultCounter")
	}
	c := inputProbeCheckDeps()
	if funcPtr(c.Screen) != funcPtr(cmux.ReadScreenBounded) {
		t.Fatal("Deps.Screen must be cmux.ReadScreenBounded, not cmux.ReadScreen")
	}
	if funcPtr(c.TTYOf) != funcPtr(inputprobe.TTYOf) {
		t.Fatal("Deps.TTYOf must be inputprobe.TTYOf")
	}
}

func TestSidebarDepsWireDeadInputs(t *testing.T) {
	s := newProbeServer(t, Config{CmuxSidebar: true, CmuxInputProbe: true})
	deps := s.cmuxSyncDeps()
	if deps.DeadInputs == nil {
		t.Fatal("sidebar deps: DeadInputs not wired")
	}
	x, dead := inputprobe.Target{Surface: "s-a", SessionID: "sid"}, inputprobe.Result{Verdict: inputprobe.VerdictDead}
	s.inputTracker.Record(x, dead, probeNow)
	s.inputTracker.Record(x, dead, probeNow.Add(time.Minute))
	s.inputTracker.Record(inputprobe.Target{Surface: "s-b", SessionID: "other"}, inputprobe.Result{Verdict: inputprobe.VerdictAlive}, probeNow.Add(time.Minute))
	s.inputNow = func() time.Time { return probeNow.Add(2 * time.Minute) }
	if got := deps.DeadInputs(); !reflect.DeepEqual(got, []string{"S-A"}) {
		t.Fatalf("DeadInputs = %v, want the tracker's dead surfaces", got)
	}
	s.inputNow = func() time.Time { return probeNow.Add(40 * time.Minute) }
	if got := deps.DeadInputs(); len(got) != 0 {
		t.Fatalf("DeadInputs = %v at +40 min with no new answer, want none (held)", got)
	}
}

// A real tick() of the production loop (its Server, its check deps and its
// probe deps, with only the process, tty and counter readers faked): every
// cmux call it makes is ping or read-screen, read-screen aimed by UUID, none
// carries a CMUX_* variable, and the only bytes written to a tty are Query,
// to the fake claude's own tty.
func TestInputProbeCallsRunWithoutCmuxEnvAndOnlyPingReadScreen(t *testing.T) {
	dir := t.TempDir()
	logf := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "cmux")
	script := "#!/bin/sh\nenv | grep '^CMUX_' >> " + logf + "\nprintf 'ARGS %s\\n' \"$*\" >> " + logf + "\nprintf 'PONG'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cmux.EnvBundledCLI, bin)
	t.Setenv("CMUX_SURFACE_ID", "X")
	t.Setenv("CMUX_WORKSPACE_ID", "Y")
	const sf = "3A23D5DD-59EE-471D-9270-32F0FB06E099"
	const tty = "/dev/ttys042"

	s := newProbeServer(t, Config{CmuxSidebar: true, CmuxInputProbe: true})
	s.cmuxSync.SetEnabled(true)
	l := s.inputProbeLoop()

	// Production check deps, except the readers of processes and ttys.
	w := &probeWorld{n: 1}
	fake := w.check()
	fake.Environ = func(int) (string, error) { return "CMUX_SURFACE_ID=" + sf + " CLAUDE_CONFIG_DIR=/cfg", nil }
	fake.TTYListing = func() ([]byte, error) { return []byte("101 101 101 S+ ttys042\n"), nil }
	check := inputProbeCheckDeps()
	check.Procs, check.Environ, check.Native, check.Now = fake.Procs, fake.Environ, fake.Native, fake.Now
	check.TTYListing, check.TTYOf = fake.TTYListing, func(int) (inputprobe.TTYInfo, error) {
		return inputprobe.TTYInfo{TTY: tty, Foreground: true}, nil
	}
	check.Transcript, check.Shells = fake.Transcript, fake.Shells
	l.check = check

	// Production probe deps, except the counter, the tty reader and the
	// writer (a recorder). The counter is paired with the recorder: flat on
	// a baseline window, +10 on the window after a recorded write.
	var mu sync.Mutex
	var written []string
	var pending, counter uint64
	pd := inputProbeDeps()
	pd.Counter = func(int) (uint64, error) {
		mu.Lock()
		defer mu.Unlock()
		counter += pending
		pending = 0
		return counter, nil
	}
	pd.TTYOf = check.TTYOf
	pd.Write = func(path string, b []byte) error {
		mu.Lock()
		defer mu.Unlock()
		written = append(written, path+" "+string(b))
		pending = 10
		return nil
	}
	pd.Sleep = func(time.Duration) {}
	params := inputprobe.DefaultParams()
	l.probe = func(tg inputprobe.Target) inputprobe.Result { return inputprobe.Probe(pd, params, tg) }

	l.checkRunning = func() bool { return false }
	l.maintenance = func() bool { return false }
	l.inStandby = func(string) (bool, error) { return false, nil }
	l.photo = func() (cmuxcheck.Photo, error) { return cmuxcheck.Photo{}, nil }
	l.now = func() time.Time { return probeNow }
	l.logf = func(string, ...any) {}

	if !l.tick() {
		t.Fatal("the tick was skipped")
	}
	b, err := os.ReadFile(logf)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "CMUX_") {
			t.Fatalf("a call carries %q", line)
		}
		args := strings.Fields(strings.TrimPrefix(line, "ARGS "))
		if len(args) == 0 || (args[0] != "ping" && args[0] != "read-screen") {
			t.Fatalf("call %q: not ping or read-screen", line)
		}
		if args[0] == "read-screen" && !strings.Contains(line, "--surface "+sf) {
			t.Fatalf("call %q: not aimed by UUID", line)
		}
		seen[args[0]]++
	}
	if seen["ping"] == 0 || seen["read-screen"] == 0 {
		t.Fatalf("calls %v: want ping and read-screen each at least once", seen)
	}
	if len(written) == 0 {
		t.Fatal("the probe wrote nothing: the tick never reached the writer")
	}
	for _, w := range written {
		if w != tty+" "+inputprobe.Query {
			t.Fatalf("wrote %q: only Query to the fake claude's tty", w)
		}
	}
}

func TestInputProbeTraceOnlyWhenEnabled(t *testing.T) {
	traceLines := func(trace bool) []string {
		w := &probeWorld{n: 2, alive: map[string]bool{"sid-01": true}}
		l := w.loop()
		l.trace = trace
		l.tick()
		var out []string
		for _, s := range w.logs {
			if strings.HasPrefix(s, "input probe: surface ") && strings.Contains(s, " verdict ") {
				out = append(out, s)
			}
		}
		return out
	}
	if got := traceLines(false); len(got) != 0 {
		t.Fatalf("trace off: lines %q", got)
	}
	got := traceLines(true)
	want := []string{
		"input probe: surface S-01 tty /dev/ttys001 verdict alive",
		"input probe: surface S-02 tty /dev/ttys002 verdict dead",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trace on: lines %q, want %q", got, want)
	}
}
