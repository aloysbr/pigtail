package store

import (
	"path/filepath"
	"testing"
)

// 0022 (D1, sub_x000000000000041): a theme created before the migration reads
// space = ” after it; a newline in a space is refused by the CHECK.
func TestMigration0022ThemeSpace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0022.db")
	db := migrateBefore(t, path, "0022")
	if _, err := db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope)
		VALUES ('thm_old','Old','project','Perso/Projects/old',0,1,'perso')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0022): %v", err)
	}
	defer s.Close()
	th, err := s.GetTheme("thm_old")
	if err != nil {
		t.Fatal(err)
	}
	if th.Space != "" {
		t.Fatalf("space after 0022 = %q, want empty", th.Space)
	}
	if _, err := s.db.Exec(`UPDATE themes SET space = 'a' || char(10) || 'b' WHERE id = 'thm_old'`); err == nil {
		t.Fatal("a space with a newline was accepted")
	}
	if _, err := s.db.Exec(`UPDATE themes SET space = 'Terrace' WHERE id = 'thm_old'`); err != nil {
		t.Fatalf("a plain space was refused: %v", err)
	}
}
