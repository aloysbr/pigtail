package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func openMem(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestUpsertPeerMergesWithoutErasing(t *testing.T) {
	st := openMem(t)
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", ConfigDir: "/c", CmuxWorkspaceID: "W", Source: "hook"}); err != nil {
		t.Fatal(err)
	}
	// A later snapshot knows the pid and name but not the cmux workspace.
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", PID: 42, Name: "alpha", Source: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPeer("s1")
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigDir != "/c" || p.CmuxWorkspaceID != "W" || p.PID != 42 || p.Name != "alpha" || p.Source != "snapshot" {
		t.Fatalf("merge lost a field: %+v", p)
	}
	if err := st.EndPeer("s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	live, _ := st.ListPeers(false)
	if len(live) != 0 {
		t.Fatalf("ended peer still listed: %+v", live)
	}
	// Upserting again revives it (a resumed session keeps its id).
	_ = st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1"})
	live, _ = st.ListPeers(false)
	if len(live) != 1 || live[0].EndedAt != nil {
		t.Fatalf("revive failed: %+v", live)
	}
}

func newMsg(id, from, to string, now time.Time) *pigtail.Message {
	return &pigtail.Message{ID: id, ThreadID: "thr_1", FromSession: from, ToSession: to, Kind: "info", Body: "hi",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
}

func TestClaimPendingIsAtomicAndOrdered(t *testing.T) {
	st := openMem(t)
	now := time.Now().UTC()
	for i, id := range []string{"msg_a", "msg_b"} {
		if err := st.CreateMessage(newMsg(id, "s1", "s2", now.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
	}
	n, _ := st.CountPending("s2", now)
	if n != 2 {
		t.Fatalf("pending = %d", n)
	}
	got, err := st.ClaimPending("s2", "prompt", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "msg_a" || got[1].State != "delivered" || got[1].DeliveredVia != "prompt" {
		t.Fatalf("claim = %+v", got)
	}
	again, _ := st.ClaimPending("s2", "tool", now)
	if len(again) != 0 {
		t.Fatalf("second claim got %d messages", len(again))
	}
}

func TestAckAndExpire(t *testing.T) {
	st := openMem(t)
	now := time.Now().UTC()
	_ = st.CreateMessage(newMsg("msg_a", "s1", "s2", now))
	old := newMsg("msg_old", "s1", "s2", now.Add(-2*time.Hour))
	_ = st.CreateMessage(old)

	if err := st.AckMessage("msg_a", now); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetMessage("msg_a")
	if m.State != "acked" || m.AckedAt == nil || m.DeliveredAt == nil {
		t.Fatalf("ack = %+v", m)
	}
	if err := st.AckMessage("msg_nope", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ack unknown: %v", err)
	}
	exp, err := st.ExpireDue(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(exp) != 1 || exp[0].ID != "msg_old" {
		t.Fatalf("expired = %+v", exp)
	}
	if err := st.AckMessage("msg_old", now); err == nil {
		t.Fatal("acking an expired message should fail")
	}
}

func TestListMessagesFiltersAndCounts(t *testing.T) {
	st := openMem(t)
	now := time.Now().UTC()
	_ = st.CreateMessage(newMsg("msg_1", "s1", "s2", now))
	m2 := newMsg("msg_2", "s2", "s1", now.Add(time.Millisecond))
	m2.Kind, m2.InReplyTo, m2.SubjectID = "reply", "msg_1", "sub_x"
	_ = st.CreateMessage(m2)
	_ = st.CreateMessage(&pigtail.Message{ID: "msg_3", ThreadID: "thr_2", FromSession: "s1", ToSession: "s3", Kind: "info", Body: "x", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})

	th, _ := st.ListMessages(pigtail.MessageFilter{Thread: "thr_1"})
	if len(th) != 2 || th[1].InReplyTo != "msg_1" {
		t.Fatalf("thread = %+v", th)
	}
	sub, _ := st.ListMessages(pigtail.MessageFilter{Subject: "sub_x"})
	if len(sub) != 1 {
		t.Fatalf("subject = %+v", sub)
	}
	either, _ := st.ListMessages(pigtail.MessageFilter{Session: "s2"})
	if len(either) != 2 {
		t.Fatalf("session = %+v", either)
	}
	last, _ := st.ListMessages(pigtail.MessageFilter{Limit: 1})
	if len(last) != 1 {
		t.Fatalf("limit = %+v", last)
	}
	c, _ := st.CountThread("thr_1")
	r, _ := st.CountSentSince("s1", "s2", now.Add(-time.Minute))
	if c != 2 || r != 1 {
		t.Fatalf("thread=%d rate=%d", c, r)
	}
	if err := st.CreateMessage(&pigtail.Message{ID: "msg_bad", ThreadID: "t", FromSession: "a", ToSession: "b", Kind: "shout", Body: "x", CreatedAt: now, ExpiresAt: now}); err == nil {
		t.Fatal("kind CHECK not enforced")
	}
}

func TestUpsertPeerLastWorktreeNewestWins(t *testing.T) {
	st := openMem(t)
	t1 := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	must := func(p pigtail.PeerSession) {
		t.Helper()
		if err := st.UpsertPeer(&p); err != nil {
			t.Fatal(err)
		}
	}
	must(pigtail.PeerSession{SessionID: "s1", LastWorktree: "/wt/b", LastWorktreeAt: &t2, Source: "snapshot"})
	must(pigtail.PeerSession{SessionID: "s1", LastWorktree: "/wt/a", LastWorktreeAt: &t1, Source: "snapshot"}) // older: ignored
	must(pigtail.PeerSession{SessionID: "s1", Source: "hook"})                                                 // no marker: erases nothing
	p, err := st.GetPeer("s1")
	if err != nil {
		t.Fatal(err)
	}
	if p.LastWorktree != "/wt/b" || p.LastWorktreeAt == nil || !p.LastWorktreeAt.Equal(t2) {
		t.Fatalf("last worktree = %q at %v, want /wt/b at %v", p.LastWorktree, p.LastWorktreeAt, t2)
	}
	t3 := t2.Add(time.Minute)
	must(pigtail.PeerSession{SessionID: "s1", LastWorktree: "/wt/c", LastWorktreeAt: &t3})
	p, _ = st.GetPeer("s1")
	if p.LastWorktree != "/wt/c" {
		t.Fatalf("newer marker not taken: %q", p.LastWorktree)
	}
}

// The wake sweep asks recipients oldest message first, and reads whether one
// of the pending messages is the user's own answer (the budget exemption).
func TestPendingRecipientsByAgeOrdersByOldestMessage(t *testing.T) {
	st := openMem(t)
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	// Insertion order: s-b (t0+1m), s-a (t0+2m), s-b again (t0+3m... newer),
	// s-c (t0): the oldest message decides, not the recipient's first insert.
	for _, m := range []struct {
		id, from, to string
		at           time.Duration
	}{
		{"m1", "x", "s-b", time.Minute},
		{"m2", "x", "s-a", 2 * time.Minute},
		{"m3", "x", "s-b", 3 * time.Minute},
		{"m4", pigtail.HumanSender, "s-c", 0},
		{"m5", "x", "s-d", -3 * time.Hour}, // expired at now: ignored
	} {
		if err := st.CreateMessage(newMsg(m.id, m.from, m.to, t0.Add(m.at))); err != nil {
			t.Fatal(err)
		}
	}
	now := t0.Add(10 * time.Minute)
	got, err := st.PendingRecipientsByAge(now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Session)
	}
	if want := []string{"s-c", "s-b", "s-a"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("order = %v, want %v (positive control: s-d expired)", names, want)
	}
	if !got[0].OldestAt.Equal(t0) || !got[1].OldestAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("oldest = %v / %v", got[0].OldestAt, got[1].OldestAt)
	}
	if !got[0].HumanPending || got[1].HumanPending || got[2].HumanPending {
		t.Fatalf("human flags = %v %v %v, want true false false", got[0].HumanPending, got[1].HumanPending, got[2].HumanPending)
	}
}

func TestCountPendingExceptKind(t *testing.T) {
	st := openMem(t)
	now := time.Now().UTC()
	mk := func(id, to, kind string, expires time.Time) {
		m := newMsg(id, "s1", to, now)
		m.Kind = kind
		m.ExpiresAt = expires
		if err := st.CreateMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	mk("msg_info", "s2", "info", now.Add(time.Hour))
	mk("msg_notice", "s2", pigtail.MessageKindNotice, now.Add(time.Hour))
	mk("msg_only_notice", "s3", pigtail.MessageKindNotice, now.Add(time.Hour))
	mk("msg_due", "s4", "info", now.Add(-time.Minute))
	mk("msg_standby", "s5", "info", now.Add(-time.Minute))
	if err := st.SetStandby("s5", now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		to   string
		want int
	}{{"s2", 1}, {"s3", 0}, {"s4", 0}, {"s5", 1}} {
		if n, err := st.CountPendingExceptKind(c.to, pigtail.MessageKindNotice, now); err != nil || n != c.want {
			t.Fatalf("%s: CountPendingExceptKind = %d, %v; want %d", c.to, n, err, c.want)
		}
	}
	if n, _ := st.CountPending("s2", now); n != 2 {
		t.Fatalf("CountPending s2 = %d, want 2", n)
	}
}
