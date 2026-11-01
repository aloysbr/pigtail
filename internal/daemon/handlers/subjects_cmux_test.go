package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	cmuxWS  = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"
	cmuxWS2 = "F8290864-F5B5-4B3F-9B93-0B027295DECC"
	cmuxWS3 = "0D7E5B1C-3A44-4F0E-9D11-3C1E2B9A7F01"
	cmuxWS4 = "0D7E5B1C-3A44-4F0E-9D11-3C1E2B9A7F02"
	cmuxWS5 = "0D7E5B1C-3A44-4F0E-9D11-3C1E2B9A7F03"
)

func seedBinding(t *testing.T, st *store.Store, ws, sub string) {
	t.Helper()
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: sub, Source: "attach", BoundAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
}

func boundTo(t *testing.T, st *store.Store, ws string) string {
	t.Helper()
	b, err := st.GetCmuxBinding(ws)
	if err != nil {
		return ""
	}
	return b.SubjectID
}

// TestCmuxBindingScopeMatrix walks caller × target over the four link routes,
// with the subject-route policy: work never sees perso (404), perso writes
// work only with the cross-scope opt-in (403 otherwise).
func TestCmuxBindingScopeMatrix(t *testing.T) {
	t.Parallel()
	type verb struct {
		name    string
		write   bool
		method  string
		path    string // %s = t or p
		body    any
		seed    bool // bind cmuxWS to the target subject first
		allowed int
	}
	verbs := []verb{
		{"attach", true, "POST", "/subjects/sub_%s/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS}, false, 201},
		{"list", false, "GET", "/subjects/sub_%s/cmux-workspaces", nil, true, 200},
		{"detach", true, "DELETE", "/subjects/sub_%s/cmux-workspaces/" + cmuxWS, nil, true, 200},
		{"lookup", false, "GET", "/cmux-workspaces/" + cmuxWS, nil, true, 200},
	}
	for _, v := range verbs {
		for _, c := range []caller{none, work, workCross, perso, persoCross} {
			for _, target := range []string{"t", "p"} {
				r, st := scopedFixture(t)
				if v.seed {
					seedBinding(t, st, cmuxWS, "sub_"+target)
				}
				path := v.path
				if strings.Contains(path, "%s") {
					path = fmt.Sprintf(path, target)
				}
				want := v.allowed
				switch {
				case target == "p" && c.scope != "perso":
					want = 404
				case target == "t" && c.scope == "perso" && v.write && !c.cross:
					want = 403
				}
				rec := doAs(t, r, c, v.method, path, v.body)
				if rec.Code != want {
					t.Errorf("%s as %s on %s: %d, want %d (%s)", v.name, c, target, rec.Code, want, rec.Body.String())
				}
				if want == 404 && strings.Contains(rec.Body.String(), "sub_p") {
					t.Errorf("%s as %s: 404 names the perso subject: %s", v.name, c, rec.Body.String())
				}
			}
		}
	}
}

// D7: a work caller learns that a workspace is bound to a subject it cannot
// read — never which one, nor its slug, title or scope.
func TestCmuxLookupTellsBoundElsewhereWithoutNaming(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	seedBinding(t, st, cmuxWS, "sub_p")
	hidden := doAs(t, r, work, "GET", "/cmux-workspaces/"+cmuxWS, nil)
	unbound := doAs(t, r, work, "GET", "/cmux-workspaces/"+cmuxWS2, nil)
	if hidden.Code != 404 || !strings.Contains(hidden.Body.String(), "bound to a subject this scope cannot read") {
		t.Fatalf("perso-bound as work = %d %s", hidden.Code, hidden.Body)
	}
	for _, leak := range []string{"sub_p", "perso subject", `"p"`, "perso", "thm_p"} {
		if strings.Contains(hidden.Body.String(), leak) {
			t.Fatalf("bound-elsewhere 404 leaks %q: %s", leak, hidden.Body)
		}
	}
	if unbound.Code != 404 || !strings.Contains(unbound.Body.String(), "no subject bound to this cmux workspace") {
		t.Fatalf("unbound as work = %d %s", unbound.Code, unbound.Body)
	}
	// testResponder drops details: read them through the package's
	// envelopeResponder (offsite_run_test.go), which writes them like the daemon.
	env := chi.NewRouter()
	RegisterSubjectRoutes(env, &Services{Store: st}, envelopeResponder{})
	if body := doAs(t, env, work, "GET", "/cmux-workspaces/"+cmuxWS, nil).Body.String(); !strings.Contains(body, `"bound_elsewhere":true`) {
		t.Fatalf("no bound_elsewhere detail: %s", body)
	}
	if body := doAs(t, env, work, "GET", "/cmux-workspaces/"+cmuxWS2, nil).Body.String(); strings.Contains(body, "bound_elsewhere") {
		t.Fatalf("unbound carries bound_elsewhere: %s", body)
	}
	ok := doAs(t, r, perso, "GET", "/cmux-workspaces/"+strings.ToLower(cmuxWS), nil)
	var got pigtail.CmuxWorkspaceLookup
	if ok.Code != 200 || json.Unmarshal(ok.Body.Bytes(), &got) != nil {
		t.Fatalf("perso lookup = %d %s", ok.Code, ok.Body)
	}
	if got.Binding.SubjectID != "sub_p" || got.Subject.Slug != "p" || got.Subject.Scope != "perso" || got.Subject.ShortTitle != "perso subject" {
		t.Fatalf("lookup = %+v", got)
	}
}

// First link wins: a second subject gets 409, whose body never names the
// holder; replace needs write access to the holder too, else the same 409.
func TestCmuxAttachFirstLinkWins(t *testing.T) {
	t.Parallel()
	attach := func(replace bool) pigtail.AttachCmuxWorkspaceRequest {
		return pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, Replace: replace}
	}
	cases := []struct {
		name    string
		holder  string // subject bound first
		c       caller
		target  string
		replace bool
		code    int
		bound   string // holder after the call
	}{
		{"work vs work, no replace", "sub_t", work, "sub_t", false, 200, "sub_t"}, // same subject: idempotent
		{"perso over work, no replace", "sub_t", persoCross, "sub_p", false, 409, "sub_t"},
		{"perso over work, replace without opt-in", "sub_t", perso, "sub_p", true, 409, "sub_t"},
		{"perso over work, replace with opt-in", "sub_t", persoCross, "sub_p", true, 201, "sub_p"},
		{"work over perso, no replace", "sub_p", work, "sub_t", false, 409, "sub_p"},
		{"work over perso, replace", "sub_p", work, "sub_t", true, 409, "sub_p"},
		{"work over perso, replace + cross", "sub_p", workCross, "sub_t", true, 409, "sub_p"},
	}
	for _, tc := range cases {
		r, st := scopedFixture(t)
		seedBinding(t, st, cmuxWS, tc.holder)
		rec := doAs(t, r, tc.c, "POST", "/subjects/"+tc.target+"/cmux-workspaces", attach(tc.replace))
		if rec.Code != tc.code {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.code, rec.Body)
		}
		if got := boundTo(t, st, cmuxWS); got != tc.bound {
			t.Errorf("%s: bound to %q, want %q", tc.name, got, tc.bound)
		}
		if rec.Code == 409 {
			body := rec.Body.String()
			if strings.Contains(body, tc.holder) || strings.Contains(body, "work subject") || strings.Contains(body, "perso subject") {
				t.Errorf("%s: 409 leaks the holder: %s", tc.name, body)
			}
			if !strings.Contains(body, "already bound") {
				t.Errorf("%s: 409 body = %s", tc.name, body)
			}
		}
	}
	// A work caller's 409 with and without replace are identical: replace
	// teaches it nothing more.
	r, st := scopedFixture(t)
	seedBinding(t, st, cmuxWS, "sub_p")
	a := doAs(t, r, work, "POST", "/subjects/sub_t/cmux-workspaces", attach(false))
	b := doAs(t, r, work, "POST", "/subjects/sub_t/cmux-workspaces", attach(true))
	if a.Body.String() != b.Body.String() {
		t.Fatalf("409 differs with replace: %s / %s", a.Body, b.Body)
	}
}

func TestCmuxAttachValidationAndRetitle(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	for _, body := range []any{
		pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: "workspace:4"},
		pigtail.AttachCmuxWorkspaceRequest{},
		pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, Source: "manual"},
		"not an object",
	} {
		if rec := doAs(t, r, work, "POST", "/subjects/sub_t/cmux-workspaces", body); rec.Code != 400 {
			t.Errorf("attach %+v = %d, want 400", body, rec.Code)
		}
	}
	// Lower case in, upper case stored; source defaults to attach.
	rec := doAs(t, r, work, "POST", "/subjects/sub_t/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: strings.ToLower(cmuxWS)})
	var b pigtail.SubjectCmuxBinding
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &b) != nil || b.CmuxWorkspaceID != cmuxWS || b.Source != "attach" || b.SubjectID != "sub_t" {
		t.Fatalf("attach = %d %s", rec.Code, rec.Body)
	}
	// Re-attach to the same subject is idempotent; with retitle it hands the
	// names back to pigtail.
	markUser(t, st, cmuxWS)
	rec = doAs(t, r, work, "POST", "/subjects/sub_t/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, Source: "ptnew"})
	if rec.Code != 200 {
		t.Fatalf("re-attach = %d %s", rec.Code, rec.Body)
	}
	if got, _ := st.GetCmuxBinding(cmuxWS); got.TitleOwner != "user" || got.Source != "attach" {
		t.Fatalf("re-attach without retitle changed the binding: %+v", got)
	}
	rec = doAs(t, r, work, "POST", "/subjects/sub_t/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, Retitle: true})
	if got, _ := st.GetCmuxBinding(cmuxWS); rec.Code != 200 || got.TitleOwner != "pigtail" || got.DescriptionOwner != "pigtail" {
		t.Fatalf("retitle = %d, binding %+v", rec.Code, got)
	}
	// Detach: invalid id 400, not bound to this subject 404.
	if rec := doAs(t, r, work, "DELETE", "/subjects/sub_t/cmux-workspaces/nope", nil); rec.Code != 400 {
		t.Errorf("detach invalid = %d", rec.Code)
	}
	if rec := doAs(t, r, work, "DELETE", "/subjects/sub_t/cmux-workspaces/"+cmuxWS2, nil); rec.Code != 404 {
		t.Errorf("detach unbound = %d", rec.Code)
	}
}

func markUser(t *testing.T, st *store.Store, ws string) {
	t.Helper()
	// Simulate the sidebar loop seeing a manual rename: re-create the row
	// with user owners (the store has no public setter; the loop will).
	b, err := st.GetCmuxBinding(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCmuxBinding(b.SubjectID, ws); err != nil {
		t.Fatal(err)
	}
	b.TitleOwner, b.DescriptionOwner = "user", "user"
	if err := st.CreateCmuxBinding(b); err != nil {
		t.Fatal(err)
	}
}

// GET /subjects/:id lists the bound workspaces; echoing them back in a
// partial update is harmless (read-only key), and sending only them writes
// nothing.
func TestSubjectCarriesCmuxWorkspaces(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	seedBinding(t, st, cmuxWS, "sub_t")
	seedBinding(t, st, cmuxWS2, "sub_t")
	rec := doAs(t, r, work, "GET", "/subjects/sub_t", nil)
	var sub pigtail.Subject
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &sub) != nil {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	if strings.Join(sub.CmuxWorkspaces, ",") != cmuxWS+","+cmuxWS2 {
		t.Fatalf("cmux_workspaces = %v", sub.CmuxWorkspaces)
	}
	rec = doAs(t, r, work, "POST", "/subjects/sub_t", map[string]any{"rank": 7, "cmux_workspaces": []string{}})
	if rec.Code != 200 {
		t.Fatalf("echo with cmux_workspaces = %d %s", rec.Code, rec.Body)
	}
	if list, _ := st.ListCmuxBindings("sub_t"); len(list) != 2 {
		t.Fatalf("partial update touched the bindings: %+v", list)
	}
	if rec := doAs(t, r, work, "POST", "/subjects/sub_t", map[string]any{"cmux_workspaces": []string{cmuxWS}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("read-only key alone = %d, want 400 nothing to update", rec.Code)
	}
}

// The binding fills the SUBJECT of `session ls` (GET /peers): a session in a
// bound cmux workspace shows the bound subject, even over the one deduced
// from its worktree, and as soon as the link is written. A work caller gets
// the work session with a perso bound subject stripped.
func TestPeersSubjectFromCmuxBinding(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	workSub, persoSub := scopedPeers(f, t)
	f.peers(t,
		// work cell, in the work worktree, but its cmux workspace is bound to the perso subject
		pigtail.PeerSession{SessionID: "s-bound-perso", Cwd: "/wt/work-relay", Name: "bp", Scope: "gx", CellID: "mt", CmuxWorkspaceID: cmuxWS},
		// work cell, no worktree: its subject comes from the binding alone
		pigtail.PeerSession{SessionID: "s-bound-work", Name: "bt", Scope: "gx", CellID: "mt", CmuxWorkspaceID: strings.ToLower(cmuxWS2)},
	)
	if p := peersAs(t, f, perso, "GET", nil)["s-bound-work"]; p.SubjectID != "" {
		t.Fatalf("unbound session has a subject: %+v", p)
	}
	for _, a := range []struct {
		c   caller
		sub string
		ws  string
	}{{perso, persoSub.ID, cmuxWS}, {work, workSub.ID, cmuxWS2}} {
		if rec := doAs(t, f.r, a.c, "POST", "/subjects/"+a.sub+"/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: a.ws}); rec.Code != http.StatusCreated {
			t.Fatalf("attach %s = %d %s", a.sub, rec.Code, rec.Body)
		}
	}
	ps := peersAs(t, f, perso, "GET", nil)
	if p := ps["s-bound-perso"]; p.SubjectID != persoSub.ID || p.SubjectSlug != persoSub.Slug {
		t.Errorf("perso view: binding must win over the worktree: %+v", p)
	}
	if p := ps["s-bound-work"]; p.SubjectSlug != workSub.Slug || !strings.HasPrefix(p.Label, pigtail.ShortTitle(workSub)) {
		t.Errorf("perso view: bound work session = %+v", p)
	}
	ps = peersAs(t, f, work, "GET", nil)
	if p, ok := ps["s-bound-perso"]; !ok || p.SubjectID != "" || p.SubjectSlug != "" || strings.Contains(p.Label, persoSub.Slug) {
		t.Errorf("work view leaks the perso bound subject: %+v (listed=%v)", p, ok)
	}
	if p := ps["s-bound-work"]; p.SubjectID != workSub.ID {
		t.Errorf("work view: work bound subject missing: %+v", p)
	}
}

// Every change of a link leaves a note in the subject log: who took or lost a
// workspace stays traceable. An idempotent re-attach writes nothing.
func TestCmuxLinkChangesAreLogged(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	notes := func(sub string) []string {
		t.Helper()
		es, err := st.ListLog(sub, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range es {
			if e.Kind != "note" {
				t.Errorf("log kind = %q, want note", e.Kind)
			}
			out = append(out, e.Text)
		}
		return out
	}
	attach := func(c caller, sub string, replace bool) {
		t.Helper()
		if rec := doAs(t, r, c, "POST", "/subjects/"+sub+"/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, Replace: replace}); rec.Code >= 300 {
			t.Fatalf("attach %s = %d %s", sub, rec.Code, rec.Body)
		}
	}
	attach(work, "sub_t", false)
	attach(work, "sub_t", false) // idempotent: no second note
	has := func(notes []string, word string) bool {
		for _, n := range notes {
			if strings.Contains(n, cmuxWS) && strings.Contains(n, word) {
				return true
			}
		}
		return false
	}
	if got := notes("sub_t"); len(got) != 1 || !has(got, "lié") {
		t.Fatalf("attach notes = %q", got)
	}
	attach(persoCross, "sub_p", true)
	if got := notes("sub_t"); len(got) != 2 || !has(got, "remplacé") {
		t.Fatalf("holder notes after replace = %q", got)
	}
	if got := notes("sub_p"); len(got) != 1 || !has(got, "remplace") {
		t.Fatalf("new subject notes after replace = %q", got)
	}
	if rec := doAs(t, r, perso, "DELETE", "/subjects/sub_p/cmux-workspaces/"+cmuxWS, nil); rec.Code != 200 {
		t.Fatalf("detach = %d", rec.Code)
	}
	if got := notes("sub_p"); len(got) != 2 || !has(got, "délié") {
		t.Fatalf("detach notes = %q", got)
	}
}

func TestMarkersFollowWrites(t *testing.T) {
	t.Parallel()
	r, _, svc := scopedFixtureSvc(t)
	n := 0
	svc.SyncWorkspaceMarkers = func() { n++ }
	steps := []struct {
		method, path string
		body         any
		want         int
	}{
		{"POST", "/subjects/sub_p/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS}, 1},
		{"POST", "/cmux-workspaces/" + cmuxWS + "/off-topic", pigtail.DeclareOffTopicRequest{}, 1}, // 409: bound, no sync
		{"DELETE", "/subjects/sub_p/cmux-workspaces/" + cmuxWS, nil, 2},
		{"POST", "/cmux-workspaces/" + cmuxWS + "/off-topic", pigtail.DeclareOffTopicRequest{Space: "Inbox"}, 3},
		{"DELETE", "/cmux-workspaces/" + cmuxWS + "/off-topic", nil, 4},
	}
	for _, k := range steps {
		doAs(t, r, perso, k.method, k.path, k.body)
		if n != k.want {
			t.Fatalf("%s %s: syncs = %d, want %d", k.method, k.path, n, k.want)
		}
	}
}

// TestBindLaunchedCreatesLinkLogsAndNudges: the daemon's launcher binds the
// tab it created without an HTTP call (web front launch, lot 2): a ptnew link,
// one note in the subject's log, one sidebar nudge; a tab already bound is
// refused with store.ErrCmuxBound, never replaced.
func TestBindLaunchedCreatesLinkLogsAndNudges(t *testing.T) {
	t.Parallel()
	_, st, svc := scopedFixtureSvc(t)
	ctl := &fakeSidebarCtl{}
	svc.CmuxSidebar = ctl
	before, err := st.ListLog("sub_p", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := BindLaunched(svc, "sub_p", strings.ToLower(cmuxWS)); err != nil {
		t.Fatal(err)
	}
	b, err := st.GetCmuxBinding(cmuxWS)
	if err != nil || b.SubjectID != "sub_p" || b.Source != pigtail.CmuxSourcePtnew {
		t.Fatalf("binding = %+v, %v; want sub_p via ptnew", b, err)
	}
	after, err := st.ListLog("sub_p", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 || !strings.Contains(after[len(after)-1].Text, cmuxWS) {
		t.Fatalf("log = %+v, want one more note naming %s", after, cmuxWS)
	}
	if ctl.nudges != 1 {
		t.Fatalf("nudges = %d, want 1", ctl.nudges)
	}
	if err := BindLaunched(svc, "sub_t", cmuxWS); !errors.Is(err, store.ErrCmuxBound) {
		t.Fatalf("binding a bound tab = %v, want store.ErrCmuxBound", err)
	}
	if boundTo(t, st, cmuxWS) != "sub_p" {
		t.Fatal("a launch bind must never replace a link")
	}
}
