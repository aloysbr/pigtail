package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var spT0 = time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

// spaceFixture: theme thm_a (perso, space Terrace) with subject sub_x, theme
// thm_b (perso, space Design).
func spaceFixture(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	for _, th := range []pigtail.Theme{
		{ID: "thm_a", Name: "A", Kind: "area", VaultLink: "Perso/a", Scope: "perso", Space: "Terrace", CreatedAt: spT0},
		{ID: "thm_b", Name: "B", Kind: "area", VaultLink: "Perso/b", Scope: "perso", Space: "Design", CreatedAt: spT0},
	} {
		th := th
		if err := s.CreateTheme(&th); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSubject(&pigtail.Subject{ID: "sub_x", ThemeID: "thm_a", Title: "Long title", ShortTitle: "Short",
		Slug: "x", Status: pigtail.StatusBuilding, CreatedAt: spT0, UpdatedAt: spT0}); err != nil {
		t.Fatal(err)
	}
	return s
}

func noteTexts(t *testing.T, s *Store, id string) []string {
	t.Helper()
	l, err := s.ListLog(id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range l {
		if e.Kind == pigtail.LogNote {
			out = append(out, e.Text)
		}
	}
	return out
}

func TestSetSubjectSpaceByDrag(t *testing.T) {
	s := spaceFixture(t)
	changed, err := s.SetSubjectSpaceByDrag("sub_x", "Hedge", "Terrace", spT0)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want true", changed, err)
	}
	sub, _ := s.GetSubject("sub_x")
	if sub.Space != "Hedge" {
		t.Fatalf("Space = %q", sub.Space)
	}
	want := "space dragged: Terrace → Hedge"
	if got := noteTexts(t, s, "sub_x"); len(got) != 1 || got[0] != want {
		t.Fatalf("log = %v, want [%q]", got, want)
	}
	j, err := s.ListJournal(JournalFilter{Kind: pigtail.JournalKindSubjectSpaceDrag, Scope: "perso"})
	if err != nil || len(j) != 1 {
		t.Fatalf("journal = %+v, %v", j, err)
	}
	if j[0].SubjectID != "sub_x" || j[0].Scope != "perso" || !strings.Contains(j[0].Summary, "Short") || !strings.Contains(j[0].Summary, want) {
		t.Fatalf("journal line = %+v", j[0])
	}
	// Same again: nothing changes, nothing is written.
	changed, err = s.SetSubjectSpaceByDrag("sub_x", "Hedge", "Terrace", spT0.Add(time.Second))
	if err != nil || changed {
		t.Fatalf("second call changed=%v err=%v, want false", changed, err)
	}
	if got := noteTexts(t, s, "sub_x"); len(got) != 1 {
		t.Fatalf("log after no-op = %v", got)
	}
	// Back to the theme's space.
	if changed, err = s.SetSubjectSpaceByDrag("sub_x", "", "Hedge", spT0.Add(2*time.Second)); err != nil || !changed {
		t.Fatalf("clear: changed=%v err=%v", changed, err)
	}
	got := noteTexts(t, s, "sub_x")
	if len(got) != 2 || !contains(got, "space dragged: Hedge → (theme: Terrace)") {
		t.Fatalf("log = %v", got)
	}
	// Two successive drags leave two journal lines.
	if j, _ = s.ListJournal(JournalFilter{Kind: pigtail.JournalKindSubjectSpaceDrag}); len(j) != 2 {
		t.Fatalf("journal lines = %d, want 2", len(j))
	}
	// Unknown subject.
	before, _ := s.ListJournal(JournalFilter{})
	if _, err = s.SetSubjectSpaceByDrag("sub_nope", "Hedge", "Terrace", spT0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown subject err = %v", err)
	}
	if after, _ := s.ListJournal(JournalFilter{}); len(after) != len(before) {
		t.Fatal("unknown subject wrote a journal line")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestUpdateSubjectKeepsSpace(t *testing.T) {
	s := spaceFixture(t)
	if _, err := s.SetSubjectSpaceByDrag("sub_x", "Hedge", "Terrace", spT0); err != nil {
		t.Fatal(err)
	}
	sub, _ := s.GetSubject("sub_x")
	sub.Title = "Renamed"
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSubject("sub_x")
	if got.Title != "Renamed" || got.Space != "Hedge" {
		t.Fatalf("after title update: %+v", got)
	}
}

// D8: a theme change clears the dragged space in the same transaction, traced.
func TestRethemeClearsSubjectSpace(t *testing.T) {
	s := spaceFixture(t)
	if _, err := s.SetSubjectSpaceByDrag("sub_x", "Hedge", "Terrace", spT0); err != nil {
		t.Fatal(err)
	}
	sub, _ := s.GetSubject("sub_x")
	sub.ThemeID = "thm_b"
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSubject("sub_x")
	if got.Space != "" || got.ThemeID != "thm_b" {
		t.Fatalf("after retheme: %+v", got)
	}
	if !contains(noteTexts(t, s, "sub_x"), "space cleared (theme changed): Hedge → (theme)") {
		t.Fatalf("log = %v", noteTexts(t, s, "sub_x"))
	}
	j, _ := s.ListJournal(JournalFilter{Kind: pigtail.JournalKindSubjectSpaceCleared})
	if len(j) != 1 || j[0].SubjectID != "sub_x" || j[0].Scope != "perso" {
		t.Fatalf("journal = %+v", j)
	}
	// A subject with no own space writes nothing more on a retheme.
	sub.ThemeID = "thm_a"
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	if j, _ = s.ListJournal(JournalFilter{Kind: pigtail.JournalKindSubjectSpaceCleared}); len(j) != 1 {
		t.Fatalf("a subject without own space wrote a journal line: %+v", j)
	}
	if n := len(noteTexts(t, s, "sub_x")); n != 2 {
		t.Fatalf("log notes = %d, want 2", n)
	}
}

func TestSetOffTopicSpaceByDrag(t *testing.T) {
	s := newTestStore(t)
	o := &pigtail.WorkspaceOffTopic{CmuxWorkspaceID: "11111111-2222-3333-4444-555555555555", Scope: "perso",
		SessionID: "ses_1", Reason: "x", DeclaredAt: spT0, Space: "Terrace"}
	if err := s.CreateOffTopic(o); err != nil {
		t.Fatal(err)
	}
	changed, err := s.SetOffTopicSpaceByDrag(o.CmuxWorkspaceID, "Hedge", "Terrace", spT0)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := s.GetOffTopic(o.CmuxWorkspaceID)
	if got.Space != "Hedge" {
		t.Fatalf("Space = %q", got.Space)
	}
	j, _ := s.ListJournal(JournalFilter{Kind: pigtail.JournalKindOffTopicSpaceDrag})
	if len(j) != 1 || j[0].Scope != "perso" || !strings.Contains(j[0].Summary, "space dragged: Terrace → Hedge") {
		t.Fatalf("journal = %+v", j)
	}
	if changed, err = s.SetOffTopicSpaceByDrag(o.CmuxWorkspaceID, "Hedge", "Terrace", spT0); err != nil || changed {
		t.Fatalf("identical: changed=%v err=%v", changed, err)
	}
	if j, _ = s.ListJournal(JournalFilter{Kind: pigtail.JournalKindOffTopicSpaceDrag}); len(j) != 1 {
		t.Fatalf("journal lines = %d after no-op", len(j))
	}
	if _, err = s.SetOffTopicSpaceByDrag("absent", "Hedge", "Terrace", spT0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent row err = %v", err)
	}
}

// An empty from (a theme with no space) reads « (aucun) », never a bare
// arrow (Tâche 6 note).
func TestSpaceDragTextEmptyFrom(t *testing.T) {
	if got, want := spaceDragText("", "Hedge", "T"), "space dragged: (aucun) → Hedge"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}
