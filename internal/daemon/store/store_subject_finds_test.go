package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const findWS = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"

func newFind(id string, ts int64, ws string, hits ...string) *pigtail.SubjectFind {
	f := &pigtail.SubjectFind{ID: id, Scope: "perso", Timestamp: time.UnixMilli(ts).UTC(), Query: "q " + id, CmuxWorkspaceID: ws, Candidates: []pigtail.SubjectFindHit{}}
	for _, h := range hits {
		f.Candidates = append(f.Candidates, pigtail.SubjectFindHit{SubjectID: h, Score: 1})
	}
	return f
}

func findLine(f *pigtail.SubjectFind) *pigtail.JournalEntry {
	return &pigtail.JournalEntry{ID: pigtail.JournalKindSubjectFind + ":" + f.ID, Timestamp: f.Timestamp,
		Kind: pigtail.JournalKindSubjectFind, Scope: f.Scope, Summary: "recherche « " + f.Query + " »", Source: pigtail.SubjectFindSource(f.ID)}
}

func pickLine(p pigtail.SubjectFindPick, f *pigtail.SubjectFind) *pigtail.JournalEntry {
	return &pigtail.JournalEntry{ID: pigtail.JournalKindSubjectFindPick + ":" + f.ID, Timestamp: p.PickedAt,
		Kind: pigtail.JournalKindSubjectFindPick, Scope: f.Scope, Summary: "pick " + p.SubjectID, Source: pigtail.SubjectFindSource(f.ID)}
}

func TestCreateSubjectFindAtomic(t *testing.T) {
	s := openMem(t)
	f := newFind("sf_a", 10, findWS, "sub_1", "sub_2")
	if err := s.CreateSubjectFind(f, findLine(f)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSubjectFind("sf_a")
	if err != nil || got.Query != "q sf_a" || got.CmuxWorkspaceID != findWS || len(got.Candidates) != 2 || got.Candidates[1].SubjectID != "sub_2" || !got.PickedAt.IsZero() {
		t.Fatalf("got %+v, %v", got, err)
	}
	// A journal line the CHECK refuses (newline) rolls the row back too.
	bad := newFind("sf_b", 11, "")
	j := findLine(bad)
	j.Summary = "two\nlines"
	if err := s.CreateSubjectFind(bad, j); err == nil {
		t.Fatal("bad journal line accepted")
	}
	if _, err := s.GetSubjectFind("sf_b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("row written without its journal line: %v", err)
	}
	if _, err := s.GetSubjectFind("sf_none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestPickSubjectFindSearches(t *testing.T) {
	s := openMem(t)
	for _, f := range []*pigtail.SubjectFind{
		newFind("sf_1", 10, findWS, "sub_x"),
		newFind("sf_2", 20, findWS, "sub_y", "sub_z"),
		newFind("sf_other", 15, "", "sub_z"),
	} {
		if err := s.CreateSubjectFind(f, findLine(f)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.PickSubjectFind("sf_2", "sub_z", time.UnixMilli(30).UTC(), pickLine)
	if err != nil || !p.Created || p.Rank != 2 || p.Candidates != 2 || p.Searches != 2 {
		t.Fatalf("first pick = %+v, %v; want created, rank 2/2, 2 searches", p, err)
	}
	again, err := s.PickSubjectFind("sf_2", "sub_z", time.UnixMilli(99).UTC(), pickLine)
	if err != nil || again.Created || again.Searches != 2 || !again.PickedAt.Equal(time.UnixMilli(30).UTC()) {
		t.Fatalf("same pick = %+v, %v; want unchanged, not created", again, err)
	}
	if _, err := s.PickSubjectFind("sf_2", "sub_y", time.UnixMilli(40).UTC(), pickLine); !errors.Is(err, ErrFindPicked) {
		t.Fatalf("other subject: %v, want ErrFindPicked", err)
	}
	// A find after the pick counts from it; no workspace counts 1; not among the candidates is rank 0.
	f3 := newFind("sf_3", 50, findWS, "sub_x")
	if err := s.CreateSubjectFind(f3, findLine(f3)); err != nil {
		t.Fatal(err)
	}
	if p, err = s.PickSubjectFind("sf_3", "sub_x", time.UnixMilli(60).UTC(), pickLine); err != nil || p.Searches != 1 || p.Rank != 1 {
		t.Fatalf("after a pick = %+v, %v; want 1 search, rank 1", p, err)
	}
	if p, err = s.PickSubjectFind("sf_other", "sub_q", time.UnixMilli(70).UTC(), pickLine); err != nil || p.Searches != 1 || p.Rank != 0 {
		t.Fatalf("no workspace = %+v, %v; want 1 search, rank 0", p, err)
	}
	if _, err := s.PickSubjectFind("sf_none", "sub_x", time.UnixMilli(80).UTC(), pickLine); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown find: %v", err)
	}
	// sf_1 (ts 10) is picked after the workspace's pick of sf_3 (at 60): the count is 0 by the query, the floor is 1.
	if p, err = s.PickSubjectFind("sf_1", "sub_x", time.UnixMilli(90).UTC(), pickLine); err != nil || p.Searches != 1 || p.Rank != 1 {
		t.Fatalf("older search = %+v, %v; want 1 search (floor), rank 1", p, err)
	}
	rows, err := s.ListJournal(JournalFilter{Kind: pigtail.JournalKindSubjectFindPick})
	if err != nil || len(rows) != 4 {
		t.Fatalf("pick lines = %d, %v; want 4 (the repeat writes none)", len(rows), err)
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Source, "pigtail:subject-find/sf_") {
			t.Errorf("source %q", r.Source)
		}
	}
}

func TestSubjectLastActivity(t *testing.T) {
	s := openMem(t)
	seedSubjectForLog(t, s, "sub_a")
	seedSubjectForLog(t, s, "sub_b")
	for i, l := range []struct {
		sub, kind string
		ts        int64
	}{{"sub_a", "phase", 100}, {"sub_a", "message", 900}, {"sub_a", "delivery", 300}, {"sub_b", "note", 500}} {
		if err := s.AppendLog(&pigtail.SubjectLogEntry{ID: "log_" + string(rune('a'+i)), SubjectID: l.sub, Timestamp: time.UnixMilli(l.ts).UTC(), Kind: l.kind, Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SubjectLastActivity([]string{"phase", "progress", "handoff", "delivery", "decision"})
	if err != nil || len(got) != 1 || got["sub_a"] != 300 {
		t.Fatalf("got %v, %v; want only sub_a at 300 (message and note ignored)", got, err)
	}
	if got, err := s.SubjectLastActivity(nil); err != nil || len(got) != 0 {
		t.Fatalf("no kinds: %v, %v", got, err)
	}
}

func seedSubjectForLog(t *testing.T, s *Store, id string) {
	t.Helper()
	now := time.UnixMilli(1).UTC()
	if _, err := s.GetTheme("thm_seed"); err != nil {
		if err := s.CreateTheme(&pigtail.Theme{ID: "thm_seed", Name: "Seed", Kind: "project", VaultLink: "Perso/Projects/seed", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSubject(&pigtail.Subject{ID: id, ThemeID: "thm_seed", Title: id, Slug: id, Status: pigtail.StatusBuilding, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}
