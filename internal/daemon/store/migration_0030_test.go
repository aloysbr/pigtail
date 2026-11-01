package store

import (
	"path/filepath"
	"testing"
)

// TestMigration0030SubjectOutputs: the subject_outputs table exists with its
// CHECKs and its UNIQUE(subject_id, key); rows seeded before stay intact.
func TestMigration0030SubjectOutputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0030.db")
	db := migrateBefore(t, path, "0030")
	if _, err := db.Exec(`INSERT INTO journal (id, scope, ts, kind, subject_id, summary, source) VALUES ('j1','perso',1,'note','sub_p','kept','test')`); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0030): %v", err)
	}
	defer s.Close()

	ins := `INSERT INTO subject_outputs (id, subject_id, kind, path, url, key, title, ts, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1)`
	if _, err := s.db.Exec(ins, "out_1", "sub_p", "capture", "/a.png", nil, "/a.png", "a.png"); err != nil {
		t.Fatalf("valid insert: %v", err)
	}
	if _, err := s.db.Exec(ins, "out_2", "sub_p", "video", "/b.png", nil, "/b.png", "b.png"); err == nil {
		t.Error("kind 'video' accepted, want CHECK failure")
	}
	if _, err := s.db.Exec(ins, "out_3", "sub_p", "file", nil, nil, "k", "k"); err == nil {
		t.Error("row with neither path nor url accepted")
	}
	if _, err := s.db.Exec(ins, "out_4", "sub_p", "file", "/a.png", nil, "/a.png", "a.png"); err == nil {
		t.Error("second row with the same (subject_id, key) accepted, want UNIQUE failure")
	}
	if _, err := s.db.Exec(ins, "out_5", nil, "file", "/c", nil, "/c", "c"); err == nil {
		t.Error("row with a NULL subject_id accepted, want NOT NULL failure")
	}
	var summary string
	if err := s.db.QueryRow(`SELECT summary FROM journal WHERE id = 'j1'`).Scan(&summary); err != nil || summary != "kept" {
		t.Errorf("journal row after 0030 = %q, %v; want kept", summary, err)
	}

	// Replay: opening the store again on a base already at 0030 is a no-op
	// that keeps the rows.
	_ = s.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen at 0030: %v", err)
	}
	defer again.Close()
	var n int
	if err := again.db.QueryRow(`SELECT COUNT(*) FROM subject_outputs WHERE id = 'out_1'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows after reopen = %d, %v; want out_1 kept", n, err)
	}
}
