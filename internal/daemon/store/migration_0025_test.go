package store

import (
	"path/filepath"
	"testing"
)

// 0025: a database from before the migration gains the two focus tables; the
// CHECKs refuse a name holding a newline and the read-only alias scope.
func TestMigration0025FocusTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0025.db")
	db := migrateBefore(t, path, "0025")
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0025): %v", err)
	}
	defer s.Close()
	for _, table := range []string{"focus_spaces", "focus_members"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s: n=%d err=%v", table, n, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO focus_spaces (id, name, scope, created_at) VALUES ('fcs_ok', 'Ok', 'perso', 1)`); err != nil {
		t.Fatalf("valid row refused: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO focus_spaces (id, name, scope, created_at) VALUES ('fcs_nl', 'a' || char(10) || 'b', 'perso', 1)`); err == nil {
		t.Fatal("a name with a newline was accepted")
	}
	if _, err := s.db.Exec(`INSERT INTO focus_spaces (id, name, scope, created_at) VALUES ('fcs_bad', 'T', 'Two Words', 1)`); err == nil {
		t.Fatal("a scope that is not a lowercase identifier was accepted")
	}
}
