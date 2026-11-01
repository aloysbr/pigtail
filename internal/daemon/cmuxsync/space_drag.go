package cmuxsync

import (
	"maps"
	"slices"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/scope"
)

// A hand drag into a space folder (lot 3 of sub_x000000000000033, P2, D1–D2):
// the user drags a tab from one of pigtail's own folders into another. The drag
// is read as a transition between two trusted listings of the window where the
// tab shows both times, never from one listing alone, and never from what
// pigtail itself wrote (spaceWrote): a listing that shows the engine's own add
// or remove is the engine's transition, whatever the delay (3a); one that does
// not show it yet may still be cmux lagging until DragReadGuard has passed
// (3b, cmux probe P22); past it, the listing is the user's gesture (3c). The rules
// are the former « Prio » detector's (prio_drag.go, gone with #244), with a
// slot in place of a boolean: a tab's own folder, by group ID (a folder
// renamed by hand keeps its ID: never a gesture), its name beside it.
//
// Latency: no round is added (D13 of sub_x000000000000029). The drag is
// judged in the round the signature or the slow listing already owe, so up to
// one SlowInterval after the gesture, once cmux is not key.

// dragRead is one trusted listing of a window, as the judge reads it.
type dragRead struct {
	order       []string
	groups      []cmux.Group
	pinned      map[string]bool
	anchorHosts map[string]bool
	folders     []string  // the round's folder order (r.folderOrder(fv): focus names first, as Arrange)
	listStart   time.Time // taken before ListWorkspaces
}

// ownSlot is a workspace's own folder in a listing, by group ID and name
// (zero = free, a manual or pinned group, or the anchor itself).
type ownSlot struct{ group, folder string }

// spaceBaseEntry is a workspace's slot at the last trusted listing and its
// binding identity then: "sub:<id>", "off" (declared), "" (neither).
type spaceBaseEntry struct {
	slot  ownSlot
	ident string
}

// spaceWrite is the slot the engine last put a workspace in, and when;
// pending until a trusted listing shows it.
type spaceWrite struct {
	slot ownSlot
	at   time.Time
}

// spaceMove is one hand drag into a space folder, in listing order.
type spaceMove struct{ ws, folder string }

// input is the listing as the arrangement reads it (splitGroups, pairStale);
// Order is set: pairStale reads it.
func (d dragRead) input() ArrangeInput {
	return ArrangeInput{Order: d.order, Groups: d.groups, Folders: d.folders, AnchorHosts: d.anchorHosts}
}

// stalePair is Arrange's own test (pairStale, folders.go): a pinned group or
// an own folder whose anchor the workspace list does not show means the two
// reads do not match; nothing is judged on them.
func stalePair(d dragRead) bool {
	in := d.input()
	own, _ := splitGroups(in)
	return pairStale(in, own)
}

// ownSlots is each workspace's own folder in d: a member (never the anchor)
// of the group splitGroups reads as pigtail's folder of that name.
func ownSlots(d dragRead) map[string]ownSlot {
	own, _ := splitGroups(d.input())
	out := map[string]ownSlot{}
	for _, gr := range d.groups {
		if o := own[gr.Name]; o == nil || o.ID != gr.ID {
			continue
		}
		for _, m := range gr.Members {
			if m != gr.AnchorID {
				out[m] = ownSlot{group: gr.ID, folder: gr.Name}
			}
		}
	}
	return out
}

// judgeSpaceDrags reads the hand drags of one trusted listing into a space
// folder (spaces: the names that are; ident: each workspace's binding identity
// now). It updates the window's drag memory (spaceBase, spaceWrote) and calls
// nothing; ok false = a stale pair, nothing judged, nothing changed.
//
//  1. A stale pair judges nothing.
//  2. The first listing (no base: a new runner, a resume) is the base only.
//  3. A workspace absent from the base is based only. A pending engine write
//     shown by the listing is forgotten (3a); not shown and the listing began
//     within DragReadGuard of it, the base entry is kept whole, identity
//     included (3b); past the guard, it is forgotten and its slot is the base
//     the listing is judged against (3c).
//  4. A changed identity (bound, declared, rebound since the base) makes the
//     listing the new base, never a gesture (D2·t2). Else, a tab not pinned
//     whose own group changed into a space folder is a drag.
//  5. The new base is this listing, but where 3b keeps it; the unlisted are
//     forgotten from the base and from the pending writes.
func judgeSpaceDrags(st *winFolders, d dragRead, spaces map[string]bool, ident map[string]string) (moved []spaceMove, ok bool) {
	if stalePair(d) {
		return nil, false
	}
	cur := ownSlots(d)
	next := make(map[string]spaceBaseEntry, len(d.order))
	listed := make(map[string]bool, len(d.order))
	first := st.spaceBase == nil
	for _, ws := range d.order {
		listed[ws] = true
		c := cur[ws]
		now := spaceBaseEntry{slot: c, ident: ident[ws]}
		b, known := st.spaceBase[ws]
		if first || !known {
			next[ws] = now
			continue
		}
		if w, pending := st.spaceWrote[ws]; pending {
			switch {
			case c.group == w.slot.group: // 3a: the engine's own transition
				delete(st.spaceWrote, ws)
				next[ws] = now
				continue
			case d.listStart.Before(w.at.Add(DragReadGuard)): // 3b: cmux may still lag
				next[ws] = b
				continue
			default: // 3c: undone (or never applied) before a listing showed it
				delete(st.spaceWrote, ws)
				b.slot = w.slot
			}
		}
		next[ws] = now
		if ident[ws] != b.ident {
			continue // D2·t2: a gesture made before the binding never writes
		}
		if !d.pinned[ws] && c.group != b.slot.group && spaces[c.folder] {
			moved = append(moved, spaceMove{ws: ws, folder: c.folder})
		}
	}
	for ws := range st.spaceWrote {
		if !listed[ws] {
			delete(st.spaceWrote, ws) // gone from the window: nothing left to see
		}
	}
	st.spaceBase = next
	return moved, true
}

// noteSpaceWrite records, after a successful op, the slot the engine put a
// workspace in (spaceWrote), read against the round's groups: an add → its
// group; a remove → free; a dissolve or delete → free for every member but the
// anchor. A rename keeps the ID (nothing moves); the other ops move no
// workspace between groups.
func (st *winFolders) noteSpaceWrite(op FolderOp, at time.Time) {
	mark := func(ws string, slot ownSlot) {
		if st.spaceWrote == nil {
			st.spaceWrote = map[string]spaceWrite{}
		}
		st.spaceWrote[ws] = spaceWrite{slot: slot, at: at}
	}
	switch op.Kind {
	case FolderAdd:
		mark(op.Workspace, ownSlot{group: op.Group, folder: st.groupName(op.Group)})
	case FolderRemove:
		mark(op.Workspace, ownSlot{})
	case FolderDissolve, FolderDelete:
		for _, gr := range st.roundGroups {
			if gr.ID != op.Group {
				continue
			}
			for _, m := range gr.Members {
				if m != gr.AnchorID {
					mark(m, ownSlot{})
				}
			}
		}
	}
}

// groupName is the name of group id in the round's groups, else of a folder
// created this pass, else "".
func (st *winFolders) groupName(id string) string {
	for _, gr := range st.roundGroups {
		if gr.ID == id {
			return gr.Name
		}
	}
	for name, gid := range st.createdIDs {
		if gid == id {
			return name
		}
	}
	return ""
}

// applySpaceDrags judges d and writes the space of each hand drag into a space
// folder (spec rules 3–9), then updates f so that this round files the tab
// where it was dropped and its siblings after it. skip: the workspaces whose
// write failed, neither filed back nor moved this round; their drag memory is
// put back so that the next trusted listing judges them again.
//
//   - bound: the subject's space = the folder, ” when it is the theme's;
//     a subject already moved this pass keeps the first (listing order); a
//     space of another scope than the subject's is refused; a drag into the
//     effective space is no change.
//   - declared off-topic: its declaration's space = the folder, when the
//     declaring scope reads the folder's.
//   - neither: nothing (folderTargets keeps a young one where it was dropped).
func (r *Runner) applySpaceDrags(st *winFolders, d dragRead, f Filing, fv focusView) (skip map[string]bool) {
	if !r.spaceDragWired() {
		return nil
	}
	ident := make(map[string]string, len(d.order))
	for _, ws := range d.order {
		switch {
		case f.BoundBy[ws].Subject != "":
			ident[ws] = "sub:" + f.BoundBy[ws].Subject
		case hasOffTopic(f, ws):
			ident[ws] = "off"
		}
	}
	known := r.knownSpace()
	spaces := map[string]bool{}
	for _, n := range d.folders {
		if known(n) && !slices.Contains(fv.names, n) { // a focus named like a space is no space here
			spaces[n] = true
		}
	}
	base, wrote := maps.Clone(st.spaceBase), maps.Clone(st.spaceWrote)
	moved, ok := judgeSpaceDrags(st, d, spaces, ident)
	if !ok || len(moved) == 0 {
		return nil
	}
	dragged := f.dragged
	if dragged == nil {
		dragged = map[string]string{}
	}
	failed := func(ws string, err error) {
		if skip == nil {
			skip = map[string]bool{}
		}
		skip[ws] = true
		if msg := err.Error(); msg != r.spaceDragErr {
			r.spaceDragErr = msg
			r.deps.Logf("cmuxsync: folders: space drag: %v (workspace %s)", err, ws)
		}
		if e, had := base[ws]; had {
			st.spaceBase[ws] = e
		} else {
			delete(st.spaceBase, ws)
		}
		if e, had := wrote[ws]; had {
			if st.spaceWrote == nil {
				st.spaceWrote = map[string]spaceWrite{}
			}
			st.spaceWrote[ws] = e
		} else {
			delete(st.spaceWrote, ws)
		}
	}
	for _, m := range moved {
		sc, scoped := r.deps.SpaceScope(m.folder)
		if b, isBound := f.BoundBy[m.ws]; isBound && b.Subject != "" {
			switch {
			case dragged[b.Subject] == m.folder:
				continue // a sibling dropped in the same folder: the same gesture
			case dragged[b.Subject] != "":
				r.deps.Logf("cmuxsync: folders: space drag ignored: subject %s already moved this pass (workspace %s)", b.Subject, m.ws)
				continue
			case !scoped || sc != b.Scope:
				r.deps.Logf("cmuxsync: folders: space drag refused: %s is not a space of scope %s (workspace %s)", m.folder, b.Scope, m.ws)
				continue
			case m.folder == b.Space:
				continue // its effective space: no change
			}
			want := m.folder
			if want == b.ThemeSpace {
				want = "" // back to its theme's
			}
			if err := r.deps.SubjectSpaceSet(b.Subject, want, b.Space); err != nil {
				failed(m.ws, err)
				continue
			}
			r.spaceDragErr = ""
			dragged[b.Subject] = m.folder
			r.deps.Logf("cmuxsync: folders: space set by drag: subject %s %s → %s (workspace %s)", b.Subject, b.Space, m.folder, m.ws)
			for ws2, b2 := range f.BoundBy {
				if b2.Subject == b.Subject {
					b2.Space = m.folder
					f.BoundBy[ws2] = b2
					f.Bound[ws2] = fv.folderOf(b2)
				}
			}
			continue
		}
		if !hasOffTopic(f, m.ws) {
			continue
		}
		switch {
		case !scoped || !scope.CanRead(scope.Scope(f.OffTopicScope[m.ws]), scope.Scope(sc)):
			r.deps.Logf("cmuxsync: folders: space drag refused: %s is not a space of scope %s (workspace %s)", m.folder, f.OffTopicScope[m.ws], m.ws)
			continue
		case m.folder == f.OffTopic[m.ws]:
			continue
		}
		if err := r.deps.OffTopicSpaceSet(m.ws, m.folder, f.OffTopic[m.ws]); err != nil {
			failed(m.ws, err)
			continue
		}
		r.spaceDragErr = ""
		r.deps.Logf("cmuxsync: folders: space set by drag: off-topic %s → %s", m.ws, m.folder)
		f.OffTopic[m.ws] = m.folder
	}
	return skip
}
