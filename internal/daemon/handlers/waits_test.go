package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// GET /waits and GET /waits/{id} (sub_x000000000000026, Task 5): the open
// waits the caller may read, with their session, subject and content; one
// wait, open or closed, with its answer. A wait of another scope reads as an
// unknown id.

func listWaits(t *testing.T, f *relayFixture, c caller, query string) []pigtail.Wait {
	t.Helper()
	rec := doAs(t, f.r, c, "GET", "/waits"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /waits%s = %d %s", query, rec.Code, rec.Body.String())
	}
	var out []pigtail.Wait
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

func getWait(t *testing.T, f *relayFixture, c caller, id string) pigtail.Wait {
	t.Helper()
	rec := doAs(t, f.r, c, "GET", "/waits/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /waits/%s = %d %s", id, rec.Code, rec.Body.String())
	}
	var out pigtail.Wait
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

func TestListWaitsShowsSubjectAndDetails(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	sub := f.seedSubjectWorktree(t, "Web front", "/wt/webfront")
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Cwd: "/wt/webfront/cmd", Name: "alpha", Scope: "gx"})
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "arbitrage", Reason: "Sidebar order", Details: json.RawMessage(arbitrageDetails)})

	ws := listWaits(t, f, none, "")
	if len(ws) != 1 {
		t.Fatalf("waits = %+v", ws)
	}
	w := ws[0]
	if !strings.HasPrefix(w.ID, pigtail.PrefixWait) || w.SessionID != "s1" || w.SessionLabel == "" || w.SubjectID != sub.ID ||
		w.SubjectShortTitle != pigtail.ShortTitle(sub) || w.Type != "arbitrage" || w.Reason != "Sidebar order" ||
		w.Source != pigtail.WaitSourceDeclared || w.State != pigtail.WaitStateOpen || !w.OpenedAt.Equal(f.clock()) {
		t.Fatalf("wait = %+v", w)
	}
	if w.Details == nil || len(w.Details.Options) != 2 || w.Details.Options[0].ID != "A" || !w.Details.Options[0].Reco || w.Details.Why != "cheaper" {
		t.Fatalf("details = %+v", w.Details)
	}
}

func TestListWaitsHidesSnoozedUnlessAll(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})
	id := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	if _, err := f.st.SnoozeWait(id, f.clock().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ws := listWaits(t, f, none, ""); len(ws) != 0 {
		t.Fatalf("a snoozed wait is listed: %+v", ws)
	}
	ws := listWaits(t, f, none, "?all=1")
	if len(ws) != 1 || ws[0].ID != id || ws[0].SnoozedUntil == nil {
		t.Fatalf("?all=1 = %+v", ws)
	}
}

func TestGetWaitClosedKeepsHistory(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "arbitrage", Details: json.RawMessage(arbitrageDetails)})
	first := openRecord(t, f, "s1", pigtail.WaitSourceDeclared).ID
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})
	w := getWait(t, f, none, first)
	if w.ID != first || w.State != pigtail.WaitStateReplaced || w.ClosedAt == nil || w.Details == nil || len(w.Details.Options) != 2 {
		t.Fatalf("wait = %+v", w)
	}
}

func hookPermission() pigtail.SessionWaitRequest {
	return pigtail.SessionWaitRequest{Source: pigtail.WaitSourceAuto, Type: "permission", Reason: "Bash",
		Details: json.RawMessage(`{"cmd":"make","where":"/w"}`)}
}

func TestListWaitsShowsAutoPermission(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", hookPermission())
	ws := listWaits(t, f, none, "")
	if len(ws) != 1 || ws[0].Source != pigtail.WaitSourceAuto || ws[0].Type != "permission" || ws[0].Details == nil ||
		ws[0].Details.Cmd != "make" || ws[0].Details.Where != "/w" {
		t.Fatalf("waits = %+v", ws)
	}
}

func TestAutoWaitClearClosesRecord(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", hookPermission())
	id := openRecord(t, f, "s1", pigtail.WaitSourceAuto).ID
	if rec := doJSON(t, f.r, "DELETE", "/peers/s1/wait?source=auto", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if ws := listWaits(t, f, none, ""); len(ws) != 0 {
		t.Fatalf("waits after DELETE = %+v", ws)
	}
	if w := getWait(t, f, none, id); w.State != pigtail.WaitStateCleared {
		t.Fatalf("wait = %+v", w)
	}

	// The sidebar loop's path.
	postWait(t, f, "s1", hookPermission())
	id = openRecord(t, f, "s1", pigtail.WaitSourceAuto).ID
	if err := f.rl.ClearWaits("s1", pigtail.WaitSourceAuto); err != nil {
		t.Fatal(err)
	}
	if ws := listWaits(t, f, none, ""); len(ws) != 0 {
		t.Fatalf("waits after ClearWaits = %+v", ws)
	}
	if w := getWait(t, f, none, id); w.State != pigtail.WaitStateCleared {
		t.Fatalf("wait = %+v", w)
	}
}

func TestWaitOfOtherScopeReadsAsUnknown(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-perso", Name: "pa", Scope: "perso"})
	if rec := doAs(t, f.r, perso, "POST", "/peers/s-perso/wait", pigtail.SessionWaitRequest{Type: "review", Reason: "x"}); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
	}
	id := openRecord(t, f, "s-perso", pigtail.WaitSourceDeclared).ID
	if w := getWait(t, f, perso, id); w.ID != id {
		t.Fatalf("positive control: perso reads its wait: %+v", w)
	}
	hidden := doAs(t, f.r, work, "GET", "/waits/"+id, nil)
	unknown := doAs(t, f.r, work, "GET", "/waits/wait_unknown", nil)
	if hidden.Code != http.StatusNotFound || hidden.Code != unknown.Code || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("other scope = %d %s, unknown = %d %s", hidden.Code, hidden.Body.String(), unknown.Code, unknown.Body.String())
	}
	if ws := listWaits(t, f, work, "?all=1"); len(ws) != 0 {
		t.Fatalf("work lists a perso wait: %+v", ws)
	}
	if ws := listWaits(t, f, perso, ""); len(ws) != 1 {
		t.Fatalf("perso lists %+v", ws)
	}
}

// D23: a work session whose cmux workspace is bound to a perso subject shows
// that subject's title as its workspace title. Its wait is work's, but a work
// caller never reads the perso subject — neither its id nor its title, nor
// the workspace title that carries it; the master reads the workspace title.
func TestListWaitsRedactsTitlesOfOtherScope(t *testing.T) {
	t.Parallel()
	const ws, title = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994", "Zqx perso title"
	f := newRelayFixture(t)
	rec := doAs(t, f.r, perso, "POST", "/themes", pigtail.Theme{Name: "Zqx", Kind: "area", VaultLink: "Perso/Areas/zqx"})
	var th pigtail.Theme
	_ = json.Unmarshal(rec.Body.Bytes(), &th)
	rec = doAs(t, f.r, perso, "POST", "/subjects", pigtail.Subject{ThemeID: th.ID, Title: title})
	var sub pigtail.Subject
	if err := json.Unmarshal(rec.Body.Bytes(), &sub); err != nil || sub.ID == "" {
		t.Fatalf("seed subject = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(t, f.r, perso, "POST", "/subjects/"+sub.ID+"/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: ws}); rec.Code != http.StatusCreated {
		t.Fatalf("bind = %d %s", rec.Code, rec.Body.String())
	}
	f.peers(t, pigtail.PeerSession{SessionID: "s-work-bound", Name: "tb", Scope: "gx", CellID: "mt", CmuxWorkspaceID: ws, CmuxTitle: title})
	postWait(t, f, "s-work-bound", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})

	master := listWaits(t, f, perso, "")
	// The record froze no subject (the session's scope cannot read it); the
	// master still reads the title through the workspace label.
	if len(master) != 1 || master[0].SubjectID != "" || !strings.Contains(master[0].SessionLabel, title) {
		t.Fatalf("positive control, the master's view = %+v", master)
	}
	got := listWaits(t, f, work, "")
	if len(got) != 1 {
		t.Fatalf("work view = %+v", got)
	}
	one := getWait(t, f, work, got[0].ID)
	for _, w := range []pigtail.Wait{got[0], one} {
		body := jsonString(w)
		if w.SubjectID != "" || w.SubjectShortTitle != "" || strings.Contains(body, title) || strings.Contains(body, sub.ID) {
			t.Fatalf("work reads the perso subject: %s", body)
		}
		if !strings.Contains(w.SessionLabel, "tb") {
			t.Fatalf("work label = %q, want the session's own name", w.SessionLabel)
		}
	}
}

// GET /waits?subject=&session=&closed= (sub_x000000000000025, Task 5): the
// open waits of one subject or session, then up to `closed` closed ones,
// the most recent first, with their answer.

func TestListWaitsFiltersBySubject(t *testing.T) {
	f, _, sub, id := answerFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-free", Name: "free", Scope: "gx"})
	postWait(t, f, "s-free", pigtail.SessionWaitRequest{Type: "review", Reason: "elsewhere"})
	if all := listWaits(t, f, none, ""); len(all) != 2 {
		t.Fatalf("unfiltered = %+v; want 2", all)
	}
	ws := listWaits(t, f, none, "?subject="+sub.ID)
	if len(ws) != 1 || ws[0].ID != id || ws[0].SubjectID != sub.ID {
		t.Fatalf("?subject = %+v; want only %s", ws, id)
	}
	if ws := listWaits(t, f, none, "?subject=sub_nope"); len(ws) != 0 {
		t.Fatalf("?subject=unknown = %+v; want none", ws)
	}
}

func TestListWaitsFiltersBySession(t *testing.T) {
	f := newWaitFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s2", Name: "beta", Scope: "gx"})
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "one"})
	postWait(t, f, "s2", pigtail.SessionWaitRequest{Type: "review", Reason: "two"})
	ws := listWaits(t, f, none, "?session=s2")
	if len(ws) != 1 || ws[0].SessionID != "s2" || ws[0].Reason != "two" {
		t.Fatalf("?session=s2 = %+v", ws)
	}
}

func TestListWaitsClosedHistoryWithAnswers(t *testing.T) {
	f, _, sub, first := answerFixture(t)
	if rec, _ := answerAs(t, f, none, first, pigtail.WaitAnswerRequest{Choice: "A"}); rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	postWait(t, f, "s-alpha", pigtail.SessionWaitRequest{Type: "review", Reason: "second"})
	second := openRecord(t, f, "s-alpha", pigtail.WaitSourceDeclared).ID
	if rec, _ := answerAs(t, f, none, second, pigtail.WaitAnswerRequest{Seen: true}); rec.Code != http.StatusOK {
		t.Fatalf("answer 2 = %d %s", rec.Code, rec.Body.String())
	}
	postWait(t, f, "s-alpha", pigtail.SessionWaitRequest{Type: "review", Reason: "third, open"})
	open := openRecord(t, f, "s-alpha", pigtail.WaitSourceDeclared).ID

	if ws := listWaits(t, f, none, "?subject="+sub.ID); len(ws) != 1 || ws[0].ID != open {
		t.Fatalf("no closed= : %+v; want the open one only", ws)
	}
	ws := listWaits(t, f, none, "?subject="+sub.ID+"&closed=5")
	if len(ws) != 3 || ws[0].ID != open || ws[1].ID != second || ws[2].ID != first {
		ids := []string{}
		for _, w := range ws {
			ids = append(ids, w.ID+":"+w.State)
		}
		t.Fatalf("closed=5 = %v; want open, then %s, %s (most recent first)", ids, second, first)
	}
	if ws[2].State != pigtail.WaitStateAnswered || ws[2].Answer == nil || ws[2].Answer.Choice != "A" {
		t.Fatalf("closed wait = %+v; want its answer", ws[2])
	}
	if ws := listWaits(t, f, none, "?subject="+sub.ID+"&closed=1"); len(ws) != 2 || ws[1].ID != second {
		t.Fatalf("closed=1 = %+v; want open + the latest closed", ws)
	}
}

func TestListWaitsClosedBounds(t *testing.T) {
	f := newWaitFixture(t)
	for _, q := range []string{"?closed=-1", "?closed=6", "?closed=x", "?closed=1.5"} {
		rec := doAs(t, f.r, none, "GET", "/waits"+q, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "closed must be 0..5") {
			t.Errorf("GET /waits%s = %d %s; want 400 closed must be 0..5", q, rec.Code, rec.Body.String())
		}
	}
	for _, q := range []string{"?closed=0", "?closed=5"} {
		if rec := doAs(t, f.r, none, "GET", "/waits"+q, nil); rec.Code != http.StatusOK {
			t.Errorf("GET /waits%s = %d", q, rec.Code)
		}
	}
}
