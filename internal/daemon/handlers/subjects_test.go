package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// testResponder is a minimal Responder writing JSON; mirrors the daemon's.
type testResponder struct{}

func (testResponder) JSON(w http.ResponseWriter, _ *http.Request, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func (testResponder) Error(w http.ResponseWriter, _ *http.Request, status int, code pigtail.ErrorCode, msg string, _ map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg})
}

// newTestRouter wires the subject routes against an in-memory store. Returns
// the router and the store so tests can seed/inspect directly.
func newTestRouter(t *testing.T) (*chi.Mux, *store.Store) {
	t.Helper()
	r, st, _ := newTestRouterSvc(t)
	return r, st
}

// newTestRouterSvc is newTestRouter with its Services, for tests that hook a callback.
func newTestRouterSvc(t *testing.T) (*chi.Mux, *store.Store, *Services) {
	t.Helper()
	return newTestRouterSvcAt(t, ":memory:")
}

// newTestRouterSvcAt is newTestRouterSvc on the store at path: a file lets a
// test reach the database through a second connection (fault injection).
func newTestRouterSvcAt(t *testing.T, path string) (*chi.Mux, *store.Store, *Services) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := &Services{Store: st}
	resp := testResponder{}
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, resp)
	return r, st, svc
}

func doJSON(t *testing.T, r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestThemeEndpoints(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)

	// Create — id minted server-side when absent.
	rec := doJSON(t, r, "POST", "/themes", pigtail.Theme{Name: "Meeting", Kind: "project", VaultLink: "Globex/Projects/rain-gauge"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var created pigtail.Theme
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" || created.ID[:4] != "thm_" {
		t.Fatalf("id not minted: %q", created.ID)
	}

	// List.
	rec = doJSON(t, r, "GET", "/themes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var list []pigtail.Theme
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "Meeting" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestUpdateThemeEndpoint(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	id := seedThemeAPI(t, r) // name "X", vault Globex/Projects/x

	// PATCH name + vault_link.
	rec := doJSON(t, r, "PATCH", "/themes/"+id, map[string]string{"name": "Renamed", "vault_link": "Globex/Projects/renamed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var th pigtail.Theme
	_ = json.Unmarshal(rec.Body.Bytes(), &th)
	if th.Name != "Renamed" || th.VaultLink != "Globex/Projects/renamed" {
		t.Fatalf("patch not applied: %+v", th)
	}
	if th.Kind != "project" {
		t.Fatalf("kind must be immutable: %+v", th)
	}

	// Empty body → 400.
	rec = doJSON(t, r, "PATCH", "/themes/"+id, map[string]string{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status = %d, want 400", rec.Code)
	}

	// Unknown theme → 404.
	rec = doJSON(t, r, "PATCH", "/themes/thm_missing", map[string]string{"name": "z"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing patch status = %d, want 404", rec.Code)
	}
}

func TestDeleteThemeEndpoint(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	id := seedThemeAPI(t, r)

	// Link a subject → DELETE must be refused with 409 and the count.
	rec := doJSON(t, r, "POST", "/subjects", pigtail.Subject{ThemeID: id, Title: "S"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed subject status = %d", rec.Code)
	}
	rec = doJSON(t, r, "DELETE", "/themes/"+id, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete-with-subject status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "1 subject") {
		t.Fatalf("409 message missing count: %s", rec.Body.String())
	}

	// Empty theme deletes cleanly.
	empty := seedThemeAPI(t, r)
	rec = doJSON(t, r, "DELETE", "/themes/"+empty, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete empty status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["id"] != empty {
		t.Fatalf("delete response id = %q, want %q", out["id"], empty)
	}

	// Unknown theme → 404.
	rec = doJSON(t, r, "DELETE", "/themes/thm_missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing status = %d, want 404", rec.Code)
	}
}

func TestSubjectThemeReassignEndpoint(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	src := seedThemeAPI(t, r)
	// A second theme as the reassignment target.
	rec := doJSON(t, r, "POST", "/themes", pigtail.Theme{Name: "Dst", Kind: "area", VaultLink: "Globex/Areas/dst"})
	var dst pigtail.Theme
	_ = json.Unmarshal(rec.Body.Bytes(), &dst)

	rec = doJSON(t, r, "POST", "/subjects", pigtail.Subject{ThemeID: src, Title: "Move me"})
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)

	// Reassign to dst.
	sub.ThemeID = dst.ID
	rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, sub)
	if rec.Code != http.StatusOK {
		t.Fatalf("reassign status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var updated pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.ThemeID != dst.ID {
		t.Fatalf("theme not reassigned: %+v", updated)
	}

	// Reassign to an unknown theme → 404.
	updated.ThemeID = "thm_ghost"
	rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, updated)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reassign to unknown theme status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func seedThemeAPI(t *testing.T, r http.Handler) string {
	t.Helper()
	rec := doJSON(t, r, "POST", "/themes", pigtail.Theme{Name: "X", Kind: "project", VaultLink: "Globex/Projects/x"})
	var th pigtail.Theme
	_ = json.Unmarshal(rec.Body.Bytes(), &th)
	return th.ID
}

func TestSlugify(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Checklist style": "checklist-style",
		"  Trim  Me  ":    "trim-me",
		"Accents: éàç!":   "accents-eac",
		"Already-Kebab":   "already-kebab",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubjectEndpoints(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	themeID := seedThemeAPI(t, r)

	// Create — server mints id, slug, status=inbox, timestamps.
	rec := doJSON(t, r, "POST", "/subjects", pigtail.Subject{ThemeID: themeID, Title: "Checklist style"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var created pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID[:4] != "sub_" || created.Slug != "checklist-style" || created.Status != pigtail.StatusInbox {
		t.Fatalf("defaults wrong: %+v", created)
	}

	// Get.
	rec = doJSON(t, r, "GET", "/subjects/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	rec = doJSON(t, r, "GET", "/subjects/sub_missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get missing status = %d, want 404", rec.Code)
	}

	// Update — change status + scores via full-struct POST.
	created.Status = pigtail.StatusBuilding
	created.Impact, created.Effort, created.Urgency = 5, 2, 4
	rec = doJSON(t, r, "POST", "/subjects/"+created.ID, created)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var updated pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Status != pigtail.StatusBuilding || updated.Impact != 5 {
		t.Fatalf("update not applied: %+v", updated)
	}

	// List + filter.
	rec = doJSON(t, r, "GET", "/subjects", nil)
	var all []pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &all)
	if len(all) != 1 {
		t.Fatalf("list len = %d", len(all))
	}
	rec = doJSON(t, r, "GET", "/subjects?status=inbox", nil)
	var none []pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &none)
	if len(none) != 0 {
		t.Fatalf("status filter should exclude building subject, got %d", len(none))
	}

	// Next — actionable subject returned.
	rec = doJSON(t, r, "GET", "/subjects/next", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("next status = %d", rec.Code)
	}
	var next pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &next)
	if next.ID != created.ID {
		t.Fatalf("next = %s, want %s", next.ID, created.ID)
	}
}

func TestPhaseAndLogEndpoints(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	themeID := seedThemeAPI(t, r)
	rec := doJSON(t, r, "POST", "/subjects", pigtail.Subject{ThemeID: themeID, Title: "Subj"})
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)

	// Create a phase.
	rec = doJSON(t, r, "POST", "/subjects/"+sub.ID+"/phases", pigtail.SubjectPhase{Kind: pigtail.PhaseResearch})
	if rec.Code != http.StatusCreated {
		t.Fatalf("phase create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var ph pigtail.SubjectPhase
	_ = json.Unmarshal(rec.Body.Bytes(), &ph)
	if ph.ID[:4] != "phs_" || ph.State != pigtail.PhasePending {
		t.Fatalf("phase defaults wrong: %+v", ph)
	}

	// Update the phase.
	rec = doJSON(t, r, "POST", "/phases/"+ph.ID, map[string]string{"state": pigtail.PhaseActive, "workspace_id": "ws_9"})
	if rec.Code != http.StatusOK {
		t.Fatalf("phase update status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// List phases.
	rec = doJSON(t, r, "GET", "/subjects/"+sub.ID+"/phases", nil)
	var phases []pigtail.SubjectPhase
	_ = json.Unmarshal(rec.Body.Bytes(), &phases)
	if len(phases) != 1 || phases[0].State != pigtail.PhaseActive || phases[0].WorkspaceID != "ws_9" {
		t.Fatalf("phase list wrong: %+v", phases)
	}

	// Append a log entry.
	rec = doJSON(t, r, "POST", "/subjects/"+sub.ID+"/log", pigtail.SubjectLogEntry{Kind: pigtail.LogIdea, Text: "an idea"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("log append status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// List log.
	rec = doJSON(t, r, "GET", "/subjects/"+sub.ID+"/log", nil)
	var logs []pigtail.SubjectLogEntry
	_ = json.Unmarshal(rec.Body.Bytes(), &logs)
	if len(logs) != 1 || logs[0].Text != "an idea" {
		t.Fatalf("log list wrong: %+v", logs)
	}
}

func TestSubjectWorktreeEndpoint(t *testing.T) {
	t.Parallel()
	r, st := newTestRouter(t)
	themeID := seedThemeAPI(t, r)
	rec := doJSON(t, r, "POST", "/subjects", pigtail.Subject{ThemeID: themeID, Title: "Subj"})
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)

	// No phase yet → 404.
	rec = doJSON(t, r, "GET", "/subjects/"+sub.ID+"/worktree", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Seed a project + workspace + a phase linking it, then expect 200 with the ws.
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_w", Name: "w", RootPath: "/tmp/w", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{},
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	ws := &pigtail.Workspace{ID: "ws_link", ProjectID: "prj_w", State: pigtail.StateBuilding, Path: "/tmp/x", BackendKind: "git"}
	if err := st.CreateWorkspace(ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.CreatePhase(&pigtail.SubjectPhase{
		ID: "phs_w", SubjectID: sub.ID, Kind: pigtail.PhaseImplement, State: pigtail.PhaseActive,
		WorkspaceID: ws.ID, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create phase: %v", err)
	}

	rec = doJSON(t, r, "GET", "/subjects/"+sub.ID+"/worktree", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got pigtail.Workspace
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ID != ws.ID {
		t.Fatalf("got %s want %s", got.ID, ws.ID)
	}
}

// TestThemeKindOngoing: POST /themes accepts "ongoing", tolerates the
// historical "area" (stored "ongoing") and refuses anything else.
func TestThemeKindOngoing(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	for _, in := range []string{"ongoing", "area"} {
		rec := doJSON(t, r, "POST", "/themes", pigtail.Theme{Name: "K-" + in, Kind: in, VaultLink: "Globex/Areas/k-" + in})
		if rec.Code != http.StatusCreated {
			t.Fatalf("kind=%s status = %d, body=%s", in, rec.Code, rec.Body.String())
		}
		var th pigtail.Theme
		_ = json.Unmarshal(rec.Body.Bytes(), &th)
		if th.Kind != "ongoing" {
			t.Fatalf("kind=%s stored as %q, want ongoing", in, th.Kind)
		}
	}
	rec := doJSON(t, r, "POST", "/themes", pigtail.Theme{Name: "E", Kind: "epic", VaultLink: "Globex/Areas/e"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `kind must be \"project\" or \"ongoing\"`) {
		t.Fatalf("kind=epic: %d %s", rec.Code, rec.Body.String())
	}
}

// getBlockedBy GETs /subjects/{id} and returns its raw body and blocked_by.
func getBlockedBy(t *testing.T, r http.Handler, id string) (string, []string, bool) {
	t.Helper()
	rec := doJSON(t, r, "GET", "/subjects/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, body=%s", id, rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	v, ok := raw["blocked_by"]
	if !ok {
		return rec.Body.String(), nil, false
	}
	var ids []string
	if err := json.Unmarshal(v, &ids); err != nil {
		t.Fatalf("blocked_by %s: %v", v, err)
	}
	return rec.Body.String(), ids, true
}

// seedDepSubjects creates A and B (B depends on A, A in status a) and C with
// no dependency, straight in the store.
func seedDepSubjects(t *testing.T, r http.Handler, st *store.Store, a string) *pigtail.Subject {
	t.Helper()
	themeID := seedThemeAPI(t, r)
	now := time.Now().UTC()
	mk := func(id, status string, deps ...string) *pigtail.Subject {
		s := &pigtail.Subject{ID: id, ThemeID: themeID, Title: id, Slug: id, Status: status, Deps: deps, Source: pigtail.SourceLocal, CreatedAt: now, UpdatedAt: now}
		if err := st.CreateSubject(s); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return s
	}
	subA := mk("A", a)
	mk("B", pigtail.StatusInbox, "A")
	mk("C", pigtail.StatusInbox)
	return subA
}

// TestSubjectBlockedBy: GET /subjects/:id carries blocked_by, the ids
// of the dependencies SatisfiesDeps does not satisfy, [] (never null) when
// none; GET /subjects never carries it.
func TestSubjectBlockedBy(t *testing.T) {
	t.Parallel()
	r, st := newTestRouter(t)
	subA := seedDepSubjects(t, r, st, pigtail.StatusParked)

	if _, ids, ok := getBlockedBy(t, r, "B"); !ok || len(ids) != 1 || ids[0] != "A" {
		t.Fatalf("A parked: blocked_by = %v (present %v), want [A]", ids, ok)
	}
	for _, s := range []string{pigtail.StatusDone, pigtail.StatusCaptured} {
		subA.Status = s
		if err := st.UpdateSubject(subA); err != nil {
			t.Fatal(err)
		}
		if _, ids, ok := getBlockedBy(t, r, "B"); !ok || ids == nil || len(ids) != 0 {
			t.Fatalf("A %s: blocked_by = %v (present %v), want []", s, ids, ok)
		}
	}
	body, ids, ok := getBlockedBy(t, r, "C")
	if !ok || ids == nil || len(ids) != 0 {
		t.Fatalf("C without deps: blocked_by = %v (present %v), want []", ids, ok)
	}
	if !strings.Contains(body, `"blocked_by":[]`) {
		t.Fatalf("raw body lacks \"blocked_by\":[]: %s", body)
	}
	if list := doJSON(t, r, "GET", "/subjects", nil).Body.String(); strings.Contains(list, "blocked_by") {
		t.Fatalf("GET /subjects carries blocked_by: %s", list)
	}
	// A client echoing back what it read (the CLI's set does) never gets a
	// stale blocked_by back: the key is read-only.
	var echo map[string]any
	_ = json.Unmarshal([]byte(body), &echo)
	echo["rank"] = 4
	if rec := doJSON(t, r, "POST", "/subjects/C", echo); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "blocked_by") {
		t.Fatalf("POST echo = %d, body=%s, want 200 without blocked_by", rec.Code, rec.Body.String())
	}
}

// TestSubjectBlockedByDropped: a dropped dependency no longer blocks,
// through the same SatisfiesDeps as subject next.
func TestSubjectBlockedByDropped(t *testing.T) {
	t.Parallel()
	r, st := newTestRouter(t)
	seedDepSubjects(t, r, st, pigtail.StatusDropped)
	if _, ids, ok := getBlockedBy(t, r, "B"); !ok || ids == nil || len(ids) != 0 {
		t.Fatalf("A dropped: blocked_by = %v (present %v), want []", ids, ok)
	}
}

const wantUnknownResearching = `unknown status "researching": use one of inbox, understanding, designing, building, done, parked, dropped, captured`

// wantInvalidStatus checks a 400 INVALID_REQUEST with the exact message.
func wantInvalidStatus(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body struct{ Code, Message string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusBadRequest || body.Code != string(pigtail.ErrInvalidRequest) || body.Message != wantUnknownResearching {
		t.Fatalf("got %d %s %q, want 400 INVALID_REQUEST %q", rec.Code, body.Code, body.Message, wantUnknownResearching)
	}
}

// TestUpdateSubjectRefusesUnknownStatus: POST /subjects/{id} with an
// unknown status answers 400 with the exact message; the status is unchanged.
func TestUpdateSubjectRefusesUnknownStatus(t *testing.T) {
	t.Parallel()
	r, st := newTestRouter(t)
	seedDepSubjects(t, r, st, pigtail.StatusUnderstanding)
	wantInvalidStatus(t, doJSON(t, r, "POST", "/subjects/A", map[string]any{"status": "researching"}))
	if got, err := st.GetSubject("A"); err != nil || got.Status != pigtail.StatusUnderstanding {
		t.Fatalf("status after refusal = %v, %v; want understanding", got, err)
	}
}

// TestCreateSubjectRefusesUnknownStatus: same refusal at creation;
// nothing is created.
func TestCreateSubjectRefusesUnknownStatus(t *testing.T) {
	t.Parallel()
	r, st := newTestRouter(t)
	themeID := seedThemeAPI(t, r)
	wantInvalidStatus(t, doJSON(t, r, "POST", "/subjects", map[string]any{"theme_id": themeID, "title": "x", "status": "researching"}))
	if subs, err := st.ListSubjects(store.SubjectFilter{}); err != nil || len(subs) != 0 {
		t.Fatalf("subjects after refusal = %d, %v; want none", len(subs), err)
	}
}

// TestListSubjectsRefusesUnknownStatus (M8): GET /subjects?status= validates
// the status like create and update; a known one still filters.
func TestListSubjectsRefusesUnknownStatus(t *testing.T) {
	t.Parallel()
	r, st := newTestRouter(t)
	seedDepSubjects(t, r, st, pigtail.StatusUnderstanding)
	wantInvalidStatus(t, doJSON(t, r, "GET", "/subjects?status=researching", nil))
	if rec := doJSON(t, r, "GET", "/subjects?status=understanding", nil); rec.Code != http.StatusOK {
		t.Fatalf("known status: %d %s", rec.Code, rec.Body.String())
	}
}

// doWeb is a request as the daemon's cookie path hands it on: master
// scope, the server's cross-scope opt-in, the web front mark.
func doWeb(t *testing.T, r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	switch b := body.(type) {
	case nil:
	case string:
		buf.WriteString(b)
	default:
		if err := json.NewEncoder(&buf).Encode(b); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req = req.WithContext(WithWeb(req.Context()))
	req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
	req.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func setStatus(t *testing.T, st *store.Store, id, status string) {
	t.Helper()
	sub, err := st.GetSubject(id)
	if err != nil {
		t.Fatal(err)
	}
	sub.Status = status
	if err := st.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
}

func assertForbiddenForWeb(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), string(pigtail.ErrForbiddenForWeb)) {
		t.Errorf("%s = %d %s, want 403 %s", what, rec.Code, rec.Body.String(), pigtail.ErrForbiddenForWeb)
	}
}

// The web front writes one field, the status, and only an open one or parked
// : done, dropped and captured never come from the browser.
func TestUpdateSubjectWebStatusOnly(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	setStatus(t, st, "sub_p", pigtail.StatusBuilding)
	for _, body := range []string{`{"status":"done"}`, `{"status":"dropped"}`, `{"status":"captured"}`,
		`{"title":"x"}`, `{"status":"parked","rank":3}`, `{}`, `{"status":3}`, `not json`} {
		assertForbiddenForWeb(t, "web front POST "+body, doWeb(t, r, "POST", "/subjects/sub_p", body))
		sub, _ := st.GetSubject("sub_p")
		if sub.Status != pigtail.StatusBuilding || sub.Title != "perso subject" || sub.Rank != 1 {
			t.Fatalf("after a refused %s: %+v", body, sub)
		}
	}
	rec := doWeb(t, r, "POST", "/subjects/sub_p", `{"status":"parked"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("web front parked = %d %s", rec.Code, rec.Body)
	}
	logs, err := st.ListLog("sub_p", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range logs {
		found = found || (e.Kind == pigtail.LogStatus && strings.HasPrefix(e.Text, "building → parked"))
	}
	if !found {
		t.Errorf("no status log line building → parked: %+v", logs)
	}
	// Outside the web front the contract is unchanged.
	if rec := doAs(t, r, perso, "POST", "/subjects/sub_p", map[string]any{"status": "done"}); rec.Code != http.StatusOK {
		t.Fatalf("CLI done = %d %s", rec.Code, rec.Body)
	}
}

// A closed subject (done, dropped, captured) is not reopened by the cookie.
func TestUpdateSubjectWebCurrentStatus(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	for _, cur := range []string{pigtail.StatusDone, pigtail.StatusDropped, pigtail.StatusCaptured} {
		setStatus(t, st, "sub_p", cur)
		assertForbiddenForWeb(t, "web front building on a "+cur+" subject", doWeb(t, r, "POST", "/subjects/sub_p", `{"status":"building"}`))
		if sub, _ := st.GetSubject("sub_p"); sub.Status != cur {
			t.Fatalf("a %s subject became %s", cur, sub.Status)
		}
	}
	// Positive control: from parked, the web front resumes.
	setStatus(t, st, "sub_p", pigtail.StatusParked)
	if rec := doWeb(t, r, "POST", "/subjects/sub_p", `{"status":"building"}`); rec.Code != http.StatusOK {
		t.Fatalf("web front building on a parked subject = %d %s", rec.Code, rec.Body)
	}
}
