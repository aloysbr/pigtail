package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration0020OffTopic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0020.db")
	db := migrateBefore(t, path, "0020")
	if _, err := db.Exec(`INSERT INTO journal (id, scope, ts, kind, subject_id, summary, source) VALUES ('j_old','perso',1,'note',NULL,'kept','test')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0020): %v", err)
	}
	defer s.Close()
	if _, err := s.db.Exec(`INSERT INTO workspace_off_topic (cmux_workspace_id, scope, declared_at) VALUES ('W','perso',1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO workspace_off_topic (cmux_workspace_id, scope, reason, declared_at) VALUES ('W2','perso','a` + "\n" + `b',1)`); err == nil {
		t.Fatal("newline in reason accepted")
	}
	if _, err := s.db.Exec(`INSERT INTO workspace_off_topic (cmux_workspace_id, scope, reason, declared_at) VALUES ('W5','perso',?,1)`, strings.Repeat("é", 201)); err == nil {
		t.Fatal("201-character reason accepted")
	}
	if _, err := s.db.Exec(`INSERT INTO workspace_off_topic (cmux_workspace_id, scope, reason, declared_at) VALUES ('W6','perso',?,1)`, strings.Repeat("é", 200)); err != nil {
		t.Fatalf("200-character reason (400 bytes) refused — length() must count characters: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO workspace_off_topic (cmux_workspace_id, scope, declared_at) VALUES ('W3','',1)`); err == nil {
		t.Fatal("empty scope accepted")
	}
	if _, err := s.db.Exec(`INSERT INTO workspace_off_topic (cmux_workspace_id, scope, declared_at) VALUES ('W4','Two Words',1)`); err == nil {
		t.Fatal("a scope that is not a lowercase identifier was accepted")
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM journal WHERE id = 'j_old' AND summary = 'kept'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("existing journal row lost by 0020: %d %v", n, err)
	}
}
