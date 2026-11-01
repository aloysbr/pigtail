package cmuxsync

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/space"
)

// A subject's own space (D3, D5 of sub_x000000000000033) in the arrangement:
// focus > own space > theme's space; a name no longer in the table follows the
// theme. Against the simulated cmux, in both sort modes.

// filedWorld and bothModes came from runner_folders_prio_test.go, gone with
// the Prio folder (#244); the subject-space and D4 tests still need them.

// filedWorld: Terrace {A, B}, Hedge {C}, « Inbox » {Shed}, sorted (cmux
// creating groups on top, P11) or not.
func filedWorld(t *testing.T, ns bool, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	if ns {
		return noSortWorld(t, opts...)
	}
	h, fw := newFolderHarness(t, opts...)
	fw.createAtTop = true
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Terrace")
	h.sessionIn("WS-C", "Hedge")
	fw.add(win, "Shed", "Shed")
	h.offTopic("Shed")
	h.converge(10)
	h.assertQuiet("sorted world")
	for ws, f := range map[string]string{"WS-A": "Terrace", "WS-B": "Terrace", "WS-C": "Hedge", "Shed": space.NoSubject} {
		if got := fw.folderOf(ws); got != f {
			t.Fatalf("world: %s in %q, want %q (%q)", ws, got, f, fw.windows[win])
		}
	}
	return h, fw
}

// bothModes runs fn sorted, then with the sort off (the daemon's mode).
func bothModes(t *testing.T, fn func(t *testing.T, ns bool)) {
	for _, ns := range []bool{false, true} {
		t.Run(map[bool]string{false: "sorted", true: "no sort"}[ns], func(t *testing.T) { fn(t, ns) })
	}
}

func TestSubjectSpaceFiledOverTheme(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw := filedWorld(t, ns)
		h.setSubjectSpace(h.subjectIn("Terrace"), "Hedge")
		h.converge(10)
		for _, ws := range []string{"WS-A", "WS-B"} {
			if got := fw.folderOf(ws); got != "Hedge" {
				t.Fatalf("%s in %q, want its own space Hedge", ws, got)
			}
		}
		h.assertQuiet("own space")
	})
}

func TestFocusBeatsSubjectSpace(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw := filedWorld(t, ns)
		h.setSubjectSpace(h.subjectIn("Terrace"), "Hedge")
		h.focusOn(offsite, fSubject(h.subjectIn("Terrace")))
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != offsite {
			t.Fatalf("WS-A in %q, want the focus %q", got, offsite)
		}
	})
}

func TestUnknownSubjectSpaceFollowsTheme(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw := filedWorld(t, ns)
		h.setSubjectSpace(h.subjectIn("Terrace"), "Gone")
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != "Terrace" {
			t.Fatalf("WS-A in %q, want its theme's Terrace", got)
		}
		h.assertQuiet("unknown own space")
	})
}

// D8: a subject moved to a theme in another space follows it (its dragged space
// is cleared by the store).
func TestRethemedSubjectFollowsNewThemeSpace(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw := filedWorld(t, ns)
		sub := h.subjectIn("Terrace")
		h.setSubjectSpace(sub, "Hedge")
		h.converge(10)
		if got := fw.folderOf("WS-A"); got != "Hedge" {
			t.Fatalf("WS-A in %q, want Hedge before the retheme", got)
		}
		_ = h.subjectIn("Trellis")
		cur, err := h.st.GetSubject(sub)
		if err != nil {
			t.Fatal(err)
		}
		cur.ThemeID = h.themeOf("Trellis")
		cur.UpdatedAt = h.now
		if err := h.st.UpdateSubject(cur); err != nil {
			t.Fatal(err)
		}
		h.converge(10)
		for _, ws := range []string{"WS-A", "WS-B"} {
			if got := fw.folderOf(ws); got != "Trellis" {
				t.Fatalf("%s in %q, want Trellis", ws, got)
			}
		}
		h.assertQuiet("retheme")
	})
}

// The sidebar reads the same rule as the API: an off-topic tab citing a
// subject whose own space left the table is filed in its theme's folder.
func TestOffTopicStaleSubjectSpaceFiledInThemeFolder(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw := filedWorld(t, ns)
		sub := h.subjectIn("Terrace")
		h.setSubjectSpace(sub, "Gone")
		fw.add(win, "OT", "OT")
		h.offTopicAs("OT", "perso", "", "see "+sub)
		h.converge(10)
		if got := fw.folderOf("OT"); got != "Terrace" {
			t.Fatalf("OT in %q, want its theme's Terrace (never Other)", got)
		}
	})
}

func TestOffTopicValidSubjectSpaceFiledInSubjectFolder(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw := filedWorld(t, ns)
		sub := h.subjectIn("Terrace")
		h.setSubjectSpace(sub, "Hedge")
		fw.add(win, "OT", "OT")
		h.offTopicAs("OT", "perso", "", "see "+sub)
		h.converge(10)
		if got := fw.folderOf("OT"); got != "Hedge" {
			t.Fatalf("OT in %q, want Hedge", got)
		}
	})
}
