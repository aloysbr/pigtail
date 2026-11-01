package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// POST /waits/{id}/snooze (sub_x000000000000026, Task 11): the user hides an open
// wait until a time. The return is computed at read (cfg.Now), no timer; the
// session is not touched.

func snoozeAs(t *testing.T, f *relayFixture, c caller, id string, until time.Time) *httptest.ResponseRecorder {
	t.Helper()
	return doAs(t, f.r, c, "POST", "/waits/"+id+"/snooze", pigtail.WaitSnoozeRequest{Until: until})
}

func TestSnoozeHidesThenReturns(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	until := f.clock().Add(time.Hour)

	rec := snoozeAs(t, f, none, id, until)
	if rec.Code != http.StatusOK {
		t.Fatalf("snooze = %d %s", rec.Code, rec.Body.String())
	}
	if ws := listWaits(t, f, none, ""); len(ws) != 0 {
		t.Fatalf("snoozed wait still listed: %+v", ws)
	}
	all := listWaits(t, f, none, "?all=1")
	if len(all) != 1 || all[0].SnoozedUntil == nil || !all[0].SnoozedUntil.Equal(until) || all[0].State != pigtail.WaitStateOpen {
		t.Fatalf("all=1 = %+v", all)
	}
	es := eventsOf(f, pigtail.EventWaitSnoozed)
	if len(es) != 1 || es[0].Data["wait_id"] != id || es[0].Data["state"] != pigtail.WaitStateOpen {
		t.Fatalf("wait.snoozed = %+v", es)
	}
	// Snoozing again replaces the time; a closed wait answers 409; a past time 400.
	if rec := snoozeAs(t, f, none, id, f.clock().Add(2*time.Hour)); rec.Code != http.StatusOK {
		t.Fatalf("resnooze = %d %s", rec.Code, rec.Body.String())
	}
	if rec := snoozeAs(t, f, none, id, f.clock().Add(-time.Minute)); rec.Code != http.StatusBadRequest {
		t.Fatalf("past until = %d %s", rec.Code, rec.Body.String())
	}
	if rec := snoozeAs(t, f, none, id, time.Time{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("zero until = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.st.ClearSessionWaits("s1"); err != nil {
		t.Fatal(err)
	}
	if rec := snoozeAs(t, f, none, id, f.clock().Add(time.Hour)); rec.Code != http.StatusConflict {
		t.Fatalf("closed = %d %s", rec.Code, rec.Body.String())
	}
}

// Review Focus 6: the clock advances with no timer running; the wait is back.
func TestSnoozedWaitReturnsAfterUntil(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	if rec := snoozeAs(t, f, none, id, f.clock().Add(time.Hour)); rec.Code != http.StatusOK {
		t.Fatalf("snooze = %d", rec.Code)
	}
	f.advance(59 * time.Minute)
	if ws := listWaits(t, f, none, ""); len(ws) != 0 {
		t.Fatalf("before until: %+v", ws)
	}
	f.advance(2 * time.Minute)
	if ws := listWaits(t, f, none, ""); len(ws) != 1 || ws[0].ID != id {
		t.Fatalf("after until: %+v", ws)
	}
}

func TestSnoozeLeavesSessionUntouched(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	before, _ := f.st.SessionWaits("s1")
	if rec := snoozeAs(t, f, none, id, f.clock().Add(time.Hour)); rec.Code != http.StatusOK {
		t.Fatalf("snooze = %d", rec.Code)
	}
	after, _ := f.st.SessionWaits("s1")
	if len(before) != 1 || len(after) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("SessionWaits before %+v after %+v", before, after)
	}
	ms, err := f.st.ListMessages(pigtail.MessageFilter{})
	if err != nil || len(ms) != 0 {
		t.Fatalf("messages = %+v %v", ms, err)
	}
	if st, _ := f.st.GetStandby("s1"); st != nil {
		t.Fatalf("standby set: %+v", st)
	}
	if es := eventsOf(f, pigtail.EventWaitAnswered); len(es) != 0 {
		t.Fatalf("wait.answered: %+v", es)
	}
}

func TestSnoozeOtherScopeReadsAsUnknown(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.rl.svc.Bus = nil
	f.peers(t, pigtail.PeerSession{SessionID: "s-perso", Name: "pa", Scope: "perso"}, pigtail.PeerSession{SessionID: "s-work", Name: "ta", Scope: "gx"})
	if rec := doAs(t, f.r, perso, "POST", "/peers/s-perso/wait", pigtail.SessionWaitRequest{Type: "blocked", Reason: "x"}); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
	}
	persoID := openRecord(t, f, "s-perso", pigtail.WaitSourceDeclared).ID
	until := f.clock().Add(time.Hour)
	hidden := snoozeAs(t, f, work, persoID, until)
	unknown := snoozeAs(t, f, work, "wait_unknown", until)
	if hidden.Code != http.StatusNotFound || hidden.Code != unknown.Code || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("other scope = %d %s, unknown = %d %s", hidden.Code, hidden.Body.String(), unknown.Code, unknown.Body.String())
	}
	if r := openRecord(t, f, "s-perso", pigtail.WaitSourceDeclared); r.SnoozedUntil != nil {
		t.Fatalf("snoozed anyway: %+v", r)
	}

	postWait(t, f, "s-work", pigtail.SessionWaitRequest{Type: "blocked", Reason: "y"})
	workID := openRecord(t, f, "s-work", pigtail.WaitSourceDeclared).ID
	rec := snoozeAs(t, f, perso, workID, until)
	if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrScopeMismatch) {
		t.Fatalf("master without cross = %d %s", rec.Code, rec.Body.String())
	}
	if rec := snoozeAs(t, f, persoCross, workID, until); rec.Code != http.StatusOK {
		t.Fatalf("master with cross = %d %s", rec.Code, rec.Body.String())
	}
}

// D3: snoozing, like answering, is the user's from his terminal; a request that
// carries an agent session id is refused before anything else and writes
// nothing.
func TestSnoozeFromAgentSessionRefused(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	body := fmt.Sprintf(`{"until":%q}`, f.clock().Add(time.Hour).Format(time.RFC3339))
	req := httptest.NewRequest("POST", "/waits/"+id+"/snooze", strings.NewReader(body))
	req.Header.Set(pigtail.HeaderSession, "s-beta")
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrUnauthorized) || !strings.Contains(rec.Body.String(), "snooze from your terminal, not from an agent session") {
		t.Fatalf("snooze from a session = %d %s", rec.Code, rec.Body.String())
	}
	if ws := listWaits(t, f, none, ""); len(ws) != 1 || ws[0].ID != id || ws[0].SnoozedUntil != nil {
		t.Fatalf("wait changed: %+v", ws)
	}
	if es := eventsOf(f, pigtail.EventWaitSnoozed); len(es) != 0 {
		t.Fatalf("wait.snoozed published: %+v", es)
	}
}
