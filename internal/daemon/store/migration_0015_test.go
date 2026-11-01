package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// 0015 keeps every row and replaces the closed CHECKs by a shape check that
// refuses the empty scope and anything but a lowercase identifier.
func TestMigration0015KeepsScopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0015.db")
	db := migrateBefore(t, path, "0015")
	for _, q := range []string{
		`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_t','T','project','Globex/x',0,1,'globex'), ('thm_p','P','area','Perso/y',0,1,'perso')`,
		`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at) VALUES ('sub_t','thm_t','t','t','inbox',1,1)`,
		`INSERT INTO journal (id, scope, ts, kind, summary, source) VALUES ('j1','globex',1,'gate','s','pigtail:x'), ('j2','perso',1,'gate','s','pigtail:x')`,
		`INSERT INTO peer_sessions (session_id, scope, updated_at) VALUES ('a','globex',1), ('b','perso',1), ('c','',1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0015): %v", err)
	}
	defer s.Close()
	got := map[string]string{}
	for _, q := range []string{
		`SELECT 'themes:'||id, scope FROM themes`,
		`SELECT 'journal:'||id, scope FROM journal`,
		`SELECT 'peer:'||session_id, scope FROM peer_sessions`,
	} {
		rows, err := s.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			got[k] = v
		}
		rows.Close()
	}
	want := map[string]string{
		"themes:thm_t": "globex", "themes:thm_p": "perso",
		"journal:j1": "globex", "journal:j2": "perso",
		"peer:a": "globex", "peer:b": "perso", "peer:c": "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("rows after 0015 = %d, want %d (%v)", len(got), len(want), got)
	}
	// The subject still joins its theme (the rebuild kept ids and the FK target).
	if sc, err := s.SubjectScope("sub_t"); err != nil || sc != "globex" {
		t.Fatalf("SubjectScope(sub_t) = %q, %v", sc, err)
	}
	for _, bad := range []string{"", "Work", "two words"} {
		_, err := s.db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_x','X','area','X/',0,1,?)`, bad)
		if err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("themes.scope %q accepted (err=%v)", bad, err)
		}
		_, err = s.db.Exec(`INSERT INTO journal (id, scope, ts, kind, summary, source) VALUES ('jx', ?, 1, 'gate', 's', 'pigtail:x')`, bad)
		if err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("journal.scope %q accepted (err=%v)", bad, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_a','A','area','ACME/x',0,1,'acme')`); err != nil {
		t.Fatalf("a third scope must fit the schema: %v", err)
	}
	for _, idx := range []string{"idx_themes_scope", "idx_journal_ts", "idx_journal_scope_ts", "idx_journal_subject_ts"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after the rebuild", idx)
		}
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reports a violation after 0015")
	}
}

// The rebuilt tables keep their exact columns, in order.
func TestMigration0015KeepsColumns(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for table, want := range map[string]string{
		"themes":  "id,name,kind,vault_link,code_project_id,archived,created_at,scope,space,deadline,carried",
		"journal": "id,scope,ts,kind,subject_id,summary,source",
	} {
		rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for rows.Next() {
			var c string
			_ = rows.Scan(&c)
			cols = append(cols, c)
		}
		rows.Close()
		if got := strings.Join(cols, ","); got != want {
			t.Errorf("%s columns = %s, want %s", table, got, want)
		}
	}
}
