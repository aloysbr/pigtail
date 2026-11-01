package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var suggestKinds = []string{"delivery", "phase", "handoff", "progress", "note", "decision"}

func TestSuggestLogLines(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	for _, id := range []string{"a", "b"} {
		if err := s.CreateSubject(mkSubject(id, themeID, pigtail.StatusBuilding, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for i, e := range []pigtail.SubjectLogEntry{
		{SubjectID: "a", Kind: "phase", Text: "draft PR globex/monorepo#9225"},
		{SubjectID: "b", Kind: "message", Text: "as #9225 did"},
		{SubjectID: "b", Kind: "phase", Text: "unrelated 1234"},
		{SubjectID: "b", Kind: "progress", Text: "on feat/x_y"},
	} {
		e.ID = "log_" + string(rune('a'+i))
		e.Timestamp = time.UnixMilli(int64(i + 1)).UTC()
		if err := s.AppendLog(&e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.SuggestLogLines([]string{"9225"}, suggestKinds)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SubjectID != "a" || got[0].Kind != "phase" || got[0].Text != "draft PR globex/monorepo#9225" {
		t.Fatalf("got %+v, want a's phase line only", got)
	}

	got, err = s.SuggestLogLines([]string{"nope", "feat/x_y"}, suggestKinds)
	if err != nil || len(got) != 1 || got[0].SubjectID != "b" {
		t.Fatalf("second needle: %v %+v", err, got)
	}
	// LIKE wildcards in a needle are literal.
	got, err = s.SuggestLogLines([]string{"feat/x%y"}, suggestKinds)
	if err != nil || len(got) != 0 {
		t.Fatalf("wildcard needle: %v %+v", err, got)
	}

	// No needle (or no kind): no line and no query at all — proven on a
	// closed store, where any query would fail.
	_ = s.Close()
	for _, args := range [][2][]string{{nil, suggestKinds}, {{"9225"}, nil}, {{""}, suggestKinds}} {
		got, err := s.SuggestLogLines(args[0], args[1])
		if err != nil || len(got) != 0 {
			t.Fatalf("SuggestLogLines(%q, %q) = %+v, %v; want nothing, no query", args[0], args[1], got, err)
		}
	}
}

func TestSuggestLinkedWorktrees(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProject(&pigtail.Project{
		ID: "prj_test", Name: "test", RootPath: "/tmp/test", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{},
	}); err != nil {
		t.Fatal(err)
	}
	themeID := seedTheme(t, s)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.CreateSubject(mkSubject(id, themeID, pigtail.StatusBuilding, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateWorkspace(&pigtail.Workspace{ID: "ws_a", ProjectID: "prj_test", State: pigtail.StateBuilding, Path: "/dev/wt/a", BackendKind: "git"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []pigtail.SubjectPhase{
		{ID: "ph_a", SubjectID: "a", Kind: "implement", State: "active", WorkspaceID: "ws_a"},
		{ID: "ph_b", SubjectID: "b", Kind: "implement", State: "active", WorkspaceID: "ws_ghost"},
		{ID: "ph_c", SubjectID: "c", Kind: "spec", State: "active"},
	} {
		p.CreatedAt = time.Now()
		if err := s.CreatePhase(&p); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SuggestLinkedWorktrees()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SubjectID != "a" || got[0].Path != "/dev/wt/a" {
		t.Fatalf("got %+v, want a → /dev/wt/a only", got)
	}
}
