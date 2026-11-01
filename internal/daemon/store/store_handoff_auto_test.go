package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func seedLogSubject(t *testing.T, s *Store, id string) {
	t.Helper()
	themeID := seedTheme(t, s)
	if err := s.CreateSubject(mkSubject(id, themeID, pigtail.StatusInbox, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestAppendLogOnceIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	seedLogSubject(t, s, "a")
	e := &pigtail.SubjectLogEntry{ID: "handoff-auto:s:t", SubjectID: "a", Timestamp: time.UnixMilli(100).UTC(), Kind: pigtail.LogHandoffAuto, Text: "first"}
	created, err := s.AppendLogOnce(e)
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	e2 := *e
	e2.Text = "second"
	created, err = s.AppendLogOnce(&e2)
	if err != nil || created {
		t.Fatalf("second: created=%v err=%v", created, err)
	}
	all, _ := s.ListLog("a", time.Time{})
	if len(all) != 1 || all[0].Text != "first" {
		t.Fatalf("log=%+v", all)
	}
}

func TestLatestLogOfKindIgnoresOtherKinds(t *testing.T) {
	s := newTestStore(t)
	seedLogSubject(t, s, "a")
	none, err := s.LatestLogOfKind("a", pigtail.LogHandoff)
	if err != nil || none != nil {
		t.Fatalf("none=%v err=%v", none, err)
	}
	for _, e := range []*pigtail.SubjectLogEntry{
		{ID: "l1", SubjectID: "a", Timestamp: time.UnixMilli(100).UTC(), Kind: pigtail.LogHandoff, Text: "manual"},
		{ID: "l2", SubjectID: "a", Timestamp: time.UnixMilli(900).UTC(), Kind: pigtail.LogHandoffAuto, Text: "auto"},
	} {
		if err := s.AppendLog(e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LatestLogOfKind("a", pigtail.LogHandoff)
	if err != nil || got == nil || got.ID != "l1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestLogExists(t *testing.T) {
	s := newTestStore(t)
	seedLogSubject(t, s, "a")
	_ = s.AppendLog(&pigtail.SubjectLogEntry{ID: "l1", SubjectID: "a", Timestamp: time.UnixMilli(1).UTC(), Kind: pigtail.LogNote, Text: "x"})
	ok, err := s.LogExists("l1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ok, err = s.LogExists("nope")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestLatestTurnEnd(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if _, _, ok, err := s.LatestTurnEnd("s1"); err != nil || ok {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.RecordTurnEnd("s1", "perso", "p1", t0, t0.Add(time.Minute), "stop"))
	must(s.RecordTurnEnd("s1", "perso", "p2", t0, t0.Add(2*time.Minute), "stop"))
	must(s.RecordTurnEnd("s1", "perso", "p3", t0, t0.Add(3*time.Minute), "failure"))
	id, at, ok, err := s.LatestTurnEnd("s1")
	if err != nil || !ok || id != "p2" || !at.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("id=%q at=%v ok=%v err=%v", id, at, ok, err)
	}
}

func TestTurnEndStart(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if _, ok, err := s.TurnEndStart("s1", "p1"); err != nil || ok {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	if err := s.RecordTurnEnd("s1", "perso", "p1", t0, t0.Add(time.Minute), "stop"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.TurnEndStart("s1", "p1")
	if err != nil || !ok || !got.Equal(t0) {
		t.Fatalf("got=%v ok=%v err=%v", got, ok, err)
	}
}
