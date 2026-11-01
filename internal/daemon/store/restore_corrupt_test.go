package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The restore tests that came with the command all start from a *healthy* live
// database: they prove the snapshot is consistent, that the generation is left
// in place, that stale sidecars are cleared, and that a corrupt *generation* is
// refused. None of them starts from a broken *live* database — the only
// situation `pigtail db restore` exists for.
//
// This file closes that gap from the store's side; internal/daemon closes the
// other half (a daemon actually starting on the result).

// corruptLive truncates a database file in half, keeping a valid header over
// pages that are no longer there. This is the shape a crash mid-write leaves,
// and — unlike a zero-length file — SQLite recognises it as damage rather than
// as a database it may create from scratch.
func corruptLive(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() < 4096 {
		t.Fatalf("database is %d bytes; too small for a meaningful truncation", fi.Size())
	}
	if err := os.Truncate(path, fi.Size()/2); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreRecoversACorruptLiveDatabase walks the whole recovery path in the
// order an operator does: notice the live database is broken, look at what the
// generations hold, restore, reopen.
//
// The assertion that makes the rest mean anything is the second one: the
// truncated file must genuinely refuse to open. Without it the test would still
// pass on a database that was never broken, and would prove nothing.
func TestRestoreRecoversACorruptLiveDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")

	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	// Written after the backup, so its absence from the restored database is
	// the proof that the restored file is the generation and not the live one
	// somehow surviving.
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_lost", Name: "lost-to-the-crash", RootPath: "/tmp/lost", DefaultBackend: "git",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	corruptLive(t, db)

	if broken, err := Open(db); err == nil {
		_ = broken.Close()
		t.Fatal("the truncated database still opens; the rest of this test would prove nothing")
	}

	// What the operator sees first: `pigtail db list` must show the live row as
	// damaged and the generation as usable, or there is no way to choose.
	rows := Inspect(db, BackupGenerations)
	if len(rows) < 2 {
		t.Fatalf("Inspect returned %d rows, want the live database and at least one generation", len(rows))
	}
	if rows[0].Generation != LiveGeneration {
		t.Fatalf("first row is generation %d, want the live database", rows[0].Generation)
	}
	if rows[0].IntegrityOK {
		t.Fatal("the corrupt live database passed its integrity check")
	}
	if rows[0].IntegrityErr == "" {
		t.Fatal("the corrupt live database reports no reason; the operator has nothing to go on")
	}
	if !rows[1].IntegrityOK || !rows[1].HasMigrations {
		t.Fatalf("generation .0 is not usable: IntegrityOK=%v HasMigrations=%v err=%q",
			rows[1].IntegrityOK, rows[1].HasMigrations, rows[1].IntegrityErr)
	}

	res, err := Restore(db, 0, restoreClock())
	if err != nil {
		t.Fatalf("Restore over a corrupt live database: %v", err)
	}

	restored, err := Open(db)
	if err != nil {
		t.Fatalf("open the restored database: %v", err)
	}
	defer restored.Close() //nolint:errcheck
	if _, err := restored.GetProject("prj_backup"); err != nil {
		t.Fatalf("the restored database lost the backed-up row: %v", err)
	}
	if _, err := restored.GetProject("prj_lost"); err == nil {
		t.Fatal("the restored database holds a row written after the backup")
	}

	// The broken file is kept, not deleted: it is the only copy of whatever was
	// written since the last generation, and forensics on it may still be worth
	// more than the hour it represents.
	if res.PreRestorePath == "" {
		t.Fatal("PreRestorePath is empty; the corrupt database was not kept")
	}
	kept, err := os.Stat(res.PreRestorePath)
	if err != nil {
		t.Fatalf("stat the set-aside corrupt database: %v", err)
	}
	if kept.Size() == 0 {
		t.Fatal("the set-aside corrupt database is empty; it was not kept as found")
	}
	if info := InspectGeneration(res.PreRestorePath, LiveGeneration); info.IntegrityOK {
		t.Fatal("the set-aside file passes its integrity check; it is not the corrupt database")
	}
}

// TestRestoreRefusesACorruptGenerationOverACorruptLiveDatabase is the guard on
// the other side of the same move: a generation that is itself broken must not
// be put over a live database, corrupt or not. Already covered for a healthy
// live database; asserted here for a corrupt one, because that is when the
// temptation to "just try the next generation" is highest and when a second
// destructive step would be unrecoverable.
func TestRestoreRefusesACorruptGenerationOverACorruptLiveDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")

	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	corruptLive(t, db)
	if err := os.WriteFile(backupPath(db, 0), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := dirListing(t, dir)

	if _, err := Restore(db, 0, restoreClock()); err == nil {
		t.Fatal("Restore accepted a corrupt generation")
	}
	if after := dirListing(t, dir); after != before {
		t.Fatalf("a refused restore moved files while the live database was already broken:\nbefore %v\nafter  %v", before, after)
	}
}

// TestAZeroLengthLiveDatabaseReadsAsAFreshInstall documents a limit found while
// writing the test above, not a behaviour anyone chose.
//
// Truncation to zero is the one corruption shape that does NOT fail: an empty
// file is a valid empty SQLite database, so Open applies every migration and
// returns a working store holding nothing. That is correct for a first run —
// where the file does not exist — and indistinguishable from it here. The
// daemon therefore starts happily on a database that lost everything, and says
// nothing.
//
// Pinned as a test rather than left implicit so that a future change of that
// behaviour is a deliberate one, and so the limit is discoverable from the code
// that has it.
func TestAZeroLengthLiveDatabaseReadsAsAFreshInstall(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")

	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(db, 0); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(db)
	if err != nil {
		t.Fatalf("a zero-length database is expected to open as a fresh one, got: %v", err)
	}
	defer reopened.Close() //nolint:errcheck
	if _, err := reopened.GetProject("prj_backup"); err == nil {
		t.Fatal("the zero-length database still holds its rows; this test no longer describes reality")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found on an emptied database, got: %v", err)
	}

	// The one signal that separates it from a real fresh install: generations
	// exist beside it. Nothing acts on that today — this records where a future
	// warning would come from.
	if info := InspectGeneration(backupPath(db, 0), 0); !info.Exists || !info.HasMigrations {
		t.Fatal("a populated generation sits beside the emptied live database; " +
			"that is the signal a fresh install does not have")
	}
}
