package main

import (
	"encoding/json"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `subject set` sends only the fields it was given, so a field this binary
// does not know (or was not asked to touch) is never part of the body.
func TestSubjectPatchSendsOnlyNamedKeys(t *testing.T) {
	t.Parallel()
	sub := pigtail.Subject{ID: "sub_1", ThemeID: "thm_a", Title: "T", Slug: "t", Status: "inbox", BoardCard: "ABC-1", VaultPath: "v", Rank: 3}
	for _, kv := range [][2]string{{"status", "building"}, {"impact", "4"}, {"theme", "thm_b"}, {"board_card", ""}} {
		if err := applySubjectField(&sub, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := subjectPatch(sub, []string{"status", "impact", "theme", "board_card"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	want := map[string]any{"status": "building", "impact": float64(4), "theme_id": "thm_b", "board_card": nil}
	if len(body) != len(want) {
		t.Fatalf("body = %v, want exactly %v", body, want)
	}
	for k, v := range want {
		if bv, ok := body[k]; !ok || bv != v {
			t.Errorf("body[%q] = %v (present=%v), want %v", k, bv, ok, v)
		}
	}
}

// A key reset to its zero value goes out as null, the daemon's reset.
func TestSubjectPatchZeroIntIsNull(t *testing.T) {
	t.Parallel()
	sub := pigtail.Subject{ID: "sub_1", Rank: 5}
	if err := applySubjectField(&sub, "rank", "0"); err != nil {
		t.Fatal(err)
	}
	got, err := subjectPatch(sub, []string{"rank"})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got["rank"]; !ok || v != nil {
		t.Fatalf("rank = %v (present=%v), want null", v, ok)
	}
}
