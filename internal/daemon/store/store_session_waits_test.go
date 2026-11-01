package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestSessionWaitsSetReplaceClear(t *testing.T) {
	st := openMem(t)
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := st.SetSessionWait("s1", pigtail.SessionWait{Type: "arbitrage", Reason: "short title", Source: pigtail.WaitSourceDeclared, Since: t0}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionWait("s1", pigtail.SessionWait{Type: "permission", Reason: "Bash", Source: pigtail.WaitSourceAuto, Since: t0.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	// The last declaration wins.
	if err := st.SetSessionWait("s1", pigtail.SessionWait{Type: "review", Reason: "PR #1", Source: pigtail.WaitSourceDeclared, Since: t0.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	ws, err := st.SessionWaits("s1")
	if err != nil || len(ws) != 2 {
		t.Fatalf("waits = %+v, %v", ws, err)
	}
	if ws[0].Source != pigtail.WaitSourceAuto || ws[0].Type != "permission" || ws[0].Reason != "Bash" || !ws[0].Since.Equal(t0.Add(time.Second)) {
		t.Fatalf("auto = %+v", ws[0])
	}
	if ws[1].Source != pigtail.WaitSourceDeclared || ws[1].Type != "review" || ws[1].Reason != "PR #1" {
		t.Fatalf("declared = %+v", ws[1])
	}

	all, err := st.ListSessionWaits()
	if err != nil || len(all["s1"]) != 2 {
		t.Fatalf("list = %+v, %v", all, err)
	}

	n, err := st.ClearSessionWaits("s1", pigtail.WaitSourceAuto)
	if err != nil || n != 1 {
		t.Fatalf("clear auto = %d, %v", n, err)
	}
	if ws, _ := st.SessionWaits("s1"); len(ws) != 1 || ws[0].Source != pigtail.WaitSourceDeclared {
		t.Fatalf("after clearing auto: %+v", ws)
	}
	if n, _ := st.ClearSessionWaits("s1"); n != 1 {
		t.Fatalf("clear all removed %d, want 1", n)
	}
	if n, _ := st.ClearSessionWaits("s1"); n != 0 {
		t.Fatalf("clearing nothing removed %d", n)
	}
	if ws, _ := st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("left: %+v", ws)
	}
}

func TestSessionWaitRejectsUnknownSource(t *testing.T) {
	st := openMem(t)
	if err := st.SetSessionWait("s1", pigtail.SessionWait{Type: "review", Source: "guess", Since: time.Now()}); err == nil {
		t.Fatal("an unknown source was stored")
	}
}

// SessionEnd ends the wait with the session.
func TestEndPeerClearsWaits(t *testing.T) {
	st := openMem(t)
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	_ = st.SetSessionWait("s1", pigtail.SessionWait{Type: "blocked", Source: pigtail.WaitSourceDeclared, Since: time.Now()})
	if err := st.EndPeer("s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ws, _ := st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("an ended session still waits: %+v", ws)
	}
}

// An auto wait's until survives the round trip; absent = nil (A2, Review Focus 3).
func TestSessionWaitUntilRoundTrip(t *testing.T) {
	s := openMem(t)
	until := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if err := s.SetSessionWait("s1", pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: until.Add(-time.Hour), Until: &until}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionWait("s2", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: until}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListSessionWaits()
	if err != nil {
		t.Fatal(err)
	}
	if w := all["s1"]; len(w) != 1 || w[0].Until == nil || !w[0].Until.Equal(until) {
		t.Fatalf("s1 = %+v", w)
	}
	if w := all["s2"]; len(w) != 1 || w[0].Until != nil {
		t.Fatalf("s2 = %+v", w)
	}
	// A later auto wait without until replaces the row whole: no stale until.
	if err := s.SetSessionWait("s1", pigtail.SessionWait{Type: "permission", Source: pigtail.WaitSourceAuto, Since: until}); err != nil {
		t.Fatal(err)
	}
	if w, _ := s.SessionWaits("s1"); len(w) != 1 || w[0].Until != nil {
		t.Fatalf("until survived a replace: %+v", w)
	}
}

// A held wait keeps its holder pid and until, coexists with a declared one.
func TestSessionWaitHeldRoundTrip(t *testing.T) {
	s := openMem(t)
	until := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if err := s.SetSessionWait("s1", pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, HolderPID: 4242, Since: until.Add(-time.Hour), Until: &until}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionWait("s1", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: until}); err != nil {
		t.Fatal(err)
	}
	ws, err := s.SessionWaits("s1")
	if err != nil || len(ws) != 2 {
		t.Fatalf("waits = %+v %v", ws, err)
	}
	if ws[0].Source != pigtail.WaitSourceDeclared || ws[0].HolderPID != 0 {
		t.Fatalf("declared = %+v", ws[0])
	}
	h := ws[1]
	if h.Source != pigtail.WaitSourceHeld || h.HolderPID != 4242 || h.Until == nil || !h.Until.Equal(until) {
		t.Fatalf("held = %+v", h)
	}
	if err := s.SetSessionWait("s1", pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, Since: until}); err == nil {
		t.Fatal("held without holder pid stored")
	}
}

// Default clear = the turn's waits (auto, declared): held survives; an
// explicit "held" clears only it.
func TestClearSessionWaitsKeepsHeldByDefault(t *testing.T) {
	s := openMem(t)
	now := time.Now()
	for _, w := range []pigtail.SessionWait{
		{Type: "permission", Source: pigtail.WaitSourceAuto, Since: now},
		{Type: "review", Source: pigtail.WaitSourceDeclared, Since: now},
		{Type: "mac", Source: pigtail.WaitSourceHeld, HolderPID: 7, Since: now},
	} {
		if err := s.SetSessionWait("s1", w); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.ClearSessionWaits("s1"); err != nil || n != 2 {
		t.Fatalf("default clear = %d %v", n, err)
	}
	if ws, _ := s.SessionWaits("s1"); len(ws) != 1 || ws[0].Source != pigtail.WaitSourceHeld {
		t.Fatalf("held lost: %+v", ws)
	}
	_ = s.SetSessionWait("s1", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: now})
	if n, err := s.ClearSessionWaits("s1", pigtail.WaitSourceHeld); err != nil || n != 1 {
		t.Fatalf("held clear = %d %v", n, err)
	}
	if ws, _ := s.SessionWaits("s1"); len(ws) != 1 || ws[0].Source != pigtail.WaitSourceDeclared {
		t.Fatalf("declared lost: %+v", ws)
	}
}

// ClearHeldWait deletes only the held row of that holder pid.
func TestClearHeldWaitConditional(t *testing.T) {
	s := openMem(t)
	now := time.Now()
	if ok, err := s.ClearHeldWait("s1", 4242); err != nil || ok {
		t.Fatalf("no held: %v %v", ok, err)
	}
	_ = s.SetSessionWait("s1", pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, HolderPID: 5151, Since: now})
	if ok, err := s.ClearHeldWait("s1", 4242); err != nil || ok {
		t.Fatalf("other holder: %v %v", ok, err)
	}
	if ws, _ := s.SessionWaits("s1"); len(ws) != 1 {
		t.Fatalf("held of another holder was touched: %+v", ws)
	}
	if ok, err := s.ClearHeldWait("s1", 5151); err != nil || !ok {
		t.Fatalf("own holder: %v %v", ok, err)
	}
	if ws, _ := s.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("held survived: %+v", ws)
	}
}

// SessionEnd drops the held wait too.
func TestEndPeerClearsHeld(t *testing.T) {
	st := openMem(t)
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	_ = st.SetSessionWait("s1", pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, HolderPID: 9, Since: time.Now()})
	if err := st.EndPeer("s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ws, _ := st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("an ended session still holds: %+v", ws)
	}
}

func TestSetAutoWaitReasonOnlyMatchingSince(t *testing.T) {
	s := newTestStore(t)
	since := time.Date(2026, 10, 30, 9, 0, 0, 123_000_000, time.UTC)
	until := since.Add(time.Hour)
	if err := s.SetSessionWait("s1", pigtail.SessionWait{Type: "api_error", Source: pigtail.WaitSourceAuto, Reason: "server_error", Since: since, Until: &until}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionWait("s2", pigtail.SessionWait{Type: "api_error", Source: pigtail.WaitSourceDeclared, Reason: "mine", Since: since}); err != nil {
		t.Fatal(err)
	}
	read := func(id, src string) pigtail.SessionWait {
		t.Helper()
		ws, err := s.SessionWaits(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws {
			if w.Source == src {
				return w
			}
		}
		t.Fatalf("no %s wait for %s", src, id)
		return pigtail.SessionWait{}
	}
	if ok, err := s.SetAutoWaitReason("s1", "api_error", since, "relance à 09:01 (1/5)"); err != nil || !ok {
		t.Fatalf("matching since: %v %v", ok, err)
	}
	w := read("s1", pigtail.WaitSourceAuto)
	if w.Reason != "relance à 09:01 (1/5)" || w.Until == nil || !w.Until.Equal(until) || !w.Since.Equal(since) {
		t.Fatalf("after rewrite: %+v", w)
	}
	if ok, _ := s.SetAutoWaitReason("s1", "api_error", since.Add(time.Second), "x"); ok {
		t.Fatal("another since must change nothing")
	}
	if ok, _ := s.SetAutoWaitReason("s1", "limit", since, "x"); ok {
		t.Fatal("another type must change nothing")
	}
	if ok, _ := s.SetAutoWaitReason("s2", "api_error", since, "x"); ok {
		t.Fatal("a declared wait must change nothing")
	}
	if r := read("s2", pigtail.WaitSourceDeclared).Reason; r != "mine" {
		t.Fatalf("declared reason touched: %q", r)
	}
	if r := read("s1", pigtail.WaitSourceAuto).Reason; r != "relance à 09:01 (1/5)" {
		t.Fatalf("reason changed by a non-matching call: %q", r)
	}
}
