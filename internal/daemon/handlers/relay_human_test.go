package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The human sender (sub_x000000000000026, Task 7): the user's answer to a wait
// is a relay message from pigtail.HumanSender. No peer stands behind it, so
// every "not a peer session" test treats it like the relay itself; no expiry
// notice goes back to it, no reply goes to it, and no peer may register
// under its id.

func seedHumanMessage(t *testing.T, f *relayFixture, id, to string) pigtail.Message {
	t.Helper()
	now := f.clock()
	m := pigtail.Message{ID: id, ThreadID: "wait_x", FromSession: pigtail.HumanSender, FromLabel: "Ada",
		ToSession: to, ToLabel: to, Kind: pigtail.MessageKindInfo, Body: "Answer from the user", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := f.st.CreateMessage(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHumanSenderMessageVisibleInItsScope(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-work", Name: "t", Scope: "gx"}, pigtail.PeerSession{SessionID: "s-perso", Name: "p", Scope: "perso"})
	toWork := seedHumanMessage(t, f, "msg_h1", "s-work")
	toPerso := seedHumanMessage(t, f, "msg_h2", "s-perso")
	workScope := scope.Canonical("gx")
	if !f.rl.messageVisible(workScope, toWork) {
		t.Fatal("work cannot read Ada' answer to its own session")
	}
	if f.rl.messageVisible(workScope, toPerso) {
		t.Fatal("work reads Ada' answer to a perso session")
	}
	if !f.rl.sessionReadable(workScope, pigtail.HumanSender) {
		t.Fatal("the human sender is unreadable from work")
	}
	rec := doAs(t, f.r, work, "GET", "/messages?session=s-work", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "msg_h1") || strings.Contains(rec.Body.String(), "msg_h2") {
		t.Fatalf("GET /messages as work = %d %s", rec.Code, rec.Body.String())
	}
	if sc := f.rl.conversationScope(toWork); sc != workScope {
		t.Fatalf("conversation scope of an answer to work = %q, want %q", sc, workScope)
	}
}

func TestExpireSkipsHumanSender(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "one", Scope: "gx"})
	seedHumanMessage(t, f, "msg_h1", "s1")
	f.advance(2 * time.Hour)
	f.rl.expire()
	m, err := f.st.GetMessage("msg_h1")
	if err != nil || m.State != pigtail.MessageStateExpired {
		t.Fatalf("positive control: the answer did not expire: %+v %v", m, err)
	}
	all, _ := f.st.ListMessages(pigtail.MessageFilter{})
	for _, x := range all {
		if x.ToSession == pigtail.HumanSender {
			t.Fatalf("an expiry notice went to the human sender: %+v", x)
		}
	}
}

func TestHumanSenderAnswerTakesNoReply(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "one", Scope: "gx"})
	seedHumanMessage(t, f, "msg_h1", "s1")
	rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s1", InReplyTo: "msg_h1", Body: "ok"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "answers from the user take no reply") {
		t.Fatalf("reply to the human = %d %s", rec.Code, rec.Body.String())
	}
}

func TestPostPeersRefusesReservedSenderIDs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		req  pigtail.PeersRequest
	}{
		{"human", pigtail.PeersRequest{Peers: []pigtail.PeerSession{{SessionID: "s-ok", Name: "ok"}, {SessionID: pigtail.HumanSender, Name: "Ada"}}}},
		{"pigtail", pigtail.PeersRequest{Peers: []pigtail.PeerSession{{SessionID: pigtail.SystemSender}, {SessionID: "s-ok", Name: "ok"}}}},
		{"end human", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: pigtail.HumanSender}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRelayFixture(t)
			rec := doJSON(t, f.r, "POST", "/peers", c.req)
			if rec.Code != http.StatusBadRequest || errCode(rec) != string(pigtail.ErrInvalidRequest) || !strings.Contains(rec.Body.String(), "is reserved") {
				t.Fatalf("POST /peers = %d %s", rec.Code, rec.Body.String())
			}
			for _, id := range []string{pigtail.HumanSender, pigtail.SystemSender, "s-ok"} {
				if _, err := f.st.GetPeer(id); err == nil {
					t.Fatalf("peer %s written despite the refusal", id)
				}
			}
		})
	}
}
