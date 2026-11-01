package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Work/perso isolation of the relay's sessions themselves: a work caller never
// learns a perso session id from the relay, and never acts as a session of
// another scope (send as it, claim, wait on or ack its messages). The caller
// is X-Pigtail-Scope; the session's scope is the one its registry row holds.

func (f *relayFixture) sendAs(t *testing.T, c caller, req pigtail.SendMessageRequest) (*httptest.ResponseRecorder, pigtail.SendMessageResponse) {
	t.Helper()
	rec := doAs(t, f.r, c, "POST", "/messages", req)
	var out pigtail.SendMessageResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestPeersHidePersoSessionsFromWork(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	snapshot := pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: "s-work"}, {SessionID: "s-perso"}, {SessionID: "s-perso2"},
	}}
	for _, c := range []caller{none, work} {
		for _, method := range []string{"GET", "POST"} {
			var body any
			if method == "POST" {
				body = snapshot
			}
			for _, path := range []string{"/peers", "/peers?all=1"} {
				if method == "POST" && path != "/peers" {
					continue
				}
				rec := doAs(t, f.r, c, method, path, body)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s as %s = %d %s", method, path, c, rec.Code, rec.Body.String())
				}
				if b := rec.Body.String(); strings.Contains(b, "s-perso") || strings.Contains(b, "perso-a") || strings.Contains(b, "Perso two") {
					t.Errorf("%s %s as %s shows a perso session: %s", method, path, c, b)
				}
				if b := rec.Body.String(); !strings.Contains(b, "s-work") {
					t.Errorf("%s %s as %s lost a work session: %s", method, path, c, b)
				}
			}
		}
	}
	ps := peersAs(t, f, perso, "GET", nil)
	for _, id := range []string{"s-work", "s-mate", "s-onperso", "s-perso", "s-perso2"} {
		if _, ok := ps[id]; !ok {
			t.Errorf("perso caller must see every session, %s missing", id)
		}
	}
}

// The session-bound routes act for a session named in the request. A caller
// whose scope is not that session's gets exactly what an unknown session gets,
// and nothing is claimed, acked, sent or superseded.
func TestSessionBoundRoutesRefuseAnotherScope(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	rec, sent := f.sendAs(t, perso, pigtail.SendMessageRequest{From: "s-perso2", To: "perso-a", Body: "perso secret"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("perso send = %d %s", rec.Code, rec.Body.String())
	}
	id := sent.Message.ID

	// What an unknown session / message gets, per route.
	unknownSend := doAs(t, f.r, none, "POST", "/messages", pigtail.SendMessageRequest{From: "s-nobody", To: "perso-a", Body: "x"})
	unknownClaim := doAs(t, f.r, none, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-nobody"})
	unknownWait := doAs(t, f.r, none, "GET", "/messages/wait?session=s-nobody&timeout=1", nil)
	unknownAck := doAs(t, f.r, none, "POST", "/messages/msg_nope/ack", pigtail.AckRequest{Session: "s-perso"})
	if unknownSend.Code != http.StatusBadRequest || unknownClaim.Code != http.StatusNotFound ||
		unknownWait.Code != http.StatusNotFound || unknownAck.Code != http.StatusNotFound {
		t.Fatalf("unknown: send %d, claim %d, wait %d, ack %d", unknownSend.Code, unknownClaim.Code, unknownWait.Code, unknownAck.Code)
	}
	same := func(what string, got, want *httptest.ResponseRecorder, sessionIn, sessionWant string) {
		t.Helper()
		if got.Code != want.Code || strings.ReplaceAll(got.Body.String(), sessionIn, sessionWant) != want.Body.String() {
			t.Errorf("%s: %d %s, want the unknown answer %d %s", what, got.Code, got.Body.String(), want.Code, want.Body.String())
		}
	}

	for _, c := range []caller{none, work} {
		same("send as perso from "+c.String(),
			doAs(t, f.r, c, "POST", "/messages", pigtail.SendMessageRequest{From: "s-perso2", To: "perso-a", Body: "x"}),
			unknownSend, "s-perso2", "s-nobody")
		same("claim perso as "+c.String(),
			doAs(t, f.r, c, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-perso"}),
			unknownClaim, "s-perso", "s-nobody")
		same("wait perso as "+c.String(),
			doAs(t, f.r, c, "GET", "/messages/wait?session=s-perso&timeout=1", nil),
			unknownWait, "s-perso", "s-nobody")
		same("ack perso as "+c.String(),
			doAs(t, f.r, c, "POST", "/messages/"+id+"/ack", pigtail.AckRequest{Session: "s-perso"}),
			unknownAck, id, "msg_nope")
	}
	// Equality, not "may read": a perso caller does not act as a work session.
	if rec := doAs(t, f.r, perso, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-mate"}); rec.Code != http.StatusNotFound {
		t.Errorf("perso claiming a work session = %d, want 404", rec.Code)
	}
	if rec := doAs(t, f.r, caller{scope: "everything"}, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-perso"}); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid scope header = %d, want 400", rec.Code)
	}

	m, err := f.st.GetMessage(id)
	if err != nil || m.State != pigtail.MessageStatePending {
		t.Fatalf("the perso message was touched by a foreign caller: %+v %v", m, err)
	}
	if n, _ := f.st.CountThread(sent.Message.ThreadID); n != 1 {
		t.Fatalf("a foreign caller added to the perso thread: %d messages", n)
	}

	// The session's own scope still does everything.
	rec = doAs(t, f.r, perso, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-perso", Via: "prompt"})
	var cl pigtail.ClaimResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &cl)
	if rec.Code != http.StatusOK || len(cl.Messages) != 1 || cl.Messages[0].ID != id {
		t.Fatalf("perso claiming its own inbox = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(t, f.r, perso, "POST", "/messages/"+id+"/ack", pigtail.AckRequest{Session: "s-perso"}); rec.Code != http.StatusOK {
		t.Fatalf("perso ack = %d %s", rec.Code, rec.Body.String())
	}
	// And a work session its own (headerless = work).
	if rec := doAs(t, f.r, none, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-mate"}); rec.Code != http.StatusOK {
		t.Fatalf("work claiming its own inbox = %d %s", rec.Code, rec.Body.String())
	}
}

// A work wait on a perso session must not register a waiter: it would
// supersede the perso session's own watcher.
func TestWorkWaitDoesNotSupersedePersoWatcher(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	got := make(chan pigtail.ClaimResponse, 1)
	go func() {
		rec := doAs(t, f.r, perso, "GET", "/messages/wait?session=s-perso&timeout=5", nil)
		var cr pigtail.ClaimResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &cr)
		got <- cr
	}()
	testutil.Eventually(t, 30*time.Second, "perso watcher registered", func() bool { return f.rl.watched("s-perso") })
	if rec := doAs(t, f.r, work, "GET", "/messages/wait?session=s-perso&timeout=1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("work wait on perso = %d", rec.Code)
	}
	f.sendAs(t, perso, pigtail.SendMessageRequest{From: "s-perso2", To: "perso-a", Body: "wake"})
	select {
	case cr := <-got:
		if cr.Superseded || len(cr.Messages) != 1 {
			t.Fatalf("perso watcher = %+v", cr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("perso watcher never woke")
	}
}

// A work sender never resolves a perso session: it is not a candidate, so
// neither an error nor an ambiguity list names it.
func TestWorkSenderDoesNotSeePersoSessions(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-work-shared", CmuxTitle: "Shared board", Name: "ts", Scope: "gx", CellID: "mt"})
	doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: "s-perso-shared", CmuxTitle: "Shared notes", Name: "ps", Scope: "perso", CellID: "mp"},
	}})

	rec, _ := f.sendAs(t, work, pigtail.SendMessageRequest{From: "s-mate", To: "Perso two", Body: "x"})
	if rec.Code != http.StatusNotFound || errCode(rec) != string(pigtail.ErrAddressUnresolved) || strings.Contains(rec.Body.String(), "s-perso") {
		t.Fatalf("work → perso title = %d %s, want 404 unresolved", rec.Code, rec.Body.String())
	}
	rec, out := f.sendAs(t, work, pigtail.SendMessageRequest{From: "s-mate", To: "shared", Body: "x"})
	if rec.Code != http.StatusCreated || out.Message.ToSession != "s-work-shared" {
		t.Fatalf("work → \"shared\" = %d %s, want the work session alone", rec.Code, rec.Body.String())
	}
	// A perso sender still sees work sessions, and is refused by scope.
	rec, _ = f.sendAs(t, perso, pigtail.SendMessageRequest{From: "s-perso2", To: "Mate", Body: "x"})
	if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrScopeMismatch) {
		t.Fatalf("perso → work = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// POST /peers is an upsert any snapshot may send, perso rows included. A work
// caller may refresh them, but never change a stored scope nor end a session
// of another scope — either would reopen the session-bound routes to it.
func TestPostPeersWorkCannotRescopeOrEndPerso(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	doAs(t, f.r, work, "POST", "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: "s-perso", Scope: "gx", CellID: "mt", Name: "renamed"},
	}})
	p, err := f.st.GetPeer("s-perso")
	if err != nil || p.Scope != "perso" || p.CellID != "mp" {
		t.Fatalf("work caller rescoped a perso session: %+v %v", p, err)
	}
	if rec := doAs(t, f.r, work, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-perso"}); rec.Code != http.StatusNotFound {
		t.Fatalf("after a rescoping attempt, work claim = %d", rec.Code)
	}
	doAs(t, f.r, none, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s-perso"}}})
	if p, _ := f.st.GetPeer("s-perso"); p.EndedAt != nil {
		t.Fatal("work caller ended a perso session")
	}
	// Its own scope ends it.
	doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s-perso"}}})
	if p, _ := f.st.GetPeer("s-perso"); p.EndedAt == nil {
		t.Fatal("perso caller could not end its session")
	}
	// A perso caller may fix a stored scope (a session moved between cells).
	doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{{SessionID: "s-mate", Scope: "perso", CellID: "mp"}}})
	if p, _ := f.st.GetPeer("s-mate"); p.Scope != "perso" {
		t.Fatalf("perso caller could not rescope: %+v", p)
	}
}
