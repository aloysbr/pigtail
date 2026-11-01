package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// journalAll is the zero filter: every journal row.
func journalAll() store.JournalFilter { return store.JournalFilter{} }

func TestOffTopicDeclareLift(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	body := pigtail.DeclareOffTopicRequest{SessionID: "s1", Reason: `question "rapide" — é`, Space: "Inbox"}
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/off-topic", body); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/off-topic", pigtail.DeclareOffTopicRequest{Reason: "autre"}); rec.Code != 200 {
		t.Fatalf("again = %d", rec.Code)
	}
	if o, _ := st.GetOffTopic(cmuxWS); o == nil || o.Reason != `question "rapide" — é` || o.SessionID != "s1" || o.Scope != "perso" {
		t.Fatalf("first declaration not kept (reason, session_id, scope): %+v", o)
	}
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS2+"/off-topic", pigtail.DeclareOffTopicRequest{Reason: "a\nb"}); rec.Code != 400 {
		t.Fatalf("newline reason = %d", rec.Code)
	}
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS2+"/off-topic", pigtail.DeclareOffTopicRequest{Reason: strings.Repeat("é", 201)}); rec.Code != 400 {
		t.Fatalf("201-rune reason = %d, want 400", rec.Code)
	}
	seedBinding(t, st, cmuxWS3, "sub_p")
	rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS3+"/off-topic", body)
	if rec.Code != 409 || strings.Contains(rec.Body.String(), "sub_p") {
		t.Fatalf("declare on bound = %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, perso, "DELETE", "/cmux-workspaces/"+cmuxWS+"/off-topic", nil); rec.Code != 200 {
		t.Fatalf("lift = %d", rec.Code)
	}
	if rec := doAs(t, r, perso, "DELETE", "/cmux-workspaces/"+cmuxWS+"/off-topic", nil); rec.Code != 404 {
		t.Fatalf("lift absent = %d", rec.Code)
	}
	es, _ := st.ListJournal(journalAll())
	n := 0
	for _, e := range es {
		if e.Kind == "workspace-off-topic" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("journal lines = %d, want 1", n)
	}
	if rec := doAs(t, r, work, "POST", "/cmux-workspaces/"+cmuxWS4+"/off-topic", pigtail.DeclareOffTopicRequest{Space: "Inbox"}); rec.Code != 201 {
		t.Fatalf("work declare = %d %s", rec.Code, rec.Body)
	}
	es, _ = st.ListJournal(journalAll())
	scopes := map[string]int{}
	sums := map[string]bool{}
	for _, e := range es {
		if e.Kind == "workspace-off-topic" {
			scopes[e.Scope]++
			sums[e.Summary] = true
		}
	}
	if scopes["perso"] != 1 || scopes["globex"] != 1 {
		t.Fatalf("journal scopes = %v, want one perso (master caller, concrete scope) and one globex", scopes)
	}
	// D19 n° 22, by literal value: 8 first characters of the workspace, then " : <reason>" only when a reason is given.
	for _, want := range []string{
		"workspace cmux " + cmuxWS[:8] + ` déclaré hors sujet : question "rapide" — é`,
		"workspace cmux " + cmuxWS4[:8] + " déclaré hors sujet",
	} {
		if !sums[want] {
			t.Fatalf("journal summary %q missing; got %v", want, sums)
		}
	}
	// the limit counts runes, not bytes: 200 × « é » (400 bytes) is accepted (after the journal counts, which it would shift)
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS2+"/off-topic", pigtail.DeclareOffTopicRequest{Reason: strings.Repeat("é", 200), Space: "Inbox"}); rec.Code != 201 {
		t.Fatalf("200-rune reason = %d %s, want 201", rec.Code, rec.Body)
	}
}

func TestOffTopicScopeMatrix(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/off-topic", pigtail.DeclareOffTopicRequest{SessionID: "sess-perso", Reason: "perso", Space: "Inbox"}); rec.Code != 201 {
		t.Fatal(rec.Code)
	}
	// work declares a workspace perso already declared: 200 (idempotent), body without the perso row's reason, session or scope.
	rec0 := doAs(t, r, work, "POST", "/cmux-workspaces/"+cmuxWS+"/off-topic", pigtail.DeclareOffTopicRequest{Reason: "gx"})
	if rec0.Code != 200 || strings.Contains(rec0.Body.String(), "perso") || !strings.Contains(rec0.Body.String(), cmuxWS) || !strings.Contains(rec0.Body.String(), `"declared_at"`) {
		t.Fatalf("work declare on perso declaration = %d %s, want 200 with cmux_workspace_id + declared_at only", rec0.Code, rec0.Body)
	}
	if o, _ := st.GetOffTopic(cmuxWS); o == nil || o.Scope != "perso" || o.Reason != "perso" {
		t.Fatalf("work declare changed the perso row: %+v", o)
	}
	var s pigtail.CmuxWorkspaceState
	rec := doAs(t, r, work, "GET", "/cmux-workspaces/"+cmuxWS+"/state", nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.State != pigtail.WorkspaceStateOffTopic || s.OffTopic != nil {
		t.Fatalf("work reads perso declaration = %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, work, "DELETE", "/cmux-workspaces/"+cmuxWS+"/off-topic", nil); rec.Code != 404 {
		t.Fatalf("work lifts perso = %d", rec.Code)
	}
	seedBinding(t, st, cmuxWS2, "sub_p")
	rec = doAs(t, r, work, "POST", "/cmux-workspaces/"+cmuxWS2+"/off-topic", pigtail.DeclareOffTopicRequest{})
	if rec.Code != 409 || strings.Contains(rec.Body.String(), "sub_p") {
		t.Fatalf("work declare on perso link = %d %s", rec.Code, rec.Body)
	}
	rec = doAs(t, r, work, "GET", "/off-topic-workspaces", nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), cmuxWS) {
		t.Fatalf("work list shows perso row = %s", rec.Body)
	}
	// perso (master) lifts a work declaration only with the cross-scope opt-in (CLI: lift --cross-scope).
	if rec := doAs(t, r, work, "POST", "/cmux-workspaces/"+cmuxWS3+"/off-topic", pigtail.DeclareOffTopicRequest{Space: "Inbox"}); rec.Code != 201 {
		t.Fatal(rec.Code)
	}
	if rec := doAs(t, r, perso, "DELETE", "/cmux-workspaces/"+cmuxWS3+"/off-topic", nil); rec.Code != 404 {
		t.Fatalf("perso lifts work without cross-scope = %d, want 404", rec.Code)
	}
	if rec := doAs(t, r, persoCross, "DELETE", "/cmux-workspaces/"+cmuxWS3+"/off-topic", nil); rec.Code != 200 {
		t.Fatalf("perso lifts work with cross-scope = %d, want 200", rec.Code)
	}
}

func TestAttachLiftsOffTopic(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/off-topic", pigtail.DeclareOffTopicRequest{Space: "Inbox"}); rec.Code != 201 {
		t.Fatal(rec.Code)
	}
	if rec := doAs(t, r, perso, "POST", "/subjects/sub_p/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS}); rec.Code != 201 {
		t.Fatalf("attach = %d %s", rec.Code, rec.Body)
	}
	if _, err := st.GetOffTopic(cmuxWS); err == nil {
		t.Fatal("declaration survived the link")
	}
	// D19 n° 23, by literal value: the note in sub_p's log.
	logs, _ := st.ListLog("sub_p", time.Time{})
	want, found := "workspace cmux "+cmuxWS+" : hors sujet levé par le lien", false
	for _, e := range logs {
		found = found || e.Text == want
	}
	if !found {
		t.Fatalf("log of sub_p lacks %q: %+v", want, logs)
	}
}

func TestCmuxWorkspaceState(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	seedBinding(t, st, cmuxWS, "sub_p")
	cases := []struct {
		c     caller
		ws    string
		state string
	}{
		{perso, cmuxWS, pigtail.WorkspaceStateBound},
		{work, cmuxWS, pigtail.WorkspaceStateBoundElsewhere}, // D25: a perso link reads bound-elsewhere from work, naming nothing
		{perso, cmuxWS2, pigtail.WorkspaceStateUnbound},
	}
	for _, k := range cases {
		var s pigtail.CmuxWorkspaceState
		rec := doAs(t, r, k.c, "GET", "/cmux-workspaces/"+k.ws+"/state", nil)
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.State != k.state {
			t.Errorf("%s on %s = %d %s, want %s", k.c, k.ws, rec.Code, rec.Body, k.state)
		}
		if k.c == work && (strings.Contains(rec.Body.String(), "sub_p") || s.Subject != nil) {
			t.Errorf("work state names the perso subject: %s", rec.Body)
		}
	}
}

// An orphan link (its subject is gone: GetSubject answers ErrNotFound) reads
// unbound with 200, as LookupCmuxWorkspace does — never a 500.
func TestCmuxWorkspaceStateOrphanLinkReadsUnbound(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	seedBinding(t, st, cmuxWS, "sub_gone")
	if _, err := st.GetSubject("sub_gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("precondition: sub_gone must not exist, got %v", err)
	}
	var s pigtail.CmuxWorkspaceState
	rec := doAs(t, r, perso, "GET", "/cmux-workspaces/"+cmuxWS+"/state", nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.State != pigtail.WorkspaceStateUnbound || s.Subject != nil {
		t.Fatalf("orphan link state = %d %s, want 200 unbound with no subject", rec.Code, rec.Body)
	}
}
func TestOffTopicLiftIfSession(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/off-topic", pigtail.DeclareOffTopicRequest{SessionID: "s1", Space: "Inbox"}); rec.Code != 201 {
		t.Fatal(rec.Code)
	}
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS2+"/off-topic", pigtail.DeclareOffTopicRequest{Space: "Inbox"}); rec.Code != 201 {
		t.Fatal(rec.Code)
	}
	cases := []struct {
		ws, query string
		want      int
		kept      bool
	}{
		{cmuxWS, "?session_id=s2", 404, true},  // another session ended: the declaration stays
		{cmuxWS2, "?session_id=s1", 404, true}, // a row with no session is only lifted at close
		{cmuxWS, "?session_id=s1", 200, false}, // the declaring session ended
		{cmuxWS2, "", 200, false},              // no parameter: unconditional, unchanged
	}
	for _, k := range cases {
		rec := doAs(t, r, perso, "DELETE", "/cmux-workspaces/"+k.ws+"/off-topic"+k.query, nil)
		_, err := st.GetOffTopic(k.ws)
		if rec.Code != k.want || (err == nil) != k.kept {
			t.Fatalf("DELETE %s%s = %d (row kept: %v), want %d (kept: %v)", k.ws, k.query, rec.Code, err == nil, k.want, k.kept)
		}
		// literal, not msgOffTopicMissing (written in Step 3): compiles at Step 1 and pins D19 n° 10
		if k.want == 404 && !strings.Contains(rec.Body.String(), "no off-topic declaration on this cmux workspace") {
			t.Fatalf("404 body = %s, want the same message as no row", rec.Body)
		}
	}
}

// TestAttachLiftsOffTopicOnEverySuccess (D24-1): the idempotent 200 and the --replace 201 lift the declaration and resync the markers too.
func TestAttachLiftsOffTopicOnEverySuccess(t *testing.T) {
	t.Parallel()
	r, st, svc := scopedFixtureSvc(t)
	n := 0
	svc.SyncWorkspaceMarkers = func() { n++ }
	// A bound workspace with a declaration exists only through the store (the route answers 409): an older daemon, a race.
	seedBinding(t, st, cmuxWS, "sub_p")
	seedBinding(t, st, cmuxWS2, "sub_t")
	for _, ws := range []string{cmuxWS, cmuxWS2} {
		if err := st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: "perso", DeclaredAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		c    caller
		req  pigtail.AttachCmuxWorkspaceRequest
		code int
	}{
		{"idempotent 200", perso, pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS}, 200},
		{"replace 201", persoCross, pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS2, Replace: true}, 201},
	}
	for i, k := range cases {
		rec := doAs(t, r, k.c, "POST", "/subjects/sub_p/cmux-workspaces", k.req)
		if rec.Code != k.code {
			t.Fatalf("%s: attach = %d %s", k.name, rec.Code, rec.Body)
		}
		if _, err := st.GetOffTopic(k.req.CmuxWorkspaceID); err == nil {
			t.Fatalf("%s: declaration survived the link", k.name)
		}
		if n != i+1 {
			t.Fatalf("%s: syncs = %d, want %d", k.name, n, i+1)
		}
	}
}

// TestOffTopicRoutesRejectBadWorkspace (D24-2): every new route that names a workspace answers 400 on a non-UUID
// (GET /off-topic-workspaces names none).
func TestOffTopicRoutesRejectBadWorkspace(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	for _, k := range []struct{ method, path string }{
		{"GET", "/cmux-workspaces/not-a-uuid/state"},
		{"POST", "/cmux-workspaces/not-a-uuid/off-topic"},
		{"DELETE", "/cmux-workspaces/not-a-uuid/off-topic"},
	} {
		if rec := doAs(t, r, perso, k.method, k.path, nil); rec.Code != 400 {
			t.Errorf("%s %s = %d %s, want 400", k.method, k.path, rec.Code, rec.Body)
		}
	}
}

// TestOffTopicJournalSummaryTruncated (D24-3): the journal summary is cut at 280 runes. The route caps the reason at
// 200, so the cut is reached only by the helper itself.
func TestOffTopicJournalSummaryTruncated(t *testing.T) {
	t.Parallel()
	_, st, svc := scopedFixtureSvc(t)
	o := &pigtail.WorkspaceOffTopic{CmuxWorkspaceID: cmuxWS, Scope: "perso", Reason: strings.Repeat("é", 300), DeclaredAt: time.Now().UTC()}
	journalOffTopic(svc, o)
	es, err := st.ListJournal(journalAll())
	if err != nil || len(es) != 1 {
		t.Fatalf("journal = %+v %v", es, err)
	}
	if got := []rune(es[0].Summary); len(got) != 280 || !strings.HasPrefix(es[0].Summary, "workspace cmux "+cmuxWS[:8]+" déclaré hors sujet : é") {
		t.Fatalf("summary = %d runes %q, want 280 with the prefix", len(got), es[0].Summary)
	}
}
