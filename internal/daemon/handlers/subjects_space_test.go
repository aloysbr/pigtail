package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func spaceRouter(t *testing.T) *chi.Mux {
	t.Helper()
	r, _, svc := newTestRouterSvc(t)
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	svc.Spaces = tbl
	return r
}

func seedSpaceTheme(t *testing.T, r http.Handler, c caller, link string) string {
	t.Helper()
	rec := doAs(t, r, c, "POST", "/themes", pigtail.Theme{Name: "T", Kind: "project", VaultLink: link})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create theme = %d %s", rec.Code, rec.Body.String())
	}
	var th pigtail.Theme
	_ = json.Unmarshal(rec.Body.Bytes(), &th)
	return th.ID
}

func TestThemeSpacePatch(t *testing.T) {
	t.Parallel()
	r := spaceRouter(t)
	pid := seedSpaceTheme(t, r, perso, "Perso/Projects/chat")
	tid := seedSpaceTheme(t, r, work, "Globex/Projects/chat")

	rec := doAs(t, r, perso, "PATCH", "/themes/"+pid, map[string]any{"space": "Allotment"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"space":"Allotment"`) {
		t.Fatalf("valid space = %d %s", rec.Code, rec.Body.String())
	}
	rec = doAs(t, r, perso, "PATCH", "/themes/"+pid, map[string]any{"space": "Vineyard"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown space") {
		t.Fatalf("other scope's space = %d %s", rec.Code, rec.Body.String())
	}
	rec = doAs(t, r, perso, "PATCH", "/themes/"+pid, map[string]any{"space": "Nope"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Allotment") || !strings.Contains(rec.Body.String(), "Garden") {
		t.Fatalf("unknown space must list the visible ones: %d %s", rec.Code, rec.Body.String())
	}
	// another scope's space answers like an unknown one.
	rec = doAs(t, r, work, "PATCH", "/themes/"+tid, map[string]any{"space": "Allotment"})
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "Allotment") || strings.Contains(rec.Body.String(), "Garden") {
		t.Fatalf("work must not learn a perso space: %d %s", rec.Code, rec.Body.String())
	}
	rec = doAs(t, r, perso, "PATCH", "/themes/"+pid, map[string]any{"space": ""})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"space"`) {
		t.Fatalf("clear = %d %s", rec.Code, rec.Body.String())
	}
}

func TestThemeSpaceCreate(t *testing.T) {
	t.Parallel()
	r := spaceRouter(t)
	rec := doAs(t, r, perso, "POST", "/themes", pigtail.Theme{Name: "T", Kind: "project", VaultLink: "Perso/Projects/x", Space: "Garden"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"space":"Garden"`) {
		t.Fatalf("create with space = %d %s", rec.Code, rec.Body.String())
	}
	rec = doAs(t, r, perso, "POST", "/themes", pigtail.Theme{Name: "T", Kind: "project", VaultLink: "Perso/Projects/y", Space: "Vineyard"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with another scope's space = %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetSpacesFollowsScope(t *testing.T) {
	t.Parallel()
	r := spaceRouter(t)
	for c, want := range map[caller][]string{
		perso: {"Garden", "Vineyard", "Meadow", "Greenhouse", "Nursery", "Allotment", "Compost", "Pond"},
		work:  {"Vineyard", "Meadow", "Greenhouse", "Nursery"},
	} {
		rec := doAs(t, r, c, "GET", "/spaces", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c, rec.Code, rec.Body.String())
		}
		var got []pigtail.Space
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range got {
			names = append(names, s.Name)
		}
		if !reflect.DeepEqual(names, want) {
			t.Errorf("%s sees %q, want %q", c, names, want)
		}
	}
}

// a POST that changes the theme clears the space the
// subject was dragged into; the response shows the cleared state; a POST that
// does not change the theme keeps it; `space` itself is read-only.
func TestPostSubjectRethemeClearsSpace(t *testing.T) {
	t.Parallel()
	r, _, svc := newTestRouterSvc(t)
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	svc.Spaces = tbl
	t1 := seedSpaceTheme(t, r, perso, "Perso/Projects/one")
	t2 := seedSpaceTheme(t, r, perso, "Perso/Projects/two")
	rec := doAs(t, r, perso, "POST", "/subjects", pigtail.Subject{ThemeID: t1, Title: "S"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create subject = %d %s", rec.Code, rec.Body.String())
	}
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)
	setSpace := func() {
		t.Helper()
		if _, err := svc.Store.SetSubjectSpaceByDrag(sub.ID, "Garden", "Allotment", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	curSpace := func() string {
		t.Helper()
		got, err := svc.Store.GetSubject(sub.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Space
	}
	post := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		return doAs(t, r, perso, "POST", "/subjects/"+sub.ID, body)
	}

	setSpace()
	// A title-only POST keeps the space, response included.
	if rec := post(map[string]any{"title": "S2"}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"space":"Garden"`) || curSpace() != "Garden" {
		t.Fatalf("title-only POST = %d %s, space %q", rec.Code, rec.Body.String(), curSpace())
	}
	// A body that only carries `space`: nothing writable.
	if rec := post(map[string]any{"space": "Hack"}); rec.Code != http.StatusBadRequest || curSpace() != "Garden" {
		t.Fatalf("space-only POST = %d %s, space %q", rec.Code, rec.Body.String(), curSpace())
	}
	// `space` beside a title is ignored.
	if rec := post(map[string]any{"title": "S3", "space": "Hack"}); rec.Code != http.StatusOK || curSpace() != "Garden" {
		t.Fatalf("space+title POST = %d %s, space %q", rec.Code, rec.Body.String(), curSpace())
	}
	// An unknown theme: 404, nothing cleared.
	if rec := post(map[string]any{"theme_id": "thm_nope"}); rec.Code != http.StatusNotFound || curSpace() != "Garden" {
		t.Fatalf("unknown theme POST = %d %s, space %q", rec.Code, rec.Body.String(), curSpace())
	}
	// A theme change clears it, and the response says so.
	rec = post(map[string]any{"theme_id": t2})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"space"`) || curSpace() != "" {
		t.Fatalf("retheme POST = %d %s, space %q", rec.Code, rec.Body.String(), curSpace())
	}
}

// TestThemeWithRetiredSpace: a theme written by the store with a pre-taxonomy
// space (an old row) does not stop the table from loading nor the theme from
// being read; the name is simply no longer declared (plan B, task 17).
func TestThemeWithRetiredSpace(t *testing.T) {
	t.Parallel()
	r, st, svc := newTestRouterSvc(t)
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	svc.Spaces = tbl
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_old", Name: "Old", Kind: "project", VaultLink: "Globex/Projects/old", Space: "Hedge", CreatedAt: time.UnixMilli(1).UTC()}); err != nil {
		t.Fatal(err)
	}
	rec := doAs(t, r, work, "GET", "/themes", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"space":"Hedge"`) {
		t.Fatalf("a theme on a retired space must still be listed as stored: %d %s", rec.Code, rec.Body.String())
	}
	if slices.Contains(tbl.Names(), "Hedge") {
		t.Fatalf("Hedge still declared: %q", tbl.Names())
	}
	for _, n := range []string{"Hedge", "Design", "Lawn", "Terrace"} {
		if slices.Contains(tbl.FolderOrder(), n) {
			t.Fatalf("%s still a folder: %q", n, tbl.FolderOrder())
		}
	}
}
