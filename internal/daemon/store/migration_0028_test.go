package store

import (
	"path/filepath"
	"testing"
)

// TestMigration0028KindOngoing: a theme of kind "area" becomes "ongoing"; a "project" theme is untouched.
func TestMigration0028KindOngoing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0028.db")
	db := migrateBefore(t, path, "0028")
	if _, err := db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_a','A','area','Perso/Areas/a',0,1,'perso'), ('thm_p','P','project','Globex/Projects/p',0,1,'globex')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0028): %v", err)
	}
	defer s.Close()
	for id, want := range map[string]string{"thm_a": "ongoing", "thm_p": "project"} {
		var got string
		if err := s.db.QueryRow(`SELECT kind FROM themes WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s kind = %q, want %q", id, got, want)
		}
	}
}
