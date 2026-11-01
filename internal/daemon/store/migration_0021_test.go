package store

import (
	"path/filepath"
	"testing"
)

// 0021 (D5, sub_x000000000000018): a title taken by hand before D5 goes back
// to pigtail; applied_title, prev_title and the description owner are kept.
func TestMigration0021TitleOwnerBackToPigtail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0021.db")
	db := migrateBefore(t, path, "0021")
	for _, q := range []string{
		`INSERT INTO subject_cmux_bindings (cmux_workspace_id, subject_id, source, bound_at, prev_title, prev_title_custom, applied_title, applied_description, title_owner, description_owner)
		 VALUES ('W1','sub_1','attach',1,'Avant',1,'Court','Desc','user','user')`,
		`INSERT INTO subject_cmux_bindings (cmux_workspace_id, subject_id, source, bound_at, applied_title, title_owner, description_owner)
		 VALUES ('W2','sub_1','attach',1,'Court','pigtail','pigtail')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0021): %v", err)
	}
	defer s.Close()
	b, err := s.GetCmuxBinding("W1")
	if err != nil {
		t.Fatal(err)
	}
	if b.TitleOwner != "pigtail" || b.DescriptionOwner != "user" || b.AppliedTitle != "Court" || b.PrevTitle != "Avant" || !b.PrevTitleCustom {
		t.Fatalf("W1 after 0021: %+v", b)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM subject_cmux_bindings WHERE title_owner = 'user'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("title_owner=user left: %d %v", n, err)
	}
}
