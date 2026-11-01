package store

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestInspectGenerationReportsAHealthyBackup(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}

	got := InspectGeneration(backupPath(db, 0), 0)
	if !got.Exists {
		t.Fatal("Exists = false, want true")
	}
	if !got.IntegrityOK {
		t.Fatalf("IntegrityOK = false (%s), want true", got.IntegrityErr)
	}
	if !got.HasMigrations {
		t.Fatal("HasMigrations = false, want true")
	}
	if got.Projects != 1 {
		t.Fatalf("Projects = %d, want 1", got.Projects)
	}
	if got.SizeBytes <= 0 {
		t.Fatalf("SizeBytes = %d, want > 0", got.SizeBytes)
	}
}

// An unreadable generation is exactly the case this command exists for, so it
// is reported as data, never as a failure of the call.
func TestInspectGenerationReportsCorruptionInsteadOfFailing(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	// Overwrite the header with junk: still a file, no longer a database.
	if err := os.WriteFile(backupPath(db, 0), []byte("not a sqlite file at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := InspectGeneration(backupPath(db, 0), 0)
	if !got.Exists {
		t.Fatal("Exists = false; a corrupt file still exists")
	}
	if got.IntegrityOK {
		t.Fatal("IntegrityOK = true on a corrupt file")
	}
	if got.IntegrityErr == "" {
		t.Fatal("IntegrityErr is empty; the reason must be reportable")
	}
}

func TestInspectGenerationOnAMissingFile(t *testing.T) {
	got := InspectGeneration(filepath.Join(t.TempDir(), "nope.db.0"), 0)
	if got.Exists {
		t.Fatal("Exists = true for a missing file")
	}
	if got.IntegrityOK {
		t.Fatal("IntegrityOK = true for a missing file")
	}
}

func TestInspectListsTheLiveDatabaseThenEachGeneration(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	for i := 0; i < 2; i++ {
		if err := st.BackupRotated(BackupGenerations); err != nil {
			t.Fatal(err)
		}
	}

	got := Inspect(db, BackupGenerations)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 (live + .0 + .1)", len(got))
	}
	if got[0].Generation != LiveGeneration {
		t.Fatalf("first row generation = %d, want %d (live)", got[0].Generation, LiveGeneration)
	}
	if got[1].Generation != 0 || got[2].Generation != 1 {
		t.Fatalf("generations = %d, %d; want 0, 1", got[1].Generation, got[2].Generation)
	}
	// The generation that was never written is omitted, not reported empty.
	for _, g := range got {
		if !g.Exists {
			t.Fatalf("generation %d listed but does not exist", g.Generation)
		}
	}
}

// restoreClock is fixed so the pre-restore filename is deterministic.
func restoreClock() time.Time {
	return time.Date(2026, 7, 28, 14, 5, 6, 0, time.UTC)
}

func TestRestoreMovesTheLiveDatabaseAsideAndSwapsInTheGeneration(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	// A row written after the backup: it must be gone from the restored file
	// and still present in the set-aside one.
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_after", Name: "after", RootPath: "/tmp/y", DefaultBackend: "git",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := Restore(db, 0, restoreClock())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.PreRestorePath == "" {
		t.Fatal("PreRestorePath is empty; the previous database must be findable")
	}

	restored, err := Open(db)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer restored.Close() //nolint:errcheck
	if _, err := restored.GetProject("prj_backup"); err != nil {
		t.Fatalf("restored database lost the backed-up row: %v", err)
	}
	if _, err := restored.GetProject("prj_after"); err == nil {
		t.Fatal("restored database contains a row written after the backup")
	}

	// A wrong restore must not be fatal: the previous state is still there.
	previous, err := Open(res.PreRestorePath)
	if err != nil {
		t.Fatalf("open set-aside database: %v", err)
	}
	defer previous.Close() //nolint:errcheck
	if _, err := previous.GetProject("prj_after"); err != nil {
		t.Fatalf("the set-aside database lost its content: %v", err)
	}
}

// The generation is copied, not renamed: a second attempt must stay possible
// and the rotation must not be disturbed.
func TestRestoreLeavesTheGenerationInPlace(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(db, 0, restoreClock()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backupPath(db, 0)); err != nil {
		t.Fatalf("generation .0 disappeared: %v", err)
	}
}

// A WAL belonging to the OLD database, left beside the restored file, is
// corruption by construction: SQLite would replay pages matching nothing.
func TestRestoreClearsStaleSidecars(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(db+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Restore(db, 0, restoreClock())
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(db + suffix); !os.IsNotExist(err) {
			t.Fatalf("%s survived the restore (stat err = %v)", db+suffix, err)
		}
	}
	if len(res.SidecarsMoved) != 2 {
		t.Fatalf("SidecarsMoved = %v, want two entries", res.SidecarsMoved)
	}
}

func TestRestoreRefusesACorruptGenerationAndTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath(db, 0), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := dirListing(t, dir)

	if _, err := Restore(db, 0, restoreClock()); err == nil {
		t.Fatal("Restore accepted a corrupt generation")
	}
	if after := dirListing(t, dir); after != before {
		t.Fatalf("the directory changed on a refused restore:\nbefore %v\nafter  %v", before, after)
	}
}

func TestRestoreRefusesAMissingGeneration(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before := dirListing(t, dir)

	if _, err := Restore(db, 2, restoreClock()); err == nil {
		t.Fatal("Restore accepted a generation that does not exist")
	}
	if after := dirListing(t, dir); after != before {
		t.Fatal("the directory changed on a refused restore")
	}
}

// dirListing renders a directory as a stable string, for asserting inertness.
func dirListing(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
