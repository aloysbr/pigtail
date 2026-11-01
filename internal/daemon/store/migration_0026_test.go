package store

import (
	"path/filepath"
	"testing"
	"time"
)

// 0026 (sub_x000000000000029, D7, D9): a database from before the migration
// gains session_prio with its three columns; applying it twice is harmless.
func TestMigration0026(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0026.db")
	db := migrateBefore(t, path, "0026")
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0026): %v", err)
	}
	cols := map[string]bool{}
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('session_prio')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		cols[n] = true
	}
	_ = rows.Close()
	for _, c := range []string{"session_id", "since", "missing_ticks"} {
		if !cols[c] {
			t.Fatalf("column %s missing: %v", c, cols)
		}
	}
	body, err := migrationsFS.ReadFile("migrations/0026_session_prio.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(string(body)); err != nil {
		t.Fatalf("second application: %v", err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s.Close()
}

// EnterStandby no longer reads nor writes session_prio (D3 of
// sub_x000000000000014: the prio is cmux's pin): a row left in the 0026 table
// stays, and EnterStandby reports nothing about a Prio (its signature has no
// prioEnded any more).
func TestEnterStandbyNoPrio(t *testing.T) {
	st := newTestStore(t)
	enter := (func(string, time.Time) error)(st.EnterStandby) // the signature is the check
	if _, err := st.db.Exec(`INSERT INTO session_prio (session_id, since) VALUES ('s1', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := enter("s1", time.UnixMilli(5)); err != nil {
		t.Fatal(err)
	}
	if sb, err := st.GetStandby("s1"); err != nil || sb == nil {
		t.Fatalf("standby = %+v, %v", sb, err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM session_prio WHERE session_id = 's1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("session_prio rows = %d, %v: EnterStandby touched the table", n, err)
	}
}
