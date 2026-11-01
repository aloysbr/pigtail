package cmuxsync

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// « Maintenance » in the arrangement (maintenance window, C4, G1): a workspace
// with a live session holding a Mac wait that announced a duration ("90 min")
// is filed in « Maintenance », just above « Standby ». Every test goes through
// the arrange path against the simulated cmux (a non-nil sidebar, as the real
// daemon always has).

// macNeedOrder is the folder order the wiring passes with the Maintenance
// folder: ..., « Inbox », « Maintenance », « Standby ».
var macNeedOrder = []string{"Hedge", "Trellis", "Lawn", "Terrace", "Garden", space.NoSubject, space.MacMaintenance, space.Standby}

// withMacNeed wires Deps.MacNeed over the harness's real store, as the daemon
// does, and the folder order with « Maintenance ».
func withMacNeed(_ *Config, d *Deps) {
	st := d.Store
	d.FolderOrder = func() []string { return slices.Clone(macNeedOrder) }
	d.FolderIcon = func(f string) string {
		if f == space.MacMaintenance {
			return space.MacMaintenanceIcon
		}
		return testFolderIcon[f]
	}
	d.MacNeed = func() (map[string]bool, error) {
		waits, err := st.ListSessionWaits()
		if err != nil {
			return nil, err
		}
		return MacNeedSessions(waits), nil
	}
}

// macWait makes the session s-<ws> hold a Mac wait with this reason.
func (fh *folderHarness) macWait(ws, reason string) {
	fh.t.Helper()
	w := pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, Reason: reason, HolderPID: os.Getpid(), Since: fh.now}
	if err := fh.st.SetSessionWait("s-"+ws, w); err != nil {
		fh.t.Fatal(err)
	}
}

// macServed ends the held wait of s-<ws> (the Mac-hold helper was served and exited).
func (fh *folderHarness) macServed(ws string) {
	fh.t.Helper()
	if _, err := fh.st.ClearHeldWait("s-"+ws, os.Getpid()); err != nil {
		fh.t.Fatal(err)
	}
}

// macBothModes runs fn sorted, then with the sort off (the daemon's mode).
func macBothModes(t *testing.T, fn func(t *testing.T, ns bool)) {
	for _, ns := range []bool{false, true} {
		t.Run(map[bool]string{false: "sorted", true: "no sort"}[ns], func(t *testing.T) { fn(t, ns) })
	}
}

// macWorld: Terrace {A, B}, Hedge {C}, « Inbox » {Lab}, cmux creating groups
// on top as the real one (P11), sorted or not, « Maintenance » wired.
func macWorld(t *testing.T, ns bool, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	opts = append([]func(*Config, *Deps){withMacNeed}, opts...)
	if ns {
		opts = append([]func(*Config, *Deps){noSort}, opts...)
	}
	h, fw := newFolderHarness(t, opts...)
	fw.createAtTop = true
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	h.sessionIn("WS-C", "Hedge")
	fw.add(win, "Lab", "Lab")
	h.offTopic("Lab")
	h.converge(10)
	h.assertQuiet(map[bool]string{false: "sorted world", true: "no-sort world"}[ns])
	if ns {
		for ws, f := range map[string]string{"WS-A": "Terrace", "WS-B": "Terrace", "WS-C": "Hedge", "Lab": space.NoSubject} {
			if got := fw.folderOf(ws); got != f {
				t.Fatalf("world: %s in %q, want %q (%q)", ws, got, f, fw.windows[win])
			}
		}
	}
	return h, fw
}

func TestMacNeedWaitFiledInMaintenance(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		h.macWait("WS-A", "90 min")
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != space.MacMaintenance {
			t.Fatalf("WS-A in %q, want « Maintenance » (%q)", got, fw.windows[win])
		}
		if got := fw.folderOf("WS-B"); got != "Terrace" {
			t.Fatalf("WS-B in %q, want its space", got)
		}
		gr := fw.groupNamed(space.MacMaintenance)
		if gr == nil || !gr.AnchorGenerated || gr.Collapsed || gr.Icon != space.MacMaintenanceIcon {
			t.Fatalf("« Maintenance » = %+v, want a generated expanded group with %s", gr, space.MacMaintenanceIcon)
		}
		names := folderNames(fw)
		mi, si := slices.Index(names, space.MacMaintenance), slices.Index(names, space.Standby)
		ii := slices.Index(names, space.NoSubject)
		if mi < 0 || (si >= 0 && mi > si) || (ii >= 0 && mi < ii) {
			t.Fatalf("folder order %q, want « Maintenance » under « Inbox » and above « Standby »", names)
		}
		h.assertQuiet("maintenance filed")
		if ns {
			noReorder(t, fw)
		}
	})
}

func TestMacWaitWithoutNeedNotMoved(t *testing.T) { // a reason that is no duration: nothing moves
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		for _, reason := range []string{"", "/lend", "0 min", "721 min", "90min"} {
			h, fw := macWorld(t, ns)
			mark := len(fw.calls)
			h.macWait("WS-A", reason)
			h.converge(8)
			if got := fw.folderOf("WS-A"); got != "Terrace" {
				t.Fatalf("reason %q: WS-A in %q, want its space", reason, got)
			}
			if fw.groupNamed(space.MacMaintenance) != nil {
				t.Fatalf("reason %q: a « Maintenance » folder was made", reason)
			}
			if w := folderWritesIn(fw.calls[mark:]); len(w) != 0 {
				t.Fatalf("reason %q: folder writes %q", reason, w)
			}
		}
	})
}

func TestMaintenanceBeatsAreaAndBands(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		if fw.folderOf("WS-A") != "Terrace" {
			t.Fatalf("instrument: WS-A in %q, want its area", fw.folderOf("WS-A"))
		}
		h.macWait("WS-A", "30 min")
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != space.MacMaintenance {
			t.Fatalf("bound to an area, yet WS-A in %q, want « Maintenance »", got)
		}
		h.macServed("WS-A")
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != "Terrace" {
			t.Fatalf("once served WS-A in %q, want its area back", got)
		}
	})
}

// #244: a pinned tab bound to a subject is filed like any other, so it goes to
// « Maintenance » and keeps its pin (no unpin write, still pinned).
func TestPinnedLinkedFiledInMaintenanceKeepsPin(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		fw.pin("WS-A")
		h.converge(5)
		mark := len(fw.calls)
		h.macWait("WS-A", "30 min")
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != space.MacMaintenance {
			t.Fatalf("pinned bound WS-A in %q, want « Maintenance »", got)
		}
		if !fw.ws["WS-A"].Pinned {
			t.Fatal("the pin was lost")
		}
		for _, c := range fw.calls[mark:] {
			if strings.Contains(c, "unpin") {
				t.Fatalf("an unpin was written: %q", c)
			}
		}
		h.assertQuiet("pinned linked in maintenance")
	})
}

// D13 of #244: a pinned tab not bound to a subject is never filed, with or
// without a Mac wait.
func TestPinnedUnlinkedNotFiled(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		fw.add(win, "WS-U", "WS-U")
		h.session("s-WS-U", "WS-U", "idle", true) // a live session, no subject
		fw.pin("WS-U")
		h.converge(10)
		h.macWait("WS-U", "30 min")
		h.converge(10)
		if got := fw.folderOf("WS-U"); got == space.MacMaintenance {
			t.Fatalf("a pinned unlinked tab was filed in %q", got)
		}
		if fw.groupNamed(space.MacMaintenance) != nil {
			t.Fatal("a « Maintenance » folder exists for a pinned unlinked tab")
		}
	})
}

func TestStandbyBeatsMaintenance(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		h.macWait("WS-A", "30 min")
		h.putStandby("WS-A")
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != space.Standby {
			t.Fatalf("WS-A in %q, want « Standby »", got)
		}
		if fw.groupNamed(space.MacMaintenance) != nil {
			t.Fatal("a « Maintenance » folder exists for a workspace in Standby")
		}
	})
}

func TestMaintenanceLeavesWhenServed(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		h.macWait("WS-A", "45 min")
		h.converge(10)
		if fw.folderOf("WS-A") != space.MacMaintenance {
			t.Fatalf("instrument: WS-A in %q", fw.folderOf("WS-A"))
		}
		h.macServed("WS-A")
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != "Terrace" {
			t.Fatalf("served, WS-A in %q, want its space back", got)
		}
		if fw.groupNamed(space.MacMaintenance) != nil {
			t.Fatalf("the empty « Maintenance » folder stays: %+v", fw.groupNamed(space.MacMaintenance))
		}
		h.assertQuiet("maintenance left")
	})
}

func TestDeadSessionNotFiled(t *testing.T) { // 2026-10-16T00:07 /standby: a ghost session counted alive
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		fw.add(win, "WS-D", "WS-D")
		h.session("s-WS-D", "WS-D", "idle", false) // its process is gone
		h.bindIn("WS-D", "Hedge")
		h.converge(10)
		h.macWait("WS-D", "60 min")
		h.converge(10)
		if got := fw.folderOf("WS-D"); got == space.MacMaintenance {
			t.Fatalf("a dead session's wait filed WS-D in « Maintenance »")
		}
		if fw.groupNamed(space.MacMaintenance) != nil {
			t.Fatal("a « Maintenance » folder exists for a dead session")
		}
	})
}

// 2026-10-12T10:30: the focus was read before the OpInterval pause. Here the
// wait is served during the pause between two writes of the round: the
// workspace is not added to « Maintenance » on the stale read.
func TestMacNeedReadEveryTick(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		h.macWait("WS-A", "30 min")
		h.macWait("WS-B", "30 min")
		h.step(3 * time.Second) // the folder is created; its members are added in a later round
		served := false
		fw.onSleep = func() { // the first pause between two writes of the next round
			if !served {
				served = true
				h.macServed("WS-B")
			}
		}
		for i := 0; i < 4 && !served; i++ {
			h.step(3 * time.Second)
		}
		fw.onSleep = nil
		if !served {
			t.Fatalf("instrument: no round paused between two writes: %q", fw.folderWrites())
		}
		if got := fw.folderOf("WS-B"); got == space.MacMaintenance {
			t.Fatalf("WS-B filed in « Maintenance » after its wait was served")
		}
		h.converge(10)
		if fw.folderOf("WS-A") != space.MacMaintenance || fw.folderOf("WS-B") != "Terrace" {
			t.Fatalf("converged: A in %q, B in %q", fw.folderOf("WS-A"), fw.folderOf("WS-B"))
		}
	})
}

// 2026-10-12T18:30 « les dossiers restent à leur position ».
func TestOtherFoldersKeepPosition(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		others := func() []string {
			return slices.DeleteFunc(folderNames(fw), func(n string) bool { return n == space.MacMaintenance })
		}
		before := others()
		h.macWait("WS-A", "30 min")
		h.converge(10)
		if got := others(); !slices.Equal(got, before) {
			t.Fatalf("folders moved when « Maintenance » appeared: %q → %q", before, got)
		}
		h.macServed("WS-A")
		h.converge(10)
		if got := others(); !slices.Equal(got, before) {
			t.Fatalf("folders moved when « Maintenance » left: %q → %q", before, got)
		}
	})
}

func TestArrangeSignatureChangesWithMaintenance(t *testing.T) {
	t.Parallel()
	h, _ := macWorld(t, true)
	h.step(3 * time.Second)
	f, err := h.r.filing(focusView{})
	if err != nil {
		t.Fatal(err)
	}
	now := h.now
	without := h.r.arrangeSignature(win, f, focusView{}, now)
	f.MacNeed = map[string]bool{"WS-A": true}
	with := h.r.arrangeSignature(win, f, focusView{}, now)
	if with == without {
		t.Fatal("a workspace entering « Maintenance » owes no round: the signature is unchanged")
	}
	if !strings.Contains(with, "WS-A|m|") {
		t.Fatalf("signature has no maintenance class: %q", with)
	}
}

// 2026-10-17T11:48: the tail reorder moved a workspace pinned or hand-placed
// between the add and the next pass. A Mac wait appears then is served: the
// pinned workspace and the hand-placed one keep their place.
func TestPinnedAndHandPlacedKeepPlace(t *testing.T) {
	t.Parallel()
	h, fw := macWorld(t, true)
	fw.pin("WS-C")
	moveBefore(fw, "Lab", "WS-B") // Ada drags Lab up by hand
	h.converge(5)
	rest := func() []string { // everything but WS-A and the folder's own anchor
		out := slices.Clone(fw.windows[win])
		if gr := fw.groupNamed(space.MacMaintenance); gr != nil {
			out = slices.DeleteFunc(out, func(x string) bool { return x == gr.AnchorID })
		}
		return slices.DeleteFunc(out, func(x string) bool { return x == "WS-A" })
	}
	before := rest()
	cBefore, nBefore := fw.folderOf("WS-C"), fw.folderOf("Lab")
	h.macWait("WS-A", "30 min")
	h.converge(10)
	if fw.folderOf("WS-A") != space.MacMaintenance {
		t.Fatalf("instrument: WS-A in %q", fw.folderOf("WS-A"))
	}
	if got := rest(); !slices.Equal(got, before) {
		t.Fatalf("a pinned or hand-placed workspace moved when « Maintenance » appeared: %q → %q", before, got)
	}
	h.macServed("WS-A")
	h.converge(10)
	if got := rest(); !slices.Equal(got, before) {
		t.Fatalf("a pinned or hand-placed workspace moved when « Maintenance » left: %q → %q", before, got)
	}
	if fw.folderOf("WS-C") != cBefore || fw.folderOf("Lab") != nBefore {
		t.Fatalf("folders changed: C %q→%q, Lab %q→%q", cBefore, fw.folderOf("WS-C"), nBefore, fw.folderOf("Lab"))
	}
}

// 2026-10-19T00:07: folders_wait/folders_window kept running, stale, when
// cmux was unreachable. A wait appears and is served while cmux is down: no
// write, and on its return the real state rules (no stale « Maintenance »).
func TestMaintenanceNotStaleWhenCmuxDown(t *testing.T) {
	t.Parallel()
	macBothModes(t, func(t *testing.T, ns bool) {
		h, fw := macWorld(t, ns)
		h.step(31 * time.Second)
		mark := len(fw.calls)
		fw.windowsErr = errors.New("cmux: socket timeout")
		h.macWait("WS-A", "30 min")
		for i := 0; i < 4; i++ {
			h.step(3 * time.Second)
		}
		h.macServed("WS-A")
		for i := 0; i < 4; i++ {
			h.step(3 * time.Second)
		}
		if w := folderWritesIn(fw.calls[mark:]); len(w) != 0 {
			t.Fatalf("cmux down, yet folder writes: %q", w)
		}
		fw.windowsErr = nil
		h.converge(10)
		if fw.groupNamed(space.MacMaintenance) != nil {
			t.Fatal("a stale « Maintenance » folder was written when cmux returned")
		}
		if got := fw.folderOf("WS-A"); got != "Terrace" {
			t.Fatalf("WS-A in %q, want its space", got)
		}
		// Control: a wait that is still held when cmux returns is filed.
		fw.windowsErr = errors.New("cmux: socket timeout")
		h.macWait("WS-A", "30 min")
		h.step(3 * time.Second)
		fw.windowsErr = nil
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != space.MacMaintenance {
			t.Fatalf("control: WS-A in %q, want « Maintenance »", got)
		}
	})
}
