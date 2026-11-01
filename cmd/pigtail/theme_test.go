package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestApplyThemeField(t *testing.T) {
	t.Parallel()
	var f themeUpdateFields
	if err := applyThemeField(&f, "name", "New name"); err != nil {
		t.Fatalf("name: %v", err)
	}
	if err := applyThemeField(&f, "vault_link", "Globex/Projects/x"); err != nil {
		t.Fatalf("vault_link: %v", err)
	}
	if f.name == nil || *f.name != "New name" {
		t.Fatalf("name not set: %+v", f.name)
	}
	if f.vaultLink == nil || *f.vaultLink != "Globex/Projects/x" {
		t.Fatalf("vault_link not set: %+v", f.vaultLink)
	}
}

func TestApplyThemeFieldUnknownKey(t *testing.T) {
	t.Parallel()
	var f themeUpdateFields
	for _, key := range []string{"id", "kind", "created_at", "bogus"} {
		err := applyThemeField(&f, key, "v")
		if err == nil {
			t.Fatalf("expected error for immutable/unknown key %q", key)
		}
		if !strings.Contains(err.Error(), "name, vault_link, space, deadline, carried") {
			t.Fatalf("key %q: message must list the settable keys: %v", key, err)
		}
	}
}

func TestApplyThemeFieldSpace(t *testing.T) {
	t.Parallel()
	var f themeUpdateFields
	if err := applyThemeField(&f, "space", "Terrace"); err != nil {
		t.Fatalf("space: %v", err)
	}
	if f.space == nil || *f.space != "Terrace" {
		t.Fatalf("space not set: %+v", f.space)
	}
}

func TestThemeListLineShowsSpace(t *testing.T) {
	t.Parallel()
	if got := themeLine(pigtail.Theme{ID: "thm_1", Kind: "project", Scope: "perso", Name: "Chat", VaultLink: "Perso/Projects/chat", Space: "Terrace"}); !strings.Contains(got, "{Terrace}") {
		t.Fatalf("line = %q", got)
	}
	if got := themeLine(pigtail.Theme{ID: "thm_1", Kind: "project", Scope: "perso", Name: "Chat", VaultLink: "Perso/Projects/chat"}); strings.Contains(got, "{") {
		t.Fatalf("no space must print no braces: %q", got)
	}
}

// create --space sends the field (and only then): the daemon would refuse nothing else.
func TestThemeCreateSendsSpace(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	var bodies []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/themes", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		writeData(w, http.StatusCreated, pigtail.Theme{ID: "thm_x", Name: "X", Kind: "project", VaultLink: "Perso/Projects/x"})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	base := []string{"create", "--name", "X", "--kind", "project", "--vault-link", "Perso/Projects/x"}
	if _, err := runTheme(t, s, append(base, "--space", "Terrace")...); err != nil {
		t.Fatal(err)
	}
	if _, err := runTheme(t, s, base...); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0]["space"] != "Terrace" {
		t.Fatalf("bodies = %+v, want the first to carry space = Terrace", bodies)
	}
	if _, has := bodies[1]["space"]; has {
		t.Fatalf("no --space must send no space: %+v", bodies[1])
	}
}

// spacesDaemon serves the real subject routes with the built-in space table.
func spacesDaemon(t *testing.T) *stubDaemon {
	t.Helper()
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := realSubjectDaemonSvc(t, func(svc *handlers.Services) { svc.Spaces = tbl }, nil)
	return s
}

// theme spaces --json prints what GET /spaces answers: order, filtered by the
// caller's scope (real routes, real tables: the filter is the daemon's, the order
// and the pass-through are the CLI's).
func TestThemeSpacesJSONFollowsScope(t *testing.T) {
	for _, c := range []struct {
		cell string
		want []string
	}{
		{"perso", []string{"Garden", "Vineyard", "Meadow", "Greenhouse", "Nursery", "Allotment", "Compost", "Pond"}},
		{"work", []string{"Vineyard", "Meadow", "Greenhouse", "Nursery"}},
	} {
		t.Run(c.cell, func(t *testing.T) {
			withCells(t, func(work, perso string) string { return map[string]string{"work": work, "perso": perso}[c.cell] })
			s := spacesDaemon(t)
			out, err := runTheme(t, s, "spaces", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var got []pigtail.Space
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("not JSON: %q", out)
			}
			var names []string
			for _, sp := range got {
				names = append(names, sp.Name)
			}
			if !reflect.DeepEqual(names, c.want) {
				t.Fatalf("spaces = %q, want %q", names, c.want)
			}
			if c.cell == "work" && (strings.Contains(out, "Terrace") || strings.Contains(out, "Garden") || strings.Contains(out, "Allotment") || strings.Contains(out, "Compost") || strings.Contains(out, "Pond")) {
				t.Fatalf("a work cell must not learn another scope's space: %s", out)
			}
		})
	}
}

func TestApplyThemeFieldDeadlineCarried(t *testing.T) {
	var f themeUpdateFields
	if err := applyThemeField(&f, "deadline", "2026-10-31"); err != nil || f.deadline == nil || *f.deadline != "2026-10-31" {
		t.Fatalf("deadline: %v %+v", err, f.deadline)
	}
	if err := applyThemeField(&f, "deadline", ""); err != nil || f.deadline == nil || *f.deadline != "" {
		t.Fatalf("deadline clear: %v %+v", err, f.deadline)
	}
	err := applyThemeField(&f, "deadline", "31/10/2026")
	if want := `deadline must be a date YYYY-MM-DD, or empty to clear (got "31/10/2026")`; err == nil || err.Error() != want {
		t.Fatalf("bad deadline err = %v, want %q", err, want)
	}
	if err := applyThemeField(&f, "carried", "true"); err != nil || f.carried == nil || !*f.carried {
		t.Fatalf("carried=true: %v %+v", err, f.carried)
	}
	if err := applyThemeField(&f, "carried", "false"); err != nil || f.carried == nil || *f.carried {
		t.Fatalf("carried=false: %v %+v", err, f.carried)
	}
	for _, bad := range []string{"yes", ""} {
		err := applyThemeField(&f, "carried", bad)
		if want := `carried must be true or false (got "` + bad + `")`; err == nil || err.Error() != want {
			t.Fatalf("carried=%q err = %v, want %q", bad, err, want)
		}
	}
}

func TestThemeListLineShowsDeadline(t *testing.T) {
	th := pigtail.Theme{ID: "thm_1", Kind: "project", Scope: "perso", Name: "Chat", VaultLink: "Perso/Projects/chat", Space: "Allotment", Deadline: "2026-10-31"}
	if got := themeLine(th); !strings.HasSuffix(got, "{Allotment}  due 2026-10-31") {
		t.Fatalf("line = %q", got)
	}
	th.Deadline = ""
	if got := themeLine(th); strings.Contains(got, "due") {
		t.Fatalf("no deadline must print no due: %q", got)
	}
}

func TestThemeSetDeadlineCarriedE2E(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemonWith(t, nil)
	if _, err := runTheme(t, s, "set", "thm_p", "deadline=2026-10-31", "carried=true"); err != nil {
		t.Fatal(err)
	}
	rowsOf := func() map[string]map[string]any {
		out, err := runTheme(t, s, "list", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("not JSON: %q", out)
		}
		byID := map[string]map[string]any{}
		for _, r := range rows {
			if _, ok := r["deadline"]; !ok {
				t.Fatalf("row without deadline key: %v", r)
			}
			if _, ok := r["carried"]; !ok {
				t.Fatalf("row without carried key: %v", r)
			}
			byID[r["id"].(string)] = r
		}
		return byID
	}
	rows := rowsOf()
	if rows["thm_p"]["deadline"] != "2026-10-31" || rows["thm_p"]["carried"] != true {
		t.Fatalf("thm_p = %v", rows["thm_p"])
	}
	if r, ok := rows["thm_t"]; ok && (r["deadline"] != "" || r["carried"] != false) {
		t.Fatalf("thm_t = %v", r)
	}
	if out, err := runTheme(t, s, "list"); err != nil || !strings.Contains(out, "due 2026-10-31") {
		t.Fatalf("text list = %q, %v", out, err)
	}
	if _, err := runTheme(t, s, "set", "thm_p", "deadline="); err != nil {
		t.Fatal(err)
	}
	if th, _ := st.GetTheme("thm_p"); th.Deadline != "" || !th.Carried {
		t.Fatalf("after clear = %+v", th)
	}
	if rows = rowsOf(); rows["thm_p"]["deadline"] != "" {
		t.Fatalf("cleared deadline must read \"\": %v", rows["thm_p"])
	}
	before := len(pathsOf(s))
	_, err := runTheme(t, s, "set", "thm_p", "name=Q", "deadline=2026-13-01")
	if err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
		t.Fatalf("bad date err = %v", err)
	}
	for _, p := range pathsOf(s)[before:] {
		if strings.HasPrefix(p, "PATCH /themes/thm_p") {
			t.Fatalf("a bad date reached the wire: %v", pathsOf(s)[before:])
		}
	}
	if th, _ := st.GetTheme("thm_p"); th.Name != "P" {
		t.Fatalf("name = %q, want P", th.Name)
	}
}

func TestThemeSetDeadlineFollowsScope(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemonWith(t, nil)
	if _, err := runTheme(t, s, "set", "thm_t", "carried=true"); err == nil {
		t.Fatal("perso cell set a work theme without --cross-scope")
	}
	if th, _ := st.GetTheme("thm_t"); th.Carried {
		t.Fatal("thm_t carried despite the refusal")
	}
	if _, err := runTheme(t, s, "set", "thm_t", "carried=true", "--cross-scope"); err != nil {
		t.Fatal(err)
	}
	if th, _ := st.GetTheme("thm_t"); !th.Carried {
		t.Fatal("thm_t not carried with --cross-scope")
	}

	withCells(t, func(work, _ string) string { return work })
	s, st = realSubjectDaemonWith(t, nil)
	if _, err := runTheme(t, s, "set", "thm_p", "deadline=2026-10-31"); err == nil {
		t.Fatal("work cell set a perso theme")
	}
	if th, _ := st.GetTheme("thm_p"); th.Deadline != "" {
		t.Fatalf("thm_p deadline = %q despite the refusal", th.Deadline)
	}
}

func TestThemeSetSaysAnOlderDaemonDroppedTheFields(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	mux := http.NewServeMux()
	mux.HandleFunc("/themes/thm_x", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, pigtail.Theme{ID: "thm_x", Name: "X", Kind: "project", VaultLink: "Perso/Projects/x"})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	for _, args := range [][]string{
		{"set", "thm_x", "deadline=2026-10-31"},
		{"set", "thm_x", "carried=true", "--json"},
	} {
		if _, err := runTheme(t, s, args...); err == nil || !strings.Contains(err.Error(), "make install") {
			t.Fatalf("%v: err = %v, want the make install hint", args, err)
		}
	}
}

// A pre-0034 daemon answers 400 "nothing to update" when only the new keys are sent.
func TestThemeSetOldDaemonNothingToUpdate(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	mux := http.NewServeMux()
	mux.HandleFunc("/themes/thm_x", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"INVALID_REQUEST","message":"nothing to update: provide name, vault_link and/or space"}`))
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	if _, err := runTheme(t, s, "set", "thm_x", "carried=false"); err == nil || !strings.Contains(err.Error(), "make install") {
		t.Fatalf("err = %v, want the make install hint", err)
	}
}
