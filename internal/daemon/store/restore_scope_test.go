package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail db list` must be able to count subjects per scope, so a work cell
// is shown work subjects only.
func TestInspectGenerationCountsSubjectsPerScope(t *testing.T) {
	db := filepath.Join(t.TempDir(), "pigtail.db")
	st := seededStore(t, db)
	now := time.Now()
	for _, th := range []pigtail.Theme{
		{ID: "thm_t", Name: "T", Kind: "project", VaultLink: "Globex/Projects/t", CreatedAt: now},
		{ID: "thm_p", Name: "P", Kind: "area", VaultLink: "Perso/Areas/p", CreatedAt: now},
	} {
		th := th
		if err := st.CreateTheme(&th); err != nil {
			t.Fatal(err)
		}
	}
	for i, theme := range []string{"thm_t", "thm_p", "thm_p", "thm_p"} {
		sub := pigtail.Subject{ID: "sub_" + string(rune('a'+i)), ThemeID: theme, Title: "s", Slug: "s" + string(rune('a'+i)),
			Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}
		if err := st.CreateSubject(&sub); err != nil {
			t.Fatal(err)
		}
	}
	got := InspectGeneration(db, LiveGeneration)
	if got.Subjects != 4 || len(got.SubjectsByScope) != 2 || got.SubjectsByScope["globex"] != 1 || got.SubjectsByScope["perso"] != 3 {
		t.Fatalf("subjects = %d by scope %v, want 4, globex:1 perso:3", got.Subjects, got.SubjectsByScope)
	}
}

// A generation from before migration 0010 has no themes.scope column: the
// split falls back to the rule 0010 backfilled with (Globex/ prefix = work,
// anything else perso), and a subject whose theme is gone counts as perso.
func TestInspectGenerationScopesAPre0010Generation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE _migrations (name TEXT)`, `INSERT INTO _migrations VALUES ('0009')`,
		`CREATE TABLE themes (id TEXT, vault_link TEXT)`,
		`CREATE TABLE subjects (id TEXT, theme_id TEXT)`,
		`INSERT INTO themes VALUES ('thm_t','Globex/x'), ('thm_p','Perso/y'), ('thm_o','Areas/z')`,
		`INSERT INTO subjects VALUES ('s1','thm_t'), ('s2','thm_t'), ('s3','thm_p'), ('s4','thm_o'), ('s5','thm_gone')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()
	got := InspectGeneration(path, 0)
	if got.Subjects != 5 || len(got.SubjectsByScope) != 2 || got.SubjectsByScope["globex"] != 2 || got.SubjectsByScope["perso"] != 3 {
		t.Fatalf("subjects = %d by scope %v, want 5, globex:2 perso:3", got.Subjects, got.SubjectsByScope)
	}
}

// Before migration 0006 there is no subjects table at all: every count is
// unknown, not zero.
func TestInspectGenerationScopeCountsUnknownWithoutSubjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "older.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE _migrations (name TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	got := InspectGeneration(path, 0)
	if got.Subjects != -1 || got.SubjectsByScope != nil {
		t.Fatalf("counts = %d / %v, want -1 / nil", got.Subjects, got.SubjectsByScope)
	}
}

// A generation saved before migration 0015 stores the alias, a current one
// globex: both count in the same column.
func TestInspectGenerationCountsAliasAndGlobex(t *testing.T) {
	for _, stored := range []string{"gx", "globex"} {
		t.Run(stored, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gen.db")
			var db *sql.DB
			if stored == "gx" {
				db = migrateBefore(t, path, "0015")
			} else {
				db = migrateBefore(t, path, "9999")
			}
			for _, q := range []string{
				`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_t','T','project','Globex/x',0,1,'` + stored + `')`,
				`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at) VALUES ('sub_t','thm_t','t','t','inbox',1,1)`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			_ = db.Close()
			got := InspectGeneration(path, 0)
			if got.Subjects != 1 || len(got.SubjectsByScope) != 1 || got.SubjectsByScope["globex"] != 1 {
				t.Fatalf("stored %s: subjects = %d by scope %v, want 1, globex:1", stored, got.Subjects, got.SubjectsByScope)
			}
		})
	}
}

// A generation written before 0015 holds the alias 'gx': it counts under globex.
func TestInspectGenerationReadsAPre0015Generation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0015.db")
	db := migrateBefore(t, path, "0015")
	for _, q := range []string{
		`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_t','T','project','Globex/x',0,1,'gx')`,
		`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at) VALUES ('s1','thm_t','t','t','inbox',1,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	got := InspectGeneration(path, LiveGeneration)
	if got.SubjectsByScope["globex"] != 1 || got.SubjectsByScope["gx"] != 0 {
		t.Fatalf("SubjectsByScope = %v, want globex:1 and no gx key", got.SubjectsByScope)
	}
	if _, ok := got.SubjectsByScope["gx"]; ok {
		t.Fatalf("SubjectsByScope = %v has a gx key", got.SubjectsByScope)
	}
}
