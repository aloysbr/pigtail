package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestMigration0027 checks the wait_records table (sub_x000000000000026):
// its columns, an idempotent second application, and the invariant of at
// most one open wait per session and per source.
func TestMigration0027(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0027.db")
	db := migrateBefore(t, path, "0027")
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0027): %v", err)
	}
	cols := map[string]bool{}
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('wait_records')`)
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
	for _, c := range []string{"id", "session_id", "source", "scope", "subject_id", "type", "reason",
		"details", "state", "opened_at", "closed_at", "snoozed_until", "answer",
		"owner", "message_id", "delivered_to", "delivered_at"} {
		if !cols[c] {
			t.Fatalf("column %s missing: %v", c, cols)
		}
	}

	// insert writes a record with an explicit owner (D28: the subject in its
	// scope for an answerable declared wait bound to a subject, else the
	// session).
	insert := func(id, session, source, sc, subject, typ, owner, state string) error {
		_, err := s.db.Exec(`INSERT INTO wait_records (id, session_id, source, scope, subject_id, type, owner, state, opened_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`, id, session, source, sc, subject, typ, owner, state)
		return err
	}
	ok := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	refused := func(name string, err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %s", name, err, want)
		}
	}
	ok("first open", insert("wait_1", "s1", "declared", "", "", "question", "session:s1", "open"))
	refused("second open declared wait of s1", insert("wait_2", "s1", "declared", "", "", "question", "session:s1", "open"), "UNIQUE")
	ok("an open auto beside the open declared", insert("wait_3", "s1", "auto", "", "", "question", "session:s1", "open"))
	ok("a closed wait beside the open one", insert("wait_4", "s1", "declared", "", "", "question", "session:s1", "replaced"))
	refused("source held", insert("wait_5", "s2", "held", "", "", "question", "session:s2", "open"), "CHECK")
	refused("state gone", insert("wait_6", "s2", "declared", "", "", "question", "session:s2", "gone"), "CHECK")

	// One open per subject in its scope (D29-B).
	ok("arbitrage of A on S", insert("wait_10", "sa", "declared", "perso", "sub_s", "arbitrage", "subject:perso:sub_s", "open"))
	refused("arbitrage of B on S, same scope", insert("wait_11", "sb", "declared", "perso", "sub_s", "arbitrage", "subject:perso:sub_s", "open"), "UNIQUE")
	ok("arbitrage of C on S, another scope", insert("wait_12", "sc", "declared", "globex", "sub_s", "arbitrage", "subject:globex:sub_s", "open"))
	ok("no subject, session D", insert("wait_13", "sd", "declared", "perso", "", "arbitrage", "session:sd", "open"))
	ok("no subject, session E", insert("wait_14", "se", "declared", "perso", "", "arbitrage", "session:se", "open"))
	// One open per session and source, whatever the owner.
	ok("F on subject T", insert("wait_15", "sf", "declared", "perso", "sub_t", "review", "subject:perso:sub_t", "open"))
	refused("F again as its own owner", insert("wait_16", "sf", "declared", "perso", "", "working", "session:sf", "open"), "UNIQUE")
	// Two auto dialogs of two sessions on one subject: the session owns each.
	ok("auto of G on S", insert("wait_17", "sg", "auto", "perso", "sub_s", "question", "session:sg", "open"))
	ok("auto of H on S", insert("wait_18", "sh", "auto", "perso", "sub_s", "question", "session:sh", "open"))
	// The CHECK keeps the owner rule.
	refused("working owned by its subject", insert("wait_19", "si", "declared", "perso", "sub_u", "working", "subject:perso:sub_u", "open"), "CHECK")
	refused("arbitrage on a subject owned by its session", insert("wait_20", "sj", "declared", "perso", "sub_u", "arbitrage", "session:x", "open"), "CHECK")

	body, err := migrationsFS.ReadFile("migrations/0027_wait_records.sql")
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
