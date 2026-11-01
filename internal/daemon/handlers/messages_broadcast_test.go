package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func (f *relayFixture) broadcastAs(t *testing.T, c caller, req pigtail.BroadcastRequest) (*httptest.ResponseRecorder, pigtail.BroadcastResponse) {
	t.Helper()
	rec := doAs(t, f.r, c, "POST", "/messages/broadcast", req)
	var out pigtail.BroadcastResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func recipients(out pigtail.BroadcastResponse) []string {
	var ids []string
	for _, r := range out.Results {
		ids = append(ids, r.To.SessionID)
	}
	sort.Strings(ids)
	return ids
}

// D2: without the opt-in, every cell — the master included — reaches its own
// scope only, never itself.
func TestBroadcastStaysInTheSenderScope(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	rec, out := f.broadcastAs(t, work, pigtail.BroadcastRequest{From: "s-work", Body: "reboot at six"})
	if rec.Code != http.StatusOK || strings.Join(recipients(out), ",") != "s-mate,s-onperso" || out.Sent != 2 {
		t.Fatalf("work broadcast = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "s-perso") {
		t.Fatalf("work broadcast names a perso session: %s", rec.Body.String())
	}
	// C7/D19: a non-master learns nothing of the other scopes — the field is absent.
	if out.OtherScopesSkipped != 0 || strings.Contains(rec.Body.String(), "other_scopes_skipped") {
		t.Fatalf("work broadcast reports other scopes: %s", rec.Body.String())
	}
	rec, out = f.broadcastAs(t, perso, pigtail.BroadcastRequest{From: "s-perso", Body: "reboot at six"})
	if rec.Code != http.StatusOK || strings.Join(recipients(out), ",") != "s-perso2" {
		t.Fatalf("master broadcast without opt-in = %d %s, want its own scope", rec.Code, rec.Body.String())
	}
	// C7: the master's forgotten opt-in is never silent — the 3 work sessions
	// it did not reach are counted (s-work, s-mate, s-onperso).
	if out.OtherScopesSkipped != 3 {
		t.Fatalf("master without opt-in: other_scopes_skipped = %d, want 3: %s", out.OtherScopesSkipped, rec.Body.String())
	}
	for _, r := range out.Results {
		m, _ := f.st.GetMessage(r.MessageID)
		if m == nil || m.Kind != pigtail.MessageKindNotice {
			t.Fatalf("broadcast stored %+v, want a notice", m)
		}
	}
}

// D2/T3: the master with the opt-in reaches every scope; a full inbox is one
// refused line, the others are sent.
func TestMasterBroadcastReachesEveryScopePartially(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	for i := 0; i < relay.MaxPendingPerPeer; i++ { // fill s-mate's inbox from its own scope (RateMax = 5 allows it)
		if rec, _ := f.sendAs(t, work, pigtail.SendMessageRequest{From: "s-work", To: "Mate", Body: "x"}); rec.Code != http.StatusCreated {
			t.Fatalf("seed %d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec, out := f.broadcastAs(t, persoCross, pigtail.BroadcastRequest{From: "s-perso", Body: "reboot at six"})
	if rec.Code != http.StatusOK || strings.Join(recipients(out), ",") != "s-mate,s-onperso,s-perso2,s-work" {
		t.Fatalf("master broadcast = %d %s", rec.Code, rec.Body.String())
	}
	if out.Sent != 3 || out.Refused != 1 {
		t.Fatalf("sent=%d refused=%d, want 3/1: %s", out.Sent, out.Refused, rec.Body.String())
	}
	for _, r := range out.Results {
		if r.To.SessionID == "s-mate" && (r.Refused != pigtail.ErrRelayRefused || !strings.Contains(r.Reason, "unread")) {
			t.Errorf("full inbox result = %+v", r)
		}
		if r.To.SessionID != "s-mate" && (r.MessageID == "" || r.Delivery == "") {
			t.Errorf("sent result lacks its id or delivery: %+v", r)
		}
	}
}

// C4: the opt-in widens nothing from a non-master — refused, nothing sent.
func TestBroadcastRefusesCrossScopeFromANonMaster(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	rec, _ := f.broadcastAs(t, workCross, pigtail.BroadcastRequest{From: "s-work", Body: "x"})
	if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrScopeMismatch) {
		t.Fatalf("work+cross broadcast = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "perso") {
		t.Fatalf("refusal names the master scope (D19): %s", rec.Body.String())
	}
	if ms, _ := f.st.ListMessages(pigtail.MessageFilter{From: "s-work"}); len(ms) != 0 {
		t.Fatalf("refused broadcast still sent %d messages", len(ms))
	}
}

// Body rules apply once, before any recipient; an unknown sender reads like
// POST /messages; nobody to notify is an empty, successful answer.
func TestBroadcastRequestLevelRefusals(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	if rec, _ := f.broadcastAs(t, work, pigtail.BroadcastRequest{From: "s-work", Body: "ghp_abcdefghijklmnop"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("secret body = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := f.broadcastAs(t, work, pigtail.BroadcastRequest{From: "s-perso", Body: "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("work acting as a perso sender = %d %s, want 400 like an unknown one", rec.Code, rec.Body.String())
	}
	g := newRelayFixture(t)
	g.peers(t, pigtail.PeerSession{SessionID: "alone", Name: "alone", Scope: "globex"})
	rec, out := g.broadcastAs(t, work, pigtail.BroadcastRequest{From: "alone", Body: "x"})
	if rec.Code != http.StatusOK || out.Sent != 0 || len(out.Results) != 0 {
		t.Fatalf("broadcast to nobody = %d %s", rec.Code, rec.Body.String())
	}
}
