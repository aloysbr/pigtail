package cmuxsync

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/space"
)

var folders = []string{"Hedge", "Terrace", "Inbox", "Standby"}

func g(id, name, anchor string, gen bool, members ...string) cmux.Group {
	return cmux.Group{ID: id, Name: name, AnchorID: anchor, AnchorGenerated: gen, Members: append([]string{anchor}, members...)}
}

func TestArrangeCreatesMissingFolderFirst(t *testing.T) {
	t.Parallel()
	ops := Arrange(ArrangeInput{Folders: folders, Order: []string{"A", "S"},
		Target: map[string]string{"A": "Terrace", "S": "Standby"}, Collapse: map[string]bool{"Standby": true},
		Icons: map[string]string{"Terrace": "house.fill", "Standby": "moon.zzz"}})
	want := []FolderOp{{Kind: FolderCreate, Name: "Terrace", Icon: "house.fill"}, {Kind: FolderCreate, Name: "Standby", Icon: "moon.zzz", Collapse: true, RefFolder: "Terrace"}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v", ops)
	}
}

// D6: a created folder carries its fixed place (cmux creates at the top,
// cmux probe P11): after its closest existing predecessor, else before the first
// existing own folder, else after the last pinned group.
func TestArrangeCreatePlacesFolder(t *testing.T) {
	t.Parallel()
	order := []string{"P", "GS", "S1", "GB", "A", "B"}
	pinned := g("GP", "Mine", "P", false)
	pinned.Pinned = true
	for _, tc := range []struct {
		name   string
		groups []cmux.Group
		target map[string]string
		want   FolderOp
	}{
		{"after existing predecessor", []cmux.Group{pinned, g("G-S", "Terrace", "GS", true, "S1")},
			map[string]string{"S1": "Terrace", "A": "Inbox"},
			FolderOp{Kind: FolderCreate, Name: "Inbox", Ref: "G-S"}},
		{"first: before the next own folder", []cmux.Group{pinned, g("G-S", "Terrace", "GS", true, "S1")},
			map[string]string{"S1": "Terrace", "A": "Hedge"},
			FolderOp{Kind: FolderCreate, Name: "Hedge", Ref: "G-S", Before: true}},
		{"alone: after the last pinned group", []cmux.Group{pinned},
			map[string]string{"A": "Inbox"},
			FolderOp{Kind: FolderCreate, Name: "Inbox", Ref: "GP"}},
		{"alone, nothing pinned: nowhere", nil,
			map[string]string{"A": "Inbox"},
			FolderOp{Kind: FolderCreate, Name: "Inbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := Arrange(ArrangeInput{Folders: folders, Order: order, Groups: tc.groups, Target: tc.target})
			if len(ops) != 1 || !reflect.DeepEqual(ops[0], tc.want) {
				t.Fatalf("ops = %+v, want %+v", ops, tc.want)
			}
		})
	}
}

func TestArrangeFreesFiledAnchorFirst(t *testing.T) {
	t.Parallel()
	ops := Arrange(ArrangeInput{Folders: folders, Order: []string{"A", "B"},
		Groups: []cmux.Group{g("G1", "Perso", "A", false, "B")}, Target: map[string]string{"A": "Terrace", "B": "Terrace"}})
	if !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderDissolve, Group: "G1"}}) {
		t.Fatalf("ops = %+v", ops)
	}
}

// An anchor-only group in the shape cmux really lists it (Task 3 P1 fixture, F1 or
// F2): R4's `len(Members) == 1` holds because cmux.ParseGroups normalizes.
func TestArrangeAnchorOnlyGroupFromProbeFixture(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../cmux/testdata/workspace-group-list-anchor-only.json")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := cmux.ParseGroups(raw)
	if err != nil {
		t.Fatal(err)
	}
	var gr cmux.Group
	for _, c := range gs {
		if c.Name == "PT Folder" {
			gr = c
		}
	}
	if gr.ID == "" {
		t.Fatal("fixture has no PT Folder group")
	}
	in := ArrangeInput{Folders: folders, Order: []string{gr.AnchorID}, Groups: []cmux.Group{gr},
		CloseEmptyGeneratedAnchors: true, Closable: map[string]bool{gr.AnchorID: true}}
	if ops := Arrange(in); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderDissolve, Group: gr.ID, RemoveAnchor: true}}) {
		t.Fatalf("foreign anchor-only group, bare anchor: ops = %+v", ops)
	}
	in.Groups[0].Name = "Terrace" // the same shape as a clean empty folder: deleted
	if ops := Arrange(in); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderDelete, Group: gr.ID}}) {
		t.Fatalf("own anchor-only folder, bare anchor: ops = %+v", ops)
	}
}

func TestArrangeSetsFolderIconNeverColor(t *testing.T) { // D10, D11
	t.Parallel()
	in := ArrangeInput{Folders: folders, Order: []string{"GP", "A"},
		Groups: []cmux.Group{g("P", "Terrace", "GP", true, "A")},
		Target: map[string]string{"A": "Terrace"}, Icons: map[string]string{"Terrace": "house.fill"}}
	if ops := Arrange(in); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderSetIcon, Group: "P", Icon: "house.fill"}}) {
		t.Fatalf("ops = %+v", ops)
	}
	in.Groups[0].Icon = "house.fill"
	if ops := Arrange(in); ops != nil {
		t.Fatalf("icon already set: %+v", ops)
	}
}

// D9: a group the user makes after the rollout (cmux generates its anchor) is
// dissolved and its session goes back to its folder. Its generated anchor,
// even bare, goes only when proved bare (D18 (A), CloseEmptyGeneratedAnchors);
// otherwise (D7) it stays, loose, for the runner to file in « Inbox ».
func TestArrangeDissolvesGroupMadeAfterRollout(t *testing.T) {
	t.Parallel()
	for _, closeAnchors := range []bool{false, true} { // D18 (A): engine keeps both values of the constant
		st := ArrangeInput{Folders: folders, Order: []string{"GP", "B", "GA", "A"},
			Groups:   []cmux.Group{g("P", "Terrace", "GP", true, "B"), g("Mine", "Mine", "GA", true, "A")},
			Target:   map[string]string{"A": "Terrace", "B": "Terrace"},
			Closable: map[string]bool{"GA": true}, CloseEmptyGeneratedAnchors: closeAnchors}
		for round := 0; Arrange(st) != nil; round++ {
			if round > 4 {
				t.Fatal("no convergence")
			}
			st = simulate(t, st, Arrange(st))
		}
		if folderOf(st, "A") != "Terrace" || len(st.Groups) != 1 {
			t.Fatalf("D18=%v: manual group kept: %+v", closeAnchors, st)
		}
		if kept := slices.Contains(st.Order, "GA"); kept == closeAnchors {
			t.Fatalf("D18=%v: generated anchor GA kept=%v", closeAnchors, kept)
		}
	}
}

// Spec 3: a generated anchor not proved bare (the user opened it and ran claude,
// bound it, split it, runs a process) is never closed: an empty own folder is
// dissolved instead, its anchor kept. D18 (A): a dissolved foreign
// group's generated anchor goes only when CloseEmptyGeneratedAnchors AND bare.
func TestArrangeKeepsAnchorNotClosable(t *testing.T) {
	t.Parallel()
	in := ArrangeInput{Folders: folders, Order: []string{"GM", "GX"},
		Groups: []cmux.Group{g("M", "Hedge", "GM", true), g("X", "Mine", "GX", true)}}
	want := []FolderOp{{Kind: FolderDissolve, Group: "X"}, {Kind: FolderDissolve, Group: "M"}}
	if ops := Arrange(in); !reflect.DeepEqual(ops, want) {
		t.Fatalf("not closable: ops = %+v, want %+v", ops, want)
	}
	in.CloseEmptyGeneratedAnchors = true // D18 on, anchors still not proved bare
	if ops := Arrange(in); !reflect.DeepEqual(ops, want) {
		t.Fatalf("D18 on, not closable: ops = %+v", ops)
	}
	in.Closable = map[string]bool{"GM": true, "GX": true}
	want = []FolderOp{{Kind: FolderDissolve, Group: "X", RemoveAnchor: true}, {Kind: FolderDelete, Group: "M"}}
	if ops := Arrange(in); !reflect.DeepEqual(ops, want) {
		t.Fatalf("D18 on, bare: ops = %+v", ops)
	}
	in.CloseEmptyGeneratedAnchors = false // D18 off: the dissolved group's anchor stays (D7)
	want = []FolderOp{{Kind: FolderDissolve, Group: "X"}, {Kind: FolderDelete, Group: "M"}}
	if ops := Arrange(in); !reflect.DeepEqual(ops, want) {
		t.Fatalf("D18 off, bare: ops = %+v", ops)
	}
}

func TestArrangeRemovesBeforeAddWhenAddDoesNotMove(t *testing.T) { // P2 (b)
	t.Parallel()
	st := ArrangeInput{Folders: folders, Order: []string{"GM", "A", "GX", "B"},
		Groups: []cmux.Group{g("M", "Hedge", "GM", true), g("X", "Mine", "GX", false, "B")},
		Target: map[string]string{"A": "Hedge", "B": "Hedge"}} // A is in no group, B in the foreign group X
	if ops, want := Arrange(st), []FolderOp{{Kind: FolderAdd, Group: "M", Workspace: "A"}, {Kind: FolderAdd, Group: "M", Workspace: "B"}}; !reflect.DeepEqual(ops, want) {
		t.Fatalf("P2 (a): ops = %+v, want %+v", ops, want)
	}
	st.AddNeedsRemove = true
	want := []FolderOp{{Kind: FolderAdd, Group: "M", Workspace: "A"}, {Kind: FolderRemove, Workspace: "B"}, {Kind: FolderAdd, Group: "M", Workspace: "B"}}
	if ops := Arrange(st); !reflect.DeepEqual(ops, want) { // never a remove for A: it has no group
		t.Fatalf("P2 (b): ops = %+v, want %+v", ops, want)
	}
}

func TestArrangeDissolvesFolderWhoseAnchorHostsSession(t *testing.T) { // the user lance claude dans l'ancre d'un dossier propre, membres encore dedans
	t.Parallel()
	st := ArrangeInput{Folders: folders, Order: []string{"GM", "A", "B"},
		Groups:      []cmux.Group{g("M", "Hedge", "GM", true, "A", "B")},
		Target:      map[string]string{"GM": "Terrace", "A": "Hedge", "B": "Hedge"}, // GM héberge une session : classée comme un workspace
		AnchorHosts: map[string]bool{"GM": true},
		Closable:    map[string]bool{"GM": true}, CloseEmptyGeneratedAnchors: true} // même « fermable » : jamais fermée
	ops := Arrange(st)
	if len(ops) == 0 || ops[0].Kind != FolderDissolve || ops[0].Group != "M" || ops[0].RemoveAnchor {
		t.Fatalf("a folder whose anchor hosts a session is foreign: dissolve, anchor kept: %+v", ops)
	}
	for round := 0; ; round++ {
		if round > 8 {
			t.Fatal("no convergence in 8 rounds")
		}
		ops := Arrange(st)
		if ops == nil {
			break
		}
		for _, op := range ops {
			if op.RemoveAnchor || op.Kind == FolderDelete {
				t.Fatalf("the anchor hosts a session, never closed: %+v", op)
			}
		}
		st = simulate(t, st, ops)
	}
	if !slices.Contains(st.Order, "GM") || folderOf(st, "GM") != "Terrace" {
		t.Errorf("the session stays open, filed in its space: %q", folderOf(st, "GM"))
	}
	for _, ws := range []string{"A", "B"} {
		if folderOf(st, ws) != "Hedge" {
			t.Errorf("%s in %q, want Hedge", ws, folderOf(st, ws))
		}
	}
	for _, gr := range st.Groups {
		if gr.Name == "Hedge" && gr.AnchorID == "GM" {
			t.Errorf("« Hedge » keeps the session as its header")
		}
	}
}

// A manual group already named like one of pigtail's folders, its anchor not
// generated, stays foreign: the real folder is created, and the manual group
// is dissolved once its member has moved (D9). Its anchor is never closed.
func TestArrangeManualFolderNameIsForeign(t *testing.T) { // Review Focus 5
	t.Parallel()
	for _, name := range []string{"Standby", "Inbox"} {
		t.Run(name, func(t *testing.T) {
			st := ArrangeInput{Folders: folders, Order: []string{"E", "X"},
				Groups: []cmux.Group{g("G1", name, "E", false, "X")}, Target: map[string]string{"X": name},
				CloseEmptyGeneratedAnchors: true, Closable: map[string]bool{"E": true}}
			ops := Arrange(st)
			if len(ops) != 1 || ops[0].Kind != FolderCreate || ops[0].Name != name {
				t.Fatalf("a manual « %s » must not be adopted: %+v", name, ops)
			}
			dissolved := false
			for round := 0; ops != nil; round++ {
				if round > 8 {
					t.Fatal("no convergence")
				}
				for _, op := range ops {
					if op.Group == "G1" && op.Kind == FolderDissolve {
						dissolved = true
						if op.RemoveAnchor {
							t.Fatalf("the manual anchor E is not generated, never closed: %+v", op)
						}
					}
					if op.Group == "G1" && (op.Kind == FolderAdd || op.Kind == FolderSetIcon || op.Kind == FolderDelete) {
						t.Fatalf("the manual « %s » is treated as pigtail's own: %+v", name, op)
					}
				}
				st = simulate(t, st, ops)
				ops = Arrange(st)
			}
			if !dissolved || slices.ContainsFunc(st.Groups, func(gr cmux.Group) bool { return gr.ID == "G1" }) {
				t.Fatalf("the manual « %s » group is not dissolved: %+v", name, st.Groups)
			}
			if got := folderOf(st, "X"); got != name {
				t.Fatalf("X in %q, want the real « %s »", got, name)
			}
			for _, gr := range st.Groups {
				if gr.Name == name && (!gr.AnchorGenerated || gr.AnchorID == "E") {
					t.Fatalf("the real « %s » must have a generated anchor: %+v", name, gr)
				}
			}
			if !slices.Contains(st.Order, "E") {
				t.Fatalf("the manual anchor E was closed: %q", st.Order)
			}
		})
	}
}

func TestArrangeOrderD6GravityStableStandbyUnsorted(t *testing.T) {
	t.Parallel()
	in := ArrangeInput{Folders: folders,
		Order:    []string{"GS", "Y", "X", "GM", "A", "B", "C", "GN", "N"},
		Groups:   []cmux.Group{g("S", "Standby", "GS", true, "Y", "X"), g("M", "Hedge", "GM", true, "A", "B", "C"), g("N", "Inbox", "GN", true, "N")},
		Target:   map[string]string{"A": "Hedge", "B": "Hedge", "C": "Hedge", "N": "Inbox", "X": "Standby", "Y": "Standby"},
		Rank:     map[string]int{"A": 10, "B": 0, "C": 10, "X": 0, "Y": 15},
		Unsorted: map[string]bool{"Standby": true}}
	want := []string{"GM", "B", "A", "C", "GN", "N", "GS", "Y", "X"}
	ops := Arrange(in)
	if len(ops) != 1 || ops[0].Kind != FolderReorder || !reflect.DeepEqual(ops[0].Order, want) {
		t.Fatalf("ops = %+v, want reorder %q", ops, want)
	}
}

func TestArrangeNoOpWhenSorted(t *testing.T) { // pas de permutation sans changement d'attente
	t.Parallel()
	in := ArrangeInput{Folders: folders, Order: []string{"GM", "A", "C"},
		Groups: []cmux.Group{g("M", "Hedge", "GM", true, "A", "C")},
		Target: map[string]string{"A": "Hedge", "C": "Hedge"}, Rank: map[string]int{"A": 10, "C": 10}}
	if ops := Arrange(in); ops != nil {
		t.Fatalf("equal gravity, already ordered: %+v", ops)
	}
}

// Spec 2: a folder left with its generated anchor only goes, in a round of
// its own, and nothing else is closed (its anchor proved bare, spec 3).
func TestArrangeDeletesEmptyFolderAndPromotedAnchor(t *testing.T) {
	t.Parallel()
	st := ArrangeInput{Folders: folders, Order: []string{"GM", "P", "A"},
		Groups:   []cmux.Group{g("M", "Hedge", "GM", true), g("P2", "Terrace", "P", false, "A")},
		Target:   map[string]string{"P": "Terrace", "A": "Terrace"},
		Closable: map[string]bool{"GM": true}} // proved bare by the runner (spec 3)
	ops := Arrange(st)
	if !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderDissolve, Group: "P2"}}) { // R1: P (a session) anchors it
		t.Fatalf("ops = %+v", ops)
	}
	var deleteRounds [][]FolderOp
	for round := 0; ops != nil; round++ {
		if round > 6 {
			t.Fatal("no convergence")
		}
		if slices.ContainsFunc(ops, func(o FolderOp) bool { return o.Kind == FolderDelete }) {
			deleteRounds = append(deleteRounds, ops)
		}
		st = simulate(t, st, ops)
		ops = Arrange(st)
	}
	if !reflect.DeepEqual(deleteRounds, [][]FolderOp{{{Kind: FolderDelete, Group: "M"}}}) {
		t.Fatalf("delete rounds = %+v, want one round {FolderDelete M} and nothing else", deleteRounds)
	}
	if slices.Contains(st.Order, "GM") || !slices.Contains(st.Order, "P") || !slices.Contains(st.Order, "A") {
		t.Fatalf("generated anchor GM must be closed, P and A kept: %q", st.Order)
	}
}

// P4b: reorder-workspaces leaves group blocks in place, so own folders reach
// the D6 order by FolderMove first, then their members are sorted in place.
func TestArrangeMovesFoldersWhenReorderKeepsBlocks(t *testing.T) {
	t.Parallel()
	st := ArrangeInput{Folders: folders, GroupMoves: true,
		Order:    []string{"GN", "N", "GS", "X", "GM", "B", "A"},
		Groups:   []cmux.Group{g("N", "Inbox", "GN", true, "N"), g("S", "Standby", "GS", true, "X"), g("M", "Hedge", "GM", true, "B", "A")},
		Target:   map[string]string{"A": "Hedge", "B": "Hedge", "N": "Inbox", "X": "Standby"},
		Rank:     map[string]int{"A": 0, "B": 10},
		Unsorted: map[string]bool{"Standby": true}}
	if ops := Arrange(st); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderMove, Group: "M", Ref: "N", Before: true}}) {
		t.Fatalf("first round = %+v, want Hedge moved before Inbox", ops)
	}
	for round := 0; Arrange(st) != nil; round++ {
		if round > 4 {
			t.Fatal("no convergence: reorder alone cannot move blocks in P4b")
		}
		st = simulate(t, st, Arrange(st))
	}
	if want := []string{"GM", "A", "B", "GN", "N", "GS", "X"}; !slices.Equal(st.Order, want) {
		t.Fatalf("order = %q, want %q", st.Order, want)
	}
	st.GroupMoves = false // P4a: one reorder, never a move
	st.Order = []string{"GN", "N", "GM", "A", "B", "GS", "X"}
	if ops := Arrange(st); len(ops) != 1 || ops[0].Kind != FolderReorder {
		t.Fatalf("P4a ops = %+v", ops)
	}
}

func TestArrangeSkipsRoundWhenAnchorMissingFromOrder(t *testing.T) { // two reads, one stale: no panic in folderMoves
	t.Parallel()
	pg := g("PG", "Épinglé", "GPG", false, "P1")
	pg.Pinned = true
	in := ArrangeInput{Folders: folders, GroupMoves: true, Order: []string{"GM", "A"}, // GPG absent from Order
		Groups: []cmux.Group{pg, g("M", "Hedge", "GM", true, "A")},
		Target: map[string]string{"A": "Hedge"}}
	if ops := Arrange(in); ops != nil {
		t.Fatalf("pinned anchor missing: ops = %+v, want nil", ops)
	}
	in.Groups = []cmux.Group{g("M", "Hedge", "GM", true, "A")}
	in.Order = []string{"A"} // own folder anchor GM absent
	if ops := Arrange(in); ops != nil {
		t.Fatalf("folder anchor missing: ops = %+v, want nil", ops)
	}
}

func TestArrangePinnedGroupStaysOutOfFoldersAndD6(t *testing.T) { // D13
	t.Parallel()
	pg := g("PG", "Épinglé", "GPG", false, "P1")
	pg.Pinned = true
	in := ArrangeInput{Folders: folders, Order: []string{"GPG", "P1", "GM", "A"},
		Groups: []cmux.Group{pg, g("M", "Hedge", "GM", true, "A")},
		Target: map[string]string{"A": "Hedge"}}
	if ops := Arrange(in); ops != nil { // not dissolved (D9 spares it), not reordered
		t.Fatalf("ops = %+v", ops)
	}
	in.Order = []string{"GM", "A", "GPG", "P1"} // pinned group drifted below the folders: back on top, unsorted
	want := []FolderOp{{Kind: FolderReorder, Order: []string{"GPG", "P1", "GM", "A"}}}
	if ops := Arrange(in); !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v", ops)
	}
}

// D13 + cmux probe P10: cmux keeps a pinned workspace a member of its group
// (P10a) and `reorder-workspaces` hands it back to its block when asked to put
// it on top. So a pinned member of an own folder keeps its slot inside the
// block, unsorted, and is never emitted on top (that reorder would be undone
// by cmux at every round); the other members are sorted around it, and no id
// is ever emitted twice (Reorder refuses a duplicate).
func TestArrangePinnedWorkspaceInsideOwnFolder(t *testing.T) {
	t.Parallel()
	st := ArrangeInput{Folders: folders, Order: []string{"GM", "A", "P", "B"},
		Groups: []cmux.Group{g("M", "Hedge", "GM", true, "A", "P", "B")}, // P pinned, still a member of the folder
		Pinned: map[string]bool{"P": true},
		Target: map[string]string{"A": "Hedge", "B": "Hedge"}, // P never filed (D13)
		Rank:   map[string]int{"A": 10, "B": 0}}
	want := []FolderOp{{Kind: FolderReorder, Order: []string{"GM", "B", "P", "A"}}} // P keeps its slot, B and A sorted around it
	ops := Arrange(st)
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v, want %+v", ops, want)
	}
	seen := map[string]bool{}
	for _, id := range ops[0].Order {
		if seen[id] {
			t.Fatalf("duplicate %q in %v", id, ops[0].Order)
		}
		seen[id] = true
	}
	for round := 0; ; round++ { // converges: never asks cmux to lift P out of its block
		if round > 4 {
			t.Fatal("no convergence")
		}
		ops := Arrange(st)
		if ops == nil {
			break
		}
		for _, op := range ops { // never extracted from or regrouped by pigtail
			if op.Kind == FolderAdd || op.Kind == FolderDissolve || op.Kind == FolderRemove {
				t.Fatalf("pinned member touched: %+v", op)
			}
		}
		st = simulate(t, st, ops)
	}
	if folderOf(st, "P") != "Hedge" || slices.Index(st.Order, "P") != 2 {
		t.Fatalf("P left its slot: order %q", st.Order)
	}
	loose := st // a pinned workspace in no group still goes on top, unsorted
	loose.Groups = []cmux.Group{g("M", "Hedge", "GM", true, "A", "B")}
	loose.Order = []string{"GM", "A", "B", "P"}
	loose.Rank = map[string]int{"A": 0, "B": 10}
	if ops := Arrange(loose); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderReorder, Order: []string{"P", "GM", "A", "B"}}}) {
		t.Fatalf("loose pinned: ops = %+v", ops)
	}
}

// D21 B (cmux probe P2b/P2c): only the add of the window's selected workspace
// expands a collapsed group. So the arrangement never adds the selected
// workspace to a collapsed folder, nor creates a folder born collapsed for it
// alone; it leaves it where it is and files it at a later round, once it is
// no longer selected. Never a create/delete churn meanwhile.
func TestArrangeDefersSelectedIntoCollapsedFolder(t *testing.T) {
	t.Parallel()
	m := g("M", "Hedge", "GM", true, "B")
	m.Collapsed = true
	in := ArrangeInput{Folders: folders, Order: []string{"GM", "B", "A"},
		Groups: []cmux.Group{m}, Target: map[string]string{"A": "Hedge", "B": "Hedge"}, Selected: "A"}
	if ops := Arrange(in); ops != nil {
		t.Fatalf("selected A into collapsed Hedge: ops = %+v, want nil", ops)
	}
	in.Selected = "X" // A no longer selected: filed
	if ops := Arrange(in); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderAdd, Group: "M", Workspace: "A"}}) {
		t.Fatalf("A not selected: ops = %+v", ops)
	}
	in.Selected, in.Groups[0].Collapsed = "A", false // expanded folder: the add expands nothing
	if ops := Arrange(in); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderAdd, Group: "M", Workspace: "A"}}) {
		t.Fatalf("expanded folder: ops = %+v", ops)
	}

	// A folder that would be born collapsed, wanted by the selected workspace
	// only: not created. An empty collapsed folder it wants: deleted (bare
	// anchor), never recreated while A stays selected.
	e := g("S", "Terrace", "GS", true)
	e.Collapsed = true
	st := ArrangeInput{Folders: folders, Order: []string{"GS", "A", "C"}, Groups: []cmux.Group{e},
		Target: map[string]string{"A": "Terrace", "C": "Hedge"}, Collapse: map[string]bool{"Terrace": true, "Hedge": true},
		Closable: map[string]bool{"GS": true}, Selected: "A"}
	converge := func(st ArrangeInput) ArrangeInput {
		t.Helper()
		for round := 0; ; round++ {
			if round > 6 {
				t.Fatal("no convergence: create/delete churn")
			}
			ops := Arrange(st)
			if ops == nil {
				return st
			}
			for _, op := range ops {
				if op.Kind == FolderAdd && op.Workspace == st.Selected {
					t.Fatalf("selected %s added to a collapsed folder: %+v", st.Selected, op)
				}
			}
			st = simulate(t, st, ops)
		}
	}
	st = converge(st)
	if folderOf(st, "A") != "" || folderOf(st, "C") != "Hedge" {
		t.Fatalf("A (selected) in %q, C in %q; want A loose, C in Hedge", folderOf(st, "A"), folderOf(st, "C"))
	}
	st.Selected = "C" // A no longer selected: Terrace born collapsed, A filed, nothing expanded
	st = converge(st)
	if folderOf(st, "A") != "Terrace" {
		t.Fatalf("A in %q, want Terrace", folderOf(st, "A"))
	}
	for _, gr := range st.Groups {
		if !gr.Collapsed {
			t.Errorf("folder %q expanded", gr.Name)
		}
	}
}

// The state of 2026-10-09 in miniature: manual groups with empty-terminal
// anchors (not generated), a session anchor, a loose workspace. Every round
// applied by simulate must converge, and no workspace may disappear but a
// generated anchor.
func TestArrangeRolloutConvergesClosingNoSession(t *testing.T) {
	t.Parallel()
	st := ArrangeInput{Folders: folders,
		Order:  []string{"S1", "S2", "E1", "S3", "E2", "S4", "L"},
		Groups: []cmux.Group{g("Perso", "Perso", "S1", false, "S2"), g("Hotfix", "Hotfix", "E1", false, "S3"), g("Hold", "On hold", "E2", false, "S4")},
		Target: map[string]string{"S1": "Terrace", "S2": "Inbox", "S3": "Hedge", "S4": "Standby", "L": "Inbox",
			"E1": "Inbox", "E2": "Inbox"}, // D7: bare terminals, never closed
		Collapse: map[string]bool{"Standby": true}, Unsorted: map[string]bool{"Standby": true}}
	sessions := []string{"S1", "S2", "S3", "S4", "L", "E1", "E2"}
	for round := 0; ; round++ {
		if round > 8 {
			t.Fatal("no convergence in 8 rounds")
		}
		ops := Arrange(st)
		if ops == nil {
			break
		}
		st = simulate(t, st, ops)
	}
	for _, s := range sessions {
		if !slices.Contains(st.Order, s) {
			t.Errorf("%s disappeared", s)
		}
	}
	for ws, f := range st.Target {
		if got := folderOf(st, ws); got != f {
			t.Errorf("%s in %q, want %q", ws, got, f)
		}
	}
	for _, gr := range st.Groups {
		if !gr.AnchorGenerated {
			t.Errorf("group %q kept a non-generated anchor", gr.Name)
		}
	}
}

// simulate applies one round to the state as cmux 0.64.24 does (cmux probe notes):
// create appends a generated anchor; add moves the workspace out of its group
// (P2 (a)) to the end of the target block, and expands a collapsed target when
// the workspace is the selected one (P2b); reorder moves blocks and members
// (P4a) or, with GroupMoves (P4b), only members and loose workspaces; move
// places a whole block.
func simulate(t *testing.T, in ArrangeInput, ops []FolderOp) ArrangeInput {
	t.Helper()
	st := in
	st.Order = slices.Clone(in.Order)
	st.Groups = make([]cmux.Group, len(in.Groups))
	for i, gr := range in.Groups {
		gr.Members = slices.Clone(gr.Members)
		st.Groups[i] = gr
	}
	find := func(id string) int {
		return slices.IndexFunc(st.Groups, func(gr cmux.Group) bool { return gr.ID == id })
	}
	groupOf := func(ws string) int {
		return slices.IndexFunc(st.Groups, func(gr cmux.Group) bool { return slices.Contains(gr.Members, ws) })
	}
	drop := func(ids ...string) {
		st.Order = slices.DeleteFunc(st.Order, func(x string) bool { return slices.Contains(ids, x) })
	}
	for _, op := range ops {
		switch op.Kind {
		case FolderCreate:
			anchor := "GEN-" + op.Name
			st.Groups = append(st.Groups, cmux.Group{ID: "G-" + op.Name, Name: op.Name, AnchorID: anchor, AnchorGenerated: true,
				Members: []string{anchor}, Collapsed: op.Collapse, Icon: op.Icon})
			st.Order = append(st.Order, anchor)
		case FolderAdd:
			gi := find(op.Group)
			if gi < 0 {
				t.Fatalf("add to unknown group: %+v", op)
			}
			if from := groupOf(op.Workspace); from >= 0 {
				if st.AddNeedsRemove {
					t.Fatalf("P2 (b): %s still a member of %s", op.Workspace, st.Groups[from].ID)
				}
				if st.Groups[from].AnchorID == op.Workspace {
					t.Fatalf("P3: cmux refuses to add the anchor %s of another group", op.Workspace)
				}
				st.Groups[from].Members = slices.DeleteFunc(st.Groups[from].Members, func(x string) bool { return x == op.Workspace })
			}
			gr := &st.Groups[gi]
			last := gr.Members[len(gr.Members)-1]
			gr.Members = append(gr.Members, op.Workspace)
			if gr.Collapsed && op.Workspace == st.Selected {
				gr.Collapsed = false // P2b
			}
			drop(op.Workspace)
			st.Order = slices.Insert(st.Order, slices.Index(st.Order, last)+1, op.Workspace)
		case FolderRemove:
			if from := groupOf(op.Workspace); from >= 0 {
				st.Groups[from].Members = slices.DeleteFunc(st.Groups[from].Members, func(x string) bool { return x == op.Workspace })
			}
		case FolderDissolve, FolderDelete:
			gi := find(op.Group)
			if gi < 0 {
				t.Fatalf("unknown group: %+v", op)
			}
			if op.Kind == FolderDelete || op.RemoveAnchor {
				drop(st.Groups[gi].AnchorID)
			}
			st.Groups = slices.Delete(st.Groups, gi, gi+1)
		case FolderSetIcon:
			st.Groups[find(op.Group)].Icon = op.Icon
		case FolderReorder:
			st.Order = reorderAs(st, op.Order)
		case FolderPlace: // D39, cmux probe P26 b: one workspace, within its group
			gi := groupOf(op.Workspace)
			if gi < 0 || gi != groupOf(op.Ref) || op.Workspace == op.Ref {
				t.Fatalf("place outside its group: %+v", op)
			}
			drop(op.Workspace)
			at := slices.Index(st.Order, op.Ref)
			if !op.Before {
				at++
			}
			st.Order = slices.Insert(st.Order, at, op.Workspace)
		case FolderMove:
			gi, ri := find(op.Group), find(op.Ref)
			if gi < 0 || ri < 0 {
				t.Fatalf("move of unknown group: %+v", op)
			}
			block := blockOf(st, st.Groups[gi])
			drop(block...)
			ref := blockOf(st, st.Groups[ri])
			at := slices.Index(st.Order, ref[0])
			if !op.Before {
				at = slices.Index(st.Order, ref[len(ref)-1]) + 1
			}
			st.Order = slices.Insert(st.Order, at, block...)
		default:
			t.Fatalf("unknown op %+v", op)
		}
	}
	for i := range st.Groups { // members follow the display order, anchor first
		gr := &st.Groups[i]
		rest := slices.DeleteFunc(slices.Clone(gr.Members), func(x string) bool { return x == gr.AnchorID })
		slices.SortStableFunc(rest, func(a, b string) int { return slices.Index(st.Order, a) - slices.Index(st.Order, b) })
		gr.Members = append([]string{gr.AnchorID}, rest...)
	}
	return st
}

// blockOf is a group's workspaces in display order, anchor first.
func blockOf(st ArrangeInput, gr cmux.Group) []string {
	var out []string
	for _, ws := range st.Order {
		if slices.Contains(gr.Members, ws) {
			out = append(out, ws)
		}
	}
	return out
}

// reorderAs is the window order after `reorder-workspaces --order want`: P4a
// takes it as is; P4b (GroupMoves) keeps the current block sequence, the k-th
// block of want replaced by the k-th current block, its members in want's order.
func reorderAs(st ArrangeInput, want []string) []string {
	if !st.GroupMoves {
		return slices.Clone(want)
	}
	groupOf := func(ws string) string {
		for _, gr := range st.Groups {
			if slices.Contains(gr.Members, ws) {
				return gr.ID
			}
		}
		return ""
	}
	var cur []string // current block sequence
	for _, ws := range st.Order {
		if id := groupOf(ws); id != "" && !slices.Contains(cur, id) {
			cur = append(cur, id)
		}
	}
	var out []string
	var started []string
	for _, ws := range want {
		id := groupOf(ws)
		if id == "" {
			out = append(out, ws)
			continue
		}
		if slices.Contains(started, id) {
			continue
		}
		started = append(started, id)
		k := cur[len(started)-1]
		for _, gr := range st.Groups {
			if gr.ID != k {
				continue
			}
			members := slices.Clone(gr.Members)
			slices.SortStableFunc(members, func(a, b string) int {
				if a == gr.AnchorID {
					return -1
				}
				if b == gr.AnchorID {
					return 1
				}
				return slices.Index(want, a) - slices.Index(want, b)
			})
			out = append(out, members...)
		}
	}
	return out
}

// folderOf is the name of the group ws is a non-anchor member of, "" if none.
func folderOf(st ArrangeInput, ws string) string {
	for _, gr := range st.Groups {
		if gr.AnchorID != ws && slices.Contains(gr.Members, ws) {
			return gr.Name
		}
	}
	return ""
}

// P10a, desiredOrder: a pinned member keeps its slot whatever its rank; only
// the free members are sorted around it (the pinned one never enters the sort).
func TestArrangePinnedMemberKeepsSlotWhateverItsRank(t *testing.T) {
	t.Parallel()
	st := ArrangeInput{Folders: folders, Order: []string{"GM", "A", "P", "B"},
		Groups: []cmux.Group{g("M", "Hedge", "GM", true, "A", "P", "B")},
		Pinned: map[string]bool{"P": true},
		Target: map[string]string{"A": "Hedge", "B": "Hedge"},
		Rank:   map[string]int{"P": 0, "A": 10, "B": 5}} // P the gravest: still not moved
	want := []FolderOp{{Kind: FolderReorder, Order: []string{"GM", "B", "P", "A"}}}
	if ops := Arrange(st); !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v, want %+v", ops, want)
	}
}

// D40: a young free workspace stays exactly where it was born.
func TestDesiredOrderHoldsYoungWhereBorn(t *testing.T) {
	t.Parallel()
	sys := func(ms ...string) cmux.Group { return g("G-S", "Terrace", "GS", true, ms...) }
	nos := g("G-N", "Inbox", "GN", true, "Shed")
	tgt := map[string]string{"A": "Terrace", "B": "Terrace", "Shed": "Inbox"}
	in := func(order []string, hold map[string]bool, groups ...cmux.Group) ArrangeInput {
		return ArrangeInput{Folders: folders, Order: order, Groups: groups, Target: tgt, Hold: hold, GroupMoves: true}
	}
	order := func(ws ...string) []string { return ws }
	check := func(t *testing.T, in ArrangeInput, want []string) {
		t.Helper()
		own, _ := splitGroups(in)
		if got := desiredOrder(in, own); !slices.Equal(got, want) {
			t.Fatalf("desiredOrder = %v, want %v", got, want)
		}
	}
	t.Run("a: between two folders, no op", func(t *testing.T) {
		o := order("GS", "A", "B", "N", "GN", "Shed")
		x := in(o, map[string]bool{"N": true}, sys("A", "B"), nos)
		check(t, x, o)
		if ops := Arrange(x); len(ops) != 0 {
			t.Fatalf("ops = %+v", ops)
		}
	})
	t.Run("b: attention sort reorders the folder before it", func(t *testing.T) {
		x := in(order("GS", "A", "B", "N", "GN", "Shed"), map[string]bool{"N": true}, sys("A", "B"), nos)
		x.Rank = map[string]int{"B": 0, "A": 1}
		check(t, x, order("GS", "B", "A", "N", "GN", "Shed"))
	})
	t.Run("c: two consecutive", func(t *testing.T) {
		o := order("GS", "A", "B", "N1", "N2", "GN", "Shed")
		check(t, in(o, map[string]bool{"N1": true, "N2": true}, sys("A", "B"), nos), o)
	})
	t.Run("d: at index 0", func(t *testing.T) {
		o := order("N", "GS", "A", "GN", "Shed")
		check(t, in(o, map[string]bool{"N": true}, sys("A"), nos), o)
		x := in(order("N", "P", "GS", "A", "GN", "Shed"), map[string]bool{"N": true}, sys("A"), nos)
		x.Pinned = map[string]bool{"P": true}
		check(t, x, order("P", "N", "GS", "A", "GN", "Shed"))
	})
	t.Run("e: held member of an own folder is sorted in it", func(t *testing.T) {
		x := in(order("GS", "A", "B", "GN", "Shed"), map[string]bool{"B": true}, sys("A", "B"), nos)
		x.Rank = map[string]int{"B": 0, "A": 1}
		check(t, x, order("GS", "B", "A", "GN", "Shed"))
	})
	t.Run("f: no Hold = today's behaviour", func(t *testing.T) {
		check(t, in(order("GS", "A", "N", "GN", "Shed"), nil, sys("A"), nos), order("GS", "A", "GN", "Shed", "N"))
	})
	t.Run("g: after a loose workspace that is not held", func(t *testing.T) {
		check(t, in(order("GS", "A", "L", "N", "GN", "Shed"), map[string]bool{"N": true}, sys("A"), nos), order("GS", "A", "GN", "Shed", "L", "N"))
	})
	t.Run("h: held and targeted, deferred by D21 B", func(t *testing.T) {
		cs := sys("A")
		cs.Collapsed = true
		o := order("GS", "A", "N", "GN", "Shed")
		x := in(o, map[string]bool{"N": true}, cs, nos)
		x.Target = map[string]string{"A": "Terrace", "N": "Terrace", "Shed": "Inbox"}
		x.Selected = "N"
		if ops := Arrange(x); len(ops) != 0 {
			t.Fatalf("ops = %+v", ops)
		}
	})
	t.Run("i: after a pinned group, before a pinned workspace", func(t *testing.T) {
		pg := g("G-P", "Mine", "P1", false, "P2")
		pg.Pinned = true
		o := order("P1", "P2", "N", "PW", "GS", "A", "GN", "Shed")
		x := in(o, map[string]bool{"N": true}, pg, sys("A"), nos)
		x.Pinned = map[string]bool{"PW": true}
		check(t, x, o)
		x.Hold = nil
		own, _ := splitGroups(x)
		if got := desiredOrder(x, own); slices.Equal(got, o) {
			t.Fatal("without Hold N would go below the folders")
		}
	})
}

// D1 (sub_x000000000000019): in a sorted folder, the off-topic sessions filed
// in a space (Last) come after the other free members, whatever their
// gravity; between them, the rank order holds; a pinned member keeps its slot
// (P10); an Unsorted folder ignores Last.
func TestArrangeLastSortsAfterBound(t *testing.T) {
	t.Parallel()
	in := ArrangeInput{Folders: folders,
		Order:    []string{"GS", "L0", "P", "B1", "L1", "B2", "GT", "X", "Y"},
		Groups:   []cmux.Group{g("S", "Terrace", "GS", true, "L0", "P", "B1", "L1", "B2"), g("T", "Standby", "GT", true, "X", "Y")},
		Pinned:   map[string]bool{"P": true},
		Target:   map[string]string{"L0": "Terrace", "B1": "Terrace", "L1": "Terrace", "B2": "Terrace", "X": "Standby", "Y": "Standby"},
		Rank:     map[string]int{"L0": 0, "L1": 3, "B1": 5, "B2": 5, "X": 0, "Y": 0},
		Last:     map[string]bool{"L0": true, "L1": true, "X": true},
		Unsorted: map[string]bool{"Standby": true}}
	want := []string{"GS", "B1", "P", "B2", "L0", "L1", "GT", "X", "Y"}
	ops := Arrange(in)
	if len(ops) != 1 || ops[0].Kind != FolderReorder || !reflect.DeepEqual(ops[0].Order, want) {
		t.Fatalf("ops = %+v, want reorder %q", ops, want)
	}
}

// Lot 6 (sub_x000000000000010): the folder pigtail made as « Other »
// (generated anchor, hosting nothing) is renamed « Inbox » in place, alone in
// its round (cmux probe P20a: position, members, fold, icon kept), sorted or not
// (« les dossiers restent à leur position », sub_x000000000000041). Never
// renamed — the merge (R1–R4) converges instead, as « Sans sujet » did at D9 —
// with an « Inbox » group already listed (P20d: cmux accepts a duplicate), two
// « Other », an anchor not generated (a group of the user's) or hosting a session.
func TestArrangeRenamesFormerNoSubjectInPlace(t *testing.T) {
	t.Parallel()
	order := []string{"GO", "N1", "GS", "S1", "GI"}
	tgt := map[string]string{"N1": space.NoSubject, "S1": "Terrace"}
	other := g("G-O", space.FormerNoSubject, "GO", true, "N1")
	sys := g("G-S", "Terrace", "GS", true, "S1")
	rename := []FolderOp{{Kind: FolderRename, Group: "G-O", Name: space.NoSubject}}
	for _, noSort := range []bool{false, true} {
		ops := Arrange(ArrangeInput{Folders: folders, Order: order, Groups: []cmux.Group{other, sys}, Target: tgt, NoSort: noSort})
		if !reflect.DeepEqual(ops, rename) {
			t.Fatalf("noSort=%v: ops = %+v, want %+v", noSort, ops, rename)
		}
	}
	manual := g("G-O", space.FormerNoSubject, "GO", false, "N1")
	for name, in := range map[string]ArrangeInput{
		"inbox-listed":  {Groups: []cmux.Group{other, sys, g("G-I", space.NoSubject, "GI", false)}},
		"two-former":    {Groups: []cmux.Group{other, sys, g("G-O2", space.FormerNoSubject, "GI", true)}},
		"manual-anchor": {Groups: []cmux.Group{manual, sys}},
		"hosted-anchor": {Groups: []cmux.Group{other, sys}, AnchorHosts: map[string]bool{"GO": true}},
	} {
		in.Folders, in.Order, in.Target, in.NoSort = folders, order, tgt, true
		for _, op := range Arrange(in) {
			if op.Kind == FolderRename {
				t.Fatalf("%s: renamed (%+v), want the merge path", name, op)
			}
		}
	}
}
