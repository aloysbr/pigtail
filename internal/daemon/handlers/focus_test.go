package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/daemon/store/storetest"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// focusRouter: scopedFixture (thm_t/sub_t work, thm_p/sub_p perso), the
// built-in space table and a sidebar stand-in whose folders run.
func focusRouter(t *testing.T) (*chi.Mux, *store.Store, *Services, *fakeSidebarCtl) {
	t.Helper()
	r, st, svc := scopedFixtureSvc(t)
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	ctl := &fakeSidebarCtl{state: pigtail.CmuxSidebarState{Enabled: true, Folders: true}}
	svc.Spaces, svc.CmuxSidebar = tbl, ctl
	return r, st, svc, ctl
}

func postFocus(t *testing.T, r http.Handler, c caller, name string, endsAt *time.Time, members ...pigtail.FocusMember) *httptest.ResponseRecorder {
	t.Helper()
	return doAs(t, r, c, "POST", "/focus", FocusCreateRequest{Name: name, EndsAt: endsAt, Members: members})
}

func theme(id string) pigtail.FocusMember {
	return pigtail.FocusMember{Kind: pigtail.FocusMemberTheme, ID: id}
}
func subj(id string) pigtail.FocusMember {
	return pigtail.FocusMember{Kind: pigtail.FocusMemberSubject, ID: id}
}

func decodeFocus(t *testing.T, rec *httptest.ResponseRecorder) pigtail.Focus {
	t.Helper()
	var f pigtail.Focus
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return f
}

func mustCreateFocus(t *testing.T, r http.Handler, c caller, name string, members ...pigtail.FocusMember) pigtail.Focus {
	t.Helper()
	rec := postFocus(t, r, c, name, nil, members...)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %q as %v = %d %s", name, c, rec.Code, rec.Body.String())
	}
	return decodeFocus(t, rec)
}

func storeFocus(t *testing.T, st *store.Store) []pigtail.Focus {
	t.Helper()
	fs, _, err := st.ListFocus(time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestCreateFocusOwnScope(t *testing.T) {
	t.Parallel()
	r, st, _, _ := focusRouter(t)
	f := mustCreateFocus(t, r, perso, "Workshop", theme("thm_p"), subj("sub_p"))
	got, _, err := st.GetFocus(f.ID, time.Now())
	if err != nil || got.Scope != "perso" || got.Name != "Workshop" || len(got.Members) != 2 {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	if !strings.HasPrefix(f.ID, pigtail.PrefixFocus) {
		t.Fatalf("id = %q", f.ID)
	}
}

func TestCreateFocusOtherScopeMemberHidden(t *testing.T) {
	t.Parallel()
	r, st, _, _ := focusRouter(t)
	for _, pair := range [][2]pigtail.FocusMember{{theme("thm_p"), theme("thm_nope")}, {subj("sub_p"), subj("sub_nope")}} {
		hidden := postFocus(t, r, work, "Worky", nil, pair[0])
		unknown := postFocus(t, r, work, "Worky", nil, pair[1])
		if hidden.Code != http.StatusNotFound || hidden.Body.String() != unknown.Body.String() {
			t.Fatalf("hidden %v = %d %s; unknown = %d %s", pair[0], hidden.Code, hidden.Body, unknown.Code, unknown.Body)
		}
	}
	if fs := storeFocus(t, st); len(fs) != 0 {
		t.Fatalf("created: %+v", fs)
	}
}

// D6: the master, even with --cross-scope, cannot mix scopes in a focus.
func TestCreateFocusMasterMixedRefused(t *testing.T) {
	t.Parallel()
	r, st, _, _ := focusRouter(t)
	for _, m := range []pigtail.FocusMember{theme("thm_t"), subj("sub_t")} {
		rec := postFocus(t, r, persoCross, "Mixed", nil, theme("thm_p"), m)
		body := rec.Body.String()
		if rec.Code != http.StatusForbidden || !strings.Contains(body, string(pigtail.ErrScopeMismatch)) ||
			!strings.Contains(body, focusMixedMsg) || strings.Contains(body, string(scope.Canonical("gx"))) || strings.Contains(body, "perso") {
			t.Fatalf("mixed %v = %d %s", m, rec.Code, body)
		}
	}
	if fs := storeFocus(t, st); len(fs) != 0 {
		t.Fatalf("created: %+v", fs)
	}
}

func TestCreateFocusNameCollisions(t *testing.T) {
	t.Parallel()
	r, _, _, _ := focusRouter(t)
	mustCreateFocus(t, r, perso, "Open one")
	for _, c := range []struct {
		name string
		code int
	}{
		{"Vineyard", http.StatusConflict},  // a work space, asked by perso
		{"Allotment", http.StatusConflict}, // a perso space
		{space.NoSubject, http.StatusBadRequest},
		{space.Standby, http.StatusBadRequest},
		{space.Prio, http.StatusBadRequest},
		{"-flag", http.StatusBadRequest},
		{"Open one", http.StatusConflict},
	} {
		rec := postFocus(t, r, perso, c.name, nil)
		body := rec.Body.String()
		if rec.Code != c.code || strings.Contains(body, string(scope.Canonical("gx"))) || strings.Contains(body, "perso") {
			t.Fatalf("%q = %d %s, want %d without a scope name", c.name, rec.Code, body, c.code)
		}
	}
}

// Focus D28: a refused focus name is named a focus name; the rule text is the
// space one, unchanged.
func TestCreateFocusRefusedNameSaysFocusName(t *testing.T) {
	t.Parallel()
	r, _, _, _ := focusRouter(t)
	for _, c := range []struct{ name, rule string }{
		{space.NoSubject, " is reserved"},
		{space.Prio, " is reserved"},
		{"-flag", ": 1 to 64 characters, not starting with -"},
	} {
		rec := postFocus(t, r, perso, c.name, nil)
		var e pigtail.APIError
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Fatalf("%q: decode %s: %v", c.name, rec.Body, err)
		}
		want := fmt.Sprintf("focus name %q", c.name) + c.rule
		if rec.Code != http.StatusBadRequest || !strings.HasPrefix(e.Message, `focus name "`) || e.Message != want {
			t.Fatalf("%q = %d %q, want 400 %q", c.name, rec.Code, e.Message, want)
		}
	}
}

// sub_x000000000000029: « Prio » is the name of pigtail's own top folder, never
// a focus.
func TestCreateFocusPrioRefused(t *testing.T) {
	t.Parallel()
	r, _, _, _ := focusRouter(t)
	rec := postFocus(t, r, perso, space.Prio, nil)
	var e pigtail.APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if rec.Code != http.StatusBadRequest || e.Code != pigtail.ErrInvalidRequest {
		t.Fatalf("focus Prio = %d %+v, want 400 INVALID_REQUEST", rec.Code, e)
	}
}

// Focus D15: one body, byte for byte, whoever holds the name.
func TestCreateFocusNameTakenNeutralAcrossScopes(t *testing.T) {
	t.Parallel()
	r, _, _, _ := focusRouter(t)
	held := mustCreateFocus(t, r, perso, "Zqx")
	b1 := postFocus(t, r, work, "Zqx", nil)
	b2 := postFocus(t, r, perso, "Zqx", nil)
	b3 := postFocus(t, r, work, "Garden", nil) // a perso space
	for _, rec := range []*httptest.ResponseRecorder{b1, b2, b3} {
		if rec.Code != http.StatusConflict {
			t.Fatalf("code = %d %s", rec.Code, rec.Body)
		}
	}
	if b1.Body.String() != b2.Body.String() || b1.Body.String() != b3.Body.String() {
		t.Fatalf("bodies differ:\n%s\n%s\n%s", b1.Body, b2.Body, b3.Body)
	}
	body := b1.Body.String()
	for _, leak := range []string{string(scope.Canonical("perso")), held.ID, "space", "perso"} {
		if strings.Contains(body, leak) {
			t.Fatalf("body %s names %q", body, leak)
		}
	}
	if !strings.Contains(body, focusNameTakenMsg) {
		t.Fatalf("body = %s", body)
	}
}

// Focus D15 (review M1): a name held by a space and a name held by another
// scope's focus answer alike whatever else is wrong in the request — a past
// deadline, a member the caller may not see: the request is judged first, the
// name last, so the order of the checks names nothing.
func TestCreateFocusNameTakenNeutralWithBadRequest(t *testing.T) {
	t.Parallel()
	r, _, _, _ := focusRouter(t)
	mustCreateFocus(t, r, perso, "Zqx")
	past := time.Now().Add(-time.Minute)
	for _, c := range []struct {
		what    string
		endsAt  *time.Time
		members []pigtail.FocusMember
		code    int
	}{
		{"past deadline", &past, nil, http.StatusBadRequest},
		{"hidden member", nil, []pigtail.FocusMember{theme("thm_p")}, http.StatusNotFound},
	} {
		bySpace := postFocus(t, r, work, "Garden", c.endsAt, c.members...) // a perso space
		byFocus := postFocus(t, r, work, "Zqx", c.endsAt, c.members...)     // a perso focus
		if bySpace.Code != c.code || byFocus.Code != c.code || bySpace.Body.String() != byFocus.Body.String() {
			t.Fatalf("%s: space name = %d %s; other scope's focus name = %d %s; want both %d, same body",
				c.what, bySpace.Code, bySpace.Body, byFocus.Code, byFocus.Body, c.code)
		}
	}
}

func TestCreateFocusPastDeadline(t *testing.T) {
	t.Parallel()
	r, st, _, _ := focusRouter(t)
	past := time.Now().Add(-time.Minute)
	rec := postFocus(t, r, perso, "Late", &past, theme("thm_p"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), focusPastMsg) {
		t.Fatalf("past = %d %s", rec.Code, rec.Body)
	}
	if fs := storeFocus(t, st); len(fs) != 0 {
		t.Fatalf("created: %+v", fs)
	}
}

// Correction l. 9981: the loop off is a State() with Folders false, never a nil control.
func TestCreateFocusNoteWhenFoldersIdle(t *testing.T) {
	t.Parallel()
	r, _, _, ctl := focusRouter(t)
	ctl.state = pigtail.CmuxSidebarState{Enabled: true, Folders: false}
	f := mustCreateFocus(t, r, perso, "Idle")
	if f.Note != "folders: idle (pigtail cmux sidebar on, --cmux-folders)" {
		t.Fatalf("note = %q", f.Note)
	}
	var list FocusList
	_ = json.Unmarshal(doAs(t, r, perso, "GET", "/focus", nil).Body.Bytes(), &list)
	if list.Note != f.Note {
		t.Fatalf("list note = %q", list.Note)
	}
	ctl.state = pigtail.CmuxSidebarState{Folders: true, FoldersNote: ""}
	if f := mustCreateFocus(t, r, perso, "Running"); f.Note != "" {
		t.Fatalf("note with folders running = %q", f.Note)
	}
	ctl.state = pigtail.CmuxSidebarState{Folders: true, FoldersNote: "several windows"}
	if f := mustCreateFocus(t, r, perso, "Noted"); f.Note != "folders: several windows" {
		t.Fatalf("note = %q", f.Note)
	}
}

func TestFocusMemberRoutes(t *testing.T) {
	t.Parallel()
	r, st, _, _ := focusRouter(t)
	mustCreateFocus(t, r, perso, "Alpha", theme("thm_p"))
	beta := mustCreateFocus(t, r, perso, "Beta")
	rec := doAs(t, r, perso, "POST", "/focus/"+beta.ID+"/members", theme("thm_p"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "theme thm_p is already in an open focus") {
		t.Fatalf("taken = %d %s", rec.Code, rec.Body)
	}
	rec = doAs(t, r, perso, "POST", "/focus/"+beta.ID+"/members", subj("sub_p"))
	if f := decodeFocus(t, rec); rec.Code != http.StatusOK || len(f.Members) != 1 || f.Members[0].ID != "sub_p" {
		t.Fatalf("add = %d %s", rec.Code, rec.Body)
	}
	rec = doAs(t, r, perso, "DELETE", "/focus/"+beta.ID+"/members/subject/sub_p", nil)
	if f := decodeFocus(t, rec); rec.Code != http.StatusOK || len(f.Members) != 0 {
		t.Fatalf("rm = %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, perso, "DELETE", "/focus/"+beta.ID+"/members/subject/sub_p", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("rm absent = %d %s", rec.Code, rec.Body)
	}
	// Another scope's focus reads, to a work caller, like an unknown id.
	hidden := doAs(t, r, work, "POST", "/focus/"+beta.ID+"/end", nil)
	unknown := doAs(t, r, work, "POST", "/focus/fcs_nope/end", nil)
	if hidden.Code != http.StatusNotFound || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("work end perso = %d %s; unknown = %s", hidden.Code, hidden.Body, unknown.Body)
	}
	rec = doAs(t, r, perso, "POST", "/focus/"+beta.ID+"/end", nil)
	if f := decodeFocus(t, rec); rec.Code != http.StatusOK || f.EndReason != pigtail.FocusEndManual {
		t.Fatalf("end = %d %s", rec.Code, rec.Body)
	}
	rec = doAs(t, r, perso, "POST", "/focus/"+beta.ID+"/end", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), focusEndedMsg) {
		t.Fatalf("end again = %d %s", rec.Code, rec.Body)
	}
	// D6 at add: compared with the focus's scope, not the caller's.
	worky := mustCreateFocus(t, r, work, "Worky")
	if worky.Scope != string(scope.Canonical("gx")) {
		t.Fatalf("work focus scope = %q", worky.Scope)
	}
	// A perso theme in no other focus: only the scope rule can refuse it.
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_p2", Name: "P2", Kind: "ongoing", VaultLink: "Notes/Areas/p2", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec = doAs(t, r, persoCross, "POST", "/focus/"+worky.ID+"/members", theme("thm_p2"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), string(pigtail.ErrScopeMismatch)) {
		t.Fatalf("master adds perso to work focus = %d %s", rec.Code, rec.Body)
	}
	if got, _, _ := st.GetFocus(worky.ID, time.Now()); len(got.Members) != 0 {
		t.Fatalf("members after refusal = %+v", got.Members)
	}
	rec = doAs(t, r, persoCross, "POST", "/focus/"+worky.ID+"/members", theme("thm_t"))
	if rec.Code != http.StatusOK {
		t.Fatalf("master adds work theme = %d %s", rec.Code, rec.Body)
	}
	if got, _, _ := st.GetFocus(worky.ID, time.Now()); len(got.Members) != 1 || got.Members[0].ID != "thm_t" {
		t.Fatalf("members = %+v", got.Members)
	}
}

func TestListFocusFiltersByScope(t *testing.T) {
	t.Parallel()
	r, _, _, _ := focusRouter(t)
	p := mustCreateFocus(t, r, perso, "Pfocus", theme("thm_p"))
	tk := mustCreateFocus(t, r, work, "Tfocus", theme("thm_t"))
	list := func(c caller) map[string]bool {
		var l FocusList
		rec := doAs(t, r, c, "GET", "/focus", nil)
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("list = %d %s", rec.Code, rec.Body)
		}
		out := map[string]bool{}
		for _, f := range l.Focus {
			out[f.ID] = true
		}
		return out
	}
	if got := list(work); len(got) != 1 || !got[tk.ID] {
		t.Fatalf("work sees %v", got)
	}
	if got := list(perso); len(got) != 2 || !got[tk.ID] || !got[p.ID] {
		t.Fatalf("master sees %v", got)
	}
}

func TestListFocusMarksDeletedMember(t *testing.T) {
	t.Parallel()
	r, st, _, _ := focusRouter(t)
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_p2", Name: "P2", Kind: "ongoing", VaultLink: "Notes/Areas/p2", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mustCreateFocus(t, r, perso, "Gone", theme("thm_p2"))
	if rec := doAs(t, r, perso, "DELETE", "/themes/thm_p2", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete theme = %d %s", rec.Code, rec.Body)
	}
	body := doAs(t, r, perso, "GET", "/focus", nil).Body.String()
	if !strings.Contains(body, `"id":"thm_p2"`) || !strings.Contains(body, `"missing":true`) {
		t.Fatalf("list = %s", body)
	}
}

func TestListFocusServedWhenSealFails(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pigtail.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_p", Name: "P", Kind: "ongoing", VaultLink: "Notes/Areas/p", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_p", ThemeID: "thm_p", Title: "p", Slug: "p", Status: pigtail.StatusInbox, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var logged []string
	svc := &Services{Store: st, Logf: func(f string, a ...any) { logged = append(logged, f) }}
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, testResponder{})
	ends := time.Now().Add(-time.Hour)
	f := &pigtail.Focus{ID: "fcs_old", Name: "Old", Scope: "perso", EndsAt: &ends, Members: []pigtail.FocusMember{subj("sub_p")}}
	if err := st.CreateFocus(f, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	restore := storetest.FailFocusSeal(t, path)
	rec := doAs(t, r, perso, "GET", "/focus", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "fcs_old") {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if len(logged) == 0 {
		t.Fatal("seal failure not logged")
	}
	rec = doAs(t, r, perso, "POST", "/focus/fcs_old/end", nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "focus: seal") {
		t.Fatalf("end = %d %s", rec.Code, rec.Body)
	}
	if lines, _ := st.ListLog("sub_p", time.Time{}); len(lines) != 0 {
		t.Fatalf("sealed while failing: %+v", lines)
	}
	if rec := doAs(t, r, perso, "POST", "/focus/fcs_nope/end", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown = %d %s", rec.Code, rec.Body)
	}
	restore()
	var l FocusList
	_ = json.Unmarshal(doAs(t, r, perso, "GET", "/focus?all=1", nil).Body.Bytes(), &l)
	if len(l.Focus) != 1 || l.Focus[0].EndReason != pigtail.FocusEndDeadline || l.Focus[0].EndedAt == nil || !l.Focus[0].EndedAt.Equal(time.UnixMilli(ends.UnixMilli())) {
		t.Fatalf("after restore = %+v", l.Focus)
	}
	if lines, _ := st.ListLog("sub_p", time.Time{}); len(lines) != 1 {
		t.Fatalf("lines after restore = %+v", lines)
	}
}
