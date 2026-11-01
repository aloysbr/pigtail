package cmuxsync

import (
	"reflect"
	"slices"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/space"
)

// sub_x000000000000030 (D5-A): with NoSort the arrangement files, never sorts.

// An unsorted window (folders out of D6 order, a grave session below a calm
// one): the sort would reorder it (control), NoSort leaves it.
func TestArrangeNoSortNeverReorders(t *testing.T) {
	t.Parallel()
	base := ArrangeInput{Folders: folders,
		Order:  []string{"GS", "S1", "S2", "GM", "M1"},
		Groups: []cmux.Group{g("G-S", "Terrace", "GS", true, "S1", "S2"), g("G-M", "Hedge", "GM", true, "M1")},
		Target: map[string]string{"S1": "Terrace", "S2": "Terrace", "M1": "Hedge"},
		Rank:   map[string]int{"S1": 5, "S2": 1}}
	for _, moves := range []bool{false, true} {
		in := base
		in.GroupMoves = moves
		if ops := Arrange(in); len(ops) == 0 {
			t.Fatalf("control (GroupMoves=%v): the sort must reorder this window", moves)
		}
		in.NoSort = true
		if ops := Arrange(in); ops != nil {
			t.Fatalf("NoSort (GroupMoves=%v): ops = %+v, want none", moves, ops)
		}
	}
}

// D2, D4: a created folder goes right before « Inbox » where it stands
// now; without it, and always for « Standby », after the last block.
func TestArrangeNoSortCreatePlace(t *testing.T) {
	t.Parallel()
	pinned := g("GP", "Mine", "P", false)
	pinned.Pinned = true
	for _, tc := range []struct {
		name   string
		order  []string
		groups []cmux.Group
		target map[string]string
		want   []FolderOp
	}{
		{"before Inbox where it stands", // Ada put « Inbox » on top; D6 would put Hedge before Terrace
			[]string{"GN", "N1", "GS", "S1", "A"},
			[]cmux.Group{g("G-N", "Inbox", "GN", true, "N1"), g("G-S", "Terrace", "GS", true, "S1")},
			map[string]string{"N1": "Inbox", "S1": "Terrace", "A": "Hedge"},
			[]FolderOp{{Kind: FolderCreate, Name: "Hedge", Ref: "G-N", Before: true}}},
		{"no Inbox: after the last block",
			[]string{"GS", "S1", "A"},
			[]cmux.Group{g("G-S", "Terrace", "GS", true, "S1")},
			map[string]string{"S1": "Terrace", "A": "Hedge"},
			[]FolderOp{{Kind: FolderCreate, Name: "Hedge", Ref: "G-S"}}},
		{"Standby after the last block, not after Inbox",
			[]string{"GN", "N1", "GS", "S1", "B"},
			[]cmux.Group{g("G-N", "Inbox", "GN", true, "N1"), g("G-S", "Terrace", "GS", true, "S1")},
			map[string]string{"N1": "Inbox", "S1": "Terrace", "B": "Standby"},
			[]FolderOp{{Kind: FolderCreate, Name: "Standby", Ref: "G-S"}}},
		{"chained at the end in one round",
			[]string{"GS", "S1", "A", "B", "C"},
			[]cmux.Group{g("G-S", "Terrace", "GS", true, "S1")},
			map[string]string{"S1": "Terrace", "A": "Hedge", "B": "Inbox", "C": "Standby"},
			[]FolderOp{{Kind: FolderCreate, Name: "Hedge", Ref: "G-S"},
				{Kind: FolderCreate, Name: "Inbox", RefFolder: "Hedge"},
				{Kind: FolderCreate, Name: "Standby", RefFolder: "Inbox"}}},
		{"a pinned group is a block",
			[]string{"P", "A"}, []cmux.Group{pinned},
			map[string]string{"A": "Inbox"},
			[]FolderOp{{Kind: FolderCreate, Name: "Inbox", Ref: "GP"}}},
		{"no block at all: nowhere (T1)",
			[]string{"A"}, nil,
			map[string]string{"A": "Inbox"},
			[]FolderOp{{Kind: FolderCreate, Name: "Inbox"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := Arrange(ArrangeInput{Folders: folders, Order: tc.order, Groups: tc.groups, Target: tc.target, NoSort: true})
			if !reflect.DeepEqual(ops, tc.want) {
				t.Fatalf("ops = %+v, want %+v", ops, tc.want)
			}
		})
	}
}

// D3: a session dragged into another folder is added back, at the bottom of
// its own; then nothing more.
func TestArrangeNoSortStillFiles(t *testing.T) {
	t.Parallel()
	st := ArrangeInput{Folders: folders, NoSort: true,
		Order:  []string{"GS", "S2", "GM", "M1", "S1"},
		Groups: []cmux.Group{g("G-S", "Terrace", "GS", true, "S2"), g("G-M", "Hedge", "GM", true, "M1", "S1")},
		Target: map[string]string{"S1": "Terrace", "S2": "Terrace", "M1": "Hedge"},
		Rank:   map[string]int{"S1": 1, "S2": 5}}
	ops := Arrange(st)
	if want := []FolderOp{{Kind: FolderAdd, Group: "G-S", Workspace: "S1"}}; !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v, want %+v", ops, want)
	}
	st = simulate(t, st, ops)
	if got := blockOf(st, st.Groups[0]); !slices.Equal(got, []string{"GS", "S2", "S1"}) {
		t.Fatalf("Terrace = %v, want S1 at the bottom despite its gravity", got)
	}
	if ops := Arrange(st); ops != nil {
		t.Fatalf("filed, yet ops = %+v", ops)
	}
}

// H2 at the engine: dissolving a foreign group, filing its members and
// creating « Inbox » never moves the other workspaces, and no round ever
// carries a reorder or a block move.
func TestArrangeNoSortDissolutionKeepsOrder(t *testing.T) {
	t.Parallel()
	st := ArrangeInput{Folders: folders, NoSort: true, GroupMoves: true,
		Order:  []string{"E1", "X", "GS", "S2", "S1"},
		Groups: []cmux.Group{g("F", "Perso", "E1", false, "X"), g("G-S", "Terrace", "GS", true, "S2", "S1")},
		Target: map[string]string{"S1": "Terrace", "S2": "Terrace", "X": "Inbox", "E1": "Inbox"},
		Rank:   map[string]int{"S1": 1, "S2": 5}}
	for round := 0; ; round++ {
		if round > 8 {
			t.Fatal("no convergence in 8 rounds")
		}
		ops := Arrange(st)
		if ops == nil {
			break
		}
		for _, op := range ops {
			if op.Kind == FolderReorder || op.Kind == FolderMove || op.Kind == FolderPlace {
				t.Fatalf("round %d: %+v moves positions", round, op)
			}
		}
		st = simulate(t, st, ops)
	}
	kept := slices.DeleteFunc(slices.Clone(st.Order), func(x string) bool { return !slices.Contains([]string{"GS", "S2", "S1"}, x) })
	if !slices.Equal(kept, []string{"GS", "S2", "S1"}) {
		t.Fatalf("Terrace moved: %v", st.Order)
	}
	if folderOf(st, "X") != "Inbox" || folderOf(st, "E1") != "Inbox" {
		t.Fatalf("not filed: %+v", st.Groups)
	}
}

// D11 (A) of sub_x000000000000030, cmux probe P19: cmux's add keeps the side the
// workspace comes from, so one added from above lands right after the anchor.
// Without the sort, the only reorder is the one that puts those workspaces
// (Enter, pigtail's own adds) back at the bottom of their folder; everything
// else keeps its place.
func TestArrangeNoSortEnterOnly(t *testing.T) {
	t.Parallel()
	pinned := g("GP", "Mine", "P", false, "P1")
	pinned.Pinned = true
	for _, tc := range []struct {
		name  string
		order []string
		enter map[string]bool
		want  []string // nil: no op
	}{
		{"added from above, after the anchor", []string{"GM", "M1", "GS", "S3", "S2", "S1", "F"},
			map[string]bool{"S3": true}, []string{"GM", "M1", "GS", "S2", "S1", "S3", "F"}},
		{"already at the bottom", []string{"GM", "M1", "GS", "S2", "S1", "S3", "F"},
			map[string]bool{"S3": true}, nil},
		{"two tails keep their order, below the others", []string{"GM", "M1", "GS", "S3", "S2", "S1", "F"},
			map[string]bool{"S3": true, "S1": true}, []string{"GM", "M1", "GS", "S2", "S3", "S1", "F"}},
		{"several folders", []string{"GM", "M2", "M1", "GS", "S3", "S2", "S1", "F"},
			map[string]bool{"S3": true, "M2": true}, []string{"GM", "M1", "M2", "GS", "S2", "S1", "S3", "F"}},
		{"a free, pinned or unknown workspace is never moved", []string{"P", "P1", "GM", "M1", "GS", "S2", "S1", "F"},
			map[string]bool{"F": true, "P1": true, "gone": true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gm, gs []string
			for _, ws := range tc.order {
				switch ws[0] {
				case 'M':
					gm = append(gm, ws)
				case 'S':
					gs = append(gs, ws)
				}
			}
			groups := []cmux.Group{g("G-M", "Hedge", "GM", true, gm...), g("G-S", "Terrace", "GS", true, gs...)}
			target := map[string]string{}
			for _, ws := range gm {
				target[ws] = "Hedge"
			}
			for _, ws := range gs {
				target[ws] = "Terrace"
			}
			if tc.order[0] == "P" {
				groups = append(groups, pinned)
			}
			in := ArrangeInput{Folders: folders, NoSort: true, GroupMoves: true, Order: tc.order, Groups: groups,
				Target: target, Rank: map[string]int{"S1": 1, "S2": 5}, Enter: tc.enter}
			ops := Arrange(in)
			if tc.want == nil {
				if ops != nil {
					t.Fatalf("ops = %+v, want none", ops)
				}
				return
			}
			got := applyPlaces(t, in.Order, ops) // D39: single moves only
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ops = %+v give %v, want %v", ops, got, tc.want)
			}
			for _, op := range ops {
				if !tc.enter[op.Workspace] {
					t.Fatalf("%+v moves a tab that does not enter", op)
				}
			}
			if ops := Arrange(simulate(t, in, ops)); ops != nil {
				t.Fatalf("after the reorder, ops = %+v", ops)
			}
		})
	}
}

// D13 over D11: a workspace the user pinned right after pigtail added it is
// never an entering tab to move, even still listed in Enter.
func TestArrangeNoSortPinnedEnterNotMoved(t *testing.T) {
	t.Parallel()
	in := ArrangeInput{Folders: folders, NoSort: true, GroupMoves: true,
		Order:  []string{"GS", "S3", "S2", "S1"},
		Groups: []cmux.Group{g("G-S", "Terrace", "GS", true, "S3", "S2", "S1")},
		Target: map[string]string{"S1": "Terrace", "S2": "Terrace", "S3": "Terrace"},
		Pinned: map[string]bool{"S3": true},
		Enter:  map[string]bool{"S3": true}}
	if ops := Arrange(in); ops != nil {
		t.Fatalf("a pinned tail was moved: %+v", ops)
	}
}

// No « Prio » folder any more (D3 of sub_x000000000000014: the prio is cmux's
// own pin): whatever the listing — a new window, pinned groups, own folders, a
// group of the user's named « Prio » — no round creates a « Prio » folder,
// sorted or not.
func pinnedG(id, anchor string) cmux.Group {
	gr := g(id, "Mine "+id, anchor, false)
	gr.Pinned = true
	return gr
}

func TestNoFolderCreatePrioWhateverTheListing(t *testing.T) {
	t.Parallel()
	folders := []string{offsite, "Hedge", "Terrace", "Inbox", "Standby"}
	cases := map[string]struct {
		order  []string
		groups []cmux.Group
		target map[string]string
		pinned map[string]bool
	}{
		"new window": {[]string{"A", "B", "C"}, nil,
			map[string]string{"A": "Hedge", "B": "Inbox", "C": "Standby"}, map[string]bool{"A": true}},
		"pinned groups": {[]string{"P", "Q", "A", "B"}, []cmux.Group{pinnedG("GP", "P"), pinnedG("GQ", "Q")},
			map[string]string{"A": "Hedge", "B": "Inbox"}, nil},
		"own folders": {[]string{"GM", "M1", "GS", "S1", "A"},
			[]cmux.Group{g("G-M", "Hedge", "GM", true, "M1"), g("G-S", "Terrace", "GS", true, "S1")},
			map[string]string{"M1": "Hedge", "S1": "Terrace", "A": offsite}, map[string]bool{"A": true}},
		"hand group named Prio": {[]string{"GX", "X1", "A"}, []cmux.Group{g("G-X", space.Prio, "GX", false, "X1")},
			map[string]string{"X1": "Hedge", "A": "Inbox"}, nil},
	}
	for name, c := range cases {
		for _, noSort := range []bool{true, false} {
			ops := Arrange(ArrangeInput{Folders: folders, Order: c.order, Groups: c.groups, Target: c.target, Pinned: c.pinned, NoSort: noSort})
			for _, op := range ops {
				if op.Kind == FolderCreate && op.Name == space.Prio {
					t.Errorf("%s (noSort=%v): a « Prio » folder created: %+v", name, noSort, ops)
				}
			}
		}
	}
}
