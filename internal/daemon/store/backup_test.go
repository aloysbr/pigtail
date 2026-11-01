package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seededStore opens a real on-disk store carrying one recognisable row.
func seededStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_backup", Name: "backup-me", RootPath: "/tmp/x", DefaultBackend: "git",
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return st
}

// A backup nobody can reopen is not a backup. This is the assertion the whole
// item rests on: the point is restoring, not producing a file.
func TestBackupProducesAReopenableDatabase(t *testing.T) {
	dir := t.TempDir()
	st := seededStore(t, filepath.Join(dir, "pigtail.db"))

	dest := filepath.Join(dir, "pigtail.db.0")
	if err := st.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	restored, err := Open(dest)
	if err != nil {
		t.Fatalf("reopen backup: %v", err)
	}
	defer restored.Close() //nolint:errcheck
	p, err := restored.GetProject("prj_backup")
	if err != nil {
		t.Fatalf("GetProject from backup: %v", err)
	}
	if p.Name != "backup-me" {
		t.Fatalf("restored project name = %q, want backup-me", p.Name)
	}
}

// Writes landing after the snapshot must not appear in it — otherwise the
// backup is not a point in time and "restore to before the corruption" means
// nothing.
func TestBackupIsAPointInTime(t *testing.T) {
	dir := t.TempDir()
	st := seededStore(t, filepath.Join(dir, "pigtail.db"))

	dest := filepath.Join(dir, "pigtail.db.0")
	if err := st.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_after", Name: "after", RootPath: "/tmp/y", DefaultBackend: "git",
	}); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close() //nolint:errcheck
	if _, err := restored.GetProject("prj_after"); err == nil {
		t.Fatal("the backup contains a row written after it was taken")
	}
}

func TestRotateBackupsShiftsGenerations(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	write("pigtail.db.0", "newest")
	write("pigtail.db.1", "middle")
	write("pigtail.db.2", "oldest")

	if err := rotateBackups(db, 3); err != nil {
		t.Fatalf("rotateBackups: %v", err)
	}

	// Everything shifts one generation older; the oldest falls off the end.
	if got := read("pigtail.db.1"); got != "newest" {
		t.Fatalf(".1 = %q, want the former .0", got)
	}
	if got := read("pigtail.db.2"); got != "middle" {
		t.Fatalf(".2 = %q, want the former .1", got)
	}
	// .0 is now free for the fresh snapshot — VACUUM INTO refuses an existing file.
	if _, err := os.Stat(filepath.Join(dir, "pigtail.db.0")); !os.IsNotExist(err) {
		t.Fatalf("slot .0 must be free after a rotation, stat err = %v", err)
	}
}

// The first few runs have nothing to shift; a missing generation is normal,
// never an error.
func TestRotateBackupsToleratesMissingGenerations(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	if err := os.WriteFile(filepath.Join(dir, "pigtail.db.0"), []byte("only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rotateBackups(db, 3); err != nil {
		t.Fatalf("rotateBackups with gaps: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "pigtail.db.1"))
	if err != nil || string(b) != "only" {
		t.Fatalf("read .1 = %q, %v; want the former .0", b, err)
	}
}

// BackupRotated is the operation the daemon actually schedules: rotate, then
// snapshot into the freed slot.
func TestBackupRotatedKeepsBoundedGenerations(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)

	for i := 0; i < 5; i++ {
		if err := st.BackupRotated(3); err != nil {
			t.Fatalf("BackupRotated #%d: %v", i, err)
		}
	}

	for _, gen := range []string{"pigtail.db.0", "pigtail.db.1", "pigtail.db.2"} {
		if _, err := os.Stat(filepath.Join(dir, gen)); err != nil {
			t.Fatalf("%s missing after five runs: %v", gen, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pigtail.db.3")); !os.IsNotExist(err) {
		t.Fatal("a fourth generation was kept; keep=3 must bound it")
	}

	// The newest generation must still be a usable database, five rotations in.
	restored, err := Open(filepath.Join(dir, "pigtail.db.0"))
	if err != nil {
		t.Fatalf("reopen newest backup: %v", err)
	}
	defer restored.Close() //nolint:errcheck
	if _, err := restored.GetProject("prj_backup"); err != nil {
		t.Fatalf("newest backup lost its content: %v", err)
	}
}

// An in-memory store has no file to snapshot; asking for one is a no-op rather
// than an error the daemon would log every hour.
func TestBackupRotatedSkipsAnInMemoryDatabase(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close() //nolint:errcheck
	if err := st.BackupRotated(3); err != nil {
		t.Fatalf("BackupRotated on :memory: = %v, want a silent no-op", err)
	}
}

// The daemon calls this at start and hourly. Backing up unconditionally at
// start would let a restart loop churn through every generation in minutes,
// destroying the history precisely when something is going wrong.
func TestBackupRotatedIfStaleSkipsAFreshGeneration(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)

	done, err := st.BackupRotatedIfStale(3, time.Hour)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !done {
		t.Fatal("the first call must back up: there is no generation yet")
	}

	done, err = st.BackupRotatedIfStale(3, time.Hour)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if done {
		t.Fatal("a generation written seconds ago must not be rotated out")
	}
	if _, err := os.Stat(filepath.Join(dir, "pigtail.db.1")); !os.IsNotExist(err) {
		t.Fatal("the skipped call still rotated")
	}
}

func TestBackupRotatedIfStaleRunsWhenTheNewestIsOld(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)

	if _, err := st.BackupRotatedIfStale(3, time.Hour); err != nil {
		t.Fatal(err)
	}
	// Age the newest generation past the threshold.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "pigtail.db.0"), old, old); err != nil {
		t.Fatal(err)
	}

	done, err := st.BackupRotatedIfStale(3, time.Hour)
	if err != nil {
		t.Fatalf("stale call: %v", err)
	}
	if !done {
		t.Fatal("a generation older than minAge must trigger a backup")
	}
	if _, err := os.Stat(filepath.Join(dir, "pigtail.db.1")); err != nil {
		t.Fatalf("the aged generation should have shifted to .1: %v", err)
	}
}
