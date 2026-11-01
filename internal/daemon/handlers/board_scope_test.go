package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

// TestBoardScope: the board is public (no scope header), so ?scope= picks the
// view and its absence is work — a browser tab never shows perso by accident.
func TestBoardScope(t *testing.T) {
	t.Parallel()
	_, st := scopedFixture(t)
	h := Board(&Services{Store: st})
	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/board"+q, nil))
		return rec
	}
	cases := []struct {
		q         string
		title     string
		show, hid []string
	}{
		{"", "subjects · globex (1)", []string{"work subject"}, []string{"perso subject"}},
		{"?scope=gx", "subjects · globex (1)", []string{"work subject"}, []string{"perso subject"}},
		{"?scope=perso", "subjects · perso (1)", []string{"perso subject"}, []string{"work subject"}},
		{"?scope=all", "subjects · all (2)", []string{"work subject", "perso subject"}, nil},
	}
	for _, tc := range cases {
		rec := get(tc.q)
		if rec.Code != http.StatusOK {
			t.Fatalf("board %s: status %d", tc.q, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, tc.title) {
			t.Errorf("board %s: title %q missing", tc.q, tc.title)
		}
		for _, s := range tc.show {
			if !strings.Contains(body, s) {
				t.Errorf("board %s: %q missing", tc.q, s)
			}
		}
		for _, s := range tc.hid {
			if strings.Contains(body, s) {
				t.Errorf("board %s: %q must not show", tc.q, s)
			}
		}
		// The theme names of the other scope must not leak either.
		if tc.q == "" && strings.Contains(body, `class="theme">P<`) {
			t.Errorf("board (work) shows the perso theme name")
		}
	}
	if rec := get("?scope=bogus"); rec.Code != http.StatusBadRequest {
		t.Errorf("board ?scope=bogus: %d, want 400", rec.Code)
	}
}

// TestBoardUnknownScopeNamesNoScope (D20): the board stays the master's view,
// but its 400 for an unknown name lists no scope — it echoes the name it got
// and nothing else.
func TestBoardUnknownScopeNamesNoScope(t *testing.T) {
	scopetest.Three(t)
	_, st := scopedFixture(t)
	rec := httptest.NewRecorder()
	Board(&Services{Store: st})(rec, httptest.NewRequest("GET", "/board?scope=nope", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("board ?scope=nope: %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"nope"`) {
		t.Errorf("board 400 does not name the unknown scope: %q", body)
	}
	for _, name := range []string{"acme", "perso", "globex", "gx"} {
		if strings.Contains(body, name) {
			t.Errorf("board 400 names scope %q: %q", name, body)
		}
	}
}

// The alias reads as its scope; every label says the canonical name.
func TestBoardScopeAlias(t *testing.T) {
	t.Parallel()
	for q, want := range map[string]string{"": "globex", "gx": "globex"} {
		sc, label, err := boardScope(q)
		if err != nil || string(sc) != want || label != want {
			t.Errorf("boardScope(%q) = %q, %q, %v; want %s", q, sc, label, err, want)
		}
	}
	_, st := scopedFixture(t)
	rec := httptest.NewRecorder()
	Board(&Services{Store: st})(rec, httptest.NewRequest("GET", "/board?scope=gx", nil))
	if !strings.Contains(rec.Body.String(), "<title>pigtail · subjects · globex</title>") {
		t.Errorf("board ?scope=gx title: %s", rec.Body.String()[:200])
	}
}
