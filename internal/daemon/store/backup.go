package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The database holds worktree state, port allocations and the subject layer.
// It is the one piece of pigtail whose loss is not recoverable by re-running
// anything: a crash mid-write corrupts it and there is nothing to restore from.
// This file is the rolling snapshot that gives it a floor.

// BackupGenerations is how many rolling snapshots are kept beside the database:
// pigtail.db.0 (newest) through pigtail.db.2. Three is enough to survive a
// corruption that goes unnoticed for two backup cycles, and small enough that
// the copies stay a rounding error next to the database itself.
const BackupGenerations = 3

// Backup writes a consistent snapshot of the live database to dest.
//
// It uses VACUUM INTO rather than copying the file. A plain copy of a database
// being written to yields a torn file — and the WAL beside it means the bytes
// on disk are not the whole story anyway. VACUUM INTO runs inside a read
// transaction, so the snapshot is exactly one point in time regardless of what
// the daemon writes during it, and the result is a standalone database with no
// WAL to carry along.
//
// dest must not exist: SQLite refuses to overwrite, which is deliberate here —
// the rotation frees the slot first, so a failure to rotate can never quietly
// destroy the newest good copy.
func (s *Store) Backup(dest string) error {
	// SQLite's INTO clause takes an expression, so the destination binds as a
	// parameter — no hand-rolled quote escaping, and nothing for a path with an
	// apostrophe in it to break.
	if _, err := s.db.Exec("VACUUM INTO ?", dest); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dest, err)
	}
	return nil
}

// backupPath names one generation: <db>.0 is the newest.
func backupPath(dbPath string, gen int) string {
	return fmt.Sprintf("%s.%d", dbPath, gen)
}

// rotateBackups shifts every generation one slot older, drops the one falling
// off the end, and leaves slot 0 free for a fresh snapshot.
//
// It walks from the oldest down so no rename ever lands on an occupied slot. A
// missing generation is normal — the first runs have nothing to shift — and is
// not an error.
func rotateBackups(dbPath string, keep int) error {
	if keep < 1 {
		return nil
	}
	oldest := backupPath(dbPath, keep-1)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("drop oldest backup %s: %w", oldest, err)
	}
	for gen := keep - 2; gen >= 0; gen-- {
		from, to := backupPath(dbPath, gen), backupPath(dbPath, gen+1)
		if err := os.Rename(from, to); err != nil {
			if os.IsNotExist(err) {
				continue // nothing written at this generation yet
			}
			return fmt.Errorf("rotate %s → %s: %w", from, to, err)
		}
	}
	return nil
}

// BackupRotated is the operation the daemon schedules: rotate the existing
// generations, then snapshot into the freed newest slot.
//
// An in-memory database — ":memory:" above all — is a silent no-op rather than
// an error, so an ephemeral daemon does not log a failure every cycle. The
// decision rests on the path this store was actually opened with, never on one
// supplied by the caller.
func (s *Store) BackupRotated(keep int) error {
	if !isFileBacked(s.path) {
		return nil
	}
	if err := rotateBackups(s.path, keep); err != nil {
		return err
	}
	return s.Backup(backupPath(s.path, 0))
}

// BackupRotatedIfStale backs up only when the newest generation is at least
// minAge old, and reports whether it did.
//
// This is what the daemon schedules, rather than BackupRotated directly. A
// snapshot taken unconditionally at every start would let a restart loop churn
// through all three generations in minutes — shredding the history at exactly
// the moment something is going wrong. Gating on the newest generation's age
// keeps both properties that matter: a daemon restarted often still backs up,
// and a daemon restarting constantly does not destroy what it already has.
func (s *Store) BackupRotatedIfStale(keep int, minAge time.Duration) (bool, error) {
	if !isFileBacked(s.path) {
		return false, nil
	}
	switch fi, err := os.Stat(backupPath(s.path, 0)); {
	case err == nil:
		if time.Since(fi.ModTime()) < minAge {
			return false, nil
		}
	case !os.IsNotExist(err):
		return false, fmt.Errorf("stat newest backup: %w", err)
		// os.IsNotExist: nothing backed up yet, so take one now.
	}
	if err := s.BackupRotated(keep); err != nil {
		return false, err
	}
	return true, nil
}

// isFileBacked reports whether dbPath names a database on disk, as opposed to
// SQLite's in-memory forms.
func isFileBacked(dbPath string) bool {
	if dbPath == "" || dbPath == ":memory:" {
		return false
	}
	// file::memory: and file:foo?mode=memory are the URI spellings.
	if filepath.Base(dbPath) == ":memory:" {
		return false
	}
	return true
}

// Path returns where this database lives, as passed to Open. Reported rather
// than assumed: the daemon config's DBPath and the store actually open can
// differ (tests open ":memory:" under a file-shaped config).
func (s *Store) Path() string { return s.path }
