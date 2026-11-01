package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// the user's answer to a wait (sub_x000000000000026, Task 7) is a message from
// pigtail.HumanSender: no peer row stands behind it, so a scoped view keeps
// it on its recipient's scope, like a message of the relay itself.

func seedHumanAnswer(t *testing.T) *Store {
	t.Helper()
	st := openMem(t)
	for _, p := range []pigtail.PeerSession{{SessionID: "t1", Scope: "gx"}, {SessionID: "p1", Scope: "perso"}} {
		p := p
		if err := st.UpsertPeer(&p); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for _, m := range []*pigtail.Message{
		newMsg("m_answer_work", pigtail.HumanSender, "t1", now),
		newMsg("m_answer_perso", pigtail.HumanSender, "p1", now.Add(time.Second)),
		newMsg("m_sys_work", pigtail.SystemSender, "t1", now.Add(2*time.Second)),
	} {
		if err := st.CreateMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func msgIDs(ms []pigtail.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestHumanSenderMessageVisibleInItsScopeStore(t *testing.T) {
	st := seedHumanAnswer(t)
	got, err := st.ListMessagesInScope(pigtail.MessageFilter{To: "t1"}, scope.Scope("globex"))
	if err != nil {
		t.Fatal(err)
	}
	if g := msgIDs(got); len(g) != 2 || g[0] != "m_answer_work" || g[1] != "m_sys_work" {
		t.Fatalf("work view of t1 = %v, want [m_answer_work m_sys_work]", g)
	}
	got, _ = st.ListMessagesInScope(pigtail.MessageFilter{}, scope.Scope("globex"))
	for _, id := range msgIDs(got) {
		if id == "m_answer_perso" {
			t.Fatalf("work view shows an answer to a perso session: %v", msgIDs(got))
		}
	}
}

// A filter on the relay (from=pigtail) never returns the user's answers: the
// notice branch of fromIs stays on SystemSender alone.
func TestListMessagesFromFilterKeepsHumanApart(t *testing.T) {
	st := seedHumanAnswer(t)
	for _, sc := range []scope.Scope{scope.Scope("globex"), scope.Any} {
		got, err := st.ListMessagesInScope(pigtail.MessageFilter{From: pigtail.SystemSender}, sc)
		if err != nil {
			t.Fatal(err)
		}
		if g := msgIDs(got); len(g) != 1 || g[0] != "m_sys_work" {
			t.Fatalf("%s: from=pigtail = %v, want [m_sys_work]", sc, g)
		}
	}
	got, _ := st.ListMessagesInScope(pigtail.MessageFilter{From: pigtail.HumanSender}, scope.Scope("globex"))
	if g := msgIDs(got); len(g) != 1 || g[0] != "m_answer_work" {
		t.Fatalf("from=human in work = %v, want [m_answer_work]", g)
	}
}
