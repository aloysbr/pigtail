package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestHelperListener is not a real test: re-executed as a subprocess, it holds
// a port until killed. Same pattern as internal/devserver's and the watchdog's
// — duplicated rather than shared because os.Args[0] re-execs THIS package's
// test binary. A separate process is essential here: the code under test kills
// the PIDs holding the port, and an in-process listener would make the test
// binary kill itself.
func TestHelperListener(t *testing.T) {
	t.Parallel()
	if os.Getenv("HANDLERS_HELPER") != "1" {
		t.Skip("helper process only")
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("HANDLERS_PORT"))
	if err != nil {
		os.Exit(1)
	}
	defer l.Close()
	time.Sleep(60 * time.Second)
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// spawnPortListener holds port from a helper running in dir — the
// workspace's own server, as far as the cwd attribution is concerned.
func spawnPortListener(t *testing.T, port int, dir string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=TestHelperListener") //nolint:gosec // re-exec of the test binary
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HANDLERS_HELPER=1", fmt.Sprintf("HANDLERS_PORT=%d", port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
}

func waitPort(t *testing.T, port int, want bool) {
	t.Helper()
	// 30 s: a real helper process and a real lsof under a loaded machine;
	// the wait ends at the event, the bound only names a hang.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if busy, _, _ := devserver.IsPortBusy(port); busy == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port :%d never became busy=%v", port, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// newRenameDeathFixture builds the real thing: an in-memory store holding a
// project and a workspace, a real listener on the workspace's dev port, and a
// RAM monitor watching that store through its real seams (lsof-backed port
// probing included). Returns the router, the monitor and the port.
func newRenameDeathFixture(t *testing.T) (*chi.Mux, *rammon.RAMMonitor, int) {
	t.Helper()
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(&pigtail.Project{ID: "prj1", Name: "p", RootPath: root}); err != nil {
		t.Fatal(err)
	}
	port := freeTestPort(t)
	if err := st.CreateWorkspace(&pigtail.Workspace{
		ID: "ws1", ProjectID: "prj1", Path: filepath.Join(root, "old"),
		State: pigtail.StateBuilding, Ports: map[string]int{"dev": port},
	}); err != nil {
		t.Fatal(err)
	}
	spawnPortListener(t, port, filepath.Join(root, "old"))
	waitPort(t, port, true)

	cfg := rammon.Defaults()
	cfg.FloorBytes = 1         // never under the floor: no eviction path
	cfg.BatteryEnabled = false // no power sampling in a unit test
	mon := rammon.New(events.New(), st,
		rammon.WithConfig(cfg),
		rammon.WithLogger(func(string, ...any) {}),
	)
	svc := &Services{Store: st, RAMMonitor: mon} // Backend nil: the move fails AFTER the kill
	r := chi.NewRouter()
	r.Post("/workspaces/{id}/rename", RenameWorkspace(svc, testResponder{}))
	return r, mon, port
}

// TestForcedRenameDoesNotReportADeath: `POST /workspaces/{id}/rename` with
// force=true kills the workspace's dev servers from inside the daemon. The
// workspace row stays in the store, so those servers are still expected, and
// their ports go free — the exact shape of a death. It must mark its stops.
func TestForcedRenameDoesNotReportADeath(t *testing.T) {
	t.Parallel()
	r, mon, port := newRenameDeathFixture(t)

	mon.Sweep(context.Background()) // the dev server is seen alive

	rec := doJSON(t, r, "POST", "/workspaces/ws1/rename", map[string]interface{}{
		"new_slug": "new", "force": true,
	})
	// The rename itself fails (no backend wired) — deliberately: the kill
	// happens before that point, which is all this test is about.
	if rec.Code == http.StatusConflict {
		t.Fatalf("force=true should not be blocked by the busy-server guard: %s", rec.Body.String())
	}
	waitPort(t, port, false) // the forced rename really did kill the server

	mon.Sweep(context.Background())

	if deaths := mon.RecentDeaths(time.Now()); len(deaths) != 0 {
		t.Fatalf("a forced rename reported its own kills as deaths: %+v", deaths)
	}
}

// TestKillOutsideRenameIsStillADeath is the control: the same fixture, the same
// two sweeps, but the server is killed by something other than the daemon. If
// this did not report a death, the test above would pass on a broken detector.
func TestKillOutsideRenameIsStillADeath(t *testing.T) {
	t.Parallel()
	_, mon, port := newRenameDeathFixture(t)

	mon.Sweep(context.Background())

	busy, pids, _ := devserver.IsPortBusy(port)
	if !busy {
		t.Fatal("fixture listener is not running")
	}
	devserver.KillPIDs(pids)
	waitPort(t, port, false)

	mon.Sweep(context.Background())

	deaths := mon.RecentDeaths(time.Now())
	if len(deaths) != 1 || deaths[0].WorkspaceID != "ws1" || deaths[0].Port != port {
		t.Fatalf("deaths = %+v, want one for ws1:dev on :%d", deaths, port)
	}
}

// seamRenameFixture is newRenameDeathFixture on the seams alone: the port's
// listener is a switch (busy with pid 4242 inside the workspace while live),
// the kill flips it, the monitor reads the same switch on a scripted clock.
// No process, no lsof, no wall clock: what it proves cannot flake.
func seamRenameFixture(t *testing.T) (r *chi.Mux, mon *rammon.RAMMonitor, kill func(), now *time.Time) {
	t.Helper()
	root := t.TempDir()
	wsPath := filepath.Join(root, "old")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(&pigtail.Project{ID: "prj1", Name: "p", RootPath: root}); err != nil {
		t.Fatal(err)
	}
	const port, pid = 47291, 4242
	if err := st.CreateWorkspace(&pigtail.Workspace{
		ID: "ws1", ProjectID: "prj1", Path: wsPath,
		State: pigtail.StateBuilding, Ports: map[string]int{"dev": port},
	}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	live := true
	portBusy := func(p int) (bool, []int, error) {
		mu.Lock()
		defer mu.Unlock()
		if p == port && live {
			return true, []int{pid}, nil
		}
		return false, nil, nil
	}
	kill = func() { mu.Lock(); live = false; mu.Unlock() }
	clock := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	now = &clock

	cfg := rammon.Defaults()
	cfg.FloorBytes = 1
	cfg.BatteryEnabled = false
	mon = rammon.New(events.New(), st,
		rammon.WithConfig(cfg),
		rammon.WithLogger(func(string, ...any) {}),
		rammon.WithPortPIDs(portBusy),
		rammon.WithKiller(func([]int) { kill() }),
		rammon.WithClock(func() time.Time { return *now }),
	)
	svc := &Services{Store: st, RAMMonitor: mon,
		PortBusy:    portBusy,
		ProcessCwds: func([]int) (map[int]string, error) { return map[int]string{pid: wsPath}, nil },
		KillPIDs:    func([]int) { kill() },
	}
	r = chi.NewRouter()
	r.Post("/workspaces/{id}/rename", RenameWorkspace(svc, testResponder{}))
	return r, mon, kill, now
}

// TestForcedRenameMarksItsStopsOnSeams is TestForcedRenameDoesNotReportADeath's
// deterministic twin: the forced rename's kill is marked as voluntary, so the
// next sweep reports no death; the same kill from outside the rename is one
// (the control: a twin that never saw a death would prove nothing).
func TestForcedRenameMarksItsStopsOnSeams(t *testing.T) {
	t.Parallel()
	r, mon, _, now := seamRenameFixture(t)
	mon.Sweep(context.Background()) // the dev server is seen alive
	rec := doJSON(t, r, "POST", "/workspaces/ws1/rename", map[string]interface{}{
		"new_slug": "new", "force": true,
	})
	if rec.Code == http.StatusConflict {
		t.Fatalf("force=true should not be blocked by the busy-server guard: %s", rec.Body.String())
	}
	*now = now.Add(15 * time.Second)
	mon.Sweep(context.Background())
	if deaths := mon.RecentDeaths(now.Add(5 * time.Second)); len(deaths) != 0 {
		t.Fatalf("a forced rename reported its own kills as deaths: %+v", deaths)
	}

	_, mon, kill, now := seamRenameFixture(t)
	mon.Sweep(context.Background())
	kill() // killed by something other than the daemon
	*now = now.Add(15 * time.Second)
	mon.Sweep(context.Background())
	if deaths := mon.RecentDeaths(now.Add(5 * time.Second)); len(deaths) != 1 || deaths[0].WorkspaceID != "ws1" {
		t.Fatalf("control: an outside kill gave deaths = %+v, want one for ws1", deaths)
	}
}
