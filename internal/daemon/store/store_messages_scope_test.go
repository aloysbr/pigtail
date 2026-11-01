package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestListMessagesInScope(t *testing.T) {
	st := openMem(t)
	for _, p := range []pigtail.PeerSession{
		{SessionID: "t1", Scope: "gx"}, {SessionID: "t2", Scope: "gx"},
		{SessionID: "p1", Scope: "perso"}, {SessionID: "p2", Scope: "perso"},
		{SessionID: "u1"}, // no scope: fail-closed
	} {
		p := p
		if err := st.UpsertPeer(&p); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for _, m := range []*pigtail.Message{
		newMsg("m_work", "t1", "t2", now),
		newMsg("m_perso", "p1", "p2", now.Add(time.Second)),
		newMsg("m_sys_work", pigtail.SystemSender, "t1", now.Add(2*time.Second)),
		newMsg("m_sys_perso", pigtail.SystemSender, "p1", now.Add(3*time.Second)),
		newMsg("m_unknown", "u1", "t1", now.Add(4*time.Second)),
		newMsg("m_gone", "t1", "nobody", now.Add(5*time.Second)),
	} {
		if err := st.CreateMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(ms []pigtail.Message) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	got, err := st.ListMessagesInScope(pigtail.MessageFilter{}, scope.Scope("globex"))
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); len(g) != 2 || g[0] != "m_work" || g[1] != "m_sys_work" {
		t.Fatalf("work view = %v, want [m_work m_sys_work]", g)
	}
	got, _ = st.ListMessagesInScope(pigtail.MessageFilter{Limit: 1}, scope.Scope("globex"))
	if g := ids(got); len(g) != 1 || g[0] != "m_sys_work" {
		t.Fatalf("limit must count visible messages: %v", g)
	}
	got, _ = st.ListMessagesInScope(pigtail.MessageFilter{}, scope.Scope("perso"))
	if g := ids(got); len(g) != 2 || g[0] != "m_perso" || g[1] != "m_sys_perso" {
		t.Fatalf("perso-only view = %v", g)
	}
	got, _ = st.ListMessages(pigtail.MessageFilter{})
	if len(got) != 6 {
		t.Fatalf("unscoped listing = %v", ids(got))
	}
}

// A notice is visible from its recipient's scope whatever the sender's (D1):
// the recipient must see and ack it. Any other kind still needs both sides.
func TestListMessagesInScopeShowsANoticeToItsRecipientScope(t *testing.T) {
	st := openMem(t)
	for _, p := range []pigtail.PeerSession{{SessionID: "t1", Scope: "gx"}, {SessionID: "p1", Scope: "perso"}} {
		p := p
		if err := st.UpsertPeer(&p); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	notice := newMsg("m_notice", "p1", "t1", now)
	notice.Kind = pigtail.MessageKindNotice
	info := newMsg("m_info", "p1", "t1", now.Add(time.Second))
	backwards := newMsg("m_notice_out", "t1", "p1", now.Add(2*time.Second))
	backwards.Kind = pigtail.MessageKindNotice
	for _, m := range []*pigtail.Message{notice, info, backwards} {
		if err := st.CreateMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListMessagesInScope(pigtail.MessageFilter{}, scope.Scope("globex"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "m_notice" {
		t.Fatalf("work view = %+v, want [m_notice] alone", got)
	}
}
