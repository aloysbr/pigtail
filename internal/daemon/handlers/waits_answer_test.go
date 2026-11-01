package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// POST /waits/{id}/answer (sub_x000000000000026, Task 9): the user's answer goes
// to the session as a relay message from pigtail.HumanSender, wakes it, lifts
// its Standby (D1) and leaves a trace in the subject log and the journal.
// Every refusal writes nothing.

func answerAs(t *testing.T, f *relayFixture, c caller, id string, req pigtail.WaitAnswerRequest) (*httptest.ResponseRecorder, pigtail.WaitAnswerResponse) {
	t.Helper()
	rec := doAs(t, f.r, c, "POST", "/waits/"+id+"/answer", req)
	var out pigtail.WaitAnswerResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return rec, out
}

// answerFixture: the wake fixture with a bus, s-alpha (idle, its pane
// verified) working on a work subject, and an arbitrage it declared.
func answerFixture(t *testing.T) (*relayFixture, *fakeWake, pigtail.Subject, string) {
	t.Helper()
	f, fw, _ := newWakeFixture(t)
	f.rl.svc.Bus = events.New()
	f.rl.svc.HumanName = "Ada"
	sub := f.seedSubjectWorktree(t, "Web front", "/wt/webfront")
	p := alphaPeer()
	p.Cwd = "/wt/webfront/cmd"
	f.wakePeers(t, p)
	postWait(t, f, "s-alpha", pigtail.SessionWaitRequest{Type: "arbitrage", Reason: "Sidebar order", Details: json.RawMessage(arbitrageDetails)})
	id := openRecord(t, f, "s-alpha", pigtail.WaitSourceDeclared).ID
	fw.resetCounts()
	return f, fw, sub, id
}

func eventsOf(f *relayFixture, typ pigtail.EventType) []pigtail.Event {
	return f.rl.svc.Bus.Replay("", []string{string(typ)})
}

func humanMessages(t *testing.T, f *relayFixture) []pigtail.Message {
	t.Helper()
	ms, err := f.st.ListMessages(pigtail.MessageFilter{From: pigtail.HumanSender})
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// nothingWritten: the wait is still open, no message from the user, no
// wait.answered.
func nothingWritten(t *testing.T, f *relayFixture, id string) {
	t.Helper()
	if w := getWait(t, f, caller{scope: string(scope.Master()), cross: true}, id); w.State != pigtail.WaitStateOpen {
		t.Fatalf("wait %s = %s, want open", id, w.State)
	}
	if ms := humanMessages(t, f); len(ms) != 0 {
		t.Fatalf("messages from Ada: %+v", ms)
	}
	if f.rl.svc.Bus != nil {
		if es := eventsOf(f, pigtail.EventWaitAnswered); len(es) != 0 {
			t.Fatalf("wait.answered published: %+v", es)
		}
	}
}

func TestAnswerDeliversWakesAndTraces(t *testing.T) {
	t.Parallel()
	f, _, sub, id := answerFixture(t)
	rec, out := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Choice: "reco", Reason: "cheaper to undo"})
	if rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	if out.Delivery != pigtail.DeliveryWake || out.MessageID == "" || out.StandbyEnded || out.Wait.State != pigtail.WaitStateAnswered ||
		out.DeliveredTo != "s-alpha" || out.DeliveredToLabel == "" || out.Wait.DeliveredTo != "s-alpha" {
		t.Fatalf("response = %+v", out)
	}
	m, err := f.st.GetMessage(out.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m.FromSession != pigtail.HumanSender || m.FromLabel != "Ada" || m.ToSession != "s-alpha" || m.ThreadID != id ||
		m.SubjectID != sub.ID || m.Kind != pigtail.MessageKindInfo || !strings.HasPrefix(m.Body, "Answer from Ada to your wait arbitrage") ||
		!m.ExpiresAt.Equal(m.CreatedAt.Add(relay.MaxTTL)) {
		t.Fatalf("message = %+v", m)
	}
	es := eventsOf(f, pigtail.EventWaitAnswered)
	if len(es) != 1 || es[0].Data["wait_id"] != id || es[0].Data["state"] != pigtail.WaitStateAnswered || es[0].Scope != string(scope.Canonical("gx")) {
		t.Fatalf("wait.answered = %+v", es)
	}
	if len(eventsOf(f, pigtail.EventMessageCreated)) != 1 {
		t.Fatal("no message.created")
	}
	w := getWait(t, f, none, id)
	if w.State != pigtail.WaitStateAnswered || w.Answer == nil || w.Answer.Choice != "A" || w.Answer.Reason != "cheaper to undo" || !w.Answer.At.Equal(f.clock()) {
		t.Fatalf("wait = %+v (answer %+v)", w, w.Answer)
	}
	if ws, _ := f.st.SessionWaits("s-alpha"); len(ws) != 0 {
		t.Fatalf("declared row left: %+v", ws)
	}
	var waitLines int
	logs, _ := f.st.ListLog(sub.ID, time.Time{})
	for _, l := range logs {
		if l.Kind == "wait" {
			waitLines++
			if !strings.HasPrefix(l.Text, "Ada → ") || !strings.Contains(l.Text, "[answer "+id+"]") || l.ID == "" {
				t.Fatalf("log line = %+v", l)
			}
		}
	}
	if waitLines != 1 {
		t.Fatalf("subject log = %+v, want one wait line", logs)
	}
	js, err := f.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "wait-answer"})
	if err != nil || len(js) != 1 || js[0].SubjectID != sub.ID || js[0].Source != "pigtail:wait/"+id || js[0].Scope != string(scope.Canonical("gx")) {
		t.Fatalf("journal = %+v %v", js, err)
	}
}

// D1, the real path: in Standby, the session's claude-inbox-wait watcher stays
// in the long-poll and claims nothing; the answer lifts the Standby before
// waking it, and the long-poll hands the answer over.
func TestAnswerLiftsStandbyDeliversToOpenWatcher(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s2", Name: "beta", Scope: "gx"})
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "blocked", Reason: "/login"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	if err := f.st.SetStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.sendBody(t, "s2", "s1", "a peer message held by the Standby")

	done := make(chan pigtail.ClaimResponse, 1)
	go func() {
		rec := doJSON(t, f.r, "GET", "/messages/wait?session=s1&timeout=5", nil)
		var cr pigtail.ClaimResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &cr)
		done <- cr
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.rl.mu.Lock()
		in := f.rl.waiters["s1"] != nil
		f.rl.mu.Unlock()
		if in {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the watcher never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case cr := <-done:
		t.Fatalf("the watcher returned during the Standby: %+v", cr)
	case <-time.After(100 * time.Millisecond):
	}
	if n, _ := f.st.CountPending("s1", f.clock()); n != 1 {
		t.Fatalf("pending before the answer = %d, want the peer message alone", n)
	}

	rec, out := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Seen: true})
	if rec.Code != http.StatusOK || !out.StandbyEnded || out.Delivery != pigtail.DeliveryWaiter {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	select {
	case cr := <-done:
		found := false
		for _, m := range cr.Messages {
			if m.FromSession == pigtail.HumanSender && m.ThreadID == id {
				found = true
			}
		}
		if !found {
			t.Fatalf("the long-poll returned without the answer: %+v", cr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the long-poll never returned the answer")
	}
	if sb, err := f.st.GetStandby("s1"); err != nil || sb != nil {
		t.Fatalf("standby after the answer: %+v %v", sb, err)
	}
}

// D1 without a watcher: the Standby goes (row, marker, event), the answer's
// expiry is not stretched by the lift (D28), and the next sweep wakes it.
func TestAnswerLiftsStandbyAndWakes(t *testing.T) {
	t.Parallel()
	f, fw, _, id := answerFixture(t)
	dir := f.withStandbyMarkers(t)
	if err := f.st.SetStandby("s-alpha", f.clock().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.rl.refreshStandbyMarker("s-alpha")
	if !standbyMarker(dir, "s-alpha") {
		t.Fatal("positive control: no Standby marker before the answer")
	}
	rec, out := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Choice: "B"})
	if rec.Code != http.StatusOK || !out.StandbyEnded {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	if sb, err := f.st.GetStandby("s-alpha"); err != nil || sb != nil {
		t.Fatalf("standby after the answer: %+v %v", sb, err)
	}
	if standbyMarker(dir, "s-alpha") {
		t.Fatal("the Standby marker survived the answer")
	}
	es := eventsOf(f, pigtail.EventSessionStandbyChanged)
	if len(es) != 1 || es[0].Data["standby"] != false || es[0].Data["session_id"] != "s-alpha" {
		t.Fatalf("session.standby_changed = %+v", es)
	}
	m, err := f.st.GetMessage(out.MessageID)
	if err != nil || !m.ExpiresAt.Equal(m.CreatedAt.Add(relay.MaxTTL)) {
		t.Fatalf("the lift stretched the answer's expiry: %+v %v", m, err)
	}
	f.rl.sweepIdle()
	if len(fw.sent) != 1 || fw.sent[0] != surf {
		t.Fatalf("wakes = %v, want the wake line in surf", fw.sent)
	}
	if len(fw.compacts) != 0 {
		t.Fatalf("compacted after the lift: %v", fw.compacts)
	}
}

func TestAnswerJournalSummaryOneLine(t *testing.T) {
	t.Parallel()
	f, _, _, id := answerFixture(t)
	rec, _ := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Text: strings.Repeat("é", 400), Reason: "second line"})
	if rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	js, err := f.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "wait-answer"})
	if err != nil || len(js) != 1 {
		t.Fatalf("journal = %+v %v", js, err)
	}
	s := js[0].Summary
	if strings.ContainsAny(s, "\r\n") || utf8.RuneCountInString(s) > pigtail.JournalSummaryMax || !strings.HasPrefix(s, "Answer from Ada") {
		t.Fatalf("summary (%d runes) = %q", utf8.RuneCountInString(s), s)
	}
}

func TestAnswerRefusesAutoDialog(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s2", Name: "beta", Scope: "gx"})
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Source: pigtail.WaitSourceAuto, Type: "question", Reason: "AskUserQuestion",
		Details: json.RawMessage(`{"summary":"which?","options":[{"id":"1","label":"a"}]}`)})
	q := openRecord(t, f, "s1", pigtail.WaitSourceAuto)
	postWait(t, f, "s2", hookPermission())
	perm := openRecord(t, f, "s2", pigtail.WaitSourceAuto).ID

	recQ, _ := answerAs(t, f, none, q.ID, pigtail.WaitAnswerRequest{Choice: "1"})
	recP, _ := answerAs(t, f, none, perm, pigtail.WaitAnswerRequest{Seen: true})
	if recQ.Code != http.StatusUnprocessableEntity || !strings.Contains(recQ.Body.String(), "answer it in the terminal") {
		t.Fatalf("auto question = %d %s", recQ.Code, recQ.Body.String())
	}
	if recQ.Body.String() != recP.Body.String() || recP.Code != recQ.Code {
		t.Fatalf("auto question %d %s ≠ permission %d %s", recQ.Code, recQ.Body.String(), recP.Code, recP.Body.String())
	}
	nothingWritten(t, f, q.ID)
	nothingWritten(t, f, perm)
}

func TestAnswerRefusesFromAgentSession(t *testing.T) {
	t.Parallel()
	f, _, _, id := answerFixture(t)
	req := httptest.NewRequest("POST", "/waits/"+id+"/answer", strings.NewReader(`{"choice":"A"}`))
	req.Header.Set(pigtail.HeaderSession, "s-beta")
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrUnauthorized) || !strings.Contains(rec.Body.String(), "from your terminal") {
		t.Fatalf("answer from a session = %d %s", rec.Code, rec.Body.String())
	}
	nothingWritten(t, f, id)
}

func TestAnswerRefusesPermission(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "permission", Reason: "make"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	rec, _ := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Seen: true})
	if rec.Code != http.StatusUnprocessableEntity || errCode(rec) != string(pigtail.ErrRelayRefused) || !strings.Contains(rec.Body.String(), "terminal") {
		t.Fatalf("permission = %d %s", rec.Code, rec.Body.String())
	}
	nothingWritten(t, f, id)
}

func TestAnswerRefusesClosed(t *testing.T) {
	t.Parallel()
	f, _, _, id := answerFixture(t)
	if rec, _ := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Choice: "A"}); rec.Code != http.StatusOK {
		t.Fatalf("first answer = %d %s", rec.Code, rec.Body.String())
	}
	rec, _ := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Choice: "B"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "is answered") {
		t.Fatalf("second answer = %d %s", rec.Code, rec.Body.String())
	}
	if ms := humanMessages(t, f); len(ms) != 1 {
		t.Fatalf("messages from Ada = %d, want 1", len(ms))
	}
}

func TestAnswerAcceptanceKoWithoutNote(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "Acceptance",
		Details: json.RawMessage(`{"items":[{"label":"one"},{"label":"two"}]}`)})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	rec, _ := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Items: []string{"1=ok", "2=ko"}})
	if rec.Code != http.StatusBadRequest || errCode(rec) != string(pigtail.ErrInvalidRequest) || !strings.Contains(rec.Body.String(), "note") {
		t.Fatalf("ko without note = %d %s", rec.Code, rec.Body.String())
	}
	nothingWritten(t, f, id)
}

func TestAnswerRefusesSecretBeforeWriting(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "blocked", Reason: "token"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	rec, _ := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Text: "here: ghp_0123456789abcdefghij0123456789abcd"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "text") || strings.Contains(rec.Body.String(), "ghp_") {
		t.Fatalf("secret = %d %s", rec.Code, rec.Body.String())
	}
	nothingWritten(t, f, id)
}

// Review Focus 5: the message write fails — the wait stays open, nothing is
// published, the Standby stays.
func TestAnswerMessageInsertFailureKeepsWaitOpen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pigtail.db")
	f := newRelayFixtureAt(t, path)
	f.rl.svc.Bus = events.New()
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "alpha", Scope: "gx"})
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "blocked", Reason: "/login"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	if err := f.st.SetStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	failInserts(t, path, "messages")
	rec, _ := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Seen: true})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	nothingWritten(t, f, id)
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 1 || ws[0].Source != pigtail.WaitSourceDeclared {
		t.Fatalf("declared row = %+v", ws)
	}
	if es := eventsOf(f, pigtail.EventMessageCreated); len(es) != 0 {
		t.Fatalf("message.created published: %+v", es)
	}
	if sb, err := f.st.GetStandby("s1"); err != nil || sb == nil {
		t.Fatalf("the Standby was lifted by a failed answer: %+v %v", sb, err)
	}
}

func TestAnswerOtherScopeReadsAsUnknown(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.rl.svc.Bus = events.New()
	f.peers(t, pigtail.PeerSession{SessionID: "s-perso", Name: "pa", Scope: "perso"}, pigtail.PeerSession{SessionID: "s-work", Name: "ta", Scope: "gx"})
	if rec := doAs(t, f.r, perso, "POST", "/peers/s-perso/wait", pigtail.SessionWaitRequest{Type: "blocked", Reason: "x"}); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
	}
	persoID := openRecord(t, f, "s-perso", pigtail.WaitSourceDeclared).ID
	hidden, _ := answerAs(t, f, work, persoID, pigtail.WaitAnswerRequest{Seen: true})
	unknown, _ := answerAs(t, f, work, "wait_unknown", pigtail.WaitAnswerRequest{Seen: true})
	if hidden.Code != http.StatusNotFound || hidden.Code != unknown.Code || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("other scope = %d %s, unknown = %d %s", hidden.Code, hidden.Body.String(), unknown.Code, unknown.Body.String())
	}
	nothingWritten(t, f, persoID)

	postWait(t, f, "s-work", pigtail.SessionWaitRequest{Type: "blocked", Reason: "y"})
	workID := openRecord(t, f, "s-work", pigtail.WaitSourceDeclared).ID
	rec, _ := answerAs(t, f, perso, workID, pigtail.WaitAnswerRequest{Seen: true})
	if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrScopeMismatch) {
		t.Fatalf("master without cross = %d %s", rec.Code, rec.Body.String())
	}
	nothingWritten(t, f, workID)
	if rec, _ := answerAs(t, f, persoCross, workID, pigtail.WaitAnswerRequest{Seen: true}); rec.Code != http.StatusOK {
		t.Fatalf("master with cross = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := answerAs(t, f, perso, persoID, pigtail.WaitAnswerRequest{Seen: true}); rec.Code != http.StatusOK {
		t.Fatalf("master on its own scope = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnswerStandbyUnreadableAnswersAnyway(t *testing.T) {
	t.Parallel()
	f, _, _, id := answerFixture(t)
	f.rl.getStandby = func(string) (*pigtail.SessionStandby, error) { return nil, errors.New("disk gone") }
	rec, out := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Choice: "A"})
	if rec.Code != http.StatusOK || out.StandbyEnded || out.MessageID == "" {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(out.Delivery, "standby unreadable") || !strings.Contains(out.Delivery, "disk gone") {
		t.Fatalf("delivery = %q, want why the Standby stayed", out.Delivery)
	}
	if es := eventsOf(f, pigtail.EventSessionStandbyChanged); len(es) != 0 {
		t.Fatalf("session.standby_changed published: %+v", es)
	}
}

func TestWaitAnswerUsesHumanName(t *testing.T) {
	t.Parallel()
	f, _, _, id := answerFixture(t)
	f.rl.svc.HumanName = ""
	if rec, out := answerAs(t, f, none, id, pigtail.WaitAnswerRequest{Choice: "reco"}); rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	} else if m, err := f.st.GetMessage(out.MessageID); err != nil || m.FromLabel != "the user" || !strings.HasPrefix(m.Body, "Answer from the user to your wait") {
		t.Fatalf("default name: %+v %v", m, err)
	}
}
