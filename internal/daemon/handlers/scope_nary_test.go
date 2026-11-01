package handlers

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// naryFixture seeds one theme, subject and phase per vault root:
// thm_m/sub_m/phs_m (Perso/), thm_t (Globex/), thm_a (ACME/).
func naryFixture(t *testing.T) (*chi.Mux, *store.Store) {
	t.Helper()
	r, st := newTestRouter(t)
	now := time.UnixMilli(1).UTC()
	for k, link := range map[string]string{"m": "Perso/Areas/m", "t": "Globex/Projects/t", "a": "ACME/Projects/a"} {
		if err := st.CreateTheme(&pigtail.Theme{ID: "thm_" + k, Name: k, Kind: "project", VaultLink: link, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateSubject(&pigtail.Subject{ID: "sub_" + k, ThemeID: "thm_" + k, Title: k, Slug: k, Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_" + k, SubjectID: "sub_" + k, Kind: "research", State: "pending", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return r, st
}

// runNaryMatrix walks caller × target × verb. Expected, for 3 scopes:
//
//	caller \ target   master        globex     acme
//	master            rw            r (+cross w)   r (+cross w)
//	globex        404           rw             404
//	acme              404           404            rw
//	(no header)       = globex
func runNaryMatrix(t *testing.T, master string) {
	targetScope := map[string]string{"m": master, "t": "globex", "a": "acme"}
	callers := []caller{{}, {scope: "gx"}, {scope: "globex"}, {scope: "acme"}, {scope: "acme", cross: true},
		{scope: master}, {scope: master, cross: true}}
	type verb struct {
		name, method, path string
		write              bool
		body               any
		ok                 int
	}
	verbs := []verb{
		{"get subject", "GET", "/subjects/sub_%s", false, nil, 200},
		{"list phases", "GET", "/subjects/sub_%s/phases", false, nil, 200},
		{"update subject", "POST", "/subjects/sub_%s", true, map[string]any{"title": "x", "status": "understanding"}, 200},
		{"append log", "POST", "/subjects/sub_%s/log", true, map[string]any{"kind": "note", "text": "x"}, 201},
		{"patch theme", "PATCH", "/themes/thm_%s", true, map[string]any{"name": "renamed"}, 200},
	}
	for _, v := range verbs {
		for _, c := range callers {
			for target, ts := range targetScope {
				who := scope.Canonical(c.scope)
				if c.scope == "" {
					who = scope.Default()
				}
				want := v.ok
				switch {
				case !scope.SeesAll(who) && string(who) != ts:
					want = 404
				case v.write && string(who) != ts && !c.cross:
					want = 403
				}
				// Each cell on its own fixture, in parallel: the caller
				// stays serial, its table in place until every cell is done.
				t.Run(fmt.Sprintf("%s/as %s/on %s", v.name, c, ts), func(t *testing.T) {
					t.Parallel()
					r, _ := naryFixture(t)
					rec := doAs(t, r, c, v.method, fmt.Sprintf(v.path, target), v.body)
					if rec.Code != want {
						t.Errorf("[master=%s] %s as %s on %s: %d, want %d (%s)", master, v.name, c, ts, rec.Code, want, rec.Body.String())
					}
					if rec.Code == 403 && !strings.Contains(rec.Body.String(), string(pigtail.ErrScopeMismatch)) {
						t.Errorf("403 without SCOPE_MISMATCH: %s", rec.Body.String())
					}
				})
			}
		}
	}
}

func TestScopeMatrixThreeScopes(t *testing.T)   { scopetest.Three(t); runNaryMatrix(t, "perso") }
func TestScopeMatrixRenamedMaster(t *testing.T) { scopetest.RenamedMaster(t); runNaryMatrix(t, "root") }

// Lists: each non-master scope lists itself only; the master lists all three.
func TestScopeListsThreeScopes(t *testing.T) {
	scopetest.Three(t)
	r, _ := naryFixture(t)
	for c, want := range map[string]string{"globex": "t", "acme": "a", "perso": "a,m,t"} {
		rec := doAs(t, r, caller{scope: c}, "GET", "/subjects", nil)
		got := slugsOf(t, rec)
		if got != want {
			t.Errorf("GET /subjects as %s = %s, want %s", c, got, want)
		}
	}
	// the 400 of an unknown name, same body once the name is masked.
	forb := doAs(t, r, caller{scope: "acme"}, "GET", "/subjects?scope=globex", nil)
	unk := doAs(t, r, caller{scope: "acme"}, "GET", "/subjects?scope=nope", nil)
	if forb.Code != 400 || strings.ReplaceAll(forb.Body.String(), "globex", "<N>") != strings.ReplaceAll(unk.Body.String(), "nope", "<N>") {
		t.Errorf("acme listing globex = %d %s, want the unknown name's 400 (%s)", forb.Code, forb.Body.String(), unk.Body.String())
	}
}

// slugsOf decodes a GET /subjects answer into its sorted slugs, comma-joined.
func slugsOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var subs []pigtail.Subject
	if err := json.Unmarshal(rec.Body.Bytes(), &subs); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	slugs := make([]string, 0, len(subs))
	for _, s := range subs {
		slugs = append(slugs, s.Slug)
	}
	sort.Strings(slugs)
	return strings.Join(slugs, ",")
}

// Each hardcoded master test of the step 4 table, proven on its own route
// under a master that is not called perso. Red on the state of tasks 1-3:
// scope.Scope("perso") is still the literal "perso", so these sites treat root as a
// non-master; CanRead/CanWrite (Task 3) already read the table.
func TestHardcodedMasterSitesFollowTheTable(t *testing.T) {
	root, ts := caller{scope: "root"}, caller{scope: "globex"}

	// messages.go:259 — view = caller unless caller == scope.Scope("perso"): root's
	// view stays "root", and ListMessagesInScope keeps only conversations
	// whose peers are stored 'root' (store_messages.go:236-240).
	t.Run("GET /messages", func(t *testing.T) {
		scopetest.RenamedMaster(t)
		f := newRelayFixture(t)
		f.peers(t,
			pigtail.PeerSession{SessionID: "s-a1", Name: "acme-1", Scope: "acme", CellID: "ma"},
			pigtail.PeerSession{SessionID: "s-a2", Name: "acme-2", Scope: "acme", CellID: "ma"},
		)
		m := &pigtail.Message{ID: "msg_acme", ThreadID: "msg_acme", FromSession: "s-a1", ToSession: "s-a2",
			Kind: "info", Body: "acme note", State: "pending", CreatedAt: f.now, ExpiresAt: f.now.Add(time.Hour)}
		if err := f.st.CreateMessage(m); err != nil {
			t.Fatal(err)
		}
		if body := doAs(t, f.r, root, "GET", "/messages", nil).Body.String(); !strings.Contains(body, "msg_acme") {
			t.Errorf("GET /messages as root misses the acme conversation: %s", body)
		}
		if body := doAs(t, f.r, ts, "GET", "/messages", nil).Body.String(); strings.Contains(body, "msg_acme") {
			t.Errorf("GET /messages as globex lists the acme conversation: %s", body)
		}
	})

	// board.go:51 — ListFilter(scope.Scope("perso"), "acme"): CanRead("perso", acme)
	// is false once perso is not the master → ErrCrossScope → 400.
	t.Run("GET /board?scope=acme", func(t *testing.T) {
		scopetest.RenamedMaster(t)
		_, st := naryFixture(t)
		rec := httptest.NewRecorder()
		Board(&Services{Store: st})(rec, httptest.NewRequest("GET", "/board?scope=acme", nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "subjects · acme") {
			t.Errorf("/board?scope=acme = %d %s, want 200 labelled acme", rec.Code, rec.Body.String())
		}
	})

	// peers.go:55 — caller != scope.Scope("perso") is true for root: the stored
	// scope is blanked before UpsertPeer, so root cannot move a session.
	t.Run("POST /peers changes a stored scope", func(t *testing.T) {
		scopetest.RenamedMaster(t)
		f := newRelayFixture(t)
		f.peers(t, pigtail.PeerSession{SessionID: "s-a", Name: "acme-a", Scope: "acme", CellID: "ma"})
		move := pigtail.PeersRequest{Peers: []pigtail.PeerSession{{SessionID: "s-a", Scope: "globex", CellID: "mt"}}}
		if rec := doAs(t, f.r, ts, "POST", "/peers", move); rec.Code != 200 {
			t.Fatalf("POST /peers as globex = %d", rec.Code)
		}
		if p, _ := f.st.GetPeer("s-a"); p.Scope != "acme" {
			t.Fatalf("a non-master moved a session: scope = %q", p.Scope) // guard, green before and after
		}
		if rec := doAs(t, f.r, root, "POST", "/peers", move); rec.Code != 200 {
			t.Fatalf("POST /peers as root = %d", rec.Code)
		}
		if p, _ := f.st.GetPeer("s-a"); p.Scope != "globex" {
			t.Errorf("root could not move the session: scope = %q, want globex", p.Scope)
		}
	})

	// subjects_cmux.go:150 — mayWriteSubject on an orphan binding (subject
	// unknown to the store) answers c.scope == scope.Scope("perso"): false for root → 409.
	t.Run("replace an orphan cmux binding", func(t *testing.T) {
		scopetest.RenamedMaster(t)
		r, st := naryFixture(t)
		seedBinding(t, st, cmuxWS, "sub_gone")
		req := pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, Replace: true}
		if rec := doAs(t, r, ts, "POST", "/subjects/sub_t/cmux-workspaces", req); rec.Code != 409 {
			t.Fatalf("globex replacing an orphan binding = %d, want 409", rec.Code) // guard
		}
		if rec := doAs(t, r, root, "POST", "/subjects/sub_m/cmux-workspaces", req); rec.Code != 201 || boundTo(t, st, cmuxWS) != "sub_m" {
			t.Errorf("root replacing an orphan binding = %d %s, bound to %q; want 201 on sub_m", rec.Code, rec.Body.String(), boundTo(t, st, cmuxWS))
		}
	})
}

// Every fail-closed fallback lands on the table's master — root here — never
// on the name perso: an orphan is seen by root only, written by root without
// --cross-scope, and reported as root.
func TestOrphansFollowTheRenamedMaster(t *testing.T) {
	scopetest.RenamedMaster(t)
	r, st := naryFixture(t)
	now := time.UnixMilli(1).UTC()
	// Orphan subject: its theme row is gone (DeleteTheme does not cascade).
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_o", Name: "o", Kind: "project", VaultLink: "Globex/Projects/o", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_o", ThemeID: "thm_o", Title: "o", Slug: "o", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTheme("thm_o"); err != nil {
		t.Fatal(err)
	}
	// Orphan phase: its subject never existed (foreign keys are not enforced).
	if err := st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_x", SubjectID: "sub_gone", Kind: "research", State: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Theme whose link no root claims, created without a scope (store.go:703).
	th := &pigtail.Theme{ID: "thm_z", Name: "z", Kind: "area", VaultLink: "ZZZ/x", CreatedAt: now}
	if err := st.CreateTheme(th); err != nil {
		t.Fatal(err)
	}
	if th.Scope != "root" {
		t.Errorf("CreateTheme fallback scope = %q, want root", th.Scope)
	}
	root, ts, acme := caller{scope: "root"}, caller{scope: "globex"}, caller{scope: "acme"}
	for _, c := range []caller{ts, acme} {
		for _, q := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/subjects/sub_o", nil},
			{"POST", "/phases/phs_x", map[string]any{"state": "done"}},
			{"POST", "/journal", jBody("o", "", "sub_o")},
		} {
			if rec := doAs(t, r, c, q.method, q.path, q.body); rec.Code != 404 {
				t.Errorf("%s %s as %s = %d, want 404", q.method, q.path, c, rec.Code)
			}
		}
		if got := slugsOf(t, doAs(t, r, c, "GET", "/subjects", nil)); strings.Contains(got, "o") {
			t.Errorf("GET /subjects as %s lists the orphan: %s", c, got)
		}
	}
	// root: it reads them as root, and writes them WITHOUT --cross-scope
	// (base: the fallback says perso → 403 for a root caller writing perso).
	rec := doAs(t, r, root, "GET", "/subjects/sub_o", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"scope":"root"`) {
		t.Errorf("GET orphan as root = %d %s, want 200 scope root", rec.Code, rec.Body.String())
	}
	if got := slugsOf(t, doAs(t, r, root, "GET", "/subjects?scope=root", nil)); !strings.Contains(got, "o") {
		t.Errorf("GET /subjects?scope=root misses the orphan: %s", got)
	}
	if rec := doAs(t, r, root, "POST", "/phases/phs_x", map[string]any{"state": "done"}); rec.Code != 200 {
		t.Errorf("orphan phase as root = %d %s, want 200", rec.Code, rec.Body.String())
	}
	rec = doAs(t, r, root, "POST", "/journal", jBody("o", "", "sub_o"))
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"scope":"root"`) {
		t.Errorf("journal on orphan as root = %d %s, want 201 scope root", rec.Code, rec.Body.String())
	}
}

func TestScopeErrorsNameOnlyNameableScopes(t *testing.T) {
	scopetest.Three(t)
	r, _ := naryFixture(t)
	others := map[string][]string{
		"acme":   {"globex", "perso", "Globex/", "Perso/"},
		"globex": {"acme", "perso", "ACME/", "Perso/"},
	}
	for c, forbidden := range others {
		for _, q := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/subjects?scope=nope", nil},
			{"GET", "/subjects/next?scope=nope", nil},
			{"GET", "/journal?scope=nope", nil},
			{"POST", "/journal", map[string]any{"id": "j_nope", "scope": "nope", "kind": "gate", "summary": "s", "source": "pigtail:x"}},
			{"POST", "/themes", map[string]any{"name": "z", "kind": "area", "vault_link": "ZZZ/x"}},
		} {
			rec := doAs(t, r, caller{scope: c}, q.method, q.path, q.body)
			if rec.Code != 400 {
				t.Errorf("%s %s as %s = %d, want 400", q.method, q.path, c, rec.Code)
			}
			for _, w := range forbidden {
				if strings.Contains(rec.Body.String(), w) {
					t.Errorf("%s %s as %s names %q: %s", q.method, q.path, c, w, rec.Body.String())
				}
			}
		}
	}
	// The master is told every name; an unreadable header is told none.
	if body := doAs(t, r, caller{scope: "perso"}, "GET", "/subjects?scope=nope", nil).Body.String(); !strings.Contains(body, "acme") || !strings.Contains(body, "globex") {
		t.Errorf("master error lists %s, want every scope", body)
	}
	if body := doAs(t, r, caller{scope: "nope"}, "GET", "/subjects", nil).Body.String(); strings.Contains(body, "acme") || strings.Contains(body, "globex") || strings.Contains(body, "perso") {
		t.Errorf("unreadable header answer names a scope: %s", body)
	}
}

// TestNonMasterForbiddenScopeAnswersLikeUnknown: to a non-master caller,
// a declared scope it cannot read is indistinguishable from a name that does
// not exist — same status, same body once the echoed name is masked.
func TestNonMasterForbiddenScopeAnswersLikeUnknown(t *testing.T) {
	scopetest.Three(t)
	r, _ := naryFixture(t)
	type probe struct {
		what, method, path string
		body               func(name string) any
	}
	q := func(p string) probe { return probe{"GET " + p, "GET", p + "?scope=%s", nil} }
	for _, c := range []struct {
		caller         string
		forbidden      []string // names (aliases included) of scopes it cannot read
		forbiddenRoots []string
		ownTheme       string
		ownSubject     string
	}{
		{"acme", []string{"perso", "globex", "gx"}, []string{"Perso", "Globex"}, "thm_a", "sub_a"},
		{"globex", []string{"perso", "acme"}, []string{"Perso", "ACME"}, "thm_t", "sub_t"},
	} {
		probes := []probe{
			q("/subjects"), q("/subjects/next"), q("/themes"), q("/journal"),
			{"POST /journal (no subject)", "POST", "/journal", func(n string) any {
				return map[string]any{"id": "j_" + n, "scope": n, "kind": "gate", "summary": "s", "source": "pigtail:x"}
			}},
			// On its OWN subject: the head canonicalisation must answer before
			// "differs from the subject's scope", which would name the scope.
			{"POST /journal (own subject_id)", "POST", "/journal", func(n string) any {
				return map[string]any{"id": "js_" + n, "subject_id": c.ownSubject, "scope": n, "kind": "gate", "summary": "s", "source": "pigtail:x"}
			}},
			{"POST /themes", "POST", "/themes", func(n string) any { return map[string]any{"name": "z", "kind": "area", "vault_link": n + "/x"} }},
		}
		send := func(p probe, name string) (int, string) {
			path, body := p.path, any(nil)
			if p.body != nil {
				body = p.body(name)
			} else {
				path = fmt.Sprintf(p.path, name)
			}
			rec := doAs(t, r, caller{scope: c.caller}, p.method, path, body)
			return rec.Code, strings.ReplaceAll(rec.Body.String(), name, "<NAME>")
		}
		for _, p := range probes {
			names := c.forbidden
			if p.path == "/themes" && p.method == "POST" {
				names = c.forbiddenRoots
			}
			wantCode, wantBody := send(p, map[bool]string{true: "ZZZ", false: "nope"}[p.method == "POST" && p.path == "/themes"])
			if wantCode != 400 {
				t.Fatalf("%s as %s, unknown name = %d, want 400", p.what, c.caller, wantCode)
			}
			for _, n := range names {
				if code, body := send(p, n); code != wantCode || body != wantBody {
					t.Errorf("%s as %s: %q → %d %s, unknown → %d %s", p.what, c.caller, n, code, body, wantCode, wantBody)
				}
			}
		}
		// PATCH: moving its own theme under another scope's root reads like an unknown root.
		patch := func(root string) (int, string) {
			rec := doAs(t, r, caller{scope: c.caller}, "PATCH", "/themes/"+c.ownTheme, map[string]any{"vault_link": root + "/x"})
			return rec.Code, strings.ReplaceAll(rec.Body.String(), root, "<NAME>")
		}
		wantCode, wantBody := patch("ZZZ")
		for _, root := range c.forbiddenRoots {
			if code, body := patch(root); code != wantCode || body != wantBody {
				t.Errorf("PATCH as %s to %s/: %d %s, unknown root → %d %s", c.caller, root, code, body, wantCode, wantBody)
			}
		}
	}
	// The master keeps the full answers: a declared name it may read is not an error.
	if rec := doAs(t, r, caller{scope: "perso"}, "GET", "/subjects?scope=acme", nil); rec.Code != 200 {
		t.Errorf("master ?scope=acme = %d, want 200", rec.Code)
	}
	// "all" names no scope: a non-master still gets 403 for it.
	if rec := doAs(t, r, caller{scope: "acme"}, "GET", "/subjects?scope=all", nil); rec.Code != 403 {
		t.Errorf("acme ?scope=all = %d, want 403", rec.Code)
	}
}
