package cmuxsync

import (
	"reflect"
	"slices"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/space"
)

// Maturity bands (D2, D11, D14–D17 of sub_x000000000000014): inside an area
// folder or « Inbox », the pinned tabs (cmux's own order), then bands 0..3
// (building, designing, understanding, inbox), then the tabs of closed
// subjects; a tab with no subject keeps its slot; inside a band the current
// order holds and a tab that enters goes to the bottom of its band.

func TestBandOf(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]int{"building": BandBuilding, "designing": BandDesigning, "understanding": BandUnderstanding, "inbox": BandInbox} {
		if got, ok := BandOf(status); !ok || got != want {
			t.Errorf("BandOf(%q) = %d, %v; want %d, true", status, got, ok, want)
		}
	}
	for _, status := range []string{"done", "parked", "dropped", "captured", "", "implement", "Building"} {
		if _, ok := BandOf(status); ok {
			t.Errorf("BandOf(%q): a band, want none", status)
		}
	}
}

// bandInput is one « Hedge » folder (anchor AM) holding ms, in that order.
// The first letter names the tab: B building, D designing, U understanding,
// I inbox (all bound), X bound to a closed subject (no band), any other
// letter has no subject.
func bandInput(ms ...string) ArrangeInput {
	in := ArrangeInput{Folders: folders, NoSort: true, GroupMoves: true,
		Order:  append([]string{"AM"}, ms...),
		Groups: []cmux.Group{g("G-M", "Hedge", "AM", true, ms...)},
		Target: map[string]string{}, Banded: map[string]bool{"Hedge": true, space.NoSubject: true},
		Band: map[string]int{}, Bound: map[string]bool{}, Pinned: map[string]bool{}, Enter: map[string]bool{}}
	for _, m := range ms {
		in.Target[m] = "Hedge"
		band := map[byte]int{'B': BandBuilding, 'D': BandDesigning, 'U': BandUnderstanding, 'I': BandInbox}
		if b, ok := band[m[0]]; ok {
			in.Band[m], in.Bound[m] = b, true
		}
		if m[0] == 'X' {
			in.Bound[m] = true
		}
	}
	return in
}

// wantOrder asserts the folder reaches want (anchor first), then nothing
// once applied: sorted, one reorder; without the sort, single moves only
// (FolderPlace, D39) whose application gives want; want nil = no op at all.
func wantOrder(t *testing.T, in ArrangeInput, want ...string) {
	t.Helper()
	ops := Arrange(in)
	if want == nil {
		if ops != nil {
			t.Fatalf("ops = %+v, want none", ops)
		}
		return
	}
	full := append([]string{"AM"}, want...)
	if in.NoSort {
		if got := applyPlaces(t, in.Order, ops); !slices.Equal(got, full) {
			t.Fatalf("ops = %+v give %v, want %v", ops, got, full)
		}
		assertPlacesSafe(t, in, ops)
	} else if len(ops) != 1 || ops[0].Kind != FolderReorder || !slices.Equal(ops[0].Order, full) {
		t.Fatalf("ops = %+v, want one reorder to %v", ops, full)
	}
	if again := Arrange(simulate(t, in, ops)); again != nil {
		t.Fatalf("after the reorder, ops = %+v", again)
	}
}

func TestBandsOrderNoSort(t *testing.T) {
	t.Parallel()
	wantOrder(t, bandInput("I1", "B1", "D1", "U1"), "B1", "D1", "U1", "I1")
}

func TestBandsKeepManualOrderInBand(t *testing.T) {
	t.Parallel()
	in := bandInput("B2", "B1", "D1")
	wantOrder(t, in)
	wantOrder(t, in)
}

func TestBandsEnterGoesBottomOfBand(t *testing.T) {
	t.Parallel()
	in := bandInput("B2", "B1", "D1")
	in.Enter["B2"] = true
	wantOrder(t, in, "B1", "B2", "D1")
}

func TestBandsUnbandedUnderBands(t *testing.T) {
	t.Parallel()
	wantOrder(t, bandInput("X", "B1"), "B1", "X")
	wantOrder(t, bandInput("B1", "X", "Y"))
}

func TestBandsUnboundKeepsPlace(t *testing.T) {
	t.Parallel()
	in := bandInput("X", "B1", "Y", "B0")
	delete(in.Bound, "X") // X and Y have no subject
	in.Band["B1"], in.Band["B0"] = BandDesigning, BandBuilding
	wantOrder(t, in, "X", "B0", "Y", "B1")
}

func TestBandsPinnedKeepSlot(t *testing.T) {
	t.Parallel()
	in := bandInput("P", "B1", "I1")
	in.Band["P"], in.Bound["P"], in.Pinned["P"] = BandInbox, true, true
	wantOrder(t, in)
}

// Review Focus 3: a tab pigtail added, then pinned by hand before the next
// pass, is never moved. Montage of B7 (D39): cmux holds a pinned tab at the
// head of its group (P24 a, e), so P is first; B1 goes right under it.
func TestBandsEnterPinnedNeverMoved(t *testing.T) {
	t.Parallel()
	in := bandInput("P", "I1", "B1")
	in.Band["P"], in.Bound["P"], in.Pinned["P"], in.Enter["P"] = BandBuilding, true, true, true
	ops := Arrange(in)
	if want := []FolderOp{{Kind: FolderPlace, Workspace: "B1", Ref: "P"}}; !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v, want %+v, P never moved", ops, want)
	}
	if again := Arrange(simulate(t, in, ops)); again != nil {
		t.Fatalf("after the reorder, ops = %+v", again)
	}
}

// D40: a young free tab held where it was born keeps its slot while the
// bands arrange the folder (Hold holds free workspaces only).
func TestBandsHeldYoungKeepSlot(t *testing.T) {
	t.Parallel()
	in := bandInput("I1", "B1")
	in.NoSort = false
	in.Order = append([]string{"N"}, in.Order...)
	in.Hold = map[string]bool{"N": true}
	ops := Arrange(in)
	if len(ops) != 1 || ops[0].Kind != FolderReorder || !slices.Equal(ops[0].Order, []string{"N", "AM", "B1", "I1"}) {
		t.Fatalf("ops = %+v, want one reorder to [N AM B1 I1]", ops)
	}
	if again := Arrange(simulate(t, in, ops)); again != nil {
		t.Fatalf("after the reorder, ops = %+v", again)
	}
}

// D14: never in a focus folder nor in « Standby ».
func TestBandsNotInFocusNorStandby(t *testing.T) {
	t.Parallel()
	for _, f := range []string{offsite, space.Standby} {
		in := bandInput("I1", "B1")
		in.Folders = []string{f, "Hedge", space.NoSubject, space.Standby}
		in.Groups[0].Name = f
		for _, m := range []string{"I1", "B1"} {
			in.Target[m] = f
		}
		in.Banded = map[string]bool{"Hedge": true, space.NoSubject: true}
		in.Unsorted = map[string]bool{space.Standby: true}
		wantOrder(t, in)
	}
}

func TestBandsSortedBandThenGravity(t *testing.T) {
	t.Parallel()
	in := bandInput("B1", "I1", "B2")
	in.NoSort = false
	in.Rank = map[string]int{"B1": 5, "I1": 1, "B2": 1}
	wantOrder(t, in, "B2", "B1", "I1")
}

func TestBandsIdempotent(t *testing.T) {
	t.Parallel()
	in := bandInput("I2", "X", "D1", "Y", "B1", "I1", "U1")
	in.Enter["D1"] = true
	ops := Arrange(in)
	if len(ops) == 0 || slices.ContainsFunc(ops, func(op FolderOp) bool { return op.Kind != FolderPlace }) {
		t.Fatalf("ops = %+v, want single moves only (D39)", ops)
	}
	if again := Arrange(simulate(t, in, ops)); again != nil {
		t.Fatalf("Arrange on its own output: %+v", again)
	}
}

// D3: a pinned tab bound to a subject is filed in its folder; an unbound
// pinned tab is left alone (D13 unchanged for it).
func TestPinnedBoundIsFiled(t *testing.T) {
	t.Parallel()
	pinned := map[string]bool{"P": true, "Q": true}
	got := folderTargets([]string{"P", "Q"}, nil, pinned, nil, nil,
		Filing{Bound: map[string]string{"P": "Terrace"}}, targetFolders)
	if got["P"] != "Terrace" {
		t.Fatalf("bound pinned target = %q, want Terrace", got["P"])
	}
	if v, ok := got["Q"]; ok {
		t.Fatalf("unbound pinned filed: %q", v)
	}
	ops := Arrange(ArrangeInput{Folders: targetFolders, Order: []string{"GS", "P"},
		Groups: []cmux.Group{g("G-S", "Terrace", "GS", true)}, Target: got, Pinned: pinned})
	if len(ops) != 1 || !reflect.DeepEqual(ops[0], FolderOp{Kind: FolderAdd, Group: "G-S", Workspace: "P"}) {
		t.Fatalf("ops = %+v, want one add of P (no repin: cmux.AddPinnedLandsAtHead)", ops)
	}
}

// NoSort, « Standby » (Unsorted): an entering tab (cmux's add keeps the side
// it comes from, P19: at the head) goes to the bottom of the folder, as
// tailOrder did; no band is applied there.
func TestBandsNoSortUnsortedFolderEnterGoesLast(t *testing.T) {
	t.Parallel()
	in := bandInput("I1", "B1")
	in.Folders = []string{"Hedge", space.NoSubject, space.Standby}
	in.Groups[0].Name = space.Standby
	for _, m := range []string{"I1", "B1"} {
		in.Target[m] = space.Standby
	}
	in.Unsorted = map[string]bool{space.Standby: true}
	in.Enter["I1"] = true
	wantOrder(t, in, "B1", "I1")
	delete(in.Enter, "I1") // no entry: the current order holds, no band sort
	wantOrder(t, in)
}

// D14: a focus folder is not banded; a band change or an unpin there marks
// nothing, the focus keeps its order. In a banded folder both still enter.
func TestEnterForBandedFoldersOnly(t *testing.T) {
	t.Parallel()
	order := []string{"AF", "B1", "I1", "AM", "M1", "M2"}
	groups := []cmux.Group{
		g("G-F", offsite, "AF", true, "B1", "I1"),
		g("G-M", "Hedge", "AM", true, "M1", "M2"),
	}
	banded := map[string]bool{"Hedge": true}
	pinned := map[string]bool{"B1": true, "M1": true}
	band := map[string]int{"B1": BandBuilding, "I1": BandInbox, "M2": BandBuilding}
	st := &winFolders{}
	if e := st.enterFor(order, groups, pinned, band, banded, false); e != nil {
		t.Fatalf("first pass: %v", e)
	}
	pinned = map[string]bool{}                                                      // B1 and M1 unpinned
	band = map[string]int{"B1": BandDesigning, "I1": BandInbox, "M2": BandBuilding} // band changes
	got := st.enterFor(order, groups, pinned, band, banded, false)
	if got["B1"] || got["I1"] {
		t.Fatalf("focus members marked: %v", got)
	}
	if !got["M1"] {
		t.Fatalf("unpinned member of a banded folder not marked: %v", got)
	}
}

// Sorted mode never reads Enter (blockMembers sorts by gravity): nothing is
// kept, so no entry lingers unconsumed.
func TestEnterForSortedKeepsNothing(t *testing.T) {
	t.Parallel()
	order := []string{"AM", "M1", "M2"}
	groups := []cmux.Group{g("G-M", "Hedge", "AM", true, "M1", "M2")}
	banded := map[string]bool{"Hedge": true}
	st := &winFolders{}
	st.enterFor(order, groups, map[string]bool{"M1": true}, map[string]int{"M2": BandBuilding}, banded, true)
	if e := st.enterFor(order, groups, map[string]bool{}, map[string]int{"M2": BandBuilding}, banded, true); e != nil || len(st.enter) != 0 {
		t.Fatalf("sorted: enter %v / %v", e, st.enter)
	}
}

// D39 of sub_x000000000000014: without the sort, each tab the bands or an
// entry move is moved alone (FolderPlace = `reorder-workspace --after
// <ref>`), right after a neighbour of its own folder, never above a pinned one
// (cmux probe P24 c2, P26 c); a whole-window FolderReorder would lift a folder
// holding a pinned member (P26 a).

// applyPlaces is order after ops, each a FolderPlace applied as cmux renders a
// single move (P26 b): the workspace taken out, put back right after (before)
// its ref.
func applyPlaces(t *testing.T, order []string, ops []FolderOp) []string {
	t.Helper()
	out := slices.Clone(order)
	for _, op := range ops {
		if op.Kind != FolderPlace {
			t.Fatalf("op %+v, want only FolderPlace", op)
		}
		if op.Workspace == op.Ref || !slices.Contains(out, op.Workspace) || !slices.Contains(out, op.Ref) {
			t.Fatalf("bad place %+v in %v", op, out)
		}
		out = slices.DeleteFunc(out, func(x string) bool { return x == op.Workspace })
		at := slices.Index(out, op.Ref)
		if !op.Before {
			at++
		}
		out = slices.Insert(out, at, op.Workspace)
	}
	return out
}

// assertPlacesSafe: no op moves a pinned tab or an unbound tab that does not
// enter, aims at the anchor of a group holding a pinned member, or goes
// before a pinned tab.
func assertPlacesSafe(t *testing.T, in ArrangeInput, ops []FolderOp) {
	t.Helper()
	pinnedIn := map[string]bool{} // anchor → its group holds a pinned member
	for _, gr := range in.Groups {
		for _, m := range gr.Members {
			if in.Pinned[m] {
				pinnedIn[gr.AnchorID] = true
			}
		}
	}
	for _, op := range ops {
		switch {
		case in.Pinned[op.Workspace]:
			t.Fatalf("%+v moves a pinned tab", op)
		case !in.Bound[op.Workspace] && !in.Enter[op.Workspace] && in.Bound != nil:
			t.Fatalf("%+v moves an unbound tab that does not enter", op)
		case pinnedIn[op.Ref]:
			t.Fatalf("%+v aims at the anchor of a group holding a pinned tab", op)
		case op.Before && in.Pinned[op.Ref]:
			t.Fatalf("%+v goes before a pinned tab", op)
		}
	}
}

func TestNoSortPlacesOnly(t *testing.T) {
	t.Parallel()
	place := func(ws, ref string) FolderOp { return FolderOp{Kind: FolderPlace, Workspace: ws, Ref: ref} }
	for _, tc := range []struct {
		name string
		in   func() ArrangeInput
		want []FolderOp
	}{
		{"an entering tab moves, not its band", func() ArrangeInput {
			in := bandInput("B2", "B1", "D1")
			in.Enter["B2"] = true
			return in
		}, []FolderOp{place("B2", "B1")}},
		{"a band change is an entry", func() ArrangeInput {
			in := bandInput("B1", "D1", "U1", "I1")
			in.Band["U1"], in.Enter["U1"] = BandBuilding, true
			return in
		}, []FolderOp{place("U1", "B1")}},
		{"just unpinned, to the bottom of its band", func() ArrangeInput {
			in := bandInput("P", "B1", "D1", "I1")
			in.Band["P"], in.Bound["P"], in.Enter["P"] = BandDesigning, true, true
			return in
		}, []FolderOp{place("P", "D1")}},
		{"under the pinned one, never after the anchor", func() ArrangeInput {
			in := bandInput("P", "I1", "B1")
			in.Band["P"], in.Bound["P"], in.Pinned["P"] = BandDesigning, true, true
			return in
		}, []FolderOp{place("B1", "P")}},
		{"two entries for one place: the non-entering one is kept", func() ArrangeInput {
			in := bandInput("E1", "B1", "E2")
			for _, m := range []string{"E1", "E2"} {
				in.Band[m], in.Bound[m], in.Enter[m] = BandBuilding, true, true
			}
			return in
		}, []FolderOp{place("E1", "B1")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in()
			ops := Arrange(in)
			if !reflect.DeepEqual(ops, tc.want) {
				t.Fatalf("ops = %+v, want %+v", ops, tc.want)
			}
			assertPlacesSafe(t, in, ops)
			if again := Arrange(simulate(t, in, ops)); again != nil {
				t.Fatalf("after the moves, ops = %+v", again)
			}
		})
	}
}

// noSortTables are the sort-off tables of bands_test.go and
// folders_nosort_test.go with the window order each expects (the order
// noSortOrder's whole reorder used to send).
func noSortTables() []struct {
	name string
	in   ArrangeInput
	want []string
} {
	type tc = struct {
		name string
		in   ArrangeInput
		want []string
	}
	var out []tc
	add := func(name string, in ArrangeInput, want ...string) { out = append(out, tc{name, in, want}) }
	add("bands", bandInput("I1", "B1", "D1", "U1"), "AM", "B1", "D1", "U1", "I1")
	in := bandInput("B2", "B1", "D1")
	in.Enter["B2"] = true
	add("enter bottom of band", in, "AM", "B1", "B2", "D1")
	add("unbanded under bands", bandInput("X", "B1"), "AM", "B1", "X")
	in = bandInput("X", "B1", "Y", "B0")
	delete(in.Bound, "X")
	in.Band["B1"], in.Band["B0"] = BandDesigning, BandBuilding
	add("unbound keeps place", in, "AM", "X", "B0", "Y", "B1")
	in = bandInput("P", "I1", "B1")
	in.Band["P"], in.Bound["P"], in.Pinned["P"], in.Enter["P"] = BandBuilding, true, true, true
	add("entering pinned never moved", in, "AM", "P", "B1", "I1")
	in = bandInput("I2", "X", "D1", "Y", "B1", "I1", "U1")
	in.Enter["D1"] = true
	add("idempotent", in, "AM", "B1", "D1", "U1", "Y", "I2", "I1", "X")
	for _, c := range []struct {
		name         string
		order, wantO []string
		enter        map[string]bool
	}{
		{"added from above", []string{"GM", "M1", "GS", "S3", "S2", "S1", "F"}, []string{"GM", "M1", "GS", "S2", "S1", "S3", "F"}, map[string]bool{"S3": true}},
		{"two tails", []string{"GM", "M1", "GS", "S3", "S2", "S1", "F"}, []string{"GM", "M1", "GS", "S2", "S3", "S1", "F"}, map[string]bool{"S3": true, "S1": true}},
		{"several folders", []string{"GM", "M2", "M1", "GS", "S3", "S2", "S1", "F"}, []string{"GM", "M1", "M2", "GS", "S2", "S1", "S3", "F"}, map[string]bool{"S3": true, "M2": true}},
	} {
		var gm, gs []string
		target := map[string]string{}
		for _, ws := range c.order {
			switch ws[0] {
			case 'M':
				gm, target[ws] = append(gm, ws), "Hedge"
			case 'S':
				gs, target[ws] = append(gs, ws), "Terrace"
			}
		}
		add(c.name, ArrangeInput{Folders: folders, NoSort: true, GroupMoves: true, Order: c.order,
			Groups: []cmux.Group{g("G-M", "Hedge", "GM", true, gm...), g("G-S", "Terrace", "GS", true, gs...)},
			Target: target, Rank: map[string]int{"S1": 1, "S2": 5}, Enter: c.enter}, c.wantO...)
	}
	return out
}

// Property: on those tables, the FolderPlace ops, applied in order, give the
// order the tables expect, and none is unsafe.
func TestNoSortPlacesRebuildWant(t *testing.T) {
	t.Parallel()
	for _, tc := range noSortTables() {
		t.Run(tc.name, func(t *testing.T) {
			ops := Arrange(tc.in)
			if got := applyPlaces(t, tc.in.Order, ops); !slices.Equal(got, tc.want) {
				t.Fatalf("places %+v give %v, want %v", ops, got, tc.want)
			}
			assertPlacesSafe(t, tc.in, ops)
		})
	}
}

// No whole-window reorder with the sort off, on the same tables.
func TestNoSortNeverWholeReorder(t *testing.T) {
	t.Parallel()
	for _, tc := range noSortTables() {
		for _, op := range Arrange(tc.in) {
			if op.Kind == FolderReorder {
				t.Fatalf("%s: a whole reorder %+v", tc.name, op)
			}
		}
	}
}
