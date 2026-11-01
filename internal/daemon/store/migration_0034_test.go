package store

import (
	"path/filepath"
	"testing"
)

// 0034 (D1 of sub_x000000000000003): a theme from before the migration reads
// Deadline == "" and Carried == false; the CHECKs refuse a malformed date and
// a non-boolean flag; reopening the database replays nothing.
func TestMigration0034ThemeDeadlineCarried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0034.db")
	db := migrateBefore(t, path, "0034")
	if _, err := db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope)
		VALUES ('thm_old','Old','project','Perso/Projects/old',0,1,'perso')`); err != nil {
		t.Fatalf("seed theme: %v", err)
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0034): %v", err)
	}
	th, err := s.GetTheme("thm_old")
	if err != nil || th.Deadline != "" || th.Carried {
		t.Fatalf("old theme = %+v, %v; want Deadline empty, Carried false", th, err)
	}
	for _, bad := range []string{"soon", "2026-10-3", "2026-10-31x"} {
		if _, err := s.db.Exec(`UPDATE themes SET deadline = ? WHERE id = 'thm_old'`, bad); err == nil {
			t.Fatalf("deadline %q accepted, want the CHECK to refuse it", bad)
		}
	}
	if _, err := s.db.Exec(`UPDATE themes SET deadline = '2026-10-31' WHERE id = 'thm_old'`); err != nil {
		t.Fatalf("valid deadline refused: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE themes SET carried = 2 WHERE id = 'thm_old'`); err == nil {
		t.Fatal("carried = 2 accepted, want the CHECK to refuse it")
	}
	if _, err := s.db.Exec(`UPDATE themes SET carried = 1 WHERE id = 'thm_old'`); err != nil {
		t.Fatalf("carried = 1 refused: %v", err)
	}
	if th, err = s.GetTheme("thm_old"); err != nil || !th.Carried || th.Deadline != "2026-10-31" {
		t.Fatalf("theme = %+v, %v; want Carried true, Deadline 2026-10-31", th, err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s.Close()
}
