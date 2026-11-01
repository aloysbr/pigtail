package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// caller is who sends a request: its X-Pigtail-Scope header ("" = none) and
// whether it opts into a cross-scope write.
type caller struct {
	scope string
	cross bool
}

func (c caller) String() string {
	s := c.scope
	if s == "" {
		s = "none"
	}
	if c.cross {
		s += "+cross"
	}
	return s
}

var (
	none       = caller{}
	work       = caller{scope: "gx"}
	workCross  = caller{scope: "gx", cross: true}
	perso      = caller{scope: "perso"}
	persoCross = caller{scope: "perso", cross: true}
)

func doAs(t *testing.T, r http.Handler, c caller, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if c.scope != "" {
		req.Header.Set(pigtail.HeaderScope, c.scope)
	}
	if c.cross {
		req.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// scopedFixture seeds one work and one perso theme, each with one subject and
// one phase: thm_t/sub_t/phs_t and thm_p/sub_p/phs_p.
func scopedFixture(t *testing.T) (*chi.Mux, *store.Store) {
	t.Helper()
	r, st, _ := scopedFixtureSvc(t)
	return r, st
}

// scopedFixtureSvc is scopedFixture with its Services, for tests that hook a callback.
func scopedFixtureSvc(t *testing.T) (*chi.Mux, *store.Store, *Services) {
	t.Helper()
	return scopedFixtureSvcAt(t, ":memory:")
}

// scopedFixtureSvcAt is scopedFixtureSvc on the store at path.
func scopedFixtureSvcAt(t *testing.T, path string) (*chi.Mux, *store.Store, *Services) {
	t.Helper()
	r, st, svc := newTestRouterSvcAt(t, path)
	now := time.UnixMilli(1).UTC()
	for _, th := range []*pigtail.Theme{
		{ID: "thm_t", Name: "T", Kind: "project", VaultLink: "Globex/Projects/t", CreatedAt: now},
		{ID: "thm_p", Name: "P", Kind: "area", VaultLink: "Perso/Areas/p", CreatedAt: now},
	} {
		if err := st.CreateTheme(th); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []*pigtail.Subject{
		{ID: "sub_t", ThemeID: "thm_t", Title: "work subject", Slug: "t", Status: pigtail.StatusInbox, Rank: 2, CreatedAt: now, UpdatedAt: now},
		{ID: "sub_p", ThemeID: "thm_p", Title: "perso subject", Slug: "p", Status: pigtail.StatusInbox, Rank: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.CreateSubject(sub); err != nil {
			t.Fatal(err)
		}
		if err := st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_" + sub.Slug, SubjectID: sub.ID, Kind: "research", State: "pending", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return r, st, svc
}

// TestScopeMatrix walks caller × target × verb over every by-id route.
func TestScopeMatrix(t *testing.T) {
	t.Parallel()
	type verb struct {
		name        string
		write       bool
		method      string
		path        string // %s = t or p
		body        any
		allowedCode int
	}
	verbs := []verb{
		{"get subject", false, "GET", "/subjects/sub_%s", nil, 200},
		{"list phases", false, "GET", "/subjects/sub_%s/phases", nil, 200},
		{"list log", false, "GET", "/subjects/sub_%s/log", nil, 200},
		{"update subject", true, "POST", "/subjects/sub_%s", map[string]any{"title": "x", "status": "understanding"}, 200},
		{"create phase", true, "POST", "/subjects/sub_%s/phases", map[string]any{"kind": "spec"}, 201},
		{"append log", true, "POST", "/subjects/sub_%s/log", map[string]any{"kind": "note", "text": "x"}, 201},
		{"update phase", true, "POST", "/phases/phs_%s", map[string]any{"state": "active"}, 200},
		{"patch theme", true, "PATCH", "/themes/thm_%s", map[string]any{"name": "renamed"}, 200},
		{"delete theme", true, "DELETE", "/themes/thm_%s", nil, 409}, // allowed → reaches the linked-subjects guard
	}
	// expected returns the status for caller c acting on target (t|p).
	expected := func(v verb, c caller, target string) int {
		isPerso := c.scope == "perso"
		switch {
		case target == "p" && !isPerso:
			return 404 // work (or no header): perso does not exist
		case target == "t" && isPerso && v.write && !c.cross:
			return 403
		}
		return v.allowedCode
	}
	for _, v := range verbs {
		for _, c := range []caller{none, work, workCross, perso, persoCross} {
			for _, target := range []string{"t", "p"} {
				// Each cell on its own fixture, in parallel.
				t.Run(fmt.Sprintf("%s/as %s/on %s", v.name, c, target), func(t *testing.T) {
					t.Parallel()
					r, _ := scopedFixture(t)
					rec := doAs(t, r, c, v.method, fmt.Sprintf(v.path, target), v.body)
					if want := expected(v, c, target); rec.Code != want {
						t.Errorf("%s as %s on %s: status %d, want %d (body=%s)", v.name, c, target, rec.Code, want, rec.Body.String())
					}
					if rec.Code == 403 && !strings.Contains(rec.Body.String(), string(pigtail.ErrScopeMismatch)) {
						t.Errorf("%s as %s on %s: 403 without SCOPE_MISMATCH: %s", v.name, c, target, rec.Body.String())
					}
				})
			}
		}
	}
}

// TestScopeHidesPersoLikeUnknown: a work caller cannot tell a perso id from an
// id that does not exist.
func TestScopeHidesPersoLikeUnknown(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	hidden := doAs(t, r, work, "GET", "/subjects/sub_p", nil)
	unknown := doAs(t, r, work, "GET", "/subjects/sub_nope", nil)
	if hidden.Code != 404 || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("perso vs unknown differ: %d %s / %d %s", hidden.Code, hidden.Body, unknown.Code, unknown.Body)
	}
	hidden = doAs(t, r, work, "POST", "/phases/phs_p", map[string]any{"state": "active"})
	unknown = doAs(t, r, work, "POST", "/phases/phs_nope", map[string]any{"state": "active"})
	if hidden.Code != 404 || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("perso phase vs unknown differ: %s / %s", hidden.Body, unknown.Body)
	}
}

func TestScopeInvalidHeader(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	for _, path := range []string{"/subjects", "/themes", "/subjects/sub_t", "/subjects/next"} {
		rec := doAs(t, r, caller{scope: "everything"}, "GET", path, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s with invalid scope header: %d, want 400", path, rec.Code)
		}
	}
}

func subjectIDs(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var subs []pigtail.Subject
	if err := json.Unmarshal(rec.Body.Bytes(), &subs); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	var ids []string
	for _, s := range subs {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func TestScopeListSubjects(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	cases := []struct {
		c     caller
		query string
		code  int
		ids   string
	}{
		{none, "", 200, "sub_t"},
		{work, "", 200, "sub_t"},
		{work, "?scope=gx", 200, "sub_t"},
		{work, "?scope=perso", 400, ""}, // a forbidden scope answers like an unknown one
		{work, "?scope=all", 403, ""},
		{work, "?scope=bogus", 400, ""},
		{perso, "", 200, "sub_p,sub_t"},
		{perso, "?scope=all", 200, "sub_p,sub_t"},
		{perso, "?scope=gx", 200, "sub_t"},
		{perso, "?scope=perso", 200, "sub_p"},
		{work, "?theme=thm_p", 200, ""}, // a perso theme filter yields nothing, not an error
	}
	for _, tc := range cases {
		rec := doAs(t, r, tc.c, "GET", "/subjects"+tc.query, nil)
		if rec.Code != tc.code {
			t.Errorf("list as %s %s: %d, want %d", tc.c, tc.query, rec.Code, tc.code)
			continue
		}
		if tc.code == 200 {
			if got := subjectIDs(t, rec); got != tc.ids {
				t.Errorf("list as %s %s = %q, want %q", tc.c, tc.query, got, tc.ids)
			}
		}
	}
}

func TestScopeListThemes(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	names := func(rec *httptest.ResponseRecorder) string {
		var ths []pigtail.Theme
		_ = json.Unmarshal(rec.Body.Bytes(), &ths)
		var ids []string
		for _, th := range ths {
			ids = append(ids, th.ID+"="+th.Scope)
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}
	if got := names(doAs(t, r, none, "GET", "/themes", nil)); got != "thm_t=globex" {
		t.Errorf("themes as none = %q", got)
	}
	if got := names(doAs(t, r, perso, "GET", "/themes", nil)); got != "thm_p=perso,thm_t=globex" {
		t.Errorf("themes as perso = %q", got)
	}
	if got := names(doAs(t, r, perso, "GET", "/themes?scope=perso", nil)); got != "thm_p=perso" {
		t.Errorf("themes as perso ?scope=perso = %q", got)
	}
	if rec := doAs(t, r, work, "GET", "/themes?scope=perso", nil); rec.Code != 400 { //
		t.Errorf("work ?scope=perso = %d, want 400", rec.Code)
	}
}

func TestScopeNextSubject(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t) // sub_p has the better rank (1) of the two
	next := func(c caller, q string) (int, string) {
		rec := doAs(t, r, c, "GET", "/subjects/next"+q, nil)
		var s pigtail.Subject
		_ = json.Unmarshal(rec.Body.Bytes(), &s)
		return rec.Code, s.ID
	}
	if code, id := next(work, ""); code != 200 || id != "sub_t" {
		t.Errorf("work next = %d %s, want sub_t", code, id)
	}
	if code, id := next(none, ""); code != 200 || id != "sub_t" {
		t.Errorf("no-header next = %d %s, want sub_t", code, id)
	}
	if code, id := next(perso, ""); code != 200 || id != "sub_p" {
		t.Errorf("perso next = %d %s, want sub_p", code, id)
	}
	if code, id := next(perso, "?scope=gx"); code != 200 || id != "sub_t" {
		t.Errorf("perso next ?scope=gx = %d %s, want sub_t", code, id)
	}
	if code, _ := next(perso, "?scope=all"); code != 400 {
		t.Errorf("next ?scope=all = %d, want 400", code)
	}
	if code, _ := next(work, "?scope=perso"); code != 400 { //
		t.Errorf("work next ?scope=perso = %d, want 400", code)
	}
}

func TestScopeCreateTheme(t *testing.T) {
	t.Parallel()
	cases := []struct {
		c     caller
		link  string
		code  int
		scope string
	}{
		{work, "Globex/Projects/a", 201, "globex"},
		{none, "Globex/Projects/a", 201, "globex"},
		{work, "Perso/Areas/a", 400, ""}, // another scope's root reads as no declared root
		{workCross, "Perso/Areas/a", 400, ""},
		{perso, "Perso/Areas/a", 201, "perso"},
		{perso, "Globex/Projects/a", 403, ""},
		{persoCross, "Globex/Projects/a", 201, "globex"},
		{perso, "Areas/a", 400, ""},
		{perso, "work/Projects/a", 400, ""},
	}
	for _, tc := range cases {
		r, _ := newTestRouter(t)
		// A client-sent scope is ignored: the vault link decides.
		rec := doAs(t, r, tc.c, "POST", "/themes", map[string]any{"name": "A", "kind": "area", "vault_link": tc.link, "scope": "perso"})
		if rec.Code != tc.code {
			t.Errorf("create %s as %s: %d, want %d (%s)", tc.link, tc.c, rec.Code, tc.code, rec.Body)
			continue
		}
		if tc.code == 201 {
			var th pigtail.Theme
			_ = json.Unmarshal(rec.Body.Bytes(), &th)
			if th.Scope != tc.scope {
				t.Errorf("create %s as %s: scope %q, want %q", tc.link, tc.c, th.Scope, tc.scope)
			}
		}
	}
}

func TestScopeThemeCannotMoveScope(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	rec := doAs(t, r, persoCross, "PATCH", "/themes/thm_t", map[string]any{"vault_link": "Perso/Areas/moved"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("moving a theme across scopes: %d, want 400 (%s)", rec.Code, rec.Body)
	}
	rec = doAs(t, r, work, "PATCH", "/themes/thm_t", map[string]any{"vault_link": "Projects/unscoped"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unscoped vault_link: %d, want 400", rec.Code)
	}
	rec = doAs(t, r, work, "PATCH", "/themes/thm_t", map[string]any{"vault_link": "Globex/Areas/moved"})
	if rec.Code != http.StatusOK {
		t.Fatalf("same-scope move: %d, want 200 (%s)", rec.Code, rec.Body)
	}
}

func TestScopeCreateSubject(t *testing.T) {
	t.Parallel()
	cases := []struct {
		c     caller
		theme string
		code  int
	}{
		{work, "thm_t", 201},
		{work, "thm_p", 404}, // unknown theme, from work's point of view
		{workCross, "thm_p", 404},
		{perso, "thm_p", 201},
		{perso, "thm_t", 403},
		{persoCross, "thm_t", 201},
	}
	for _, tc := range cases {
		r, _ := scopedFixture(t)
		rec := doAs(t, r, tc.c, "POST", "/subjects", map[string]any{"theme_id": tc.theme, "title": "new"})
		if rec.Code != tc.code {
			t.Errorf("create on %s as %s: %d, want %d (%s)", tc.theme, tc.c, rec.Code, tc.code, rec.Body)
		}
	}
	r, _ := scopedFixture(t)
	hidden := doAs(t, r, work, "POST", "/subjects", map[string]any{"theme_id": "thm_p", "title": "x"})
	unknown := doAs(t, r, work, "POST", "/subjects", map[string]any{"theme_id": "thm_nope", "title": "x"})
	if strings.ReplaceAll(hidden.Body.String(), "thm_p", "ID") != strings.ReplaceAll(unknown.Body.String(), "thm_nope", "ID") {
		t.Errorf("perso theme vs unknown theme differ: %s / %s", hidden.Body, unknown.Body)
	}
}

func TestScopeSubjectReassignment(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	body := func(theme string) map[string]any { return map[string]any{"title": "work subject", "theme_id": theme} }
	if rec := doAs(t, r, work, "POST", "/subjects/sub_t", body("thm_p")); rec.Code != 404 {
		t.Errorf("work moving to a perso theme: %d, want 404", rec.Code)
	}
	if rec := doAs(t, r, persoCross, "POST", "/subjects/sub_t", body("thm_p")); rec.Code != 400 {
		t.Errorf("moving a subject across scopes: %d, want 400 (%s)", rec.Code, rec.Body)
	}
	// Without the opt-in, touching the work theme is refused first (403);
	// with it, the scope change itself is (400).
	if rec := doAs(t, r, perso, "POST", "/subjects/sub_p", map[string]any{"title": "perso subject", "theme_id": "thm_t"}); rec.Code != 403 {
		t.Errorf("perso subject to work theme: %d, want 403 (%s)", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, persoCross, "POST", "/subjects/sub_p", map[string]any{"title": "perso subject", "theme_id": "thm_t"}); rec.Code != 400 {
		t.Errorf("perso subject to work theme with opt-in: %d, want 400 (%s)", rec.Code, rec.Body)
	}
}

func TestScopeDeps(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	// Update: a work subject cannot depend on a perso one.
	rec := doAs(t, r, persoCross, "POST", "/subjects/sub_t", map[string]any{"title": "work subject", "deps": []string{"sub_p"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("cross-scope dep on update: %d, want 400 (%s)", rec.Code, rec.Body)
	}
	// Create: same.
	rec = doAs(t, r, perso, "POST", "/subjects", map[string]any{"theme_id": "thm_p", "title": "n", "deps": []string{"sub_t"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("cross-scope dep on create: %d, want 400 (%s)", rec.Code, rec.Body)
	}
	// Same-scope ids are allowed; an unknown id is refused.
	rec = doAs(t, r, work, "POST", "/subjects", map[string]any{"theme_id": "thm_t", "title": "n", "deps": []string{"sub_t", "sub_later"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown dep: %d, want 400 (%s)", rec.Code, rec.Body)
	}
	rec = doAs(t, r, work, "POST", "/subjects", map[string]any{"theme_id": "thm_t", "title": "n", "deps": []string{"sub_t"}})
	if rec.Code != http.StatusCreated {
		t.Errorf("same-scope deps: %d, want 201 (%s)", rec.Code, rec.Body)
	}
	// A legacy cross-scope dep already on the subject does not block other edits.
	legacy, _ := st.GetSubject("sub_p")
	legacy.Deps = []string{"sub_t"}
	if err := st.UpdateSubject(legacy); err != nil {
		t.Fatal(err)
	}
	rec = doAs(t, r, perso, "POST", "/subjects/sub_p", map[string]any{"title": "renamed", "deps": []string{"sub_t"}})
	if rec.Code != http.StatusOK {
		t.Errorf("legacy dep kept: %d, want 200 (%s)", rec.Code, rec.Body)
	}
}

func TestScopeSubjectWorktree(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	if err := st.CreateProject(&pigtail.Project{ID: "prj_w", Name: "w", RootPath: "/tmp/w", DefaultBackend: "git", PortRanges: map[string]pigtail.PortRange{}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(&pigtail.Workspace{ID: "ws_p", ProjectID: "prj_w", State: pigtail.StateBuilding, Path: "/tmp/p", BackendKind: "git"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePhase("phs_p", pigtail.PhaseActive, "ws_p"); err != nil {
		t.Fatal(err)
	}
	if rec := doAs(t, r, work, "GET", "/subjects/sub_p/worktree", nil); rec.Code != 404 {
		t.Errorf("work reading a perso worktree: %d, want 404", rec.Code)
	}
	if rec := doAs(t, r, perso, "GET", "/subjects/sub_p/worktree", nil); rec.Code != 200 {
		t.Errorf("perso reading its worktree: %d, want 200 (%s)", rec.Code, rec.Body)
	}
}

// scopeMW carries the web front mark of the context (handlers.WithWeb, set
// by the daemon's cookie path) into the caller; no mark, no web front.
func TestScopeMWCopiesWeb(t *testing.T) {
	t.Parallel()
	for _, marked := range []bool{false, true} {
		var got callerScope
		h := scopeMW(testResponder{})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = callerOf(r)
		}))
		req := httptest.NewRequest(http.MethodGet, "/subjects", nil)
		if marked {
			req = req.WithContext(WithWeb(req.Context()))
		}
		if IsWeb(req.Context()) != marked {
			t.Fatalf("IsWeb = %v, want %v", !marked, marked)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got.web != marked {
			t.Errorf("context marked %v: caller.web = %v", marked, got.web)
		}
	}
}
