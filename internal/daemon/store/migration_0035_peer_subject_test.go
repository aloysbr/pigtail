package store

import (
	"path/filepath"
	"testing"
)

// 0035 (D2 of sub_x000000000000021): an empty peer_sessions.subject_id is
// filled from the session's cmux workspace binding when the binding is not
// newer than the row; a non-empty value is never overwritten; reopening
// replays nothing.
func TestMigration0035BackfillsPeerSubject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0035.db")
	db := migrateBefore(t, path, "0035_peer")
	const ws = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"
	for _, q := range []string{
		`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_p','P','project','Perso/Projects/p',0,1,'perso')`,
		`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at) VALUES ('sub_a','thm_p','A','a','inbox',1,1)`,
		`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at) VALUES ('sub_x','thm_p','X','x','inbox',1,1)`,
		`INSERT INTO subject_cmux_bindings (cmux_workspace_id, subject_id, source, bound_at) VALUES ('` + ws + `','sub_a','attach',100)`,
		`INSERT INTO peer_sessions (session_id, cmux_workspace_id, subject_id, updated_at) VALUES ('fill', lower('` + ws + `'), '', 200)`,
		`INSERT INTO peer_sessions (session_id, cmux_workspace_id, subject_id, updated_at) VALUES ('old', '` + ws + `', '', 50)`,
		`INSERT INTO peer_sessions (session_id, cmux_workspace_id, subject_id, updated_at) VALUES ('kept', '` + ws + `', 'sub_x', 200)`,
		`INSERT INTO peer_sessions (session_id, cmux_workspace_id, subject_id, updated_at) VALUES ('nows', '', '', 200)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0035): %v", err)
	}
	want := map[string]string{"fill": "sub_a", "old": "", "kept": "sub_x", "nows": ""}
	for id, w := range want {
		if got, err := peerStamp(s, id); err != nil || got != w {
			t.Errorf("%s stamp = %q, %v; want %q", id, got, err, w)
		}
	}
	// Reopening replays nothing: a stamp cleared since stays cleared.
	if _, err := s.db.Exec(`UPDATE peer_sessions SET subject_id = '' WHERE session_id = 'fill'`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if got, _ := peerStamp(s, "fill"); got != "" {
		t.Fatalf("reopen replayed the backfill: fill = %q", got)
	}
}
