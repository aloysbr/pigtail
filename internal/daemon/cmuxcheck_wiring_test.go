package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxwatch"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Wiring guard (like TestServerWiresMaintenanceSidebar): Server.Run starts
// the post-relaunch watcher with Config.CmuxCheckMode and real deps — breaks
// if server.go stops calling it or drops the mode.
func TestServerStartsCmuxWatch(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := startCmuxWatchFn
	t.Cleanup(func() { startCmuxWatchFn = old })
	type call struct {
		mode string
		deps cmuxwatch.Deps
	}
	got := make(chan call, 1)
	startCmuxWatchFn = func(_ context.Context, mode string, _ func(func()), d cmuxwatch.Deps) bool {
		got <- call{mode, d}
		return true
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t), CmuxCheckMode: cmuxwatch.ModeFix}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(startDaemon(t, srv))
	select {
	case c := <-got:
		if c.mode != cmuxwatch.ModeFix {
			t.Fatalf("watcher started in mode %q, want Config.CmuxCheckMode (fix)", c.mode)
		}
		if c.deps.Sentinel == nil || c.deps.Run == nil || c.deps.Logf == nil || c.deps.SidebarOn == nil || c.deps.Args == nil || c.deps.BusyLeft == nil {
			t.Fatalf("watcher deps not wired: %+v", c.deps)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Run never called startCmuxWatch")
	}
}

func TestStartCmuxWatchOffStartsNothing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, mode := range []string{cmuxwatch.ModeOff, ""} { // "" = a daemon.Config built by a test
		spawned := 0
		if startCmuxWatch(context.Background(), mode, func(func()) { spawned++ }, cmuxwatch.Deps{}) || spawned != 0 {
			t.Fatalf("mode %q: watcher started (%d spawns)", mode, spawned)
		}
	}
	spawned := 0
	if !startCmuxWatch(context.Background(), cmuxwatch.ModeReport, func(func()) { spawned++ }, cmuxwatch.Deps{}) || spawned != 1 {
		t.Fatalf("report: spawns = %d, want 1", spawned)
	}
}

// D7: the daemon's check sees the peers of every scope. Outside any cell,
// `--scope all` speaks for the master (cmd/pigtail/scope.go resolveScope);
// the subprocess drops the cell variables pigtaild may have inherited (a
// pigtaild started from a Claude pane), so it is never a non-master cell
// whose `--scope all` the CLI would refuse.
func TestRunCmuxCheckEnvOutsideCell(t *testing.T) {
	got := cmuxCheckEnv([]string{"HOME=/h", "CLAUDE_CONFIG_DIR=/cfg/work", "PIGTAIL_SCOPE=globex", "PATH=/bin",
		"CMUX_SURFACE_ID=S", "CMUX_PANEL_ID=P", "CMUX_TAB_ID=T", "CMUX_WORKSPACE_ID=W", "CMUX_SOCKET_PATH=/s"})
	if strings.Join(got, " ") != "HOME=/h PATH=/bin CMUX_SOCKET_PATH=/s" {
		t.Fatalf("env = %q; want no cell, scope nor pane variable (the socket stays)", got)
	}
	for _, v := range cmux.PaneEnv { // the one list (Task 5): a variable added there is dropped here
		if g := cmuxCheckEnv([]string{v + "=x"}); len(g) != 0 {
			t.Errorf("%s kept: %q", v, g)
		}
	}
}

func TestRunCmuxCheckSeam(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldExec, oldSelf := cmuxCheckExec, cmuxCheckSelf
	t.Cleanup(func() { cmuxCheckExec, cmuxCheckSelf = oldExec, oldSelf })
	cmuxCheckSelf = func() (string, error) { return "/opt/pt/pigtaild", nil }
	var bin string
	var args []string
	cmuxCheckExec = func(_ context.Context, b string, a []string, out io.Writer) (int, error) {
		bin, args = b, a
		fmt.Fprintln(out, "cmux check: fake run")
		return 1, nil
	}
	ctx := context.Background()
	fixArgs := cmuxCheckArgs(cmuxwatch.ModeFix, "", nil)
	if code, err := runCmuxCheck(ctx, fixArgs); err != nil || code != 1 {
		t.Fatalf("exit %d, %v; want the CLI's exit code (1) passed through", code, err)
	}
	if bin != "/opt/pt/pigtail" || strings.Join(args, " ") != "cmux check --scope all --unattended --fix" { // D13: no --notify
		t.Fatalf("ran %s %q", bin, args)
	}
	log, err := os.ReadFile(filepath.Join(cmuxcheck.StateDir(), "daemon.log"))
	if err != nil || !strings.Contains(string(log), "pigtail cmux check --scope all --unattended --fix") || !strings.Contains(string(log), "fake run") {
		t.Fatalf("daemon.log = %q, %v", log, err)
	}
	cmuxCheckSelf = func() (string, error) { return "", errors.New("no executable") }
	bin = ""
	if code, err := runCmuxCheck(ctx, cmuxCheckArgs(cmuxwatch.ModeReport, "", nil)); code != 2 || err == nil || bin != "" {
		t.Fatalf("self unknown: exit %d, %v, ran %q; want exit 2, nothing run", code, err, bin)
	}
}

// A child killed by a signal has no exit code: the real exec says so with an
// error, never (-1, nil) that the watcher could take for a finished check.
func TestCmuxCheckExecKilledIsAnError(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pigtail")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nkill -9 $$\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, err := cmuxCheckExec(context.Background(), bin, nil, io.Discard)
	if err == nil || code == 0 || code == 1 {
		t.Fatalf("killed child: exit %d, %v; want an error", code, err)
	}
}

// The daemon's check subprocess is bounded: a check stuck on a mute cmux
// (or anything else) is killed at cmuxCheckTimeout and comes back as an
// error naming the timeout — the watcher retries it, never records it.
func TestRunCmuxCheckBounded(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pigtail"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldSelf, oldTimeout := cmuxCheckSelf, cmuxCheckTimeout
	t.Cleanup(func() { cmuxCheckSelf, cmuxCheckTimeout = oldSelf, oldTimeout })
	cmuxCheckSelf = func() (string, error) { return filepath.Join(dir, "pigtaild"), nil }
	cmuxCheckTimeout = 200 * time.Millisecond
	type res struct {
		code int
		err  error
	}
	done := make(chan res, 1)
	go func() {
		c, err := runCmuxCheck(context.Background(), cmuxCheckArgs(cmuxwatch.ModeReport, "", nil))
		done <- res{c, err}
	}()
	select {
	case r := <-done:
		if r.err == nil || !strings.Contains(r.err.Error(), "timed out") || r.code == 0 || r.code == 1 {
			t.Fatalf("stuck check: exit %d, %v; want a timeout error", r.code, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runCmuxCheck still running after 5 s: the subprocess is unbounded")
	}
}

// The watcher's log names the argv runCmuxCheck runs — one list, read by both
// (N2 pass, 2026-10-08: the log named no command). Through the seams only.
func TestCmuxWatchDepsNameTheCommand(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldExec, oldSelf := cmuxCheckExec, cmuxCheckSelf
	t.Cleanup(func() { cmuxCheckExec, cmuxCheckSelf = oldExec, oldSelf })
	cmuxCheckSelf = func() (string, error) { return "/opt/pt/pigtaild", nil }
	var ran []string
	cmuxCheckExec = func(_ context.Context, _ string, a []string, _ io.Writer) (int, error) { ran = a; return 0, nil }
	d := (&Server{cfg: Config{CmuxCheckCtrlC: "unread"}}).cmuxWatchDeps()
	if d.Args == nil || d.Run == nil {
		t.Fatal("cmuxWatchDeps: no Args or Run wired")
	}
	for _, mode := range []string{cmuxwatch.ModeReport, cmuxwatch.ModePilot, cmuxwatch.ModeFix} {
		args := d.Args(mode, []string{"S-A"})
		if _, err := d.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		if strings.Join(args, " ") != strings.Join(ran, " ") {
			t.Fatalf("%s: Args = %q, Run ran %q", mode, args, ran)
		}
	}
	if got := strings.Join(d.Args(cmuxwatch.ModePilot, nil), " "); got != "cmux check --scope all --unattended --fix-pilot --ctrl-c unread" {
		t.Fatalf("pilot Args = %q: the server's --cmux-check-ctrl-c must reach it", got)
	}
}

// D3, D6, D8: the subprocess's argv by mode — always --unattended, --ctrl-c
// only when not the default, --surface for the second pass.
func TestCmuxCheckArgsByMode(t *testing.T) {
	cases := []struct {
		mode, ctrlC string
		surfaces    []string
		want        string
	}{
		{"report", "rescue", nil, "cmux check --scope all --unattended"},
		{"pilot", "rescue", nil, "cmux check --scope all --unattended --fix-pilot"},
		{"fix", "unread", nil, "cmux check --scope all --unattended --fix --ctrl-c unread"},
		{"pilot", "rescue", []string{"S-A", "S-B"}, "cmux check --scope all --unattended --fix-pilot --surface S-A --surface S-B"},
	}
	for _, c := range cases {
		if got := strings.Join(cmuxCheckArgs(c.mode, c.ctrlC, c.surfaces), " "); got != c.want {
			t.Errorf("%s/%s/%v: %q, want %q", c.mode, c.ctrlC, c.surfaces, got, c.want)
		}
	}
}

// D6: BusyLeft reads last.json — the rows the second pass retries; an
// unreadable last.json is an error (logged by the watcher, no second pass).
func TestCmuxWatchDepsBusyLeft(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	d := (&Server{}).cmuxWatchDeps()
	if _, err := d.BusyLeft(7, true); err == nil {
		t.Fatal("no last.json: want an error")
	}
	rep := cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 7}, Rows: []cmuxcheck.Row{{SurfaceID: "S-A", Remaining: true, Blocked: "busy: x"}},
		Pilot: cmuxcheck.PilotInfo{SurfaceID: "S-A"}}
	if err := cmuxcheck.SaveLast(cmuxcheck.StateDir(), rep); err != nil {
		t.Fatal(err)
	}
	if got, err := d.BusyLeft(7, true); err != nil || strings.Join(got, ",") != "S-A" {
		t.Fatalf("BusyLeft = %v, %v", got, err)
	}
}

// D2: the notice file holds the last report's line for its launch, and the
// bar is told (cmux.check_notice); a failure is the line itself.
func TestCmuxCheckNoticeWritten(t *testing.T) {
	dir := t.TempDir()
	rep := cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 100}, Rows: []cmuxcheck.Row{{Title: "PLG", Remaining: true, Blocked: "busy: status waiting"}}}
	if err := cmuxcheck.SaveLast(dir, rep); err != nil {
		t.Fatal(err)
	}
	var published []pigtail.EventType
	writeCmuxCheckNotice(dir, 100, "", func(e pigtail.Event) { published = append(published, e.Type) }, t.Logf)
	n, err := cmuxcheck.LoadNotice(dir)
	if err != nil || n.LaunchPID != 100 || !strings.Contains(n.Line, "PLG attend ta réponse") {
		t.Fatalf("notice = %+v, %v", n, err)
	}
	if len(published) != 1 || published[0] != pigtail.EventCmuxCheckNotice {
		t.Fatalf("published = %v", published)
	}
	writeCmuxCheckNotice(dir, 101, "abandon après 3 essais", func(pigtail.Event) {}, t.Logf)
	if n, _ := cmuxcheck.LoadNotice(dir); n.LaunchPID != 101 || !strings.Contains(n.Line, "Reprise cmux impossible (abandon après 3 essais)") {
		t.Fatalf("give-up notice = %+v", n)
	}
	// Seconde passe ratée : la première a démarré des sessions, l'avis garde sa ligne.
	if err := cmuxcheck.SaveLast(dir, rep); err != nil {
		t.Fatal(err)
	}
	writeCmuxCheckNotice(dir, 100, cmuxwatch.FailSecondPass, func(pigtail.Event) {}, t.Logf)
	n, _ = cmuxcheck.LoadNotice(dir)
	if !strings.Contains(n.Line, "PLG attend ta réponse") || !strings.Contains(n.Line, "seconde passe ratée") || strings.Contains(n.Line, "impossible") {
		t.Fatalf("second-pass notice = %q, want the report line + « seconde passe ratée »", n.Line)
	}
}

// The watcher deps carry the notice writer.
func TestCmuxWatchDepsHaveNotice(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.cmuxWatchDeps().Notice == nil {
		t.Fatal("cmuxcheck_wiring.go lost Deps.Notice")
	}
}
