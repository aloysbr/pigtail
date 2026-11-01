package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestTurnEnds(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetSessionSkill("s1", "my-plan", t0))
	must(s.RecordTurnEnd("s1", "perso", "p1", t0, t0.Add(time.Minute), "stop"))
	must(s.RecordTurnEnd("s1", "perso", "p1", t0, t0.Add(2*time.Minute), "stop")) // a second Stop of p1: once
	must(s.RecordTurnEnd("s1", "perso", "p2", t0, t0.Add(3*time.Minute), "failure"))
	must(s.RecordTurnEnd("s1", "perso", "p2", t0, t0.Add(3*time.Minute), "stop")) // Stop after StopFailure: still failed
	must(s.RecordTurnEnd("s1", "perso", "p3", t0, t0.Add(4*time.Minute), "stop"))
	must(s.RecordTurnEnd("s1", "perso", "p3", t0, t0.Add(4*time.Minute), "failure")) // StopFailure after Stop: flips to failed
	must(s.RecordTurnEnd("s2", "gx", "q1", t0, t0.Add(5*time.Minute), "stop"))       // no skill: ""
	got, err := s.HandbacksBySkill(t0, scope.Any)
	must(err)
	want := []pigtail.SkillHandbacks{
		{Skill: "", Count: 1, LastAt: t0.Add(5 * time.Minute)},
		{Skill: "my-plan", Count: 1, LastAt: t0.Add(time.Minute)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if work, _ := s.HandbacksBySkill(t0, "gx"); len(work) != 1 || work[0].Skill != "" {
		t.Fatalf("work view = %+v", work)
	}
	if none, err := s.HandbacksBySkill(t0, "acme"); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("a scope with no turn = %+v %v", none, err)
	}
	if err := s.RecordTurnEnd("s1", "perso", "p9", t0, t0, "other"); err == nil {
		t.Fatal("an unknown event was recorded")
	}
}

// D12: a turn in which the session declared a wait is not an undeclared handback.
func TestTurnEndWithDeclarationNotCounted(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if err := s.SetSessionWait("s1", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: t0.Add(30 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	// the loop clears the declared wait before Stop (WaitTracker, !sawBefore): the turn still reads as declared
	if _, err := s.ClearSessionWaits("s1", pigtail.WaitSourceDeclared); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTurnEnd("s1", "perso", "p1", t0, t0.Add(time.Minute), "stop"); err != nil {
		t.Fatal(err)
	}
	// a declaration older than the turn (not cleared yet) does not count for it
	if err := s.SetSessionWait("s2", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: t0.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTurnEnd("s2", "perso", "q1", t0, t0.Add(time.Minute), "stop"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.HandbacksBySkill(t0, "perso")
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("got %+v, want only s2's turn", got)
	}
}

// DeclaredSince is RecordTurnEnd's declaration test, read alone (the 🔄 inference).
func TestDeclaredSince(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if ok, err := s.DeclaredSince("s1", t0); err != nil || ok {
		t.Fatalf("never declared = %v, %v", ok, err)
	}
	if err := s.SetSessionWait("s1", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearSessionWaits("s1"); err != nil { // the declaration's time outlives its row
		t.Fatal(err)
	}
	if ok, _ := s.DeclaredSince("s1", t0); !ok {
		t.Fatal("a declaration at the turn's start not seen")
	}
	if ok, _ := s.DeclaredSince("s1", t0.Add(time.Millisecond)); ok {
		t.Fatal("an older declaration seen")
	}
	if err := s.SetSessionWait("s2", pigtail.SessionWait{Type: "permission", Source: pigtail.WaitSourceAuto, Since: t0}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.DeclaredSince("s2", t0); ok {
		t.Fatal("an auto wait read as a declaration")
	}
}

// sub_x000000000000041: /standby hands back without a declaration (a wait
// would hold the /compact): a turn ended while the session has its Standby
// row is not an undeclared hand-back. Once the Standby ends, a turn counts.
func TestTurnEndInStandbyNotCounted(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if err := s.SetSessionSkill("s1", "standby", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStandby("s1", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTurnEnd("s1", "perso", "p1", t0, t0.Add(time.Minute), "stop"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.HandbacksBySkill(t0, "perso"); len(got) != 0 {
		t.Fatalf("in Standby: got %+v, want no hand-back", got)
	}
	if _, err := s.ClearStandby("s1", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTurnEnd("s1", "perso", "p2", t0.Add(3*time.Minute), t0.Add(4*time.Minute), "stop"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.HandbacksBySkill(t0, "perso")
	if len(got) != 1 || got[0].Skill != "standby" || got[0].Count != 1 {
		t.Fatalf("out of Standby: got %+v, want one standby turn", got)
	}
}

func TestTurnSucceededSince(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	ok, err := s.TurnSucceededSince("s1", t0)
	if err != nil || ok {
		t.Fatalf("no turn: %v %v", ok, err)
	}
	if err := s.RecordTurnEnd("s1", "perso", "stop-before", t0.Add(-2*time.Minute), t0.Add(-time.Minute), "stop"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.TurnSucceededSince("s1", t0); ok {
		t.Fatal("a Stop before t is no success since t")
	}
	if err := s.RecordTurnEnd("s1", "perso", "fail-after", t0, t0.Add(time.Minute), "failure"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.TurnSucceededSince("s1", t0); ok {
		t.Fatal("a failed turn is no success")
	}
	if err := s.RecordTurnEnd("s1", "perso", "stop-after", t0, t0.Add(2*time.Minute), "stop"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TurnSucceededSince("s1", t0); err != nil || !ok {
		t.Fatalf("a Stop after t: %v %v", ok, err)
	}
	if ok, _ := s.TurnSucceededSince("s2", t0); ok {
		t.Fatal("another session's turn counted")
	}
}
