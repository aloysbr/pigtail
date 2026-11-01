package store

import (
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ranksFixture seeds one theme and the subjects ids with the given ranks
// (0 = unranked).
func ranksFixture(t *testing.T, ranks map[string]int) *Store {
	t.Helper()
	s := newTestStore(t)
	now := time.UnixMilli(1000).UTC()
	if err := s.CreateTheme(&pigtail.Theme{ID: "thm_r", Name: "R", Kind: "area", VaultLink: "Perso/Areas/r", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for id, rk := range ranks {
		if err := s.CreateSubject(&pigtail.Subject{ID: id, ThemeID: "thm_r", Title: id, Slug: id, Status: pigtail.StatusInbox,
			Rank: rk, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func rankOf(t *testing.T, s *Store, id string) int {
	t.Helper()
	sub, err := s.GetSubject(id)
	if err != nil {
		t.Fatal(err)
	}
	return sub.Rank
}

func TestSetRanksAtomic(t *testing.T) {
	s := ranksFixture(t, map[string]int{"sub_a": 1, "sub_b": 2, "sub_c": 3})
	err := s.SetRanks([]RankChange{{ID: "sub_a", From: 1, To: 9}, {ID: "sub_b", From: 7, To: 8}, {ID: "sub_c", From: 3, To: 7}})
	if !errors.Is(err, ErrRankChanged) {
		t.Fatalf("a stale From in the middle = %v, want ErrRankChanged", err)
	}
	for id, want := range map[string]int{"sub_a": 1, "sub_b": 2, "sub_c": 3} {
		if got := rankOf(t, s, id); got != want {
			t.Errorf("%s rank = %d after a refused change, want %d (nothing written)", id, got, want)
		}
	}
	if err := s.SetRanks([]RankChange{{ID: "sub_a", From: 1, To: 2}, {ID: "sub_nope", From: 0, To: 1}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id = %v, want ErrNotFound", err)
	}
	if got := rankOf(t, s, "sub_a"); got != 1 {
		t.Fatalf("sub_a rank = %d after an unknown id, want 1", got)
	}
	before, _ := s.GetSubject("sub_b")
	if err := s.SetRanks([]RankChange{{ID: "sub_a", From: 1, To: 2}, {ID: "sub_b", From: 2, To: 1}}); err != nil {
		t.Fatal(err)
	}
	if rankOf(t, s, "sub_a") != 2 || rankOf(t, s, "sub_b") != 1 {
		t.Fatal("swap not applied")
	}
	if after, _ := s.GetSubject("sub_b"); !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("updated_at moved: %v → %v", before.UpdatedAt, after.UpdatedAt)
	}
}

// Two neighbours of the same rank (perso: 23 ranks, 16 values) are told
// apart by the move.
func TestSetRanksSwapDuplicates(t *testing.T) {
	s := ranksFixture(t, map[string]int{"sub_a": 5, "sub_b": 5})
	if err := s.SetRanks([]RankChange{{ID: "sub_a", From: 5, To: 5}, {ID: "sub_b", From: 5, To: 6}}); err != nil {
		t.Fatal(err)
	}
	if a, b := rankOf(t, s, "sub_a"), rankOf(t, s, "sub_b"); a != 5 || b != 6 {
		t.Fatalf("ranks = %d, %d, want 5, 6", a, b)
	}
}

func TestSetRanksUnrank(t *testing.T) {
	s := ranksFixture(t, map[string]int{"sub_a": 4, "sub_b": 0})
	if err := s.SetRanks([]RankChange{{ID: "sub_a", From: 4, To: 0}, {ID: "sub_b", From: 0, To: 3}}); err != nil {
		t.Fatal(err)
	}
	var null bool
	if err := s.db.QueryRow(`SELECT rank IS NULL FROM subjects WHERE id = 'sub_a'`).Scan(&null); err != nil || !null {
		t.Fatalf("To 0 must store NULL: null=%v err=%v", null, err)
	}
	if got := rankOf(t, s, "sub_b"); got != 3 {
		t.Fatalf("sub_b rank = %d, want 3", got)
	}
}
