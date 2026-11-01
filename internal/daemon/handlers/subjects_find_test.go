package handlers

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// findFixtureAt is suggestFixture on a database file, so a test can break the
// schema through a second connection (fault injection, modelled on
// newTestRouterSvcAt's own comment and failInserts in relay_respawn_test.go).
func findFixtureAt(t *testing.T) (*chi.Mux, *store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pigtail.db")
	r, st, _ := newTestRouterSvcAt(t, path)
	now := time.UnixMilli(1).UTC()
	for _, th := range []*pigtail.Theme{
		{ID: "thm_t", Name: "T", Kind: "project", VaultLink: "Globex/Projects/t", CreatedAt: now},
		{ID: "thm_p", Name: "P", Kind: "area", VaultLink: "Perso/Areas/p", CreatedAt: now},
	} {
		if err := st.CreateTheme(th); err != nil {
			t.Fatal(err)
		}
	}
	return r, st, path
}

// failUpdates makes every UPDATE of table fail from a second connection (the
// counterpart of failInserts).
func failUpdates(t *testing.T, path, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER test_fail_upd_` + table + ` BEFORE UPDATE ON ` + table +
		` BEGIN SELECT RAISE(ABORT, 'test: ` + table + ` update refused'); END`); err != nil {
		t.Fatal(err)
	}
}

func findAs(t *testing.T, r http.Handler, c caller, req pigtail.SubjectFindRequest) (*httptest.ResponseRecorder, pigtail.SubjectFindResult) {
	t.Helper()
	rec := doAs(t, r, c, "POST", "/subjects/find", req)
	var res pigtail.SubjectFindResult
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
	}
	return rec, res
}

func addFindLog(t *testing.T, st *store.Store, subjectID, kind string, ts int64) {
	t.Helper()
	suggestLogSeq++
	if err := st.AppendLog(&pigtail.SubjectLogEntry{ID: "log_find_" + subjectID + "_" + kind + "_" + time.UnixMilli(ts).Format("150405.000") + "_" + string(rune('a'+suggestLogSeq%26)),
		SubjectID: subjectID, Timestamp: time.UnixMilli(ts).UTC(), Kind: kind, Text: "x"}); err != nil {
		t.Fatal(err)
	}
}

// findJournalByID is the journal line of one search (or pick) by its id: the
// list order (ts DESC, id DESC) is no contract for two lines in the same ms.
func findJournalByID(t *testing.T, st *store.Store, kind, fid string) *pigtail.JournalEntry {
	t.Helper()
	rows, err := st.ListJournal(store.JournalFilter{Kind: kind, IDs: []string{kind + ":" + fid}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("journal %s:%s = %+v, want exactly one line", kind, fid, rows)
	}
	return &rows[0]
}

func findJournalRows(t *testing.T, st *store.Store, kind string) []pigtail.JournalEntry {
	t.Helper()
	rows, err := st.ListJournal(store.JournalFilter{Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestFindSubjectsRanked(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_notif", "thm_p", "Notifications push", pigtail.StatusBuilding)
	addSuggestSubject(t, st, "sub_done", "thm_p", "Notifications email", pigtail.StatusDone)
	addSuggestSubject(t, st, "sub_other", "thm_p", "Board columns", pigtail.StatusBuilding)
	addFindLog(t, st, "sub_notif", "phase", 5000)
	addFindLog(t, st, "sub_notif", "message", 9000) // not an owner kind
	rec, res := findAs(t, r, perso, pigtail.SubjectFindRequest{Query: "notif"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(res.FindID, pigtail.PrefixSubjectFind) || res.Query != "notif" || res.Total != 2 || res.Elsewhere != 0 || len(res.Candidates) != 2 {
		t.Fatalf("got %+v", res)
	}
	a, b := res.Candidates[0], res.Candidates[1]
	if a.SubjectID != "sub_notif" || a.Theme != "P" || a.ThemeID != "thm_p" || a.Status != pigtail.StatusBuilding || a.Closed || !a.Writable ||
		a.LastActivity == nil || !a.LastActivity.Equal(time.UnixMilli(5000)) || !reflect.DeepEqual(a.Matched, []string{"notif"}) || a.ShortTitle == "" {
		t.Fatalf("first = %+v", a)
	}
	if b.SubjectID != "sub_done" || !b.Closed || b.LastActivity != nil {
		t.Fatalf("second = %+v", b)
	}
	if strings.Contains(rec.Body.String(), `"last_activity":null`) || !strings.Contains(rec.Body.String(), `"matched":["notif"]`) {
		t.Fatalf("body shape: %s", rec.Body)
	}
	// limit truncates candidates, never total.
	_, res = findAs(t, r, perso, pigtail.SubjectFindRequest{Query: "notif", Limit: 1})
	if len(res.Candidates) != 1 || res.Total != 2 {
		t.Fatalf("limit 1: %+v", res)
	}
}

// Modelled on TestSuggestScopePartition: the GET probe never calls a POST.
func TestFindScopePartition(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_secret", "thm_p", "Zqx secret notifications", pigtail.StatusBuilding)
	addSuggestSubject(t, st, "sub_work", "thm_t", "Notifications work", pigtail.StatusBuilding)
	rec, res := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications"})
	if len(res.Candidates) != 1 || res.Candidates[0].SubjectID != "sub_work" || res.Elsewhere != 1 || res.Total != 1 {
		t.Fatalf("work: got %+v, want sub_work only, elsewhere 1", res)
	}
	if b := rec.Body.String(); strings.Contains(b, "sub_secret") || strings.Contains(b, "Zqx") {
		t.Fatalf("work body leaks the perso subject: %s", b)
	}
	f, err := st.GetSubjectFind(res.FindID)
	if err != nil || f.Scope != string(scope.Canonical("gx")) || len(f.Candidates) != 1 || f.Candidates[0].SubjectID != "sub_work" {
		t.Fatalf("stored work row = %+v, %v", f, err)
	}
	for _, j := range findJournalRows(t, st, pigtail.JournalKindSubjectFind) {
		if strings.Contains(j.Summary, "sub_secret") || strings.Contains(j.Summary, "Zqx") {
			t.Fatalf("journal line leaks the perso subject: %+v", j)
		}
	}
	_, res = findAs(t, r, perso, pigtail.SubjectFindRequest{Query: "notifications"})
	var workCand *pigtail.SubjectFindCandidate
	for i := range res.Candidates {
		if res.Candidates[i].SubjectID == "sub_work" {
			workCand = &res.Candidates[i]
		}
	}
	if len(res.Candidates) != 2 || res.Elsewhere != 0 || workCand == nil || workCand.Writable {
		t.Fatalf("perso: got %+v, want both, sub_work not writable", res)
	}
}

func TestFindRecordsRowAndJournal(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_n", "thm_t", "Notifications push", pigtail.StatusBuilding)
	_, res := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications", WorkspaceID: strings.ToLower(cmuxWS)})
	f, err := st.GetSubjectFind(res.FindID)
	if err != nil || f.Query != "notifications" || f.CmuxWorkspaceID != cmuxWS || f.PickedSubjectID != "" {
		t.Fatalf("row = %+v, %v", f, err)
	}
	j := findJournalByID(t, st, pigtail.JournalKindSubjectFind, res.FindID)
	if j.Source != "pigtail:subject-find/"+res.FindID ||
		j.Summary != "recherche « notifications » : 1 candidat(s), tête sub_n" || j.SubjectID != "" {
		t.Fatalf("journal = %+v", j)
	}
	// No candidate is still a recorded search; elsewhere shows in the summary.
	addSuggestSubject(t, st, "sub_p", "thm_p", "Zqx hidden", pigtail.StatusBuilding)
	_, res = findAs(t, r, work, pigtail.SubjectFindRequest{Query: "zqx"})
	if len(res.Candidates) != 0 || res.Elsewhere != 1 || res.FindID == "" {
		t.Fatalf("no candidate: %+v", res)
	}
	if j = findJournalByID(t, st, pigtail.JournalKindSubjectFind, res.FindID); j.Summary != "recherche « zqx » : aucun candidat (+1 ailleurs)" {
		t.Fatalf("journal = %+v", j)
	}
	// A long query is clipped in the summary only; the tail survives whole.
	long := strings.Repeat("notifications ", 33) // 461 bytes
	_, res = findAs(t, r, work, pigtail.SubjectFindRequest{Query: long})
	j = findJournalByID(t, st, pigtail.JournalKindSubjectFind, res.FindID)
	if f, err = st.GetSubjectFind(res.FindID); err != nil || len(f.Query) < 300 {
		t.Fatalf("stored query not whole: %d bytes, %v", len(f.Query), err)
	}
	wantTail := " » : 1 candidat(s), tête sub_n"
	if !strings.HasPrefix(j.Summary, "recherche « ") || !strings.HasSuffix(j.Summary, "…"+wantTail) ||
		len([]rune(j.Summary)) > pigtail.JournalSummaryMax {
		t.Fatalf("long query summary = %q", j.Summary)
	}
}

func TestFindRejects(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_n", "thm_t", "Notifications push", pigtail.StatusBuilding)
	for name, c := range map[string]struct {
		body any
		msg  string
	}{
		"empty":     {pigtail.SubjectFindRequest{Query: "  \n "}, "query is required"},
		"stopwords": {pigtail.SubjectFindRequest{Query: "le de a"}, "query has no searchable word (stop words and one-letter words are ignored)"},
		"too long":  {pigtail.SubjectFindRequest{Query: strings.Repeat("abcd ", 101)}, "query too long (504 bytes, max 500)"},
		"limit":     {pigtail.SubjectFindRequest{Query: "notif", Limit: 21}, "limit must be 1..20"},
		"negative":  {pigtail.SubjectFindRequest{Query: "notif", Limit: -1}, "limit must be 1..20"},
		"workspace": {pigtail.SubjectFindRequest{Query: "notif", WorkspaceID: "workspace:3"}, ""},
		"bad json":  {"not an object", "invalid JSON"},
	} {
		rec := doAs(t, r, work, "POST", "/subjects/find", c.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.msg) {
			t.Errorf("%s: %d %s, want 400 %q", name, rec.Code, rec.Body, c.msg)
		}
	}
	if rows := findJournalRows(t, st, pigtail.JournalKindSubjectFind); len(rows) != 0 {
		t.Fatalf("a refused search was recorded: %+v", rows)
	}
}

// Fault injection: a search that cannot be recorded answers 500 with no
// result and leaves no row and no journal line (never an unrecorded search).
func TestFindNotRecordedIs500(t *testing.T) {
	r, st, path := findFixtureAt(t)
	addSuggestSubject(t, st, "sub_n", "thm_t", "Notifications push", pigtail.StatusBuilding)
	failInserts(t, path, "subject_finds")
	rec, res := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications"})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "subject find not recorded") {
		t.Fatalf("status %d %s, want 500 subject find not recorded", rec.Code, rec.Body)
	}
	if res.FindID != "" || strings.Contains(rec.Body.String(), "sub_n") || strings.Contains(rec.Body.String(), "candidates") {
		t.Fatalf("a result came out of an unrecorded search: %s", rec.Body)
	}
	if rows := findJournalRows(t, st, pigtail.JournalKindSubjectFind); len(rows) != 0 {
		t.Fatalf("journal line written without its row: %+v", rows)
	}
}

func TestFindQueryWhitespaceNormalised(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_n", "thm_t", "Notifications push", pigtail.StatusBuilding)
	rec, res := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "  notifications\n\tpush\r\n  "})
	if rec.Code != http.StatusOK || res.Query != "notifications push" {
		t.Fatalf("%d %+v", rec.Code, res)
	}
	if f, err := st.GetSubjectFind(res.FindID); err != nil || f.Query != "notifications push" {
		t.Fatalf("row = %+v, %v", f, err)
	}
}

func pickAs(t *testing.T, r http.Handler, c caller, fid, subjectID string) (*httptest.ResponseRecorder, pigtail.SubjectFindPick) {
	t.Helper()
	rec := doAs(t, r, c, "POST", "/subject-finds/"+fid+"/pick", pigtail.SubjectFindPickRequest{SubjectID: subjectID})
	var p pigtail.SubjectFindPick
	if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
	}
	return rec, p
}

func TestPickSubjectFind(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_a", "thm_t", "Notifications push", pigtail.StatusBuilding)
	addSuggestSubject(t, st, "sub_b", "thm_t", "Notifications email", pigtail.StatusBuilding)
	_, first := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "badge", WorkspaceID: cmuxWS})
	_, second := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications email", WorkspaceID: cmuxWS})
	rec, p := pickAs(t, r, work, second.FindID, "sub_b")
	if rec.Code != http.StatusCreated || !p.Created || p.Rank != 1 || p.Searches != 2 || p.Candidates != len(second.Candidates) {
		t.Fatalf("pick = %d %+v; want 201, rank 1, 2 searches", rec.Code, p)
	}
	if f, _ := st.GetSubjectFind(second.FindID); f.PickedSubjectID != "sub_b" || f.PickedAt.IsZero() {
		t.Fatalf("row = %+v", f)
	}
	if rec, p = pickAs(t, r, work, second.FindID, "sub_b"); rec.Code != http.StatusOK || p.Created {
		t.Fatalf("same pick = %d %+v; want 200 not created", rec.Code, p)
	}
	if rec, _ = pickAs(t, r, work, second.FindID, "sub_a"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "subject find already picked for another subject") {
		t.Fatalf("other subject = %d %s; want 409", rec.Code, rec.Body)
	}
	rows := findJournalRows(t, st, pigtail.JournalKindSubjectFindPick)
	if len(rows) != 1 || rows[0].SubjectID != "sub_b" || rows[0].ID != "subject-find-pick:"+second.FindID {
		t.Fatalf("pick lines = %+v", rows)
	}
	for name, c := range map[string]struct {
		fid, sub string
		code     int
		msg      string
	}{
		"unknown find":    {"sf_0000000000000000", "sub_a", http.StatusNotFound, "no such subject find"},
		"unknown subject": {first.FindID, "sub_none", http.StatusNotFound, "no such subject"},
		"no subject":      {first.FindID, "", http.StatusBadRequest, "subject_id is required"},
	} {
		if rec, _ := pickAs(t, r, work, c.fid, c.sub); rec.Code != c.code || !strings.Contains(rec.Body.String(), c.msg) {
			t.Errorf("%s: %d %s, want %d %q", name, rec.Code, rec.Body, c.code, c.msg)
		}
	}
}

// A search of another scope reads like an unknown one (same body); the master
// needs the opt-in; a pick line never carries a subject_id of another scope.
func TestPickSubjectFindScope(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_t", "thm_t", "Notifications work", pigtail.StatusBuilding)
	addSuggestSubject(t, st, "sub_p", "thm_p", "Notifications perso", pigtail.StatusBuilding)
	_, persoFind := findAs(t, r, perso, pigtail.SubjectFindRequest{Query: "notifications"})
	_, workFind := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications"})
	unknown, _ := pickAs(t, r, work, "sf_0000000000000000", "sub_t")
	other, _ := pickAs(t, r, work, persoFind.FindID, "sub_t")
	if other.Code != http.StatusNotFound || other.Body.String() != unknown.Body.String() {
		t.Fatalf("work on a perso find = %d %s; want the unknown body %s", other.Code, other.Body, unknown.Body)
	}
	if rec, _ := pickAs(t, r, perso, workFind.FindID, "sub_t"); rec.Code != http.StatusForbidden {
		t.Fatalf("master without opt-in = %d %s; want 403", rec.Code, rec.Body)
	}
	// The master with the opt-in cannot tie a perso subject to a work search: the
	// search's scope cannot read it, so the answer is the unknown subject's 404,
	// before any write.
	unknownSub, _ := pickAs(t, r, persoCross, workFind.FindID, "sub_none")
	if rec, _ := pickAs(t, r, persoCross, workFind.FindID, "sub_p"); rec.Code != http.StatusNotFound || rec.Body.String() != unknownSub.Body.String() {
		t.Fatalf("persoCross on a work find with sub_p = %d %s; want the unknown subject's 404 %s", rec.Code, rec.Body, unknownSub.Body)
	}
	if f, _ := st.GetSubjectFind(workFind.FindID); f.PickedSubjectID != "" {
		t.Fatalf("the refused pick was stored: %+v", f)
	}
	if n := len(findJournalRows(t, st, pigtail.JournalKindSubjectFindPick)); n != 0 {
		t.Fatalf("a refused pick left %d journal line(s)", n)
	}
	if rec, _ := pickAs(t, r, persoCross, workFind.FindID, "sub_t"); rec.Code != http.StatusCreated {
		t.Fatalf("master with opt-in = %d %s; want 201", rec.Code, rec.Body)
	}
	// The master picks a work subject on its own search: the line stays in its scope, without subject_id.
	if rec, _ := pickAs(t, r, perso, persoFind.FindID, "sub_t"); rec.Code != http.StatusCreated {
		t.Fatalf("perso picks work subject = %d %s", rec.Code, rec.Body)
	}
	for _, j := range findJournalRows(t, st, pigtail.JournalKindSubjectFindPick) {
		if j.SubjectID != "" && j.Scope != string(scope.Canonical("gx")) {
			t.Fatalf("pick line %+v carries a subject of another scope", j)
		}
	}
	rows, _ := st.ListJournal(store.JournalFilter{Scope: scope.Canonical("gx")})
	for _, j := range rows {
		if strings.Contains(j.Summary, "sub_p") || j.SubjectID == "sub_p" {
			t.Fatalf("work journal names the perso subject: %+v", j)
		}
	}
	if f, _ := st.GetSubjectFind(workFind.FindID); strings.Contains(f.PickedSubjectID, "sub_p") {
		t.Fatalf("work search row names the perso subject: %+v", f)
	}
}

// The summaries are the indicator's contract (docs/wire-protocol.md § Subject find).
func TestPickJournalSummaryFormat(t *testing.T) {
	for _, c := range []struct {
		p    pigtail.SubjectFindPick
		want string
	}{
		{pigtail.SubjectFindPick{FindID: "sf_1", SubjectID: "sub_a", Rank: 1, Candidates: 3, Searches: 2}, "recherche sf_1 : sub_a choisi au rang 1/3 après 2 recherche(s)"},
		{pigtail.SubjectFindPick{FindID: "sf_1", SubjectID: "sub_a", Rank: 0, Candidates: 5, Searches: 1}, "recherche sf_1 : sub_a choisi hors liste (5 candidat(s)) après 1 recherche(s)"},
	} {
		got := pickSummary(c.p)
		if got != c.want {
			t.Errorf("pickSummary = %q, want %q", got, c.want)
		}
		if !regexp.MustCompile(`après ([0-9]+) recherche`).MatchString(got) {
			t.Errorf("%q: the jq recipe would not read the count", got)
		}
	}
}

func attachAs(t *testing.T, r http.Handler, c caller, subjectID string, req pigtail.AttachCmuxWorkspaceRequest) (*httptest.ResponseRecorder, pigtail.SubjectCmuxBinding) {
	t.Helper()
	rec := doAs(t, r, c, "POST", "/subjects/"+subjectID+"/cmux-workspaces", req)
	var b pigtail.SubjectCmuxBinding
	if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
	}
	return rec, b
}

func TestAttachWithFind(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_a", "thm_t", "Notifications push", pigtail.StatusBuilding)
	_, res := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications", WorkspaceID: cmuxWS})
	rec, b := attachAs(t, r, work, "sub_a", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, FindID: res.FindID})
	if rec.Code != http.StatusCreated || b.FindID != res.FindID || b.SubjectID != "sub_a" {
		t.Fatalf("attach = %d %+v", rec.Code, b)
	}
	if f, _ := st.GetSubjectFind(res.FindID); f.PickedSubjectID != "sub_a" {
		t.Fatalf("pick not recorded: %+v", f)
	}
	// Idempotent attach with the same find: 200, still picked, one pick line.
	if rec, b = attachAs(t, r, work, "sub_a", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, FindID: res.FindID}); rec.Code != http.StatusOK || b.FindID != res.FindID {
		t.Fatalf("again = %d %+v", rec.Code, b)
	}
	if n := len(findJournalRows(t, st, pigtail.JournalKindSubjectFindPick)); n != 1 {
		t.Fatalf("pick lines = %d, want 1", n)
	}
	// Without find_id the answer has no find_id key.
	rec, _ = attachAs(t, r, work, "sub_a", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS})
	if strings.Contains(rec.Body.String(), "find_id") {
		t.Fatalf("plain attach carries find_id: %s", rec.Body)
	}
}

func TestAttachWithFindRefusals(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_a", "thm_t", "Notifications push", pigtail.StatusBuilding)
	addSuggestSubject(t, st, "sub_b", "thm_t", "Notifications email", pigtail.StatusBuilding)
	addSuggestSubject(t, st, "sub_p", "thm_p", "Notifications perso", pigtail.StatusBuilding)
	_, persoFind := findAs(t, r, perso, pigtail.SubjectFindRequest{Query: "notifications"})
	_, workFind := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications"})
	if rec, _ := pickAs(t, r, work, workFind.FindID, "sub_b"); rec.Code != http.StatusCreated {
		t.Fatalf("seed pick: %d %s", rec.Code, rec.Body)
	}
	for name, c := range map[string]struct {
		fid  string
		code int
		msg  string
	}{
		"unknown":      {"sf_0000000000000000", http.StatusNotFound, "no such subject find"},
		"other scope":  {persoFind.FindID, http.StatusNotFound, "no such subject find"},
		"picked for b": {workFind.FindID, http.StatusConflict, "subject find already picked for another subject"},
	} {
		rec, _ := attachAs(t, r, work, "sub_a", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS2, FindID: c.fid})
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.msg) {
			t.Errorf("%s: %d %s, want %d %q", name, rec.Code, rec.Body, c.code, c.msg)
		}
		if _, err := st.GetCmuxBinding(cmuxWS2); err == nil {
			t.Fatalf("%s: the workspace was linked despite the refusal", name)
		}
	}
	// The master with the opt-in cannot link a perso subject on a work search:
	// the search's scope cannot read it (404, nothing linked, nothing written).
	_, workFind2 := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications"})
	rec, _ := attachAs(t, r, persoCross, "sub_p", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS2, FindID: workFind2.FindID})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such subject") {
		t.Fatalf("persoCross attach of sub_p on a work find = %d %s; want 404 no such subject", rec.Code, rec.Body)
	}
	if _, err := st.GetCmuxBinding(cmuxWS2); err == nil {
		t.Fatal("the workspace was linked despite the refusal")
	}
	if f, _ := st.GetSubjectFind(workFind2.FindID); f.PickedSubjectID != "" {
		t.Fatalf("the refused attach picked: %+v", f)
	}
	all, _ := st.ListJournal(store.JournalFilter{Scope: scope.Canonical("gx")})
	for _, j := range all {
		if strings.Contains(j.Summary, "sub_p") || j.SubjectID == "sub_p" {
			t.Fatalf("work journal names the perso subject: %+v", j)
		}
	}
}

// Fault injection: the workspace is linked, the pick write fails — the answer
// is a 500 saying so; the link stands, no pick and no pick line were written.
func TestAttachWithFindPickFailure(t *testing.T) {
	r, st, path := findFixtureAt(t)
	addSuggestSubject(t, st, "sub_a", "thm_t", "Notifications push", pigtail.StatusBuilding)
	_, res := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications", WorkspaceID: cmuxWS})
	failUpdates(t, path, "subject_finds")
	rec, _ := attachAs(t, r, work, "sub_a", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, FindID: res.FindID})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "workspace linked, but the find pick was not recorded") {
		t.Fatalf("attach = %d %s; want 500 workspace linked, but the find pick was not recorded", rec.Code, rec.Body)
	}
	if b, err := st.GetCmuxBinding(cmuxWS); err != nil || b.SubjectID != "sub_a" {
		t.Fatalf("binding = %+v, %v; want the link to stand", b, err)
	}
	if f, _ := st.GetSubjectFind(res.FindID); f.PickedSubjectID != "" {
		t.Fatalf("pick stored despite the failure: %+v", f)
	}
	if rec, _ := pickAs(t, r, work, res.FindID, "sub_a"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("pick = %d %s; want 500", rec.Code, rec.Body)
	}
	if n := len(findJournalRows(t, st, pigtail.JournalKindSubjectFindPick)); n != 0 {
		t.Fatalf("%d pick line(s) written by a failed pick", n)
	}
}

// Review round 3: a pick with a body that is no object is a 400, nothing written.
func TestPickSubjectFindInvalidJSON(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_a", "thm_t", "Notifications push", pigtail.StatusBuilding)
	_, res := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications"})
	rec := doAs(t, r, work, "POST", "/subject-finds/"+res.FindID+"/pick", "not an object")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Fatalf("got %d %s, want 400 invalid JSON", rec.Code, rec.Body)
	}
	if f, _ := st.GetSubjectFind(res.FindID); f.PickedSubjectID != "" {
		t.Fatalf("pick stored: %+v", f)
	}
}

// Review round 3: the master without --cross-scope cannot attach a work
// subject with a work search: 403, nothing linked.
func TestAttachWithFindMasterNeedsOptIn(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_t", "thm_t", "Notifications work", pigtail.StatusBuilding)
	_, workFind := findAs(t, r, work, pigtail.SubjectFindRequest{Query: "notifications"})
	rec, _ := attachAs(t, r, perso, "sub_t", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS2, FindID: workFind.FindID})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("master without opt-in = %d %s; want 403", rec.Code, rec.Body)
	}
	if _, err := st.GetCmuxBinding(cmuxWS2); err == nil {
		t.Fatal("the workspace was linked despite the refusal")
	}
}

type findCase struct {
	Query  string `json:"query"`
	Expect string `json:"expect"`
	Caller string `json:"caller"`
	Perso  bool   `json:"perso"`
}

// rankOf is the 1-based place of id among the candidates, 0 = absent.
func rankOf(res pigtail.SubjectFindResult, id string) int {
	for i, c := range res.Candidates {
		if c.SubjectID == id {
			return i + 1
		}
	}
	return 0
}

type findFixture struct {
	Themes []struct {
		ID, Name, Kind string
		VaultLink      string `json:"vault_link"`
	} `json:"themes"`
	Subjects []struct {
		ID, Theme, Title, Status string
		ShortTitle               string `json:"short_title"`
		Logs                     []struct {
			Kind string `json:"kind"`
			TS   int64  `json:"ts"`
		} `json:"logs"`
	} `json:"subjects"`
	Cases []findCase `json:"cases"`
}

// The floors are the rates measured on the fixture when it was committed
// (rounded down), in percent: first rank and top-5.
const (
	findReplayFirstFloor = 90
	findReplayTop5Floor  = 100
)

// TestSubjectFindReplay is a non-regression guard, not the promise: synthetic
// resolutions shaped like real ones, against a fixture world that holds close
// neighbours of the expected subjects (distractors), so the bench discriminates.
// The promise (the user, 2026-10-29) is that the right subject is among the
// first 5 candidates in the large majority of cases; its real rank is measured
// live, from the recorded picks (subject_finds). Both rates are gated here.
func TestSubjectFindReplay(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "subject_find_replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx findFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 20 {
		t.Fatalf("%d cases, want at least 20", len(fx.Cases))
	}
	r, st, _ := newTestRouterSvc(t)
	now := time.UnixMilli(1).UTC()
	for _, th := range fx.Themes {
		if err := st.CreateTheme(&pigtail.Theme{ID: th.ID, Name: th.Name, Kind: th.Kind, VaultLink: th.VaultLink, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range fx.Subjects {
		if err := st.CreateSubject(&pigtail.Subject{ID: s.ID, ThemeID: s.Theme, Title: s.Title, ShortTitle: s.ShortTitle, Slug: s.ID, Status: s.Status, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		for _, l := range s.Logs {
			addFindLog(t, st, s.ID, l.Kind, l.TS)
		}
	}
	first, top5 := 0, 0
	for _, c := range fx.Cases {
		who := c.Caller
		if who == "" {
			who = string(scope.Master())
		}
		_, res := findAs(t, r, caller{scope: who}, pigtail.SubjectFindRequest{Query: c.Query, Limit: pigtail.SubjectFindLimitMax})
		rank := rankOf(res, c.Expect)
		if rank == 1 {
			first++
		} else {
			t.Logf("not first: %q rank %d", c.Query, rank)
		}
		if rank >= 1 && rank <= 5 {
			top5++
		}
	}
	t.Logf("first %d/%d, top-5 %d/%d", first, len(fx.Cases), top5, len(fx.Cases))
	if first*100 < findReplayFirstFloor*len(fx.Cases) {
		t.Errorf("first rank %d/%d below the %d %% floor", first, len(fx.Cases), findReplayFirstFloor)
	}
	if top5*100 < findReplayTop5Floor*len(fx.Cases) {
		t.Errorf("top-5 %d/%d below the %d %% floor", top5, len(fx.Cases), findReplayTop5Floor)
	}
}

// TestSubjectFindReplayLive replays real resolutions against a copy of
// pigtail.db: the picked searches of its own subject_finds table, plus
// PIGTAIL_FIND_REPLAY_CASES (a local JSON array of {query, expect}, never
// committed). Skipped unless PIGTAIL_FIND_REPLAY_DB is set; the caller is
// PIGTAIL_FIND_REPLAY_SCOPE (default the master). A query is never printed:
// its sha8 only. The copy is written to (the replayed searches): never the live file.
func TestSubjectFindReplayLive(t *testing.T) {
	dbPath := os.Getenv("PIGTAIL_FIND_REPLAY_DB")
	if dbPath == "" {
		t.Skip("PIGTAIL_FIND_REPLAY_DB not set")
	}
	who := os.Getenv("PIGTAIL_FIND_REPLAY_SCOPE")
	if who == "" {
		who = string(scope.Master())
	}
	r, _, _ := newTestRouterSvcAt(t, dbPath)
	var cases []findCase
	if p := os.Getenv("PIGTAIL_FIND_REPLAY_CASES"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &cases); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT query, picked_subject_id FROM subject_finds WHERE picked_subject_id IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c findCase
		if err := rows.Scan(&c.Query, &c.Expect); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, c)
	}
	_ = rows.Close()
	_ = db.Close()
	if len(cases) == 0 {
		t.Fatal("no case: neither PIGTAIL_FIND_REPLAY_CASES nor a picked search in the copy")
	}
	first, top5, pFirst, pTop5, pN := 0, 0, 0, 0, 0
	for _, c := range cases {
		sum := sha256.Sum256([]byte(c.Query))
		start := time.Now()
		rec, res := findAs(t, r, caller{scope: who}, pigtail.SubjectFindRequest{Query: c.Query, Limit: pigtail.SubjectFindLimitMax})
		took := time.Since(start)
		if rec.Code != http.StatusOK {
			t.Errorf("q:%x status %d %s", sum[:4], rec.Code, rec.Body)
			continue
		}
		rank := rankOf(res, c.Expect)
		if rank == 1 {
			first++
		}
		if rank >= 1 && rank <= 5 {
			top5++
		}
		if c.Perso {
			pN++
			if rank == 1 {
				pFirst++
			}
			if rank >= 1 && rank <= 5 {
				pTop5++
			}
		}
		top := ""
		if len(res.Candidates) > 0 {
			top = res.Candidates[0].SubjectID
		}
		t.Logf("q:%x expect:%s rank:%d top:%s total:%d %s", sum[:4], c.Expect, rank, top, res.Total, took.Round(time.Millisecond))
		if took > 300*time.Millisecond {
			t.Errorf("q:%x took %s > 300ms", sum[:4], took)
		}
	}
	t.Logf("first %d/%d, top-5 %d/%d", first, len(cases), top5, len(cases))
	t.Logf("perso: first %d/%d, top-5 %d/%d", pFirst, pN, pTop5, pN)
	// No gate on the rate: the promise is top-5 and its real rank is measured
	// in use from the recorded picks; this run is a one-off reading.
}

// A handoff-auto line is not the subject's own activity: findKinds compares
// kinds by exact equality (the plan's contract, task 7a).
func TestFindRecencyIgnoresHandoffAuto(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_auto", "thm_p", "Notifications push", pigtail.StatusBuilding)
	addSuggestSubject(t, st, "sub_man", "thm_p", "Notifications email", pigtail.StatusBuilding)
	addFindLog(t, st, "sub_auto", pigtail.LogHandoffAuto, 7000)
	addFindLog(t, st, "sub_man", pigtail.LogHandoff, 7000) // positive control: a manual handoff counts
	_, res := findAs(t, r, perso, pigtail.SubjectFindRequest{Query: "notif"})
	byID := map[string]pigtail.SubjectFindCandidate{}
	for _, c := range res.Candidates {
		byID[c.SubjectID] = c
	}
	if c := byID["sub_man"]; c.LastActivity == nil {
		t.Fatalf("control: a manual handoff must set the recency: %+v", c)
	}
	if c, ok := byID["sub_auto"]; !ok || c.LastActivity != nil {
		t.Fatalf("a handoff-auto line changed the recency: %+v", c)
	}
}
