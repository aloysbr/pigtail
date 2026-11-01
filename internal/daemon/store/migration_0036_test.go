package store

import (
	"path/filepath"
	"testing"
)

func TestMigration0036CreatesRoles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0036.db")
	db := migrateBefore(t, path, "0036")
	if _, err := db.Exec(`INSERT INTO journal (id, ts, kind, scope, summary, source) VALUES ('j1', 1, 'note', 'perso', 's', 'pigtail:x')`); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0036): %v", err)
	}
	defer s.Close()
	ins := `INSERT INTO roles (scope, name, subject_id, cadence_ms, grace_ms, stall_ms, created_at, updated_at) VALUES ('perso','a','sub',1,1,1,'x','x')`
	if _, err := s.db.Exec(ins); err != nil {
		t.Fatalf("roles insert: %v", err)
	}
	if _, err := s.db.Exec(ins); err == nil {
		t.Fatal("duplicate (scope, name) accepted")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal WHERE id = 'j1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("earlier rows lost: n=%d err=%v", n, err)
	}
}
