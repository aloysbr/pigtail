package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestThemeCLIShapeSpaces freezes the shape of the CLI (A7): command names,
// the --space flag and the JSON keys stay as they are; only the space table
// grows (green before and after the areas change).
func TestThemeCLIShapeSpaces(t *testing.T) {
	t.Parallel()
	var names []string
	for _, c := range themeCmd().Commands() {
		names = append(names, c.Name())
	}
	for _, want := range []string{"list", "create", "set", "delete", "spaces"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Errorf("theme lacks the %q command (have %q)", want, names)
		}
	}
	create, _, err := themeCmd().Find([]string{"create"})
	if err != nil || create.Flags().Lookup("space") == nil {
		t.Fatalf("theme create lost --space: %v", err)
	}
	for _, key := range []string{"space", "code_project_id", "id", "vault_link"} {
		if !strings.Contains(string(mustJSON(t, pigtail.Theme{ID: "x", Space: "s", CodeProjectID: "c", VaultLink: "v"})), `"`+key+`"`) {
			t.Errorf("Theme JSON lost the key %q", key)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestThemeSpacesAreas: the six aires (D4, D5) are declared per scope and
// `theme set <thm> space=<aire>` accepts them; an aire of the other scope is
// refused.
func TestThemeSpacesAreas(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s := spacesDaemon(t)
	out, err := runTheme(t, s, "spaces", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []pigtail.Space
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	byScope := map[string][]string{}
	for _, sp := range got {
		byScope[sp.Scope] = append(byScope[sp.Scope], sp.Name)
	}
	if want := []string{"Vineyard", "Meadow", "Greenhouse", "Nursery"}; !reflect.DeepEqual(byScope["globex"], want) {
		t.Fatalf("globex spaces = %q, want %q", byScope["globex"], want)
	}
	if want := []string{"Garden", "Allotment", "Compost", "Pond"}; !reflect.DeepEqual(byScope["perso"], want) {
		t.Fatalf("perso spaces = %q, want %q", byScope["perso"], want)
	}
	if _, err := runTheme(t, s, "set", "thm_t", "space=Vineyard", "--cross-scope"); err != nil {
		t.Fatalf("set thm_t space=Vineyard: %v", err)
	}
	if _, err := runTheme(t, s, "set", "thm_p", "space=Allotment"); err != nil {
		t.Fatalf("set thm_p space=Allotment: %v", err)
	}
	if _, err := runTheme(t, s, "set", "thm_p", "space=Pond"); err != nil {
		t.Fatalf("set thm_p space=Pond (D4 t2): %v", err)
	}
	if _, err := runTheme(t, s, "set", "thm_p", "space=Vineyard"); err == nil {
		t.Fatal("a perso theme must not take a globex aire")
	}
}

// TestRetiredSpaceNameRefused: the pre-taxonomy names are gone from the table
// (plan B, task 17): `theme set` refuses one as an unknown space and leaves the
// theme as it was.
func TestRetiredSpaceNameRefused(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s := spacesDaemon(t)
	if _, err := runTheme(t, s, "set", "thm_p", "space=Allotment"); err != nil {
		t.Fatalf("set thm_p space=Allotment: %v", err)
	}
	_, err := runTheme(t, s, "set", "thm_p", "space=Terrace")
	if err == nil || !strings.Contains(err.Error(), "unknown space") {
		t.Fatalf("space=Terrace: err = %v, want an unknown space refusal", err)
	}
	out, err := runTheme(t, s, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var themes []pigtail.Theme
	if err := json.Unmarshal([]byte(out), &themes); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	found := false
	for _, th := range themes {
		if th.ID == "thm_p" {
			found = true
			if th.Space != "Allotment" {
				t.Fatalf("thm_p space = %q after the refusal, want Allotment (intact)", th.Space)
			}
		}
	}
	if !found {
		t.Fatalf("thm_p missing from %s", out)
	}
}
