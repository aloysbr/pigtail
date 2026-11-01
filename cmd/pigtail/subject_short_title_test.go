package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestApplySubjectFieldShortTitle(t *testing.T) {
	t.Parallel()
	sub := &pigtail.Subject{ID: "sub_1"}
	if err := applySubjectField(sub, "short_title", "Sidebar pigtail"); err != nil {
		t.Fatal(err)
	}
	if sub.ShortTitle != "Sidebar pigtail" {
		t.Fatalf("short_title not applied: %q", sub.ShortTitle)
	}
	if err := applySubjectField(sub, "short_title", ""); err != nil || sub.ShortTitle != "" {
		t.Fatalf("short_title= must clear: %q, %v", sub.ShortTitle, err)
	}
}

// showJSON runs `subject show --json` and decodes it.
func showJSON(t *testing.T, s *stubDaemon, id string) (sub pigtail.Subject, displayTitle string) {
	t.Helper()
	out, err := runSubject(t, s, "show", id, "--json")
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var v struct {
		Subject      pigtail.Subject `json:"subject"`
		DisplayTitle string          `json:"display_title"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return v.Subject, v.DisplayTitle
}

// TestSubjectShortTitleSetShow: set short_title=, then show renders it; an
// unset subject renders the derived title; other writes keep it.
func TestSubjectShortTitleSetShow(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)

	if _, err := runSubject(t, s, "set", "sub_p", "short_title=Sidebar pigtail"); err != nil {
		t.Fatalf("set short_title: %v", err)
	}
	sub, effective := showJSON(t, s, "sub_p")
	if sub.ShortTitle != "Sidebar pigtail" || effective != "Sidebar pigtail" {
		t.Fatalf("show --json: subject.short_title=%q display_title=%q", sub.ShortTitle, effective)
	}
	out, err := runSubject(t, s, "show", "sub_p")
	if err != nil || !strings.Contains(out, "short: Sidebar pigtail\n") {
		t.Fatalf("show = %q, %v; want the short title line", out, err)
	}

	// Another field's read-modify-write keeps the short title.
	if _, err := runSubject(t, s, "set", "sub_p", "rank=3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSubject("sub_p"); got.ShortTitle != "Sidebar pigtail" {
		t.Fatalf("rank write dropped short_title: %q", got.ShortTitle)
	}

	// Over the limit: refused by the daemon, nothing written.
	if _, err := runSubject(t, s, "set", "sub_p", "short_title="+strings.Repeat("x", pigtail.ShortTitleMax+1)); err == nil {
		t.Fatal("33-character short_title accepted")
	}

	// Cleared: the derived title comes back, flagged as derived.
	if _, err := runSubject(t, s, "set", "sub_p", "short_title="); err != nil {
		t.Fatal(err)
	}
	sub, effective = showJSON(t, s, "sub_p")
	if sub.ShortTitle != "" || effective != "perso one" {
		t.Fatalf("after clear: subject.short_title=%q display_title=%q, want \"\" and the derived title", sub.ShortTitle, effective)
	}
	out, _ = runSubject(t, s, "show", "sub_p")
	if !strings.Contains(out, "short: perso one (derived)\n") {
		t.Fatalf("show after clear = %q", out)
	}
}

// TestSubjectCreateShortTitle: create --short-title stores it (not derived);
// an invalid one is refused by the daemon, loudly, and creates nothing.
func TestSubjectCreateShortTitle(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)

	out, err := runSubject(t, s, "create", "--theme", "thm_p", "--title", "Rendre lisibles les noms de sujets dans la sidebar cmux",
		"--short-title", "Noms de sujets", "--local", "--json")
	if err != nil {
		t.Fatalf("create --short-title: %v (%s)", err, out)
	}
	var created pigtail.Subject
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("create --json output %q: %v", out, err)
	}
	if got, _ := st.GetSubject(created.ID); got == nil || got.ShortTitle != "Noms de sujets" {
		t.Fatalf("stored short_title = %+v, want \"Noms de sujets\"", got)
	}
	if show, _ := runSubject(t, s, "show", created.ID); !strings.Contains(show, "short: Noms de sujets\n") {
		t.Fatalf("show = %q, want the explicit short title (not derived)", show)
	}

	// Criterion 6: without --short-title, the derived fallback is unchanged.
	plainOut, err := runSubject(t, s, "create", "--theme", "thm_p", "--title", "Sans titre court", "--local", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var plain pigtail.Subject
	_ = json.Unmarshal([]byte(plainOut), &plain)
	if show, _ := runSubject(t, s, "show", plain.ID); !strings.Contains(show, "(derived)") {
		t.Fatalf("show without --short-title = %q, want the derived label", show)
	}

	before, _ := st.ListSubjects(store.SubjectFilter{})
	for _, bad := range []string{strings.Repeat("x", pigtail.ShortTitleMax+1), "deux\nlignes"} {
		_, err := runSubject(t, s, "create", "--theme", "thm_p", "--title", "refusé", "--short-title", bad, "--local")
		if err == nil || !strings.Contains(err.Error(), "short_title") {
			t.Fatalf("short title %q: err = %v, want the daemon's invalid short_title message", bad, err)
		}
	}
	if after, _ := st.ListSubjects(store.SubjectFilter{}); len(after) != len(before) {
		t.Fatalf("a refused create wrote a subject: %d → %d", len(before), len(after))
	}
}
