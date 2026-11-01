package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func addCaptureAs(t *testing.T, r http.Handler, c caller, theme, text, from string) (*httptest.ResponseRecorder, pigtail.Capture) {
	t.Helper()
	body := map[string]any{"theme_id": theme, "text": text}
	if from != "" {
		body["from_subject_id"] = from
	}
	rec := doAs(t, r, c, "POST", "/captures", body)
	var cp pigtail.Capture
	_ = json.Unmarshal(rec.Body.Bytes(), &cp)
	return rec, cp
}

func TestCaptureAddListPromoteDelete(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	rec, cp := addCaptureAs(t, r, work, "thm_t", "  an idea ", "")
	if rec.Code != http.StatusCreated || cp.ID == "" || cp.ID[:4] != "cap_" || cp.Text != "an idea" || cp.Scope != "globex" {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := addCaptureAs(t, r, work, "thm_t", "   ", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty text: %d, want 400", rec.Code)
	}
	rec = doAs(t, r, work, "GET", "/captures?theme=thm_t&open=1", nil)
	var list []pigtail.Capture
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = doAs(t, r, work, "POST", "/captures/promote", map[string]any{"capture_ids": []string{cp.ID}, "title": "Do the thing"})
	var res struct {
		Subject  pigtail.Subject   `json:"subject"`
		Captures []pigtail.Capture `json:"captures"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusCreated || res.Subject.Status != pigtail.StatusInbox || res.Subject.ThemeID != "thm_t" || len(res.Captures) != 1 || res.Captures[0].ClosedAt == nil {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body)
	}
	if got, err := st.GetSubject(res.Subject.ID); err != nil || got.Scope != "globex" {
		t.Fatalf("promoted subject = %+v, %v", got, err)
	}
	rec = doAs(t, r, work, "DELETE", "/captures/"+cp.ID, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete promoted: %d, want 400", rec.Code)
	}
}

func TestCaptureDeleteRestoresViaAPI(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	_, cp := addCaptureAs(t, r, work, "thm_t", "replaces", "sub_t")
	if sub, _ := st.GetSubject("sub_t"); sub.Status != pigtail.StatusCaptured {
		t.Fatalf("status = %q, want captured", sub.Status)
	}
	rec := doAs(t, r, work, "DELETE", "/captures/"+cp.ID, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"subject"`) {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if sub, _ := st.GetSubject("sub_t"); sub.Status != pigtail.StatusInbox {
		t.Fatalf("status = %q, want inbox", sub.Status)
	}
	// Subject changed since: the response says it, the status is not touched.
	_, cp = addCaptureAs(t, r, work, "thm_t", "again", "sub_t")
	sub, _ := st.GetSubject("sub_t")
	sub.Status = pigtail.StatusBuilding
	_ = st.UpdateSubject(sub)
	rec = doAs(t, r, work, "DELETE", "/captures/"+cp.ID, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "changed since") {
		t.Fatalf("delete after change: %d %s", rec.Code, rec.Body)
	}
}

// Work reads and writes of a perso theme are 404 (no-probe CanWrite →
// ErrNotVisible before the cross-scope check); the perso master needs
// --cross-scope to write a work theme and gets 403 SCOPE_MISMATCH otherwise.
func TestCaptureScope(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	_, persoCap := addCaptureAs(t, r, perso, "thm_p", "perso idea", "")
	_, workCap := addCaptureAs(t, r, work, "thm_t", "work idea", "")
	if persoCap.ID == "" || workCap.ID == "" {
		t.Fatal("seed captures")
	}

	var list []pigtail.Capture
	rec := doAs(t, r, work, "GET", "/captures", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list) != 1 || list[0].ID != workCap.ID || strings.Contains(rec.Body.String(), "perso idea") {
		t.Fatalf("work list leaks or misses: %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, work, "GET", "/captures?theme=thm_p", nil); rec.Code != 404 {
		t.Fatalf("work list of a perso theme: %d, want 404", rec.Code)
	}
	if rec, _ := addCaptureAs(t, r, work, "thm_p", "x", ""); rec.Code != 404 {
		t.Fatalf("work add on a perso theme: %d, want 404", rec.Code)
	}
	if rec, _ := addCaptureAs(t, r, workCross, "thm_p", "x", ""); rec.Code != 404 {
		t.Fatalf("work add on a perso theme with cross: %d, want 404", rec.Code)
	}
	if rec := doAs(t, r, work, "POST", "/captures/promote", map[string]any{"capture_ids": []string{persoCap.ID}, "title": "t"}); rec.Code != 404 {
		t.Fatalf("work promote of a perso capture: %d, want 404", rec.Code)
	}
	if rec := doAs(t, r, work, "DELETE", "/captures/"+persoCap.ID, nil); rec.Code != 404 {
		t.Fatalf("work delete of a perso capture: %d, want 404", rec.Code)
	}

	// The perso master: reads all, writes a work theme only with cross-scope.
	rec = doAs(t, r, perso, "GET", "/captures", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list) != 2 {
		t.Fatalf("master list: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := addCaptureAs(t, r, perso, "thm_t", "x", ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), string(pigtail.ErrScopeMismatch)) {
		t.Fatalf("master add on work without cross: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := addCaptureAs(t, r, persoCross, "thm_t", "x", ""); rec.Code != http.StatusCreated {
		t.Fatalf("master add on work with cross: %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, perso, "DELETE", "/captures/"+workCap.ID, nil); rec.Code != 403 {
		t.Fatalf("master delete on work without cross: %d, want 403", rec.Code)
	}
	if rec := doAs(t, r, persoCross, "DELETE", "/captures/"+workCap.ID, nil); rec.Code != 200 {
		t.Fatalf("master delete on work with cross: %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, perso, "POST", "/captures/promote", map[string]any{"capture_ids": []string{persoCap.ID}, "title": "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("master promote on perso: %d %s", rec.Code, rec.Body)
	}
	_ = st
}

func TestCaptureFromOtherThemeAPI(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	if rec, _ := addCaptureAs(t, r, perso, "thm_p", "x", "sub_t"); rec.Code != http.StatusBadRequest {
		t.Fatalf("from a subject of another theme: %d %s, want 400", rec.Code, rec.Body)
	}
}

func TestCapturePromoteMixedThemesAPI(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	_, a := addCaptureAs(t, r, persoCross, "thm_p", "a", "")
	_, b := addCaptureAs(t, r, persoCross, "thm_t", "b", "")
	rec := doAs(t, r, persoCross, "POST", "/captures/promote", map[string]any{"capture_ids": []string{a.ID, b.ID}, "title": "mixed"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mixed themes: %d %s, want 400", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "captures to promote are not all in the same theme") || strings.Contains(rec.Body.String(), "capture and subject") {
		t.Fatalf("mixed themes message: %s", rec.Body)
	}
}

// No orphan capture: a theme with open captures cannot be deleted (foreign
// keys are off, so 0029's FOREIGN KEY protects nothing).
func TestDeleteThemeWithCaptures(t *testing.T) {
	t.Parallel()
	r, st := newTestRouter(t)
	th := seedThemeAPI(t, r)
	rec := doJSON(t, r, "POST", "/captures", map[string]any{"theme_id": th, "text": "an idea"})
	var cp pigtail.Capture
	_ = json.Unmarshal(rec.Body.Bytes(), &cp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, r, "DELETE", "/themes/"+th, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete theme with an open capture: %d, want 409", rec.Code)
	}
	if _, err := st.GetTheme(th); err != nil {
		t.Fatalf("theme gone: %v", err)
	}
	if _, err := st.GetCapture(cp.ID); err != nil {
		t.Fatalf("capture gone: %v", err)
	}
	// Promoted: the capture is closed, the theme (with the new subject) still
	// refuses on the subject; delete the subject's link by deleting nothing —
	// here only the closed-capture rule matters.
	rec = doJSON(t, r, "POST", "/captures/promote", map[string]any{"capture_ids": []string{cp.ID}, "title": "S"})
	var res struct {
		Subject pigtail.Subject `json:"subject"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if n, _ := st.CountOpenCapturesByTheme(th); n != 0 {
		t.Fatalf("open captures after promote = %d", n)
	}
	// A theme whose only capture was deleted is deletable.
	th2 := doJSON(t, r, "POST", "/themes", pigtail.Theme{Name: "Y", Kind: "project", VaultLink: "Globex/Projects/y"})
	var t2 pigtail.Theme
	_ = json.Unmarshal(th2.Body.Bytes(), &t2)
	rec = doJSON(t, r, "POST", "/captures", map[string]any{"theme_id": t2.ID, "text": "x"})
	_ = json.Unmarshal(rec.Body.Bytes(), &cp)
	if rec := doJSON(t, r, "DELETE", "/captures/"+cp.ID, nil); rec.Code != 200 {
		t.Fatalf("delete capture: %d", rec.Code)
	}
	if rec := doJSON(t, r, "DELETE", "/themes/"+t2.ID, nil); rec.Code != 200 {
		t.Fatalf("delete theme after its capture is gone: %d %s", rec.Code, rec.Body)
	}
}

// Captures POST/promote/DELETE are held by a freeze; GET stays served.
func TestCaptureFreeze(t *testing.T) {
	t.Parallel()
	r, _ := frozenRouter(t, frozen())
	for _, w := range [][2]string{{"POST", "/captures"}, {"POST", "/captures/promote"}, {"DELETE", "/captures/cap_x"}} {
		rec := req(r, w[0], w[1], "other")
		if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "MAINTENANCE_FROZEN") {
			t.Fatalf("%s %s under freeze: %d %s, want 423", w[0], w[1], rec.Code, rec.Body)
		}
		if rec := req(r, w[0], w[1], "lead"); rec.Code == http.StatusLocked {
			t.Fatalf("%s %s: the owner must pass the freeze", w[0], w[1])
		}
	}
	if rec := req(r, "GET", "/captures", "other"); rec.Code == http.StatusLocked {
		t.Fatal("GET /captures must be served under a freeze")
	}
}

// A capture never shows on the board: not as a card, not in the count.
func TestBoardHidesCapture(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_1", Name: "Meeting", Kind: "project", VaultLink: "Globex/Projects/rain-gauge", CreatedAt: time.UnixMilli(1).UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_live", ThemeID: "thm_1", Title: "Live card", Slug: "live", Status: pigtail.StatusInbox, CreatedAt: time.UnixMilli(2).UTC(), UpdatedAt: time.UnixMilli(2).UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddCapture(&pigtail.Capture{ID: "cap_1", ThemeID: "thm_1", Text: "Hidden idea", CreatedAt: time.UnixMilli(3).UTC()}, "log_1"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	Board(&Services{Store: st})(rec, httptest.NewRequest("GET", "/board?scope=all", nil))
	body := rec.Body.String()
	if strings.Contains(body, "Hidden idea") || strings.Contains(body, "cap_1") || !strings.Contains(body, "(1)</header>") {
		t.Fatalf("a capture shows on the board: %s", body)
	}
}

// A capture made from a subject closes it (captured): not by the web front.
func TestCookieCaptureFromSubjectRefused(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	setStatus(t, st, "sub_p", pigtail.StatusBuilding)
	assertForbiddenForWeb(t, "web front capture from a subject",
		doWeb(t, r, "POST", "/captures", map[string]any{"theme_id": "thm_p", "text": "idea", "from_subject_id": "sub_p"}))
	if sub, _ := st.GetSubject("sub_p"); sub.Status != pigtail.StatusBuilding {
		t.Fatalf("subject became %s", sub.Status)
	}
	if caps, _ := st.ListCaptures(store.CaptureFilter{Scope: scope.Any}); len(caps) != 0 {
		t.Fatalf("a refused capture was created: %+v", caps)
	}
	if rec := doWeb(t, r, "POST", "/captures", map[string]any{"theme_id": "thm_p", "text": "idea"}); rec.Code != http.StatusCreated {
		t.Fatalf("web front plain capture = %d %s, want 201", rec.Code, rec.Body)
	}
}

// Undoing a capture born of a subject puts its previous status back, done
// included: not by the web front.
func TestCookieDeleteSubjectCaptureRefused(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	setStatus(t, st, "sub_p", pigtail.StatusDone)
	rec, cp := addCaptureAs(t, r, perso, "thm_p", "replaces", "sub_p")
	if rec.Code != http.StatusCreated || cp.FromPrevStatus != pigtail.StatusDone {
		t.Fatalf("seed capture: %d %s", rec.Code, rec.Body)
	}
	assertForbiddenForWeb(t, "web front delete of a subject's capture", doWeb(t, r, "DELETE", "/captures/"+cp.ID, nil))
	if got, err := st.GetCapture(cp.ID); err != nil || got.ClosedAt != nil {
		t.Fatalf("capture after a refused delete: %+v %v", got, err)
	}
	if sub, _ := st.GetSubject("sub_p"); sub.Status != pigtail.StatusCaptured {
		t.Fatalf("subject became %s", sub.Status)
	}
	if rec := doAs(t, r, perso, "DELETE", "/captures/"+cp.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("CLI delete = %d %s, want 200", rec.Code, rec.Body)
	}
	if sub, _ := st.GetSubject("sub_p"); sub.Status != pigtail.StatusDone {
		t.Fatalf("CLI undo: subject = %s, want done", sub.Status)
	}
}
