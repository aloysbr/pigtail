package store

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seedRanked builds, in the perso theme: A (rank 1, dep on B building), B
// (rank 2), C (rank 0), D (done); and one work subject.
func seedRanked(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	seedScoped(t, s)
	a := mkSubject("A", "thm_p", pigtail.StatusBuilding, 1)
	a.Deps = []string{"B"}
	for _, sub := range []*pigtail.Subject{
		a,
		mkSubject("B", "thm_p", pigtail.StatusBuilding, 2),
		mkSubject("C", "thm_p", pigtail.StatusBuilding, 0),
		mkSubject("D", "thm_p", pigtail.StatusDone, 3),
		mkSubject("T", "thm_t", pigtail.StatusBuilding, 1),
	} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatalf("create %s: %v", sub.ID, err)
		}
	}
	return s
}

func TestRankedSubjectsFlagsBlockedDeps(t *testing.T) {
	s := seedRanked(t)
	got, err := s.RankedSubjects(scope.Scope("perso"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if len(ids) != 3 || ids[0] != "A" || ids[1] != "B" || ids[2] != "C" {
		t.Fatalf("RankedSubjects(perso) = %v, want [A B C]", ids)
	}
	if len(got[0].BlockedBy) != 1 || got[0].BlockedBy[0] != "B" {
		t.Fatalf("A.BlockedBy = %v, want [B]", got[0].BlockedBy)
	}
	if len(got[1].BlockedBy) != 0 {
		t.Fatalf("B.BlockedBy = %v, want empty", got[1].BlockedBy)
	}
}

func TestNextSubjectUnchangedOverRanked(t *testing.T) {
	s := seedRanked(t)
	got, err := s.NextSubject(scope.Scope("perso"))
	if err != nil || got.ID != "B" {
		t.Fatalf("NextSubject(perso) = %+v, %v; want B", got, err)
	}
}
