package cmuxsync

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store/storetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Focus folders in the arrangement (Tâche 5, sub_x000000000000007): a focus is
// one more pigtail folder, on top under the pinned groups (D4, focus D20),
// an overlay of the theme's space (D1), against the simulated cmux.

const offsite = "Workshop-T"

// focusWorld: WS-M in « Hedge », WS-S in « Terrace », converged and quiet.
func focusWorld(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	h, fw := newFolderHarness(t, opts...)
	h.sessionIn("WS-M", "Hedge")
	h.sessionIn("WS-S", "Terrace")
	h.converge(10)
	h.assertQuiet("focus world")
	if fw.folderOf("WS-M") != "Hedge" || fw.folderOf("WS-S") != "Terrace" {
		t.Fatalf("world not filed: %+v", fw.groups)
	}
	return h, fw
}

// subjectOfTheme adds subject id to theme themeID.
func (fh *folderHarness) subjectOfTheme(themeID, id string) {
	fh.t.Helper()
	if err := fh.st.CreateSubject(&pigtail.Subject{ID: id, ThemeID: themeID, Title: "S " + id, ShortTitle: "S " + id,
		Slug: id, Status: pigtail.StatusInbox, CreatedAt: fh.now, UpdatedAt: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
}

// sessionOf opens workspace ws with a live session bound to subject sub.
func (fh *folderHarness) sessionOf(ws, sub string) {
	fh.t.Helper()
	fh.fw.add(win, ws, ws)
	fh.session("s-"+ws, ws, "idle", true)
	if err := fh.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: sub, Source: "attach", BoundAt: fh.now}); err != nil {
		fh.t.Fatal(err)
	}
}

// writesCiting are the folder writes that name id.
func writesCiting(calls []string, id string) []string {
	var out []string
	for _, c := range folderWritesIn(calls) {
		if slices.Contains(strings.Fields(c), id) {
			out = append(out, c)
		}
	}
	return out
}

func TestFocusFolderBornOnTop(t *testing.T) { // D4
	t.Parallel()
	h, fw := focusWorld(t)
	sys := fw.groupNamed("Terrace").ID
	mark := len(fw.calls)
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(10)
	gr := fw.groupNamed(offsite)
	if gr == nil || !gr.AnchorGenerated || gr.Icon != space.FocusIcon {
		t.Fatalf("focus folder = %+v, want a generated group with %q", gr, space.FocusIcon)
	}
	if got := fw.members(offsite); !slices.Equal(got, []string{"WS-M"}) {
		t.Fatalf("focus members = %q, want [WS-M]", got)
	}
	if fw.groupNamed("Hedge") != nil {
		t.Fatalf("« Hedge » emptied must go (R4): %+v", fw.groups)
	}
	if fw.folderOf("WS-S") != "Terrace" || fw.group(sys) == nil {
		t.Fatalf("« Terrace » moved: %+v", fw.groups)
	}
	calls := fw.calls[mark:]
	created := slices.IndexFunc(calls, func(c string) bool { return c == "group-create "+offsite })
	if created < 0 {
		t.Fatalf("no group-create %s: %q", offsite, folderWritesIn(calls))
	}
	moved := slices.IndexFunc(calls[created:], func(c string) bool {
		return strings.HasPrefix(c, "group-move "+gr.ID+" --before ")
	})
	if moved < 0 {
		t.Fatalf("the creation round places the focus before the first folder: %q", folderWritesIn(calls[created:]))
	}
	if got := folderNames(fw); len(got) == 0 || got[0] != offsite {
		t.Fatalf("folder order %q, want %s on top", got, offsite)
	}
	h.assertQuiet("focus filed")
}

func TestFocusFolderBornUnderPinned(t *testing.T) { // focus D20
	t.Parallel()
	t.Run("own folders below the pinned group", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		fw.manualGroup("PG", "Mine", "T-PG", false)
		fw.group("PG").Pinned = true
		fw.createAtTop = true
		h.sessionIn("WS-M", "Hedge")
		h.sessionIn("WS-S", "Terrace")
		h.converge(10)
		if got := folderNames(fw); !slices.Equal(got, []string{"Mine", "Hedge", "Terrace"}) {
			t.Fatalf("base order %q", got)
		}
		mark := len(fw.calls)
		h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
		h.converge(10)
		if got := folderNames(fw); !slices.Equal(got, []string{"Mine", offsite, "Terrace"}) {
			t.Fatalf("order %q, want Mine, %s, Terrace (window %q)", got, offsite, fw.windows[win])
		}
		moves := fw.count("group-move")
		h.assertQuiet("focus under pinned")
		if fw.count("group-move") != moves {
			t.Fatal("a second pass moved a group")
		}
		if got := folderNames(fw); !slices.Equal(got, []string{"Mine", offsite, "Terrace"}) {
			t.Fatalf("order after a quiet pass %q", got)
		}
		if w := append(writesCiting(fw.calls[mark:], "PG"), writesCiting(fw.calls[mark:], "T-PG")...); len(w) != 0 {
			t.Fatalf("pinned group written (D13): %q", w)
		}
	})
	t.Run("no own folder yet", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		fw.manualGroup("PG", "Mine", "T-PG", false)
		fw.group("PG").Pinned = true
		fw.createAtTop = true
		h.subjectIn("Hedge")
		h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
		h.sessionIn("WS-M", "Hedge")
		for i := 0; i < 10 && fw.groupNamed(offsite) == nil; i++ {
			h.step(3 * time.Second)
		}
		fw.key = true
		h.converge(5)
		if got := folderNames(fw); !slices.Equal(got, []string{"Mine", offsite}) {
			t.Fatalf("order after the creation round %q, want Mine then %s (window %q)", got, offsite, fw.windows[win])
		}
	})
}

func TestFocusFolderBornUnfolded(t *testing.T) { // focus D19
	t.Parallel()
	h, fw := newFolderHarness(t)
	if err := SaveCollapseFile(h.statePath, map[string]bool{offsite: true}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.sessionIn("WS-M", "Hedge")
	h.converge(10)
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(10)
	gr := fw.groupNamed(offsite)
	if gr == nil || gr.Collapsed {
		t.Fatalf("a focus folder is born expanded whatever the memory says: %+v", gr)
	}
	if n := len(writesCiting(fw.calls, gr.ID)); n == 0 || slices.Contains(fw.calls, "group-collapse "+gr.ID) {
		t.Fatalf("group-collapse sent to the newborn focus: %q", writesCiting(fw.calls, gr.ID))
	}
}

func TestFocusSubjectOverTheme(t *testing.T) { // D1
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-1", "Hedge")
	thm := h.themeOf("Hedge")
	h.subjectOfTheme(thm, "sub_two")
	h.sessionOf("WS-2", "sub_two")
	h.focusOn("A", fTheme(thm))
	h.focusOn("B", fSubject("sub_two"))
	h.converge(12)
	if fw.folderOf("WS-1") != "A" || fw.folderOf("WS-2") != "B" {
		t.Fatalf("WS-1 in %q (want A), WS-2 in %q (want B)", fw.folderOf("WS-1"), fw.folderOf("WS-2"))
	}
}

func TestFocusStandbyWins(t *testing.T) { // Review Focus 2
	t.Parallel()
	h, fw := standbyWorld(t)
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.putStandby("WS-S")
	h.converge(12)
	if fw.folderOf("WS-S") != space.Standby || fw.folderOf("WS-M") != offsite {
		t.Fatalf("WS-S in %q (want Standby), WS-M in %q (want %s)", fw.folderOf("WS-S"), fw.folderOf("WS-M"), offsite)
	}
	h.leaveStandby("WS-S")
	h.converge(12)
	if got := fw.folderOf("WS-S"); got != offsite {
		t.Fatalf("out of Standby, WS-S in %q, want the focus", got)
	}
}

func TestFocusChangeOwesARound(t *testing.T) {
	t.Parallel()
	h, fw := focusWorld(t)
	if h.winSt().dirty {
		t.Fatal("the world must be settled")
	}
	before := fw.count("group-create")
	h.focusOn(offsite, fTheme(h.themeOf("Hedge"))) // nothing else changes
	h.step(3 * time.Second)
	if fw.count("group-create") == before {
		t.Fatalf("a focus opened owed a round, none ran: %q", fw.folderWrites())
	}
}

func TestFocusSortedByGravity(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-1", "Hedge")
	h.sessionIn("WS-2", "Hedge")
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(12)
	before := fw.members(offsite)
	if len(before) != 2 {
		t.Fatalf("members = %q", before)
	}
	h.wait("s-"+before[1], "permission")
	h.converge(8)
	if got := fw.members(offsite); got[0] != before[1] {
		t.Fatalf("the grave pill does not rise: %q then %q", before, got)
	}
}

func TestFocusWithoutSessionsCreatesNoFolder(t *testing.T) { // Review Focus 4
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-S", "Terrace")
	h.focusOn(offsite, fTheme(h.themeOf("Hedge"))) // a theme with no session
	h.converge(10)
	if slices.Contains(fw.calls, "group-create "+offsite) {
		t.Fatalf("a focus without sessions got a folder: %q", fw.folderWrites())
	}
	h.sessionIn("WS-M", "Hedge") // positive control, same fixture
	h.converge(10)
	if fw.groupNamed(offsite) == nil || fw.folderOf("WS-M") != offsite {
		t.Fatalf("control: a session of the theme opened, WS-M in %q: %+v", fw.folderOf("WS-M"), fw.groups)
	}
}

func TestFocusManualGroupSameNameDissolved(t *testing.T) { // Review Focus 3
	t.Parallel()
	h, fw := focusWorld(t)
	mark := len(fw.calls)
	fw.manualGroup("MINE", offsite, "T-OFF", false) // Ada' group, named like the focus about to open
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.step(30 * time.Second)
	h.converge(12)
	// Dissolved like any foreign group (D9: after the convergence), never
	// adopted: pigtail creates its own folder and never adds to « MINE ».
	calls := fw.calls[mark:]
	ungroup := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "ungroup MINE") })
	create := slices.Index(calls, "group-create "+offsite)
	if ungroup < 0 || create < 0 || slices.Contains(calls, "group-add MINE WS-M") {
		t.Fatalf("the manual group is dissolved, pigtail's folder created: ungroup at %d, create at %d: %q", ungroup, create, folderWritesIn(calls))
	}
	gr := fw.groupNamed(offsite)
	if fw.group("MINE") != nil || gr == nil || !gr.AnchorGenerated || fw.folderOf("WS-M") != offsite {
		t.Fatalf("groups %+v, WS-M in %q", fw.groups, fw.folderOf("WS-M"))
	}
}

func TestFocusRebindFollowsNewSubject(t *testing.T) {
	t.Parallel()
	h, fw := focusWorld(t)
	h.subjectOfTheme(h.themeOf("Garden"), "sub_focus")
	h.focusOn(offsite, fSubject("sub_focus"))
	h.converge(6)
	if fw.folderOf("WS-M") != "Hedge" {
		t.Fatalf("WS-M left its space before the rebind: %q", fw.folderOf("WS-M"))
	}
	if err := h.st.ForgetCmuxWorkspace("WS-M"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: "WS-M", SubjectID: "sub_focus", Source: "attach", BoundAt: h.now}); err != nil {
		t.Fatal(err)
	}
	h.converge(12)
	if got := fw.folderOf("WS-M"); got != offsite {
		t.Fatalf("re-bound to a subject in focus, WS-M in %q", got)
	}
}

func TestFocusReadErrorSkipsRound(t *testing.T) {
	t.Parallel()
	h, fw := focusWorld(t)
	h.focusErr = errors.New("database is locked")
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.moveTo("WS-S", "Hedge") // a change a round is owed to
	mark := len(fw.calls)
	h.converge(6)
	if w := folderWritesIn(fw.calls[mark:]); len(w) != 0 {
		t.Fatalf("a focus read error read as « no focus »: %q", w)
	}
	n := 0
	for _, l := range h.logs {
		if strings.Contains(l, "round skipped, store read: focus: database is locked") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one « store read: focus: » line, got %d: %q", n, h.logs)
	}
	h.focusErr = nil
	h.converge(12)
	if fw.folderOf("WS-M") != offsite || fw.folderOf("WS-S") != offsite {
		t.Fatalf("control: readable again, the round runs: %+v", fw.groups)
	}
}

func TestFocusSealFailureStillHandsBack(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "pigtail.db")
	h, fw := newFolderHarnessOn(t, dbPath)
	h.storeFocus()
	h.sessionIn("WS-M", "Hedge")
	h.sessionIn("WS-S", "Terrace")
	ends := h.now.Add(time.Minute)
	f := pigtail.Focus{Name: offsite, Scope: "perso", EndsAt: &ends, Members: []pigtail.FocusMember{fTheme(h.themeOf("Hedge"))}}
	if err := h.st.CreateFocus(&f, h.now); err != nil {
		t.Fatal(err)
	}
	h.converge(10)
	if fw.folderOf("WS-M") != offsite {
		t.Fatalf("not in focus: %+v", fw.groups)
	}
	restore := storetest.FailFocusSeal(t, dbPath)
	h.now = h.now.Add(2 * time.Minute)
	h.step(31 * time.Second) // a slow pass
	h.converge(10)
	if fw.folderOf("WS-M") != "Hedge" || fw.groupNamed(offsite) != nil {
		t.Fatalf("past its deadline, the focus still holds: WS-M in %q, %+v", fw.folderOf("WS-M"), fw.groups)
	}
	seal := 0
	for _, l := range h.logs {
		if strings.Contains(l, "round skipped") {
			t.Fatalf("a seal error skipped a round: %q", l)
		}
		if strings.Contains(l, "focus: seal") {
			seal++
		}
	}
	if seal != 1 {
		t.Fatalf("want one « focus: seal » line over the passes, got %d: %q", seal, h.logs)
	}
	sub := h.subjectIn("Hedge")
	if lines := endLines(t, h, sub); len(lines) != 0 {
		t.Fatalf("a line written while the seal fails: %+v", lines)
	}
	restore()
	h.step(3 * time.Second)
	got, _, err := h.st.GetFocus(f.ID, h.now)
	if err != nil || got.EndedAt == nil || !got.EndedAt.Equal(ends) {
		t.Fatalf("restored, the focus is sealed at its deadline: %+v, %v", got, err)
	}
	lines := endLines(t, h, sub)
	if len(lines) != 1 || !lines[0].Timestamp.Equal(ends) {
		t.Fatalf("one end line dated the deadline, got %+v", lines)
	}
}

// endLines are the subject's focus end lines.
func endLines(t *testing.T, h *folderHarness, sub string) []pigtail.SubjectLogEntry {
	t.Helper()
	es, err := h.st.ListLog(sub, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []pigtail.SubjectLogEntry
	for _, e := range es {
		if strings.HasPrefix(e.Text, `focus "`) {
			out = append(out, e)
		}
	}
	return out
}

// Inverted from TestFocusPinnedNeverFiled (D13) by D18 of
// sub_x000000000000014: a pinned tab bound to a subject in an open focus is
// filed at the head of the focus folder (cmux puts an added pinned tab at the
// head, cmux probe P24 a, b), the unpinned one behind it.
func TestFocusPinnedFiledAtHeadOfFocus(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-P", "Hedge")
	h.sessionIn("WS-M", "Hedge")
	fw.pin("WS-P")
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(12)
	gr := fw.groupNamed(offsite)
	if gr == nil || fw.folderOf("WS-P") != offsite || len(gr.Members) < 2 || gr.Members[1] != "WS-P" {
		t.Fatalf("pinned not first of the focus: in %q, folder %+v", fw.folderOf("WS-P"), gr)
	}
	if fw.folderOf("WS-M") != offsite || slices.Index(gr.Members, "WS-M") < slices.Index(gr.Members, "WS-P") {
		t.Fatalf("the unpinned one is filed behind the pinned one: %+v", gr)
	}
}

func TestFocusKeyWaits(t *testing.T) { // D2
	t.Parallel()
	h, fw := focusWorld(t)
	fw.key = true
	mark := len(fw.calls)
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(6)
	if w := folderWritesIn(fw.calls[mark:]); len(w) != 0 {
		t.Fatalf("cmux key, yet writes: %q", w)
	}
	fw.key = false
	h.converge(10)
	if fw.groupNamed(offsite) == nil || fw.folderOf("WS-M") != offsite {
		t.Fatalf("control: not key, the focus is filed: %+v", fw.groups)
	}
}

func TestFocusFolderGetsFocusIcon(t *testing.T) { // D5, focus D19
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-M", "Hedge")
	h.sessionIn("WS-S", "Terrace")
	h.focusOn(offsite, fTheme(h.themeOf("Terrace")))
	h.converge(10)
	gr, mod := fw.groupNamed(offsite), fw.groupNamed("Hedge")
	if gr == nil || mod == nil {
		t.Fatalf("folders %+v", fw.groups)
	}
	if gr.Icon != space.FocusIcon || !slices.Contains(fw.calls, "set-icon "+gr.ID+" "+space.FocusIcon) {
		t.Fatalf("focus icon %q, calls %q", gr.Icon, writesCiting(fw.calls, gr.ID))
	}
	if mod.Icon != testFolderIcon["Hedge"] || mod.Icon == gr.Icon {
		t.Fatalf("« Hedge » icon %q", mod.Icon)
	}
}

func TestArrangeKeyWindowOutsideListHolds(t *testing.T) { // focus D11, one window
	t.Parallel()
	h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Windows = []string{win} })
	h.sessionIn("WS-S", "Terrace")
	fw.add(winC, "WS-Z", "z")
	fw.keyWins = map[string]bool{winC: true}
	h.converge(6)
	if !slices.Contains(fw.calls, "list-windows") {
		t.Fatal("instrument: the windows were never read")
	}
	if w := fw.folderWrites(); len(w) != 0 {
		t.Fatalf("a cmux window is key (outside Config.Windows), yet writes: %q", w)
	}
	fw.keyWins[winC] = false
	h.converge(10)
	if fw.folderOf("WS-S") != "Terrace" {
		t.Fatalf("control: no window key, WS-S in %q", fw.folderOf("WS-S"))
	}
}

// --- Tâche 6: a focus that ends, the toggle ------------------------------

func TestFocusEndReturnsToSpaceAndFolderGoes(t *testing.T) { // l. 7785
	t.Parallel()
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "bare anchor", true: "anchor busy at the re-proof"}[busy], func(t *testing.T) {
			h, fw := focusWorld(t)
			h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
			h.converge(12)
			gr := fw.groupNamed(offsite)
			if gr == nil || fw.folderOf("WS-M") != offsite || fw.groupNamed("Hedge") != nil {
				t.Fatalf("not in focus: %+v", fw.groups)
			}
			id, anchor := gr.ID, gr.AnchorID
			open := slices.Clone(fw.windows[win])
			surface := "S-" + anchor
			if busy { // the round's read sees it bare, the read before the op busy
				fw.busyAfter[surface] = fw.shellCalls[surface] + 1
			}
			h.focusOff(offsite)
			h.converge(12)
			if fw.folderOf("WS-M") != "Hedge" || fw.folderOf("WS-S") != "Terrace" {
				t.Fatalf("the focus ended: WS-M in %q, WS-S in %q", fw.folderOf("WS-M"), fw.folderOf("WS-S"))
			}
			if fw.group(id) != nil {
				t.Fatalf("the focus folder stays: %+v", fw.groups)
			}
			removed := slices.Contains(fw.calls, "ungroup "+id+" --remove-generated-anchor")
			if removed == busy || (h.index(anchor) < 0) == busy {
				t.Fatalf("busy=%v: anchor removed=%v open=%v: %q", busy, removed, h.index(anchor) >= 0, writesCiting(fw.calls, id))
			}
			if fw.shellCalls[surface] < 2 {
				t.Fatalf("the anchor is proved at the round and again before the op: %d shell reads", fw.shellCalls[surface])
			}
			if busy && fw.folderOf(anchor) != space.NoSubject {
				t.Fatalf("the kept anchor is filed in « Inbox », got %q", fw.folderOf(anchor))
			}
			for _, ws := range open {
				if ws != anchor && h.index(ws) < 0 {
					t.Fatalf("workspace %s closed", ws)
				}
			}
		})
	}
}

// focusInStore opens a focus on theme thm in the store, due in d.
func focusInStore(t *testing.T, h *folderHarness, thm string, d time.Duration) (pigtail.Focus, time.Time) {
	t.Helper()
	ends := h.now.Add(d)
	f := pigtail.Focus{Name: offsite, Scope: "perso", EndsAt: &ends, Members: []pigtail.FocusMember{fTheme(thm)}}
	if err := h.st.CreateFocus(&f, h.now); err != nil {
		t.Fatal(err)
	}
	return f, ends
}

func TestFocusDeadlineJudgedAtRead(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.storeFocus()
	h.sessionIn("WS-M", "Hedge")
	h.sessionIn("WS-S", "Terrace")
	focusInStore(t, h, h.themeOf("Hedge"), time.Minute)
	h.converge(10)
	if fw.folderOf("WS-M") != offsite {
		t.Fatalf("not in focus: %+v", fw.groups)
	}
	h.now = h.now.Add(2 * time.Minute)
	h.step(h.r.cfg.SlowInterval)
	h.converge(10)
	if fw.folderOf("WS-M") != "Hedge" || fw.groupNamed(offsite) != nil {
		t.Fatalf("past its deadline: WS-M in %q, %+v", fw.folderOf("WS-M"), fw.groups)
	}
}

func TestFocusExpiredAcrossRestart(t *testing.T) { // Review Focus 1
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.storeFocus()
	h.sessionIn("WS-M", "Hedge")
	h.sessionIn("WS-S", "Terrace")
	f, ends := focusInStore(t, h, h.themeOf("Hedge"), time.Minute)
	h.converge(10)
	if fw.folderOf("WS-M") != offsite {
		t.Fatalf("not in focus: %+v", fw.groups)
	}
	h.now = h.now.Add(10 * time.Minute) // pigtaild down across the deadline
	h.restart()
	h.converge(10)
	if fw.folderOf("WS-M") != "Hedge" || fw.groupNamed(offsite) != nil {
		t.Fatalf("after the restart: WS-M in %q, %+v", fw.folderOf("WS-M"), fw.groups)
	}
	got, _, err := h.st.GetFocus(f.ID, h.now)
	if err != nil || got.EndedAt == nil || !got.EndedAt.Equal(ends) || got.EndReason != pigtail.FocusEndDeadline {
		t.Fatalf("sealed at the deadline: %+v, %v", got, err)
	}
	lines := endLines(t, h, h.subjectIn("Hedge"))
	if len(lines) != 1 || !lines[0].Timestamp.Equal(ends) || lines[0].Text != `focus "`+offsite+`" ended (deadline)` {
		t.Fatalf("one end line dated the deadline, got %+v", lines)
	}
}

func TestFocusRecreatedSameNameAdoptsNothingStale(t *testing.T) {
	t.Parallel()
	h, fw := focusWorld(t)
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(12)
	gr := fw.groupNamed(offsite)
	if gr == nil {
		t.Fatalf("not in focus: %+v", fw.groups)
	}
	id := gr.ID
	h.focusOff(offsite)
	fw.key = true // the end is read, nothing dissolved yet
	h.converge(4)
	h.focusOn(offsite, fTheme(h.themeOf("Hedge"))) // a new focus, same name
	fw.key = false
	h.converge(12)
	if n := fw.count("group-create"); slices.Contains(fw.calls[slices.Index(fw.calls, "group-create "+offsite)+1:], "group-create "+offsite) {
		t.Fatalf("a second group-create %s (%d creates): %q", offsite, n, fw.folderWrites())
	}
	if gr := fw.groupNamed(offsite); gr == nil || gr.ID != id || fw.folderOf("WS-M") != offsite {
		t.Fatalf("the generated group %s is adopted: %+v", id, fw.groups)
	}
}

func TestToggleIncludesFocusFolders(t *testing.T) { // l. 7878
	t.Parallel()
	h, fw := toggleWorld(t)
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(12)
	gr := fw.groupNamed(offsite)
	if gr == nil || fw.folderOf("WS-C") != offsite {
		t.Fatalf("not in focus: %+v", fw.groups)
	}
	res, err := h.toggle()
	if err != nil || !res.Collapsed {
		t.Fatalf("toggle = %+v, %v", res, err)
	}
	if gr := fw.groupNamed(offsite); !gr.Collapsed {
		t.Fatalf("the focus folder stays expanded: %+v", gr)
	}
	for f, c := range ownFoldersCollapsed(fw) {
		if !c {
			t.Fatalf("%q still expanded", f)
		}
	}
	if res, err := h.toggle(); err != nil || res.Collapsed || fw.groupNamed(offsite).Collapsed {
		t.Fatalf("second toggle = %+v, %v, focus %+v", res, err, fw.groupNamed(offsite))
	}
	h.session("s9", gr.AnchorID, "idle", true) // the anchor now hosts a session
	for _, want := range []bool{true, false} {
		mark := len(fw.calls)
		res, err := h.toggle()
		if err != nil || res.Collapsed != want {
			t.Fatalf("toggle = %+v, %v", res, err)
		}
		if w := writesCiting(fw.calls[mark:], gr.ID); len(w) != 0 {
			t.Fatalf("a focus folder whose anchor hosts a session was toggled: %q", w)
		}
	}
}

func TestToggleFocusReadErrorWritesNothing(t *testing.T) {
	t.Parallel()
	h, fw := toggleWorld(t)
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(12)
	if fw.groupNamed(offsite) == nil {
		t.Fatalf("not in focus: %+v", fw.groups)
	}
	file, err := os.ReadFile(h.statePath)
	if err != nil {
		t.Fatal(err)
	}
	mem := maps.Clone(h.r.collapsedMem)
	h.focusErr = errors.New("database is locked")
	mark := len(fw.calls)
	if _, err := h.toggle(); err == nil || !strings.Contains(err.Error(), "focus: ") {
		t.Fatalf("toggle err = %v, want a « focus: » error", err)
	}
	if n := len(slices.DeleteFunc(slices.Clone(fw.calls[mark:]), func(c string) bool {
		v := strings.Fields(c)[0]
		return v != "group-collapse" && v != "group-expand"
	})); n != 0 {
		t.Fatalf("folds written on a focus read error: %q", folderWritesIn(fw.calls[mark:]))
	}
	after, err := os.ReadFile(h.statePath)
	if err != nil || string(after) != string(file) || !maps.Equal(mem, h.r.collapsedMem) {
		t.Fatalf("folded state changed: %s → %s, %v → %v", file, after, mem, h.r.collapsedMem)
	}
}

// D8 × focus end (review C2 of sub_x000000000000007): a focus present in win
// and winB ends: each window ungroups its own focus folder with its own
// --window, and closes its generated anchor only once re-proved bare in that
// window; an anchor busy at winB's re-proof stays open, win's goes.
func TestFocusEndInEachWindow(t *testing.T) {
	t.Parallel()
	for _, busyB := range []bool{false, true} {
		t.Run(map[bool]string{false: "bare anchors", true: "winB's anchor busy at the re-proof"}[busyB], func(t *testing.T) {
			h, fw := focusWorld(t)
			h.sessionOn(winB, "WS-N", "Hedge")
			h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
			h.converge(14)
			grA, grB := fw.groupNamed(offsite), fw.groupNamedIn(winB, offsite)
			if grA == nil || grB == nil || fw.folderOf("WS-M") != offsite || fw.folderIn(winB, "WS-N") != offsite {
				t.Fatalf("not in focus in both windows: A %+v, B %+v", fw.groups, fw.groupsIn(winB))
			}
			idA, ancA, idB, ancB := grA.ID, grA.AnchorID, grB.ID, grB.AnchorID
			if busyB { // winB's round read sees it bare, the read right before the op busy
				fw.busyAfter["S-"+ancB] = fw.shellCalls["S-"+ancB] + 1
			}
			h.focusOff(offsite)
			h.converge(14)
			if fw.folderOf("WS-M") != "Hedge" || fw.folderIn(winB, "WS-N") != "Hedge" {
				t.Fatalf("the focus ended: WS-M in %q, WS-N in %q", fw.folderOf("WS-M"), fw.folderIn(winB, "WS-N"))
			}
			if fw.groupNamed(offsite) != nil || fw.groupNamedIn(winB, offsite) != nil {
				t.Fatalf("a focus folder stays: A %+v, B %+v", fw.groups, fw.groupsIn(winB))
			}
			if !slices.Contains(fw.calls, "ungroup "+idA+" --remove-generated-anchor") || h.index(ancA) >= 0 {
				t.Fatalf("win: anchor not removed with its group: %q", writesCiting(fw.calls, idA))
			}
			removedB := slices.Contains(fw.calls, "ungroup "+idB+" --remove-generated-anchor @"+winB)
			keptB := slices.Contains(fw.calls, "ungroup "+idB+" @"+winB)
			openB := slices.Contains(fw.windows[winB], ancB)
			if removedB == busyB || keptB != busyB || openB != busyB {
				t.Fatalf("winB busy=%v: removed=%v kept=%v open=%v: %q", busyB, removedB, keptB, openB, writesCiting(fw.calls, idB))
			}
			if fw.shellCalls["S-"+ancB] < 2 {
				t.Fatalf("winB's anchor is proved at the round and again before the op: %d shell reads", fw.shellCalls["S-"+ancB])
			}
		})
	}
}

// sub_x000000000000029: a focus named « Prio » (inserted in the store, past
// the handler's check) is never filed: « Prio » is pigtail's own top folder.
// Logged once.
func TestFocusNamedPrioDropped(t *testing.T) {
	t.Parallel()
	h, fw := focusWorld(t)
	h.storeFocus()
	f := pigtail.Focus{Name: space.Prio, Scope: "perso", Members: []pigtail.FocusMember{fTheme(h.themeOf("Hedge"))}}
	if err := h.st.CreateFocus(&f, h.now); err != nil {
		t.Fatal(err)
	}
	h.converge(10)
	h.step(31 * time.Second) // a slow pass reads it again
	h.converge(10)
	if fw.groupNamed(space.Prio) != nil || fw.folderOf("WS-M") != "Hedge" {
		t.Fatalf("a focus named Prio was filed: WS-M in %q, %+v", fw.folderOf("WS-M"), fw.groups)
	}
	n := 0
	for _, l := range h.logs {
		if strings.Contains(l, `focus "Prio"`) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one log line for the dropped focus, got %d: %q", n, h.logs)
	}
}
