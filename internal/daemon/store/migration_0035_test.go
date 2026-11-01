package store

import (
	"path/filepath"
	"testing"
)

// 0035 (sub_x000000000000031): the table exists after the migration, its
// CHECKs refuse an empty or multi-line query, a malformed scope and
// a half-written pick; reopening replays nothing.
func TestMigration0035SubjectFinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0035.db")
	db := migrateBefore(t, path, "0035")
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0035): %v", err)
	}
	ok := `INSERT INTO subject_finds (id, scope, ts, query) VALUES ('sf_ok', 'perso', 1, 'noms de sujets')`
	if _, err := s.db.Exec(ok); err != nil {
		t.Fatalf("valid row refused: %v", err)
	}
	for name, q := range map[string]string{
		"empty query":    `INSERT INTO subject_finds (id, scope, ts, query) VALUES ('sf_1', 'perso', 1, '')`,
		"newline":        `INSERT INTO subject_finds (id, scope, ts, query) VALUES ('sf_2', 'perso', 1, 'a' || char(10) || 'b')`,
		"bad scope":      `INSERT INTO subject_finds (id, scope, ts, query) VALUES ('sf_3', 'Two Words', 1, 'x')`,
		"half pick":      `INSERT INTO subject_finds (id, scope, ts, query, picked_subject_id) VALUES ('sf_4', 'perso', 1, 'x', 'sub_x')`,
		"query too long": `INSERT INTO subject_finds (id, scope, ts, query) VALUES ('sf_5', 'perso', 1, printf('%.501c', 'a'))`,
	} {
		if _, err := s.db.Exec(q); err == nil {
			t.Errorf("%s accepted, want the CHECK to refuse it", name)
		}
	}
	_ = s.Close()
	if s, err = Open(path); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s.Close()
}
