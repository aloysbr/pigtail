package cmuxsync

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
)

// sub_x000000000000030: the spec's criteria at the runner, against the
// simulated cmux (runner_folders_harness_test.go), never the real sidebar.

func noSort(c *Config, _ *Deps) { c.FolderNoSort = true }

// noSortWorld: Terrace {A, B}, Hedge {C}, « Inbox » {Shed}, filed with
// the sort off, cmux creating groups on top as the real one (P11).
func noSortWorld(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	h, fw := newFolderHarness(t, append([]func(*Config, *Deps){noSort}, opts...)...)
	fw.createAtTop = true
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	h.sessionIn("WS-C", "Hedge")
	fw.add(win, "Shed", "Shed")
	h.offTopic("Shed")
	h.converge(10)
	h.assertQuiet("no-sort world")
	for ws, f := range map[string]string{"WS-A": "Terrace", "WS-B": "Terrace", "WS-C": "Hedge", "Shed": space.NoSubject} {
		if got := fw.folderOf(ws); got != f {
			t.Fatalf("world: %s in %q, want %q (%q)", ws, got, f, fw.windows[win])
		}
	}
	return h, fw
}

// noReorder: the sort off sends no reorder but the one that puts pigtail's own
// adds back at the bottom of their folder (D11, P19: it moves workspaces pigtail
// added, nothing else), and a group move only right after a create (its birth
// place, D2).
func noReorder(t *testing.T, fw *folderWorld) {
	t.Helper()
	i := 0
	for _, c := range fw.calls {
		if strings.Fields(c)[0] != "reorder" {
			continue
		}
		if i >= len(fw.reorderFrom) {
			t.Fatalf("reorder %d refused by the simulator: %q", i, fw.folderWrites())
		}
		added := func(o []string) []string {
			return slices.DeleteFunc(slices.Clone(o), func(x string) bool { return fw.adds(x) > 0 })
		}
		after := strings.Split(strings.Fields(c)[1], ",")
		if before := fw.reorderFrom[i]; !slices.Equal(added(before), added(after)) {
			t.Fatalf("a reorder with the sort off moves more than pigtail's adds: %q → %q", before, after)
		}
		i++
	}
	w := fw.folderWrites()
	for i, c := range w {
		if strings.HasPrefix(c, "group-move") && (i == 0 || !strings.HasPrefix(w[i-1], "group-create")) {
			t.Fatalf("a group move outside a creation: %q", w)
		}
	}
}

// moveBefore puts ws (with its whole block when it is an anchor) right before
// ref, as the user's drag would; members stay in their groups.
func moveBefore(fw *folderWorld, ws, ref string) {
	moved := []string{ws}
	if gr := fw.groupOf(ws); gr != nil && gr.AnchorID == ws {
		moved = blockOf(ArrangeInput{Order: fw.windows[win], Groups: fw.groups}, *gr)
	}
	order := slices.DeleteFunc(slices.Clone(fw.windows[win]), func(x string) bool { return slices.Contains(moved, x) })
	fw.windows[win] = slices.Insert(order, slices.Index(order, ref), moved...)
	fw.normalize()
}

func TestNoSortKeepsManualOrderInFolder(t *testing.T) { // spec 2, critère 1
	t.Parallel()
	h, fw := noSortWorld(t)
	moveBefore(fw, "WS-B", "WS-A")
	h.wait("s-WS-A", "permission") // sorted, A would come back on top
	h.step(31 * time.Second)
	h.converge(10)
	h.assertQuiet("manual order")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-B", "WS-A"}) {
		t.Fatalf("Terrace = %v, want Ada' order [WS-B WS-A]", got)
	}
	noReorder(t, fw)
}

func TestNoSortKeepsFolderMovedToTop(t *testing.T) { // spec 2, critère 2
	t.Parallel()
	h, fw := noSortWorld(t)
	top := fw.windows[win][0]
	sys := fw.groupNamed("Terrace").AnchorID
	if top == sys {
		t.Fatalf("world: Terrace already on top: %q", fw.windows[win])
	}
	moveBefore(fw, sys, top)
	h.step(31 * time.Second)
	h.converge(10)
	h.assertQuiet("folder moved")
	if fw.windows[win][0] != sys {
		t.Fatalf("Terrace left the top: %q", fw.windows[win])
	}
	noReorder(t, fw)
}

func TestNoSortNewSessionLandsAtBottom(t *testing.T) { // spec 3, critère 3
	t.Parallel()
	h, fw := noSortWorld(t)
	fw.bornAfter("WS-D", "WS-A") // born after the selected one's block (P18)
	h.session("s-WS-D", "WS-D", "idle", true)
	h.bindIn("WS-D", "Hedge") // bound: filed at once, the birth grace is for unbound ones (D31)
	h.converge(10)
	h.assertQuiet("new session")
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"WS-C", "WS-D"}) {
		t.Fatalf("Hedge = %v, want WS-D at the bottom", got)
	}
	noReorder(t, fw)
}

// Spec 3 as written (critère 3): born unbound, held where it was born during
// its grace (D40), then bound — filed at the bottom of its folder, and it
// stays there once the grace has passed. Grave before its binding: sorted,
// it would rise above WS-C (the behaviour red on the base).
func TestNoSortBoundAfterBirthLandsAtBottom(t *testing.T) { // spec 3, critère 3
	t.Parallel()
	h, fw := noSortWorld(t)
	fw.bornAfter("WS-D", "WS-A") // P18, unbound: held in its slot (D40)
	h.session("s-WS-D", "WS-D", "idle", true)
	i0 := h.index("WS-D")
	h.step(31 * time.Second) // well under BirthGrace (30 min)
	h.converge(3)
	if h.index("WS-D") != i0 || fw.folderOf("WS-D") != "" || fw.adds("WS-D") != 0 {
		t.Fatalf("WS-D moved or filed during its grace: index %d (was %d), in %q", h.index("WS-D"), i0, fw.folderOf("WS-D"))
	}
	h.wait("s-WS-D", "permission") // the gravity a sort would act on
	h.bindIn("WS-D", "Hedge")
	h.converge(6)
	h.assertQuiet("bound after birth")
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"WS-C", "WS-D"}) {
		t.Fatalf("Hedge = %v, want WS-D at the bottom", got)
	}
	h.pastGrace()
	h.converge(5)
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"WS-C", "WS-D"}) {
		t.Fatalf("after the grace, Hedge = %v, want WS-D still at the bottom", got)
	}
	if n := fw.adds("WS-D"); n != 1 {
		t.Fatalf("want one group-add of WS-D, got %d", n)
	}
	noReorder(t, fw)
}

func TestNoSortNewFolderBeforeNoSubject(t *testing.T) { // spec 4, D2, critère 4
	t.Parallel()
	h, fw := noSortWorld(t)
	ns := fw.groupNamed(space.NoSubject).AnchorID
	moveBefore(fw, ns, fw.windows[win][0]) // « Inbox » on top: the current order, not D6
	h.converge(4)
	h.sessionIn("WS-E", "Trellis")
	h.converge(10)
	h.assertQuiet("new folder")
	de := fw.groupNamed("Trellis")
	if de == nil {
		t.Fatalf("no « Trellis »: %+v", fw.groups)
	}
	block := blockOf(ArrangeInput{Order: fw.windows[win], Groups: fw.groups}, *de)
	if h.index(block[len(block)-1])+1 != h.index(ns) {
		t.Fatalf("« Trellis » not right before « Inbox »: %q", fw.windows[win])
	}
	noReorder(t, fw)
}

func TestNoSortNewFolderAtEndWithoutNoSubject(t *testing.T) { // D2 « sinon en fin »
	t.Parallel()
	h, fw := newFolderHarness(t, noSort)
	fw.createAtTop = true
	h.sessionIn("WS-A", "Terrace")
	h.converge(10)
	h.sessionIn("WS-E", "Trellis") // D6 would put « Trellis » above « Terrace »
	h.converge(10)
	h.assertQuiet("folder at the end")
	if fw.groupNamed(space.NoSubject) != nil {
		t.Fatalf("world: unexpected « Inbox »: %+v", fw.groups)
	}
	if h.index(fw.groupNamed("Trellis").AnchorID) < h.index("WS-A") {
		t.Fatalf("« Trellis » not at the end: %q", fw.windows[win])
	}
	noReorder(t, fw)
}

func TestNoSortDraggedSessionIsPutBackAtBottom(t *testing.T) { // D3, critère 5
	t.Parallel()
	h, fw := noSortWorld(t)
	sys, mod := fw.groupNamed("Terrace"), fw.groupNamed("Hedge")
	sys.Members = slices.DeleteFunc(sys.Members, func(x string) bool { return x == "WS-A" })
	mod.Members = append(mod.Members, "WS-A")
	fw.contiguous()
	h.step(31 * time.Second)
	h.converge(10)
	h.assertQuiet("dragged back")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-B", "WS-A"}) {
		t.Fatalf("Terrace = %v, want WS-A back at the bottom", got)
	}
	noReorder(t, fw)
}

func TestNoSortStandbyBornLastThenStays(t *testing.T) { // spec 5, D4
	t.Parallel()
	h, fw := noSortWorld(t)
	h.putStandby("WS-C")
	h.converge(10)
	h.assertQuiet("standby born")
	sb := fw.groupNamed(space.Standby)
	if sb == nil || fw.folderOf("WS-C") != space.Standby {
		t.Fatalf("WS-C not in « Standby »: %+v", fw.groups)
	}
	sbAnchor := sb.AnchorID // fw.groups may be reallocated by later rounds: keep the id, not the pointer
	if last := fw.windows[win][len(fw.windows[win])-1]; fw.groupOf(last) == nil || fw.groupOf(last).Name != space.Standby {
		t.Fatalf("« Standby » not born last: %q", fw.windows[win])
	}
	moveBefore(fw, sbAnchor, fw.windows[win][0]) // the user puts it on top
	h.putStandby("WS-B")
	h.step(31 * time.Second)
	h.converge(10)
	h.assertQuiet("standby moved")
	if fw.windows[win][0] != sbAnchor || fw.folderOf("WS-B") != space.Standby {
		t.Fatalf("« Standby » left the top or WS-B not in it: %q", fw.windows[win])
	}
	noReorder(t, fw)
}

func TestNoSortToggleFolds(t *testing.T) { // spec 6, critère 6
	t.Parallel()
	h, fw := toggleWorld(t, noSort)
	res, err := h.toggle()
	if err != nil || !res.Collapsed {
		t.Fatalf("toggle = %+v, %v", res, err)
	}
	h.assertQuiet("folded")
	for f, c := range ownFoldersCollapsed(fw) {
		if !c {
			t.Fatalf("%s unfolded by the arrangement", f)
		}
	}
	if res, err := h.toggle(); err != nil || res.Collapsed {
		t.Fatalf("second toggle = %+v, %v", res, err)
	}
}

// Without the sort, a pill (gravity) change owes no round: no group list
// outside the slow pass. Sorted, the same change does (control).
func TestNoSortPillChangeOwesNoRound(t *testing.T) {
	t.Parallel()
	for _, sorted := range []bool{true, false} {
		var h *folderHarness
		var fw *folderWorld
		if sorted {
			h, fw = newFolderHarness(t)
			h.sessionIn("WS-A", "Terrace")
			h.sessionIn("WS-B", "Terrace")
			h.converge(10)
			h.assertQuiet("sorted world")
		} else {
			h, fw = noSortWorld(t)
		}
		h.step(31 * time.Second) // a slow pass now: the next ones are > 20 s away
		h.converge(1)
		before := fw.count("group-list")
		h.wait("s-WS-B", "permission")
		for i := 0; i < 6; i++ {
			h.step(time.Second)
		}
		got := fw.count("group-list") - before
		if sorted && got == 0 {
			t.Fatal("control: sorted, a pill change must owe a round")
		}
		if !sorted && got != 0 {
			t.Fatalf("sort off: %d group list(s) after a pill change", got)
		}
	}
}

// H2 at the runner: a group the user made by hand is dissolved and its members
// filed; nothing else moves.
func TestNoSortDissolutionMovesNothing(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	h.sessionIn("WS-X", "Terrace")
	h.sessionIn("WS-Y", "Hedge")
	fw.manualGroup("G-HAND", "Perso", "WS-X", false, "WS-Y")
	others := func() []string {
		return slices.DeleteFunc(slices.Clone(fw.windows[win]), func(x string) bool { return x == "WS-X" || x == "WS-Y" })
	}
	before := others()
	h.step(31 * time.Second)
	h.converge(10)
	h.assertQuiet("dissolved")
	if fw.group("G-HAND") != nil || fw.folderOf("WS-X") != "Terrace" || fw.folderOf("WS-Y") != "Hedge" {
		t.Fatalf("not dissolved and filed: %+v", fw.groups)
	}
	if after := others(); !slices.Equal(before, after) {
		t.Fatalf("other positions moved: %q → %q", before, after)
	}
	noReorder(t, fw)
}

// D11 (A), cmux probe P19: real cmux's add keeps the side the workspace comes
// from. Dragged into « Hedge », above « Terrace »: the add back lands right
// after Terrace's anchor, then one reorder puts WS-A at the bottom.
func TestNoSortDraggedSessionIsPutBackAtBottomKeepsSide(t *testing.T) { // D3, critère 5
	t.Parallel()
	h, fw := noSortWorld(t)
	fw.addKeepsSide = true
	sys, mod := fw.groupNamed("Terrace"), fw.groupNamed("Hedge")
	if h.index(mod.AnchorID) > h.index(sys.AnchorID) {
		t.Fatalf("world: « Hedge » not above « Terrace »: %q", fw.windows[win])
	}
	sys.Members = slices.DeleteFunc(sys.Members, func(x string) bool { return x == "WS-A" })
	mod.Members = append(mod.Members, "WS-A")
	fw.contiguous()
	places, reorders := fw.count("place"), fw.count("reorder")
	h.step(31 * time.Second)
	h.converge(10)
	h.assertQuiet("dragged back")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-B", "WS-A"}) {
		t.Fatalf("Terrace = %v, want WS-A back at the bottom", got)
	}
	if n, r := fw.count("place")-places, fw.count("reorder")-reorders; n != 1 || r != 0 {
		t.Fatalf("%d place(s), %d reorder(s), want the one move that puts WS-A at the bottom (D39)", n, r)
	}
	noReorder(t, fw)
}

// D11 (A), P19, spec 3 (critère 3): born free above the folder of its
// subject, then bound — the add lands right after the anchor, one reorder
// puts it at the bottom, and it stays there.
func TestNoSortBoundFreeAboveLandsAtBottomKeepsSide(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	fw.addKeepsSide = true
	fw.add(win, "WS-D", "WS-D")
	fw.windows[win] = slices.Insert(fw.windows[win][:len(fw.windows[win])-1], 0, "WS-D") // free, on top
	h.session("s-WS-D", "WS-D", "idle", true)
	if h.index("WS-D") > h.index(fw.groupNamed("Terrace").AnchorID) {
		t.Fatalf("world: WS-D not above « Terrace »: %q", fw.windows[win])
	}
	h.bindIn("WS-D", "Terrace")
	h.converge(10)
	h.assertQuiet("bound, free above")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-B", "WS-D"}) {
		t.Fatalf("Terrace = %v, want WS-D at the bottom", got)
	}
	h.pastGrace()
	h.converge(5)
	h.assertQuiet("after the grace")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-B", "WS-D"}) {
		t.Fatalf("after the grace, Terrace = %v, want WS-D still at the bottom", got)
	}
	if n := fw.adds("WS-D"); n != 1 {
		t.Fatalf("want one group-add of WS-D, got %d", n)
	}
	moveBefore(fw, "WS-D", "WS-A") // seen at the bottom, it is forgotten: Ada' order now stays
	h.step(31 * time.Second)
	h.converge(10)
	h.assertQuiet("moved by hand")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-D", "WS-A", "WS-B"}) {
		t.Fatalf("Terrace = %v, want Ada' order [WS-D WS-A WS-B]", got)
	}
	noReorder(t, fw)
}

// D1 (sub_x000000000000040): a session spawned from the middle of a folder is
// born right under its caller (cmux places it, P23; modelled by bornNextTo:
// the simulator does not read the spawn argv); filed in that same folder, the
// sort off never moves it, whatever the user has selected.
func TestNoSortSpawnBornUnderCaller(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	moveBefore(fw, "WS-B", "WS-A") // Ada' order: B, A
	fw.selected = "WS-C"           // Ada looks elsewhere
	fw.bornNextTo("WS-D", "WS-B")  // spawned from WS-B
	h.session("s-WS-D", "WS-D", "idle", true)
	h.bindIn("WS-D", "Terrace")
	h.converge(10)
	h.assertQuiet("spawned under the caller")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-B", "WS-D", "WS-A"}) {
		t.Fatalf("Terrace = %v, want [WS-B WS-D WS-A]", got)
	}
	if n := fw.adds("WS-D"); n != 0 {
		t.Fatalf("born in its folder: want no group-add of WS-D, got %d", n)
	}
	noReorder(t, fw)
}

// D1: the spawned subject is filed in another folder than the caller's: it
// goes to that folder's bottom — not under the session the user has selected
// there (Review Focus 2).
func TestNoSortSpawnOtherFolderAtBottom(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	h.sessionIn("WS-E", "Hedge")
	h.converge(10)
	h.assertQuiet("Hedge {C, E}")
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"WS-C", "WS-E"}) {
		t.Fatalf("world: Hedge = %v", got)
	}
	fw.selected = "WS-C"          // Ada looks at a session of the target folder
	fw.bornNextTo("WS-D", "WS-A") // spawned from WS-A, born in « Terrace »
	h.session("s-WS-D", "WS-D", "idle", true)
	h.bindIn("WS-D", "Hedge")
	h.converge(10)
	h.assertQuiet("spawned into another folder")
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"WS-C", "WS-E", "WS-D"}) {
		t.Fatalf("Hedge = %v, want WS-D at the bottom [WS-C WS-E WS-D]", got)
	}
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-B"}) {
		t.Fatalf("Terrace = %v, want [WS-A WS-B]", got)
	}
	noReorder(t, fw)
}

// D5-A over D11: the user drags the workspace pigtail just added (right after
// the anchor, P19) before the next pass: it is no longer where cmux's add put
// it, so his order wins and nothing moves it to the bottom.
func TestNoSortHandMoveAfterAddWins(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	fw.addKeepsSide = true
	fw.add(win, "WS-D", "WS-D")
	fw.windows[win] = slices.Insert(fw.windows[win][:len(fw.windows[win])-1], 0, "WS-D") // free, on top
	h.session("s-WS-D", "WS-D", "idle", true)
	fw.onWrite = func(call string) {
		if call == "group-add "+fw.groupNamed("Terrace").ID+" WS-D" {
			fw.onWrite = nil
			moveBefore(fw, "WS-D", "WS-B") // Ada: between WS-A and WS-B
		}
	}
	h.bindIn("WS-D", "Terrace")
	h.converge(10)
	h.assertQuiet("hand move after the add")
	if fw.adds("WS-D") != 1 {
		t.Fatalf("want one group-add of WS-D, got %d", fw.adds("WS-D"))
	}
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-D", "WS-B"}) {
		t.Fatalf("Terrace = %v, want Ada' order [WS-A WS-D WS-B]", got)
	}
	noReorder(t, fw)
}

// D8 × D11 (A): the tails pigtail added are kept per window. Two windows owe
// a round in the same pass, each adds a session born free above its
// « Terrace »: the round of one window must not forget the other's tail, so
// each ends at the bottom of its folder (review C1 of sub_x000000000000007).
func TestNoSortTailKeptAcrossWindows(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	h.sessionOn(winB, "WS-Y", "Terrace")
	h.converge(10)
	h.assertQuiet("two windows, sort off")
	if got := fw.folderIn(winB, "WS-Y"); got != "Terrace" {
		t.Fatalf("world: WS-Y in %q", got)
	}
	fw.addKeepsSide = true
	fw.add(win, "WS-D", "WS-D")
	fw.windows[win] = slices.Insert(fw.windows[win][:len(fw.windows[win])-1], 0, "WS-D") // free, on top
	h.session("s-WS-D", "WS-D", "idle", true)
	fw.add(winB, "WS-Z", "WS-Z")
	fw.windows[winB] = slices.Insert(fw.windows[winB][:len(fw.windows[winB])-1], 0, "WS-Z")
	h.session("s-WS-Z", "WS-Z", "idle", true)
	h.bindIn("WS-D", "Terrace")
	h.bindIn("WS-Z", "Terrace")
	h.converge(12)
	h.assertQuiet("bound in both windows")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-B", "WS-D"}) {
		t.Fatalf("win: Terrace = %v, want WS-D at the bottom", got)
	}
	if gr := fw.groupNamedIn(winB, "Terrace"); gr == nil || !slices.Equal(gr.Members[1:], []string{"WS-Y", "WS-Z"}) {
		t.Fatalf("winB: Terrace = %+v, want WS-Z at the bottom", gr)
	}
}

// P1 (sub_x000000000000040): ⌘N from a folder member, with cmux's
// workspaceGroups.newWorkspacePlacement at afterCurrent, is born right under
// that member (bornNextTo). Filed in the same folder, the sort off never moves
// it — during its grace, once bound, after its grace.
func TestNoSortCmdNBornUnderSelectionStays(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	fw.selected = "WS-A"
	fw.bornNextTo("WS-D", "WS-A")
	fw.selected = "WS-D" // cmux selects what ⌘N made
	h.session("s-WS-D", "WS-D", "idle", true)
	h.converge(3) // young, unbound: held where born
	h.bindIn("WS-D", "Terrace")
	h.converge(10)
	h.assertQuiet("⌘N under the selection")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-D", "WS-B"}) {
		t.Fatalf("Terrace = %v, want [WS-A WS-D WS-B]", got)
	}
	h.pastGrace()
	h.converge(5)
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-D", "WS-B"}) {
		t.Fatalf("after the grace, Terrace = %v", got)
	}
	if n := fw.adds("WS-D"); n != 0 {
		t.Fatalf("want no group-add of WS-D, got %d", n)
	}
	noReorder(t, fw)
}

// P1 « otherwise bottom »: born under WS-A in « Terrace » but filed in
// « Hedge » → bottom of « Hedge »; left unbound past its grace → bottom
// of « Inbox » (Review Focus 4).
func TestNoSortCmdNOtherFolderAtBottom(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	h.sessionIn("WS-E", "Hedge")
	h.converge(10)
	fw.selected = "WS-A"
	fw.bornNextTo("WS-D", "WS-A")
	fw.selected = "WS-D"
	h.session("s-WS-D", "WS-D", "idle", true)
	h.bindIn("WS-D", "Hedge")
	h.converge(10)
	h.assertQuiet("⌘N filed elsewhere")
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"WS-C", "WS-E", "WS-D"}) {
		t.Fatalf("Hedge = %v, want [WS-C WS-E WS-D]", got)
	}
	noReorder(t, fw)
}

func TestNoSortCmdNUnboundLeavesForOther(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t) // « Inbox » {Shed}
	fw.selected = "WS-A"
	fw.bornNextTo("WS-D", "WS-A") // never bound
	h.converge(3)
	if got := fw.folderOf("WS-D"); got != "Terrace" {
		t.Fatalf("during its grace WS-D is in %q, want held in « Terrace »", got)
	}
	h.pastGrace()
	h.converge(10)
	// The simulator starts the birth clock at the loop's first sighting, not at
	// bornNextTo: one grace is not enough, a second one moves WS-D out.
	h.pastGrace()
	h.converge(10)
	h.assertQuiet("unbound ⌘N after its grace")
	other := fw.members(space.NoSubject)
	if len(other) == 0 || other[len(other)-1] != "WS-D" {
		t.Fatalf("« Inbox » = %v, want WS-D at its bottom", other)
	}
	noReorder(t, fw)
}

// #231 × #236: the real builtin table (new aires included) and a caller filed
// in a NEW aire folder. The spawn is born under its caller by cmux; filed in
// that same folder it stays there, no move written (sort off), and in sorted
// mode at equal gravity too.
func TestNoSortSpawnBornUnderCallerInNewArea(t *testing.T) {
	t.Parallel()
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	withTable := func(c *Config, d *Deps) {
		order := tbl.FolderOrder()
		d.FolderOrder = func() []string { return slices.Clone(order) }
		d.FolderIcon = tbl.Icon
	}
	for _, area := range []string{"Pond", "Vineyard"} {
		if !slices.Contains(tbl.FolderOrder(), area) {
			t.Fatalf("builtin table has no %q aire: %q", area, tbl.FolderOrder())
		}
		t.Run("nosort/"+area, func(t *testing.T) {
			h, fw := newFolderHarness(t, noSort, withTable)
			fw.createAtTop = true
			h.sessionIn("WS-A", area)
			h.sessionIn("WS-B", area)
			h.sessionIn("WS-C", "Hedge")
			h.converge(12)
			h.assertQuiet("no-sort area world")
			moveBefore(fw, "WS-B", "WS-A") // Ada' order: B, A
			fw.selected = "WS-C"
			fw.bornNextTo("WS-D", "WS-B")
			h.session("s-WS-D", "WS-D", "idle", true)
			h.bindIn("WS-D", area)
			h.converge(10)
			h.assertQuiet("spawned under the caller in " + area)
			if got := fw.members(area); !slices.Equal(got, []string{"WS-B", "WS-D", "WS-A"}) {
				t.Fatalf("%s = %v, want [WS-B WS-D WS-A]", area, got)
			}
			if n := fw.adds("WS-D"); n != 0 {
				t.Fatalf("born in its folder: want no group-add of WS-D, got %d", n)
			}
			noReorder(t, fw)
		})
		t.Run("sorted-equal-gravity/"+area, func(t *testing.T) {
			h, fw := newFolderHarness(t, withTable)
			h.sessionIn("WS-A", area)
			h.sessionIn("WS-B", area)
			h.converge(12)
			h.assertQuiet("sorted area world")
			n := len(fw.folderWrites())
			fw.bornNextTo("WS-D", "WS-A")
			h.session("s-WS-D", "WS-D", "idle", true)
			h.bindIn("WS-D", area)
			h.converge(10)
			h.assertQuiet("spawned, sorted, equal gravity in " + area)
			if got := fw.members(area); !slices.Equal(got, []string{"WS-A", "WS-D", "WS-B"}) {
				t.Fatalf("%s = %v, want [WS-A WS-D WS-B]", area, got)
			}
			if w := fw.folderWrites()[n:]; len(w) != 0 {
				t.Fatalf("equal gravity: want 0 writes, got %q", w)
			}
		})
	}
}
