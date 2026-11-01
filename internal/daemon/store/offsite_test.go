package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Three generations beside the database survive a bad write; none of them
// survives losing the volume. These tests cover the copy that makes the
// guarantee real rather than local.
//
// The load-bearing one is TestOffsiteRefusesToCreateItsDestination: `mkdir -p
// /Volumes/nas/pigtail` on an unmounted volume creates a directory on the
// **local disk**, so pigtail would back up onto the very disk it is trying to
// escape while reporting success. Requiring the destination to pre-exist is
// what closes that, and it is the one behaviour that must never regress.

func seededWithGeneration(t *testing.T, dir string) string {
	t.Helper()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.BackupRotated(BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOffsiteCopiesTheNewestGeneration(t *testing.T) {
	db := seededWithGeneration(t, t.TempDir())
	dest := t.TempDir()

	if err := BackupOffsite(db, dest, BackupGenerations); err != nil {
		t.Fatalf("BackupOffsite: %v", err)
	}

	copied := filepath.Join(dest, "pigtail.db.0")
	// Reopenable, not merely present: a backup nobody can open is not a backup.
	st, err := Open(copied)
	if err != nil {
		t.Fatalf("open the offsite copy: %v", err)
	}
	defer st.Close() //nolint:errcheck
	if _, err := st.GetProject("prj_backup"); err != nil {
		t.Fatalf("the offsite copy lost the backed-up row: %v", err)
	}
}

// The one that matters. An unmounted volume must produce "unreachable" and
// **nothing on disk** — not a helpfully created directory that turns the
// offsite backup into a second local one.
func TestOffsiteRefusesToCreateItsDestination(t *testing.T) {
	db := seededWithGeneration(t, t.TempDir())
	parent := t.TempDir()
	dest := filepath.Join(parent, "nas-not-mounted", "pigtail")

	err := BackupOffsite(db, dest, BackupGenerations)
	if !errors.Is(err, ErrOffsiteUnreachable) {
		t.Fatalf("err = %v, want ErrOffsiteUnreachable", err)
	}
	if entries, readErr := os.ReadDir(parent); readErr != nil {
		t.Fatal(readErr)
	} else if len(entries) != 0 {
		t.Fatalf("BackupOffsite created %v under a missing destination; "+
			"on an unmounted volume that writes the backup to the local disk", entries)
	}
}

// A file where a directory belongs is the same class of misconfiguration and
// must be reported the same way, not written through.
func TestOffsiteRejectsANonDirectoryDestination(t *testing.T) {
	dir := t.TempDir()
	db := seededWithGeneration(t, dir)
	dest := filepath.Join(dir, "a-file")
	if err := os.WriteFile(dest, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := BackupOffsite(db, dest, BackupGenerations); !errors.Is(err, ErrOffsiteUnreachable) {
		t.Fatalf("err = %v, want ErrOffsiteUnreachable", err)
	}
}

// Pointing the offsite copy at the database's own directory is not a weak
// configuration, it is a destructive one: the generation names collide, so the
// copy would overwrite the local rotation it is supposed to duplicate.
func TestOffsiteRefusesTheDatabaseOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	db := seededWithGeneration(t, dir)

	err := BackupOffsite(db, dir, BackupGenerations)
	if !errors.Is(err, ErrOffsiteIsLocalDir) {
		t.Fatalf("err = %v, want ErrOffsiteIsLocalDir", err)
	}
	// And it must have refused *before* touching anything.
	if _, statErr := os.Stat(filepath.Join(dir, "pigtail.db.0.partial")); !os.IsNotExist(statErr) {
		t.Fatalf("a partial file survived a refused offsite backup (stat err = %v)", statErr)
	}
}

// The destination keeps its own rotation, so a corruption pushed offsite does
// not immediately become the only copy there either.
func TestOffsiteRotatesItsOwnGenerations(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	dest := t.TempDir()

	st := seededStore(t, db)
	for i := 0; i < BackupGenerations+1; i++ {
		if err := st.BackupRotated(BackupGenerations); err != nil {
			t.Fatal(err)
		}
		if err := BackupOffsite(db, dest, BackupGenerations); err != nil {
			t.Fatalf("BackupOffsite #%d: %v", i, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	for gen := 0; gen < BackupGenerations; gen++ {
		p := filepath.Join(dest, "pigtail.db."+string(rune('0'+gen)))
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("offsite generation .%d missing: %v", gen, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "pigtail.db.3")); !os.IsNotExist(err) {
		t.Fatalf("offsite kept a generation beyond the bound (stat err = %v)", err)
	}
}

// A copy that dies mid-flight — a NAS dropping the connection is the expected
// way for this to fail — must never leave a truncated file *named* as a
// generation. The partial name is what buys that, and nothing may survive
// under it either.
func TestOffsiteLeavesNoPartialBehind(t *testing.T) {
	dir := t.TempDir()
	db := seededWithGeneration(t, dir)
	dest := t.TempDir()

	if err := BackupOffsite(db, dest, BackupGenerations); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".partial" {
			t.Fatalf("a %q file survived a successful offsite backup", e.Name())
		}
	}
}

// Losing the source between the local backup and the copy is not a failure to
// report every hour: there is simply nothing to send yet.
func TestOffsiteIsQuietWithNoLocalGeneration(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st := seededStore(t, db)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()

	if err := BackupOffsite(db, dest, BackupGenerations); err != nil {
		t.Fatalf("BackupOffsite with no local generation: %v", err)
	}
	if entries, err := os.ReadDir(dest); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("wrote %v with no local generation to copy", entries)
	}
}

// Same rule as the local rotation: an ephemeral daemon must not log a failure
// every cycle over a database that has no home on disk.
func TestOffsiteSkipsAnInMemoryDatabase(t *testing.T) {
	dest := t.TempDir()
	if err := BackupOffsite(":memory:", dest, BackupGenerations); err != nil {
		t.Fatalf("BackupOffsite on an in-memory database: %v", err)
	}
	if entries, err := os.ReadDir(dest); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("an in-memory database produced %v offsite", entries)
	}
}

// An empty destination is how the feature stays off by default.
func TestOffsiteIsOffWithNoDestination(t *testing.T) {
	db := seededWithGeneration(t, t.TempDir())
	if err := BackupOffsite(db, "", BackupGenerations); err != nil {
		t.Fatalf("BackupOffsite with no destination: %v", err)
	}
}

// A destination on the same device is not refused — it is a weaker guarantee,
// not a destructive one — but it must be *reported*, because it looks exactly
// like protection while surviving nothing the local generations don't.
func TestOffsiteReportsADestinationOnTheSameDevice(t *testing.T) {
	dir := t.TempDir()
	db := seededWithGeneration(t, dir)
	dest := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}

	same, err := SameDevice(db, dest)
	if err != nil {
		t.Fatalf("SameDevice: %v", err)
	}
	if !same {
		t.Fatal("a directory under the database's own tree reported as a different device")
	}
	// Reported, not refused: the copy still happens.
	if err := BackupOffsite(db, dest, BackupGenerations); err != nil {
		t.Fatalf("BackupOffsite onto the same device: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "pigtail.db.0")); err != nil {
		t.Fatalf("same-device destination should still receive the copy: %v", err)
	}
}
