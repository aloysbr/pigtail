package handlers

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestBoardRendersSubjects(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	th := &pigtail.Theme{ID: "thm_1", Name: "Meeting", Kind: "project", VaultLink: "Globex/Projects/rain-gauge", CreatedAt: time.UnixMilli(1).UTC()}
	if err := st.CreateTheme(th); err != nil {
		t.Fatalf("theme: %v", err)
	}
	sub := &pigtail.Subject{
		ID: "sub_1", ThemeID: "thm_1", Title: "Checklist style", Slug: "checklist-style",
		Status: pigtail.StatusBuilding, Impact: 4, Effort: 2, Urgency: 3, Rank: 1,
		VaultPath: "Globex/Projects/rain-gauge/subjects/checklist-style",
		CreatedAt: time.UnixMilli(2).UTC(), UpdatedAt: time.UnixMilli(2).UTC(),
	}
	if err := st.CreateSubject(sub); err != nil {
		t.Fatalf("subject: %v", err)
	}

	h := Board(&Services{Store: st})
	req := httptest.NewRequest("GET", "/board", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"Checklist style", "Meeting", "building", "obsidian://"} {
		if !strings.Contains(body, want) {
			t.Errorf("board body missing %q", want)
		}
	}
	// HTML-escaping sanity: a title with a < should be escaped, not raw.
	if strings.Contains(body, "<script>") {
		t.Errorf("board body contains an unescaped <script> tag")
	}
}

func TestBoardEmpty(t *testing.T) {
	t.Parallel()
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = st.Close() })
	h := Board(&Services{Store: st})
	req := httptest.NewRequest("GET", "/board", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty board status = %d", rec.Code)
	}
}

// TestBoardHidesCaptured: GET /board shows neither a column, a card nor a
// count for a captured subject.
func TestBoardHidesCaptured(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_1", Name: "Meeting", Kind: "project", VaultLink: "Globex/Projects/rain-gauge", CreatedAt: time.UnixMilli(1).UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []*pigtail.Subject{
		{ID: "sub_live", ThemeID: "thm_1", Title: "Live card", Slug: "live", Status: pigtail.StatusInbox, Rank: 1, CreatedAt: time.UnixMilli(2).UTC(), UpdatedAt: time.UnixMilli(2).UTC()},
		{ID: "sub_cap", ThemeID: "thm_1", Title: "Captured card", Slug: "cap", Status: pigtail.StatusCaptured, Rank: 2, CreatedAt: time.UnixMilli(2).UTC(), UpdatedAt: time.UnixMilli(2).UTC()},
	} {
		if err := st.CreateSubject(sub); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	Board(&Services{Store: st})(rec, httptest.NewRequest("GET", "/board?scope=all", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "Live card") {
		t.Fatalf("live card missing: %s", body)
	}
	if strings.Contains(body, "Captured card") || strings.Contains(body, "captured") {
		t.Fatalf("a captured subject shows on the board: %s", body)
	}
	if !strings.Contains(body, "(1)</header>") {
		t.Fatalf("count must be 1: %s", body)
	}
}

// TestBoardColumnsMaturity: the board's columns are the maturity
// statuses in order, captured aside; a dropped subject shows, a captured one
// does not.
func TestBoardColumnsMaturity(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	th := &pigtail.Theme{ID: "thm_1", Name: "Meeting", Kind: "project", VaultLink: "Globex/Projects/m", CreatedAt: time.UnixMilli(1).UTC()}
	if err := st.CreateTheme(th); err != nil {
		t.Fatalf("theme: %v", err)
	}
	for _, s := range pigtail.SubjectStatuses {
		sub := &pigtail.Subject{ID: "sub_" + s, ThemeID: "thm_1", Title: "Card " + s, Slug: s, Status: s,
			CreatedAt: time.UnixMilli(2).UTC(), UpdatedAt: time.UnixMilli(2).UTC()}
		if err := st.CreateSubject(sub); err != nil {
			t.Fatalf("subject %s: %v", s, err)
		}
	}
	rec := httptest.NewRecorder()
	Board(&Services{Store: st})(rec, httptest.NewRequest("GET", "/board", nil))
	body := rec.Body.String()
	var cols []string
	for _, m := range regexp.MustCompile(`<h2>([a-z_]+) \(`).FindAllStringSubmatch(body, -1) {
		cols = append(cols, m[1])
	}
	want := []string{"inbox", "understanding", "designing", "building", "done", "parked", "dropped"}
	if strings.Join(cols, ",") != strings.Join(want, ",") {
		t.Fatalf("columns = %v, want %v", cols, want)
	}
	if !strings.Contains(body, "Card dropped") {
		t.Error("the dropped subject is not shown")
	}
	if strings.Contains(body, "Card captured") {
		t.Error("the captured subject is shown")
	}
}
