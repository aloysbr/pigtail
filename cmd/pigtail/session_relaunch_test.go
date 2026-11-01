package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/testutil"
)

const surfaceA = "2B514208-AFE5-4711-835C-EA9ACD31E843"
const surfaceB = "9C8D0CA4-466A-4DB5-B528-28D795F4D3FB"

// stubProcess makes findNative/pidAlive/processEnviron describe one live
// Claude (pid 4242) for any (cfg, sid) with the given environment.
func stubProcess(t *testing.T, found bool, env string, envErr error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps findNative
	savedFind, savedAlive, savedEnv := findNative, pidAlive, processEnviron
	t.Cleanup(func() { findNative, pidAlive, processEnviron = savedFind, savedAlive, savedEnv })
	findNative = func(cfg, sid string) (claudesession.Native, bool) {
		if !found {
			return claudesession.Native{}, false
		}
		return claudesession.Native{PID: 4242, SessionID: sid, ConfigDir: cfg}, true
	}
	pidAlive = func(pid int) bool { return found && pid == 4242 }
	processEnviron = func(int) (string, error) { return env, envErr }
}

func TestRouteRelaunch(t *testing.T) {
	src := claudeprofile.Cell{ID: "t", ConfigDir: "/c/t", Scope: "globex"}
	const sid = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	cases := []struct {
		name    string
		found   bool
		env     string
		envErr  error
		want    relaunchRoute
		wantErr string
	}{
		{name: "no live process: typed line, pid unknown", found: false, want: relaunchRoute{Kind: relaunchTyped}},
		{name: "bare shell: typed line, pid and surface kept", found: true,
			env:  "claude CLAUDE_CONFIG_DIR=/c/t CMUX_SURFACE_ID=" + surfaceA,
			want: relaunchRoute{Kind: relaunchTyped, PID: 4242, Surface: surfaceA}},
		{name: "launcher with proto: handoff", found: true,
			env:  "claude CLAUDE_RELAUNCH_FILE=/st/_dev_ttys9.sid CLAUDE_RELAUNCH_PROTO=1 CLAUDE_CONFIG_DIR=/c/t",
			want: relaunchRoute{Kind: relaunchHandoff, HandoffPath: "/st/_dev_ttys9.handoff", SidPath: "/st/_dev_ttys9.sid", PID: 4242}},
		{name: "old launcher: refused", found: true,
			env: "claude CLAUDE_RELAUNCH_FILE=/st/_dev_ttys9.sid CLAUDE_CONFIG_DIR=/c/t", wantErr: "exec zsh"},
		{name: "unknown proto: refused", found: true,
			env: "claude CLAUDE_RELAUNCH_FILE=/st/_dev_ttys9.sid CLAUDE_RELAUNCH_PROTO=2", wantErr: "protocol"},
		{name: "unreadable environment: refused", found: true, envErr: errors.New("ps boom"), wantErr: "ps boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubProcess(t, c.found, c.env, c.envErr)
			got, err := routeRelaunch(src, sid)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("routeRelaunch: %v", err)
			}
			if got != c.want {
				t.Fatalf("route = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestHandoffPathMirrorsTheLauncher(t *testing.T) {
	t.Parallel()
	// zsh: ${CLAUDE_RELAUNCH_FILE%.sid}.handoff
	if got := handoffPath("/st/_dev_ttys9.sid"); got != "/st/_dev_ttys9.handoff" {
		t.Fatalf("handoffPath = %q", got)
	}
	if got := handoffPath("/st/odd"); got != "/st/odd.handoff" {
		t.Fatalf("handoffPath without .sid = %q", got)
	}
}

func TestWriteHandoffIsAtomicPrivateAndCarriesOurPid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "_dev_ttys9.handoff")
	if err := writeHandoff(path, handoffPending, "mt", "abc"); err != nil {
		t.Fatal(err)
	}
	if err := writeHandoff(path, handoffReady, "mt", "abc"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ready mt abc " + strconv.Itoa(os.Getpid()) + "\n"; string(b) != want {
		t.Fatalf("content = %q, want %q", b, want)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want only the handoff (no temp left behind)", len(entries))
	}
	removeHandoff(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("handoff still there after removeHandoff: %v", err)
	}
	removeHandoff(path) // absent: no panic, no error
}

func TestQuitProbeWatchesTheKnownPid(t *testing.T) {
	savedRun, savedAlive := runningProbe, pidAlive
	t.Cleanup(func() { runningProbe, pidAlive = savedRun, savedAlive })
	running, alive := false, true
	runningProbe = func(string) bool { return running }
	pidAlive = func(pid int) bool { return alive && pid == 77 }

	if !quitProbe(77)("sid") {
		t.Fatal("a `c mt` Claude has no id in argv: its live pid must keep the session running")
	}
	if quitProbe(0)("sid") {
		t.Fatal("no known pid and nothing in argv: the session is gone")
	}
	alive = false
	if quitProbe(77)("sid") {
		t.Fatal("pid gone and nothing in argv: the session is gone")
	}
	running = true
	if !quitProbe(77)("sid") {
		t.Fatal("a process naming the session in argv keeps it running")
	}
}

func TestWatchInterruptCleansUpThenExits(t *testing.T) {
	t.Parallel()
	sigs := make(chan os.Signal, 1)
	var order []string
	exited := make(chan int, 1)
	stop := watchInterrupt(sigs, func() { order = append(order, "cleanup") }, func(code int) {
		order = append(order, "exit")
		exited <- code
	})
	sigs <- syscall.SIGINT
	if code := <-exited; code != 130 {
		t.Fatalf("exit code = %d, want 130", code)
	}
	stop()
	if strings.Join(order, ",") != "cleanup,exit" {
		t.Fatalf("order = %v, want cleanup then exit", order)
	}

	called := false
	stop = watchInterrupt(make(chan os.Signal), func() { called = true }, func(int) { called = true })
	stop()
	stop() // idempotent
	if called {
		t.Fatal("stop must end the watch without cleanup or exit")
	}
}

// fakeTable stubs claudeProcs/processEnviron with a fixed process table.
func fakeTable(t *testing.T, procs map[int][2]string) {
	t.Helper()
	testutil.SerialOnly(t) // swaps claudeProcs
	savedProcs, savedEnv := claudeProcs, processEnviron
	savedT, savedI := relaunchVerifyTimeout, relaunchVerifyInterval
	t.Cleanup(func() {
		claudeProcs, processEnviron = savedProcs, savedEnv
		relaunchVerifyTimeout, relaunchVerifyInterval = savedT, savedI
	})
	relaunchVerifyTimeout, relaunchVerifyInterval = 30*time.Millisecond, 5*time.Millisecond
	claudeProcs = func() ([]claudesession.Proc, error) {
		var out []claudesession.Proc
		for pid, p := range procs {
			out = append(out, claudesession.Proc{PID: pid, Args: p[0]})
		}
		return out, nil
	}
	processEnviron = func(pid int) (string, error) {
		p, ok := procs[pid]
		if !ok {
			return "", os.ErrNotExist
		}
		return p[0] + " " + p[1], nil
	}
}

func TestVerifyRelaunch(t *testing.T) {
	const sid = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	dst := claudeprofile.Cell{ID: "mt", ConfigDir: "/c/mt", Scope: "globex"}
	resumed := "/bin/claude --settings /tmp/s --resume " + sid + " --permission-mode auto"
	good := "CLAUDE_CONFIG_DIR=/c/mt CMUX_SURFACE_ID=" + surfaceA
	cases := []struct {
		name    string
		procs   map[int][2]string
		before  map[int]bool
		wantPID int
		wantErr error
		wantMsg string
	}{
		{name: "new process, right account, right pane", procs: map[int][2]string{900: {resumed, good}}, wantPID: 900},
		{name: "stale process from before /exit is not the relaunch",
			procs: map[int][2]string{800: {resumed, good}}, before: map[int]bool{800: true},
			wantErr: errRelaunchNotSeen},
		{name: "wrong account fails at once",
			procs:   map[int][2]string{900: {resumed, "CMUX_SURFACE_ID=" + surfaceA}},
			wantErr: errRelaunchWrongConfig, wantMsg: `CLAUDE_CONFIG_DIR=""`},
		{name: "same session resumed in another pane is ignored",
			procs:   map[int][2]string{900: {resumed, "CLAUDE_CONFIG_DIR=/c/mt CMUX_SURFACE_ID=" + surfaceB}},
			wantErr: errRelaunchNotSeen},
		{name: "a Claude not resuming this session is ignored",
			procs:   map[int][2]string{900: {"/bin/claude", good}},
			wantErr: errRelaunchNotSeen, wantMsg: "no new Claude process"},
		{name: "another session id is ignored",
			procs:   map[int][2]string{900: {"/bin/claude --resume " + sid + "0", good}},
			wantErr: errRelaunchNotSeen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeTable(t, c.procs)
			pid, err := verifyRelaunch(relaunchCheck{SessionID: sid, Dst: dst, Before: c.before, Surface: surfaceA})
			if c.wantErr == nil {
				if err != nil || pid != c.wantPID {
					t.Fatalf("verifyRelaunch = %d, %v; want %d", pid, err, c.wantPID)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if c.wantMsg != "" && !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantMsg)
			}
		})
	}
}

func TestRelaunchRecoveryNeverTypesIntoTheCLauncher(t *testing.T) {
	t.Parallel()
	const sid = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	dst := claudeprofile.Cell{ID: "mt", ConfigDir: "/c/mt", Scope: "globex"}
	r := sessionRow{SurfaceRef: "surface:1", SessionID: sid, Cwd: "/w", CellID: "t"}
	const wrapper = "/cmux/bin/cmux-claude-wrapper"
	typedLine := cmux.ResumeLine(wrapper, dst.ConfigDir, sid)
	for _, e := range []error{errRelaunchNotSeen, errRelaunchWrongConfig} {
		handoff := strings.Join(relaunchRecovery(e, relaunchRoute{Kind: relaunchHandoff}, r, dst, "/c/t/projects/-w/x.jsonl", wrapper), "\n")
		if !strings.Contains(handoff, "c mt --resume "+sid) {
			t.Fatalf("%v, handoff route: %q, want `c mt --resume <sid>`", e, handoff)
		}
		if strings.Contains(handoff, typedLine) {
			t.Fatalf("%v, handoff route: %q offers the typed line the c prompt would eat", e, handoff)
		}
		if !strings.Contains(handoff, "any other key") {
			t.Fatalf("%v, handoff route: %q, want it to say how to leave the c prompt before typing", e, handoff)
		}
		for _, want := range []string{"cmux surface resume set", "/c/t/projects/-w/x.jsonl"} {
			if !strings.Contains(handoff, want) {
				t.Fatalf("%v: %q, want it to mention %q", e, handoff, want)
			}
		}
		typed := strings.Join(relaunchRecovery(e, relaunchRoute{Kind: relaunchTyped}, r, dst, "/src", wrapper), "\n")
		if !strings.Contains(typed, typedLine) {
			t.Fatalf("%v, typed route: %q, want the resume line", e, typed)
		}
	}
}
