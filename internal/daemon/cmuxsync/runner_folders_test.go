package cmuxsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Task 6 (sub_x000000000000041): the runner applies the folder arrangement
// while the cmux window is not key (D2), against the simulated cmux of
// runner_folders_harness_test.go — never the real sidebar.

func TestArrangeNewBoundSessionLandsInSpace(t *testing.T) { // critère 1
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.setThemeSpace("thm_p", "Terrace")
	fw.add(win, "WS-A", "a")
	h.session("s1", "WS-A", "idle", true)
	h.bind("WS-A")
	fw.key = true
	h.step(time.Second)
	if len(fw.folderWrites()) != 0 {
		t.Fatalf("cmux has the focus, nothing may move: %q", fw.folderWrites())
	}
	fw.key = false
	for i := 0; i < 8; i++ {
		h.step(3 * time.Second)
	}
	if got := fw.folderOf("WS-A"); got != "Terrace" {
		t.Fatalf("WS-A in %q", got)
	}
}

// A store read error never reads as « no theme », « no subject » or « hosted »:
// the round is skipped whole, the pass stays dirty, and the next one is normal.
func TestArrangeSkipsRoundOnStoreReadError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(f *failReads, err error)
	}{
		{"ListPeers", func(f *failReads, err error) { f.peers = err }},
		{"ListAllCmuxBindings", func(f *failReads, err error) { f.bindings = err }},
		{"GetTheme", func(f *failReads, err error) { f.theme = err }},
		{"GetSubject", func(f *failReads, err error) { f.subject = err }},
		{"ListOffTopic", func(f *failReads, err error) { f.offTopic = err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fw := newFolderHarness(t)
			h.sessionIn("WS-A", "Terrace")
			h.sessionIn("WS-B", "Hedge")
			fw.add(win, "Shed", "Shed")
			h.converge(10)
			if fw.folderOf("WS-A") != "Terrace" || fw.folderOf("WS-B") != "Hedge" || fw.folderOf("Shed") != space.NoSubject {
				t.Fatalf("not converged: %+v", fw.groups)
			}
			before, errs := len(fw.folderWrites()), h.r.State().ErrorsLastHour
			tc.set(h.reads, errors.New("SQLITE_BUSY"))
			h.step(31 * time.Second) // a slow listing: the arrangement re-verifies
			if w := fw.folderWrites(); len(w) != before {
				t.Fatalf("a store read failed, yet folder writes: %q", w[before:])
			}
			if h.r.State().ErrorsLastHour <= errs {
				t.Fatal("the skipped round is not counted")
			}
			if !h.winSt().dirty {
				t.Fatal("the pass must stay dirty")
			}
			tc.set(h.reads, nil)
			h.converge(3)
			if w := fw.folderWrites(); len(w) != before {
				t.Fatalf("after the error, a normal round on a converged world: %q", w[before:])
			}
			if fw.folderOf("WS-A") != "Terrace" || fw.folderOf("WS-B") != "Hedge" {
				t.Fatalf("a folder was emptied: %+v", fw.groups)
			}
		})
	}
}

// --cmux-folders acts only with the loop on: with it off, not one cmux call.
func TestFoldersWithLoopOffMakesNoFolderCall(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Enabled = false })
	h.sessionIn("WS-A", "Terrace")
	for i := 0; i < 60; i++ {
		h.step(time.Second)
	}
	if len(fw.calls) != 0 {
		t.Fatalf("loop off, yet cmux calls: %q", fw.calls)
	}

	// Switched off after a run: the last listing is still in memory.
	h, fw = newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.converge(10)
	h.r.SetEnabled(false)
	h.converge(2) // the switch-off pass clears the pills
	mark := len(fw.calls)
	h.sessionIn("WS-B", "Hedge")
	h.wait("s-WS-A", "arbitrage")
	h.moveTo("WS-A", "Hedge") // a change the arrangement would act on
	for i := 0; i < 60; i++ {
		h.step(time.Second)
	}
	if c := fw.calls[mark:]; len(c) != 0 {
		t.Fatalf("loop switched off, yet cmux calls: %q", c)
	}
}

func TestArrangeStopsWhenFocusReturnsMidRound(t *testing.T) { // spec 4, D2
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.converge(10)
	for _, ws := range []string{"WS-B", "WS-C", "WS-D"} {
		h.sessionIn(ws, "Terrace")
	}
	mark := len(fw.calls)
	fw.onWrite = func(string) { fw.key = true }
	h.step(31 * time.Second)
	fw.onWrite = nil
	writes := fw.folderWrites()[len(folderWritesIn(fw.calls[:mark])):]
	if len(writes) != 1 {
		t.Fatalf("focus came back after the first write: %d writes %q", len(writes), writes)
	}
	for i := 0; i < 10; i++ {
		h.step(3 * time.Second)
	}
	if n := len(folderWritesIn(fw.calls[mark:])); n != 1 {
		t.Fatalf("cmux key: still %d writes", n)
	}
	fw.key = false
	h.converge(10)
	for _, ws := range []string{"WS-B", "WS-C", "WS-D"} {
		if fw.folderOf(ws) != "Terrace" {
			t.Fatalf("%s in %q", ws, fw.folderOf(ws))
		}
	}
	calls := fw.calls[mark:]
	for i, c := range calls {
		if slices.Contains(folderVerbs, strings.Fields(c)[0]) && (i == 0 || calls[i-1] != "list-windows") {
			t.Fatalf("folder write %q not preceded by a focus read: %q", c, calls[max(0, i-3):i+1])
		}
	}
}

// D2: the focus is read after the pause between two ops, right before the
// write. A window that turns key during that pause gets no further write.
func TestArrangeRereadsFocusAfterOpPause(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.converge(10)
	for _, ws := range []string{"WS-B", "WS-C", "WS-D"} {
		h.sessionIn(ws, "Terrace")
	}
	mark := len(fw.calls)
	paused := 0
	fw.onSleep = func() {
		if len(folderWritesIn(fw.calls[mark:])) > 0 { // a pause between two folder ops
			paused++
			fw.key = true
		}
	}
	h.step(31 * time.Second)
	fw.onSleep = nil
	if paused == 0 {
		t.Fatal("instrument: no pause after the first folder write — the round had a single op")
	}
	if w := folderWritesIn(fw.calls[mark:]); len(w) != 1 {
		t.Fatalf("the window turned key during the pause after the first write, yet %d writes: %q", len(w), w)
	}
}

// D2: a window whose state cannot be read — list-windows fails, or answers
// without the window — reads as key: the arrangement writes nothing to cmux.
func TestArrangeUnknownWindowStateWritesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(fw *folderWorld, on bool)
	}{
		{"list-windows fails", func(fw *folderWorld, on bool) {
			fw.windowsErr = nil
			if on {
				fw.windowsErr = errors.New("cmux: socket timeout")
			}
		}},
		{"window missing from the listing", func(fw *folderWorld, on bool) { fw.windowGone = on }},
		{"window B missing from the listing", func(fw *folderWorld, on bool) {
			fw.goneWins = map[string]bool{winB: on}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fw := newFolderHarness(t)
			h.sessionIn("WS-A", "Terrace")
			h.sessionIn("WS-B", "Hedge")
			h.sessionOn(winB, "WS-Z", "Hedge") // a second allowed window (l. 8013)
			h.converge(10)
			h.step(31 * time.Second) // a fresh listing: the steps below need none (< SlowInterval)
			if fw.folderOf("WS-A") != "Terrace" || fw.folderOf("WS-B") != "Hedge" {
				t.Fatalf("not converged: %+v", fw.groups)
			}
			mark := len(fw.calls)
			tc.set(fw, true)
			h.moveTo("WS-A", "Hedge") // a change the arrangement owes a round to
			for i := 0; i < 4; i++ {
				h.step(3 * time.Second)
			}
			if !slices.Contains(fw.calls[mark:], "list-windows") {
				t.Fatalf("instrument: the focus was never read: %q", fw.calls[mark:])
			}
			if w := folderWritesIn(fw.calls[mark:]); len(w) != 0 {
				t.Fatalf("window state unknown, yet folder writes: %q", w)
			}
			tc.set(fw, false)
			h.converge(10)
			if fw.folderOf("WS-A") != "Hedge" {
				t.Fatalf("control: once readable, the owed round runs — WS-A in %q", fw.folderOf("WS-A"))
			}
		})
	}
}

func folderWritesIn(calls []string) []string {
	var out []string
	for _, c := range calls {
		if slices.Contains(folderVerbs, strings.Fields(c)[0]) {
			out = append(out, c)
		}
	}
	return out
}

func TestArrangeDefersWhileFocused(t *testing.T) { // critère 2
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	h.converge(10)
	if h.index("WS-A") > h.index("WS-B") {
		t.Fatalf("start: A above B expected, %q", fw.windows[win])
	}
	fw.key = true
	h.wait("s-WS-B", "arbitrage")
	order := slices.Clone(fw.windows[win])
	for i := 0; i < 10; i++ {
		h.step(3 * time.Second)
	}
	if !slices.Equal(order, fw.windows[win]) {
		t.Fatalf("cmux key, the order moved: %q → %q", order, fw.windows[win])
	}
	fw.key = false
	h.converge(5)
	if h.index("WS-B") > h.index("WS-A") {
		t.Fatalf("not key: B (arbitrage) above A expected, %q", fw.windows[win])
	}
}

func TestArrangeManualMoveIsPutBack(t *testing.T) { // critère 3
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Hedge")
	h.converge(10)
	sys, mod := fw.groupNamed("Terrace"), fw.groupNamed("Hedge")
	sys.Members = slices.DeleteFunc(sys.Members, func(x string) bool { return x == "WS-A" })
	mod.Members = append(mod.Members, "WS-A")
	fw.contiguous()
	h.step(30 * time.Second)
	h.converge(5)
	if fw.folderOf("WS-A") != "Terrace" {
		t.Fatalf("WS-A in %q", fw.folderOf("WS-A"))
	}
}

func TestArrangeUnboundSessionGoesToNoSubject(t *testing.T) { // critère 4
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.add(win, "WS-A", "a")
	h.session("s1", "WS-A", "idle", true)
	h.converge(8)
	if fw.folderOf("WS-A") != space.NoSubject {
		t.Fatalf("WS-A in %q", fw.folderOf("WS-A"))
	}
}

func TestArrangeThemeWithoutSpaceGoesToNoSubject(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.add(win, "WS-A", "a")
	h.session("s1", "WS-A", "idle", true)
	h.bind("WS-A") // thm_p has no space
	h.converge(8)
	if fw.folderOf("WS-A") != space.NoSubject {
		t.Fatalf("WS-A in %q", fw.folderOf("WS-A"))
	}
}

func TestArrangeBareTerminalGoesToNoSubject(t *testing.T) { // D7
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.add(win, "Shed", "Shed")
	h.converge(8)
	if fw.folderOf("Shed") != space.NoSubject {
		t.Fatalf("Shed in %q", fw.folderOf("Shed"))
	}
	if h.index("Shed") < 0 {
		t.Fatal("a bare terminal is never closed")
	}
}

func TestArrangeDissolvesManualGroupAfterConvergence(t *testing.T) { // D9
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	h.converge(10)
	fw.manualGroup("MINE", "Mine", "GEN-M", true, "WS-B")
	h.step(30 * time.Second)
	h.converge(8)
	if fw.folderOf("WS-B") != "Terrace" || fw.group("MINE") != nil {
		t.Fatalf("manual group kept: %+v", fw.groups)
	}
	removed := 0
	for _, c := range fw.calls {
		if c == "ungroup MINE --remove-generated-anchor" {
			removed++
		}
	}
	if CloseEmptyGeneratedAnchors {
		if removed != 1 || h.index("GEN-M") >= 0 {
			t.Fatalf("D18 (A): the bare generated anchor goes, once: removed=%d, open=%v", removed, h.index("GEN-M") >= 0)
		}
	} else if removed != 0 || fw.folderOf("GEN-M") != space.NoSubject {
		t.Fatalf("D18 off: the anchor stays, in « Inbox »: removed=%d, in %q", removed, fw.folderOf("GEN-M"))
	}
}

func TestArrangeFolderIcons(t *testing.T) { // D11
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Hedge")
	fw.add(win, "Shed", "Shed")
	h.converge(10)
	for _, f := range []string{"Terrace", "Hedge", space.NoSubject} {
		gr := fw.groupNamed(f)
		if gr == nil || gr.Icon != testFolderIcon[f] {
			t.Fatalf("folder %q: %+v, want icon %q", f, gr, testFolderIcon[f])
		}
	}
	if fw.count("set-color") != 0 {
		t.Fatal("D10: never set-color")
	}
}

// standbyWorld: WS-M and WS-S bound to « Hedge » and WS-N declared off-topic
// (« Inbox »), converged and quiet.
func standbyWorld(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	h, fw := newFolderHarness(t, opts...)
	h.sessionIn("WS-M", "Hedge")
	h.sessionIn("WS-S", "Hedge")
	fw.add(win, "WS-N", "n")
	h.session("s-WS-N", "WS-N", "idle", true)
	h.offTopic("WS-N")
	h.converge(10)
	h.assertQuiet("standby world")
	if fw.folderOf("WS-S") != "Hedge" || fw.folderOf("WS-N") != space.NoSubject {
		t.Fatalf("world not filed: %+v", fw.groups)
	}
	return h, fw
}

func TestStandbyGoesToCollapsedFolderAtBottom(t *testing.T) { // critère 5
	t.Parallel()
	h, fw := standbyWorld(t)
	h.putStandby("WS-S")
	h.converge(10)
	if got := fw.folderOf("WS-S"); got != space.Standby {
		t.Fatalf("WS-S in %q, want « Standby »", got)
	}
	gr := fw.groupNamed(space.Standby)
	if !gr.Collapsed || gr.Icon != space.StandbyIcon {
		t.Fatalf("« Standby » must be born collapsed with %s: %+v", space.StandbyIcon, gr)
	}
	last := h.index(gr.AnchorID)
	for _, ws := range append(fw.members(space.NoSubject), fw.groupNamed(space.NoSubject).AnchorID) {
		if h.index(ws) > last {
			t.Fatalf("« Standby » is not below « Inbox »: %q", fw.windows[win])
		}
	}
	// The create op carries the MoveGroup: no round leaves it on top.
	created := slices.IndexFunc(fw.calls, func(c string) bool { return strings.HasPrefix(c, "group-create Standby") })
	if created < 0 || !slices.ContainsFunc(fw.calls[created:], func(c string) bool { return strings.HasPrefix(c, "group-move") }) {
		t.Fatalf("no group-move after the creation of « Standby »: %q", fw.calls)
	}
	h.assertQuiet("standby filed")
}

func TestStandbyChangeOwesARound(t *testing.T) {
	t.Parallel()
	h, fw := standbyWorld(t)
	if h.winSt().dirty {
		t.Fatal("the world must be settled")
	}
	h.putStandby("WS-S") // nothing else changes: the signature alone owes the round
	h.step(3 * time.Second)
	if fw.count("group-create") < 3 {
		t.Fatalf("a Standby change owed a round, none ran: %q", fw.folderWrites())
	}
}

func TestStandbyNeverSorted(t *testing.T) {
	t.Parallel()
	h, fw := standbyWorld(t)
	h.putStandby("WS-M", "WS-S")
	h.converge(10)
	before := slices.Clone(fw.members(space.Standby))
	if len(before) != 2 {
		t.Fatalf("members = %v", before)
	}
	h.wait("s-"+before[1], "permission") // a grave pill: sorted, it would come first
	h.converge(6)
	if after := fw.members(space.Standby); !slices.Equal(before, after) {
		t.Fatalf("« Standby » was sorted: %v then %v", before, after)
	}
}

func TestStandbyBornCollapsedDespiteMemory(t *testing.T) {
	t.Parallel()
	h, fw := standbyWorld(t)
	if err := SaveCollapseFile(h.statePath, map[string]bool{space.Standby: false}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.putStandby("WS-S")
	h.converge(10)
	if gr := fw.groupNamed(space.Standby); gr == nil || !gr.Collapsed {
		t.Fatalf("« Standby » born expanded though memory said so: %+v", gr)
	}
}

func TestStandbySelectedWaitsUntilDeselected(t *testing.T) { // D21 B
	t.Parallel()
	h, fw := standbyWorld(t)
	fw.selected = "WS-S"
	h.putStandby("WS-S")
	addsBefore := fw.adds("WS-S")
	h.converge(10)
	if fw.adds("WS-S") != addsBefore || fw.folderOf("WS-S") != "Hedge" {
		t.Fatalf("the selected workspace was filed: in %q", fw.folderOf("WS-S"))
	}
	fw.selected = "WS-M"
	h.converge(10)
	if got := fw.folderOf("WS-S"); got != space.Standby {
		t.Fatalf("WS-S in %q once deselected, want « Standby »", got)
	}
}

func TestStandbyReturnsToSpace(t *testing.T) { // critère 6
	t.Parallel()
	h, fw := standbyWorld(t)
	h.putStandby("WS-S")
	h.converge(10)
	h.leaveStandby("WS-S")
	h.converge(10)
	if got := fw.folderOf("WS-S"); got != "Hedge" {
		t.Fatalf("WS-S in %q, want its space", got)
	}
}

func TestStandbyFolderEmptiedIsDeleted(t *testing.T) { // écart c
	t.Parallel()
	for _, bare := range []bool{true, false} {
		name := map[bool]string{true: "bare anchor", false: "busy anchor"}[bare]
		t.Run(name, func(t *testing.T) {
			h, fw := standbyWorld(t)
			h.putStandby("WS-S")
			h.converge(10)
			gr := fw.groupNamed(space.Standby)
			anchor, id := gr.AnchorID, gr.ID
			if !bare {
				fw.busyShell["S-"+anchor] = true
			}
			orderBefore := []string{fw.groupNamed("Hedge").AnchorID, fw.groupNamed(space.NoSubject).AnchorID}
			h.leaveStandby("WS-S")
			h.converge(10)
			if fw.groupNamed(space.Standby) != nil || fw.folderOf("WS-S") != "Hedge" {
				t.Fatalf("« Standby » not gone: %+v", fw.groups)
			}
			removed := slices.Contains(fw.calls, "ungroup "+id+" --remove-generated-anchor")
			switch {
			case bare && (!removed || h.index(anchor) >= 0):
				t.Fatalf("the bare anchor must be closed after its proof: removed %v, index %d", removed, h.index(anchor))
			case !bare && (removed || h.index(anchor) < 0 || fw.folderOf(anchor) != space.NoSubject):
				t.Fatalf("a busy anchor stays open in « Inbox »: removed %v, index %d, in %q", removed, h.index(anchor), fw.folderOf(anchor))
			}
			if h.index("WS-S") < 0 || h.index("WS-N") < 0 {
				t.Fatal("a session was closed")
			}
			if h.index(orderBefore[0]) > h.index(orderBefore[1]) { // D23: relative order fixed
				t.Fatalf("the other folders changed order: %q", fw.windows[win])
			}
		})
	}
}

func TestStandbyRecreatedAtItsPlace(t *testing.T) { // D23
	t.Parallel()
	h, fw := standbyWorld(t)
	h.putStandby("WS-S")
	h.converge(10)
	h.leaveStandby("WS-S")
	h.converge(10)
	if fw.groupNamed(space.Standby) != nil {
		t.Fatal("« Standby » not deleted")
	}
	h.putStandby("WS-S")
	h.converge(10)
	gr := fw.groupNamed(space.Standby)
	if gr == nil || !gr.Collapsed || fw.folderOf("WS-S") != space.Standby {
		t.Fatalf("« Standby » not recreated collapsed: %+v", fw.groups)
	}
	if h.index(gr.AnchorID) < h.index(fw.groupNamed(space.NoSubject).AnchorID) {
		t.Fatalf("« Standby » recreated above « Inbox »: %q", fw.windows[win])
	}
}

func TestStandbyKeptWhileRelaunching(t *testing.T) { // D24
	t.Parallel()
	h, fw := standbyWorld(t)
	h.putStandby("WS-S")
	h.converge(10)
	if err := h.st.EndPeer("s-WS-S", h.now); err != nil {
		t.Fatal(err)
	}
	before := len(fw.folderWrites())
	h.converge(4)
	h.step(31 * time.Second)
	h.converge(4)
	if fw.folderOf("WS-S") != space.Standby || len(fw.folderWrites()) != before {
		t.Fatalf("relaunch moved WS-S: in %q, writes %q", fw.folderOf("WS-S"), fw.folderWrites()[before:])
	}
	h.session("s-WS-S", "WS-S", "idle", true) // the resume: same id, alive again
	h.converge(4)
	h.step(31 * time.Second)
	h.converge(4)
	if fw.folderOf("WS-S") != space.Standby || len(fw.folderWrites()) != before {
		t.Fatalf("resume moved WS-S: in %q, writes %q", fw.folderOf("WS-S"), fw.folderWrites()[before:])
	}
}

func TestStandbySplitWorkspaceNeedsAllSessions(t *testing.T) { // D30
	t.Parallel()
	h, fw := standbyWorld(t)
	h.session("s-b", "WS-S", "idle", true) // a second live session in WS-S (s-WS-S is the first)
	set := func(on ...string) {
		rows, _ := h.st.ListStandby()
		for _, r := range rows {
			_, _ = h.st.ClearStandby(r.SessionID, h.now)
		}
		for _, s := range on {
			if err := h.st.SetStandby(s, h.now); err != nil {
				t.Fatal(err)
			}
		}
		h.converge(10)
	}
	set("s-WS-S")
	if fw.folderOf("WS-S") != "Hedge" || fw.groupNamed(space.Standby) != nil {
		t.Fatalf("one of two sessions in Standby moved the workspace: in %q", fw.folderOf("WS-S"))
	}
	set("s-WS-S", "s-b")
	if fw.folderOf("WS-S") != space.Standby {
		t.Fatalf("both in Standby: in %q", fw.folderOf("WS-S"))
	}
	set("s-WS-S")
	if fw.folderOf("WS-S") != "Hedge" {
		t.Fatalf("s-b left Standby: in %q", fw.folderOf("WS-S"))
	}
	// D24: s-WS-S finished (a relaunch), s-b alive, both in Standby.
	set("s-WS-S", "s-b")
	if err := h.st.EndPeer("s-WS-S", h.now); err != nil {
		t.Fatal(err)
	}
	h.converge(6)
	if fw.folderOf("WS-S") != space.Standby {
		t.Fatalf("a finished session in Standby released the tab: in %q", fw.folderOf("WS-S"))
	}
	// A finished session outside Standby does not hold the tab back.
	set("s-b")
	if err := h.st.EndPeer("s-WS-S", h.now); err != nil {
		t.Fatal(err)
	}
	h.converge(6)
	if fw.folderOf("WS-S") != space.Standby {
		t.Fatalf("a finished session outside Standby kept the tab out: in %q", fw.folderOf("WS-S"))
	}
}

// A ghost — a session cmux still lists in the workspace (lifecycle running,
// never ended) whose process died without SessionEnd and whose pane is gone —
// is not a live session: it does not keep the workspace out of « Standby »
// (sub_x000000000000041, review round 1).
func TestStandbyGhostSessionDoesNotHoldTheTab(t *testing.T) { // D30
	t.Parallel()
	h, fw := standbyWorld(t)
	h.session("s-ghost", "WS-S", "idle", false) // pid dead, ended_at NULL
	h.world.closeSurface("WS-S", "S-s-ghost")   // its pane is long closed
	h.converge(6)
	h.putStandby("WS-S")
	h.converge(10)
	if got := fw.folderOf("WS-S"); got != space.Standby {
		t.Fatalf("a dead session kept WS-S out of « Standby »: in %q", got)
	}
}

func TestStandbyWorkspacesAllLiveSessions(t *testing.T) {
	t.Parallel()
	end := time.Unix(1, 0)
	p := func(id, ws string, ended bool) pigtail.PeerSession {
		ps := pigtail.PeerSession{SessionID: id, CmuxWorkspaceID: ws}
		if ended {
			ps.EndedAt = &end
		}
		return ps
	}
	for _, tc := range []struct {
		name    string
		peers   []pigtail.PeerSession
		standby map[string]bool
		want    map[string]bool
	}{
		{"one live in Standby", []pigtail.PeerSession{p("a", "w1", false)}, map[string]bool{"a": true}, map[string]bool{"W1": true}},
		{"live outside", []pigtail.PeerSession{p("a", "w1", false)}, map[string]bool{}, map[string]bool{}},
		{"two live, one in", []pigtail.PeerSession{p("a", "w1", false), p("b", "w1", false)}, map[string]bool{"a": true}, map[string]bool{}},
		{"two live, both in", []pigtail.PeerSession{p("a", "w1", false), p("b", "w1", false)}, map[string]bool{"a": true, "b": true}, map[string]bool{"W1": true}},
		{"ended in, live in", []pigtail.PeerSession{p("a", "w1", true), p("b", "w1", false)}, map[string]bool{"a": true, "b": true}, map[string]bool{"W1": true}},
		{"ended in, live out", []pigtail.PeerSession{p("a", "w1", true), p("b", "w1", false)}, map[string]bool{"a": true}, map[string]bool{}},
		{"ended out, live in", []pigtail.PeerSession{p("a", "w1", true), p("b", "w1", false)}, map[string]bool{"b": true}, map[string]bool{"W1": true}},
		{"ended only, in", []pigtail.PeerSession{p("a", "w1", true)}, map[string]bool{"a": true}, map[string]bool{"W1": true}},
		{"ended only, out", []pigtail.PeerSession{p("a", "w1", true)}, map[string]bool{}, map[string]bool{}},
		{"no workspace", []pigtail.PeerSession{p("a", "", false)}, map[string]bool{"a": true}, map[string]bool{}},
		{"dead unended out, live in", []pigtail.PeerSession{p("dead", "w1", false), p("b", "w1", false)}, map[string]bool{"b": true}, map[string]bool{"W1": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := standbyWorkspaces(tc.peers, tc.standby, func(p pigtail.PeerSession) bool { return p.EndedAt == nil && p.SessionID != "dead" })
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k := range tc.want {
				if !got[k] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestStandbyPinnedStaysOnTop (D13) is inverted by D29 into
// TestStandbyHandPinClearsStandby (runner_folders_bands_test.go).

func TestStandbyReadErrorSkipsRound(t *testing.T) {
	t.Parallel()
	fail := false
	h, fw := standbyWorld(t, func(_ *Config, d *Deps) {
		d.Standby = func() (map[string]bool, error) {
			if fail {
				return nil, errors.New("SQLITE_BUSY")
			}
			return map[string]bool{}, nil
		}
	})
	fail = true
	h.putStandby("WS-S")
	before := len(fw.folderWrites())
	h.converge(6)
	if w := fw.folderWrites(); len(w) != before {
		t.Fatalf("Standby unreadable, yet writes: %q", w[before:])
	}
	if !slices.ContainsFunc(h.logs, func(l string) bool { return strings.Contains(l, "round skipped") && strings.Contains(l, "SQLITE_BUSY") }) {
		t.Fatalf("no log line for the skipped round: %q", h.logs)
	}
}

func TestStandbyRebindReturnsToNewSpace(t *testing.T) { // D8
	t.Parallel()
	h, fw := standbyWorld(t)
	// An unbound session, filed in « Inbox » (declared off-topic).
	h.putStandby("WS-N")
	h.converge(10)
	if fw.folderOf("WS-N") != space.Standby {
		t.Fatalf("WS-N in %q", fw.folderOf("WS-N"))
	}
	addsBefore := fw.adds("WS-N")
	h.bindIn("WS-N", "Hedge") // bound during its Standby
	h.converge(10)
	if fw.folderOf("WS-N") != space.Standby || fw.adds("WS-N") != addsBefore {
		t.Fatalf("the bind moved a Standby workspace: in %q, adds %d", fw.folderOf("WS-N"), fw.adds("WS-N"))
	}
	h.leaveStandby("WS-N")
	h.converge(10)
	if got := fw.folderOf("WS-N"); got != "Hedge" {
		t.Fatalf("WS-N in %q after Standby, want « Hedge »", got)
	}
}

func TestStandbyFolderOnlyForStandbySessions(t *testing.T) { // Review Focus 5
	t.Parallel()
	t.Run("a manual group", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		h.sessionIn("WS-A", "Terrace")
		fw.add(win, "WS-U", "u")
		h.session("s-WS-U", "WS-U", "idle", true)
		fw.manualGroup("STB", space.Standby, "T-STB", false, "WS-U")
		h.converge(12)
		if first := fw.folderWrites()[0]; first != "ungroup STB" {
			t.Fatalf("the first folder write must dissolve the foreign group: %q", fw.folderWrites())
		}
		if fw.folderOf("WS-U") != space.NoSubject || fw.folderOf("T-STB") != space.NoSubject || h.index("T-STB") < 0 {
			t.Fatalf("WS-U in %q, T-STB in %q (index %d)", fw.folderOf("WS-U"), fw.folderOf("T-STB"), h.index("T-STB"))
		}
		if fw.groupNamed(space.Standby) != nil || fw.count("group-create") != 2 {
			t.Fatalf("no « Standby » without a Standby session: %+v / %q", fw.groups, fw.folderWrites())
		}
		for _, c := range fw.calls {
			if strings.HasPrefix(c, "set-icon") && strings.Contains(c, space.StandbyIcon) {
				t.Fatalf("no %s icon without a Standby session: %q", space.StandbyIcon, c)
			}
		}
		h.putStandby("WS-U")
		h.converge(10)
		gr := fw.groupNamed(space.Standby)
		if gr == nil || gr.ID == "STB" || !gr.AnchorGenerated || !gr.Collapsed || gr.Icon != space.StandbyIcon || fw.folderOf("WS-U") != space.Standby {
			t.Fatalf("a fresh « Standby » was expected: %+v", fw.groups)
		}
	})
	t.Run("a generated anchor", func(t *testing.T) {
		for _, bare := range []bool{true, false} {
			h, fw := newFolderHarness(t)
			h.sessionIn("WS-A", "Terrace")
			fw.add(win, "WS-U", "u")
			h.session("s-WS-U", "WS-U", "idle", true)
			fw.manualGroup("STB", space.Standby, "GEN-STB", true, "WS-U")
			if !bare {
				fw.busyShell["S-GEN-STB"] = true
			}
			h.converge(12)
			if fw.groupNamed(space.Standby) != nil || fw.folderOf("WS-U") != space.NoSubject {
				t.Fatalf("bare %v: the stale « Standby » stayed: %+v", bare, fw.groups)
			}
			if gone := h.index("GEN-STB") < 0; gone != bare {
				t.Fatalf("bare %v: GEN-STB closed %v", bare, gone)
			}
			if !bare && fw.folderOf("GEN-STB") != space.NoSubject {
				t.Fatalf("a busy anchor goes to « Inbox »: in %q", fw.folderOf("GEN-STB"))
			}
		}
	})
}

func TestYoungBornInStandbyIsFiledOut(t *testing.T) {
	t.Parallel()
	h, fw := standbyWorld(t)
	h.putStandby("WS-S")
	h.converge(10)
	fw.bornIn("WS-Y", space.Standby) // ⌘N from a Standby workspace
	h.step(31 * time.Second)
	h.converge(10)
	if got := fw.folderOf("WS-Y"); got != space.NoSubject {
		t.Fatalf("a young workspace born in « Standby » stays in %q, want « Inbox » without the grace", got)
	}
	if fw.folderOf("WS-S") != space.Standby {
		t.Fatalf("WS-S left « Standby »: in %q", fw.folderOf("WS-S"))
	}
	// Control: a young one born in another folder keeps the grace.
	fw.bornIn("WS-Z", "Hedge")
	h.step(31 * time.Second)
	h.converge(6)
	if fw.folderOf("WS-Z") != "Hedge" || fw.adds("WS-Z") != 0 {
		t.Fatalf("a young one born in « Hedge » moved: in %q", fw.folderOf("WS-Z"))
	}
}

func TestArrangeRolloutClosesNoSession(t *testing.T) { // critère 7
	t.Parallel()
	h, fw := newFolderHarness(t)
	for _, ws := range []string{"WS-A", "WS-B", "WS-H"} {
		h.sessionIn(ws, "Terrace")
	}
	fw.add(win, "T3", "t3")
	fw.manualGroup("PERSO", "Perso", "T1", false, "WS-A")
	fw.manualGroup("HOTFIX", "Hotfix", "WS-H", false, "WS-B") // its anchor is a session
	fw.manualGroup("ADMIN", "Compost", "T2", false, "T3")
	fw.manualGroup("SYSTEM", "Allotment", "GEN-S", true)
	fw.manualGroup("ONHOLD", "On hold", "GEN-O", true)
	h.converge(15)
	for _, ws := range []string{"WS-A", "WS-B", "WS-H", "T1", "T2", "T3"} {
		if h.index(ws) < 0 {
			t.Fatalf("%s closed", ws)
		}
	}
	for _, gr := range fw.groups {
		if !gr.AnchorGenerated || !slices.Contains(testFolderOrder, gr.Name) {
			t.Fatalf("manual group left: %+v", gr)
		}
	}
	if CloseEmptyGeneratedAnchors && (h.index("GEN-S") >= 0 || h.index("GEN-O") >= 0) {
		t.Fatalf("D18 (A): the bare anchors of Allotment and On hold go: %q", fw.windows[win])
	}
	h.assertQuiet("rollout")
}

// Spec 3, Review Focus 8: an anchor the user opened is never closed.
func TestArrangeNeverClosesAnchorWithSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		closes bool
		set    func(h *folderHarness, fw *folderWorld, anchor string)
		opt    func(*Config, *Deps)
	}{
		{"a session", false, func(h *folderHarness, _ *folderWorld, a string) { h.session("s9", a, "busy", true) }, nil},
		{"b link", false, func(h *folderHarness, _ *folderWorld, a string) { h.bind(a) }, nil},
		{"c second surface", false, func(_ *folderHarness, fw *folderWorld, a string) { fw.surfaces[a] = []string{"S-x", "S-y"} }, nil},
		{"d busy shell", false, func(_ *folderHarness, fw *folderWorld, a string) { fw.busyShell["S-"+a] = true }, nil},
		{"e unreadable shell", false, func(_ *folderHarness, fw *folderWorld, _ string) { fw.shellErr = errors.New("tty unreadable") }, nil},
		{"f no seam", false, func(*folderHarness, *folderWorld, string) {}, func(_ *Config, d *Deps) { d.ShellAtPrompt = nil }},
		{"g bare", true, func(*folderHarness, *folderWorld, string) {}, nil},
		{"h busy right before the op", false, func(_ *folderHarness, fw *folderWorld, a string) { fw.busyAfter["S-"+a] = fw.shellCalls["S-"+a] + 1 }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []func(*Config, *Deps)
			if tc.opt != nil {
				opts = append(opts, tc.opt)
			}
			h, fw := newFolderHarness(t, opts...)
			h.sessionIn("WS-A", "Terrace")
			h.converge(10)
			sys := fw.groupNamed("Terrace")
			if sys == nil {
				t.Fatal("no Terrace folder")
			}
			id, anchor := sys.ID, sys.AnchorID
			tc.set(h, fw, anchor)
			h.moveTo("WS-A", "Hedge")
			h.step(30 * time.Second)
			h.converge(10)
			removed := slices.Contains(fw.calls, "ungroup "+id+" --remove-generated-anchor")
			if removed != tc.closes || (h.index(anchor) < 0) != tc.closes {
				t.Fatalf("closes=%v want %v (anchor open=%v): %q", removed, tc.closes, h.index(anchor) >= 0, fw.folderWrites())
			}
			if fw.group(id) != nil {
				t.Fatalf("« Terrace » left: %+v", fw.groups)
			}
			if !tc.closes && fw.folderOf(anchor) != space.NoSubject {
				t.Fatalf("the kept anchor is filed in « Inbox », got %q", fw.folderOf(anchor))
			}
			if tc.name == "h busy right before the op" && !slices.Contains(fw.calls, "ungroup "+id) {
				t.Fatalf("re-proved busy: plain ungroup expected: %q", fw.folderWrites())
			}
		})
	}
}

// (h) on the D9 path: a manual group dissolved with its generated anchor,
// bare when the round is computed, busy right before the op, keeps that
// anchor open (spec 3: re-proved now, never trusted from the round's read).
func TestArrangeDissolveReprovesAnchorBeforeTheOp(t *testing.T) {
	t.Parallel()
	for _, busy := range []bool{false, true} {
		t.Run("busy="+strconv.FormatBool(busy), func(t *testing.T) {
			h, fw := newFolderHarness(t)
			h.sessionIn("WS-A", "Terrace")
			h.converge(10) // creates and adds done: the next round is R4 alone
			fw.manualGroup("SYSTEM", "Allotment", "GEN-S", true)
			if busy { // the round's read sees it bare, the read before the op busy
				fw.busyAfter["S-GEN-S"] = fw.shellCalls["S-GEN-S"] + 1
			}
			h.step(30 * time.Second)
			h.converge(12)
			removed := slices.Contains(fw.calls, "ungroup SYSTEM --remove-generated-anchor")
			if removed == busy || (h.index("GEN-S") < 0) == busy {
				t.Fatalf("busy=%v: anchor removed=%v open=%v: %q", busy, removed, h.index("GEN-S") >= 0, fw.folderWrites())
			}
			if fw.shellCalls["S-GEN-S"] < 2 {
				t.Fatalf("the anchor is proved at the round and again before the op: %d shell reads", fw.shellCalls["S-GEN-S"])
			}
			if busy {
				if !slices.Contains(fw.calls, "ungroup SYSTEM") {
					t.Fatalf("re-proved busy: plain ungroup expected: %q", fw.folderWrites())
				}
				if fw.folderOf("GEN-S") != space.NoSubject {
					t.Fatalf("the kept anchor is filed in « Inbox », got %q", fw.folderOf("GEN-S"))
				}
			}
			if fw.group("SYSTEM") != nil {
				t.Fatalf("the manual group is dissolved (D9): %+v", fw.groups)
			}
		})
	}
}

// D22: a generated anchor cmux never rendered (tty null, render_health
// not_started) has no shell to read, so the shell proof always fails on it;
// it counts as bare when every other D18 proof holds. Its shell stays
// unreadable here, as on the real window (no tty in cmux tree).
func TestArrangeClosesNeverRenderedAnchor(t *testing.T) {
	t.Parallel()
	never := func(fw *folderWorld, a string) { fw.neverRendered["S-"+a] = true }
	for _, tc := range []struct {
		name   string
		closes bool
		set    func(h *folderHarness, fw *folderWorld, anchor string)
		opt    func(*Config, *Deps)
	}{
		{"a never rendered", true, func(_ *folderHarness, fw *folderWorld, a string) { never(fw, a) }, nil},
		{"b a session", false, func(h *folderHarness, fw *folderWorld, a string) { never(fw, a); h.session("s9", a, "busy", true) }, nil},
		{"c link", false, func(h *folderHarness, fw *folderWorld, a string) { never(fw, a); h.bind(a) }, nil},
		{"d second surface", false, func(_ *folderHarness, fw *folderWorld, a string) {
			fw.surfaces[a] = []string{"S-x", "S-y"}
			fw.neverRendered["S-x"], fw.neverRendered["S-y"] = true, true
		}, nil},
		{"e doubt (tty null, render_health not not_started)", false, func(*folderHarness, *folderWorld, string) {}, nil},
		{"f render state unreadable", false, func(_ *folderHarness, fw *folderWorld, a string) {
			never(fw, a)
			fw.renderErr = errors.New("cmux tree unreadable")
		}, nil},
		{"g started right before the op", false, func(_ *folderHarness, fw *folderWorld, a string) {
			never(fw, a)
			fw.startedAfter["S-"+a] = fw.renderCalls["S-"+a] + 1
		}, nil},
		{"h no seam", false, func(_ *folderHarness, fw *folderWorld, a string) { never(fw, a) },
			func(_ *Config, d *Deps) { d.SurfaceNeverRendered = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []func(*Config, *Deps)
			if tc.opt != nil {
				opts = append(opts, tc.opt)
			}
			h, fw := newFolderHarness(t, opts...)
			h.sessionIn("WS-A", "Terrace")
			h.converge(10)
			sys := fw.groupNamed("Terrace")
			if sys == nil {
				t.Fatal("no Terrace folder")
			}
			id, anchor := sys.ID, sys.AnchorID
			fw.shellErr = errors.New("cmux tree gives it no tty")
			tc.set(h, fw, anchor)
			h.moveTo("WS-A", "Hedge")
			h.step(30 * time.Second)
			h.converge(10)
			removed := slices.Contains(fw.calls, "ungroup "+id+" --remove-generated-anchor")
			if removed != tc.closes || (h.index(anchor) < 0) != tc.closes {
				t.Fatalf("closes=%v want %v (anchor open=%v): %q", removed, tc.closes, h.index(anchor) >= 0, fw.folderWrites())
			}
			if fw.group(id) != nil {
				t.Fatalf("« Terrace » left: %+v", fw.groups)
			}
			if !tc.closes && fw.folderOf(anchor) != space.NoSubject {
				t.Fatalf("the kept anchor is filed in « Inbox », got %q", fw.folderOf(anchor))
			}
			if tc.closes && fw.renderCalls["S-"+anchor] < 2 {
				t.Fatalf("never rendered read at the round and again before the op: %d reads", fw.renderCalls["S-"+anchor])
			}
			if tc.name == "g started right before the op" && !slices.Contains(fw.calls, "ungroup "+id) {
				t.Fatalf("started at the re-read: plain ungroup expected: %q", fw.folderWrites())
			}
		})
	}
}

// D22 on the D9 path: never rendered at the round's read, started at the
// re-read right before the op. It then needs the shell proof: closed only
// when its shell is at its prompt, kept when that proof fails.
func TestArrangeNeverRenderedStartedFallsBackToShellProof(t *testing.T) {
	t.Parallel()
	for _, shellOK := range []bool{false, true} {
		t.Run("shellAtPrompt="+strconv.FormatBool(shellOK), func(t *testing.T) {
			h, fw := newFolderHarness(t)
			h.sessionIn("WS-A", "Terrace")
			h.converge(10)
			fw.manualGroup("SYSTEM", "Allotment", "GEN-S", true)
			fw.neverRendered["S-GEN-S"] = true
			fw.startedAfter["S-GEN-S"] = fw.renderCalls["S-GEN-S"] + 1
			if !shellOK {
				fw.busyShell["S-GEN-S"] = true
			}
			h.step(30 * time.Second)
			h.converge(12)
			removed := slices.Contains(fw.calls, "ungroup SYSTEM --remove-generated-anchor")
			if removed != shellOK || (h.index("GEN-S") < 0) != shellOK {
				t.Fatalf("shellOK=%v: anchor removed=%v open=%v: %q", shellOK, removed, h.index("GEN-S") >= 0, fw.folderWrites())
			}
			if fw.renderCalls["S-GEN-S"] < 2 || fw.shellCalls["S-GEN-S"] < 1 {
				t.Fatalf("re-read before the op, then the shell proof: %d render reads, %d shell reads",
					fw.renderCalls["S-GEN-S"], fw.shellCalls["S-GEN-S"])
			}
			if !shellOK && fw.folderOf("GEN-S") != space.NoSubject {
				t.Fatalf("the kept anchor is filed in « Inbox », got %q", fw.folderOf("GEN-S"))
			}
			if fw.group("SYSTEM") != nil {
				t.Fatalf("the manual group is dissolved (D9): %+v", fw.groups)
			}
		})
	}
}

// Critère 7 on the real window (verify round 2): the generated anchors of
// the dissolved groups were never rendered, so their shell is unreadable;
// they go all the same (D18 (A) with D22).
func TestArrangeRolloutClosesNeverRenderedAnchors(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.manualGroup("SYSTEM", "Allotment", "GEN-S", true)
	fw.manualGroup("ONHOLD", "On hold", "GEN-O", true)
	fw.manualGroup("ADMIN", "Compost", "GEN-A", true)
	fw.neverRendered["S-GEN-S"], fw.neverRendered["S-GEN-O"], fw.neverRendered["S-GEN-A"] = true, true, true
	fw.shellErr = errors.New("cmux tree gives it no tty")
	h.converge(15)
	if h.index("WS-A") < 0 {
		t.Fatal("WS-A closed")
	}
	for _, a := range []string{"GEN-S", "GEN-O", "GEN-A"} {
		if CloseEmptyGeneratedAnchors && h.index(a) >= 0 {
			t.Fatalf("D22: never rendered anchor %s left open in %q: %q", a, fw.folderOf(a), fw.windows[win])
		}
	}
	h.assertQuiet("rollout")
}

func TestAnchorHostsReadsCmuxWorkspaceID(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.add(win, "ANC", "anchor")
	h.session("s1", "ANC", "idle", true)
	if got, err := h.r.anchorHostsSession("ANC"); err != nil || !got {
		t.Fatalf("a registry row on the cmux workspace: hosted, got %v, %v", got, err)
	}
	if h.r.closable(win, "ANC") {
		t.Fatal("a registry row on the cmux workspace: never closable")
	}

	h, fw = newFolderHarness(t)
	fw.add(win, "ANC", "anchor")
	h.sessionWorktreeOnly("s2", "ANC") // pigtail worktree id = the anchor's uuid, cmux workspace unknown
	if got, err := h.r.anchorHostsSession("ANC"); err != nil || got {
		t.Fatalf("a row naming a pigtail worktree only: not hosted, got %v, %v", got, err)
	}
	if !h.r.closable(win, "ANC") {
		t.Fatal("a row naming a pigtail worktree only does not block condition (1)")
	}

	h.reads.peers = errors.New("SQLITE_BUSY")
	if got, err := h.r.anchorHostsSession("ANC"); err == nil {
		t.Fatalf("a registry read error is returned, never (%v, nil)", got)
	}
}

func TestArrangeRunnerDissolvesFolderWhoseAnchorHostsSession(t *testing.T) {
	t.Parallel()
	for _, withSession := range []bool{true, false} {
		t.Run("session="+strconv.FormatBool(withSession), func(t *testing.T) {
			h, fw := newFolderHarness(t)
			h.sessionIn("WS-A", "Hedge")
			h.sessionIn("WS-B", "Hedge")
			h.sessionIn("WS-C", "Garden")
			h.converge(10)
			mod, arl := fw.groupNamed("Hedge"), fw.groupNamed("Garden")
			oldID, oldAnchor, arlID := mod.ID, mod.AnchorID, arl.ID
			if withSession {
				h.session("s9", oldAnchor, "idle", true)
			}
			h.bindIn(oldAnchor, "Terrace")
			h.step(30 * time.Second)
			h.converge(12)
			for _, c := range fw.calls {
				if strings.HasPrefix(c, "ungroup "+oldID+" --remove-generated-anchor") {
					t.Fatalf("the anchor hosts a session, never closed: %q", c)
				}
				if strings.HasPrefix(c, "rename "+oldAnchor+" Hedge") {
					t.Fatalf("the session is never the folder's header: %q", c)
				}
			}
			for _, ws := range []string{"WS-A", "WS-B", "WS-C", oldAnchor} {
				if h.index(ws) < 0 {
					t.Fatalf("%s closed", ws)
				}
			}
			if fw.folderOf(oldAnchor) != "Terrace" {
				t.Fatalf("the anchor-session is filed in its space: %q", fw.folderOf(oldAnchor))
			}
			mod = fw.groupNamed("Hedge")
			if mod == nil || mod.AnchorID == oldAnchor || fw.folderOf("WS-A") != "Hedge" || fw.folderOf("WS-B") != "Hedge" {
				t.Fatalf("« Hedge » recreated with a new anchor, A and B in it: %+v", fw.groups)
			}
			if fw.group(arlID) == nil {
				t.Fatal("a folder whose anchor hosts nothing is left alone")
			}
		})
	}
}

// Spec 7, Review Focus 9: a folder emptied, deleted and recreated is born
// in the folded state the user last left it in, even after a restart.
func TestArrangeRecreatedFolderKeepsCollapse(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.converge(10)
	fw.groupNamed("Terrace").Collapsed = true // Ada folds it
	h.step(30 * time.Second)                  // a pass reads it
	h.converge(2)

	empty := func(what string) {
		t.Helper()
		h.moveTo("WS-A", "Hedge")
		h.step(30 * time.Second)
		h.converge(10)
		if fw.groupNamed("Terrace") != nil {
			t.Fatalf("%s: « Terrace » emptied, not deleted: %+v", what, fw.groups)
		}
	}
	refill := func(what string, wantCollapsed bool) {
		t.Helper()
		mark := len(fw.calls)
		h.moveTo("WS-A", "Terrace")
		h.step(30 * time.Second)
		h.converge(10)
		sys := fw.groupNamed("Terrace")
		if sys == nil || sys.Collapsed != wantCollapsed || fw.folderOf("WS-A") != "Terrace" {
			t.Fatalf("%s: « Terrace » recreated %+v, collapsed want %v", what, sys, wantCollapsed)
		}
		w := folderWritesIn(fw.calls[mark:])
		i := slices.Index(w, "group-create Terrace")
		if i >= 0 && i+1 < len(w) && strings.HasPrefix(w[i+1], "group-move "+sys.ID+" ") {
			i++ // D6: placed at its fixed position in the same op
		}
		if i < 0 || i+1 >= len(w) || w[i+1] != "set-icon "+sys.ID+" house.fill" {
			t.Fatalf("%s: create then set-icon in a row: %q", what, w)
		}
		if wantCollapsed && (i+2 >= len(w) || w[i+2] != "group-collapse "+sys.ID) {
			t.Fatalf("%s: born collapsed in the same op: %q", what, w)
		}
		if !wantCollapsed && slices.Contains(w, "group-collapse "+sys.ID) {
			t.Fatalf("%s: born expanded: %q", what, w)
		}
		if fw.groupNamed("Hedge") != nil {
			t.Fatalf("%s: Hedge emptied, deleted: %+v", what, fw.groups)
		}
	}

	empty("same daemon")
	mod := fw.groupNamed("Hedge")
	if mod == nil || mod.Collapsed || slices.Contains(fw.calls, "group-collapse "+mod.ID) {
		t.Fatalf("a folder never seen is born expanded: %+v", mod)
	}
	refill("same daemon", true)

	empty("before a restart")
	h.restart()
	refill("after a restart", true)

	empty("before an unreadable state")
	if err := os.WriteFile(h.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.restart()
	logs := len(h.logs)
	refill("unreadable state", false)
	n := 0
	for _, l := range h.logs[logs:] {
		if strings.Contains(l, "folded state") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unreadable state: one log line, got %d: %q", n, h.logs[logs:])
	}
}

func TestArrangeQuietWhenConverged(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Hedge")
	h.converge(10)
	writes := len(fw.folderWrites())
	for i := 0; i < 60; i++ {
		mark := len(fw.calls)
		h.step(time.Second)
		pass := fw.calls[mark:]
		if slices.Contains(pass, "list-windows") && !slices.Contains(pass, "tree "+win) {
			t.Fatalf("pass %d: a focus read outside a slow pass: %q", i, pass)
		}
	}
	if w := fw.folderWrites(); len(w) != writes {
		t.Fatalf("converged, yet writes: %q", w[writes:])
	}
}

func TestArrangeSettlesAfterWrite(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.FocusPoll = 100 * time.Millisecond }) // the settle alone spaces the re-reads
	for _, ws := range []string{"WS-A", "WS-B", "WS-C"} {
		h.sessionIn(ws, "Terrace")
	}
	fw.manualGroup("MINE", "Mine", "T1", false, "WS-B")
	for i := 0; i < 60; i++ {
		h.step(500 * time.Millisecond)
	}
	if fw.folderOf("WS-B") != "Terrace" {
		t.Fatalf("not converged: %+v", fw.groups)
	}
	if fw.tooSoon != 0 {
		t.Fatalf("%d group lists read less than ArrangeSettle after a write", fw.tooSoon)
	}
}

func stopWrites(fw *folderWorld) int {
	return fw.count("group-create", "group-add", "group-remove", "ungroup", "reorder", "place", "group-move", "set-icon", "group-collapse", "group-expand", "group-rename")
}

func TestArrangeStopsWhenCreatedNameDiffers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		set      func(fw *folderWorld)
		read     string // the name read back, in the note
		nextList bool   // caught on the next round's list, not on the create output
	}{
		{"a double encoding", func(fw *folderWorld) { fw.renameOnCreate = func(string) string { return "SysteÌ\u0080me" } }, "SysteÌ\u0080me", false},
		{"b NFD", func(fw *folderWorld) { fw.renameOnCreate = func(string) string { return "Systéme" } }, "Systéme", false},
		{"c anchor not generated", func(fw *folderWorld) { fw.createNotGen = true }, "", true},
		{"d create without name", func(fw *folderWorld) {
			fw.createOmitsName = true
			fw.renameOnCreate = func(string) string { return "Systéme" }
		}, "Systéme", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fw := newFolderHarness(t)
			tc.set(fw)
			h.sessionIn("WS-A", "Terrace")
			h.converge(4)
			if n := fw.count("group-create"); n != 1 {
				t.Fatalf("exactly one create, got %d: %q", n, fw.folderWrites())
			}
			w := fw.folderWrites()
			if !tc.nextList && (w[len(w)-1] != "group-create Terrace" || fw.count("ungroup") != 0) {
				t.Fatalf("stopped right after the create, nothing undone: %q", w)
			}
			note := h.r.State().FoldersNote
			if note == "" || (tc.read != "" && !strings.Contains(note, strconv.Quote(tc.read))) {
				t.Fatalf("FoldersNote = %q", note)
			}
			n := stopWrites(fw)
			for i := 0; i < 10; i++ {
				h.step(3 * time.Second)
			}
			h.step(31 * time.Second)
			h.sessionIn("WS-B", "Hedge")
			for i := 0; i < 19; i++ {
				h.step(3 * time.Second)
			}
			if stopWrites(fw) != n {
				t.Fatalf("folders stopped, yet writes: %q", fw.folderWrites())
			}
			h.r.SetEnabled(true)
			h.converge(4)
			if fw.count("group-create") < 2 {
				t.Fatalf("SetEnabled(true) lifts the stop: %q", fw.folderWrites())
			}
		})
	}
}

func TestArrangeCreatedNotYetListed(t *testing.T) {
	t.Parallel()
	t.Run("lag", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		fw.createOmitsName, fw.listLag = true, 1500*time.Millisecond
		h.sessionIn("WS-A", "Terrace")
		h.step(time.Second)
		if fw.count("group-create") != 1 || h.r.State().FoldersNote != "" || h.r.foldersStopped {
			t.Fatalf("one create, no stop: %q, note %q", fw.folderWrites(), h.r.State().FoldersNote)
		}
		h.converge(8)
		if fw.count("group-create") != 1 || h.r.foldersStopped || len(h.winSt().unverified) != 0 || fw.folderOf("WS-A") != "Terrace" {
			t.Fatalf("recognised next round: creates=%d stopped=%v unverified=%v", fw.count("group-create"), h.r.foldersStopped, h.winSt().unverified)
		}
		h.assertQuiet("lag")
	})
	t.Run("second read", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		fw.createOmitsName, fw.hideReads = true, 1
		h.sessionIn("WS-A", "Terrace")
		h.converge(10)
		if fw.count("group-create") != 1 || h.r.foldersStopped || fw.folderOf("WS-A") != "Terrace" {
			t.Fatalf("one create, no stop: %q", fw.folderWrites())
		}
	})
	t.Run("never listed", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		fw.createOmitsName, fw.neverList = true, true
		h.sessionIn("WS-A", "Terrace")
		h.converge(createUnseenRounds + 4)
		if fw.count("group-create") != 1 || fw.count("ungroup") != 0 {
			t.Fatalf("one create, nothing undone: %q", fw.folderWrites())
		}
		if note := h.r.State().FoldersNote; note != "folder created but never listed by cmux: folders stopped" {
			t.Fatalf("FoldersNote = %q", note)
		}
	})
}

func TestArrangeBoundsCreateWhileUnrecognised(t *testing.T) {
	t.Parallel()
	t.Run("deleted then recreated", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		h.sessionIn("WS-A", "Terrace")
		h.converge(6)
		h.moveTo("WS-A", "Hedge")
		h.step(30 * time.Second)
		h.converge(6)
		if fw.groupNamed("Terrace") != nil {
			t.Fatalf("Terrace not deleted: %+v", fw.groups)
		}
		h.step(10 * time.Second)
		h.sessionIn("WS-C", "Terrace")
		h.step(30 * time.Second)
		h.converge(6)
		if n := fw.count("group-create"); n != 3 || h.r.foldersStopped || h.r.State().FoldersNote != "" { // Terrace, Hedge, Terrace
			t.Fatalf("recreated, no stop: creates=%d note=%q", n, h.r.State().FoldersNote)
		}
	})
	t.Run("removed by hand once listed", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		h.sessionIn("WS-A", "Terrace")
		h.converge(6)
		sys := fw.groupNamed("Terrace")
		fw.groups = slices.DeleteFunc(fw.groups, func(x cmux.Group) bool { return x.ID == sys.ID }) // ungrouped by hand
		h.step(30 * time.Second)
		h.converge(8)
		if fw.groupNamed("Terrace") == nil || h.r.foldersStopped {
			t.Fatalf("recreated, no stop: %+v note %q", fw.groups, h.r.State().FoldersNote)
		}
	})
	t.Run("still there, unrecognised", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		h.sessionIn("WS-A", "Terrace")
		h.converge(6)
		fw.groupNamed("Terrace").Name = "Systéme" // cmux now serves it under another name
		h.step(30 * time.Second)
		h.converge(6)
		if n := fw.count("group-create"); n != 1 || !h.r.foldersStopped || !strings.Contains(h.r.State().FoldersNote, "second create") {
			t.Fatalf("bounded: creates=%d stopped=%v note=%q", n, h.r.foldersStopped, h.r.State().FoldersNote)
		}
	})
}

// D8 (sub_x000000000000007): with several allowed windows each one is
// arranged on its own; the note of the former idle is gone.
func TestArrangeSeveralWindowsArranged(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionOn(winB, "WS-Z", "Hedge")
	h.converge(10)
	if fw.folderOf("WS-A") != "Terrace" {
		t.Fatalf("window A: WS-A in %q", fw.folderOf("WS-A"))
	}
	if got := fw.folderIn(winB, "WS-Z"); got != "Hedge" {
		t.Fatalf("window B is arranged too: WS-Z in %q, groups %+v", got, fw.groupsIn(winB))
	}
	if n := h.r.State().FoldersNote; n != "" {
		t.Fatalf("FoldersNote = %q, want none in service", n)
	}
	h.assertQuiet("two windows")
}

func TestArrangeCountsOnlyAllowedWindows(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Windows = []string{win} })
	h.sessionIn("WS-A", "Terrace")
	fw.add(winB, "WS-Z", "z")
	h.converge(6)
	if fw.folderOf("WS-A") != "Terrace" {
		t.Fatalf("the allowed window is arranged: %+v", fw.groups)
	}
	for _, c := range fw.calls {
		if strings.Contains(c, winB) && slices.Contains(append(slices.Clone(folderVerbs), "group-list"), strings.Fields(c)[0]) {
			t.Fatalf("folder call on another window: %q", c)
		}
	}
}

// --- Lot 3 (sub_x000000000000007): every window on its own (D8) ----------

// twoWindowWorld: WS-A (Hedge) in win, WS-B (Terrace) in winB, both
// allowed, converged and quiet.
func twoWindowWorld(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	h, fw := newFolderHarness(t, opts...)
	h.sessionIn("WS-A", "Hedge")
	h.sessionOn(winB, "WS-B", "Terrace")
	h.converge(10)
	h.assertQuiet("two-window world")
	if fw.folderOf("WS-A") != "Hedge" || fw.folderIn(winB, "WS-B") != "Terrace" {
		t.Fatalf("two-window world not filed: A %+v, B %+v", fw.groups, fw.groupsIn(winB))
	}
	return h, fw
}

func TestArrangeEachWindowOwnFolders(t *testing.T) { // Review Focus 5
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Hedge")
	h.sessionIn("WS-F", "Garden")
	h.sessionOn(winB, "WS-B", "Terrace")
	h.sessionOn(winB, "WS-G", "Garden")
	h.focusOn(offsite, fTheme(h.themeOf("Garden")))
	h.converge(12)
	if fw.folderOf("WS-A") != "Hedge" || fw.folderOf("WS-F") != offsite {
		t.Fatalf("window A: WS-A in %q, WS-F in %q", fw.folderOf("WS-A"), fw.folderOf("WS-F"))
	}
	if fw.folderIn(winB, "WS-B") != "Terrace" || fw.folderIn(winB, "WS-G") != offsite {
		t.Fatalf("window B: WS-B in %q, WS-G in %q", fw.folderIn(winB, "WS-B"), fw.folderIn(winB, "WS-G"))
	}
	// each window has its own folders: none of the other window's
	for _, gr := range fw.groups {
		if gr.Name == "Terrace" {
			t.Fatalf("window A holds « Terrace », a folder only window B needs: %+v", fw.groups)
		}
	}
	for _, gr := range fw.groupsIn(winB) {
		if gr.Name == "Hedge" {
			t.Fatalf("window B holds « Hedge », a folder only window A needs: %+v", fw.groupsIn(winB))
		}
	}
	// the focus on top of both (D4)
	if fw.windows[win][0] != fw.groupNamed(offsite).AnchorID {
		t.Fatalf("window A: focus not on top: %q", fw.windows[win])
	}
	if fw.windows[winB][0] != fw.groupNamedIn(winB, offsite).AnchorID {
		t.Fatalf("window B: focus not on top: %q", fw.windows[winB])
	}
	// every write carries the window of its workspace
	for _, c := range fw.writesOn(win) {
		for _, f := range strings.Fields(c) {
			if f == "WS-B" || f == "WS-G" {
				t.Fatalf("a write for a window B workspace aimed at window A: %q", c)
			}
		}
	}
	for _, c := range fw.writesOn(winB) {
		for _, f := range strings.Fields(c) {
			if f == "WS-A" || f == "WS-F" {
				t.Fatalf("a write for a window A workspace aimed at window B: %q", c)
			}
		}
	}
	if len(fw.writesOn(winB)) == 0 {
		t.Fatal("instrument: no write aimed at window B")
	}
	h.assertQuiet("each window")
}

func TestArrangeAnyKeyWindowHoldsAll(t *testing.T) { // focus D11, two allowed windows
	t.Parallel()
	for _, tc := range []struct {
		name string
		key  func(fw *folderWorld, on bool)
	}{
		{"allowed window B key", func(fw *folderWorld, on bool) { fw.other(winB).key = on }},
		{"window C outside Config.Windows key", func(fw *folderWorld, on bool) {
			if fw.keyWins == nil {
				fw.keyWins = map[string]bool{}
			}
			fw.keyWins[winC] = on
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fw := twoWindowWorld(t, func(c *Config, _ *Deps) { c.Windows = []string{win, winB} })
			fw.add(winC, "WS-Z", "z")
			tc.key(fw, true)
			mark := len(fw.calls)
			h.moveTo("WS-A", "Garden")
			h.moveTo("WS-B", "Trellis")
			h.step(31 * time.Second)
			h.converge(6)
			if !slices.Contains(fw.calls[mark:], "list-windows") {
				t.Fatal("instrument: the windows were never read")
			}
			if w := folderWritesIn(fw.calls[mark:]); len(w) != 0 {
				t.Fatalf("a cmux window is key, yet writes: %q", w)
			}
			tc.key(fw, false)
			h.converge(10)
			if fw.folderOf("WS-A") != "Garden" || fw.folderIn(winB, "WS-B") != "Trellis" {
				t.Fatalf("control: no window key, WS-A in %q, WS-B in %q", fw.folderOf("WS-A"), fw.folderIn(winB, "WS-B"))
			}
		})
	}
}

// l. 8012: the focus is re-read after the pause, whichever window is written.
func TestArrangeKeyDuringPauseStopsAllWindows(t *testing.T) {
	t.Parallel()
	h, fw := twoWindowWorld(t)
	for _, ws := range []string{"WS-C", "WS-D", "WS-E"} {
		h.sessionIn(ws, "Hedge") // adds into window A's existing folder: one write per op
	}
	h.sessionOn(winB, "WS-Y", "Terrace")
	mark := len(fw.calls)
	paused := 0
	fw.onSleep = func() {
		if len(folderWritesIn(fw.calls[mark:])) > 0 {
			paused++
			fw.other(winB).key = true
		}
	}
	h.step(31 * time.Second)
	fw.onSleep = nil
	if paused == 0 {
		t.Fatal("instrument: no pause after the first folder write")
	}
	if w := folderWritesIn(fw.calls[mark:]); len(w) != 1 {
		t.Fatalf("window B turned key during the pause after window A's first write, yet %d writes: %q", len(w), w)
	}
	fw.other(winB).key = false
	h.converge(12)
	if fw.folderOf("WS-C") != "Hedge" || fw.folderIn(winB, "WS-Y") != "Terrace" {
		t.Fatalf("control: WS-C in %q, WS-Y in %q", fw.folderOf("WS-C"), fw.folderIn(winB, "WS-Y"))
	}
}

func TestArrangeWindowClosedForgetsState(t *testing.T) {
	t.Parallel()
	h, fw := twoWindowWorld(t)
	if h.r.wins[winB] == nil {
		t.Fatal("instrument: no state for window B")
	}
	saved := fw.windows[winB]
	delete(fw.windows, winB) // window B closed
	fw.byWin[winB].groups = nil
	h.step(31 * time.Second)
	h.converge(2)
	if _, ok := h.r.wins[winB]; ok {
		t.Fatal("window B closed, its state kept")
	}
	if h.r.wins[win] == nil || fw.folderOf("WS-A") != "Hedge" {
		t.Fatalf("window A changed: %+v", fw.groups)
	}
	// it comes back (a new window, no group): its folders are made again
	delete(fw.byWin, winB)
	fw.windows[winB] = slices.DeleteFunc(saved, func(id string) bool { return strings.HasPrefix(id, "GEN-") })
	h.step(31 * time.Second)
	h.converge(10)
	if fw.folderIn(winB, "WS-B") != "Terrace" {
		t.Fatalf("window B back: WS-B in %q, groups %+v", fw.folderIn(winB, "WS-B"), fw.groupsIn(winB))
	}
	if h.r.foldersStopped || strings.Contains(h.r.State().FoldersNote, "second create") {
		t.Fatalf("folders stopped: %q", h.r.State().FoldersNote)
	}
}

func TestCollapseMemoryTwoWindowsNoChurn(t *testing.T) {
	t.Parallel()
	saves := 0
	h, fw := twoWindowWorld(t, func(_ *Config, d *Deps) {
		save := d.SaveCollapse
		d.SaveCollapse = func(m map[string]bool) error { saves++; return save(m) }
	})
	h.sessionOn(winB, "WS-M", "Hedge")
	h.converge(10)
	if fw.folderIn(winB, "WS-M") != "Hedge" {
		t.Fatalf("not converged: B %+v", fw.groupsIn(winB))
	}
	fw.groupNamed("Hedge").Collapsed = true // folded by hand in win, expanded in winB
	saves = 0
	for i := 0; i < 10; i++ {
		h.step(31 * time.Second) // a slow listing: a round each time
	}
	if saves > 1 {
		t.Fatalf("SaveCollapse called %d times in 10 passes, want at most 1", saves)
	}
	if !fw.groupNamed("Hedge").Collapsed || fw.groupNamedIn(winB, "Hedge").Collapsed {
		t.Fatal("a fold was changed by the arrangement")
	}
}

func TestArrangeCreatedVerifiedPerWindow(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.hideReads = 6 // a created group stays unlisted for 6 lists of its window
	h.sessionIn("WS-A", "Hedge")
	h.sessionOn(winB, "WS-B", "Hedge")
	creates := func() int {
		n := 0
		for _, c := range fw.folderWrites() {
			if strings.HasPrefix(c, "group-create Hedge") {
				n++
			}
		}
		return n
	}
	h.converge(3) // window A's « Hedge » is still unlisted
	if n := creates(); n != 2 {
		t.Fatalf("« Hedge » created %d times while window A's is unlisted, want one per window: %q", n, fw.folderWrites())
	}
	h.converge(12)
	if n := creates(); n != 2 {
		t.Fatalf("« Hedge » created %d times, want once per window: %q", n, fw.folderWrites())
	}
	if fw.folderOf("WS-A") != "Hedge" || fw.folderIn(winB, "WS-B") != "Hedge" {
		t.Fatalf("WS-A in %q, WS-B in %q", fw.folderOf("WS-A"), fw.folderIn(winB, "WS-B"))
	}
	if h.r.foldersStopped {
		t.Fatalf("folders stopped: %q", h.r.State().FoldersNote)
	}
}

func TestArrangePinnedWorkspaceInsideOwnFolderRunner(t *testing.T) { // P10a, at the runner
	t.Parallel()
	h, fw := newFolderHarness(t)
	for _, ws := range []string{"WS-A", "WS-P", "WS-B"} {
		h.sessionIn(ws, "Hedge")
	}
	h.converge(10)
	fw.pin("WS-P")
	slot := h.index("WS-P") // measured after the pin: cmux moves the first pinned member to the head of its group (P24 e)
	h.wait("s-WS-B", "arbitrage")
	mark := len(fw.calls)
	h.step(30 * time.Second)
	h.converge(6)
	w := folderWritesIn(fw.calls[mark:])
	if len(w) != 1 || !strings.HasPrefix(w[0], "reorder ") {
		t.Fatalf("one reorder, in one round: %q", w)
	}
	if h.index("WS-P") != slot || h.index("WS-B") > h.index("WS-A") {
		t.Fatalf("P keeps its slot, B above A: %q", fw.windows[win])
	}
}

// Inverted from TestArrangeLeavesPinned (Review Focus 1, D13) by D3 of
// sub_x000000000000014: the pinned WS-X, bound in « Terrace », is filed
// there; the pinned group PG and its member WS-Y are still never touched.
func TestArrangeFilesBoundPinnedLeavesPinnedGroup(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-X", "Terrace")
	h.sessionIn("WS-Y", "Terrace")
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	fw.pin("WS-X")
	fw.manualGroup("PG", "Mine", "T-PG", false, "WS-Y")
	fw.group("PG").Pinned = true
	fw.windows[win] = append([]string{"WS-X", "T-PG", "WS-Y"}, slices.DeleteFunc(slices.Clone(fw.windows[win]), func(x string) bool {
		return x == "WS-X" || x == "T-PG" || x == "WS-Y"
	})...)
	h.converge(10)
	h.wait("s-WS-B", "arbitrage")
	h.converge(4)
	h.clearWait("s-WS-B")
	h.wait("s-WS-A", "arbitrage")
	h.converge(4)
	for _, c := range fw.folderWrites() {
		f := strings.Fields(c)
		if (f[0] == "group-add" && f[2] == "WS-Y") || (f[0] == "ungroup" && f[1] == "PG") {
			t.Fatalf("pinned group touched: %q", c)
		}
	}
	if !slices.Contains(fw.folderWrites(), "group-add "+fw.groupNamed("Terrace").ID+" WS-X") {
		t.Fatalf("bound pinned WS-X never added to « Terrace »: %q", fw.folderWrites())
	}
	sys := fw.groupNamed("Terrace")
	if got := fw.windows[win][:3]; !slices.Equal(got, []string{"T-PG", "WS-Y", sys.AnchorID}) {
		t.Fatalf("want the pinned group on top, then « Terrace »: %q", fw.windows[win])
	}
	if fw.group("PG") == nil || fw.folderOf("WS-Y") != "Mine" || fw.folderOf("WS-X") != "Terrace" {
		t.Fatalf("pinned group moved or WS-X not filed: %+v", fw.groups)
	}
}

func TestArrangeNeverTouchesCollapse(t *testing.T) { // Review Focus 2
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.converge(8)
	mark := len(fw.calls)
	fw.groupNamed("Terrace").Collapsed = true
	h.sessionIn("WS-B", "Terrace")
	h.wait("s-WS-B", "arbitrage")
	h.step(30 * time.Second)
	h.converge(6)
	for _, c := range fw.calls[mark:] {
		if strings.HasPrefix(c, "group-collapse") || strings.HasPrefix(c, "group-expand") {
			t.Fatalf("collapse touched after creation: %q", c)
		}
	}
	if !fw.groupNamed("Terrace").Collapsed || fw.folderOf("WS-B") != "Terrace" {
		t.Fatalf("Terrace: %+v", fw.groupNamed("Terrace"))
	}
}

func TestArrangeStopsRoundOnError(t *testing.T) { // Review Focus 3
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	fw.manualGroup("MINE", "Mine", "GEN-M", true)
	fw.failOn = "group-add"
	errs := h.r.State().ErrorsLastHour
	h.converge(4)
	if fw.count("group-add") == 0 {
		t.Fatal("no add tried")
	}
	if fw.count("ungroup", "reorder", "place") != 0 {
		t.Fatalf("a failed add ends the round: %q", fw.folderWrites())
	}
	if h.r.State().ErrorsLastHour <= errs {
		t.Fatal("the failure is counted")
	}
	// A persistent failure is retried each round but logged once, like a
	// skipped round (arrangeFailed).
	h.converge(4)
	logged := 0
	for _, l := range h.logs {
		if strings.Contains(l, "Error: injected") {
			logged++
		}
	}
	if tries := fw.count("group-add"); tries < 2 || logged != 1 {
		t.Fatalf("%d tries, logged %d times, want ≥ 2 tries logged once: %q", tries, logged, h.logs)
	}
	fw.failOn = ""
	h.converge(10)
	if fw.folderOf("WS-A") != "Terrace" || fw.folderOf("WS-B") != "Terrace" || fw.group("MINE") != nil {
		t.Fatalf("converges once lifted: %+v", fw.groups)
	}
}

func TestArrangeFollowsSpaceChange(t *testing.T) { // Review Focus 4
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.setThemeSpace("thm_p", "Terrace")
	fw.add(win, "WS-A", "a")
	h.session("s1", "WS-A", "idle", true)
	h.bind("WS-A")
	h.converge(8)
	if fw.folderOf("WS-A") != "Terrace" {
		t.Fatalf("WS-A in %q", fw.folderOf("WS-A"))
	}
	h.setThemeSpace("thm_p", "Hedge")
	h.converge(10)
	if fw.folderOf("WS-A") != "Hedge" {
		t.Fatalf("theme moved space: WS-A in %q", fw.folderOf("WS-A"))
	}
}

// Review Focus 4, second half: a subject re-linked to a theme of another
// space moves its session at the next round out of focus, not before.
func TestArrangeFollowsSubjectRelinkedToAnotherTheme(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.converge(8)
	if fw.folderOf("WS-A") != "Terrace" {
		t.Fatalf("WS-A in %q", fw.folderOf("WS-A"))
	}
	sub, err := h.st.GetSubject(h.subjectIn("Terrace"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.st.GetSubject(h.subjectIn("Hedge")) // a theme whose space is « Hedge »
	if err != nil {
		t.Fatal(err)
	}
	if other.ThemeID == sub.ThemeID {
		t.Fatal("instrument: both subjects share a theme")
	}
	sub.ThemeID = other.ThemeID
	if err := h.st.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	fw.key = true
	mark := len(fw.calls)
	h.converge(5)
	if w := folderWritesIn(fw.calls[mark:]); len(w) != 0 || fw.folderOf("WS-A") != "Terrace" {
		t.Fatalf("cmux key: nothing may move, got %q, WS-A in %q", w, fw.folderOf("WS-A"))
	}
	fw.key = false
	h.converge(10)
	if fw.folderOf("WS-A") != "Hedge" {
		t.Fatalf("subject re-linked to a « Hedge » theme: WS-A in %q", fw.folderOf("WS-A"))
	}
}

func TestArrangeOffByDefault(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Folders = false })
	h.sessionIn("WS-A", "Terrace")
	h.converge(10)
	if n := fw.count(append(slices.Clone(folderVerbs), "group-list")...); n != 0 {
		t.Fatalf("Folders off: %d folder calls", n)
	}
}

func TestArrangeRemovesThenAdds(t *testing.T) { // P2 (b)
	t.Parallel()
	for _, needs := range []bool{true, false} {
		h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.AddNeedsRemove = needs })
		fw.addNeedsRemove = needs
		h.sessionIn("WS-A", "Terrace")
		h.converge(6)
		h.sessionIn("WS-B", "Terrace")
		fw.manualGroup("MINE", "Mine", "GEN-M", true, "WS-B")
		h.step(30 * time.Second)
		h.converge(8)
		if fw.folderOf("WS-B") != "Terrace" || h.index("WS-B") < 0 {
			t.Fatalf("needs=%v: WS-B in %q", needs, fw.folderOf("WS-B"))
		}
		w := fw.folderWrites()
		rm := slices.Index(w, "group-remove WS-B")
		add := slices.Index(w, "group-add "+fw.groupNamed("Terrace").ID+" WS-B")
		if needs && (rm < 0 || add < rm) {
			t.Fatalf("remove before add: %q", w)
		}
		if !needs && fw.count("group-remove") != 0 {
			t.Fatalf("no remove when add moves: %q", w)
		}
	}
}

func TestArrangeMovesFolderBlocks(t *testing.T) { // P4b
	t.Parallel()
	h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.GroupMoves = true })
	fw.blocksFixed = true
	h.sessionIn("WS-C", "Garden")
	h.converge(6)
	h.sessionIn("WS-B", "Terrace")
	h.sessionIn("WS-A", "Hedge")
	h.step(30 * time.Second)
	h.converge(10)
	var names []string
	for _, ws := range fw.windows[win] {
		for _, gr := range fw.groups {
			if gr.AnchorID == ws {
				names = append(names, gr.Name)
			}
		}
	}
	if !slices.Equal(names, []string{"Hedge", "Terrace", "Garden"}) {
		t.Fatalf("D6 order: %q", names)
	}
	if fw.count("group-move") == 0 {
		t.Fatal("blocks placed by group-move")
	}
	for _, c := range fw.calls {
		if strings.HasPrefix(c, "group-move") && fw.group(strings.Fields(c)[1]) == nil {
			t.Fatalf("move of a group that is not an own folder: %q", c)
		}
		if strings.Contains(c, "--to-index") {
			t.Fatalf("never by index: %q", c)
		}
	}
}

// D21 B: the window's selected workspace is never added to a collapsed folder.
func TestArrangeNeverAddsSelectedToCollapsedFolder(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-B", "Terrace")
	h.converge(8)
	sys := fw.groupNamed("Terrace")
	sys.Collapsed = true
	h.sessionIn("WS-A", "Terrace")
	fw.selected = "WS-A"
	h.step(30 * time.Second)
	h.converge(6)
	if slices.Contains(fw.calls, "group-add "+sys.ID+" WS-A") || !fw.groupNamed("Terrace").Collapsed {
		t.Fatalf("(a) the selected workspace is never added to a collapsed folder: %q", fw.folderWrites())
	}
	fw.selected = "WS-B"
	h.step(30 * time.Second)
	h.converge(6)
	if !slices.Contains(fw.calls, "group-add "+sys.ID+" WS-A") || !fw.groupNamed("Terrace").Collapsed {
		t.Fatalf("(b) no longer selected: added, folder still collapsed: %q", fw.folderWrites())
	}

	h, fw = newFolderHarness(t) // (c) the selection moves to A between two ops of a round
	h.sessionIn("WS-B", "Terrace")
	h.converge(8)
	sys = fw.groupNamed("Terrace")
	sys.Collapsed = true
	h.sessionIn("WS-X", "Terrace")
	h.sessionIn("WS-A", "Terrace")
	fw.selected = "WS-Z"
	fw.onWrite = func(c string) {
		if c == "group-add "+sys.ID+" WS-X" {
			fw.selected = "WS-A"
		}
	}
	h.step(30 * time.Second)
	h.converge(4)
	if !slices.Contains(fw.calls, "group-add "+sys.ID+" WS-X") || slices.Contains(fw.calls, "group-add "+sys.ID+" WS-A") {
		t.Fatalf("(c) the add of the newly selected A is not emitted: %q", fw.folderWrites())
	}
	if !fw.groupNamed("Terrace").Collapsed {
		t.Fatal("(c) Terrace unfolded")
	}
}

// P11: cmux accepts an unknown SF Symbol and stores none. The icon is asked
// once per created group, never again every round: the arrangement converges.
func TestArrangeIconDroppedByCmuxConverges(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.dropIcons["house.fill"] = true
	h.sessionIn("WS-A", "Terrace")
	h.converge(10)
	if fw.folderOf("WS-A") != "Terrace" {
		t.Fatalf("WS-A in %q", fw.folderOf("WS-A"))
	}
	h.assertQuiet("icon dropped by cmux")
	if n := fw.count("set-icon"); n != 1 {
		t.Fatalf("set-icon asked %d times, want once", n)
	}
}

// --- the D12 toggle (Task 8) ----------------------------------------------

// toggle is enqueueToggle, then one Step, then the reply read without
// waiting: it must be there (deterministic, no goroutine).
func (fh *folderHarness) toggle() (ToggleResult, error) {
	fh.t.Helper()
	reply, err := fh.r.enqueueToggle(context.Background())
	if err != nil {
		return ToggleResult{}, err
	}
	fh.r.Step()
	select {
	case rep := <-reply:
		return rep.res, rep.err
	default:
		fh.t.Fatal("a Step served no toggle reply")
		return ToggleResult{}, nil
	}
}

// toggleWorld is a converged window: two sessions in « Terrace », one in
// « Hedge », the loose terminal in « Inbox ».
func toggleWorld(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	h, fw := newFolderHarness(t, opts...)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	h.sessionIn("WS-C", "Hedge")
	fw.add(win, "Shed", "Shed")
	h.converge(10)
	for _, f := range []string{"Terrace", "Hedge", space.NoSubject} {
		if fw.groupNamed(f) == nil {
			t.Fatalf("not converged, no %q: %+v", f, fw.groups)
		}
	}
	return h, fw
}

func ownFoldersCollapsed(fw *folderWorld) map[string]bool {
	out := map[string]bool{}
	for _, f := range testFolderOrder {
		if gr := fw.groupNamed(f); gr != nil {
			out[f] = gr.Collapsed
		}
	}
	return out
}

func TestToggleFoldersAppliesWhileKey(t *testing.T) {
	t.Parallel()
	h, fw := toggleWorld(t)
	fw.key = true // D2 aims at the arrangement, not at the user's own key
	res, err := h.toggle()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Collapsed || res.Folders != 3 {
		t.Fatalf("result = %+v", res)
	}
	for f, c := range ownFoldersCollapsed(fw) {
		if !c {
			t.Fatalf("%q still expanded: %+v", f, fw.groups)
		}
	}
	res, err = h.toggle()
	if err != nil || res.Collapsed || res.Folders != 3 {
		t.Fatalf("second toggle = %+v, %v", res, err)
	}
	for f, c := range ownFoldersCollapsed(fw) {
		if c {
			t.Fatalf("%q still collapsed: %+v", f, fw.groups)
		}
	}
}

// A folder whose anchor hosts a session is not an own folder: the toggle,
// through the Runner's own anchor computation, neither folds nor unfolds it.
func TestToggleLeavesFolderWhoseAnchorHostsSession(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Hedge")
	h.sessionIn("WS-C", "Garden")
	h.converge(10)
	mod, arl := fw.groupNamed("Hedge"), fw.groupNamed("Garden")
	if mod == nil || arl == nil {
		t.Fatalf("not converged: %+v", fw.groups)
	}
	h.session("s9", mod.AnchorID, "idle", true) // the anchor now hosts a session
	modID, arlID := mod.ID, arl.ID

	touched := func(mark int) map[string]bool {
		out := map[string]bool{}
		for _, c := range fw.calls[mark:] {
			if f := strings.Fields(c); len(f) > 1 && (f[0] == "group-collapse" || f[0] == "group-expand") {
				out[f[1]] = true
			}
		}
		return out
	}
	for i, wantCollapsed := range []bool{true, false} {
		mark := len(fw.calls)
		res, err := h.toggle()
		if err != nil {
			t.Fatal(err)
		}
		got := touched(mark)
		if res.Collapsed != wantCollapsed || res.Folders != 1 || got[modID] || !got[arlID] || len(got) != 1 {
			t.Fatalf("toggle %d = %+v, touched %v: only %q (%s) may move, never %q (%s)",
				i, res, got, "Garden", arlID, "Hedge", modID)
		}
	}
}

// Review Focus 6: what the toggle set is never undone by the next rounds.
func TestToggleNotUndoneByArrange(t *testing.T) {
	t.Parallel()
	h, fw := toggleWorld(t)
	for _, wantCollapsed := range []bool{true, false} {
		if _, err := h.toggle(); err != nil {
			t.Fatal(err)
		}
		mark := len(fw.calls)
		for i := 0; i < 20; i++ {
			if i%2 == 0 {
				h.wait("s-WS-B", "arbitrage")
			} else {
				h.clearWait("s-WS-B")
			}
			h.step(3 * time.Second)
		}
		for _, c := range fw.calls[mark:] {
			if v := strings.Fields(c)[0]; v == "group-collapse" || v == "group-expand" {
				t.Fatalf("the arrangement undid the toggle: %q", c)
			}
		}
		for f, c := range ownFoldersCollapsed(fw) {
			if c != wantCollapsed {
				t.Fatalf("%q collapsed = %v, want %v", f, c, wantCollapsed)
			}
		}
	}
}

// The D12 case of the Task 6 test: a folder collapsed by the toggle, emptied,
// deleted and recreated is born collapsed, also after a restart.
func TestToggleRecreatedFolderKeepsCollapse(t *testing.T) {
	t.Parallel()
	h, fw := toggleWorld(t)
	if _, err := h.toggle(); err != nil {
		t.Fatal(err)
	}
	cycle := func(what string) {
		t.Helper()
		h.moveTo("WS-A", "Hedge")
		h.moveTo("WS-B", "Hedge")
		h.step(30 * time.Second)
		h.converge(10)
		if fw.groupNamed("Terrace") != nil {
			t.Fatalf("%s: « Terrace » emptied, not deleted: %+v", what, fw.groups)
		}
		h.moveTo("WS-A", "Terrace")
		h.step(30 * time.Second)
		h.converge(10)
		sys := fw.groupNamed("Terrace")
		if sys == nil || !sys.Collapsed {
			t.Fatalf("%s: « Terrace » recreated %+v, want collapsed", what, sys)
		}
	}
	cycle("same daemon")
	h.restart()
	h.converge(2)
	cycle("after a restart")
}

// bounded is a context a missing refusal cannot outwait.
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func collapseExpandCalls(fw *folderWorld) int { return fw.count("group-collapse", "group-expand") }

func TestToggleRefusedWhenIdle(t *testing.T) {
	t.Parallel()
	refused := func(t *testing.T, h *folderHarness, fw *folderWorld, what string) {
		t.Helper()
		before := collapseExpandCalls(fw)
		if _, err := h.toggle(); !errors.Is(err, ErrFoldersIdle) {
			t.Fatalf("%s: err = %v, want ErrFoldersIdle", what, err)
		}
		if n := collapseExpandCalls(fw); n != before {
			t.Fatalf("%s: %d collapse/expand written", what, n-before)
		}
		for _, l := range h.logs {
			if strings.Contains(l, "panic") {
				t.Fatalf("%s: a panic was recovered: %q", what, l)
			}
		}
	}
	t.Run("a folders off", func(t *testing.T) {
		h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Folders = false })
		h.sessionIn("WS-A", "Terrace")
		h.converge(5)
		refused(t, h, fw, "folders off")
	})
	// On synctest (D4): the bound on the refusal reads the bubble's clock,
	// which only moves when every goroutine waits — a refusal that waited for
	// a Step would let the 2 s context expire, load never does.
	t.Run("c loop switched off at runtime", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h, fw := toggleWorld(t)
			h.r.SetEnabled(false)
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // a missing refusal would wait for a Step that never comes
			defer cancel()
			if _, err := h.r.ToggleFolders(ctx); !errors.Is(err, ErrFoldersIdle) {
				t.Fatalf("err = %v, want ErrFoldersIdle without a Step", err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("the refusal waited")
			}
			if n := collapseExpandCalls(fw); n != 0 {
				t.Fatalf("%d collapse/expand written", n)
			}
			// Off between the enqueue and the Step: refused by serveToggle's re-read.
			h2, fw2 := toggleWorld(t)
			reply, err := h2.r.enqueueToggle(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			h2.r.SetEnabled(false)
			h2.r.Step()
			if rep := <-reply; !errors.Is(rep.err, ErrFoldersIdle) || collapseExpandCalls(fw2) != 0 {
				t.Fatalf("reply = %+v, %d writes", rep, collapseExpandCalls(fw2))
			}
		})
	})
	t.Run("d stale list", func(t *testing.T) {
		h, fw := toggleWorld(t)
		h.step(31 * time.Second) // a fresh listing
		h.now = h.now.Add(61 * time.Second)
		refused(t, h, fw, "list 61 s old")
		h2, fw2 := toggleWorld(t)
		h2.step(31 * time.Second)
		h2.now = h2.now.Add(59 * time.Second)
		if res, err := h2.toggle(); err != nil || !res.Collapsed || collapseExpandCalls(fw2) == 0 {
			t.Fatalf("list 59 s old: %+v, %v", res, err)
		}
	})
	t.Run("e listing failing", func(t *testing.T) {
		h, fw := toggleWorld(t)
		fw.listDown = errors.New("cmux list-windows: injected")
		h.step(31 * time.Second)
		refused(t, h, fw, "listing down")
	})
	t.Run("f zero window", func(t *testing.T) {
		h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Windows = []string{"99999999-0000-4000-8000-000000000009"} })
		fw.add(win, "WS-A", "a")
		h.step(31 * time.Second)
		refused(t, h, fw, "no allowed window")
	})
	t.Run("g never ran", func(t *testing.T) {
		h, fw := toggleWorld(t)
		h.restart()
		refused(t, h, fw, "fresh runner")
	})
	t.Run("i folders stopped", func(t *testing.T) {
		h, fw := toggleWorld(t)
		fw.renameOnCreate = func(n string) string { return n + "!" }
		h.sessionIn("WS-D", "Trellis") // « Trellis » is created, read back under another name
		h.converge(4)
		if !h.r.foldersStopped {
			t.Fatal("folders not stopped")
		}
		refused(t, h, fw, "folders stopped")
	})
}

func TestToggleAppliesToTheOneAllowedWindow(t *testing.T) { // (h)
	t.Parallel()
	h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Windows = []string{win} })
	h.sessionIn("WS-A", "Terrace")
	fw.add(winB, "OTHER", "other")
	h.converge(10)
	res, err := h.toggle()
	if err != nil || !res.Collapsed || res.Folders != 1 {
		t.Fatalf("toggle = %+v, %v", res, err)
	}
	for _, c := range fw.calls {
		if strings.Contains(c, "@"+winB) {
			t.Fatalf("a write aimed at the other window: %q", c)
		}
	}
}

// D8, focus D12: the toggle acts on every allowed window, each by its own
// state: window A has an expanded folder (→ collapse its expanded ones),
// window B none (→ expand them all but Standby).
func TestToggleEachWindow(t *testing.T) {
	t.Parallel()
	h, fw := twoWindowWorld(t)
	h.sessionOn(winB, "WS-M", "Hedge")
	h.converge(10)
	for _, gr := range fw.groupsIn(winB) {
		if gr.AnchorGenerated && !gr.Collapsed {
			fw.groupNamedIn(winB, gr.Name).Collapsed = true // window B: every own folder folded by hand
		}
	}
	if fw.groupNamed("Hedge").Collapsed {
		t.Fatal("instrument: window A's « Hedge » should be expanded")
	}
	res, err := h.toggle()
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if !res.Collapsed {
		t.Fatalf("Collapsed = false, want the first window's (A collapses): %+v", res)
	}
	if !fw.groupNamed("Hedge").Collapsed {
		t.Fatal("window A: « Hedge » not collapsed")
	}
	for _, gr := range fw.groupsIn(winB) {
		if gr.AnchorGenerated && gr.Name != space.Standby && gr.Collapsed {
			t.Fatalf("window B: %q still collapsed, want expanded", gr.Name)
		}
	}
	if res.Folders < 3 {
		t.Fatalf("Folders = %d, want the sum of both windows (≥ 3)", res.Folders)
	}
	if len(fw.writesOn(winB)) == 0 {
		t.Fatal("instrument: no write aimed at window B")
	}
}

// l. 7878: a group of window B whose generated anchor hosts a session is not
// an own folder: the toggle never collapses it.
func TestToggleEachWindowAnchorHosts(t *testing.T) {
	t.Parallel()
	h, fw := twoWindowWorld(t)
	fw.add(winB, "HOST", "host")
	h.session("s-HOST", "HOST", "idle", true)
	wb := fw.other(winB)
	wb.groups = append(wb.groups, cmux.Group{ID: "GRP-H", Name: "Hedge", AnchorID: "HOST", AnchorGenerated: true, Members: []string{"HOST"}})
	res, err := h.toggle()
	if err != nil || !res.Collapsed {
		t.Fatalf("toggle = %+v, %v", res, err)
	}
	if gr := fw.groupNamedIn(winB, "Terrace"); gr == nil || !gr.Collapsed {
		t.Fatalf("window B's own folder not collapsed: %+v", fw.groupsIn(winB))
	}
	for _, c := range fw.calls {
		if strings.HasPrefix(c, "group-collapse GRP-H") {
			t.Fatalf("the anchor host's group was collapsed: %q", c)
		}
	}
}

// listFailsOn makes ListGroups fail for one window once armed.
type listFailsOn struct {
	cmux.Folders
	window string
	armed  *bool
}

func (l listFailsOn) ListGroups(w string) ([]cmux.Group, error) {
	if *l.armed && w == l.window {
		return nil, errors.New("cmux group-list: exit status 1: Error: injected")
	}
	return l.Folders.ListGroups(w)
}

// D12: an error on a later window stops the toggle and is returned; the
// window already toggled keeps its state (no rollback), the stop is logged
// with its counts and counted as an error.
func TestToggleStopsAtWindowErrorKeepsDone(t *testing.T) {
	t.Parallel()
	armed := false
	h, fw := twoWindowWorld(t, func(c *Config, d *Deps) {
		d.Folders = listFailsOn{Folders: d.Folders, window: winB, armed: &armed}
	})
	if fw.groupNamed("Hedge").Collapsed {
		t.Fatal("instrument: window A's « Hedge » should be expanded")
	}
	armed = true
	errs, bWrites := h.r.State().ErrorsLastHour, len(fw.writesOn(winB))
	res, err := h.toggle()
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("toggle = %+v, %v; want the window B error", res, err)
	}
	if !fw.groupNamed("Hedge").Collapsed {
		t.Fatal("window A was toggled before the error: it must stay collapsed (no rollback)")
	}
	if n := len(fw.writesOn(winB)) - bWrites; n != 0 {
		t.Fatalf("window B received %d writes, want none (the toggle stopped)", n)
	}
	if got := h.r.State().ErrorsLastHour; got != errs+1 {
		t.Fatalf("ErrorsLastHour = %d, want %d (countError)", got, errs+1)
	}
	logged := false
	for _, l := range h.logs {
		if strings.Contains(l, "toggle stopped after") && strings.Contains(l, "folders") && strings.Contains(l, "injected") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("no « toggle stopped after n of m folders » log: %q", h.logs)
	}
}

// overlapFolders fails the test when two group writes overlap in time.
type overlapFolders struct {
	cmux.Folders
	inflight, overlaps atomic.Int32
}

func (o *overlapFolders) enter() func() {
	if o.inflight.Add(1) > 1 {
		o.overlaps.Add(1)
	}
	time.Sleep(20 * time.Microsecond)
	return func() { o.inflight.Add(-1) }
}

func (o *overlapFolders) CreateGroup(w, n string) (cmux.Group, error) {
	defer o.enter()()
	return o.Folders.CreateGroup(w, n)
}
func (o *overlapFolders) AddToGroup(w, g, ws string) error {
	defer o.enter()()
	return o.Folders.AddToGroup(w, g, ws)
}
func (o *overlapFolders) RemoveFromGroup(w, ws string) error {
	defer o.enter()()
	return o.Folders.RemoveFromGroup(w, ws)
}
func (o *overlapFolders) Ungroup(w, g string, a bool) error {
	defer o.enter()()
	return o.Folders.Ungroup(w, g, a)
}
func (o *overlapFolders) Collapse(w, g string) error {
	defer o.enter()()
	return o.Folders.Collapse(w, g)
}
func (o *overlapFolders) Expand(w, g string) error {
	defer o.enter()()
	return o.Folders.Expand(w, g)
}
func (o *overlapFolders) SetIcon(w, g, s string) error {
	defer o.enter()()
	return o.Folders.SetIcon(w, g, s)
}
func (o *overlapFolders) RenameGroup(w, g, n string) error {
	defer o.enter()()
	return o.Folders.RenameGroup(w, g, n)
}
func (o *overlapFolders) Reorder(w string, order []string) error {
	defer o.enter()()
	return o.Folders.Reorder(w, order)
}
func (o *overlapFolders) PlaceWorkspace(w, ws, ref string, before bool) error {
	defer o.enter()()
	return o.Folders.PlaceWorkspace(w, ws, ref, before)
}
func (o *overlapFolders) MoveGroup(w, g, ref string, b bool) error {
	defer o.enter()()
	return o.Folders.MoveGroup(w, g, ref, b)
}

// serialWorld is toggleWorld with a clock that runs a thousand times faster
// than the wall once live (ArrangeSettle elapses in milliseconds; a listing
// stays fresh for two hours of it, SlowInterval being one hour) and the
// overlap detector between the runner and the simulated cmux.
func serialWorld(t *testing.T) (*folderHarness, *folderWorld, *overlapFolders, func()) {
	t.Helper()
	var (
		ov        *overlapFolders
		live      atomic.Bool
		liveStart atomic.Int64
	)
	h, fw := toggleWorld(t, func(c *Config, d *Deps) {
		c.FastInterval = 2 * time.Millisecond
		c.SlowInterval = time.Hour
		ov = &overlapFolders{Folders: d.Folders}
		d.Folders = ov
		harnessNow := d.Now
		d.Now = func() time.Time {
			if live.Load() {
				return harnessNow().Add(time.Duration(time.Now().UnixNano()-liveStart.Load()) * 1000)
			}
			return harnessNow()
		}
	})
	return h, fw, ov, func() { liveStart.Store(time.Now().UnixNano()); live.Store(true) }
}

// churn changes the sessions' waits in a loop (the arrangement writes) and
// reads State, until stop is closed.
func churn(h *folderHarness, stop <-chan struct{}, wg *sync.WaitGroup) {
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			sid := []string{"s-WS-A", "s-WS-B", "s-WS-C"}[i%3]
			if i%2 == 0 {
				_ = h.st.SetSessionWait(sid, pigtail.SessionWait{Type: "arbitrage", Source: pigtail.WaitSourceDeclared, Since: h.now})
			} else {
				_, _ = h.st.ClearSessionWaits(sid, pigtail.WaitSourceDeclared)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = h.r.State()
		}
	}()
}

func twentyToggles(t *testing.T, h *folderHarness) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := h.r.ToggleFolders(ctx)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a toggle was not served: %v", err)
		}
	}
}

// Run it under -race: pigtail admit -- go test -race ./internal/daemon/cmuxsync/ -run TestToggleSerialisedWithArrange
//
// Serial on purpose (D3): a and b need real concurrency (churn's State
// reader spins and never blocks, so synctest's clock would never move) and
// serialWorld's clock is the wall's, a thousand times faster; run alone, no
// test of the package competes with it for the CPU.
func TestToggleSerialisedWithArrange(t *testing.T) {
	t.Run("a real Run loop", func(t *testing.T) {
		h, _, ov, goLive := serialWorld(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		goLive()
		go func() { h.r.Run(ctx); close(done) }()
		stop := make(chan struct{})
		var wg sync.WaitGroup
		churn(h, stop, &wg)
		twentyToggles(t, h)
		close(stop)
		wg.Wait()
		cancel()
		<-done
		if n := ov.overlaps.Load(); n != 0 {
			t.Fatalf("%d overlapping group writes", n)
		}
	})
	t.Run("b Step in a loop", func(t *testing.T) {
		h, _, ov, goLive := serialWorld(t)
		goLive()
		stop, stepped := make(chan struct{}), make(chan struct{})
		var wg sync.WaitGroup
		churn(h, stop, &wg)
		go func() {
			defer close(stepped)
			for {
				select {
				case <-stop:
					return
				default:
				}
				h.r.Step()
				time.Sleep(time.Millisecond)
			}
		}()
		twentyToggles(t, h)
		close(stop)
		wg.Wait()
		<-stepped
		if n := ov.overlaps.Load(); n != 0 {
			t.Fatalf("%d overlapping group writes", n)
		}
	})
	t.Run("c loop off, and Run gone", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) { // the 100 ms bounds on the bubble's clock (D4)
			h, _ := toggleWorld(t)
			h.r.SetEnabled(false)
			start := time.Now()
			if _, err := h.r.ToggleFolders(bounded(t)); !errors.Is(err, ErrFoldersIdle) || time.Since(start) > 100*time.Millisecond {
				t.Fatalf("loop off: %v after %s", err, time.Since(start))
			}
			h.r.SetEnabled(true)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { h.r.Run(ctx); close(done) }()
			cancel()
			<-done
			start = time.Now()
			if _, err := h.r.ToggleFolders(bounded(t)); !errors.Is(err, ErrFoldersIdle) || time.Since(start) > 100*time.Millisecond {
				t.Fatalf("Run gone: %v after %s", err, time.Since(start))
			}
		})
	})
}

func TestArrangeNeverEmitsToggleKinds(t *testing.T) {
	t.Parallel()
	for i, in := range []ArrangeInput{
		{Folders: folders, Order: []string{"A", "S"}, Target: map[string]string{"A": "Terrace", "S": "Standby"},
			Collapse: map[string]bool{"Standby": true, "Terrace": true}},
		{Folders: folders, Order: []string{"GM", "A", "GP", "B"}, Target: map[string]string{"A": "Terrace", "B": "Hedge"},
			Groups:   []cmux.Group{collapsed(g("M", "Hedge", "GM", true, "A"), true), collapsed(g("P", "Terrace", "GP", true, "B"), false)},
			Collapse: map[string]bool{"Hedge": false, "Terrace": true}},
		{Folders: folders, Order: []string{"GX", "A"}, Target: map[string]string{"A": "Hedge"},
			Groups: []cmux.Group{g("X", "Mine", "GX", true, "A")}},
	} {
		for _, op := range Arrange(in) {
			if op.Kind == FolderCollapse || op.Kind == FolderExpand {
				t.Fatalf("input %d: the arrangement emitted %q", i, op.Kind)
			}
		}
	}
}

// D6: a folder created while others exist is placed at its fixed position in
// its creation round. Real cmux creates a group at the top of the window
// (cmux probe P11); if the focus comes back before the reorder round, the new
// folder must not sit above the folders that precede it.
func TestArrangeCreatedFolderNeverAboveEarlierFolders(t *testing.T) {
	t.Parallel()
	anchorOf := func(fw *folderWorld, name string) string {
		if gr := fw.groupNamed(name); gr != nil {
			return gr.AnchorID
		}
		return ""
	}
	for _, tc := range []struct {
		name     string
		atTop    bool     // cmux creates at the top (real) or at the end (old fake)
		existing []string // spaces with a session before the new one
		created  string
		want     []string // folder order in the window once the focus is back
	}{
		{"after its predecessor", true, []string{"Hedge", "Trellis"}, "Lawn", []string{"Hedge", "Trellis", "Lawn", space.NoSubject}},
		{"first in D6 order", false, []string{"Trellis"}, "Hedge", []string{"Hedge", "Trellis", space.NoSubject}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fw := newFolderHarness(t)
			for i, sp := range tc.existing {
				h.sessionIn("WS-"+strconv.Itoa(i), sp)
			}
			fw.add(win, "Shed", "Shed") // « Inbox »
			h.converge(10)
			h.assertQuiet("before the new folder")
			fw.createAtTop = tc.atTop
			h.sessionIn("WS-NEW", tc.created)
			for i := 0; i < 10 && fw.groupNamed(tc.created) == nil; i++ {
				h.step(3 * time.Second)
			}
			if fw.groupNamed(tc.created) == nil {
				t.Fatalf("%s never created", tc.created)
			}
			fw.key = true // the user refocuses cmux right after the creation round
			h.converge(5)
			var got []string
			for _, ws := range fw.windows[win] {
				for _, gr := range fw.groups {
					if gr.AnchorID == ws {
						got = append(got, gr.Name)
					}
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("folder order after the creation round: %q, want %q (window %q)", got, tc.want, fw.windows[win])
			}
			if tc.atTop && h.index(anchorOf(fw, tc.created)) == 0 {
				t.Fatalf("%s left at the top of the window", tc.created)
			}
		})
	}
}

// folderNames is the window's own and foreign folders, in window order.
func folderNames(fw *folderWorld) []string {
	var got []string
	for _, ws := range fw.windows[win] {
		for _, gr := range fw.groups {
			if gr.AnchorID == ws {
				got = append(got, gr.Name)
			}
		}
	}
	return got
}

// D6: folders created in the same round (first start, restart) chain on one
// another (RefFolder): each lands after the one created before it, so the
// round ends in D6 order even though cmux creates each at the top.
func TestArrangeFoldersCreatedTogetherInD6Order(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.createAtTop = true
	h.sessionIn("WS-0", "Lawn")
	h.sessionIn("WS-1", "Hedge")
	h.sessionIn("WS-2", "Trellis")
	fw.add(win, "Shed", "Shed") // « Inbox »
	want := []string{"Hedge", "Trellis", "Lawn", space.NoSubject}
	for i := 0; i < 10 && len(folderNames(fw)) < len(want); i++ {
		h.step(3 * time.Second)
	}
	fw.key = true // the user refocuses cmux right after the creation round
	h.converge(5)
	if got := folderNames(fw); !slices.Equal(got, want) {
		t.Fatalf("folder order after the creation round: %q, want %q (window %q)", got, want, fw.windows[win])
	}
}

// D6, D13: the only own folder, created at the top by cmux, goes right after
// the last pinned group in its creation round.
func TestArrangeOnlyFolderCreatedAfterLastPinnedGroup(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.manualGroup("PG", "Mine", "T-PG", false)
	fw.group("PG").Pinned = true
	fw.createAtTop = true
	h.sessionIn("WS-A", "Hedge")
	for i := 0; i < 10 && fw.groupNamed("Hedge") == nil; i++ {
		h.step(3 * time.Second)
	}
	fw.key = true
	h.converge(5)
	if got := folderNames(fw); !slices.Equal(got, []string{"Mine", "Hedge"}) {
		t.Fatalf("folder order after the creation round: %q, want Mine then Hedge (window %q)", got, fw.windows[win])
	}
}

// A refused placement move never costs the birth icon and collapse: nothing
// re-collapses an existing folder (only the D12 toggle does), so a folder
// reborn with its « collapsed » memory would stay open. The error stays logged.
func TestArrangeCreatedFolderMoveRefusedKeepsIconAndCollapse(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Hedge")
	h.converge(10)
	fw.groupNamed("Terrace").Collapsed = true // Ada folds it
	h.step(30 * time.Second)                  // a pass reads it
	h.converge(2)
	h.moveTo("WS-A", "Hedge")
	h.step(30 * time.Second)
	h.converge(10)
	if fw.groupNamed("Terrace") != nil {
		t.Fatalf("« Terrace » emptied, not deleted: %+v", fw.groups)
	}
	fw.failOn = "group-move" // cmux refuses the placement
	h.moveTo("WS-A", "Terrace")
	h.step(30 * time.Second)
	h.converge(10)
	sys := fw.groupNamed("Terrace")
	if sys == nil || !slices.ContainsFunc(fw.calls, func(c string) bool { return strings.HasPrefix(c, "group-move "+sys.ID+" ") }) {
		t.Fatalf("« Terrace » not recreated with a placement move: %+v, calls %q", sys, fw.calls)
	}
	if !sys.Collapsed || sys.Icon != testFolderIcon["Terrace"] {
		t.Fatalf("refused move lost the birth state: %+v", sys)
	}
	n := 0
	for _, l := range h.logs {
		if strings.Contains(l, "group-move") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the refused move logged %d times, want once: %q", n, h.logs)
	}
}

// --- grace at birth (D31, D32, D36, D40; sub_x000000000000041) ---------------

// graceWorld: « Terrace » filed with WS-A bound, « Inbox » present
// through the old bare terminal Shed; converged and quiet.
func graceWorld(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	h, fw := newFolderHarness(t, opts...)
	h.sessionIn("WS-A", "Terrace")
	fw.add(win, "Shed", "Shed")
	h.converge(10)
	h.assertQuiet("grace world")
	return h, fw
}

func TestYoungUnboundStaysInBirthFolder(t *testing.T) {
	t.Parallel()
	h, fw := graceWorld(t)
	fw.bornIn("WS-N", "Terrace")
	h.step(31 * time.Second)
	h.converge(5)
	if fw.folderOf("WS-N") != "Terrace" || fw.adds("WS-N") != 0 {
		t.Fatalf("WS-N left its birth folder: in %q, adds %d", fw.folderOf("WS-N"), fw.adds("WS-N"))
	}
}

func TestYoungListedBetweenSlowListingsStaysPut(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*folderHarness, *folderWorld) {
		h, fw := graceWorld(t)
		fw.bornIn("WS-N", "Terrace")
		h.wait("s-WS-A", "arbitrage") // a rank change owes a round
		return h, fw
	}
	t.Run("hors focus", func(t *testing.T) {
		h, fw := setup(t)
		h.step(3 * time.Second) // no slow listing: only arrangeInput sees WS-N
		if fw.adds("WS-N") != 0 || fw.folderOf("WS-N") != "Terrace" {
			t.Fatalf("WS-N filed at once: in %q", fw.folderOf("WS-N"))
		}
	})
	t.Run("dueWhileKey", func(t *testing.T) {
		h, fw := graceWorld(t)
		fw.key = true
		fw.bornIn("WS-N", "Terrace")
		h.wait("s-WS-A", "arbitrage")
		before := len(fw.folderWrites())
		h.step(3 * time.Second)
		if len(fw.folderWrites()) != before {
			t.Fatal("cmux has the focus, nothing may be written")
		}
		fw.key = false
		h.step(3 * time.Second)
		if fw.adds("WS-N") != 0 || fw.folderOf("WS-N") != "Terrace" {
			t.Fatalf("WS-N filed once the focus left: in %q", fw.folderOf("WS-N"))
		}
	})
}

func TestYoungFiledOnOffTopic(t *testing.T) {
	t.Parallel()
	h, fw := graceWorld(t)
	fw.bornIn("WS-N", "Terrace")
	h.step(31 * time.Second)
	h.converge(5)
	if fw.folderOf("WS-N") != "Terrace" || fw.adds("WS-N") != 0 {
		t.Fatalf("grace not held: in %q", fw.folderOf("WS-N"))
	}
	h.offTopic("WS-N")
	h.converge(5)
	if got := fw.folderOf("WS-N"); got != space.NoSubject {
		t.Fatalf("D36: WS-N in %q, want « Inbox »", got)
	}
}

func TestYoungFiledOnBind(t *testing.T) {
	t.Parallel()
	h, fw := graceWorld(t)
	fw.bornIn("WS-N", "Terrace")
	h.step(31 * time.Second)
	h.converge(5)
	if fw.folderOf("WS-N") != "Terrace" || fw.adds("WS-N") != 0 {
		t.Fatalf("grace not held: in %q", fw.folderOf("WS-N"))
	}
	h.bindIn("WS-N", "Hedge")
	h.converge(6)
	if got := fw.folderOf("WS-N"); got != "Hedge" {
		t.Fatalf("WS-N in %q, want Hedge", got)
	}
}

func TestGraceExpiryOwesARound(t *testing.T) {
	t.Parallel()
	h, fw := graceWorld(t)
	fw.bornIn("WS-N", "Terrace")
	h.step(31 * time.Second)
	t0 := h.now
	h.converge(3)
	grace := h.r.cfg.BirthGrace
	h.step(t0.Add(grace - 2*time.Second).Sub(h.now)) // a slow listing: WS-N still young
	before := len(fw.folderWrites())
	if fw.adds("WS-N") != 0 {
		t.Fatal("WS-N filed during its grace")
	}
	h.step(3 * time.Second) // no slow listing: the class change alone owes the round
	if fw.adds("WS-N") != 1 || len(fw.folderWrites()) <= before {
		t.Fatalf("the round owed by the end of the grace did not run: %q", fw.folderWrites()[before:])
	}
}

func TestBareTerminalFiledAfterGrace(t *testing.T) {
	t.Parallel()
	h, fw := graceWorld(t)
	fw.add(win, "TERM", "TERM")
	h.step(31 * time.Second)
	h.converge(5)
	if fw.folderOf("TERM") != "" || fw.adds("TERM") != 0 {
		t.Fatalf("bare terminal filed during its grace: in %q", fw.folderOf("TERM"))
	}
	h.pastGrace()
	h.converge(5)
	if got := fw.folderOf("TERM"); got != space.NoSubject || h.index("TERM") < 0 {
		t.Fatalf("after the grace TERM in %q (index %d), want « Inbox », never closed", got, h.index("TERM"))
	}
}

func TestDissolvedAnchorHasNoGrace(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	h.converge(10)
	h.assertQuiet("setup")
	fw.manualGroup("MINE", "Mine", "GEN-M", true, "WS-B")
	fw.busyShell["S-GEN-M"] = true // not closable: it must be filed
	h.step(31 * time.Second)
	h.converge(8)
	if fw.groupNamed("Mine") != nil {
		t.Fatalf("foreign group not dissolved: %+v", fw.groups)
	}
	for _, c := range fw.calls {
		if strings.HasPrefix(c, "ungroup MINE") && strings.Contains(c, "--remove-generated-anchor") {
			t.Fatalf("a busy anchor must stay open: %q", c)
		}
	}
	if h.index("GEN-M") < 0 || fw.folderOf("GEN-M") != space.NoSubject {
		t.Fatalf("GEN-M in %q (index %d): a generated anchor has no grace", fw.folderOf("GEN-M"), h.index("GEN-M"))
	}
}

func TestRestartMidGraceMovesNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cmux-births.json")
	h, fw := graceWorld(t, func(_ *Config, d *Deps) {
		d.LoadBirths = func() (map[string]time.Time, bool, error) { return LoadBirthsFile(path) }
		d.SaveBirths = func(m map[string]time.Time) error { return SaveBirthsFile(path, m) }
	})
	fw.bornIn("WS-N", "Terrace")
	h.step(31 * time.Second)
	h.converge(3)
	before := len(fw.folderWrites())
	h.restart()
	h.step(31 * time.Second)
	h.converge(5)
	if w := fw.folderWrites(); len(w) != before {
		t.Fatalf("restart moved something: %q", w[before:])
	}
	h.pastGrace()
	h.converge(5)
	if got := fw.folderOf("WS-N"); got != space.NoSubject {
		t.Fatalf("after the grace WS-N in %q", got)
	}
}

func TestYoungInManualGroupStaysLoose(t *testing.T) {
	t.Parallel()
	h, fw := graceWorld(t)
	fw.add(win, "WS-N", "N")
	fw.manualGroup("MAN", "Mine", "MA", false, "WS-N")
	h.step(31 * time.Second)
	h.converge(6)
	if fw.groupNamed("Mine") != nil {
		t.Fatalf("manual group not dissolved (D9): %+v", fw.groups)
	}
	if fw.folderOf("WS-N") != "" || fw.adds("WS-N") != 0 {
		t.Fatalf("WS-N filed during its grace: in %q", fw.folderOf("WS-N"))
	}
}

func youngHeldWorld(t *testing.T) (*folderHarness, *folderWorld) {
	t.Helper()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	fw.add(win, "Shed", "Shed")
	h.converge(10)
	h.assertQuiet("held world")
	return h, fw
}

func TestYoungLooseHeldWhereBorn(t *testing.T) {
	t.Parallel()
	h, fw := youngHeldWorld(t)
	fw.bornAfter("WS-N", "WS-B")
	i0 := h.index("WS-N")
	if i0 < 0 || fw.folderOf("WS-N") != "" {
		t.Fatalf("setup: index %d, in %q", i0, fw.folderOf("WS-N"))
	}
	w0 := len(fw.folderWrites())
	h.step(31 * time.Second)
	h.converge(5)
	if w := fw.folderWrites(); len(w) != w0 || h.index("WS-N") != i0 || fw.folderOf("WS-N") != "" {
		t.Fatalf("WS-N moved at birth: index %d (was %d), writes %q", h.index("WS-N"), i0, w[w0:])
	}
	h.wait("s-WS-B", "permission") // WS-B rises to the head of « Terrace »
	h.converge(5)
	reorders := 0
	for _, c := range fw.folderWrites()[w0:] {
		if strings.HasPrefix(c, "reorder") {
			reorders++
		}
	}
	if reorders != 1 {
		t.Fatalf("want one reorder that sorts « Terrace », got %d", reorders)
	}
	if h.index("WS-B") > h.index("WS-A") || h.index("WS-N") != i0 || fw.folderOf("WS-N") != "" {
		t.Fatalf("attention sort: order %v, WS-N want index %d", fw.windows[win], i0)
	}
	if fw.adds("WS-N") != 0 {
		t.Fatalf("WS-N written to before it is qualified: %q", fw.folderWrites()[w0:])
	}
	h.bindIn("WS-N", "Hedge")
	h.converge(6)
	if got := fw.folderOf("WS-N"); got != "Hedge" {
		t.Fatalf("on bind WS-N in %q, want Hedge", got)
	}
	// D40: one move, at the qualification, into the subject's folder.
	var adds []string
	for _, c := range fw.folderWrites()[w0:] {
		if strings.HasPrefix(c, "group-add ") && strings.HasSuffix(c, " WS-N") {
			adds = append(adds, c)
		}
	}
	if gr := fw.groupNamed("Hedge"); gr == nil || len(adds) != 1 || adds[0] != "group-add "+gr.ID+" WS-N" {
		t.Fatalf("want exactly one group-add of WS-N into Hedge, got %q", adds)
	}
	t.Run("graceEnds", func(t *testing.T) {
		h, fw := youngHeldWorld(t)
		fw.bornAfter("WS-N", "WS-B")
		h.step(31 * time.Second)
		h.converge(3)
		h.pastGrace()
		h.converge(5)
		if got := fw.folderOf("WS-N"); got != space.NoSubject {
			t.Fatalf("WS-N in %q after the grace", got)
		}
	})
}

func TestFilingReadErrorSkipsRound(t *testing.T) {
	t.Parallel()
	h, fw := graceWorld(t)
	before := len(fw.folderWrites())
	h.reads.offTopic = errors.New("SQLITE_BUSY")
	fw.bornIn("WS-N", "Terrace")
	h.offTopic("WS-N") // declared while the table cannot be read
	h.step(31 * time.Second)
	h.pastGrace() // old enough to be filed at once, were the round not skipped
	h.converge(3)
	if w := fw.folderWrites(); len(w) != before || fw.adds("WS-N") != 0 {
		t.Fatalf("a read failed, yet writes: %q", w[before:])
	}
	n := 0
	for _, l := range h.logs {
		if strings.Contains(l, "round skipped, store read: list off-topic") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one log line, got %d in %q", n, h.logs)
	}
	h.reads.offTopic = nil
	h.converge(5)
	if got := fw.folderOf("WS-N"); got != space.NoSubject {
		t.Fatalf("after the error WS-N (off-topic) is in %q, want « Inbox »", got)
	}
}

// Inverted from TestAttentionSortKeepsYoungInFolder by D15, D20 of
// sub_x000000000000014: in a banded folder, sorted, a tab with no subject
// keeps its slot — the young WS-N stays in « Terrace », below WS-A, even
// asking for attention.
func TestAttentionSortUnboundKeepsSlotInFolder(t *testing.T) {
	t.Parallel()
	h, fw := graceWorld(t)
	fw.bornIn("WS-N", "Terrace")
	h.session("s-WS-N", "WS-N", "idle", true)
	h.step(31 * time.Second)
	h.converge(4)
	h.wait("s-WS-N", "permission")
	h.converge(5)
	if fw.folderOf("WS-N") != "Terrace" || h.index("WS-N") < h.index("WS-A") {
		t.Fatalf("WS-N (no subject) should keep its slot under WS-A in « Terrace »: order %v", fw.windows[win])
	}
}

// A dissolved folder's anchor the user then closes: noteBirths forgets it after
// two slow listings, and markOld must not give it back (a save every other
// listing, for ever).
func TestClosedDissolvedAnchorIsNotRewrittenForever(t *testing.T) {
	t.Parallel()
	saves := 0
	h, fw := graceWorld(t, func(c *Config, d *Deps) {
		path := filepath.Join(t.TempDir(), "b.json")
		d.LoadBirths = func() (map[string]time.Time, bool, error) { return LoadBirthsFile(path) }
		d.SaveBirths = func(m map[string]time.Time) error { saves++; return SaveBirthsFile(path, m) }
	})
	fw.add(win, "ANCH", "anchor")
	h.step(31 * time.Second)
	h.winSt().createdMaps()
	h.winSt().createdAnchor["Gone"] = "ANCH" // its folder was dissolved, the anchor kept
	h.step(31 * time.Second)
	h.converge(2)
	fw.windows[win] = slices.DeleteFunc(fw.windows[win], func(x string) bool { return x == "ANCH" }) // the user closes it
	before := saves
	for i := 0; i < 8; i++ {
		h.step(31 * time.Second)
		h.converge(2)
	}
	if got := saves - before; got > 1 {
		t.Fatalf("a closed anchor rewrote the birth file %d times over 8 slow listings, want at most 1 (the forgetting)", got)
	}
	if _, ok := h.r.births["ANCH"]; ok {
		t.Fatal("the closed anchor is still in the birth clock")
	}
	if _, ok := h.winSt().createdAnchor["Gone"]; ok {
		t.Fatal("the closed anchor is still remembered")
	}
}

// Switching the sidebar off stops the clock: what opens meanwhile is read old
// when it is switched on again (the first listing after the activation).
func TestBirthsRearmedWhenSidebarSwitchedBackOn(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	h, fw := graceWorld(t, func(_ *Config, d *Deps) {
		d.LoadBirths = func() (map[string]time.Time, bool, error) { return LoadBirthsFile(path) }
		d.SaveBirths = func(m map[string]time.Time) error { return SaveBirthsFile(path, m) }
	})
	h.r.SetEnabled(false)
	h.step(31 * time.Second)
	fw.bornIn("WS-X", "Terrace") // opened while the loop was off
	h.step(31 * time.Second)
	h.r.SetEnabled(true)
	h.step(31 * time.Second)
	h.converge(3)
	if h.r.young("WS-X", h.now) {
		t.Fatal("WS-X opened while the sidebar was off reads young")
	}
	fw.bornIn("WS-Y", "Terrace")
	h.step(31 * time.Second)
	if !h.r.young("WS-Y", h.now) {
		t.Fatal("the re-arming is consumed once: WS-Y opened after is young")
	}
}

// D9 (sub_x000000000000007): the folder pigtail made under its former name
// « Sans sujet » is foreign once the name is « Inbox »: its members go to
// « Inbox » (R3), then it is dissolved (R4), its generated anchor closed when
// proved bare (D18 (A)), else kept and filed in « Inbox » (D7). No ghost.
func TestArrangeFormerNoSubjectFolderConverges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		needsRemove bool
		busyAnchor  bool
	}{
		{"add moves, bare anchor", false, false},
		{"add moves, busy anchor", false, true},
		{"add needs remove, bare anchor", true, false},
		{"add needs remove, busy anchor", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.AddNeedsRemove = tc.needsRemove })
			fw.addNeedsRemove = tc.needsRemove
			fw.add(win, "Shed", "Shed")
			fw.manualGroup("OLD", "Sans sujet", "GEN-O", true, "Shed")
			if tc.busyAnchor {
				fw.busyShell["S-GEN-O"] = true
			}
			h.converge(12)
			if got := fw.folderOf("Shed"); got != "Inbox" {
				t.Fatalf("Shed in %q, want Inbox", got)
			}
			if gr := fw.groupNamed("Sans sujet"); gr != nil {
				t.Fatalf("ghost folder left: %+v", *gr)
			}
			if tc.busyAnchor {
				if h.index("GEN-O") < 0 || fw.folderOf("GEN-O") != "Inbox" {
					t.Fatalf("D7: a busy anchor stays open, filed in Inbox; open=%v in %q", h.index("GEN-O") >= 0, fw.folderOf("GEN-O"))
				}
			} else if h.index("GEN-O") >= 0 {
				t.Fatal("D18 (A): the bare generated anchor of the former folder is closed")
			}
			h.assertQuiet("former « Sans sujet » folder")
		})
	}
}

// D1 in sorted mode. The rank is gravity only (rankOf → waittype.Rank) and
// desiredOrder is a stable sort: (a) at equal gravity a spawn born under its
// caller STAYS there, with no write; (b) with a different gravity the sort
// places it by gravity exactly as it places a session born at the bottom, with
// no extra write (R5: the in-folder order is the sort's).
func TestSortedSpawnBornUnderCallerStaysAtEqualGravity(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	h.converge(10)
	h.assertQuiet("sorted world")
	n := len(fw.folderWrites())
	fw.bornNextTo("WS-D", "WS-A")
	h.session("s-WS-D", "WS-D", "idle", true)
	h.bindIn("WS-D", "Terrace")
	h.converge(10)
	h.assertQuiet("spawned, sorted, equal gravity")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-D", "WS-B"}) {
		t.Fatalf("Terrace = %v, want WS-D to stay under its caller [WS-A WS-D WS-B]", got)
	}
	if w := fw.folderWrites()[n:]; len(w) != 0 {
		t.Fatalf("equal gravity: want 0 writes, got %q", w)
	}
}

func TestSortedSpawnBornUnderCallerDifferentGravityAsBottom(t *testing.T) {
	t.Parallel()
	run := func(born func(fw *folderWorld)) ([]string, []string) {
		h, fw := newFolderHarness(t)
		h.sessionIn("WS-A", "Terrace")
		h.sessionIn("WS-B", "Terrace")
		h.converge(10)
		h.assertQuiet("sorted world")
		n := len(fw.folderWrites())
		born(fw)
		h.session("s-WS-D", "WS-D", "idle", true)
		h.bindIn("WS-D", "Terrace")
		h.wait("s-WS-D", "permission") // a different gravity: the sort acts on it
		h.converge(10)
		h.assertQuiet("spawned, sorted, different gravity")
		return fw.members("Terrace"), fw.folderWrites()[n:]
	}
	underMembers, underWrites := run(func(fw *folderWorld) { fw.bornNextTo("WS-D", "WS-A") })
	bottomMembers, bottomWrites := run(func(fw *folderWorld) { fw.bornIn("WS-D", "Terrace") })
	if !slices.Equal(underMembers, bottomMembers) {
		t.Fatalf("sorted, different gravity: born under the caller → %v, born at the bottom → %v: the sort must place both the same", underMembers, bottomMembers)
	}
	if len(underWrites) > len(bottomWrites) {
		t.Fatalf("born under the caller costs extra writes: %q vs %q", underWrites, bottomWrites)
	}
}

// Lot 6: on the real runner (no sort, cmux creating on top), the « Other »
// folder pigtail made converges to one « Inbox »: the same group renamed, at
// the same place, with its members, one rename write, no create, no dissolve.
func TestRunnerRenamesFormerNoSubjectFolder(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	gr := fw.groupNamed(space.NoSubject)
	if gr == nil {
		t.Fatal("world: no « Inbox » folder")
	}
	id, members := gr.ID, slices.Clone(gr.Members)
	fw.group(id).Name = space.FormerNoSubject                                                                                               // the folder as the installed daemon left it (group(id) is the live pointer, as SetIcon uses it)
	h.restart()                                                                                                                             // a freshly installed daemon: empty bookkeeping (createdIDs["Inbox"] no longer points at this group), as after the real install
	before := slices.Clone(fw.windows[win])                                                                                                 // display order (groupsIn is insertion order: comparing it proves nothing)
	creates0, renames0, moves0 := fw.count("group-create", "ungroup"), fw.count("group-rename"), fw.count("reorder", "place", "group-move") // the world's own build already wrote groups (noSortWorld creates 3)
	h.converge(10)
	after := fw.groupNamed(space.NoSubject)
	if after == nil || after.ID != id || !slices.Equal(after.Members, members) {
		t.Fatalf("want group %s renamed with %q, got %+v", id, members, after)
	}
	if fw.groupNamed(space.FormerNoSubject) != nil {
		t.Fatal("« Other » still listed")
	}
	if !slices.Equal(before, fw.windows[win]) {
		t.Fatalf("display order %q, want %q (same place)", fw.windows[win], before)
	}
	if n := fw.count("reorder", "place", "group-move") - moves0; n != 0 {
		t.Fatalf("reorder/group-move writes since the restart = %d, want 0 (%q)", n, fw.folderWrites())
	}
	if n := fw.count("group-rename") - renames0; n != 1 {
		t.Fatalf("group-rename writes = %d, want 1", n)
	}
	if n := fw.count("group-create", "ungroup") - creates0; n != 0 {
		t.Fatalf("create/ungroup writes since the restart = %d, want 0 (%q)", n, fw.folderWrites())
	}
	h.assertQuiet("renamed « Inbox »")
}
