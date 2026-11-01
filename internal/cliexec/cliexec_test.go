package cliexec

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunExitCode(t *testing.T) {
	code, err := Run(exec.Command("sh", "-c", "exit 7"))
	if err != nil || code != 7 {
		t.Fatalf("code=%d err=%v, want 7", code, err)
	}
	code, err = Run(exec.Command("true"))
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v, want 0", code, err)
	}
}

func TestRunKilledBySignal(t *testing.T) {
	code, err := Run(exec.Command("sh", "-c", "kill -TERM $$"))
	if err != nil || code != 128+int(syscall.SIGTERM) {
		t.Fatalf("code=%d err=%v, want %d", code, err, 128+int(syscall.SIGTERM))
	}
}

func TestRunStartFailure(t *testing.T) {
	if _, err := Run(exec.Command("/nonexistent/binary")); err == nil || !strings.HasPrefix(err.Error(), "start:") {
		t.Fatalf("err = %v, want a start error", err)
	}
}

func TestRunPassesStdio(t *testing.T) {
	cmd := exec.Command("cat")
	cmd.Stdin = strings.NewReader("hello")
	var out bytes.Buffer
	cmd.Stdout = &out
	if code, err := Run(cmd); err != nil || code != 0 || out.String() != "hello" {
		t.Fatalf("code=%d err=%v out=%q", code, err, out.String())
	}
}

// TestRunRelaysSignals sends SIGINT to this process while the child runs:
// the child must receive it (and die of it) rather than this test binary.
func TestRunRelaysSignals(t *testing.T) {
	pr, pw := io.Pipe()
	cmd := exec.Command("sh", "-c", "echo ready; exec sleep 10")
	cmd.Stdout = pw
	done := make(chan struct{})
	var code int
	var err error
	go func() {
		code, err = Run(cmd)
		_ = pw.Close()
		close(done)
	}()
	// "ready" is printed after Start, hence after signal.Notify is in place.
	if _, rerr := bufio.NewReader(pr).ReadString('\n'); rerr != nil {
		t.Fatalf("child never started: %v", rerr)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the child never received the relayed SIGINT")
	}
	if err != nil || code != 128+int(syscall.SIGINT) {
		t.Fatalf("code=%d err=%v, want %d", code, err, 128+int(syscall.SIGINT))
	}
}

// readPidFile polls (test code only) for a pid written by a child shell.
func readPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitDead polls for pid's death, and kills it at the end of the test if it
// is still there: it is a process this test created.
func waitDead(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %s", pid, within)
}

// waitFile polls (test code only) until path exists.
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// TestRunGroupRelaysToGrandchildren proves the relay goes to the group, not
// to the child alone. The child traps the signal and stays alive (it then
// execs a long sleep), so the leftovers path never helps; the grandchild runs
// in the child's foreground (a "&" job of a non-interactive sh would have
// SIGINT ignored and could not trap it) and records the signal it receives.
// Only a signal sent to the group can reach it.
func TestRunGroupRelaysToGrandchildren(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		name := map[syscall.Signal]string{syscall.SIGTERM: "TERM", syscall.SIGINT: "INT", syscall.SIGHUP: "HUP"}[sig]
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			grand := filepath.Join(dir, "grand.sh")
			child := filepath.Join(dir, "child.sh")
			ready, got, childPid := filepath.Join(dir, "ready"), filepath.Join(dir, "got"), filepath.Join(dir, "child")
			if err := os.WriteFile(grand, []byte(`trap 'echo got >"$G"; exit 0' $SIG
echo ready >"$P"
while :; do sleep 1; done
`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(child, []byte(`trap : $SIG
echo $$ >"$C"
sh "$GRAND"
exec sleep 30
`), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", child)
			cmd.Env = append(os.Environ(), "SIG="+name, "G="+got, "P="+ready, "C="+childPid, "GRAND="+grand)
			type result struct {
				code int
				err  error
			}
			res := make(chan result, 1)
			go func() {
				code, err := RunWith(cmd, Opts{Group: true, Grace: 30 * time.Second})
				res <- result{code, err}
			}()
			waitFile(t, ready) // the grandchild's trap is in place
			cpid := readPidFile(t, childPid)
			// Whatever happens, the group is gone at the end of the test.
			t.Cleanup(func() { _ = syscall.Kill(-cpid, syscall.SIGKILL) })
			_ = syscall.Kill(os.Getpid(), sig)
			waitFile(t, got) // the grandchild received the relayed signal
			if !alive(cpid) {
				t.Fatalf("the child died: the grandchild's signal must not come from the leftovers path")
			}
			_ = syscall.Kill(-cpid, syscall.SIGKILL)
			select {
			case <-res:
			case <-time.After(5 * time.Second):
				t.Fatal("RunWith did not return")
			}
		})
	}
}

func TestRunGroupReapsLeftovers(t *testing.T) {
	f := filepath.Join(t.TempDir(), "pid")
	cmd := exec.Command("sh", "-c", `sleep 30 >/dev/null 2>&1 & echo $! >"$F"`)
	cmd.Env = append(os.Environ(), "F="+f)
	grace := 300 * time.Millisecond
	start := time.Now()
	code, err := RunWith(cmd, Opts{Group: true, Grace: grace})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if d := time.Since(start); d > grace+2*time.Second {
		t.Fatalf("Run took %s, want < grace+2s", d)
	}
	waitDead(t, readPidFile(t, f), 3*time.Second)
}

func TestRunStopsDetachedDescendant(t *testing.T) {
	dir := t.TempDir()
	f1, f2 := filepath.Join(dir, "p1"), filepath.Join(dir, "p2")
	script := `sleep 30 >/dev/null 2>&1 & echo $! >"$F1"; ` +
		`perl -e 'setpgrp(0,0); exec @ARGV' sleep 31 >/dev/null 2>&1 & echo $! >"$F2"`
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "F1="+f1, "F2="+f2)
	tree := func(int) []int { return []int{readPidFile(t, f1), readPidFile(t, f2)} }
	if _, err := RunWith(cmd, Opts{Group: true, Grace: 300 * time.Millisecond, Tree: tree}); err != nil {
		t.Fatal(err)
	}
	waitDead(t, readPidFile(t, f1), 3*time.Second)
	waitDead(t, readPidFile(t, f2), 3*time.Second)
}

func TestRunNoGroupUnchanged(t *testing.T) {
	cmd := exec.Command("sh", "-c", "ps -o pgid= -p $$")
	var out bytes.Buffer
	cmd.Stdout = &out
	if code, err := RunWith(cmd, Opts{}); err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if got := strings.TrimSpace(out.String()); got != strconv.Itoa(syscall.Getpgrp()) {
		t.Fatalf("child pgid = %s, want ours %d", got, syscall.Getpgrp())
	}
}

func TestSignalablePgid(t *testing.T) {
	for _, bad := range []int{-5, 0, 1, syscall.Getpgrp()} {
		if SignalablePgid(bad) {
			t.Errorf("SignalablePgid(%d) = true", bad)
		}
	}
	if !SignalablePgid(syscall.Getpgrp() + 100000) {
		t.Error("an ordinary pgid must be signalable")
	}
	// stop never signals a refused pgid, nor pid <= 1, nor this process.
	var sent [][2]int
	old := kill
	kill = func(pid int, sig syscall.Signal) error {
		if sig == 0 {
			return old(pid, 0)
		}
		sent = append(sent, [2]int{pid, int(sig)})
		return nil
	}
	t.Cleanup(func() { kill = old })
	tree := func(int) []int { return []int{0, 1, os.Getpid(), os.Getppid()} }
	done, _ := stopTree(os.Getpid(), syscall.Getpgrp(), Opts{Grace: 50 * time.Millisecond, Tree: tree})
	<-done
	for _, s := range sent {
		if s[0] <= 1 && s[0] >= -1 || s[0] == -syscall.Getpgrp() || s[0] == os.Getpid() {
			t.Errorf("signalled forbidden target %d with %d", s[0], s[1])
		}
	}
}

func TestRunOrphanedStops(t *testing.T) {
	f := filepath.Join(t.TempDir(), "pid")
	cmd := exec.Command("sh", "-c", `sleep 30 >/dev/null 2>&1 & echo $! >"$F"; wait`)
	cmd.Env = append(os.Environ(), "F="+f)
	begin := time.Now()
	var reasons []string
	o := Opts{
		Group: true, Grace: 300 * time.Millisecond, PollEvery: 50 * time.Millisecond,
		Orphaned: func() bool { return time.Since(begin) > 300*time.Millisecond },
		OnStop:   func(r string) { reasons = append(reasons, r) },
	}
	code, err := RunWith(cmd, o)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
	if want := 128 + int(syscall.SIGTERM); code != want {
		t.Fatalf("code = %d, want the child's own %d", code, want)
	}
	if len(reasons) != 1 || reasons[0] != "orphaned" {
		t.Fatalf("OnStop calls = %v", reasons)
	}
	waitDead(t, readPidFile(t, f), 3*time.Second)
}

func TestRunMaxRun(t *testing.T) {
	var reasons []string
	o := Opts{Group: true, MaxRun: 300 * time.Millisecond, Grace: 300 * time.Millisecond,
		OnStop: func(r string) { reasons = append(reasons, r) }}
	code, err := RunWith(exec.Command("sleep", "30"), o)
	if err != nil || code != 124 {
		t.Fatalf("code=%d err=%v, want 124", code, err)
	}
	if len(reasons) != 1 || reasons[0] != "ceiling" {
		t.Fatalf("OnStop calls = %v", reasons)
	}

	// A child that ends on its own before the ceiling keeps its own code.
	reasons = nil
	o.MaxRun = time.Second
	code, err = RunWith(exec.Command("sh", "-c", "sleep 0.1; exit 5"), o)
	if err != nil || code != 5 || len(reasons) != 0 {
		t.Fatalf("code=%d err=%v stops=%v, want 5 and no stop", code, err, reasons)
	}
}

func TestRunGraceThenKill(t *testing.T) {
	begin := time.Now()
	// The stop must not race the trap: under load sh can take longer than any
	// fixed delay to install it, and would then die of the TERM (143).
	ready := filepath.Join(t.TempDir(), "ready")
	o := Opts{Group: true, Grace: 300 * time.Millisecond, PollEvery: 20 * time.Millisecond,
		Orphaned: func() bool { _, err := os.Stat(ready); return err == nil }}
	cmd := exec.Command("sh", "-c", `trap '' TERM; : >"$1"; while :; do sleep 0.1; done`, "sh", ready)
	code, err := RunWith(cmd, o)
	if err != nil {
		t.Fatal(err)
	}
	if want := 128 + int(syscall.SIGKILL); code != want {
		t.Fatalf("code = %d, want %d (killed after the grace)", code, want)
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
}

// stubWatch installs the probe seams and restores them at the end.
func stubWatch(t *testing.T, ppid, pgrp int, reader func(int) (int, bool, error), hup bool) {
	t.Helper()
	oPPID, oPGRP, oRead, oHUP := getppid, getpgrp, ppidReader, hupIgnored
	getppid = func() int { return ppid }
	getpgrp = func() int { return pgrp }
	ppidReader = reader
	hupIgnored = func() bool { return hup }
	t.Cleanup(func() { getppid, getpgrp, ppidReader, hupIgnored = oPPID, oPGRP, oRead, oHUP })
}

func TestOrphanWatch(t *testing.T) {
	leader := 4242
	leaderPPID := 700
	leaderAlive := true
	var readErr error
	reader := func(pid int) (int, bool, error) {
		if pid != leader {
			t.Errorf("read of pid %d, want the leader %d", pid, leader)
		}
		return leaderPPID, leaderAlive, readErr
	}

	// Not armed: parent is launchd, leader's parent is launchd, SIGHUP ignored.
	stubWatch(t, 1, leader, reader, false)
	if ArmOrphanWatch() != nil {
		t.Error("armed with getppid()==1")
	}
	leaderPPID = 1
	stubWatch(t, 800, leader, reader, false)
	if ArmOrphanWatch() != nil {
		t.Error("armed with the leader's ppid==1")
	}
	leaderPPID = 700
	stubWatch(t, 800, leader, reader, true)
	if ArmOrphanWatch() != nil {
		t.Error("armed with SIGHUP ignored (nohup)")
	}

	// Armed, then the parent goes.
	cur := 800
	stubWatch(t, 800, leader, reader, false)
	getppid = func() int { return cur }
	w := ArmOrphanWatch()
	if w == nil {
		t.Fatal("not armed")
	}
	if w() {
		t.Error("orphan while everything is alive")
	}
	cur = 1
	if !w() {
		t.Error("getppid()==1 must read as orphaned")
	}
	cur = 800

	// The leader (the anchor) disappears.
	leaderAlive = false
	if !w() {
		t.Error("a vanished leader must read as orphaned")
	}
	leaderAlive = true

	// The leader's parent becomes launchd.
	leaderPPID = 1
	if !w() {
		t.Error("leader reparented to 1 must read as orphaned")
	}
	leaderPPID = 700

	// A read error other than "gone" is not an orphan.
	readErr = errors.New("transient")
	if w() {
		t.Error("a read error must not read as orphaned")
	}
}

func TestReadPPIDReal(t *testing.T) {
	pp, ok, err := readPPID(os.Getpid())
	if err != nil || !ok || pp != os.Getppid() {
		t.Fatalf("readPPID(self) = %d,%v,%v want %d,true,nil", pp, ok, err, os.Getppid())
	}
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readPPID(c.Process.Pid); ok || err != nil {
		t.Fatalf("readPPID(reaped child) alive=%v err=%v, want gone", ok, err)
	}
}

// A leftover that ignores SIGTERM is killed after the grace, even though the
// child is already reaped when the group is found non-empty.
func TestRunGroupKillsStubbornLeftover(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "pid")
	script := filepath.Join(dir, "left.sh")
	if err := os.WriteFile(script, []byte("trap '' TERM\necho $$ >\"$F\"\nwhile :; do sleep 1; done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `sh "$S" >/dev/null 2>&1 & while [ ! -s "$F" ]; do sleep 0.02; done; exit 0`)
	cmd.Env = append(os.Environ(), "F="+f, "S="+script)
	done := make(chan struct{})
	go func() {
		defer close(done)
		code, err := RunWith(cmd, Opts{Group: true, Grace: 300 * time.Millisecond})
		if err != nil || code != 0 {
			t.Errorf("code=%d err=%v", code, err)
		}
	}()
	pid := readPidFile(t, f)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunWith did not return")
	}
	waitDead(t, pid, 2*time.Second)
}

// A child that exits 0 keeps its code when the ceiling timer is ready at the
// same moment as its exit (select picks at random between ready cases). A
// leftover keeps the group non-empty so a stop would signal and turn it 124.
func TestRunMaxRunLosesToAnExitedChild(t *testing.T) {
	old := beforeLoop
	beforeLoop = func(exited <-chan error) {
		// Both cases ready: the child has exited and the 20 ms timer has fired,
		// however loaded the machine is.
		for end := time.Now().Add(5 * time.Second); len(exited) == 0 && time.Now().Before(end); {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(func() { beforeLoop = old })
	for i := 0; i < 12; i++ {
		cmd := exec.Command("sh", "-c", "sleep 30 >/dev/null 2>&1 & exit 0")
		code, err := RunWith(cmd, Opts{Group: true, MaxRun: 20 * time.Millisecond, Grace: 100 * time.Millisecond})
		if err != nil || code != 0 {
			t.Fatalf("round %d: code=%d err=%v, want 0", i, code, err)
		}
	}
}
