// Package storetest holds test helpers shared by the packages that test
// against the daemon's store (like net/http/httptest). It never imports the
// store: the store's own internal tests use it too.
package storetest

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite" // the store's driver
)

// FailFocusSeal makes every seal of a focus (ended_at set from NULL) fail
// from now on, through a trigger posed by a second connection to the
// database file; restore drops it. The store must be opened on a file: a
// second ":memory:" connection would see another, empty database.
func FailFocusSeal(t testing.TB, dbPath string) (restore func()) {
	t.Helper()
	if dbPath == ":memory:" || dbPath == "" {
		t.Fatal("storetest.FailFocusSeal needs a store opened on a file, not :memory:")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER test_fail_focus_seal BEFORE UPDATE OF ended_at ON focus_spaces
		WHEN OLD.ended_at IS NULL AND NEW.ended_at IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'test: focus seal refused'); END`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS test_fail_focus_seal`); err != nil {
			t.Fatal(err)
		}
	}
}
