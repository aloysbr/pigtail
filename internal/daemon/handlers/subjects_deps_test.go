package handlers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seedDeps stores subjects id -> deps (all inbox, one theme) and returns the
// router and store.
func seedDeps(t *testing.T, graph map[string][]string) (http.Handler, *store.Store) {
	t.Helper()
	r, st := newTestRouter(t)
	themeID := seedThemeAPI(t, r)
	now := time.Now().UTC()
	for id, deps := range graph {
		s := &pigtail.Subject{ID: id, ThemeID: themeID, Title: id, Slug: id, Status: pigtail.StatusInbox, Deps: deps, Source: pigtail.SourceLocal, CreatedAt: now, UpdatedAt: now}
		if err := st.CreateSubject(s); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	return r, st
}

func wantDepRefusal(t *testing.T, code int, body []byte, msg string) {
	t.Helper()
	var b struct{ Code, Message string }
	_ = json.Unmarshal(body, &b)
	if code != http.StatusBadRequest || b.Code != string(pigtail.ErrInvalidRequest) || b.Message != msg {
		t.Fatalf("got %d %s %q, want 400 INVALID_REQUEST %q", code, b.Code, b.Message, msg)
	}
}

func TestPatchSubjectDepsRefusals(t *testing.T) {
	r, st := seedDeps(t, map[string][]string{
		"sub_A": nil, "sub_B": {"sub_A"}, "sub_C": {"sub_B"}, "sub_D": {"sub_C"},
		"sub_X": nil, "sub_Y": {"sub_X"}, "sub_Z": {"sub_X", "sub_Y"},
	})
	cases := []struct {
		name, id string
		deps     []string
		want     string
	}{
		{"unknown", "sub_A", []string{"sub_nope"}, "unknown dependency sub_nope"},
		{"self", "sub_A", []string{"sub_A"}, "a subject cannot depend on itself"},
		{"cycle 2", "sub_A", []string{"sub_B"}, "dependency cycle: sub_A → sub_B → sub_A"},
		{"cycle 4", "sub_A", []string{"sub_D"}, "dependency cycle: sub_A → sub_D → sub_C → sub_B → sub_A"},
	}
	for _, c := range cases {
		rec := doJSON(t, r, "POST", "/subjects/"+c.id, map[string]any{"deps": c.deps})
		wantDepRefusal(t, rec.Code, rec.Body.Bytes(), c.want)
		if got, _ := st.GetSubject(c.id); len(got.Deps) != 0 {
			t.Errorf("%s: subject changed: deps=%v", c.name, got.Deps)
		}
	}
	// A diamond is accepted.
	if rec := doJSON(t, r, "POST", "/subjects/sub_D", map[string]any{"deps": []string{"sub_C", "sub_A"}}); rec.Code != http.StatusOK {
		t.Fatalf("diamond = %d %s", rec.Code, rec.Body.String())
	}
}

func TestPatchSubjectDepsGrandfathered(t *testing.T) {
	r, st := seedDeps(t, map[string][]string{"sub_A": {"sub_gone", "sub_A"}, "sub_B": nil})
	rec := doJSON(t, r, "POST", "/subjects/sub_A", map[string]any{"title": "renamed", "deps": []string{"sub_gone", "sub_A"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("grandfathered deps resent = %d %s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetSubject("sub_A")
	if got.Title != "renamed" || !reflect.DeepEqual(got.Deps, []string{"sub_gone", "sub_A"}) {
		t.Fatalf("got %q %v", got.Title, got.Deps)
	}
	// Adding a good dependency beside them is fine too.
	if rec := doJSON(t, r, "POST", "/subjects/sub_A", map[string]any{"deps": []string{"sub_gone", "sub_A", "sub_B"}}); rec.Code != http.StatusOK {
		t.Fatalf("add beside grandfathered = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateSubjectDepsUnknown(t *testing.T) {
	r, _ := seedDeps(t, map[string][]string{"sub_A": nil})
	themeID := seedThemeAPI(t, r)
	rec := doJSON(t, r, "POST", "/subjects", map[string]any{"theme_id": themeID, "title": "n", "deps": []string{"sub_nope"}})
	wantDepRefusal(t, rec.Code, rec.Body.Bytes(), "unknown dependency sub_nope")
	// Self before not-found: the id is chosen by the caller.
	rec = doJSON(t, r, "POST", "/subjects", map[string]any{"id": "sub_new", "theme_id": themeID, "title": "n", "deps": []string{"sub_new"}})
	wantDepRefusal(t, rec.Code, rec.Body.Bytes(), "a subject cannot depend on itself")
	rec = doJSON(t, r, "POST", "/subjects", map[string]any{"theme_id": themeID, "title": "n", "deps": []string{"sub_A"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("known dep = %d %s", rec.Code, rec.Body.String())
	}
}

func getChains(t *testing.T, r http.Handler, id string) map[string]json.RawMessage {
	t.Helper()
	rec := doJSON(t, r, "GET", "/subjects/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", id, rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func idsOf(t *testing.T, raw map[string]json.RawMessage, key string) []string {
	t.Helper()
	v, ok := raw[key]
	if !ok {
		t.Fatalf("key %s absent", key)
	}
	var ids []string
	if err := json.Unmarshal(v, &ids); err != nil || ids == nil {
		t.Fatalf("key %s = %s (%v), want an array", key, v, err)
	}
	return ids
}

func TestGetSubjectDepChains(t *testing.T) {
	r, _ := seedDeps(t, map[string][]string{"sub_A": nil, "sub_B": {"sub_A"}, "sub_C": {"sub_B"}, "sub_Lone": nil})
	raw := getChains(t, r, "sub_B")
	if got := idsOf(t, raw, "blocked_by_all"); !reflect.DeepEqual(got, []string{"sub_A"}) {
		t.Errorf("blocked_by_all = %v", got)
	}
	if got := idsOf(t, raw, "blocks"); !reflect.DeepEqual(got, []string{"sub_C"}) {
		t.Errorf("blocks = %v", got)
	}
	raw = getChains(t, r, "sub_A")
	if got := idsOf(t, raw, "blocks_all"); !reflect.DeepEqual(got, []string{"sub_B", "sub_C"}) {
		t.Errorf("blocks_all = %v", got)
	}
	raw = getChains(t, r, "sub_Lone")
	for _, k := range []string{"blocked_by_all", "blocks", "blocks_all"} {
		if got := idsOf(t, raw, k); len(got) != 0 {
			t.Errorf("%s = %v, want []", k, got)
		}
	}
	// The list route never carries them.
	body := doJSON(t, r, "GET", "/subjects", nil).Body.String()
	for _, k := range []string{"blocked_by_all", "blocks_all", `"blocks"`} {
		if strings.Contains(body, k) {
			t.Errorf("GET /subjects carries %s", k)
		}
	}
}

func TestGetSubjectDepChainsScope(t *testing.T) {
	r, st := scopedFixture(t)
	// A perso subject carries a legacy dependency on a work one (grandfathered).
	legacy, _ := st.GetSubject("sub_p")
	legacy.Deps = []string{"sub_t"}
	if err := st.UpdateSubject(legacy); err != nil {
		t.Fatal(err)
	}
	// From work: sub_t is blocked by nothing; the perso dependent is not listed.
	rec := doAs(t, r, work, "GET", "/subjects/sub_t", nil)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	for _, k := range []string{"blocks", "blocks_all"} {
		if got := idsOf(t, raw, k); len(got) != 0 {
			t.Errorf("%s = %v, want [] (other scope)", k, got)
		}
	}
}

func TestPostSubjectDepChainKeysReadOnly(t *testing.T) {
	r, _ := seedDeps(t, map[string][]string{"sub_A": nil})
	for _, k := range []string{"blocked_by_all", "blocks", "blocks_all"} {
		rec := doJSON(t, r, "POST", "/subjects/sub_A", map[string]any{k: []string{"sub_A"}})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "nothing to update") {
			t.Errorf("%s alone = %d %s, want 400 nothing to update", k, rec.Code, rec.Body.String())
		}
	}
}
