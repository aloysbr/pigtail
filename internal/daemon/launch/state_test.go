package launch

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestGetReadsCLILaunch(t *testing.T) {
	h := newHarness(t)
	t0 := time.Now().UTC().Add(-time.Minute)
	for _, e := range []spawnreg.Entry{
		{TS: t0, Kind: spawnreg.KindLaunchReserve, ID: "ln_cli", SubjectID: "sub_x", OwnerPID: os.Getpid(), Result: spawnreg.ResultPending},
		{TS: t0.Add(time.Second), Kind: spawnreg.KindLaunchRef, ID: "ln_cli", SubjectID: "sub_x", WorkspaceID: wsX, WorkspaceRef: "workspace:4", Result: spawnreg.ResultPending},
		{TS: t0.Add(2 * time.Second), Kind: spawnreg.KindLaunchArmed, ID: "ln_cli", SubjectID: "sub_x", Cell: "jp", Cwd: "/w", Mode: "plan", Result: spawnreg.ResultArmed},
		{TS: t0.Add(9 * time.Second), ID: "ln_cli", SubjectID: "sub_x", WorkspaceID: wsX, WorkspaceRef: "workspace:4", Cell: "jp", Cwd: "/w", Mode: "plan", PID: 9, Result: spawnreg.ResultLaunched},
	} {
		h.appendEntry(e)
	}
	got, ok, err := h.launcher().Get("ln_cli")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	want := pigtail.SessionLaunch{LaunchID: "ln_cli", SubjectID: "sub_x", State: pigtail.LaunchLaunched, Origin: "cli",
		WorkspaceID: wsX, WorkspaceRef: "workspace:4", Cell: "jp", PermissionMode: "plan", Cwd: "/w",
		StartedAt: t0, UpdatedAt: t0.Add(9 * time.Second)}
	if !got.StartedAt.Equal(want.StartedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("times = %v / %v", got.StartedAt, got.UpdatedAt)
	}
	got.StartedAt, got.UpdatedAt = want.StartedAt, want.UpdatedAt
	if got != want {
		t.Fatalf("Get =\n%+v\nwant\n%+v", got, want)
	}
	if _, ok, err := h.launcher().Get("ln_none"); ok || err != nil {
		t.Fatalf("unknown id: %v, %v", ok, err)
	}
}

func TestGetCarriesArmedFields(t *testing.T) {
	h, clk := confirmHarness(t)
	clk.hold = true
	pub := h.publishTo()
	l, _ := startLauncher(t, h)
	got, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, pub, pigtail.LaunchPending)
	st, ok, err := l.Get(got.LaunchID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if st.State != pigtail.LaunchPending || st.Cell != "mp" || st.Cwd != h.launchDir || st.PermissionMode != "auto" || st.WorkspaceID != got.WorkspaceID {
		t.Fatalf("Get = %+v", st)
	}
	again, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Deduplicated || again.LaunchID != got.LaunchID || again.Cell != "mp" || again.Cwd != h.launchDir || again.PermissionMode != "auto" {
		t.Fatalf("dedup = %+v", again)
	}
}

// openAPILaunch writes an API launch with its tab, armed or not.
func (h *harness) openAPILaunch(id string, born time.Time, armed bool) {
	h.appendEntry(spawnreg.Entry{TS: born.Add(-time.Second), Kind: spawnreg.KindLaunchReserve, ID: id, SubjectID: "sub_x", Origin: Origin, OwnerPID: 1 << 30, Result: spawnreg.ResultPending})
	h.appendEntry(spawnreg.Entry{TS: born, Kind: spawnreg.KindLaunchRef, ID: id, SubjectID: "sub_x", Origin: Origin, WorkspaceID: wsX, WorkspaceRef: "workspace:4", Result: spawnreg.ResultPending})
	if armed {
		h.appendEntry(spawnreg.Entry{TS: born, Kind: spawnreg.KindLaunchArmed, ID: id, SubjectID: "sub_x", Origin: Origin, WorkspaceID: wsX, WorkspaceRef: "workspace:4",
			Cell: "mp", Cwd: "/w", Mode: "auto", Prompt: "p", WorkerPID: 1 << 30, Result: spawnreg.ResultArmed})
	}
}

func (h *harness) peerOn(ws string, at time.Time) {
	h.st.peersFn = func() []pigtail.PeerSession {
		return []pigtail.PeerSession{{SessionID: "s1", PID: 55, CmuxWorkspaceID: ws, UpdatedAt: at}}
	}
}

func TestResumeAdoptsOpenAPILaunch(t *testing.T) {
	h, clk := confirmHarness(t)
	born := clk.Now().Add(-time.Minute)
	h.openAPILaunch("ln_api", born, true)
	h.peerOn(wsX, born.Add(10*time.Second))
	l, _ := startLauncher(t, h)
	l.Wait()
	st, ok, err := l.Get("ln_api")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if st.State != pigtail.LaunchLaunched || st.Cell != "mp" || st.Cwd != "/w" || st.PermissionMode != "auto" {
		t.Fatalf("resumed = %+v", st)
	}
	evs := h.eventsCopy()
	if len(evs) == 0 || evs[len(evs)-1].Data["state"] != "launched" || evs[len(evs)-1].Scope != "perso" {
		t.Fatalf("events = %+v", evs)
	}
	t.Run("deadline kept", func(t *testing.T) {
		h, clk := confirmHarness(t)
		h.openAPILaunch("ln_old", clk.Now().Add(-spawnreg.ConfirmTimeout+2*time.Second), true)
		l, _ := startLauncher(t, h)
		l.Wait()
		if st, _, _ := l.Get("ln_old"); st.State != pigtail.LaunchFailed {
			t.Fatalf("state = %+v", st)
		}
		if clk.Now().After(time.Now().Add(time.Minute)) {
			t.Fatalf("the resume waited a fresh 5 min instead of the original deadline (clock at %v)", clk.Now())
		}
	})
}

func TestResumeSkipsCLIAndRefless(t *testing.T) {
	h, clk := confirmHarness(t)
	now := clk.Now()
	h.appendEntry(spawnreg.Entry{TS: now, Kind: spawnreg.KindLaunchReserve, ID: "ln_cli", SubjectID: "sub_x", Origin: "cli", OwnerPID: os.Getpid(), Result: spawnreg.ResultPending})
	h.appendEntry(spawnreg.Entry{TS: now, Kind: spawnreg.KindLaunchRef, ID: "ln_cli", SubjectID: "sub_x", Origin: "cli", WorkspaceID: wsX, WorkspaceRef: "workspace:4", Result: spawnreg.ResultPending})
	h.appendEntry(spawnreg.Entry{TS: now, Kind: spawnreg.KindLaunchReserve, ID: "ln_noref", SubjectID: "sub_y", Origin: Origin, OwnerPID: os.Getpid(), Result: spawnreg.ResultPending})
	h.peerOn(wsX, now)
	before, _ := h.reg.Entries()
	l, _ := startLauncher(t, h)
	l.Wait()
	after, _ := h.reg.Entries()
	if len(after) != len(before) {
		t.Fatalf("resume wrote %+v", after[len(before):])
	}
	if len(h.eventsCopy()) != 0 {
		t.Fatalf("events = %+v", h.eventsCopy())
	}
}

func TestResumeRefOnlyHasEmptyArmedFields(t *testing.T) {
	h, clk := confirmHarness(t)
	born := clk.Now().Add(-time.Minute)
	h.openAPILaunch("ln_ref", born, false)
	h.peerOn(wsX, born.Add(time.Second))
	l, _ := startLauncher(t, h)
	l.Wait()
	st, _, _ := l.Get("ln_ref")
	if st.State != pigtail.LaunchLaunched || st.Cell != "" || st.Cwd != "" || st.PermissionMode != "" {
		t.Fatalf("resumed = %+v", st)
	}
	entries, _ := h.reg.Entries()
	last := entries[len(entries)-1]
	if last.Result != spawnreg.ResultLaunched || last.Cell != "" || last.Cwd != "" || last.Mode != "" || last.Prompt != "" {
		t.Fatalf("launched entry = %+v", last)
	}
}
