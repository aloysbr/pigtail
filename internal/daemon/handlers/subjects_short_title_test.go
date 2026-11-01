package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestSubjectShortTitleEndpoints(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	themeID := seedThemeAPI(t, r)

	// Create accepts a short title and trims it.
	rec := doJSON(t, r, "POST", "/subjects", pigtail.Subject{ThemeID: themeID, Title: "Enrichir la sidebar cmux avec l'état pigtail", ShortTitle: "  Sidebar pigtail "})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)
	if sub.ShortTitle != "Sidebar pigtail" {
		t.Fatalf("create short_title = %q", sub.ShortTitle)
	}

	// Read back through GET.
	rec = doJSON(t, r, "GET", "/subjects/"+sub.ID, nil)
	var got pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ShortTitle != "Sidebar pigtail" {
		t.Fatalf("get short_title = %q", got.ShortTitle)
	}

	// Over the limit → 400, and nothing is written.
	tooLong := got
	tooLong.ShortTitle = strings.Repeat("é", pigtail.ShortTitleMax+1)
	rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, tooLong)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update 33 runes status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	multi := got
	multi.ShortTitle = "two\nlines"
	if rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, multi); rec.Code != http.StatusBadRequest {
		t.Fatalf("update multi-line status = %d, want 400", rec.Code)
	}
	rec = doJSON(t, r, "POST", "/subjects", pigtail.Subject{ThemeID: themeID, Title: "x", ShortTitle: strings.Repeat("a", pigtail.ShortTitleMax+1)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create 33 runes status = %d, want 400", rec.Code)
	}
	rec = doJSON(t, r, "GET", "/subjects/"+sub.ID, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ShortTitle != "Sidebar pigtail" {
		t.Fatalf("rejected update leaked into the store: %q", got.ShortTitle)
	}

	// Exactly the limit is fine.
	got.ShortTitle = strings.Repeat("é", pigtail.ShortTitleMax)
	if rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, got); rec.Code != http.StatusOK {
		t.Fatalf("update 32 runes status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// Partial update: a body without short_title leaves it as is — including
	// a full struct whose empty short_title is dropped by omitempty.
	shortOf := func() (string, bool) {
		rec := doJSON(t, r, "GET", "/subjects/"+sub.ID, nil)
		var raw map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &raw)
		v, present := raw["short_title"]
		s, _ := v.(string)
		return s, present
	}
	if rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"short_title": "Sidebar pigtail"}); rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"rank": 4}); rec.Code != http.StatusOK {
		t.Fatalf("patch rank status = %d", rec.Code)
	}
	full := got
	full.ShortTitle = ""
	if rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, full); rec.Code != http.StatusOK {
		t.Fatalf("full struct status = %d", rec.Code)
	}
	if st, _ := shortOf(); st != "Sidebar pigtail" {
		t.Fatalf("absent short_title changed the value: %q", st)
	}
	// Over the limit through a patch → 400, value kept.
	if rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"short_title": strings.Repeat("a", pigtail.ShortTitleMax+1)}); rec.Code != http.StatusBadRequest {
		t.Fatalf("patch 33 runes status = %d, want 400", rec.Code)
	}
	if st, _ := shortOf(); st != "Sidebar pigtail" {
		t.Fatalf("rejected patch leaked: %q", st)
	}

	// "" and null both go back to the derived title: the field leaves the wire.
	for _, clear := range []any{"", "   ", nil} {
		if rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"short_title": "Sidebar pigtail"}); rec.Code != http.StatusOK {
			t.Fatalf("reset status = %d", rec.Code)
		}
		if rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"short_title": clear}); rec.Code != http.StatusOK {
			t.Fatalf("clear with %#v status = %d; body=%s", clear, rec.Code, rec.Body.String())
		}
		if st, present := shortOf(); present {
			t.Fatalf("short_title cleared with %#v still on the wire: %q", clear, st)
		}
	}
}
