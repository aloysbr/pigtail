package cmuxsync

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The folder arrangement's simulated cmux (Task 6, sub_x000000000000041): the
// sidebar world of runner_test.go plus groups, the window's key state and its
// selection, with the effects measured in the cmux probe notes.

const win = winA

// testFolderOrder is the D6 order the wiring passes, « Standby » last.
var testFolderOrder = []string{"Hedge", "Trellis", "Lawn", "Terrace", "Garden", space.NoSubject, space.Standby}

// testFolderIcon is D11 as the spaces table gives it: a globex-like space
// briefcase.fill, a perso one house.fill, « Inbox » tray.
var testFolderIcon = map[string]string{
	"Hedge": "briefcase.fill", "Trellis": "briefcase.fill", "Lawn": "briefcase.fill",
	"Terrace": "house.fill", "Garden": "house.fill", space.NoSubject: space.NoSubjectIcon, space.Standby: space.StandbyIcon,
}

// folderWorld adds cmux groups and the window's key state to cmuxWorld.
// groups, key and selected are win's; every other window keeps its own in
// byWin (focus D8: each window is arranged on its own, cmux probe P21).
type folderWorld struct {
	*cmuxWorld
	now         func() time.Time
	groups      []cmux.Group
	key         bool
	selected    string
	byWin       map[string]*winGroups // windows other than win
	cur         string                // the window the group calls act on now ("" = win)
	goneWins    map[string]bool       // windows list-windows answers without (other than win: windowGone)
	failOn      string                // a folder call that fails ("group-add", "group-create"…)
	nextID      int                   // ids of created groups and anchors
	onWrite     func(call string)     // runs after each folder write (focus coming back mid-round)
	onSleep     func()                // runs during each Deps.Sleep (the pause between two ops)
	windowsErr  error                 // list-windows fails with it (the window's state is unknown)
	windowGone  bool                  // list-windows answers without the window
	blocksFixed bool                  // P4b cmux: reorder keeps group blocks in place
	busyShell   map[string]bool       // surface whose shell is not at its prompt (a process runs)
	shellErr    error                 // ShellAtPrompt fails (unreadable pane)
	// busyAfter: the surface's shell reads busy from its n-th check on (a
	// process started between the round's read and the op's re-proof).
	busyAfter  map[string]int
	shellCalls map[string]int
	// neverRendered: surfaces cmux never rendered (tty null, render_health
	// not_started; D22). startedAfter: the surface reads started from its
	// n-th check on (cmux rendered it between the round's read and the op's).
	neverRendered map[string]bool
	startedAfter  map[string]int
	renderCalls   map[string]int
	renderErr     error // SurfaceNeverRendered fails (cmux tree unreadable)
	tooSoon       int   // group lists read less than ArrangeSettle after a folder write
	// addNeedsRemove: P2 (b) cmux, add refuses a member of another group.
	addNeedsRemove bool
	// dropIcons: set-icon of these symbols is accepted and stored null (P11).
	dropIcons map[string]bool
	// renameOnCreate alters the created name, in the create output and the lists.
	renameOnCreate func(string) string
	// renameAfterCreate alters the name served by the lists only.
	renameAfterCreate func(string) string
	createOmitsName   bool // the create output carries no name
	createNotGen      bool // the created group's anchor is not generated
	createAtTop       bool // a created group lands at the top of the window, as real cmux (cmux probe P11)
	// addKeepsSide: add keeps the side the workspace comes from, as real cmux
	// (cmux probe P19): from above the group's block → right after its anchor;
	// from below → its last member. false = always its last member.
	addKeepsSide bool
	keyWins      map[string]bool // other listed windows that are key (focus D11)
	reorderFrom  [][]string      // the window's order right before each applied reorder
	listLag      time.Duration   // a created group is hidden from lists read within this
	hideReads    int             // a created group is hidden from this many lists
	neverList    bool            // created groups are never listed
	hidden       map[string]int
	createdAt    map[string]time.Time
	groupListsAt []time.Time // when each group list was read
	lastWriteAt  time.Time
	// addLag: an engine group-add / group-remove shows in the group list
	// only addLag after it; before, the list shows the membership it had
	// (memberAt). A drag by hand is never lagged. Zero = at once (cmux probe P22).
	addLag   time.Duration
	memberAt map[string]memberWas
}

// memberWas is the group (id, "" = none) a workspace was listed in before an
// engine write at `at`.
type memberWas struct {
	group string
	at    time.Time
}

// lagFrom records ws's listed membership before an engine write, in the
// current window: what the list keeps showing for addLag.
func (fw *folderWorld) lagFrom(ws string) {
	if fw.addLag <= 0 {
		return
	}
	if fw.memberAt == nil {
		fw.memberAt = map[string]memberWas{}
	}
	now := fw.now()
	if m, ok := fw.memberAt[ws]; ok && now.Sub(m.at) < fw.addLag {
		fw.memberAt[ws] = memberWas{group: m.group, at: now} // the list still shows the older one
		return
	}
	prev := ""
	if gr := fw.groupOf(ws); gr != nil && gr.AnchorID != ws {
		prev = gr.ID
	}
	fw.memberAt[ws] = memberWas{group: prev, at: now}
}

// lagged applies memberAt to a group list read now in the current window.
func (fw *folderWorld) lagged(out []cmux.Group, now time.Time) {
	for ws, m := range fw.memberAt {
		if now.Sub(m.at) >= fw.addLag || !slices.Contains(fw.windows[fw.cw()], ws) {
			continue
		}
		for i := range out {
			if out[i].AnchorID != ws {
				out[i].Members = slices.DeleteFunc(out[i].Members, func(x string) bool { return x == ws })
			}
			if out[i].ID == m.group && !slices.Contains(out[i].Members, ws) {
				out[i].Members = append(out[i].Members, ws)
			}
		}
	}
}

// windowOf is the window listing ws ("" if none).
func (fw *folderWorld) windowOf(ws string) string {
	for w, ids := range fw.windows {
		if slices.Contains(ids, ws) {
			return w
		}
	}
	return ""
}

// lastBlockEnd is the index of the lowest member of any group in order, -1
// when no group has one listed.
func (fw *folderWorld) lastBlockEnd(order []string) int {
	end := -1
	for _, gr := range fw.groups {
		for _, m := range gr.Members {
			end = max(end, slices.Index(order, m))
		}
	}
	return end
}

// drag is the user dragging ws into groupID by hand, as cmux does it: ws leaves
// its group and lands at the end of the target's block. No write recorded,
// never lagged: the next listing shows it.
func (fw *folderWorld) drag(ws, groupID string) {
	defer fw.on(fw.windowOf(ws))()
	delete(fw.memberAt, ws)
	if gr := fw.groupOf(ws); gr != nil && gr.AnchorID != ws {
		gr.Members = slices.DeleteFunc(gr.Members, func(x string) bool { return x == ws })
	}
	gr := fw.group(groupID)
	cw := fw.cw()
	order := slices.DeleteFunc(slices.Clone(fw.windows[cw]), func(x string) bool { return x == ws })
	at := -1
	for _, m := range gr.Members {
		at = max(at, slices.Index(order, m))
	}
	gr.Members = append(gr.Members, ws)
	fw.windows[cw] = slices.Insert(order, at+1, ws)
	fw.normalize()
}

// dragOut is the user dragging ws out of its group by hand, dropped free right
// under the last block. No write recorded, never lagged.
func (fw *folderWorld) dragOut(ws string) {
	defer fw.on(fw.windowOf(ws))()
	delete(fw.memberAt, ws)
	if gr := fw.groupOf(ws); gr != nil && gr.AnchorID != ws {
		gr.Members = slices.DeleteFunc(gr.Members, func(x string) bool { return x == ws })
	}
	cw := fw.cw()
	order := slices.DeleteFunc(slices.Clone(fw.windows[cw]), func(x string) bool { return x == ws })
	fw.windows[cw] = slices.Insert(order, fw.lastBlockEnd(order)+1, ws)
	fw.normalize()
}

// winGroups is a window other than win: its groups, key state and selection.
type winGroups struct {
	groups   []cmux.Group
	key      bool
	selected string
}

// other is windowID's state (made on first use).
func (fw *folderWorld) other(windowID string) *winGroups {
	if fw.byWin == nil {
		fw.byWin = map[string]*winGroups{}
	}
	if fw.byWin[windowID] == nil {
		fw.byWin[windowID] = &winGroups{}
	}
	return fw.byWin[windowID]
}

// cw is the window the group calls act on.
func (fw *folderWorld) cw() string {
	if fw.cur != "" {
		return fw.cur
	}
	return win
}

// on makes windowID the window the group calls and helpers act on, until
// the returned restore runs: its groups are swapped into fw.groups.
func (fw *folderWorld) on(windowID string) (restore func()) {
	if windowID == "" || windowID == win || windowID == fw.cur {
		return func() {}
	}
	wg := fw.other(windowID)
	fw.groups, wg.groups = wg.groups, fw.groups
	fw.cur = windowID
	return func() {
		fw.groups, wg.groups = wg.groups, fw.groups
		fw.cur = ""
	}
}

// sel is the current window's selection.
func (fw *folderWorld) sel() string {
	if fw.cur != "" {
		return fw.other(fw.cur).selected
	}
	return fw.selected
}

// folderIn is folderOf in windowID.
func (fw *folderWorld) folderIn(windowID, ws string) string {
	defer fw.on(windowID)()
	return fw.folderOf(ws)
}

// groupNamedIn is groupNamed in windowID.
func (fw *folderWorld) groupNamedIn(windowID, name string) *cmux.Group {
	defer fw.on(windowID)()
	return fw.groupNamed(name)
}

// groupsIn is a copy of windowID's groups.
func (fw *folderWorld) groupsIn(windowID string) []cmux.Group {
	defer fw.on(windowID)()
	return slices.Clone(fw.groups)
}

// writesOn are the recorded folder writes aimed at windowID.
func (fw *folderWorld) writesOn(windowID string) []string {
	var out []string
	for _, c := range fw.folderWrites() {
		other := strings.Contains(c, " @")
		if (windowID == win && !other) || (windowID != win && strings.HasSuffix(c, " @"+windowID)) {
			out = append(out, c)
		}
	}
	return out
}

func newFolderWorld(c *cmuxWorld, now func() time.Time) *folderWorld {
	return &folderWorld{cmuxWorld: c, now: now, busyShell: map[string]bool{}, dropIcons: map[string]bool{},
		hidden: map[string]int{}, createdAt: map[string]time.Time{}, busyAfter: map[string]int{}, shellCalls: map[string]int{},
		neverRendered: map[string]bool{}, startedAfter: map[string]int{}, renderCalls: map[string]int{}}
}

var folderVerbs = []string{"group-create", "group-add", "group-remove", "ungroup", "group-collapse", "group-expand",
	"set-icon", "set-color", "reorder", "group-move", "group-rename", "place"}

// folderWrites lists the recorded folder writes.
func (fw *folderWorld) folderWrites() []string {
	var out []string
	for _, c := range fw.calls {
		if slices.Contains(folderVerbs, strings.Fields(c)[0]) {
			out = append(out, c)
		}
	}
	return out
}

// count counts the recorded calls whose verb is one of verbs.
func (fw *folderWorld) count(verbs ...string) int {
	n := 0
	for _, c := range fw.calls {
		if slices.Contains(verbs, strings.Fields(c)[0]) {
			n++
		}
	}
	return n
}

func (fw *folderWorld) group(id string) *cmux.Group {
	for i := range fw.groups {
		if fw.groups[i].ID == id {
			return &fw.groups[i]
		}
	}
	return nil
}

func (fw *folderWorld) groupNamed(name string) *cmux.Group {
	for i := range fw.groups {
		if fw.groups[i].Name == name {
			return &fw.groups[i]
		}
	}
	return nil
}

func (fw *folderWorld) groupOf(ws string) *cmux.Group {
	for i := range fw.groups {
		if slices.Contains(fw.groups[i].Members, ws) {
			return &fw.groups[i]
		}
	}
	return nil
}

// folderOf is the name of the group ws is a plain member of, "" if none.
func (fw *folderWorld) folderOf(ws string) string {
	if gr := fw.groupOf(ws); gr != nil && gr.AnchorID != ws {
		return gr.Name
	}
	return ""
}

// manualGroup builds a group in the window: anchor (added to the window if
// new), then members, as the user would.
func (fw *folderWorld) manualGroup(id, name, anchor string, generated bool, members ...string) {
	if !slices.Contains(fw.windows[win], anchor) {
		fw.add(win, anchor, name)
	}
	for _, m := range members {
		if gr := fw.groupOf(m); gr != nil {
			gr.Members = slices.DeleteFunc(gr.Members, func(x string) bool { return x == m })
		}
	}
	fw.groups = append(fw.groups, cmux.Group{ID: id, Name: name, AnchorID: anchor, AnchorGenerated: generated,
		Members: append([]string{anchor}, members...)})
	fw.contiguous()
}

// bornAfter adds a free workspace right after the block of prev (after its
// group's last member if it has one), as cmux births a new-workspace without
// --group (cmux probe P18); no write is counted.
func (fw *folderWorld) bornAfter(ws, prev string) {
	order := fw.windows[win]
	at := slices.Index(order, prev)
	if gr := fw.groupOf(prev); gr != nil {
		for _, m := range gr.Members {
			at = max(at, slices.Index(order, m))
		}
	}
	fw.add(win, ws, ws)
	order = fw.windows[win] // add appended it at the end
	order = order[:len(order)-1]
	fw.windows[win] = slices.Insert(order, at+1, ws)
}

// bornIn adds a workspace and makes it a member of folder's group, as
// new-workspace --group does; no write is counted.
func (fw *folderWorld) bornIn(ws, folder string) {
	fw.add(win, ws, ws)
	gr := fw.groupNamed(folder)
	gr.Members = append(gr.Members, ws)
	fw.contiguous()
}

// bornNextTo adds ws as a member of after's group, right after it, as cmux
// births `new-workspace --group G --group-placement afterCurrent
// --group-reference <after>` (cmux probe P23) and ⌘N from a selected member
// with workspaceGroups.newWorkspacePlacement afterCurrent; no write is counted.
func (fw *folderWorld) bornNextTo(ws, after string) {
	gr := fw.groupOf(after)
	if gr == nil {
		panic("bornNextTo: " + after + " is in no group")
	}
	fw.add(win, ws, ws)
	order := fw.windows[win]
	order = order[:len(order)-1] // add appended it at the end
	fw.windows[win] = slices.Insert(order, slices.Index(order, after)+1, ws)
	gr.Members = slices.Insert(gr.Members, slices.Index(gr.Members, after)+1, ws)
}

// adds counts the recorded group-add calls aimed at ws.
func (fw *folderWorld) adds(ws string) int {
	n := 0
	for _, c := range fw.calls {
		if strings.HasPrefix(c, "group-add ") && strings.HasSuffix(c, " "+ws) {
			n++
		}
	}
	return n
}

// pin is a pin by hand, as cmux renders it (cmux probe P24 e and its control):
// a group member lands last of its group's pinned block (right after the
// anchor when the group has none yet), a free workspace at the head of the
// window. Already pinned: nothing moves.
func (fw *folderWorld) pin(ws string) { fw.setPinned(ws, true) }

// setPinned flips ws's pin; a pin moves it as pin says, an unpin leaves it
// where it is (P24 d, f).
func (fw *folderWorld) setPinned(ws string, pinned bool) {
	w := fw.ws[ws]
	if w == nil {
		return
	}
	was := w.Pinned
	w.Pinned = pinned
	wid := fw.windowOf(ws)
	if !pinned || was || wid == "" {
		return
	}
	defer fw.on(wid)()
	cw := fw.cw()
	order := slices.DeleteFunc(slices.Clone(fw.windows[cw]), func(x string) bool { return x == ws })
	gr := fw.groupOf(ws)
	switch {
	case gr == nil: // free: the head of the window
		fw.windows[cw] = slices.Insert(order, 0, ws)
	case gr.AnchorID == ws: // a group header: not measured, left in place
		return
	default: // last of the group's pinned block
		at := slices.Index(order, gr.AnchorID) + 1
		for at < len(order) && slices.Contains(gr.Members, order[at]) && fw.ws[order[at]] != nil && fw.ws[order[at]].Pinned {
			at++
		}
		fw.windows[cw] = slices.Insert(order, at, ws)
	}
	fw.normalize()
}

// blockSeq is windowID's sequence of blocks and free rows, in display order:
// a group's id once per block, a free workspace's id. A folder that rises
// above another folder or above a free row changes it.
func (fw *folderWorld) blockSeq(windowID string) []string {
	defer fw.on(windowID)()
	var out []string
	for _, ws := range fw.windows[fw.cw()] {
		id := ws
		if gr := fw.groupOf(ws); gr != nil {
			id = gr.ID
		}
		if len(out) == 0 || out[len(out)-1] != id {
			out = append(out, id)
		}
	}
	return out
}

// write records a folder write; one aimed at a window other than win carries
// « @<window> » last.
func (fw *folderWorld) write(windowID string, call ...string) error {
	rec := call
	if windowID != win {
		rec = append(slices.Clone(call), "@"+windowID)
	}
	if err := fw.rec(rec...); err != nil {
		return err
	}
	if fw.failOn != "" && call[0] == fw.failOn {
		return fmt.Errorf("cmux %s: exit status 1: Error: injected", call[0])
	}
	return nil
}

func (fw *folderWorld) wrote(call ...string) {
	fw.normalize()
	fw.lastWriteAt = fw.now()
	if fw.onWrite != nil {
		fw.onWrite(strings.Join(call, " "))
	}
}

// normalize keeps members in display order, anchor first (as cmux lists them).
func (fw *folderWorld) normalize() {
	order := fw.windows[fw.cw()]
	for i := range fw.groups {
		gr := &fw.groups[i]
		rest := slices.DeleteFunc(slices.Clone(gr.Members), func(x string) bool { return x == gr.AnchorID })
		slices.SortStableFunc(rest, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
		gr.Members = append([]string{gr.AnchorID}, rest...)
	}
}

// contiguous makes every group a block at its first member's place, anchor
// first (P4a, P10: cmux hands a member back to its block).
func (fw *folderWorld) contiguous() {
	order := fw.windows[fw.cw()]
	var out []string
	done := map[string]bool{}
	for _, ws := range order {
		if done[ws] {
			continue
		}
		gr := fw.groupOf(ws)
		if gr == nil {
			out = append(out, ws)
			done[ws] = true
			continue
		}
		rest := slices.DeleteFunc(slices.Clone(gr.Members), func(x string) bool { return x == gr.AnchorID })
		slices.SortStableFunc(rest, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
		for _, m := range append([]string{gr.AnchorID}, rest...) {
			if !done[m] {
				out = append(out, m)
				done[m] = true
			}
		}
	}
	fw.windows[fw.cw()] = out
	fw.normalize()
}

// sleep is Deps.Sleep: no wait, onSleep runs in its place.
func (fw *folderWorld) sleep(time.Duration) {
	if fw.onSleep != nil {
		fw.onSleep()
	}
}

func (fw *folderWorld) ListWindows() ([]cmux.Window, error) {
	ws, err := fw.cmuxWorld.ListWindows()
	if err == nil && fw.windowsErr != nil {
		return nil, fw.windowsErr
	}
	if fw.windowGone {
		ws = slices.DeleteFunc(ws, func(w cmux.Window) bool { return w.ID == win })
	}
	ws = slices.DeleteFunc(ws, func(w cmux.Window) bool { return fw.goneWins[w.ID] })
	for i := range ws {
		if ws[i].ID == win {
			ws[i].Key, ws[i].SelectedWorkspaceID = fw.key, fw.selected
			continue
		}
		if wg := fw.byWin[ws[i].ID]; wg != nil {
			ws[i].Key, ws[i].SelectedWorkspaceID = wg.key, wg.selected
		}
		if fw.keyWins[ws[i].ID] {
			ws[i].Key = true
		}
	}
	return ws, err
}

func (fw *folderWorld) ListWorkspaces(windowID string) ([]cmux.WorkspaceInfo, error) {
	ws, err := fw.cmuxWorld.ListWorkspaces(windowID)
	for i := range ws {
		ws[i].Index = i
	}
	return ws, err
}

// ListSurfaces: a workspace with no pane recorded has one, "S-<id>".
func (fw *folderWorld) ListSurfaces(windowID string) (map[string][]string, error) {
	out, err := fw.cmuxWorld.ListSurfaces(windowID)
	for id, s := range out {
		if len(s) == 0 {
			out[id] = []string{"S-" + id}
		}
	}
	return out, err
}

func (fw *folderWorld) shellAtPrompt(surface string) (bool, error) {
	if fw.shellErr != nil {
		return false, fw.shellErr
	}
	n := fw.shellCalls[surface]
	fw.shellCalls[surface]++
	if after, ok := fw.busyAfter[surface]; ok && n >= after {
		return false, nil
	}
	return !fw.busyShell[surface], nil
}

func (fw *folderWorld) surfaceNeverRendered(surface string) (bool, error) {
	if fw.renderErr != nil {
		return false, fw.renderErr
	}
	n := fw.renderCalls[surface]
	fw.renderCalls[surface]++
	if after, ok := fw.startedAfter[surface]; ok && n >= after {
		return false, nil
	}
	return fw.neverRendered[surface], nil
}

// --- cmux.Folders --------------------------------------------------------

func (fw *folderWorld) ListGroups(windowID string) ([]cmux.Group, error) {
	if err := fw.rec("group-list", windowID); err != nil {
		return nil, err
	}
	defer fw.on(windowID)()
	now := fw.now()
	fw.groupListsAt = append(fw.groupListsAt, now)
	if !fw.lastWriteAt.IsZero() && now.Sub(fw.lastWriteAt) < 2*time.Second {
		fw.tooSoon++
	}
	out := []cmux.Group{}
	for _, gr := range fw.groups {
		if at, ok := fw.createdAt[gr.ID]; ok && (fw.neverList || now.Sub(at) < fw.listLag) {
			continue
		}
		if fw.hidden[gr.ID] > 0 {
			fw.hidden[gr.ID]--
			continue
		}
		gr.Members = slices.Clone(gr.Members)
		out = append(out, gr)
	}
	fw.lagged(out, now)
	return out, nil
}

func (fw *folderWorld) CreateGroup(windowID, name string) (cmux.Group, error) {
	if err := fw.write(windowID, "group-create", name); err != nil {
		return cmux.Group{}, err
	}
	defer fw.on(windowID)()
	fw.nextID++
	id, anchor := fmt.Sprintf("GRP-%03d", fw.nextID), fmt.Sprintf("GEN-%03d", fw.nextID)
	cw := fw.cw()
	fw.add(cw, anchor, name)
	if fw.createAtTop {
		fw.windows[cw] = append([]string{anchor}, slices.DeleteFunc(fw.windows[cw], func(x string) bool { return x == anchor })...)
	}
	listed := name
	if fw.renameOnCreate != nil {
		listed = fw.renameOnCreate(name)
	}
	out := cmux.Group{ID: id, Name: listed, AnchorID: anchor, AnchorGenerated: !fw.createNotGen, Members: []string{anchor}}
	stored := out
	if fw.renameAfterCreate != nil {
		stored.Name = fw.renameAfterCreate(name)
	}
	fw.groups = append(fw.groups, stored)
	fw.createdAt[id] = fw.now()
	if fw.hideReads > 0 {
		fw.hidden[id] = fw.hideReads
	}
	if fw.createOmitsName {
		out.Name = ""
	}
	fw.wrote("group-create", name)
	return out, nil
}

func (fw *folderWorld) AddToGroup(windowID, groupID, ws string) error {
	if err := fw.write(windowID, "group-add", groupID, ws); err != nil {
		return err
	}
	defer fw.on(windowID)()
	gr := fw.group(groupID)
	if gr == nil {
		return fmt.Errorf("cmux group-add: unknown group %s", groupID)
	}
	fw.lagFrom(ws)
	if from := fw.groupOf(ws); from != nil {
		if from.AnchorID == ws {
			return errors.New("Error: invalid_state: Workspace is the anchor of another group; ungroup it first") // P3
		}
		if fw.addNeedsRemove {
			return errors.New("Error: workspace already in a group") // P2 (b)
		}
		from.Members = slices.DeleteFunc(from.Members, func(x string) bool { return x == ws })
	}
	gr = fw.group(groupID)
	cw := fw.cw()
	last := gr.Members[len(gr.Members)-1]
	fromAbove := slices.Index(fw.windows[cw], ws) < slices.Index(fw.windows[cw], gr.AnchorID)
	gr.Members = append(gr.Members, ws)
	if gr.Collapsed && ws == fw.sel() {
		gr.Collapsed = false // P2b
	}
	order := slices.DeleteFunc(slices.Clone(fw.windows[cw]), func(x string) bool { return x == ws })
	if w := fw.ws[ws]; w != nil && w.Pinned && cmux.AddPinnedLandsAtHead { // P24 a, b: last of the group's pinned block, still pinned
		at := slices.Index(order, gr.AnchorID) + 1
		for at < len(order) && slices.Contains(gr.Members, order[at]) && fw.ws[order[at]] != nil && fw.ws[order[at]].Pinned {
			at++
		}
		fw.windows[cw] = slices.Insert(order, at, ws)
		fw.normalize()
	} else if fw.addKeepsSide && fromAbove { // P19: from above, right after the anchor
		fw.windows[cw] = slices.Insert(order, slices.Index(order, gr.AnchorID)+1, ws)
		fw.normalize()
	} else {
		fw.windows[cw] = slices.Insert(order, slices.Index(order, last)+1, ws)
	}
	fw.wrote("group-add", groupID, ws)
	return nil
}

func (fw *folderWorld) RemoveFromGroup(windowID, ws string) error {
	if err := fw.write(windowID, "group-remove", ws); err != nil {
		return err
	}
	defer fw.on(windowID)()
	fw.lagFrom(ws)
	if gr := fw.groupOf(ws); gr != nil && gr.AnchorID != ws {
		gr.Members = slices.DeleteFunc(gr.Members, func(x string) bool { return x == ws })
	}
	fw.wrote("group-remove", ws)
	return nil
}

func (fw *folderWorld) Ungroup(windowID, groupID string, removeAnchor bool) error {
	call := []string{"ungroup", groupID}
	if removeAnchor {
		call = append(call, "--remove-generated-anchor")
	}
	if err := fw.write(windowID, call...); err != nil {
		return err
	}
	defer fw.on(windowID)()
	gr := fw.group(groupID)
	if gr == nil {
		return fmt.Errorf("cmux ungroup: unknown group %s", groupID)
	}
	if removeAnchor {
		if !gr.AnchorGenerated || len(gr.Members) != 1 {
			return errors.New("Error: --remove-generated-anchor on a group with members")
		}
		fw.close(gr.AnchorID)
	}
	fw.groups = slices.DeleteFunc(fw.groups, func(x cmux.Group) bool { return x.ID == groupID })
	fw.wrote(call...)
	return nil
}

func (fw *folderWorld) RenameGroup(windowID, groupID, name string) error {
	if err := fw.write(windowID, "group-rename", groupID, name); err != nil {
		return err
	}
	defer fw.on(windowID)()
	gr := fw.group(groupID)
	if gr == nil {
		return fmt.Errorf("cmux rename: unknown group %s", groupID)
	}
	gr.Name = name // P20a: nothing else moves
	fw.wrote("group-rename", groupID, name)
	return nil
}

func (fw *folderWorld) Collapse(windowID, groupID string) error {
	if err := fw.write(windowID, "group-collapse", groupID); err != nil {
		return err
	}
	defer fw.on(windowID)()
	if gr := fw.group(groupID); gr != nil {
		gr.Collapsed = true
	}
	fw.wrote("group-collapse", groupID)
	return nil
}

func (fw *folderWorld) Expand(windowID, groupID string) error {
	if err := fw.write(windowID, "group-expand", groupID); err != nil {
		return err
	}
	defer fw.on(windowID)()
	if gr := fw.group(groupID); gr != nil {
		gr.Collapsed = false
	}
	fw.wrote("group-expand", groupID)
	return nil
}

func (fw *folderWorld) SetIcon(windowID, groupID, symbol string) error {
	if err := fw.write(windowID, "set-icon", groupID, symbol); err != nil {
		return err
	}
	defer fw.on(windowID)()
	if gr := fw.group(groupID); gr != nil {
		gr.Icon = symbol
		if fw.dropIcons[symbol] {
			gr.Icon = "" // P11: accepted, stored null
		}
	}
	fw.wrote("set-icon", groupID, symbol)
	return nil
}

func (fw *folderWorld) Reorder(windowID string, order []string) error {
	if err := fw.write(windowID, "reorder", strings.Join(order, ",")); err != nil {
		return err
	}
	defer fw.on(windowID)()
	cw := fw.cw()
	cur := fw.windows[cw]
	if len(order) != len(cur) {
		return fmt.Errorf("cmux reorder: %d ids for %d workspaces", len(order), len(cur))
	}
	for _, id := range order {
		if !slices.Contains(cur, id) {
			return fmt.Errorf("cmux reorder: unknown workspace %s", id)
		}
	}
	fw.reorderFrom = append(fw.reorderFrom, slices.Clone(cur))
	switch {
	case fw.blocksFixed:
		fw.windows[cw] = reorderAs(ArrangeInput{Order: cur, Groups: fw.groups, GroupMoves: true}, order)
	case cmux.ReorderLiftsPinnedGroups:
		// cmux probe P26 a: cmux's planner puts the pinned rows first (the row's
		// own pin, never its group's), the others after, each in the order
		// asked; contiguous() then glues a group at its first member's place,
		// so a group holding a pinned member rises with it (as P10).
		pinned := func(id string) bool { return fw.ws[id] != nil && fw.ws[id].Pinned }
		lifted := slices.DeleteFunc(slices.Clone(order), func(id string) bool { return !pinned(id) })
		fw.windows[cw] = append(lifted, slices.DeleteFunc(slices.Clone(order), pinned)...)
	default:
		fw.windows[cw] = slices.Clone(order)
	}
	fw.contiguous()
	if !fw.blocksFixed && cmux.ReorderLiftsPinnedGroups {
		// P26 a: a pinned group (« Home ») keeps the head of the window even
		// when the planner sends a pinned row above it: its blocks first.
		var head, rest []string
		for _, ws := range fw.windows[cw] {
			if gr := fw.groupOf(ws); gr != nil && gr.Pinned {
				head = append(head, ws)
			} else {
				rest = append(rest, ws)
			}
		}
		fw.windows[cw] = append(head, rest...)
	}
	fw.wrote("reorder")
	return nil
}

// SetPinned is cmux's `workspace-action --action pin|unpin` (Task 7): it
// records the call and pins (moving the row as fw.pin, P24 e) or unpins it
// in place; never a folder write.
func (fw *folderWorld) SetPinned(ws string, pinned bool) error {
	verb := "unpin"
	if pinned {
		verb = "pin"
	}
	if err := fw.rec(verb, ws); err != nil {
		return err
	}
	fw.setPinned(ws, pinned)
	return nil
}

// PlaceWorkspace is cmux's `reorder-workspace --workspace ws --after|--before
// ref` (D39, cmux probe P26 b): ws moves alone, right after (before) ref, a
// member or the anchor of ws's own group; block order and memberships do not
// change, a collapsed group stays collapsed (P26 d). Aimed above a pinned
// member of its group, an unpinned ws lands first of the unpinned members
// (P26 c). Any other ref (unknown, ws itself, outside ws's group) or a ws
// that is free or an anchor fails loudly: the engine never asks for it.
func (fw *folderWorld) PlaceWorkspace(windowID, ws, ref string, before bool) error {
	where := "--after"
	if before {
		where = "--before"
	}
	if err := fw.write(windowID, "place", ws, where, ref); err != nil {
		return err
	}
	defer fw.on(windowID)()
	cw := fw.cw()
	gr := fw.groupOf(ws)
	switch {
	case !slices.Contains(fw.windows[cw], ws):
		return fmt.Errorf("cmux reorder-workspace: unknown workspace %s", ws)
	case ws == ref:
		return fmt.Errorf("cmux reorder-workspace: %s placed relative to itself", ws)
	case gr == nil || gr.AnchorID == ws:
		return fmt.Errorf("cmux reorder-workspace: %s is not a group member", ws)
	case !slices.Contains(gr.Members, ref):
		return fmt.Errorf("cmux reorder-workspace: %s is outside the group of %s", ref, ws)
	}
	order := slices.DeleteFunc(slices.Clone(fw.windows[cw]), func(x string) bool { return x == ws })
	at := slices.Index(order, ref)
	if !before {
		at++
	}
	floor := slices.Index(order, gr.AnchorID) + 1 // never above its own anchor
	if w := fw.ws[ws]; w == nil || !w.Pinned {    // P26 c: never above a pinned member
		for _, m := range gr.Members {
			if p := fw.ws[m]; m != gr.AnchorID && p != nil && p.Pinned {
				floor = max(floor, slices.Index(order, m)+1)
			}
		}
	}
	fw.windows[cw] = slices.Insert(order, max(at, floor), ws)
	fw.wrote("place", ws)
	return nil
}

func (fw *folderWorld) MoveGroup(windowID, groupID, ref string, before bool) error {
	where := "--after"
	if before {
		where = "--before"
	}
	if err := fw.write(windowID, "group-move", groupID, where, ref); err != nil {
		return err
	}
	defer fw.on(windowID)()
	g, r := fw.group(groupID), fw.group(ref)
	if g == nil || r == nil {
		return errors.New("cmux group-move: unknown group")
	}
	cw := fw.cw()
	st := ArrangeInput{Order: fw.windows[cw], Groups: fw.groups}
	block := blockOf(st, *g)
	order := slices.DeleteFunc(slices.Clone(fw.windows[cw]), func(x string) bool { return slices.Contains(block, x) })
	refBlock := blockOf(ArrangeInput{Order: order, Groups: fw.groups}, *r)
	at := slices.Index(order, refBlock[0])
	if !before {
		at = slices.Index(order, refBlock[len(refBlock)-1]) + 1
	}
	fw.windows[cw] = slices.Insert(order, at, block...)
	fw.wrote("group-move", groupID)
	return nil
}

// --- the store seam ------------------------------------------------------

// failReads is folderReads over the real :memory: store, failing on the named
// read only (distinct from failOn, which aims at cmux folder calls).
type failReads struct {
	st                                        *store.Store
	peers, theme, subject, bindings, offTopic error
}

func (f *failReads) ListOffTopic() ([]pigtail.WorkspaceOffTopic, error) {
	if f.offTopic != nil {
		return nil, f.offTopic
	}
	return f.st.ListOffTopic()
}

func (f *failReads) ListPeers(includeEnded bool) ([]pigtail.PeerSession, error) {
	if f.peers != nil {
		return nil, f.peers
	}
	return f.st.ListPeers(includeEnded)
}

func (f *failReads) GetTheme(id string) (*pigtail.Theme, error) {
	if f.theme != nil {
		return nil, f.theme
	}
	return f.st.GetTheme(id)
}

func (f *failReads) GetSubject(id string) (*pigtail.Subject, error) {
	if f.subject != nil {
		return nil, f.subject
	}
	return f.st.GetSubject(id)
}

func (f *failReads) ListAllCmuxBindings() ([]pigtail.SubjectCmuxBinding, error) {
	if f.bindings != nil {
		return nil, f.bindings
	}
	return f.st.ListAllCmuxBindings()
}

// --- the harness ---------------------------------------------------------

type folderHarness struct {
	*harness
	fw        *folderWorld
	reads     *failReads
	statePath string
	cfg       Config
	opts      []func(*Config, *Deps)
	logs      []string
	spaces    map[string]string // space → subject id
	// focus is what Deps.Focus serves, focusErr its read error; focusRead,
	// when set, replaces both (FocusSource over the real store).
	focus     []pigtail.Focus
	focusErr  error
	focusRead func() ([]pigtail.Focus, error)
	// standbyPinned records each Deps.StandbyPinned call (D29) as
	// "<session> <workspace>".
	standbyPinned []string
}

// newFolderHarness: Config{Enabled, Folders} on one window, cmux not key.
func newFolderHarness(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	return newFolderHarnessOn(t, ":memory:", opts...)
}

// newFolderHarnessOn is newFolderHarness on the store at dbPath.
func newFolderHarnessOn(t *testing.T, dbPath string, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	h := newHarnessOn(t, Config{}, dbPath)
	fw := newFolderWorld(h.world, func() time.Time { return h.now })
	fw.windows[win] = nil
	fh := &folderHarness{harness: h, fw: fw, reads: &failReads{st: h.st}, opts: opts,
		statePath: filepath.Join(t.TempDir(), "cmux-folders.json"), spaces: map[string]string{}}
	fh.restart()
	return fh, fw
}

// restart builds a new Runner on the same store, world and state file (a
// daemon restart).
func (fh *folderHarness) restart() {
	cfg := Config{Enabled: true, Folders: true}
	deps := Deps{
		Sidebar:              fh.fw,
		Store:                fh.st,
		Alive:                func(p pigtail.PeerSession) bool { return fh.alive[p.SessionID] },
		Now:                  func() time.Time { return fh.now },
		Sleep:                fh.fw.sleep,
		Logf:                 fh.logf,
		Folders:              fh.fw,
		FolderOrder:          func() []string { return slices.Clone(testFolderOrder) },
		FolderIcon:           func(f string) string { return testFolderIcon[f] },
		ShellAtPrompt:        fh.fw.shellAtPrompt,
		SurfaceNeverRendered: fh.fw.surfaceNeverRendered,
		LoadCollapse:         func() (map[string]bool, error) { return LoadCollapseFile(fh.statePath) },
		SaveCollapse:         func(m map[string]bool) error { return SaveCollapseFile(fh.statePath, m) },
		FolderReads:          fh.reads,
		Standby:              func() (map[string]bool, error) { return fh.standbyRows() },
		StandbyPinned:        fh.endStandbyPinned,
		Focus:                fh.readFocus,
	}
	for _, o := range fh.opts {
		o(&cfg, &deps)
	}
	fh.cfg = cfg
	fh.r = New(cfg, deps)
}

// subjectIn returns a subject whose theme is in space sp (created once).
func (fh *folderHarness) subjectIn(sp string) string {
	fh.t.Helper()
	if id, ok := fh.spaces[sp]; ok {
		return id
	}
	n := len(fh.spaces) + 1
	thm, sub := fmt.Sprintf("thm_f%d", n), fmt.Sprintf("sub_f%d", n)
	if err := fh.st.CreateTheme(&pigtail.Theme{ID: thm, Name: "T" + sp, Kind: "ongoing", Scope: "perso", VaultLink: "Notes/Areas/f" + thm,
		Space: sp, CreatedAt: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
	if err := fh.st.CreateSubject(&pigtail.Subject{ID: sub, ThemeID: thm, Title: "S " + sp, ShortTitle: "S " + sp,
		Slug: sub, Status: pigtail.StatusInbox, CreatedAt: fh.now, UpdatedAt: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
	fh.spaces[sp] = sub
	return sub
}

// setSubjectStatus changes subject id's status through Store.UpdateSubject,
// the daemon's own path (never a made-up state).
func (fh *folderHarness) setSubjectStatus(id, status string) {
	fh.t.Helper()
	sub, err := fh.st.GetSubject(id)
	if err != nil {
		fh.t.Fatal(err)
	}
	sub.Status = status
	if err := fh.st.UpdateSubject(sub); err != nil {
		fh.t.Fatal(err)
	}
}

// endStandbyPinned is Deps.StandbyPinned as the wiring without a relay: the
// row, plus the relay's log line (EndStandbyPinned).
func (fh *folderHarness) endStandbyPinned(session, workspace string) error {
	fh.standbyPinned = append(fh.standbyPinned, session+" "+workspace)
	if _, err := fh.st.ClearStandby(session, fh.now); err != nil {
		return err
	}
	fh.logf("relay: standby cleared by pin: %s (workspace %s)", session, workspace)
	return nil
}

// bindIn binds ws to a subject of space sp.
func (fh *folderHarness) bindIn(ws, sp string) {
	fh.t.Helper()
	if err := fh.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: fh.subjectIn(sp), Source: "attach", BoundAt: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
}

// moveTo re-binds ws to a subject of space sp (its session changes space).
func (fh *folderHarness) moveTo(ws, sp string) {
	fh.t.Helper()
	if err := fh.st.ForgetCmuxWorkspace(ws); err != nil {
		fh.t.Fatal(err)
	}
	fh.bindIn(ws, sp)
}

func (fh *folderHarness) setThemeSpace(themeID, sp string) {
	fh.t.Helper()
	if err := fh.st.UpdateTheme(themeID, pigtail.ThemeUpdate{Space: &sp}); err != nil {
		fh.t.Fatal(err)
	}
}

// sessionIn opens workspace ws with a live Claude session bound to space sp.
func (fh *folderHarness) sessionIn(ws, sp string) {
	fh.t.Helper()
	fh.fw.add(win, ws, ws)
	fh.session("s-"+ws, ws, "idle", true)
	fh.bindIn(ws, sp)
}

// sessionOn opens workspace ws in windowID with a live Claude session bound
// to space sp.
func (fh *folderHarness) sessionOn(windowID, ws, sp string) {
	fh.t.Helper()
	fh.fw.add(windowID, ws, ws)
	fh.session("s-"+ws, ws, "idle", true)
	fh.bindIn(ws, sp)
}

// sessionWorktreeOnly registers a row whose pigtail worktree id is pigtailWS
// and whose cmux workspace is unknown: it names no cmux workspace.
func (fh *folderHarness) sessionWorktreeOnly(id, pigtailWS string) {
	fh.t.Helper()
	if err := fh.st.UpsertPeer(&pigtail.PeerSession{SessionID: id, WorkspaceID: pigtailWS, Status: "idle", UpdatedAt: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
}

func (fh *folderHarness) wait(sessionID, typ string) {
	fh.t.Helper()
	if err := fh.st.SetSessionWait(sessionID, pigtail.SessionWait{Type: typ, Source: pigtail.WaitSourceDeclared, Since: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
}

func (fh *folderHarness) clearWait(sessionID string) {
	fh.t.Helper()
	if _, err := fh.st.ClearSessionWaits(sessionID, pigtail.WaitSourceDeclared); err != nil {
		fh.t.Fatal(err)
	}
}

// converge steps 3 s (> ArrangeSettle, FocusPoll) n times.
func (fh *folderHarness) converge(n int) {
	for i := 0; i < n; i++ {
		fh.step(3 * time.Second)
	}
}

// converged: one more round asks nothing.
func (fh *folderHarness) assertQuiet(what string) {
	fh.t.Helper()
	before := len(fh.fw.folderWrites())
	fh.converge(3)
	fh.step(31 * time.Second) // a slow listing re-verifies
	fh.converge(2)
	if w := fh.fw.folderWrites(); len(w) != before {
		fh.t.Fatalf("%s: not converged, new writes %q", what, w[before:])
	}
}

// offTopic declares ws off-topic.
func (fh *folderHarness) offTopic(ws string) {
	fh.t.Helper()
	if err := fh.st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: "tst", DeclaredAt: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
}

// offTopicAs declares ws off-topic with its scope, explicit space and reason
// (D1, sub_x000000000000019), as a row written before 0024 or by declare.
func (fh *folderHarness) offTopicAs(ws, sc, sp, reason string) {
	fh.t.Helper()
	if err := fh.st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: sc, Space: sp, Reason: reason, DeclaredAt: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
}

// pastGrace advances the clock by BirthGrace + 1 s in one pass (the runner's
// config after defaults, not fh.cfg).
func (fh *folderHarness) pastGrace() { fh.step(fh.r.cfg.BirthGrace + time.Second) }

// winSt is win's arrangement state (made if absent).
func (fh *folderHarness) winSt() *winFolders { return fh.r.winState(win) }

func (fh *folderHarness) index(ws string) int { return slices.Index(fh.fw.windows[win], ws) }

// standbyRows is Deps.Standby over the real store, as the wiring reads it.
func (fh *folderHarness) standbyRows() (map[string]bool, error) {
	rows, err := fh.st.ListStandby()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.SessionID] = true
	}
	return out, nil
}

// putStandby puts the sessions s-<ws> in Standby.
func (fh *folderHarness) putStandby(wss ...string) {
	fh.t.Helper()
	for _, ws := range wss {
		if err := fh.st.SetStandby("s-"+ws, fh.now); err != nil {
			fh.t.Fatal(err)
		}
	}
}

// leaveStandby ends the Standby of the sessions s-<ws>.
func (fh *folderHarness) leaveStandby(wss ...string) {
	fh.t.Helper()
	for _, ws := range wss {
		if _, err := fh.st.ClearStandby("s-"+ws, fh.now); err != nil {
			fh.t.Fatal(err)
		}
	}
}

// members is the plain members of the group named name, in display order.
func (fw *folderWorld) members(name string) []string {
	gr := fw.groupNamed(name)
	if gr == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(gr.Members), func(m string) bool { return m == gr.AnchorID })
}

func (fh *folderHarness) logf(f string, a ...any) { fh.logs = append(fh.logs, fmt.Sprintf(f, a...)) }

// readFocus is Deps.Focus: focusRead when set, else focus / focusErr.
func (fh *folderHarness) readFocus() ([]pigtail.Focus, error) {
	if fh.focusRead != nil {
		return fh.focusRead()
	}
	if fh.focusErr != nil {
		return nil, fh.focusErr
	}
	return slices.Clone(fh.focus), nil
}

// focusOn opens a focus named name (served by Deps.Focus, newest last).
func (fh *folderHarness) focusOn(name string, members ...pigtail.FocusMember) {
	n := len(fh.focus) + 1
	fh.focus = append(fh.focus, pigtail.Focus{ID: fmt.Sprintf("fcs_t%d", n), Name: name, Scope: "perso",
		CreatedAt: fh.now.Add(time.Duration(n) * time.Millisecond), Members: members})
}

// focusOff ends the focus named name (removed from what Deps.Focus serves).
func (fh *folderHarness) focusOff(name string) {
	fh.focus = slices.DeleteFunc(fh.focus, func(f pigtail.Focus) bool { return f.Name == name })
}

// storeFocus makes Deps.Focus read the real store through FocusSource, with
// the harness clock and logs (never a filter written in the test).
func (fh *folderHarness) storeFocus() {
	fh.focusRead = FocusSource(fh.st.ActiveFocus, func() time.Time { return fh.now }, fh.logf)
}

// themeOf is the theme of the subject subjectIn(sp) gives.
func (fh *folderHarness) themeOf(sp string) string {
	fh.t.Helper()
	sub, err := fh.st.GetSubject(fh.subjectIn(sp))
	if err != nil {
		fh.t.Fatal(err)
	}
	return sub.ThemeID
}

// setSubjectSpace gives subject sub its own space sp (D5 of sub_x000000000000033),
// through the store's traced write; any name is accepted, known or not.
func (fh *folderHarness) setSubjectSpace(sub, sp string) {
	fh.t.Helper()
	if _, err := fh.st.SetSubjectSpaceByDrag(sub, sp, "", fh.now); err != nil {
		fh.t.Fatal(err)
	}
}

// The fake cmux's SetPinned (Task 7) records `pin`/`unpin` and updates the
// row's Pinned, as cmux's `workspace-action --action pin|unpin` does; it is
// not a folder write (pins stay out of folderVerbs).
func TestFolderWorldSetPinnedRecords(t *testing.T) {
	t.Parallel()
	fw := newFolderWorld(newWorld(), time.Now)
	fw.add(win, "WS-P", "p")
	if err := fw.SetPinned("WS-P", true); err != nil {
		t.Fatal(err)
	}
	if !fw.ws["WS-P"].Pinned || fw.count("pin") != 1 {
		t.Fatalf("after pin: pinned=%v calls=%q", fw.ws["WS-P"].Pinned, fw.calls)
	}
	if err := fw.SetPinned("WS-P", false); err != nil {
		t.Fatal(err)
	}
	if fw.ws["WS-P"].Pinned || fw.count("unpin") != 1 || fw.count("pin", "unpin") != 2 {
		t.Fatalf("after unpin: pinned=%v calls=%q", fw.ws["WS-P"].Pinned, fw.calls)
	}
	if w := fw.folderWrites(); len(w) != 0 {
		t.Fatalf("a pin counted as a folder write: %q", w)
	}
}
