package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func notice(from, to, body string) pigtail.SendMessageRequest {
	return pigtail.SendMessageRequest{From: from, To: to, Kind: pigtail.MessageKindNotice, Body: body}
}

// T1: the master needs the opt-in, and with it only a notice crosses (D3).
func TestMasterNoticeCrossesOnlyWithTheOptIn(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	rec, _ := f.sendAs(t, perso, notice("s-perso", "work-a", "reboot soon"))
	if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrScopeMismatch) || !strings.Contains(rec.Body.String(), "--cross-scope") {
		t.Fatalf("master notice without opt-in = %d %s, want 403 naming --cross-scope", rec.Code, rec.Body.String())
	}
	for _, kind := range []string{pigtail.MessageKindInfo, pigtail.MessageKindRequest} {
		rec, _ = f.sendAs(t, persoCross, pigtail.SendMessageRequest{From: "s-perso", To: "work-a", Kind: kind, Body: "x"})
		if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrScopeMismatch) {
			t.Fatalf("master %s with opt-in = %d %s, want 403", kind, rec.Code, rec.Body.String())
		}
	}
	rec, out := f.sendAs(t, persoCross, notice("s-perso", "work-a", "reboot soon"))
	if rec.Code != http.StatusCreated || out.Message.ToSession != "s-work" || out.Message.Kind != pigtail.MessageKindNotice {
		t.Fatalf("master notice with opt-in = %d %s", rec.Code, rec.Body.String())
	}
	if out.Message.FromLabel != relay.NoticeSenderLabel || out.Message.SubjectID != "" {
		t.Fatalf("stored notice carries the sender: %+v", out.Message)
	}
}

// T1: the opt-in never widens a non-master: another scope's session is no
// candidate, the answer is the unknown-address one, naming nothing.
func TestNonMasterOptInReachesNoOtherScope(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	rec, _ := f.sendAs(t, workCross, notice("s-work", "perso-a", "x"))
	if rec.Code != http.StatusNotFound || errCode(rec) != string(pigtail.ErrAddressUnresolved) || strings.Contains(rec.Body.String(), "s-perso") {
		t.Fatalf("work+cross → perso = %d %s, want 404 unresolved", rec.Code, rec.Body.String())
	}
}

// T2/D23/D19: the recipient sees and acks its notice through every path, and
// none of them names the master's session, subject, cell or scope — not even
// through its own label when it works on a subject of the master (work-p).
func TestCrossScopeNoticeShowsNothingOfTheMasterToTheRecipient(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	workSub, persoSub := scopedPeers(f, t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe("message.*")
	defer sub.Close()

	rec, out := f.sendAs(t, persoCross, notice("s-perso", "work-p", "zz reboot at six"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed notice = %d %s", rec.Code, rec.Body.String())
	}
	id := out.Message.ID
	if _, out2 := f.sendAs(t, persoCross, notice("s-perso", "work-a", "zz reboot at six")); out2.Message.ID == "" {
		t.Fatal("seed second notice")
	}
	// Markers searched in the whole body. The master scope's bare name is not
	// one of them: the recipient's own id (s-onperso, in to_session) holds it,
	// legitimately — it is checked by scopeFree, in the fields that could
	// carry it from the sender's side.
	leaks := []string{"s-perso", "perso-a", "(mp)", persoSub.ID, persoSub.Slug}
	const masterScope = "perso" // the master of the built-in table (a _test.go literal)
	scopeFree := func(what string, ms ...pigtail.Message) {
		t.Helper()
		if len(ms) == 0 {
			t.Fatalf("%s: positive control failed, no message decoded", what)
		}
		for _, m := range ms {
			for field, v := range map[string]string{"from_session": m.FromSession, "from_label": m.FromLabel,
				"subject_id": m.SubjectID, "to_label": m.ToLabel} {
				if strings.Contains(v, masterScope) {
					t.Errorf("%s: %s = %q names the master scope (D19)", what, field, v)
				}
			}
		}
	}
	check := func(what, body string) {
		t.Helper()
		if !strings.Contains(body, "zz reboot at six") && !strings.Contains(body, id) {
			t.Fatalf("%s: positive control failed, the notice is not there: %s", what, body)
		}
		for _, l := range leaks {
			if strings.Contains(body, l) {
				t.Errorf("%s shows %q to the work recipient: %s", what, l, body)
			}
		}
	}
	list := doAs(t, f.r, work, "GET", "/messages?session=s-onperso", nil)
	check("GET /messages", list.Body.String())
	var listed []pigtail.Message
	_ = json.Unmarshal(list.Body.Bytes(), &listed)
	scopeFree("GET /messages", listed...)
	// D19: a filter naming a session the caller cannot read answers exactly
	// like one naming an unknown session — else ?from=s-perso (notices back)
	// vs ?from=s-unknown ([]) would prove the master session exists.
	for _, param := range []string{"from", "session", "to"} {
		master := doAs(t, f.r, work, "GET", "/messages?"+param+"=s-perso", nil)
		unknown := doAs(t, f.r, work, "GET", "/messages?"+param+"=s-unknown", nil)
		if master.Code != unknown.Code || master.Body.String() != unknown.Body.String() {
			t.Errorf("?%s=s-perso = %d %s, ?%s=s-unknown = %d %s: must be identical (D19)",
				param, master.Code, master.Body.String(), param, unknown.Code, unknown.Body.String())
		}
	}
	one := doAs(t, f.r, work, "GET", "/messages/"+id, nil)
	check("GET /messages/{id}", one.Body.String())
	var got pigtail.Message
	_ = json.Unmarshal(one.Body.Bytes(), &got)
	scopeFree("GET /messages/{id}", got)
	claimed := doAs(t, f.r, work, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-onperso"})
	check("claim", claimed.Body.String())
	var cr pigtail.ClaimResponse
	_ = json.Unmarshal(claimed.Body.Bytes(), &cr)
	if len(cr.Messages) != 1 || cr.Messages[0].FromSession != pigtail.SystemSender || cr.Messages[0].FromLabel != relay.NoticeSenderLabel {
		t.Fatalf("claimed = %+v, want the neutral sender", cr.Messages)
	}
	scopeFree("claim", cr.Messages...)
	frame := relay.Frame(cr.Messages[0])
	check("frame", frame)
	if strings.Contains(frame, masterScope) { // the frame carries no to_session: the name alone is a leak
		t.Errorf("frame names the master scope (D19):\n%s", frame)
	}
	// 3d: the wait route returns the second notice (to work-a, still pending)
	// with the same neutral sender. timeout=0: claim once, never block.
	waited := doAs(t, f.r, work, "GET", "/messages/wait?session=s-work&timeout=0", nil)
	check("wait", waited.Body.String())
	var wr pigtail.ClaimResponse
	_ = json.Unmarshal(waited.Body.Bytes(), &wr)
	if len(wr.Messages) != 1 || wr.Messages[0].FromSession != pigtail.SystemSender || wr.Messages[0].FromLabel != relay.NoticeSenderLabel {
		t.Fatalf("waited = %d %+v, want the neutral sender", waited.Code, wr.Messages)
	}
	scopeFree("wait", wr.Messages...)
	ack := doAs(t, f.r, work, "POST", "/messages/"+id+"/ack", pigtail.AckRequest{Session: "s-onperso"})
	if ack.Code != http.StatusOK {
		t.Fatalf("work ack of its notice = %d %s", ack.Code, ack.Body.String())
	}
	check("ack", ack.Body.String())
	var acked pigtail.Message
	_ = json.Unmarshal(ack.Body.Bytes(), &acked)
	scopeFree("ack", acked)
	// Subject logs: nothing on the recipient's side (D1), one line on the sender's.
	logs, _ := f.st.ListLog(workSub.ID, time.Time{})
	for _, e := range logs {
		if e.Kind == "message" {
			t.Errorf("work subject log got a message line: %q", e.Text)
		}
	}
	plogs, _ := f.st.ListLog(persoSub.ID, time.Time{})
	if n := countPrefix(plogs, "→ "); n != 2 {
		t.Errorf("perso subject log: %d sender lines, want 2: %+v", n, plogs)
	}
	// Events: the recipient's scope sees them, redacted; the master sees them too.
	deadline := time.After(time.Second)
	for got := 0; got < 5; got++ { // created ×2, delivered ×2 (claim, wait), acked
		select {
		case e := <-sub.C:
			b, _ := json.Marshal(e.Data)
			if e.Scope != "globex" {
				t.Errorf("%s tagged %q, want the recipient's scope", e.Type, e.Scope)
			}
			for _, l := range leaks {
				if strings.Contains(string(b), l) {
					t.Errorf("%s event shows %q: %s", e.Type, l, b)
				}
			}
			var em pigtail.Message // the event data carries the message's wire fields
			_ = json.Unmarshal(b, &em)
			scopeFree(string(e.Type)+" event", em)
		case <-deadline:
			t.Fatalf("only %d message events", got)
		}
	}
}

func countPrefix(es []pigtail.SubjectLogEntry, p string) int {
	n := 0
	for _, e := range es {
		if strings.HasPrefix(e.Text, p) {
			n++
		}
	}
	return n
}

// D1: a notice takes no reply — not even a same-scope one.
func TestReplyToANoticeIsRefused(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	_, out := f.sendAs(t, work, notice("s-mate", "work-a", "heads up"))
	if out.Message.ID == "" || out.Message.SubjectID != "" {
		t.Fatalf("same-scope notice = %+v", out.Message)
	}
	rec, _ := f.sendAs(t, work, pigtail.SendMessageRequest{From: "s-work", InReplyTo: out.Message.ID, Body: "ok"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "no reply") {
		t.Fatalf("reply to a notice = %d %s, want 422", rec.Code, rec.Body.String())
	}
}

// C3: an unread notice expires without telling its sender.
func TestExpiredNoticeSendsNoExpiryNotice(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	req := notice("s-perso", "work-a", "reboot soon")
	req.TTLSeconds = 60
	rec, out := f.sendAs(t, persoCross, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
	}
	f.advance(2 * time.Minute)
	doAs(t, f.r, perso, "GET", "/messages", nil) // runs expire()
	// Positive control: expire() did run on the notice — else "no expiry
	// notice" below would hold for a pass that never happened.
	if m, err := f.st.GetMessage(out.Message.ID); err != nil || m.State != pigtail.MessageStateExpired {
		t.Fatalf("notice after its TTL = %+v %v, want state expired", m, err)
	}
	ms, _ := f.st.ListMessages(pigtail.MessageFilter{To: "s-perso"})
	if len(ms) != 0 {
		t.Fatalf("the sender got %d expiry notices: %+v", len(ms), ms)
	}
}

// The from/session filters match what the caller reads: a cross-scope notice
// reads as from pigtail to its recipient, so ?from=pigtail (and
// ?session=pigtail) returns it — while a same-scope notice keeps its real
// sender and does not match, and ?from=<the real master session> still answers
// like an unknown session (D19).
func TestFilterOnPigtailMatchesRedactedNotices(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	scopedPeers(f, t)
	_, cross := f.sendAs(t, persoCross, notice("s-perso", "work-a", "zz cross"))
	_, same := f.sendAs(t, work, notice("s-mate", "work-a", "zz same"))
	if cross.Message.ID == "" || same.Message.ID == "" {
		t.Fatalf("seed: cross=%+v same=%+v", cross.Message, same.Message)
	}
	for _, param := range []string{"from", "session"} {
		rec := doAs(t, f.r, work, "GET", "/messages?"+param+"=pigtail", nil)
		var got []pigtail.Message
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("?%s=pigtail = %d %s", param, rec.Code, rec.Body.String())
		}
		if len(got) != 1 || got[0].ID != cross.Message.ID || got[0].FromSession != pigtail.SystemSender {
			t.Errorf("?%s=pigtail as work = %+v, want the cross-scope notice alone, from pigtail", param, got)
		}
	}
	// The master reads the real sender: ?from=pigtail does not show it the
	// notice it sent itself, ?from=s-perso does.
	var master []pigtail.Message
	_ = json.Unmarshal(doAs(t, f.r, perso, "GET", "/messages?from=pigtail", nil).Body.Bytes(), &master)
	if len(master) != 0 {
		t.Errorf("?from=pigtail as master = %+v, want none", master)
	}
	_ = json.Unmarshal(doAs(t, f.r, perso, "GET", "/messages?from=s-perso", nil).Body.Bytes(), &master)
	if len(master) != 1 || master[0].ID != cross.Message.ID {
		t.Errorf("?from=s-perso as master = %+v, want its notice", master)
	}
	// D19 unchanged.
	a := doAs(t, f.r, work, "GET", "/messages?from=s-perso", nil)
	b := doAs(t, f.r, work, "GET", "/messages?from=s-unknown", nil)
	if a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Errorf("?from=s-perso = %s, ?from=s-unknown = %s: must be identical (D19)", a.Body.String(), b.Body.String())
	}
}
