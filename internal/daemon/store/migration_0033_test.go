package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// 0033 (sub_x000000000000033, D5): a subject from before the migration reads
// Space == ” (its theme's); the CHECK refuses a name of 65 characters or with
// a newline; reopening the database replays nothing.
func TestMigration0033SubjectSpace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0033.db")
	db := migrateBefore(t, path, "0033")
	if _, err := db.Exec(`INSERT INTO themes (id, name, kind, vault_link, created_at, scope)
		VALUES ('thm_o','o','area','Perso/o',1,'perso')`); err != nil {
		t.Fatalf("seed theme: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at)
		VALUES ('sub_old','thm_o','old','old','inbox',1,1)`); err != nil {
		t.Fatalf("seed subject: %v", err)
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0033): %v", err)
	}
	sub, err := s.GetSubject("sub_old")
	if err != nil || sub.Space != "" {
		t.Fatalf("old subject = %+v, %v; want Space empty", sub, err)
	}
	for _, bad := range []string{strings.Repeat("x", 65), "a\nb", "a\rb"} {
		if _, err := s.db.Exec(`UPDATE subjects SET space = ? WHERE id = 'sub_old'`, bad); err == nil {
			t.Fatalf("space %q accepted, want the CHECK to refuse it", bad)
		}
	}
	if _, err := s.db.Exec(`UPDATE subjects SET space = ? WHERE id = 'sub_old'`, strings.Repeat("x", 64)); err != nil {
		t.Fatalf("64 characters refused: %v", err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s.Close()
}
