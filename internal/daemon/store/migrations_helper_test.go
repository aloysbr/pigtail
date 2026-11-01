package store

import (
	"database/sql"
	"sort"
	"testing"
)

// migrateBefore creates a SQLite database at path holding every embedded
// migration whose name sorts before `before` (e.g. "0011"), recorded in
// _migrations exactly as Open records them. The caller seeds rows the way an
// older database holds them, closes db, then calls Open to apply the rest.
func migrateBefore(t *testing.T, path, before string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE _migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if name >= before {
			break
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO _migrations (name, applied_at) VALUES (?, 1)`, name); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
