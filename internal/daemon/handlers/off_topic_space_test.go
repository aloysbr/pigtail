package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// offTopicSpaceFixture is scopedFixture with the builtin space table (perso:
// Allotment, Garden; globex: Vineyard, Meadow, Greenhouse, Nursery), thm_p in
// « Allotment », and a sidebar stand-in that counts nudges.
func offTopicSpaceFixture(t *testing.T) (*chi.Mux, *store.Store, *fakeSidebarCtl) {
	t.Helper()
	return offTopicSpaceFixtureAt(t, ":memory:")
}

// offTopicSpaceFixtureAt is offTopicSpaceFixture on the store at path.
func offTopicSpaceFixtureAt(t *testing.T, path string) (*chi.Mux, *store.Store, *fakeSidebarCtl) {
	t.Helper()
	r, st, svc := scopedFixtureSvcAt(t, path)
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	svc.Spaces = tbl
	ctl := &fakeSidebarCtl{}
	svc.CmuxSidebar = ctl
	sp := "Allotment"
	if err := st.UpdateTheme("thm_p", pigtail.ThemeUpdate{Space: &sp}); err != nil {
		t.Fatal(err)
	}
	return r, st, ctl
}

func declareAs(t *testing.T, r *chi.Mux, c caller, ws string, req pigtail.DeclareOffTopicRequest) (*httptest.ResponseRecorder, pigtail.WorkspaceOffTopic) {
	t.Helper()
	rec := doAs(t, r, c, "POST", "/cmux-workspaces/"+ws+"/off-topic", req)
	var o pigtail.WorkspaceOffTopic
	_ = json.Unmarshal(rec.Body.Bytes(), &o)
	return rec, o
}

func storedSpace(t *testing.T, st *store.Store, ws string) string {
	t.Helper()
	o, err := st.GetOffTopic(ws)
	if err != nil {
		t.Fatal(err)
	}
	return o.Space
}

// D6: redeclaring with another --space updates the stored space; without
// --space the stored one is kept (choix du plan); an unknown one is a 400.
func TestRedeclareSpaceUpdates(t *testing.T) {
	t.Parallel()
	r, st, _ := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "revue de sub_p"}); rec.Code != 201 { // D4: a sub_… resolves a space, none is stored
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "" {
		t.Fatalf("declared without space: stored %q", got)
	}
	steps := []struct {
		space, want string
		code        int
	}{
		{"Allotment", "Allotment", 200},
		{"Garden", "Garden", 200},
		{"", "Garden", 200},     // no --space: kept
		{"Nope", "Garden", 400}, // unknown: refused, row unchanged
	}
	for _, s := range steps {
		rec, o := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: s.space})
		if rec.Code != s.code {
			t.Fatalf("redeclare --space %q = %d %s, want %d", s.space, rec.Code, rec.Body, s.code)
		}
		if got := storedSpace(t, st, cmuxWS); got != s.want {
			t.Fatalf("after --space %q: stored %q, want %q", s.space, got, s.want)
		}
		if s.code == 200 && o.ResolvedSpace != s.want {
			t.Fatalf("after --space %q: resolved_space %q, want %q", s.space, o.ResolvedSpace, s.want)
		}
	}
}

// A non-master redeclaring the master's row with --space: 200 masked, as
// today, row unchanged.
func TestRedeclareSpaceOtherScopeRowUnchanged(t *testing.T) {
	t.Parallel()
	r, st, ctl := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Garden"}); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	n := ctl.nudges
	rec, o := declareAs(t, r, work, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Vineyard"})
	if rec.Code != 200 || o.Scope != "" || o.ResolvedSpace != "" || strings.Contains(rec.Body.String(), "Garden") {
		t.Fatalf("work redeclare = %d %s, want 200 masked", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "Garden" {
		t.Fatalf("work changed the perso row's space to %q", got)
	}
	if ctl.nudges != n {
		t.Fatalf("nudges = %d, want %d (nothing written)", ctl.nudges, n)
	}
}

// Q5: the master with --cross-scope redeclaring another scope's row with
// --space (one of its own scope's): 200, row unchanged, no Nudge.
func TestRedeclareSpaceCrossScopeMasterUnchanged(t *testing.T) {
	t.Parallel()
	r, st, ctl := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, work, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Vineyard"}); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	n := ctl.nudges
	if rec, _ := declareAs(t, r, persoCross, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Allotment"}); rec.Code != 200 {
		t.Fatalf("cross redeclare = %d %s, want 200", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "Vineyard" {
		t.Fatalf("master changed the work row's space to %q", got)
	}
	if ctl.nudges != n {
		t.Fatalf("nudges = %d, want %d", ctl.nudges, n)
	}
}

// The space update nudges the sidebar once: the folder change does not wait
// for the slow pass.
func TestRedeclareSpaceNudges(t *testing.T) {
	t.Parallel()
	r, _, ctl := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "revue de sub_p"}); rec.Code != 201 { // D4: a sub_… resolves a space, none is stored
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	n := ctl.nudges
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Allotment"}); rec.Code != 200 {
		t.Fatalf("redeclare = %d %s", rec.Code, rec.Body)
	}
	if ctl.nudges != n+1 {
		t.Fatalf("nudges = %d, want %d", ctl.nudges, n+1)
	}
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Allotment"}); rec.Code != 200 || ctl.nudges != n+1 {
		t.Fatalf("same space again = %d, nudges %d, want no new nudge", rec.Code, ctl.nudges)
	}
}

func TestDeclareOffTopicSpace(t *testing.T) {
	t.Parallel()
	r, st, _ := offTopicSpaceFixture(t)
	rec, o := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: " Garden "})
	if rec.Code != 201 || o.Space != "Garden" || o.ResolvedSpace != "Garden" {
		t.Fatalf("declare with space = %d %s", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "Garden" {
		t.Fatalf("stored %q", got)
	}
	// unknown, or another scope's: the same 400, never echoing the name (D19)
	for _, c := range []struct {
		who  caller
		name string
	}{{perso, "Nope"}, {perso, "Vineyard"}, {work, "Allotment"}} {
		rec := doAs(t, r, c.who, "POST", "/cmux-workspaces/"+cmuxWS2+"/off-topic", pigtail.DeclareOffTopicRequest{Space: c.name})
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "unknown space (known: ") || strings.Contains(rec.Body.String(), c.name) {
			t.Fatalf("%s --space %s = %d %s, want 400 without the name", c.who, c.name, rec.Code, rec.Body)
		}
		if _, err := st.GetOffTopic(cmuxWS2); err == nil {
			t.Fatalf("%s --space %s: a row was written", c.who, c.name)
		}
	}
	// the 409 of a bound workspace is unchanged
	seedBinding(t, st, cmuxWS3, "sub_p")
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS3+"/off-topic", pigtail.DeclareOffTopicRequest{Space: "Allotment"}); rec.Code != 409 {
		t.Fatalf("bound = %d %s, want 409", rec.Code, rec.Body)
	}
}

// List and state fill resolved_space (explicit or inferred) only with a
// declared space; a reason citing a subject the row's scope may not read
// exposes nothing.
func TestOffTopicResolvedSpace(t *testing.T) {
	t.Parallel()
	r, st, _ := offTopicSpaceFixture(t)
	now := time.Now().UTC()
	for _, o := range []pigtail.WorkspaceOffTopic{
		{CmuxWorkspaceID: cmuxWS, Scope: "perso", Reason: "revue de sub_p", DeclaredAt: now},           // inferred
		{CmuxWorkspaceID: cmuxWS2, Scope: "perso", Space: "Garden", Reason: "sub_p", DeclaredAt: now}, // explicit wins
		{CmuxWorkspaceID: cmuxWS3, Scope: "globex", Reason: "revue de sub_p", DeclaredAt: now},         // unreadable subject
		{CmuxWorkspaceID: cmuxWS4, Scope: "perso", Space: "Gone", DeclaredAt: now},                     // not a declared space
		{CmuxWorkspaceID: cmuxWS5, Scope: "perso", Reason: "rien", DeclaredAt: now},                    // nothing cited
	} {
		o := o
		if err := st.CreateOffTopic(&o); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{cmuxWS: "Allotment", cmuxWS2: "Garden", cmuxWS4: "", cmuxWS5: ""}
	rec := doAs(t, r, perso, "GET", "/off-topic-workspaces", nil)
	var list []pigtail.WorkspaceOffTopic
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	got := map[string]string{}
	for _, o := range list {
		got[o.CmuxWorkspaceID] = o.ResolvedSpace
	}
	for ws, w := range want {
		if got[ws] != w {
			t.Errorf("list %s resolved_space = %q, want %q", ws[:8], got[ws], w)
		}
	}
	if got[cmuxWS3] != "" {
		t.Errorf("list (master) %s resolved_space = %q, want empty (its scope may not read sub_p)", cmuxWS3[:8], got[cmuxWS3])
	}
	rec = doAs(t, r, work, "GET", "/off-topic-workspaces", nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "resolved_space") || strings.Contains(rec.Body.String(), "Allotment") {
		t.Fatalf("work list = %d %s, want no resolved_space", rec.Code, rec.Body)
	}
	for ws, w := range want {
		var s pigtail.CmuxWorkspaceState
		rec := doAs(t, r, perso, "GET", "/cmux-workspaces/"+ws+"/state", nil)
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.OffTopic == nil || s.OffTopic.ResolvedSpace != w {
			t.Errorf("state %s = %d %s, want resolved_space %q", ws[:8], rec.Code, rec.Body, w)
		}
	}
	var s pigtail.CmuxWorkspaceState
	rec = doAs(t, r, work, "GET", "/cmux-workspaces/"+cmuxWS3+"/state", nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.OffTopic == nil || s.OffTopic.ResolvedSpace != "" {
		t.Fatalf("work state of its own row citing sub_p = %d %s, want no resolved_space", rec.Code, rec.Body)
	}
}

// sideConn opens a second connection on the fixture's database file, to break
// or rig the schema under the handler's feet.
func sideConn(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A first declaration whose space resolution fails (a store error reading the
// cited subject) is a 500 that writes nothing: the space is resolved before
// the row, the journal line, the markers or the nudge.
func TestDeclareOffTopicResolveErrorWritesNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pigtail.db")
	r, st, ctl := offTopicSpaceFixtureAt(t, path)
	if _, err := sideConn(t, path).Exec(`ALTER TABLE subjects RENAME TO subjects_gone`); err != nil {
		t.Fatal(err)
	}
	rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "revue de sub_p"})
	if rec.Code != 500 {
		t.Fatalf("declare = %d %s, want 500", rec.Code, rec.Body)
	}
	if _, err := st.GetOffTopic(cmuxWS); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetOffTopic after a failed declaration = %v, want not found (nothing written)", err)
	}
	if n := ctl.nudges; n != 0 {
		t.Fatalf("nudges = %d, want 0", n)
	}
}

// A redeclaration whose row is lifted between the read and the space update
// (rigged here by a trigger) declares afresh: 201 with the new space, not a
// 500 « not found ».
func TestRedeclareSpaceLiftedMeanwhileDeclaresAfresh(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pigtail.db")
	r, st, _ := offTopicSpaceFixtureAt(t, path)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "revue de sub_p"}); rec.Code != 201 { // D4: a sub_… resolves a space, none is stored
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	if _, err := sideConn(t, path).Exec(`CREATE TRIGGER lift_meanwhile BEFORE UPDATE OF space ON workspace_off_topic
		BEGIN DELETE FROM workspace_off_topic WHERE cmux_workspace_id = OLD.cmux_workspace_id; END`); err != nil {
		t.Fatal(err)
	}
	rec, o := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Garden"})
	if rec.Code != 201 || o.Space != "Garden" {
		t.Fatalf("redeclare after a concurrent lift = %d %s, want 201 with space Garden", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "Garden" {
		t.Fatalf("stored space = %q, want Garden", got)
	}
}

// R2 of sub_x000000000000027: « délie : <id> refusé » never files the session
// in the refused subject's folder. D4 (sub_x000000000000033): with no other
// space it is a 400 and nothing is written; with the sentinel it files in Inbox.
func TestDeclareOffTopicRefusedNoFolder(t *testing.T) {
	t.Parallel()
	r, st, _ := offTopicSpaceFixture(t)
	rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "délie : sub_p refusé"})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "INVALID_REQUEST") || !strings.Contains(rec.Body.String(), "--space") || !strings.Contains(rec.Body.String(), "Inbox") {
		t.Fatalf("declare = %d %s, want 400 INVALID_REQUEST naming --space and Inbox", rec.Code, rec.Body)
	}
	if _, err := st.GetOffTopic(cmuxWS); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetOffTopic = %v, want not found", err)
	}
	rec, o := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "délie : sub_p refusé", Space: "Inbox"})
	if rec.Code != 201 || o.ResolvedSpace != "Inbox" {
		t.Fatalf("declare with Inbox = %d %s, want 201 resolved Inbox (not Allotment)", rec.Code, rec.Body)
	}
}

// D4 (sub_x000000000000033): a first declaration that resolves no space is a
// 400 that writes nothing; the message points at --space and Inbox.
func TestDeclareOffTopicRefusedWithoutSpace(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		who  caller
		req  pigtail.DeclareOffTopicRequest
	}{
		{"no space no subject", perso, pigtail.DeclareOffTopicRequest{Reason: "micro-fix"}},
		{"empty", perso, pigtail.DeclareOffTopicRequest{}},
		{"refused subject", perso, pigtail.DeclareOffTopicRequest{Reason: "délie : sub_p refusé"}},
		{"subject of another scope", work, pigtail.DeclareOffTopicRequest{Reason: "revue de sub_p"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, st, ctl := offTopicSpaceFixture(t)
			rec, _ := declareAs(t, r, c.who, cmuxWS, c.req)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), "INVALID_REQUEST") || !strings.Contains(rec.Body.String(), "--space") || !strings.Contains(rec.Body.String(), "Inbox") {
				t.Fatalf("declare = %d %s, want 400 INVALID_REQUEST naming --space and Inbox", rec.Code, rec.Body)
			}
			if _, err := st.GetOffTopic(cmuxWS); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("GetOffTopic = %v, want not found", err)
			}
			if es, err := st.ListJournal(store.JournalFilter{Kind: "workspace-off-topic"}); err != nil || len(es) != 0 {
				t.Fatalf("journal = %v %v, want none", es, err)
			}
			if ctl.nudges != 0 {
				t.Fatalf("nudges = %d, want 0", ctl.nudges)
			}
		})
	}
}

// The 400 lists the caller's scope's spaces and Inbox, never another scope's;
// every listed name is accepted on retry.
func TestDeclareOffTopicRefusalListsScopeSpacesOnly(t *testing.T) {
	t.Parallel()
	r, _, _ := offTopicSpaceFixture(t)
	rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{})
	if rec.Code != 400 {
		t.Fatalf("declare = %d %s, want 400", rec.Code, rec.Body)
	}
	var env struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Message == "" {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	msg := env.Message
	i := strings.Index(msg, "(known: ")
	if i < 0 {
		t.Fatalf("message %q lists no spaces", msg)
	}
	list := msg[i+len("(known: "):]
	list = list[:strings.Index(list, ")")]
	names := strings.Split(list, ", ")
	for _, bad := range []string{"Vineyard", "Meadow", "Greenhouse", "Nursery"} {
		if strings.Contains(list, bad) {
			t.Fatalf("message %q lists %s of another scope", msg, bad)
		}
	}
	for _, want := range []string{"Allotment", "Garden", "Inbox"} {
		if !slices.Contains(names, want) {
			t.Fatalf("message %q misses %s", msg, want)
		}
	}
	for i, n := range names {
		ws := "00000000-0000-4000-8000-00000000000" + string(rune('1'+i))
		if rec, _ := declareAs(t, r, perso, ws, pigtail.DeclareOffTopicRequest{Space: n}); rec.Code != 201 {
			t.Fatalf("retry --space %s = %d %s", n, rec.Code, rec.Body)
		}
	}
}

// « Inbox » is the sentinel: accepted on a first declaration, stored as such.
func TestDeclareOffTopicInboxSentinel(t *testing.T) {
	t.Parallel()
	r, st, _ := offTopicSpaceFixture(t)
	rec, o := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Inbox"})
	if rec.Code != 201 || o.ResolvedSpace != "Inbox" {
		t.Fatalf("declare Inbox = %d %s, want 201 resolved Inbox", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "Inbox" {
		t.Fatalf("stored space %q, want Inbox", got)
	}
	// The former no-subject name « Other » (space.FormerNoSubject) is no
	// sentinel: no input reads it as a former name, so it is an unknown space.
	if rec, _ := declareAs(t, r, perso, cmuxWS3, pigtail.DeclareOffTopicRequest{Space: space.FormerNoSubject}); rec.Code != 400 {
		t.Fatalf("declare Other = %d %s, want 400 (unknown space)", rec.Code, rec.Body)
	}
	// With a refused subject in the reason, Inbox still files in Inbox.
	rec, o = declareAs(t, r, perso, cmuxWS2, pigtail.DeclareOffTopicRequest{Space: "Inbox", Reason: "délie : sub_p refusé"})
	if rec.Code != 201 || o.ResolvedSpace != "Inbox" {
		t.Fatalf("declare Inbox + refused = %d %s", rec.Code, rec.Body)
	}
}

func TestRedeclareInboxSentinel(t *testing.T) {
	t.Parallel()
	r, st, _ := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Allotment"}); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	rec, o := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Inbox"})
	if rec.Code != 200 || o.ResolvedSpace != "Inbox" {
		t.Fatalf("redeclare Inbox = %d %s, want 200 resolved Inbox", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "Inbox" {
		t.Fatalf("stored space %q, want Inbox", got)
	}
}

// Guard (green on the base): a sub_… cited with a known space is accepted.
func TestDeclareOffTopicInferredAccepted(t *testing.T) {
	t.Parallel()
	r, _, _ := offTopicSpaceFixture(t)
	rec, o := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "revue de sub_p"})
	if rec.Code != 201 || o.ResolvedSpace != "Allotment" {
		t.Fatalf("declare = %d %s, want 201 in Allotment", rec.Code, rec.Body)
	}
}

// Guard (green on the base): a redeclaration without a space is never refused.
func TestRedeclareWithoutSpaceUnchanged(t *testing.T) {
	t.Parallel()
	r, st, _ := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Garden"}); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "autre"}); rec.Code != 200 {
		t.Fatalf("redeclare = %d %s, want 200", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "Garden" {
		t.Fatalf("stored space %q, want Garden", got)
	}
}

func setOffTopicSpaceAs(t *testing.T, r *chi.Mux, c caller, ws string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doAs(t, r, c, "PATCH", "/cmux-workspaces/"+ws+"/off-topic/space", body)
}

// A8: the master rewrites only the space; session_id, reason, scope and
// declared_at stay, for its own scope's row and (with --cross-scope) another's.
func TestOffTopicSetSpaceRewrites(t *testing.T) {
	t.Parallel()
	r, st, ctl := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{SessionID: "sess-1", Reason: "why", Space: "Allotment"}); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	before, _ := st.GetOffTopic(cmuxWS)
	n := ctl.nudges
	rec := setOffTopicSpaceAs(t, r, perso, cmuxWS, map[string]string{"space": "Garden"})
	if rec.Code != 200 {
		t.Fatalf("set-space = %d %s", rec.Code, rec.Body)
	}
	after, _ := st.GetOffTopic(cmuxWS)
	if after.Space != "Garden" || after.SessionID != before.SessionID || after.Reason != before.Reason ||
		after.Scope != before.Scope || !after.DeclaredAt.Equal(before.DeclaredAt) {
		t.Fatalf("row after = %+v, before = %+v: only space may change", after, before)
	}
	if ctl.nudges != n+1 {
		t.Fatalf("nudges = %d, want %d", ctl.nudges, n+1)
	}
	// D4 t2: the perso aire Pond is a valid target for a perso row (master)
	if rec := setOffTopicSpaceAs(t, r, perso, cmuxWS, map[string]string{"space": "Pond"}); rec.Code != 200 {
		t.Fatalf("set-space Pond = %d %s", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "Pond" {
		t.Fatalf("stored %q, want Pond", got)
	}
	// another scope's row (work), master with --cross-scope
	ws2 := "11111111-2222-3333-4444-555555555555"
	if rec, _ := declareAs(t, r, work, ws2, pigtail.DeclareOffTopicRequest{SessionID: "sess-t", Reason: "t", Space: "Vineyard"}); rec.Code != 201 {
		t.Fatalf("declare work = %d %s", rec.Code, rec.Body)
	}
	if rec := setOffTopicSpaceAs(t, r, perso, ws2, map[string]string{"space": "Meadow"}); rec.Code != 403 {
		t.Fatalf("master without --cross-scope on a work row = %d %s, want 403", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, ws2); got != "Vineyard" {
		t.Fatalf("refused call changed the space to %q", got)
	}
	tb, _ := st.GetOffTopic(ws2)
	if rec := setOffTopicSpaceAs(t, r, persoCross, ws2, map[string]string{"space": "Meadow"}); rec.Code != 200 {
		t.Fatalf("cross set-space = %d %s", rec.Code, rec.Body)
	}
	ta, _ := st.GetOffTopic(ws2)
	if ta.Space != "Meadow" || ta.SessionID != tb.SessionID || ta.Reason != tb.Reason || ta.Scope != tb.Scope || !ta.DeclaredAt.Equal(tb.DeclaredAt) {
		t.Fatalf("work row after = %+v, before = %+v", ta, tb)
	}
}

// space "" is accepted: the stored space is cleared and the daemon infers it.
func TestOffTopicSetSpaceEmpty(t *testing.T) {
	t.Parallel()
	r, st, _ := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Garden"}); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	if rec := setOffTopicSpaceAs(t, r, perso, cmuxWS, map[string]string{"space": ""}); rec.Code != 200 {
		t.Fatalf("set-space '' = %d %s", rec.Code, rec.Body)
	}
	if got := storedSpace(t, st, cmuxWS); got != "" {
		t.Fatalf("stored %q, want empty", got)
	}
}

// Only the master: a work cell gets 403 SCOPE_MISMATCH, its own row included, and the row is untouched.
func TestOffTopicSetSpaceRefusedNonMaster(t *testing.T) {
	t.Parallel()
	r, st, ctl := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, work, cmuxWS, pigtail.DeclareOffTopicRequest{Space: "Vineyard"}); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	n := ctl.nudges
	for _, c := range []caller{work, workCross} {
		rec := setOffTopicSpaceAs(t, r, c, cmuxWS, map[string]string{"space": "Meadow"})
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "SCOPE_MISMATCH") {
			t.Fatalf("%s set-space = %d %s, want 403 SCOPE_MISMATCH", c, rec.Code, rec.Body)
		}
	}
	if got := storedSpace(t, st, cmuxWS); got != "Vineyard" {
		t.Fatalf("row changed to %q", got)
	}
	if ctl.nudges != n {
		t.Fatalf("nudges = %d, want %d", ctl.nudges, n)
	}
}

func TestOffTopicSetSpaceUnknown(t *testing.T) {
	t.Parallel()
	r, _, _ := offTopicSpaceFixture(t)
	if rec := setOffTopicSpaceAs(t, r, perso, cmuxWS, map[string]string{"space": "Garden"}); rec.Code != 404 {
		t.Fatalf("no row = %d %s, want 404", rec.Code, rec.Body)
	}
}

// A space of another scope than the row's, or unknown, is a 400 that never
// echoes the name; the row stays intact, space included (no half-written row).
func TestOffTopicSetSpaceUndeclared(t *testing.T) {
	t.Parallel()
	r, st, ctl := offTopicSpaceFixture(t)
	if rec, _ := declareAs(t, r, perso, cmuxWS, pigtail.DeclareOffTopicRequest{Reason: "keep", Space: "Allotment"}); rec.Code != 201 {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	before, _ := st.GetOffTopic(cmuxWS)
	n := ctl.nudges
	for _, sp := range []string{"Vineyard", "Zzqx-Nope"} {
		rec := setOffTopicSpaceAs(t, r, perso, cmuxWS, map[string]string{"space": sp})
		if rec.Code != 400 || strings.Contains(rec.Body.String(), sp) {
			t.Fatalf("space %q = %d %s, want 400 without the name", sp, rec.Code, rec.Body)
		}
	}
	after, _ := st.GetOffTopic(cmuxWS)
	if after.Space != before.Space || after.Reason != before.Reason || !after.DeclaredAt.Equal(before.DeclaredAt) {
		t.Fatalf("row changed: %+v -> %+v", before, after)
	}
	if ctl.nudges != n {
		t.Fatalf("nudges = %d, want %d (nothing written)", ctl.nudges, n)
	}
	// D4 t2: Pond is a perso aire, refused on a work row even for the master
	// with --cross-scope; the work row stays intact.
	ws2 := "11111111-2222-3333-4444-555555555555"
	if rec, _ := declareAs(t, r, work, ws2, pigtail.DeclareOffTopicRequest{SessionID: "sess-t", Reason: "t", Space: "Vineyard"}); rec.Code != 201 {
		t.Fatalf("declare work = %d %s", rec.Code, rec.Body)
	}
	tb, _ := st.GetOffTopic(ws2)
	n = ctl.nudges
	if rec := setOffTopicSpaceAs(t, r, persoCross, ws2, map[string]string{"space": "Pond"}); rec.Code != 400 || strings.Contains(rec.Body.String(), "Pond") {
		t.Fatalf("space Pond on a work row = %d %s, want 400 without the name", rec.Code, rec.Body)
	}
	ta, _ := st.GetOffTopic(ws2)
	if ta.Space != tb.Space || ta.Reason != tb.Reason || !ta.DeclaredAt.Equal(tb.DeclaredAt) {
		t.Fatalf("work row changed: %+v -> %+v", tb, ta)
	}
	if ctl.nudges != n {
		t.Fatalf("nudges = %d, want %d (nothing written)", ctl.nudges, n)
	}
}
