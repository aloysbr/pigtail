package cmuxsync

import (
	"slices"
	"testing"
	"time"
)

// Fidelity of the simulated cmux to the cmux probe notes P24 and P26 (Task 14 of
// the maturity-status plan, sub_x000000000000014): a fake that applied a
// whole reorder as asked could never show acceptance t1 item 3 (a folder holding
// a pinned member rising), the lesson of sub_x000000000000042 and
// sub_x000000000000002.

// twoFolders is X = [x…] above Y = [y…] in win, generated anchors AX and AY,
// a free row F last; pinned rows of Y are pinned in place (no move).
func twoFolders(xs, ys []string, pinnedY ...string) *folderWorld {
	fw := newFolderWorld(newWorld(), time.Now)
	for _, ws := range append(slices.Clone(xs), ys...) {
		fw.add(win, ws, ws)
	}
	fw.manualGroup("GX", "X", "AX", true, xs...)
	fw.manualGroup("GY", "Y", "AY", true, ys...)
	fw.add(win, "F", "F")
	for _, ws := range pinnedY {
		fw.ws[ws].Pinned = true
	}
	return fw
}

func TestFolderWorldReorderLiftsPinnedGroup(t *testing.T) {
	t.Parallel()
	fw := twoFolders([]string{"x1", "x2"}, []string{"y1", "y2"}, "y1")
	if got := fw.windows[win]; !slices.Equal(got, []string{"AX", "x1", "x2", "AY", "y1", "y2", "F"}) {
		t.Fatalf("setup: %q", got)
	}
	// The current order with x1 and x2 swapped: blocks kept (P26 a).
	if err := fw.Reorder(win, []string{"AX", "x2", "x1", "AY", "y1", "y2", "F"}); err != nil {
		t.Fatal(err)
	}
	if got, want := fw.blockSeq(win), []string{"GY", "GX", "F"}; !slices.Equal(got, want) {
		t.Fatalf("blocks %q, want %q: the pinned y1 goes first and Y is glued around it (P26 a)", got, want)
	}
	if got := fw.members("X"); !slices.Equal(got, []string{"x2", "x1"}) {
		t.Fatalf("X = %q, want [x2 x1]", got)
	}
	if got := fw.windows[win]; !slices.Equal(got, []string{"AY", "y1", "y2", "AX", "x2", "x1", "F"}) {
		t.Fatalf("window %q", got)
	}
}

// Behaviour kept: with no pinned row, the order asked is applied as is.
func TestFolderWorldReorderUnpinnedUnchanged(t *testing.T) {
	t.Parallel()
	fw := twoFolders([]string{"x1", "x2"}, []string{"y1", "y2"})
	want := []string{"AX", "x2", "x1", "AY", "y1", "y2", "F"}
	if err := fw.Reorder(win, want); err != nil {
		t.Fatal(err)
	}
	if got := fw.windows[win]; !slices.Equal(got, want) {
		t.Fatalf("window %q, want %q", got, want)
	}
}

// P26 b1–b4: a single move after (before) a member of the same group moves
// that workspace only; block sequence and memberships unchanged.
func TestFolderWorldPlaceKeepsBlocks(t *testing.T) {
	t.Parallel()
	fw := twoFolders([]string{"x1", "x2", "x3"}, []string{"y1", "y2", "y3"}, "y1")
	seq := fw.blockSeq(win)
	for _, step := range []struct {
		name, ws, ref string
		before        bool
		want          []string
	}{
		{"b1", "x3", "x1", false, []string{"AX", "x1", "x3", "x2", "AY", "y1", "y2", "y3", "F"}},
		{"b2", "x2", "AX", false, []string{"AX", "x2", "x1", "x3", "AY", "y1", "y2", "y3", "F"}},
		{"b3", "y3", "y1", false, []string{"AX", "x2", "x1", "x3", "AY", "y1", "y3", "y2", "F"}},
		{"b4", "x1", "x2", true, []string{"AX", "x1", "x2", "x3", "AY", "y1", "y3", "y2", "F"}},
	} {
		if err := fw.PlaceWorkspace(win, step.ws, step.ref, step.before); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := fw.windows[win]; !slices.Equal(got, step.want) {
			t.Fatalf("%s: window %q, want %q", step.name, got, step.want)
		}
		if got := fw.blockSeq(win); !slices.Equal(got, seq) {
			t.Fatalf("%s: blocks %q, want %q", step.name, got, seq)
		}
		if fw.groupOf(step.ws).ID != map[byte]string{'x': "GX", 'y': "GY"}[step.ws[0]] {
			t.Fatalf("%s: %s left its group", step.name, step.ws)
		}
	}
	if !fw.ws["y1"].Pinned {
		t.Fatal("y1 unpinned by a move")
	}
	if n := fw.count("place"); n != 4 {
		t.Fatalf("place calls %d, want 4: %q", n, fw.calls)
	}
	if w := fw.folderWrites(); len(w) != 4 {
		t.Fatalf("folder writes %q, want the 4 place", w)
	}
}

// P26 c1, c2: aimed above a pinned member of its group, an unpinned workspace
// lands first of the unpinned members, right under the pinned block.
func TestFolderWorldPlaceAbovePinnedClamped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		ref    string
		before bool
	}{{"c1 before the pinned one", "y1", true}, {"c2 after the anchor", "AY", false}} {
		t.Run(tc.name, func(t *testing.T) {
			fw := twoFolders([]string{"x1", "x2"}, []string{"y1", "y2", "y3"}, "y1")
			seq := fw.blockSeq(win)
			if err := fw.PlaceWorkspace(win, "y3", tc.ref, tc.before); err != nil {
				t.Fatal(err)
			}
			if got := fw.members("Y"); !slices.Equal(got, []string{"y1", "y3", "y2"}) {
				t.Fatalf("Y = %q, want [y1 y3 y2]", got)
			}
			if !fw.ws["y1"].Pinned {
				t.Fatal("y1 unpinned")
			}
			if got := fw.blockSeq(win); !slices.Equal(got, seq) {
				t.Fatalf("blocks %q, want %q (Y still under X)", got, seq)
			}
		})
	}
}

// The engine never asks for a reference outside the workspace's group: the
// fake refuses it loudly, the window unchanged.
func TestFolderWorldPlaceOutsideGroupRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, ws, ref string }{
		{"unknown ref", "x1", "nope"},
		{"ref is ws", "x1", "x1"},
		{"ref in another group", "x1", "y1"},
		{"anchor of another group", "x1", "AY"},
		{"free ref", "x1", "F"},
		{"free workspace", "F", "x1"},
		{"an anchor moved", "AX", "x1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fw := twoFolders([]string{"x1", "x2"}, []string{"y1", "y2"})
			before := slices.Clone(fw.windows[win])
			if err := fw.PlaceWorkspace(win, tc.ws, tc.ref, false); err == nil {
				t.Fatalf("place %s after %s accepted", tc.ws, tc.ref)
			}
			if got := fw.windows[win]; !slices.Equal(got, before) {
				t.Fatalf("window %q, want %q", got, before)
			}
		})
	}
}

// P24 e: a pin lands last of its group's pinned block (the first pin of a
// group: right after the anchor); a free workspace pinned goes to the head of
// the window (P24 control); an unpin leaves the workspace where it is.
func TestFolderWorldPinLandsLastOfPinnedBlock(t *testing.T) {
	t.Parallel()
	pins := map[string]func(fw *folderWorld, ws string){
		"fw.pin": func(fw *folderWorld, ws string) { fw.pin(ws) },
		"SetPinned": func(fw *folderWorld, ws string) {
			if err := fw.SetPinned(ws, true); err != nil {
				panic(err)
			}
		},
	}
	for name, pin := range pins {
		t.Run(name, func(t *testing.T) {
			fw := twoFolders([]string{"x1", "x2", "x3"}, []string{"y1", "y2", "y3", "y4"}, "y1", "y2")
			pin(fw, "y4") // not right under the pinned block: y3 is
			if got := fw.members("Y"); !slices.Equal(got, []string{"y1", "y2", "y4", "y3"}) {
				t.Fatalf("Y = %q, want y4 last of the pinned block [y1 y2 y4 y3]", got)
			}
			pin(fw, "x3") // first pin of X
			if got := fw.members("X"); !slices.Equal(got, []string{"x3", "x1", "x2"}) {
				t.Fatalf("X = %q, want x3 at the head [x3 x1 x2]", got)
			}
			pin(fw, "F") // free
			if got := fw.windows[win]; got[0] != "F" {
				t.Fatalf("window %q, want the pinned free F at its head", got)
			}
			if got := fw.blockSeq(win); !slices.Equal(got, []string{"F", "GX", "GY"}) {
				t.Fatalf("blocks %q", got)
			}
			fw.ws["y1"].Pinned = false
			if err := fw.SetPinned("y2", false); err != nil {
				t.Fatal(err)
			}
			if got := fw.members("Y"); !slices.Equal(got, []string{"y1", "y2", "y4", "y3"}) {
				t.Fatalf("Y = %q after the unpins, want them in place", got)
			}
		})
	}
}

// Known defect, characterised (D40; Trous n°2 of the plan): with the sort on,
// desiredOrder sends a whole FolderReorder, so a folder holding a pinned
// member rises above the folder before it as soon as anything in the window
// is reordered. D39 covers the sort off only; the router decides on this one.
func TestFolderWorldActiveSortPinnedMemberLiftsGroup(t *testing.T) {
	t.Parallel()
	fw := twoFolders([]string{"x1", "x2"}, []string{"y1", "y2"}, "y1")
	in := ArrangeInput{
		Folders: []string{"X", "Y"},
		Order:   slices.Clone(fw.windows[win]),
		Groups:  slices.Clone(fw.groups),
		Target:  map[string]string{"x1": "X", "x2": "X", "y1": "Y", "y2": "Y"},
		Rank:    map[string]int{"x2": 0, "x1": 1, "y2": 2}, // X wants [x2 x1]
		Pinned:  map[string]bool{"y1": true},
	}
	ops := Arrange(in)
	if len(ops) != 1 || ops[0].Kind != FolderReorder {
		t.Fatalf("ops %+v, want one FolderReorder (the sort on)", ops)
	}
	if got := ops[0].Order; !slices.Equal(got, []string{"AX", "x2", "x1", "AY", "y1", "y2", "F"}) {
		t.Fatalf("desired order %q keeps the blocks", got)
	}
	if err := fw.Reorder(win, ops[0].Order); err != nil {
		t.Fatal(err)
	}
	if got, want := fw.blockSeq(win), []string{"GY", "GX", "F"}; !slices.Equal(got, want) {
		t.Fatalf("blocks %q, want %q: the sort on has the same defect, D39 covers the sort off only", got, want)
	}
}

// P26 a, the « Home » group: a pinned group keeps the head of the window
// when the planner sends a pinned member of another group above it.
func TestFolderWorldReorderKeepsPinnedGroupOnTop(t *testing.T) {
	t.Parallel()
	fw := twoFolders([]string{"x1", "x2"}, []string{"y1", "y2"}, "y1")
	fw.add(win, "h1", "h1")
	fw.manualGroup("GH", "Home", "AH", false, "h1")
	fw.group("GH").Pinned = true
	fw.windows[win] = append([]string{"AH", "h1"}, fw.windows[win][:len(fw.windows[win])-2]...)
	if err := fw.Reorder(win, slices.Clone(fw.windows[win])); err != nil {
		t.Fatal(err)
	}
	if got, want := fw.blockSeq(win), []string{"GH", "GY", "GX", "F"}; !slices.Equal(got, want) {
		t.Fatalf("blocks %q, want %q", got, want)
	}
}
