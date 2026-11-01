package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestShortTitleMigration opens a database at schema 0010 holding a subject:
// 0011 adds short_title as NULL, which reads back as "" (derive from title).
func TestShortTitleMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-short-title.db")
	db := migrateBefore(t, path, "0011")
	if _, err := db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_p', 'P', 'area', 'Perso/Areas/p', 0, 1, 'perso')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at, source) VALUES ('sub_old', 'thm_p', 'Trier les workspaces cmux par attention', 'trier', 'inbox', 1, 1, 'local')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0011): %v", err)
	}
	defer s.Close()
	sub, err := s.GetSubject("sub_old")
	if err != nil {
		t.Fatal(err)
	}
	if sub.ShortTitle != "" {
		t.Fatalf("pre-existing subject short_title = %q, want empty", sub.ShortTitle)
	}
	if got := pigtail.ShortTitle(*sub); got != "Trier les workspaces cmux…" {
		t.Fatalf("derived short title = %q", got)
	}
}

func TestSubjectShortTitleRoundTrip(t *testing.T) {
	s := newTestStore(t)
	seedScoped(t, s)
	now := time.UnixMilli(10).UTC()
	sub := &pigtail.Subject{ID: "sub_1", ThemeID: "thm_p", Title: "Enrichir la sidebar cmux avec l'état pigtail",
		Slug: "sidebar", Status: pigtail.StatusInbox, ShortTitle: "Sidebar pigtail", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateSubject(sub); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSubject("sub_1")
	if err != nil || got.ShortTitle != "Sidebar pigtail" {
		t.Fatalf("after create: %+v, %v", got, err)
	}
	list, err := s.ListSubjects(SubjectFilter{Scope: "perso"})
	if err != nil || len(list) != 1 || list[0].ShortTitle != "Sidebar pigtail" {
		t.Fatalf("list: %+v, %v", list, err)
	}

	got.ShortTitle = "Sidebar v2"
	if err := s.UpdateSubject(got); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetSubject("sub_1"); got.ShortTitle != "Sidebar v2" {
		t.Fatalf("after update: %q", got.ShortTitle)
	}

	// "" goes back to NULL: the title is derived again.
	got.ShortTitle = ""
	if err := s.UpdateSubject(got); err != nil {
		t.Fatal(err)
	}
	var isNull bool
	if err := s.db.QueryRow(`SELECT short_title IS NULL FROM subjects WHERE id = 'sub_1'`).Scan(&isNull); err != nil {
		t.Fatal(err)
	}
	if !isNull {
		t.Fatal(`short_title "" must be stored as NULL`)
	}
}
