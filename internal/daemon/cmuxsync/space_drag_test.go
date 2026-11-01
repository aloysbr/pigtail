package cmuxsync

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/space"
)

// The pure judge of hand drags between own folders (space_drag.go,
// after the former Prio detector): two trusted listings,
// the engine's own writes noted (rules 3a–3c), never a pinned tab, never an
// unlisted one, never a stale pair. No runner here: dragRead is built by hand.

// DragReadGuard is derived from the P22 measure (cmux probe notes): max(TakeoverReadGuard,
// 2 × P22MaxLag), rounded up to 500 ms.
func TestDragReadGuardFromP22(t *testing.T) {
	t.Parallel()
	want := 2 * P22MaxLag
	if want < TakeoverReadGuard {
		want = TakeoverReadGuard
	}
	if r := want % (500 * time.Millisecond); r != 0 {
		want += 500*time.Millisecond - r
	}
	if DragReadGuard != want {
		t.Fatalf("DragReadGuard %v, want %v from P22MaxLag %v", DragReadGuard, want, P22MaxLag)
	}
}

// lagUnderGuard is a cmux lag the guard still covers (rule 3b), derived from
// the measured guard rather than written down: DragReadGuard − 500 ms, at
// least 500 ms.
func lagUnderGuard() time.Duration {
	return max(DragReadGuard-500*time.Millisecond, 500*time.Millisecond)
}

// The judge's own folders: a focus « F » first (as r.folderOrder), then the
// spaces, « Inbox », « Maintenance », « Standby ».
var dragFolders = []string{"F", "Terrace", "Hedge", "Trellis", space.NoSubject, space.MacMaintenance, space.Standby}

// dragSpaces are the space folders among them (r.knownSpace).
var dragSpaces = map[string]bool{"Terrace": true, "Hedge": true, "Trellis": true}

var dragT0 = time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

// dragGroupID is the own group of folder f in the hand-built listings.
func dragGroupID(f string) string { return "G-" + f }

// dragListing builds one trusted listing: each own folder of dragFolders as a
// generated anchor « A-<f> » then its members (place: workspace → folder, in
// the order given by ws), then the free workspaces (place ""), listing begun
// at at.
func dragListing(at time.Time, ws []string, place map[string]string) dragRead {
	d := dragRead{pinned: map[string]bool{}, anchorHosts: map[string]bool{}, folders: dragFolders, listStart: at}
	for _, f := range dragFolders {
		gr := cmux.Group{ID: dragGroupID(f), Name: f, AnchorID: "A-" + f, AnchorGenerated: true, Members: []string{"A-" + f}}
		d.order = append(d.order, gr.AnchorID)
		for _, w := range ws {
			if place[w] == f {
				gr.Members = append(gr.Members, w)
				d.order = append(d.order, w)
			}
		}
		d.groups = append(d.groups, gr)
	}
	for _, w := range ws {
		if place[w] == "" {
			d.order = append(d.order, w)
		}
	}
	return d
}

// judgedBase runs a first listing (the base) and fails if it judged anything.
func judgedBase(t *testing.T, st *winFolders, d dragRead, ident map[string]string) {
	t.Helper()
	if moved, ok := judgeSpaceDrags(st, d, dragSpaces, ident); !ok || len(moved) != 0 {
		t.Fatalf("base listing: moved %+v, ok %v", moved, ok)
	}
}

func TestJudgeSpaceDragsFirstListingIsBase(t *testing.T) {
	t.Parallel()
	st := &winFolders{}
	d := dragListing(dragT0, []string{"A", "B"}, map[string]string{"A": "Hedge"})
	moved, ok := judgeSpaceDrags(st, d, dragSpaces, map[string]string{"A": "sub:x"})
	if !ok || len(moved) != 0 {
		t.Fatalf("first listing: moved %+v, ok %v; want the base only", moved, ok)
	}
	if got := st.spaceBase["A"]; got != (spaceBaseEntry{slot: ownSlot{dragGroupID("Hedge"), "Hedge"}, ident: "sub:x"}) {
		t.Fatalf("base of A %+v", got)
	}
	if got, ok := st.spaceBase["B"]; !ok || got != (spaceBaseEntry{}) {
		t.Fatalf("base of B %+v (listed %v), want a free slot", got, ok)
	}
}

func TestJudgeSpaceDragsHandMove(t *testing.T) {
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x"}
	judgedBase(t, st, dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"}), ident)
	moved, ok := judgeSpaceDrags(st, dragListing(dragT0.Add(30*time.Second), []string{"A"}, map[string]string{"A": "Hedge"}), dragSpaces, ident)
	if !ok || !slices.Equal(moved, []spaceMove{{"A", "Hedge"}}) {
		t.Fatalf("hand move Terrace → Hedge: moved %+v, ok %v", moved, ok)
	}
	if got := st.spaceBase["A"].slot.folder; got != "Hedge" {
		t.Fatalf("base after the move: %q, want Hedge", got)
	}
}

func TestJudgeSpaceDragsIdentChangedIsBase(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ before, after string }{{"", "sub:x"}, {"", "off"}, {"sub:x", "sub:y"}, {"sub:x", "sub:x"}} {
		st := &winFolders{}
		judgedBase(t, st, dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"}), map[string]string{"A": c.before})
		moved, ok := judgeSpaceDrags(st, dragListing(dragT0.Add(30*time.Second), []string{"A"}, map[string]string{"A": "Hedge"}), dragSpaces, map[string]string{"A": c.after})
		if !ok {
			t.Fatalf("%q → %q: not judged", c.before, c.after)
		}
		if c.before == c.after { // control: the identity is what cuts
			if !slices.Equal(moved, []spaceMove{{"A", "Hedge"}}) {
				t.Fatalf("identity %q unchanged: moved %+v, want the drag", c.before, moved)
			}
			continue
		}
		if len(moved) != 0 {
			t.Fatalf("identity %q → %q: moved %+v, want the listing as the new base", c.before, c.after, moved)
		}
		if got := st.spaceBase["A"]; got != (spaceBaseEntry{slot: ownSlot{dragGroupID("Hedge"), "Hedge"}, ident: c.after}) {
			t.Fatalf("identity %q → %q: base %+v", c.before, c.after, got)
		}
	}
}

func TestJudgeSpaceDragsOwnWriteShown(t *testing.T) { // 3a
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x"}
	d0 := dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"})
	judgedBase(t, st, d0, ident)
	st.roundGroups = d0.groups
	st.noteSpaceWrite(FolderOp{Kind: FolderAdd, Group: dragGroupID("Hedge"), Workspace: "A"}, dragT0.Add(time.Second))
	moved, ok := judgeSpaceDrags(st, dragListing(dragT0.Add(1100*time.Millisecond), []string{"A"}, map[string]string{"A": "Hedge"}), dragSpaces, ident)
	if !ok || len(moved) != 0 {
		t.Fatalf("own add shown: moved %+v, ok %v", moved, ok)
	}
	if _, pending := st.spaceWrote["A"]; pending {
		t.Fatal("own add shown: the write is still pending")
	}
	if got := st.spaceBase["A"].slot.folder; got != "Hedge" {
		t.Fatalf("base %q, want Hedge", got)
	}
}

func TestJudgeSpaceDragsLagWithinGuard(t *testing.T) { // 3b
	t.Parallel()
	st := &winFolders{}
	d0 := dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"})
	judgedBase(t, st, d0, map[string]string{"A": "sub:x"})
	before := st.spaceBase["A"]
	st.roundGroups = d0.groups
	at := dragT0.Add(time.Second)
	st.noteSpaceWrite(FolderOp{Kind: FolderAdd, Group: dragGroupID("Hedge"), Workspace: "A"}, at)
	// cmux still lists A in Terrace; the identity changed meanwhile: the whole
	// base is kept, identity included.
	moved, ok := judgeSpaceDrags(st, dragListing(at.Add(lagUnderGuard()), []string{"A"}, map[string]string{"A": "Terrace"}), dragSpaces, map[string]string{"A": "off"})
	if !ok || len(moved) != 0 {
		t.Fatalf("lag within the guard: moved %+v, ok %v", moved, ok)
	}
	if got := st.spaceBase["A"]; got != before {
		t.Fatalf("base %+v, want kept whole %+v", got, before)
	}
	if w, pending := st.spaceWrote["A"]; !pending || w.slot.group != dragGroupID("Hedge") {
		t.Fatalf("write %+v (pending %v), want still pending", w, pending)
	}
}

func TestJudgeSpaceDragsUndoneAfterGuard(t *testing.T) { // 3c
	t.Parallel()
	for _, shown := range []string{"Trellis", "Terrace"} {
		st := &winFolders{}
		ident := map[string]string{"A": "sub:x"}
		d0 := dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"})
		judgedBase(t, st, d0, ident)
		st.roundGroups = d0.groups
		at := dragT0.Add(time.Second)
		st.noteSpaceWrite(FolderOp{Kind: FolderAdd, Group: dragGroupID("Hedge"), Workspace: "A"}, at)
		moved, ok := judgeSpaceDrags(st, dragListing(at.Add(DragReadGuard), []string{"A"}, map[string]string{"A": shown}), dragSpaces, ident)
		if !ok || !slices.Equal(moved, []spaceMove{{"A", shown}}) {
			t.Fatalf("write to Hedge, %s listed past the guard: moved %+v, ok %v; want a drag to %s", shown, moved, ok, shown)
		}
		if _, pending := st.spaceWrote["A"]; pending {
			t.Fatalf("%s: the write is still pending", shown)
		}
	}
}

func TestJudgeSpaceDragsNotASpace(t *testing.T) {
	t.Parallel()
	for _, to := range []string{space.NoSubject, space.Standby, space.MacMaintenance, "F", ""} {
		st := &winFolders{}
		ident := map[string]string{"A": "sub:x"}
		judgedBase(t, st, dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"}), ident)
		moved, ok := judgeSpaceDrags(st, dragListing(dragT0.Add(30*time.Second), []string{"A"}, map[string]string{"A": to}), dragSpaces, ident)
		if !ok || len(moved) != 0 {
			t.Fatalf("to %q: moved %+v, ok %v; want nothing", to, moved, ok)
		}
		if got := st.spaceBase["A"].slot.folder; got != to {
			t.Fatalf("to %q: base %q, want updated", to, got)
		}
	}
}

func TestJudgeSpaceDragsManualGroupIgnored(t *testing.T) {
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x"}
	judgedBase(t, st, dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"}), ident)
	d := dragListing(dragT0.Add(30*time.Second), []string{}, nil)
	d.groups = append(d.groups, cmux.Group{ID: "MG", Name: "Hedge", AnchorID: "WS-M", Members: []string{"WS-M", "A"}}) // Ada' own « Hedge »
	d.order = append(d.order, "WS-M", "A")
	moved, ok := judgeSpaceDrags(st, d, dragSpaces, ident)
	if !ok || len(moved) != 0 {
		t.Fatalf("into a manual group named Hedge: moved %+v, ok %v", moved, ok)
	}
}

func TestJudgeSpaceDragsFolderRenamedIsNoDrag(t *testing.T) {
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x"}
	judgedBase(t, st, dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"}), ident)
	d := dragListing(dragT0.Add(30*time.Second), []string{"A"}, map[string]string{"A": "Terrace"})
	for i := range d.groups {
		switch d.groups[i].Name {
		case "Hedge":
			d.groups[i].Name = "Old" // out of the way
		case "Terrace":
			d.groups[i].Name = "Hedge" // renamed by hand, same ID
		}
	}
	moved, ok := judgeSpaceDrags(st, d, dragSpaces, ident)
	if !ok || len(moved) != 0 {
		t.Fatalf("folder renamed Terrace → Hedge: moved %+v, ok %v", moved, ok)
	}
}

func TestJudgeSpaceDragsPinnedNeverJudged(t *testing.T) {
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x"}
	judgedBase(t, st, dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"}), ident)
	d := dragListing(dragT0.Add(30*time.Second), []string{"A"}, map[string]string{"A": "Hedge"})
	d.pinned["A"] = true
	if moved, ok := judgeSpaceDrags(st, d, dragSpaces, ident); !ok || len(moved) != 0 {
		t.Fatalf("pinned in Hedge: moved %+v, ok %v", moved, ok)
	}
	if got := st.spaceBase["A"].slot.folder; got != "Hedge" {
		t.Fatalf("pinned: base %q, want it to follow the listing", got)
	}
	d = dragListing(dragT0.Add(60*time.Second), []string{"A"}, map[string]string{"A": "Hedge"}) // unpinned, not moved
	if moved, ok := judgeSpaceDrags(st, d, dragSpaces, ident); !ok || len(moved) != 0 {
		t.Fatalf("unpinned, not moved: moved %+v, ok %v", moved, ok)
	}
}

func TestJudgeSpaceDragsUnlisted(t *testing.T) {
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x"}
	d0 := dragListing(dragT0, []string{"A", "B"}, map[string]string{"A": "Terrace"})
	judgedBase(t, st, d0, ident)
	st.roundGroups = d0.groups
	st.noteSpaceWrite(FolderOp{Kind: FolderAdd, Group: dragGroupID("Hedge"), Workspace: "A"}, dragT0.Add(time.Second))
	moved, ok := judgeSpaceDrags(st, dragListing(dragT0.Add(30*time.Second), []string{"B"}, nil), dragSpaces, ident)
	if !ok || len(moved) != 0 {
		t.Fatalf("A unlisted: moved %+v, ok %v", moved, ok)
	}
	if _, ok := st.spaceBase["A"]; ok {
		t.Fatal("A unlisted: still in the base")
	}
	if _, ok := st.spaceWrote["A"]; ok {
		t.Fatal("A unlisted: its write still pending")
	}
}

func TestJudgeSpaceDragsStalePair(t *testing.T) {
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x"}
	d0 := dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"})
	judgedBase(t, st, d0, ident)
	st.roundGroups = d0.groups
	st.noteSpaceWrite(FolderOp{Kind: FolderAdd, Group: dragGroupID("Trellis"), Workspace: "A"}, dragT0.Add(time.Second))
	base, wrote := maps2(st)
	d := dragListing(dragT0.Add(30*time.Second), []string{"A"}, map[string]string{"A": "Hedge"})
	d.order = slices.DeleteFunc(d.order, func(w string) bool { return w == "A-Hedge" }) // the group list and the workspace list disagree
	if moved, ok := judgeSpaceDrags(st, d, dragSpaces, ident); ok || len(moved) != 0 {
		t.Fatalf("stale pair: moved %+v, ok %v; want not judged", moved, ok)
	}
	if b, w := maps2(st); !mapsEqual(b, base) || !mapsEqual(w, wrote) {
		t.Fatalf("stale pair: base %+v, writes %+v changed (were %+v, %+v)", b, w, base, wrote)
	}
}

func maps2(st *winFolders) (map[string]spaceBaseEntry, map[string]spaceWrite) {
	b := make(map[string]spaceBaseEntry, len(st.spaceBase))
	for k, v := range st.spaceBase {
		b[k] = v
	}
	w := make(map[string]spaceWrite, len(st.spaceWrote))
	for k, v := range st.spaceWrote {
		w[k] = v
	}
	return b, w
}

func mapsEqual[V comparable](a, b map[string]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func TestJudgeSpaceDragsListingOrder(t *testing.T) {
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x", "B": "sub:y"}
	judgedBase(t, st, dragListing(dragT0, []string{"A", "B"}, map[string]string{"A": "Terrace", "B": "Terrace"}), ident)
	// B dragged into Hedge, A into Trellis: Hedge' block is listed first.
	d := dragListing(dragT0.Add(30*time.Second), []string{"B", "A"}, map[string]string{"A": "Trellis", "B": "Hedge"})
	moved, ok := judgeSpaceDrags(st, d, dragSpaces, ident)
	if !ok || !slices.Equal(moved, []spaceMove{{"B", "Hedge"}, {"A", "Trellis"}}) {
		t.Fatalf("moved %+v, ok %v; want listing order (B, then A)", moved, ok)
	}
	if ia, ib := slices.Index(d.order, "A"), slices.Index(d.order, "B"); ib > ia {
		t.Fatalf("instrument: B (%d) not listed above A (%d)", ib, ia)
	}
}

func TestNoteSpaceWrite(t *testing.T) {
	t.Parallel()
	d0 := dragListing(dragT0, []string{"A", "B"}, map[string]string{"A": "Terrace", "B": "Terrace"})
	newSt := func() *winFolders {
		st := &winFolders{roundGroups: d0.groups}
		st.createdMaps()
		return st
	}
	at := dragT0.Add(time.Second)
	st := newSt()
	st.noteSpaceWrite(FolderOp{Kind: FolderAdd, Group: dragGroupID("Hedge"), Workspace: "A"}, at)
	if got := st.spaceWrote["A"]; got != (spaceWrite{slot: ownSlot{dragGroupID("Hedge"), "Hedge"}, at: at}) {
		t.Fatalf("add: %+v", got)
	}
	st = newSt()
	st.createdIDs["Fresh"] = "G-fresh" // created this pass, not in the round's groups yet
	st.noteSpaceWrite(FolderOp{Kind: FolderAdd, Group: "G-fresh", Workspace: "A"}, at)
	if got := st.spaceWrote["A"].slot; got != (ownSlot{"G-fresh", "Fresh"}) {
		t.Fatalf("add to a created folder: %+v", got)
	}
	st = newSt()
	st.noteSpaceWrite(FolderOp{Kind: FolderRemove, Workspace: "A"}, at)
	if got, ok := st.spaceWrote["A"]; !ok || got.slot != (ownSlot{}) {
		t.Fatalf("remove: %+v (%v)", got, ok)
	}
	for _, k := range []FolderOpKind{FolderDissolve, FolderDelete} {
		st = newSt()
		st.noteSpaceWrite(FolderOp{Kind: k, Group: dragGroupID("Terrace")}, at)
		if len(st.spaceWrote) != 2 || st.spaceWrote["A"].slot != (ownSlot{}) || st.spaceWrote["B"].slot != (ownSlot{}) {
			t.Fatalf("%s: %+v, want A and B free, never the anchor", k, st.spaceWrote)
		}
	}
	for _, op := range []FolderOp{
		{Kind: FolderRename, Group: dragGroupID("Terrace"), Name: "X"},
		{Kind: FolderCreate, Name: "Trellis"},
		{Kind: FolderMove, Group: dragGroupID("Terrace"), Ref: dragGroupID("Hedge")},
		{Kind: FolderReorder, Order: d0.order},
		{Kind: FolderPlace, Workspace: "A", Ref: "B"},
		{Kind: FolderSetIcon, Group: dragGroupID("Terrace"), Icon: "house.fill"},
		{Kind: FolderCollapse, Group: dragGroupID("Terrace")},
		{Kind: FolderExpand, Group: dragGroupID("Terrace")},
	} {
		st = newSt()
		st.noteSpaceWrite(op, at)
		if len(st.spaceWrote) != 0 {
			t.Fatalf("%s noted %+v: it moves no workspace between groups", op.Kind, st.spaceWrote)
		}
	}
}

// spaceDragStubs wires the three drag Deps with writes that do nothing.
func spaceDragStubs(_ *Config, d *Deps) {
	d.SubjectSpaceSet = func(string, string, string) error { return nil }
	d.OffTopicSpaceSet = func(string, string, string) error { return nil }
	d.SpaceScope = func(string) (string, bool) { return "", false }
}

func TestNoteSpaceWriteUnwiredNotesNothing(t *testing.T) {
	t.Parallel()
	h, fw := filedWorld(t, false)
	op := FolderOp{Kind: FolderAdd, Group: fw.groupNamed("Hedge").ID, Workspace: "WS-A"}
	if err := h.r.applyFolderOp(win, op); err != nil {
		t.Fatal(err)
	}
	if n := len(h.winSt().spaceWrote); n != 0 {
		t.Fatalf("drag unwired: %d writes noted, want none (no reader would purge them)", n)
	}
	h, fw = filedWorld(t, false, spaceDragStubs)
	op.Group = fw.groupNamed("Hedge").ID
	if err := h.r.applyFolderOp(win, op); err != nil {
		t.Fatal(err)
	}
	if got := h.winSt().spaceWrote["WS-A"].slot; got.group != op.Group || got.folder != "Hedge" {
		t.Fatalf("drag wired: noted %+v, want the add to Hedge", got)
	}
	fw.failOn = "group-add" // a failed op is never noted
	op.Workspace = "WS-B"
	delete(h.winSt().spaceWrote, "WS-B") // the world's own add of WS-B to Terrace (no judge reads here to purge it)
	if err := h.r.applyFolderOp(win, op); err == nil {
		t.Fatal("instrument: the add did not fail")
	}
	if _, ok := h.winSt().spaceWrote["WS-B"]; ok {
		t.Fatal("a failed add was noted")
	}
}

// A folder stop then resume (SetEnabled(true)) forgets the drag memory with
// the other created-folder state: tabs moved while the loop was stopped are
// never read as hand drags against a base from before the stop.
func TestFolderResumeForgetsSpaceDrags(t *testing.T) {
	t.Parallel()
	h, _ := filedWorld(t, false)
	st := h.winSt()
	st.spaceBase = map[string]spaceBaseEntry{"WS-A": {slot: ownSlot{"G", "Terrace"}}}
	st.spaceWrote = map[string]spaceWrite{"WS-A": {at: h.now}}
	h.r.stopFolders("test stop")
	h.converge(2)
	if st.spaceBase == nil {
		t.Fatal("instrument: the stop already forgot the base")
	}
	h.r.SetEnabled(true)
	h.converge(1)
	if st := h.winSt(); st.spaceBase != nil || st.spaceWrote != nil {
		t.Fatalf("after resume: base %+v, writes %+v; want both forgotten", st.spaceBase, st.spaceWrote)
	}
}

// A failed store write puts the engine's pending write back (spaceWrote) with
// the base: the next trusted listing judges the gesture again instead of
// losing it. Called on a bare Runner: in a live round the engine re-adds the
// tab and masks the loss.
func TestApplySpaceDragsWriteFailsKeepsPendingEngineWrite(t *testing.T) { // mutation: drop the spaceWrote restore → no second write
	t.Parallel()
	st := &winFolders{}
	ident := map[string]string{"A": "sub:x"}
	d0 := dragListing(dragT0, []string{"A"}, map[string]string{"A": "Terrace"})
	judgedBase(t, st, d0, ident)
	st.roundGroups = d0.groups
	at := dragT0.Add(time.Second)
	st.noteSpaceWrite(FolderOp{Kind: FolderAdd, Group: dragGroupID("Hedge"), Workspace: "A"}, at) // never shown (3c)
	var sets []string
	fail := true
	r := &Runner{deps: Deps{
		Logf:        func(string, ...any) {},
		FolderOrder: func() []string { return dragFolders },
		SubjectSpaceSet: func(id, sp, from string) error {
			sets = append(sets, sp)
			if fail {
				return errors.New("SQLITE_BUSY")
			}
			return nil
		},
		OffTopicSpaceSet: func(string, string, string) error { return nil },
		SpaceScope:       func(string) (string, bool) { return "perso", true },
	}}
	filing := func() Filing {
		return Filing{
			BoundBy: map[string]boundWS{"A": {Subject: "x", Scope: "perso", Space: "Hedge", ThemeSpace: "Hedge"}},
			Bound:   map[string]string{"A": "Hedge"},
		}
	}
	d1 := dragListing(at.Add(DragReadGuard), []string{"A"}, map[string]string{"A": "Terrace"})
	if skip := r.applySpaceDrags(st, d1, filing(), focusView{}); !skip["A"] || len(sets) != 1 {
		t.Fatalf("failing write: skip %v, sets %q", skip, sets)
	}
	fail = false
	d2 := dragListing(at.Add(DragReadGuard+3*time.Second), []string{"A"}, map[string]string{"A": "Terrace"})
	if skip := r.applySpaceDrags(st, d2, filing(), focusView{}); skip["A"] || !slices.Equal(sets, []string{"Terrace", "Terrace"}) {
		t.Fatalf("store back: skip %v, sets %q; want the gesture judged again and written", skip, sets)
	}
}
