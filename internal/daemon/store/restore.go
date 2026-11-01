package store

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

// The backups PR #61 writes were, until this file existed, unreadable and
// unusable: nothing could tell a healthy generation from a corrupt one, and
// nothing could switch onto one. A backup whose restore path has never been
// exercised is an intention, not a backup.

// LiveGeneration marks the database in use, as opposed to a numbered backup.
const LiveGeneration = -1

// GenerationInfo is one row of `pigtail db list`: enough to tell a usable
// generation from a broken one, and to recognise "my database from before the
// incident" by its contents.
type GenerationInfo struct {
	Generation int // LiveGeneration, or 0..BackupGenerations-1
	Path       string
	Exists     bool
	ModTime    time.Time
	SizeBytes  int64

	// IntegrityOK is PRAGMA integrity_check coming back clean. IntegrityErr
	// carries the reason when it did not — including "this is not a database
	// at all", which is a normal thing to find here.
	IntegrityOK  bool
	IntegrityErr string

	// HasMigrations reports whether the schema was ever applied. A file that
	// passes integrity_check but carries no _migrations rows is a valid empty
	// SQLite database, not a pigtail one.
	HasMigrations bool

	// Row counts, -1 when unknown (unreadable file, or a table that postdates
	// the generation — `subjects` only exists from migration 0006).
	Workspaces int
	Projects   int
	Subjects   int

	// SubjectsByScope counts subjects per canonical scope (the alias read as
	// globex; a value the table does not declare keeps its own key). nil
	// when the tables cannot be read.
	SubjectsByScope map[string]int
}

// InspectGeneration reads one database file. It never returns an error:
// unreadability is the reported finding, not a failure of the call, because
// the whole point of the command is to look at files that may be broken.
func InspectGeneration(path string, gen int) GenerationInfo {
	info := GenerationInfo{
		Generation: gen, Path: path,
		Workspaces: -1, Projects: -1, Subjects: -1,
	}
	fi, err := os.Stat(path)
	if err != nil {
		info.IntegrityErr = err.Error()
		return info
	}
	info.Exists = true
	info.ModTime = fi.ModTime()
	info.SizeBytes = fi.Size()

	// Read-only: inspecting a backup must not modify it, and must not leave a
	// -wal/-shm pair beside it.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		info.IntegrityErr = err.Error()
		return info
	}
	defer db.Close() //nolint:errcheck

	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil {
		info.IntegrityErr = err.Error()
		return info
	}
	if check != "ok" {
		info.IntegrityErr = check
		return info
	}
	info.IntegrityOK = true

	if n := countRows(db, "_migrations"); n > 0 {
		info.HasMigrations = true
	}
	info.Workspaces = countRows(db, "workspaces")
	info.Projects = countRows(db, "projects")
	info.Subjects = countRows(db, "subjects")
	info.SubjectsByScope = countSubjectsByScope(db)
	return info
}

// countSubjectsByScope splits subjects by their theme's scope. A generation
// from before 0010 has no themes.scope: the scope comes from the vault link
// through the active table. A subject whose theme is missing, or whose link no
// root claims, counts under the master (fail-closed, like Store.SubjectScope).
// A generation from before 0015 holds the alias: read canonical. nil
// when the tables cannot be read.
func countSubjectsByScope(db *sql.DB) map[string]int {
	for _, q := range []string{
		`SELECT t.scope, NULL FROM subjects s LEFT JOIN themes t ON t.id = s.theme_id`,
		`SELECT NULL, t.vault_link FROM subjects s LEFT JOIN themes t ON t.id = s.theme_id`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			continue
		}
		out := map[string]int{}
		ok := true
		for rows.Next() {
			var sc, link sql.NullString
			if err := rows.Scan(&sc, &link); err != nil {
				ok = false
				break
			}
			key := string(scope.Master())
			switch {
			case sc.Valid:
				key = string(scope.Canonical(sc.String))
			case link.Valid:
				if s, err := scope.FromVaultLink(link.String); err == nil {
					key = string(s)
				}
			}
			out[key]++
		}
		_ = rows.Close()
		if ok && rows.Err() == nil {
			return out
		}
	}
	return nil
}

// countRows returns -1 when the table cannot be counted — most often because
// it postdates this generation's schema, which is information rather than an
// error.
func countRows(db *sql.DB, table string) int {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil { //nolint:gosec // table is a package-local literal, never input
		return -1
	}
	return n
}

// Inspect reports the live database followed by every generation present on
// disk. Absent generations are omitted rather than listed empty: the first
// hours after a fresh install have fewer than `keep` of them, and a row of
// dashes would read as damage.
func Inspect(dbPath string, keep int) []GenerationInfo {
	out := []GenerationInfo{InspectGeneration(dbPath, LiveGeneration)}
	for gen := 0; gen < keep; gen++ {
		if info := InspectGeneration(backupPath(dbPath, gen), gen); info.Exists {
			out = append(out, info)
		}
	}
	return out
}

// RestoreResult records what a restore did — above all where the previous
// database went, which is the one thing the operator needs if the choice of
// generation turns out to be wrong.
type RestoreResult struct {
	From           string   // the generation file restored from
	PreRestorePath string   // where the previous live database was moved
	SidecarsMoved  []string // stale -wal/-shm paths moved out of the way
}

// PreRestorePath is where the live database is moved before a restore. The
// timestamp is compact UTC rather than RFC3339: the latter's ':' is poorly
// supported in macOS filenames.
func PreRestorePath(dbPath string, now time.Time) string {
	return dbPath + ".pre-restore-" + now.UTC().Format("20060102T150405Z")
}

// Restore replaces the live database with one of its backup generations.
//
// The order is the whole design. Every check runs before anything is touched,
// the live database is moved aside rather than overwritten, and the generation
// is copied rather than renamed so a second attempt stays possible. A restore
// taken at the worst possible moment must not be able to make things worse.
//
// The caller is responsible for having established that no daemon holds the
// database open; this function cannot know.
func Restore(dbPath string, generation int, now time.Time) (RestoreResult, error) {
	var res RestoreResult
	src := backupPath(dbPath, generation)

	info := InspectGeneration(src, generation)
	switch {
	case !info.Exists:
		return res, fmt.Errorf("generation %d does not exist (%s)", generation, src)
	case !info.IntegrityOK:
		return res, fmt.Errorf("generation %d failed its integrity check (%s); refusing to put it over the live database", generation, info.IntegrityErr)
	case !info.HasMigrations:
		return res, fmt.Errorf("generation %d carries no schema (no _migrations rows); it is not a pigtail database", generation)
	}

	// Move the live database aside. Nothing has been destroyed up to here.
	pre := PreRestorePath(dbPath, now)
	switch _, err := os.Stat(dbPath); {
	case err == nil:
		if err := os.Rename(dbPath, pre); err != nil {
			return res, fmt.Errorf("move the live database aside: %w", err)
		}
		res.PreRestorePath = pre
	case !os.IsNotExist(err):
		return res, fmt.Errorf("stat the live database: %w", err)
		// os.IsNotExist: restoring onto nothing is legitimate.
	}

	// The old database's WAL and shared-memory files describe pages that the
	// restored file does not have. Leaving them is corruption by construction.
	for _, suffix := range []string{"-wal", "-shm"} {
		stale := dbPath + suffix
		if _, err := os.Stat(stale); err != nil {
			continue
		}
		dest := pre + suffix
		if err := os.Rename(stale, dest); err != nil {
			return res, fmt.Errorf("move %s aside: %w (the previous database is at %s)", stale, err, res.PreRestorePath)
		}
		res.SidecarsMoved = append(res.SidecarsMoved, dest)
	}

	if err := copyFile(src, dbPath); err != nil {
		return res, fmt.Errorf("copy generation %d into place: %w (the previous database is at %s — move it back)", generation, err, res.PreRestorePath)
	}
	res.From = src
	return res, nil
}

// copyFile writes src to dst, creating dst 0600, and syncs before returning:
// the next thing to touch this file is a daemon opening it as its database.
func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src is a daemon-owned backup path
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dst is the daemon's database path
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
