package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A migration that fails half-way leaves nothing behind and is not recorded.
func TestMigrationIsAtomic(t *testing.T) {
	db := openRaw(t)
	fsys := fstest.MapFS{
		"migrations/0001_ok.sql":  {Data: []byte(`CREATE TABLE a (x INTEGER);`)},
		"migrations/0002_bad.sql": {Data: []byte(`CREATE TABLE b (x INTEGER); INSERT INTO nope VALUES (1);`)},
	}
	if err := applyMigrations(db, fsys); err == nil {
		t.Fatal("broken migration applied without error")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'b'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("table b left behind by a failed migration (n=%d, err=%v)", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE name = '0002_bad.sql'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed migration recorded (n=%d, err=%v)", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE name = '0001_ok.sql'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("good migration not recorded (n=%d, err=%v)", n, err)
	}
}

// A table rebuild (drop a parent table that child rows reference) must not
// trip foreign keys, even on a connection where someone turned them on.
func TestMigrationRunsWithForeignKeysOff(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		"migrations/0001_init.sql": {Data: []byte(`CREATE TABLE p (id TEXT PRIMARY KEY);
CREATE TABLE c (pid TEXT, FOREIGN KEY (pid) REFERENCES p(id));
INSERT INTO p VALUES ('x'); INSERT INTO c VALUES ('x');`)},
		"migrations/0002_rebuild.sql": {Data: []byte(`CREATE TABLE p_new (id TEXT PRIMARY KEY);
INSERT INTO p_new SELECT id FROM p; DROP TABLE p; ALTER TABLE p_new RENAME TO p;`)},
	}
	if err := applyMigrations(db, fsys); err != nil {
		t.Fatalf("rebuild under foreign_keys=ON: %v", err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reports a violation after the rebuild")
	}
}
