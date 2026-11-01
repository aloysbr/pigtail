package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func peer(t *testing.T, st *Store, id, surface string) {
	t.Helper()
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: id, CmuxSurfaceID: surface, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// D28 (sub_x000000000000041): a message does not expire while its recipient is
// in Standby, and its countdown resumes at the recipient's return.

func standbyMsgs(t *testing.T) (*Store, time.Time) {
	t.Helper()
	st := openMem(t)
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	for _, m := range []struct{ id, to string }{{"m1", "s1"}, {"m2", "s2"}} {
		msg := newMsg(m.id, "from", m.to, t0)
		if err := st.CreateMessage(msg); err != nil { // TTL 1 h
			t.Fatal(err)
		}
	}
	return st, t0
}

func TestExpirySuspendedInStandby(t *testing.T) {
	st, t0 := standbyMsgs(t)
	if err := st.SetStandby("s1", t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	due, err := st.ExpireDue(t0.Add(2 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != "m2" { // positive control: s2 has no Standby
		t.Fatalf("expired = %+v, want m2 only", due)
	}
	if m, _ := st.GetMessage("m1"); m.State != "pending" {
		t.Fatalf("m1 = %+v", m)
	}
	if n, err := st.CountPending("s1", t0.Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("CountPending = %d, %v", n, err)
	}
	rs, err := st.PendingRecipients(t0.Add(2 * time.Hour))
	if err != nil || len(rs) != 1 || rs[0] != "s1" {
		t.Fatalf("PendingRecipients = %v, %v", rs, err)
	}
	got, err := st.ClaimPending("s1", "prompt", t0.Add(2*time.Hour))
	if err != nil || len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("ClaimPending = %+v, %v", got, err)
	}
}

func TestExpiryResumesAfterStandby(t *testing.T) {
	setup := func(t *testing.T) *Store {
		st, t0 := standbyMsgs(t)
		if err := st.SetStandby("s1", t0.Add(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		m3 := newMsg("m3", "from", "s1", t0.Add(time.Hour)) // received during the Standby
		if err := st.CreateMessage(m3); err != nil {
			t.Fatal(err)
		}
		return st
	}
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	check := func(t *testing.T, st *Store) {
		t.Helper()
		m1, _ := st.GetMessage("m1")
		m3, _ := st.GetMessage("m3")
		if !m1.ExpiresAt.Equal(t0.Add(3*time.Hour + 50*time.Minute)) {
			t.Fatalf("m1 expires %v, want t0+3h50m (the 50 min left)", m1.ExpiresAt.Sub(t0))
		}
		if !m3.ExpiresAt.Equal(t0.Add(4 * time.Hour)) {
			t.Fatalf("m3 expires %v, want t0+4h (its whole TTL)", m3.ExpiresAt.Sub(t0))
		}
		if due, _ := st.ExpireDue(t0.Add(3*time.Hour + 49*time.Minute)); len(due) != 1 || due[0].ID != "m2" {
			t.Fatalf("expired too soon: %+v", due) // m2 (no Standby) expired long ago; nothing else
		}
		if due, _ := st.ExpireDue(t0.Add(3*time.Hour + 51*time.Minute)); len(due) != 1 || due[0].ID != "m1" {
			t.Fatalf("at t0+3h51m: %+v, want m1 only", due)
		}
	}
	t.Run("ClearStandby", func(t *testing.T) {
		st := setup(t)
		if _, err := st.ClearStandby("s1", t0.Add(3*time.Hour)); err != nil {
			t.Fatal(err)
		}
		check(t, st)
	})
	t.Run("replaced on the surface", func(t *testing.T) {
		st := setup(t)
		peer(t, st, "s1", "SF")
		peer(t, st, "s2", "SF")
		if ids, err := st.EndStandbyReplacedOnSurface("sf", "s2", t0.Add(3*time.Hour)); err != nil || len(ids) != 1 {
			t.Fatalf("replaced = %v, %v", ids, err)
		}
		check(t, st)
	})
	t.Run("a re-set row loses no time and counts none twice", func(t *testing.T) {
		st := setup(t)
		if err := st.SetStandby("s1", t0.Add(2*time.Hour)); err != nil { // /standby again
			t.Fatal(err)
		}
		if _, err := st.ClearStandby("s1", t0.Add(3*time.Hour)); err != nil {
			t.Fatal(err)
		}
		check(t, st)
	})
	t.Run("a row that is not there extends nothing", func(t *testing.T) {
		st, t0 := standbyMsgs(t)
		if gone, err := st.ClearStandby("s1", t0.Add(3*time.Hour)); err != nil || gone {
			t.Fatalf("clear = %v, %v", gone, err)
		}
		if m, _ := st.GetMessage("m1"); !m.ExpiresAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("m1 expires %v", m.ExpiresAt)
		}
	})
}
