package store

import (
	"path/filepath"
	"testing"
)

// TestMigration0030ClosedBy: wait_records gains closed_by, NULL on every
// existing row (closed by the user's prompt or any path but a session end).
func TestMigration0030ClosedBy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0030.db")
	db := migrateBefore(t, path, "0030")
	if _, err := db.Exec(`INSERT INTO wait_records (id, session_id, source, type, state, opened_at, closed_at, owner) VALUES ('wr_old','s1','declared','review','cleared',1,2,'session:s1')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0030): %v", err)
	}
	defer s.Close()
	var by *string
	if err := s.db.QueryRow(`SELECT closed_by FROM wait_records WHERE id = 'wr_old'`).Scan(&by); err != nil {
		t.Fatalf("closed_by column: %v", err)
	}
	if by != nil {
		t.Fatalf("closed_by of an old row = %q, want NULL", *by)
	}
}

// TestMigration0030TwoFilesApplyInOrder: two migrations carry the number 0030
// (subject_outputs from main, wait_records_closed_by from this line). They are
// tracked by filename, so both apply on a fresh DB, in filename order, and a
// DB that recorded only wait_records_closed_by (the real one) gets the other
// on its next open. The filenames are frozen: never renumber.
func TestMigration0030TwoFilesApplyInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT name FROM _migrations WHERE name LIKE '0030_%' ORDER BY applied_at, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		got = append(got, n)
	}
	_ = rows.Close()
	want := []string{"0030_subject_outputs.sql", "0030_wait_records_closed_by.sql"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("0030 migrations applied = %v, want %v", got, want)
	}
	// The real DB: closed_by recorded, subject_outputs not yet.
	if _, err := s.db.Exec(`DROP TABLE subject_outputs`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM _migrations WHERE name = '0030_subject_outputs.sql'`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen with only closed_by recorded: %v", err)
	}
	defer again.Close()
	var n int
	if err := again.db.QueryRow(`SELECT COUNT(*) FROM subject_outputs`).Scan(&n); err != nil {
		t.Fatalf("subject_outputs not applied after reopen: %v", err)
	}
}
