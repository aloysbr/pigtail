package store

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestCapturedDepSatisfies: a subject that depends on a captured subject is
// actionable (captured satisfies dependencies like done).
func TestCapturedDepSatisfies(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	a := mkSubject("a", themeID, pigtail.StatusCaptured, 5)
	b := mkSubject("b", themeID, pigtail.StatusInbox, 1)
	b.Deps = []string{"a"}
	for _, sub := range []*pigtail.Subject{a, b} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.NextSubject(scope.Scope("globex"))
	if err != nil || got.ID != "b" {
		t.Fatalf("next = %+v, %v; want b (its dep is captured)", got, err)
	}
}

// TestParkedDepBlocks: parked closes a subject but satisfies nothing; done
// still does.
func TestParkedDepBlocks(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	a := mkSubject("a", themeID, pigtail.StatusParked, 5)
	b := mkSubject("b", themeID, pigtail.StatusInbox, 1)
	b.Deps = []string{"a"}
	for _, sub := range []*pigtail.Subject{a, b} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.NextSubject(scope.Scope("globex")); err == nil {
		t.Fatalf("next = %s, want none: b stays blocked by a parked dep", got.ID)
	}
	a.Status = pigtail.StatusDone
	if err := s.UpdateSubject(a); err != nil {
		t.Fatal(err)
	}
	if got, err := s.NextSubject(scope.Scope("globex")); err != nil || got.ID != "b" {
		t.Fatalf("next = %+v, %v; want b once a is done", got, err)
	}
}

// TestNextSkipsCaptured: subject next never returns a captured subject.
func TestNextSkipsCaptured(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	for _, sub := range []*pigtail.Subject{
		mkSubject("cap", themeID, pigtail.StatusCaptured, 1),
		mkSubject("live", themeID, pigtail.StatusInbox, 2),
	} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.NextSubject(scope.Scope("globex"))
	if err != nil || got.ID != "live" {
		t.Fatalf("next = %+v, %v; want live", got, err)
	}
}
