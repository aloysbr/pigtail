package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waitdetail"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// D28, D29 (sub_x000000000000026, Task 17): the wait belongs to its subject
// in its scope. Its answer goes to the emitter while it lives, else to the
// subject's live session of the same scope, else it is kept and goes to the
// subject's next session — at its registration or at the relay's tick.

// wsSubj is the cmux workspace bound to the subject of subjectFixture.
const wsSubj = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"

// subjectFixture: the wake fixture with a bus and a perso subject bound to
// the cmux workspace wsSubj.
func subjectFixture(t *testing.T) (*relayFixture, *fakeWake, pigtail.Subject) {
	t.Helper()
	f, fw, _ := newWakeFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.seedScopedSubjectWorktree(t, perso, "Perso/Areas/webfront", "Web front", "/wt/webfront")
	if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: wsSubj, SubjectID: sub.ID, Source: "attach", BoundAt: f.now}); err != nil {
		t.Fatal(err)
	}
	return f, fw, sub
}

// registerOn registers session id of scope sc the way the hook does, on the
// cmux workspace ws ("" = none) — bound to the subject when ws is wsSubj.
func registerOn(t *testing.T, f *relayFixture, c caller, id, ws string) {
	t.Helper()
	sc := c.scope
	rec := doAs(t, f.r, c, "POST", "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{{
		SessionID: id, Name: id, Scope: sc, CmuxWorkspaceID: ws, Source: "hook",
	}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("register %s = %d %s", id, rec.Code, rec.Body.String())
	}
}

func endSession(t *testing.T, f *relayFixture, c caller, id string) {
	t.Helper()
	if rec := doAs(t, f.r, c, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: id}}}); rec.Code != http.StatusOK {
		t.Fatalf("end %s = %d %s", id, rec.Code, rec.Body.String())
	}
}

// declareArbitrage has session id declare an arbitrage and returns its wait.
func declareArbitrage(t *testing.T, f *relayFixture, c caller, id string) string {
	t.Helper()
	postWaitAs(t, f, c, id, pigtail.SessionWaitRequest{Type: "arbitrage", Reason: "Sidebar order", Details: json.RawMessage(arbitrageDetails)})
	r := openRecord(t, f, id, pigtail.WaitSourceDeclared)
	if r == nil {
		t.Fatalf("no open record for %s", id)
	}
	return r.ID
}

func answerReco(t *testing.T, f *relayFixture, id string) pigtail.WaitAnswerResponse {
	t.Helper()
	rec, out := answerAs(t, f, perso, id, pigtail.WaitAnswerRequest{Choice: "reco"})
	if rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	return out
}

func deliveredEvents(f *relayFixture) []pigtail.Event { return eventsOf(f, pigtail.EventWaitDelivered) }

func TestSubjectWaitSurvivesSessionEnd(t *testing.T) {
	t.Parallel()
	f, _, sub := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	if rec, _ := f.st.GetWaitRecord(id); rec == nil || rec.SubjectID != sub.ID {
		t.Fatalf("record = %+v, want subject %s", rec, sub.ID)
	}
	endSession(t, f, perso, "s-a")
	for _, e := range closedEvents(f) {
		if e.Data["wait_id"] == id {
			t.Fatalf("wait.closed for the subject's wait: %+v", e)
		}
	}
	rec := doAs(t, f.r, perso, "GET", "/waits", nil)
	var ws []pigtail.Wait
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].ID != id || !ws[0].SessionEnded || ws[0].State != pigtail.WaitStateOpen {
		t.Fatalf("GET /waits = %+v", ws)
	}
}

// The respawn / fresh-session-in-the-tab case: A ends, B takes the subject's tab.
func TestAnswerGoesToSubjectsLiveSession(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	endSession(t, f, perso, "s-a")
	registerOn(t, f, perso, "s-b", wsSubj)
	if err := f.st.SetStandby("s-b", f.now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	out := answerReco(t, f, id)
	if out.Delivery == pigtail.DeliveryKept || out.DeliveredTo != "s-b" || out.MessageID == "" || !out.StandbyEnded {
		t.Fatalf("response = %+v", out)
	}
	m, err := f.st.GetMessage(out.MessageID)
	if err != nil || m.ToSession != "s-b" || m.FromSession != pigtail.HumanSender {
		t.Fatalf("message = %+v, %v", m, err)
	}
	if w := getWait(t, f, perso, id); w.DeliveredTo != "s-b" || w.DeliveredAt == nil {
		t.Fatalf("wait = %+v", w)
	}
	evs := f.rl.svc.Bus.Replay("", []string{string(pigtail.EventWaitAnswered), string(pigtail.EventWaitDelivered)})
	if len(evs) != 2 || evs[0].Type != pigtail.EventWaitAnswered || evs[1].Type != pigtail.EventWaitDelivered ||
		evs[1].Data["session_id"] != "s-b" || evs[1].Data["wait_id"] != id || evs[0].Data["session_id"] != "s-a" {
		t.Fatalf("events = %+v", evs)
	}
	if evs[1].Scope != string(scope.Canonical("perso")) {
		t.Fatalf("wait.delivered scope = %q", evs[1].Scope)
	}
	if sb, _ := f.st.GetStandby("s-b"); sb != nil {
		t.Fatalf("standby of the recipient = %+v, want lifted (D1)", sb)
	}
}

// Review Focus 9: a crash without SessionEnd — its pid dead — is gone.
func TestAnswerSkipsDeadEmitter(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	registerOn(t, f, perso, "s-b", wsSubj)
	f.rl.cfg.Alive = func(p pigtail.PeerSession) bool { return p.EndedAt == nil && p.SessionID != "s-a" }
	out := answerReco(t, f, id)
	if out.DeliveredTo != "s-b" || out.Delivery == pigtail.DeliveryKept {
		t.Fatalf("response = %+v", out)
	}
	if !out.Wait.SessionEnded {
		t.Fatalf("wait = %+v, want session_ended", out.Wait)
	}
}

// C3: the recipient ends between its choice and the write — the answer is
// kept, then reaches the subject's next session once.
func TestAnswerRecipientEndedFallsBackToKept(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	endSession(t, f, perso, "s-a")
	alive := f.rl.cfg.Alive
	f.rl.cfg.Alive = func(p pigtail.PeerSession) bool { return p.SessionID == "s-a" || alive(p) }
	out := answerReco(t, f, id)
	f.rl.cfg.Alive = alive
	if out.Delivery != pigtail.DeliveryKept || out.MessageID != "" || out.DeliveredTo != "" {
		t.Fatalf("response = %+v", out)
	}
	if ms := humanMessages(t, f); len(ms) != 0 {
		t.Fatalf("messages = %+v", ms)
	}
	if es := deliveredEvents(f); len(es) != 0 {
		t.Fatalf("wait.delivered = %+v", es)
	}
	registerOn(t, f, perso, "s-b", wsSubj)
	f.rl.deliverKeptAnswers("", true) // the tick
	ms := humanMessages(t, f)
	if len(ms) != 1 || ms[0].ToSession != "s-b" {
		t.Fatalf("messages = %+v, want one to s-b", ms)
	}
}

func TestAnswerPrefersLiveEmitter(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	registerOn(t, f, perso, "s-b", wsSubj) // more recent
	if out := answerReco(t, f, id); out.DeliveredTo != "s-a" {
		t.Fatalf("response = %+v, want the live emitter", out)
	}
}

// D29-C: nobody runs on the subject — the answer is kept, then delivered at
// the next session's registration, without any cmux call in that request.
func TestAnswerKeptWithoutLiveSession(t *testing.T) {
	t.Parallel()
	f, fw, sub := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	endSession(t, f, perso, "s-a")
	out := answerReco(t, f, id)
	if out.Delivery != pigtail.DeliveryKept || out.MessageID != "" {
		t.Fatalf("response = %+v", out)
	}
	if ms := humanMessages(t, f); len(ms) != 0 {
		t.Fatalf("messages = %+v", ms)
	}
	if n := waitLogLines(t, f, sub.ID); n != 1 {
		t.Fatalf("subject log wait lines = %d, want 1", n)
	}
	if w := getWait(t, f, perso, id); w.State != pigtail.WaitStateAnswered || w.DeliveredTo != "" {
		t.Fatalf("wait = %+v", w)
	}
	fw.resetCounts()
	registerOn(t, f, perso, "s-b", wsSubj)
	if fw.cmuxCalls != 0 {
		t.Fatalf("cmux calls during the registration = %d, want 0 (C4)", fw.cmuxCalls)
	}
	ms := humanMessages(t, f)
	if len(ms) != 1 || ms[0].ToSession != "s-b" {
		t.Fatalf("messages = %+v", ms)
	}
	rec, err := f.st.GetWaitRecord(id)
	if err != nil {
		t.Fatal(err)
	}
	if want := waitdetail.RenderAnswer(rec.Wait, *rec.Answer, f.rl.svc.humanName()); ms[0].Body != want {
		t.Fatalf("body = %q, want the rendering of the stored value %q", ms[0].Body, want)
	}
	if rec.DeliveredTo != "s-b" {
		t.Fatalf("delivered_to = %q", rec.DeliveredTo)
	}
	if es := deliveredEvents(f); len(es) != 1 || es[0].Data["session_id"] != "s-b" {
		t.Fatalf("wait.delivered = %+v", es)
	}
	if n := waitLogLines(t, f, sub.ID); n != 1 {
		t.Fatalf("subject log wait lines = %d, want still 1", n)
	}
}

// A binding set after the SessionStart: the relay's tick delivers it, once.
func TestKeptAnswerDeliveredBySweep(t *testing.T) {
	t.Parallel()
	f, _, sub := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	endSession(t, f, perso, "s-a")
	answerReco(t, f, id)
	const wsB = "BBBBBBBB-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	registerOn(t, f, perso, "s-b", wsB)
	if ms := humanMessages(t, f); len(ms) != 0 {
		t.Fatalf("delivered before the binding: %+v", ms)
	}
	if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: wsB, SubjectID: sub.ID, Source: "attach", BoundAt: f.now}); err != nil {
		t.Fatal(err)
	}
	f.rl.cfg.SweepInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.rl.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	registerOn(t, f, perso, "s-b", wsB) // concurrent with the tick
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ms := humanMessages(t, f); len(ms) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // a few more ticks: never a second message
	ms := humanMessages(t, f)
	if len(ms) != 1 || ms[0].ToSession != "s-b" {
		t.Fatalf("messages = %+v, want one to s-b", ms)
	}
}

// Review Focus 12: a work session in the perso subject's tab gets nothing.
func TestKeptAnswerNeverCrossesScope(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	endSession(t, f, perso, "s-a")
	if out := answerReco(t, f, id); out.Delivery != pigtail.DeliveryKept {
		t.Fatalf("response = %+v", out)
	}
	registerOn(t, f, work, "s-t", wsSubj)
	f.rl.deliverKeptAnswers("", true)
	if ms := humanMessages(t, f); len(ms) != 0 {
		t.Fatalf("messages = %+v", ms)
	}
	kept, err := f.st.KeptAnswers()
	if err != nil || len(kept) != 1 || kept[0].ID != id {
		t.Fatalf("kept = %+v, %v", kept, err)
	}
}

// C2: a wait without a subject answers its emitter, as before.
func TestNoSubjectWaitAnswersEmitterOnly(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-n", "")
	id := declareArbitrage(t, f, perso, "s-n")
	if rec, _ := f.st.GetWaitRecord(id); rec == nil || rec.SubjectID != "" {
		t.Fatalf("record = %+v, want no subject", rec)
	}
	f.rl.cfg.Alive = func(p pigtail.PeerSession) bool { return p.EndedAt == nil && p.SessionID != "s-n" }
	out := answerReco(t, f, id)
	if out.Delivery == pigtail.DeliveryKept || out.DeliveredTo != "s-n" || out.MessageID == "" {
		t.Fatalf("crashed emitter: response = %+v", out)
	}

	registerOn(t, f, perso, "s-m", "")
	id2 := declareArbitrage(t, f, perso, "s-m")
	endSession(t, f, perso, "s-m")
	rec, _ := answerAs(t, f, perso, id2, pigtail.WaitAnswerRequest{Choice: "reco"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("ended emitter: answer = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if kept, _ := f.st.KeptAnswers(); len(kept) != 0 {
		t.Fatalf("kept = %+v", kept)
	}
	if ms := humanMessages(t, f); len(ms) != 1 {
		t.Fatalf("messages = %+v, want the first answer only", ms)
	}
}

// D29-B: B's declaration on the subject replaces A's, told under A.
func TestDeclareOnSubjectReplacesOtherSessionsWait(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	registerOn(t, f, perso, "s-b", wsSubj)
	a := declareArbitrage(t, f, perso, "s-a")
	declareArbitrage(t, f, perso, "s-b")
	var got []pigtail.Event
	for _, e := range closedEvents(f) {
		if e.Data["wait_id"] == a {
			got = append(got, e)
		}
	}
	if len(got) != 1 || got[0].Data["state"] != pigtail.WaitStateReplaced || got[0].Data["session_id"] != "s-a" ||
		got[0].Scope != string(scope.Canonical("perso")) {
		t.Fatalf("wait.closed of A's wait = %+v", got)
	}
}

// Review Focus 10: B inherits A's arbitrage; B's turn end infers working —
// the arbitrage stays open.
func TestWorkingAfterInheritedWaitKeepsIt(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	endSession(t, f, perso, "s-a")
	registerOn(t, f, perso, "s-b", wsSubj)
	if rec := doAs(t, f.r, perso, "POST", "/peers/s-b/turn", pigtail.SessionTurnRequest{TurnID: "t1", TurnStart: f.now.Add(-time.Minute), Event: "stop", Working: true, Reason: "CI"}); rec.Code != http.StatusNoContent {
		t.Fatalf("turn = %d %s", rec.Code, rec.Body.String())
	}
	if w := sessionWaitOfPeer(t, f, "s-b"); w == nil || w.Type != "working" {
		t.Fatalf("B's wait = %+v, want working inferred", w)
	}
	if w := getWait(t, f, perso, id); w.State != pigtail.WaitStateOpen {
		t.Fatalf("inherited arbitrage = %+v", w)
	}
}

func sessionWaitOfPeer(t *testing.T, f *relayFixture, id string) *pigtail.SessionWait {
	t.Helper()
	ws, err := f.st.SessionWaits(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ws {
		if ws[i].Source == pigtail.WaitSourceDeclared {
			return &ws[i]
		}
	}
	return nil
}

// D28 (b): the stored value is the truth, the body its rendering.
func TestAnswerStoredValueIsTheSource(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	out := answerReco(t, f, id)
	w := getWait(t, f, perso, id)
	if w.Answer == nil || w.Answer.Kind != "arbitrage" || w.Answer.Schema != pigtail.WaitAnswerSchema || w.Answer.Choice == "" {
		t.Fatalf("answer = %+v", w.Answer)
	}
	m, err := f.st.GetMessage(out.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := f.st.GetWaitRecord(id)
	if err != nil {
		t.Fatal(err)
	}
	if want := waitdetail.RenderAnswer(rec.Wait, *rec.Answer, f.rl.svc.humanName()); m.Body != want {
		t.Fatalf("body = %q, want %q", m.Body, want)
	}
}

// Review of PR #229: the recipient ends between the write and the delivery —
// EndPeerClosing keeps the answer again. The response, its wait and the
// trace all say kept; none names the ended session.
func TestAnswerRecipientEndedAfterWriteSaysKept(t *testing.T) {
	t.Parallel()
	f, _, sub := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	f.rl.afterAnswerWrite = func(string) { endSession(t, f, perso, "s-a") }
	out := answerReco(t, f, id)
	f.rl.afterAnswerWrite = nil
	if out.Delivery != pigtail.DeliveryKept || out.MessageID != "" || out.DeliveredTo != "" {
		t.Fatalf("response = %+v", out)
	}
	if out.Wait.DeliveredTo != "" || out.Wait.DeliveredAt != nil {
		t.Fatalf("response wait = %+v, want no delivered_to (kept)", out.Wait)
	}
	logs, err := f.st.ListLog(sub.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range logs {
		if l.Kind == "wait" {
			line = l.Text
		}
	}
	if !strings.Contains(line, "kept for the subject's next session") || strings.Contains(line, "s-a") {
		t.Fatalf("subject log = %q, want kept, never the ended session", line)
	}
	registerOn(t, f, perso, "s-b", wsSubj)
	ms := humanMessages(t, f)
	var toB int
	for _, m := range ms {
		if m.ToSession == "s-b" {
			toB++
		}
	}
	if toB != 1 {
		t.Fatalf("messages = %+v, want one to the subject's next session", ms)
	}
}

// Review of PR #229 (D29-C): the recipient is handed the answer, then
// crashes without SessionEnd. The sidebar loop's gone clear keeps the unread
// answer again; the subject's next session receives it, once.
func TestGoneRecipientUnreadAnswerKeptAgain(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	out := answerReco(t, f, id)
	if out.DeliveredTo != "s-a" || out.MessageID == "" {
		t.Fatalf("response = %+v", out)
	}
	alive := f.rl.cfg.Alive
	f.rl.cfg.Alive = func(p pigtail.PeerSession) bool { return p.SessionID != "s-a" && alive(p) }
	// What cmuxsync_wiring's ClearWaits does for a session judged gone.
	if err := f.rl.ClearWaitsKeeping("s-a", true, pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared); err != nil {
		t.Fatal(err)
	}
	if m, err := f.st.GetMessage(out.MessageID); err != nil || m.State != pigtail.MessageStateExpired {
		t.Fatalf("message to the gone session = %+v, %v, want expired", m, err)
	}
	if w := getWait(t, f, perso, id); w.DeliveredTo != "" {
		t.Fatalf("wait = %+v, want kept again", w)
	}
	registerOn(t, f, perso, "s-b", wsSubj)
	f.rl.deliverKeptAnswers("", true) // a tick after: never a second one
	var toB int
	for _, m := range humanMessages(t, f) {
		if m.ToSession == "s-b" {
			toB++
		}
	}
	if toB != 1 {
		t.Fatalf("messages to s-b = %d, want 1", toB)
	}
}

// The plan's log line at the delivery of a kept answer: how long it was kept.
func TestKeptAnswerDeliveryLogsKeptDuration(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	var mu sync.Mutex
	var logged []string
	f.rl.svc.Logf = func(format string, args ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	endSession(t, f, perso, "s-a")
	answerReco(t, f, id)
	f.advance(90 * time.Minute)
	registerOn(t, f, perso, "s-b", wsSubj)
	mu.Lock()
	defer mu.Unlock()
	want := "relay: answer " + id + " delivered to s-b (kept 1h30m0s)"
	for _, l := range logged {
		if l == want {
			return
		}
	}
	t.Fatalf("logs = %q, want %q", logged, want)
}

// The done command in A — its prompt's clear keeps the subject's wait, the
// wrapped it declares replaces nothing of it, A ends, B takes the tab: the
// wait is still open and answerable, and its answer reaches B.
func TestDoneCommandKeepsSubjectWaitForNextSession(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	if rec := doAs(t, f.r, perso, "DELETE", "/peers/s-a/wait?keep=subject", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE keep=subject = %d %s", rec.Code, rec.Body.String())
	}
	if w := sessionWaitOfPeer(t, f, "s-a"); w != nil {
		t.Fatalf("A's declared row after the done command = %+v, want gone", w)
	}
	postWaitAs(t, f, perso, "s-a", pigtail.SessionWaitRequest{Type: "wrapped"})
	if w := sessionWaitOfPeer(t, f, "s-a"); w == nil || w.Type != "wrapped" {
		t.Fatalf("A's pill = %+v, want wrapped", w)
	}
	endSession(t, f, perso, "s-a")
	for _, e := range closedEvents(f) {
		if e.Data["wait_id"] == id {
			t.Fatalf("wait.closed for the subject's wait: %+v", e)
		}
	}
	if w := getWait(t, f, perso, id); w.State != pigtail.WaitStateOpen || !w.SessionEnded {
		t.Fatalf("wait after the done command = %+v, want open, session ended", w)
	}
	registerOn(t, f, perso, "s-b", wsSubj)
	if out := answerReco(t, f, id); out.DeliveredTo != "s-b" || out.MessageID == "" {
		t.Fatalf("response = %+v, want the answer delivered to the next session", out)
	}
}

// D30: keep=subject is the turn's clear only.
func TestClearKeepSubjectValidated(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	for _, q := range []string{"keep=bogus", "keep=subject&source=held", "keep=subject&source=all", "keep="} {
		if rec := doAs(t, f.r, perso, "DELETE", "/peers/s-a/wait?"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE ?%s = %d %s, want 400", q, rec.Code, rec.Body.String())
		}
	}
	id := declareArbitrage(t, f, perso, "s-a")
	if rec := doAs(t, f.r, perso, "DELETE", "/peers/s-a/wait", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if w := getWait(t, f, perso, id); w.State != pigtail.WaitStateCleared {
		t.Fatalf("plain clear of the emitter = %+v, want cleared as before", w)
	}
}

// D29-C, the crash named in review: the recipient is handed the answer (its
// declared row went with it, so the sidebar loop never judges it), then dies
// without SessionEnd. The relay's tick — no cmux call — sees it gone on two
// sightings goneRecipientConfirm apart, keeps the unread answer again, and
// the same tick hands it to the subject's next session, once.
func TestTickRekeepsAnswerOfCrashedRecipient(t *testing.T) {
	t.Parallel()
	f, fw, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	out := answerReco(t, f, id)
	if out.DeliveredTo != "s-a" || out.MessageID == "" {
		t.Fatalf("response = %+v", out)
	}
	if ws, _ := f.st.SessionWaits("s-a"); len(ws) != 0 {
		t.Fatalf("A's rows = %+v, want none (the loop never judges it)", ws)
	}
	registerOn(t, f, perso, "s-b", wsSubj)
	alive := f.rl.cfg.Alive
	f.rl.cfg.Alive = func(p pigtail.PeerSession) bool { return p.SessionID != "s-a" && alive(p) }
	fw.resetCounts()
	f.rl.tick() // first sighting: not yet
	if m, err := f.st.GetMessage(out.MessageID); err != nil || m.State != pigtail.MessageStatePending {
		t.Fatalf("message after one sighting = %+v, %v, want still pending", m, err)
	}
	f.advance(goneRecipientConfirm)
	f.rl.tick()
	if m, err := f.st.GetMessage(out.MessageID); err != nil || m.State != pigtail.MessageStateExpired {
		t.Fatalf("message to the crashed session = %+v, %v, want expired", m, err)
	}
	if w := getWait(t, f, perso, id); w.DeliveredTo != "s-b" {
		t.Fatalf("wait = %+v, want delivered to s-b by the same tick", w)
	}
	f.advance(goneRecipientConfirm)
	f.rl.tick() // never a second one
	var toB int
	for _, m := range humanMessages(t, f) {
		if m.ToSession == "s-b" {
			toB++
		}
	}
	if toB != 1 {
		t.Fatalf("messages to s-b = %d, want 1", toB)
	}
}

// A recipient that reads gone once, then alive again, keeps its answer; a
// wait without a subject is never kept again (D24).
func TestTickKeepsAnswerOfLiveRecipient(t *testing.T) {
	t.Parallel()
	f, _, _ := subjectFixture(t)
	registerOn(t, f, perso, "s-a", wsSubj)
	id := declareArbitrage(t, f, perso, "s-a")
	out := answerReco(t, f, id)
	registerOn(t, f, perso, "s-n", "")
	nid := declareArbitrage(t, f, perso, "s-n")
	nout := answerReco(t, f, nid)
	alive := f.rl.cfg.Alive
	dead := true
	f.rl.cfg.Alive = func(p pigtail.PeerSession) bool { return (!dead || p.SessionID != "s-a") && alive(p) }
	f.rl.tick()
	dead = false
	f.advance(goneRecipientConfirm)
	f.rl.tick()
	dead = true
	f.advance(goneRecipientConfirm)
	f.rl.tick() // a fresh first sighting
	for _, mid := range []string{out.MessageID, nout.MessageID} {
		if m, err := f.st.GetMessage(mid); err != nil || m.State != pigtail.MessageStatePending {
			t.Fatalf("message %s = %+v, %v, want pending", mid, m, err)
		}
	}
	f.rl.cfg.Alive = func(p pigtail.PeerSession) bool { return p.SessionID != "s-n" && alive(p) }
	f.advance(goneRecipientConfirm)
	f.rl.tick()
	f.advance(goneRecipientConfirm)
	f.rl.tick()
	if m, err := f.st.GetMessage(nout.MessageID); err != nil || m.State != pigtail.MessageStatePending {
		t.Fatalf("no-subject answer = %+v, %v, want pending (never kept again)", m, err)
	}
}
