package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func (f *relayFixture) longWait(t *testing.T, session string) pigtail.ClaimResponse {
	t.Helper()
	return f.decodeWait(t, doJSON(t, f.r, "GET", "/messages/wait?session="+session+"&timeout=1", nil))
}

// decodeWait checks and decodes the response of a long wait.
func (f *relayFixture) decodeWait(t *testing.T, rec *httptest.ResponseRecorder) pigtail.ClaimResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("wait = %d %s", rec.Code, rec.Body.String())
	}
	var cr pigtail.ClaimResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &cr); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return cr
}

func (f *relayFixture) sendBody(t *testing.T, from, to, body string) {
	t.Helper()
	if rec, _ := f.send(t, pigtail.SendMessageRequest{From: from, To: to, Body: body}); rec.Code != http.StatusCreated {
		t.Fatalf("send = %d %s", rec.Code, rec.Body.String())
	}
}

// D16 (Tâche 10): the long poll of the Stop watcher claims nothing for a
// session in Standby — not the message there, not one arriving during the
// wait —, says why once, and serves both once the Standby ends.
func TestWaitMessagesHeldInStandby(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	lg := &wakeLog{}
	f.rl.svc.Logf = lg.logf
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "one", Scope: "gx"}, pigtail.PeerSession{SessionID: "s2", Name: "two", Scope: "gx"})
	if err := f.st.SetStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.sendBody(t, "s2", "s1", "first")
	if cr := f.longWait(t, "s1"); len(cr.Messages) != 0 {
		t.Fatalf("first wait served %+v", cr.Messages)
	}
	// The second message arrives during the second wait: the wait runs in a
	// goroutine that only reports its response (no t.Fatal off the test's
	// goroutine), the test sends once the waiter is registered — or once the
	// wait already answered (its 1 s ran out first, on a loaded machine).
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		second <- doJSON(t, f.r, "GET", "/messages/wait?session=s1&timeout=1", nil)
	}()
	testutil.Eventually(t, 30*time.Second, "second wait registered", func() bool { return f.hasWaiter("s1") || len(second) > 0 })
	_, _ = f.send(t, pigtail.SendMessageRequest{From: "s2", To: "s1", Body: "second"})
	if cr := f.decodeWait(t, <-second); len(cr.Messages) != 0 {
		t.Fatalf("second wait served %+v", cr.Messages)
	}
	if n, _ := f.st.CountPending("s1", f.clock()); n != 2 {
		t.Fatalf("pending = %d, want 2", n)
	}
	ms, _ := f.st.ListMessages(pigtail.MessageFilter{To: "s1"})
	for _, m := range ms {
		if m.DeliveredAt != nil {
			t.Fatalf("delivered in Standby: %+v", m)
		}
	}
	if n := lg.count("inbox wait held: standby (D16)"); n != 1 {
		t.Fatalf("held line %d times:\n%s", n, lg)
	}
	if _, err := f.st.ClearStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	cr := f.longWait(t, "s1")
	if len(cr.Messages) != 2 || cr.Messages[0].DeliveredVia != "rewake" {
		t.Fatalf("after the Standby: %+v", cr.Messages)
	}
}

// An unreadable Standby holds the long poll too, never read as free.
func TestWaitMessagesHeldWhenStandbyUnreadable(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	lg := &wakeLog{}
	f.rl.svc.Logf = lg.logf
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "one", Scope: "gx"}, pigtail.PeerSession{SessionID: "s2", Name: "two", Scope: "gx"})
	f.sendBody(t, "s2", "s1", "first")
	f.rl.getStandby = func(string) (*pigtail.SessionStandby, error) { return nil, errors.New("disk gone") }
	if cr := f.longWait(t, "s1"); len(cr.Messages) != 0 {
		t.Fatalf("served %+v", cr.Messages)
	}
	if n, _ := f.st.CountPending("s1", f.clock()); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}
	if lg.count("inbox wait held: standby unreadable") != 1 {
		t.Fatalf("reason missing:\n%s", lg)
	}
}

// D28 (Tâche 17): the prompt hook can claim before claude-wait's DELETE (two
// hooks in parallel): a message whose original expiry passed during the
// Standby is still served.
func TestClaimAtPromptSeesSuspendedMessage(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "one", Scope: "gx"}, pigtail.PeerSession{SessionID: "s2", Name: "two", Scope: "gx"})
	f.sendBody(t, "s2", "s1", "while you were away")
	if err := f.st.SetStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.advance(2 * relay.DefaultTTL)
	rec := doJSON(t, f.r, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s1", Via: "prompt"})
	var cr pigtail.ClaimResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &cr); err != nil || rec.Code != http.StatusOK || len(cr.Messages) != 1 || cr.Messages[0].Body != "while you were away" {
		t.Fatalf("claim = %d %s", rec.Code, rec.Body.String())
	}
}

// D28: the relay's expiry sweep holds a message for a session in Standby — no
// message.expired, no « expired unread » notice to the sender — and expires it
// once the countdown, resumed at the return, runs out.
func TestRelayExpireHoldsStandbyMessage(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventMessageExpired))
	defer sub.Close()
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "one", Scope: "gx"}, pigtail.PeerSession{SessionID: "s2", Name: "two", Scope: "gx"})
	f.sendBody(t, "s2", "s1", "hold me")
	if err := f.st.SetStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.advance(2 * relay.DefaultTTL)
	f.rl.expire()
	select {
	case e := <-sub.C:
		t.Fatalf("message.expired during the Standby: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	if n, _ := f.st.CountPending("s2", f.clock()); n != 0 {
		t.Fatalf("the sender got %d notice(s) during the Standby", n)
	}
	if rec := doJSON(t, f.r, "DELETE", "/peers/s1/standby", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", rec.Code, rec.Body.String())
	}
	f.advance(relay.DefaultTTL - time.Hour)
	f.rl.expire()
	if n, _ := f.st.CountPending("s1", f.clock()); n != 1 {
		t.Fatal("the countdown did not resume: the message is gone early")
	}
	f.advance(2 * time.Hour)
	f.rl.expire()
	select {
	case <-sub.C:
	case <-time.After(2 * time.Second):
		t.Fatal("positive control: the message never expired after the Standby")
	}
	if n, _ := f.st.CountPending("s2", f.clock()); n != 1 {
		t.Fatalf("positive control: the sender got %d notice(s), want 1", n)
	}
}
