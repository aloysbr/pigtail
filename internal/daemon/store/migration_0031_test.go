package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// seed0031 inserts one theme and one subject per status, straight in SQL on a
// base stopped before 0031.
func seed0031(t *testing.T, db *sql.DB, statuses ...string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO themes (id, name, kind, vault_link, created_at, scope) VALUES ('thm_m', 'M', 'project', 'Perso/Projects/m', 1, 'perso')`); err != nil {
		t.Fatalf("seed theme: %v", err)
	}
	for _, st := range statuses {
		if _, err := db.Exec(`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at) VALUES (?, 'thm_m', ?, ?, ?, 1, 1)`,
			"sub_"+st, st, st, st); err != nil {
			t.Fatalf("seed subject %s: %v", st, err)
		}
	}
}

// statusLogs returns subject id → texts of its status log lines.
func statusLogs(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query(`SELECT subject_id, text FROM subject_log WHERE kind = 'status'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			t.Fatal(err)
		}
		out[id] = append(out[id], text)
	}
	return out
}

// TestMigration0031MapsAndLogs (D8, D12): every old status maps by the D1
// table, each changed subject gets one status log line marked (migration),
// captures.from_prev_status follows, and the CHECK refuses an old word.
func TestMigration0031MapsAndLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0031.db")
	db := migrateBefore(t, path, "0031")
	want := map[string]string{
		"inbox": "inbox", "triaged": "inbox", "researching": "understanding", "spec": "designing",
		"building": "building", "review": "building", "in_progress": "building", "active": "building",
		"blocked": "parked", "paused": "parked", "done": "done", "parked": "parked",
		"dropped": "dropped", "captured": "captured", "understanding": "understanding", "designing": "designing",
	}
	olds := make([]string, 0, len(want))
	for k := range want {
		olds = append(olds, k)
	}
	seed0031(t, db, olds...)
	if _, err := db.Exec(`INSERT INTO captures (id, theme_id, text, from_subject_id, from_prev_status, created_at) VALUES ('cap_1', 'thm_m', 'x', 'sub_researching', 'researching', 1)`); err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0031): %v", err)
	}
	defer s.Close()

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM subjects`).Scan(&n); err != nil || n != 16 {
		t.Fatalf("subjects = %d, %v; want 16", n, err)
	}
	for old, nw := range want {
		var got string
		if err := s.db.QueryRow(`SELECT status FROM subjects WHERE id = ?`, "sub_"+old).Scan(&got); err != nil || got != nw {
			t.Errorf("%s → %q, %v; want %q", old, got, err, nw)
		}
	}
	logs := statusLogs(t, s.db)
	total := 0
	for id, texts := range logs {
		total += len(texts)
		old := strings.TrimPrefix(id, "sub_")
		if len(texts) != 1 || texts[0] != old+" → "+want[old]+" (migration)" {
			t.Errorf("%s log = %q, want [%q]", id, texts, old+" → "+want[old]+" (migration)")
		}
		if old == want[old] {
			t.Errorf("%s unchanged but logged: %q", id, texts)
		}
	}
	if total != 8 {
		t.Errorf("status log lines = %d, want 8: %v", total, logs)
	}
	if got := logs["sub_researching"]; len(got) != 1 || got[0] != "researching → understanding (migration)" {
		t.Errorf("sub_researching log = %q", got)
	}
	var prev string
	if err := s.db.QueryRow(`SELECT from_prev_status FROM captures WHERE id = 'cap_1'`).Scan(&prev); err != nil || prev != "understanding" {
		t.Errorf("captures.from_prev_status = %q, %v; want understanding", prev, err)
	}
	ins := `INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at) VALUES (?, 'thm_m', 't', 't', ?, 1, 1)`
	if _, err := s.db.Exec(ins, "sub_new_old", "researching"); err == nil {
		t.Error("status 'researching' accepted after 0031, want CHECK failure")
	}
	for _, st := range []string{"inbox", "understanding", "designing", "building", "done", "parked", "dropped", "captured"} {
		if _, err := s.db.Exec(ins, "sub_new_"+st, st); err != nil {
			t.Errorf("status %q refused after 0031: %v", st, err)
		}
	}
	for _, idx := range []string{"idx_subjects_theme", "idx_subjects_status"} {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s count = %d, %v; want 1", idx, n, err)
		}
	}
}

// TestMigration0031UnknownStatusRollsBack: a status outside the sixteen known
// values fails the CHECK; the whole migration is undone and the database kept
// as it was (Review Focus 1).
func TestMigration0031UnknownStatusRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0031.db")
	db := migrateBefore(t, path, "0031")
	seed0031(t, db, "wip", "researching")
	_ = db.Close()

	s, err := Open(path)
	if err == nil {
		_ = s.Close()
		t.Fatal("open with a 'wip' subject succeeded, want the migration to fail")
	}
	if !strings.Contains(err.Error(), "0031_subject_status_vocabulary.sql") {
		t.Errorf("error %q does not name 0031_subject_status_vocabulary.sql", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for id, want := range map[string]string{"sub_wip": "wip", "sub_researching": "researching"} {
		var got string
		if err := raw.QueryRow(`SELECT status FROM subjects WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q kept", id, got, err, want)
		}
	}
	if logs := statusLogs(t, raw); len(logs) != 0 {
		t.Errorf("status log lines after rollback: %v", logs)
	}
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE name = '0031_subject_status_vocabulary.sql'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("_migrations has 0031 (%d, %v), want absent", n, err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE name = '0030_subject_outputs.sql'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("_migrations 0030_subject_outputs.sql = %d, %v; want kept", n, err)
	}
}
