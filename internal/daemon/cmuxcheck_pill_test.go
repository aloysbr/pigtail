package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxsync"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
)

func writeSentinel(t *testing.T, path string, pid int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("pid=%d\n", pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// D13, T-l, D15: the remaining rows of the report about the running cmux —
// orange when `--fix` can lift them (Row.Fixable), red (Blocked) when blocked
// or failed (Row.Stuck); D16: a row with no action, neither blocked nor
// failed (background work lost, D9), is no pill. Nothing for an earlier
// launch, a fatal report, cmux down or no report; an unreadable report is an
// error (logged once by the loop). A closed surface (D10) is info, never a pill.
func TestCmuxCheckDefects(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "running.sentinel")
	writeSentinel(t, sentinel, 500)
	if ds, err := cmuxCheckDefects(dir, sentinel); err != nil || ds != nil {
		t.Fatalf("no report yet: %v, %v; want none", ds, err)
	}
	rep := cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 500}, Rows: []cmuxcheck.Row{
		{SurfaceID: "S-OK"},
		{SurfaceID: "S-FIX", Remaining: true, Problems: []string{"resumed by cmux: its width may stay frozen"}, Actions: []cmuxcheck.Action{cmuxcheck.ActRelaunch}},
		{SurfaceID: "S-LOST", Remaining: true, Problems: []string{"background work lost by the relaunch (agent x)"}}, // D16: no pill
		{SurfaceID: "S-DLG", Remaining: true, Problems: []string{"stopped on a dialog (folder trust): yours to answer"}, Blocked: "dialog on screen"},
		{SurfaceID: "S-FAIL", Remaining: true, Problems: []string{"resumed by cmux: its width may stay frozen"}, Actions: []cmuxcheck.Action{cmuxcheck.ActRelaunch}, Result: "relaunch failed: did not exit"},
		{SurfaceID: "S-CLOSED", ClosedOrphan: true, Info: []string{"surface closed since the relaunch, conversation alive nowhere"}},
	}}
	if err := cmuxcheck.SaveLast(dir, rep); err != nil {
		t.Fatal(err)
	}
	want := []cmuxsync.CheckDefect{{SurfaceID: "S-FIX"}, {SurfaceID: "S-DLG", Blocked: true}, {SurfaceID: "S-FAIL", Blocked: true}}
	if ds, err := cmuxCheckDefects(dir, sentinel); err != nil || !reflect.DeepEqual(ds, want) {
		t.Fatalf("defects %+v, %v; want %+v", ds, err, want)
	}
	writeSentinel(t, sentinel, 501) // cmux relaunched since the report
	if ds, _ := cmuxCheckDefects(dir, sentinel); ds != nil {
		t.Fatalf("report of an earlier launch: %+v, want none", ds)
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	if ds, err := cmuxCheckDefects(dir, sentinel); err != nil || ds != nil {
		t.Fatalf("cmux down: %+v, %v; want none", ds, err)
	}
	writeSentinel(t, sentinel, 500)
	if err := cmuxcheck.SaveLast(dir, cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 500}, Fatal: "cmux unreachable (socket): refused"}); err != nil {
		t.Fatal(err)
	}
	if ds, err := cmuxCheckDefects(dir, sentinel); err != nil || ds != nil {
		t.Fatalf("fatal report: %+v, %v; want none", ds, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "last.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cmuxCheckDefects(dir, sentinel); err == nil {
		t.Fatal("unreadable report: want an error")
	}
}

// É25 (B2): `pigtail cmux check --surface S1` (a partial pass) after a full
// one keeps S2's pill; when it repaired S1 (--fix), S1's pill goes.
func TestCmuxCheckDefectsAfterPartialPass(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "running.sentinel")
	writeSentinel(t, sentinel, 500)
	fixable := func(s string) cmuxcheck.Row {
		return cmuxcheck.Row{SurfaceID: s, Remaining: true, Problems: []string{"x"}, Actions: []cmuxcheck.Action{cmuxcheck.ActRelaunch}}
	}
	if err := cmuxcheck.SaveLast(dir, cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 500}, Rows: []cmuxcheck.Row{fixable("S1"), fixable("S2")}}); err != nil {
		t.Fatal(err)
	}
	// --surface S1 (inventory): S1 still in defect, S2 not looked at.
	if err := cmuxcheck.SaveLast(dir, cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 500}, Partial: true, Rows: []cmuxcheck.Row{fixable("S1")}}); err != nil {
		t.Fatal(err)
	}
	want := []cmuxsync.CheckDefect{{SurfaceID: "S1"}, {SurfaceID: "S2"}}
	if ds, err := cmuxCheckDefects(dir, sentinel); err != nil || !reflect.DeepEqual(ds, want) {
		t.Fatalf("after --surface S1: %+v, %v; want %+v (S2's pill kept)", ds, err, want)
	}
	// --surface S1 --fix repaired S1: its pill goes, S2's stays.
	repaired := cmuxcheck.Row{SurfaceID: "S1", Problems: []string{"x"}, Done: []cmuxcheck.Action{cmuxcheck.ActRelaunch}, Result: "repaired"}
	if err := cmuxcheck.SaveLast(dir, cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 500}, Partial: true, Fix: true, Rows: []cmuxcheck.Row{repaired}}); err != nil {
		t.Fatal(err)
	}
	want = []cmuxsync.CheckDefect{{SurfaceID: "S2"}}
	if ds, err := cmuxCheckDefects(dir, sentinel); err != nil || !reflect.DeepEqual(ds, want) {
		t.Fatalf("after --surface S1 --fix: %+v, %v; want %+v", ds, err, want)
	}
}

// Wiring guard (like TestServerWiresMaintenanceSidebar): the sidebar loop's
// deps read last.json and the cmux sentinel at their real paths.
func TestServerWiresCmuxCheckPill(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // cmuxcheck.DefaultSentinel lives under HOME
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.CmuxCheck == nil {
		t.Fatal("cmuxsync_wiring.go lost Deps.CmuxCheck")
	}
	writeSentinel(t, cmuxcheck.DefaultSentinel(), 42)
	if err := cmuxcheck.SaveLast(cmuxcheck.StateDir(), cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 42},
		Rows: []cmuxcheck.Row{{SurfaceID: "S-1", Remaining: true, Actions: []cmuxcheck.Action{cmuxcheck.ActRelaunch}}}}); err != nil {
		t.Fatal(err)
	}
	if ds, err := deps.CmuxCheck(); err != nil || len(ds) != 1 || ds[0].SurfaceID != "S-1" {
		t.Fatalf("deps.CmuxCheck = %+v, %v", ds, err)
	}
}

func TestCmuxCheckDefectsCarryThePID(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "running.sentinel")
	writeSentinel(t, sentinel, 500)
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	rep := cmuxcheck.Report{At: at, Launch: cmuxcheck.Launch{PID: 500}, Rows: []cmuxcheck.Row{
		{SurfaceID: "S-FIX", PID: 4000, Remaining: true, Problems: []string{"x"}, Actions: []cmuxcheck.Action{cmuxcheck.ActRelaunch}},
		{SurfaceID: "S-DLG", PID: 4001, Remaining: true, Problems: []string{"x"}, Blocked: "dialog on screen"},
	}}
	if err := cmuxcheck.SaveLast(dir, rep); err != nil {
		t.Fatal(err)
	}
	want := []cmuxsync.CheckDefect{{SurfaceID: "S-FIX", PID: 4000, At: at}, {SurfaceID: "S-DLG", Blocked: true, PID: 4001, At: at}}
	ds, err := cmuxCheckDefects(dir, sentinel)
	if err != nil || len(ds) != 2 {
		t.Fatalf("defects %+v, %v", ds, err)
	}
	for i := range want {
		if ds[i].SurfaceID != want[i].SurfaceID || ds[i].PID != want[i].PID || ds[i].Blocked != want[i].Blocked || !ds[i].At.Equal(at) {
			t.Fatalf("defect %d = %+v, want %+v", i, ds[i], want[i])
		}
	}
}

// sub_x000000000000024 (bloquant 15): an « input dead » row is no check
// defect — its signal is the daemon probe's, never last.json's.
func TestCmuxCheckDefectsIgnoreInputDeadRows(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "running.sentinel")
	writeSentinel(t, sentinel, 500)
	rep := cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 500}, Rows: []cmuxcheck.Row{
		{SurfaceID: "S-DEAD", Remaining: true, InputDead: true, Problems: []string{cmuxcheck.InputDeadProblem("S-DEAD")}, Actions: []cmuxcheck.Action{cmuxcheck.ActResume}},
		{SurfaceID: "S-DEADB", Remaining: true, InputDead: true, Problems: []string{cmuxcheck.InputDeadProblem("S-DEADB")}, Blocked: "binding account differs from the live session's: resume by hand"},
	}}
	if err := cmuxcheck.SaveLast(dir, rep); err != nil {
		t.Fatal(err)
	}
	if ds, err := cmuxCheckDefects(dir, sentinel); err != nil || len(ds) != 0 {
		t.Fatalf("defects %+v, %v; want none for input-dead rows", ds, err)
	}
}
