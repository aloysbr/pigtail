package cmuxsync

import (
	"errors"
	"slices"
	"testing"

	"github.com/aloysbr/pigtail/internal/space"
)

// D1 (sub_x000000000000019, amends D36): an off-topic session goes to the
// folder of its space — explicit, or inferred from the subject its reason
// cites — after the bound sessions there. Against the simulated cmux
// (runner_folders_harness_test.go), never the real sidebar. The rows are
// written with the scope of fh.subjectIn's themes (perso): the declaration's
// scope must read the cited subject.

// offTopicWS opens WS-D, unbound, with a live session.
func offTopicWS(h *folderHarness, fw *folderWorld) {
	fw.add(win, "WS-D", "WS-D")
	h.session("s-WS-D", "WS-D", "idle", true)
}

// A row made before 0024 (no space) citing a subject of « Terrace » is caught
// up: filed in « Terrace », after the bound session, even with a graver wait.
func TestOffTopicCitedSubjectFiledInSpace(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	offTopicWS(h, fw)
	h.wait("s-WS-D", "permission") // sorted by gravity alone, WS-D would come first
	h.offTopicAs("WS-D", "perso", "", "revue de "+h.subjectIn("Terrace"))
	h.converge(10)
	h.assertQuiet("cited subject")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-D"}) {
		t.Fatalf("Terrace = %v, want [WS-A WS-D] (off-topic last)", got)
	}
}

func TestOffTopicExplicitSpace(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	offTopicWS(h, fw)
	h.offTopicAs("WS-D", "perso", "Hedge", "")
	h.converge(10)
	h.assertQuiet("explicit space")
	if got := fw.folderOf("WS-D"); got != "Hedge" {
		t.Fatalf("WS-D in %q, want Hedge", got)
	}
}

// D6: the stored space changes (redeclare --space) → the workspace follows at
// the next round.
func TestTargetsOffTopicSpaceFollowsUpdate(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	offTopicWS(h, fw)
	h.offTopicAs("WS-D", "perso", "Terrace", "")
	h.converge(10)
	if got := fw.folderOf("WS-D"); got != "Terrace" {
		t.Fatalf("WS-D in %q, want Terrace", got)
	}
	if err := h.st.SetOffTopicSpace("WS-D", "Garden"); err != nil {
		t.Fatal(err)
	}
	h.converge(10)
	h.assertQuiet("space updated")
	if got := fw.folderOf("WS-D"); got != "Garden" {
		t.Fatalf("WS-D in %q, want Garden", got)
	}
}

// The cited subject's theme changes space: the signature changes, the
// workspace follows.
func TestOffTopicThemeSpaceChangeFollows(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	offTopicWS(h, fw)
	sub := h.subjectIn("Terrace")
	h.offTopicAs("WS-D", "perso", "", "suite de "+sub)
	h.converge(10)
	if got := fw.folderOf("WS-D"); got != "Terrace" {
		t.Fatalf("WS-D in %q, want Terrace", got)
	}
	s, err := h.st.GetSubject(sub)
	if err != nil {
		t.Fatal(err)
	}
	h.setThemeSpace(s.ThemeID, "Garden")
	h.converge(10)
	h.assertQuiet("theme moved")
	if got := fw.folderOf("WS-D"); got != "Garden" {
		t.Fatalf("WS-D in %q, want Garden", got)
	}
}

// A store error while inferring skips the round (D7·t3 (d)): no folder write,
// one log line over several passes, never filed in « Inbox » because of it.
// No link in the window: boundSpaces reads no subject, the inference does.
func TestOffTopicLookupErrorSkipsRound(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	offTopicWS(h, fw)
	h.offTopicAs("WS-D", "perso", "", "revue de "+h.subjectIn("Terrace"))
	h.reads.subject = errors.New("disk on fire")
	h.converge(10)
	if w := fw.folderWrites(); len(w) != 0 {
		t.Fatalf("folder writes despite the read error: %q", w)
	}
	want := "cmuxsync: folders: round skipped, store read: off-topic space WS-D: disk on fire"
	n := 0
	for _, l := range h.logs {
		if l == want {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("log lines %q: want exactly one %q", h.logs, want)
	}
	if got := fw.folderOf("WS-D"); got == space.NoSubject {
		t.Fatalf("WS-D filed in « Inbox » on a read error")
	}
}

// D9: with the sort off, « last » is #218's bottom-of-folder placement: the
// add lands under the anchor (P19), one move (D39) brings it to the bottom.
func TestArrangeLastNoSort(t *testing.T) {
	t.Parallel()
	h, fw := noSortWorld(t)
	fw.addKeepsSide = true
	fw.add(win, "WS-D", "WS-D")
	fw.windows[win] = slices.Insert(fw.windows[win][:len(fw.windows[win])-1], 0, "WS-D") // free, on top
	h.session("s-WS-D", "WS-D", "idle", true)
	if h.index("WS-D") > h.index(fw.groupNamed("Terrace").AnchorID) {
		t.Fatalf("world: WS-D not above « Terrace »: %q", fw.windows[win])
	}
	places, reorders := fw.count("place"), fw.count("reorder")
	h.offTopicAs("WS-D", "perso", "Terrace", "")
	h.converge(10)
	h.assertQuiet("off-topic in Terrace, no sort")
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"WS-A", "WS-B", "WS-D"}) {
		t.Fatalf("Terrace = %v, want WS-D at the bottom", got)
	}
	if n := fw.adds("WS-D"); n != 1 {
		t.Fatalf("want one group-add of WS-D, got %d", n)
	}
	if n, r := fw.count("place")-places, fw.count("reorder")-reorders; n != 1 || r != 0 {
		t.Fatalf("places = %d, reorders = %d, want one place (noSortPlaces, D39)", n, r)
	}
	noReorder(t, fw)
}

// D32 merge (#220 × sub_x000000000000007): a focus files bindings only. An
// off-topic session whose reason cites a subject of a theme in focus, and the
// subject itself in another focus, stays in the theme's space (D1 of
// sub_x000000000000019), while the theme's bound session goes to its focus.
func TestOffTopicCitingFocusedSubjectStaysInSpace(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Hedge")
	sub := h.subjectIn("Hedge")
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.focusOn("B", fSubject(sub))
	offTopicWS(h, fw)
	h.offTopicAs("WS-D", "perso", "", "revue de "+sub)
	h.converge(12)
	h.assertQuiet("off-topic citing a focused subject")
	if got := fw.folderOf("WS-A"); got != "B" { // positive control: the focus are folders
		t.Fatalf("bound WS-A in %q, want its subject's focus B", got)
	}
	if got := fw.folderOf("WS-D"); got != "Hedge" {
		t.Fatalf("off-topic WS-D in %q, want its space Hedge", got)
	}
}

// A focus name among the pass's folders never takes an off-topic session:
// a declaration whose stored space is named like an open focus (a space
// removed from the table since) files in « Inbox », not in the focus.
func TestOffTopicSpaceNamedLikeFocusNotFiledInFocus(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Hedge")
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	offTopicWS(h, fw)
	h.offTopicAs("WS-D", "perso", offsite, "")
	h.converge(12)
	if got := fw.folderOf("WS-A"); got != offsite { // positive control: the focus is a folder
		t.Fatalf("bound WS-A in %q, want the focus", got)
	}
	if got := fw.folderOf("WS-D"); got != space.NoSubject {
		t.Fatalf("off-topic WS-D in %q, want %q", got, space.NoSubject)
	}
}

// D4 (sub_x000000000000033): the sentinel space « Inbox » (space.NoSubject)
// files the session in « Inbox » (it is not a pigtail space), with no tail
// sort, like a row without a space.
func TestOffTopicInboxSentinelFiledInInbox(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw := newFolderHarness(t)
		if ns {
			h, fw = noSortWorld(t)
		}
		offTopicWS(h, fw)
		h.offTopicAs("WS-D", "perso", space.NoSubject, "")
		h.converge(10)
		h.assertQuiet("off-topic sentinel Inbox")
		if got := fw.folderOf("WS-D"); got != space.NoSubject {
			t.Fatalf("WS-D in %q, want %q", got, space.NoSubject)
		}
		f := Filing{Bound: map[string]string{}, OffTopic: map[string]string{"WS-D": space.NoSubject}}
		if last := offTopicLast(map[string]string{"WS-D": space.NoSubject}, f); last["WS-D"] {
			t.Fatalf("offTopicLast holds the sentinel row: %v", last)
		}
	})
}
