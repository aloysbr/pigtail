package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Three generations beside the database survive a bad write. None of them
// survives losing the volume, which is the failure this file addresses: a copy
// of the newest generation onto a mounted volume elsewhere.
//
// Three properties shape the whole thing, and each exists because the obvious
// version of it is wrong:
//
//  1. **The destination is never created.** `mkdir -p /Volumes/nas/pigtail` on
//     an unmounted volume succeeds — by creating a directory on the *local*
//     disk. pigtail would then back up onto the very disk it is escaping, and
//     report success. A destination that must pre-exist makes an unmounted
//     volume read as "unreachable", which is the truth.
//  2. **The local generation is copied, never re-vacuumed.** `VACUUM INTO` a
//     network path would hold a read transaction open on the live database for
//     the length of a network write. No I/O to the destination happens while
//     the database is in a transaction; the file produced locally is what
//     travels.
//  3. **The copy lands atomically.** A dropped connection mid-write would
//     otherwise leave a truncated file *named* as a generation — a file that
//     looks restorable until the moment it is needed. It is written under a
//     .partial name and renamed into place, which is atomic within a volume.

// ErrOffsiteUnreachable means the destination is not there: an unmounted
// volume, a removed directory, or a path that is not a directory at all. It is
// the normal state of a laptop away from its NAS, not a defect.
var ErrOffsiteUnreachable = errors.New("offsite destination unreachable")

// ErrOffsiteIsLocalDir means the destination resolves to the database's own
// directory. Unlike a merely weak destination this one is destructive: the
// generation filenames collide, so the copy would overwrite the local rotation
// it exists to duplicate.
var ErrOffsiteIsLocalDir = errors.New("offsite destination is the database's own directory")

// partialSuffix names a copy still in flight. Deliberately not a valid
// generation name: nothing that scans for `.0`..`.2` can mistake it for one.
const partialSuffix = ".partial"

// BackupOffsite copies the newest local generation into destDir, rotating
// destDir's own generations first so the destination keeps a short history of
// its own rather than a single file an interrupted copy could ruin.
//
// It is a no-op — not an error — in the three situations that are normal
// rather than wrong: no destination configured, an in-memory database, and no
// local generation to copy yet.
//
// The caller is responsible for bounding how long this may take. On a hung
// network mount a write can block indefinitely, which is why this runs after
// the local backup and never before it.
func BackupOffsite(dbPath, destDir string, keep int) error {
	if destDir == "" || !isFileBacked(dbPath) {
		return nil
	}

	// The destination's shape picks the transport, so the flag and the daemon
	// stay unaware of it. A remote destination skips everything below: there is
	// no local directory to stat, no device to compare, and no mount that could
	// be missing — which is the whole reason it exists (see offsite_remote.go).
	if host, path, ok := ParseRemoteDest(destDir); ok {
		return backupOffsiteRemote(dbPath, host, path, keep, execRunner)
	}

	// Resolve both sides before comparing: a destination reached through a
	// symlink to the config dir is the same collision as naming it directly.
	localDir, err := resolveDir(filepath.Dir(dbPath))
	if err != nil {
		return fmt.Errorf("resolve the database directory: %w", err)
	}
	info, err := os.Stat(destDir)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrOffsiteUnreachable, destDir)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrOffsiteUnreachable, destDir)
	}
	resolvedDest, err := resolveDir(destDir)
	if err != nil {
		return fmt.Errorf("resolve the offsite directory: %w", err)
	}
	if resolvedDest == localDir {
		return fmt.Errorf("%w: %s", ErrOffsiteIsLocalDir, destDir)
	}

	src := backupPath(dbPath, 0)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil // nothing backed up locally yet; nothing to send
		}
		return fmt.Errorf("stat the newest local generation: %w", err)
	}

	// The destination mirrors the local layout, so a restore is `cp` followed
	// by the same `pigtail db restore` the local generations use — no second
	// recovery procedure to remember during an incident.
	destBase := filepath.Join(destDir, filepath.Base(dbPath))
	if err := rotateBackups(destBase, keep); err != nil {
		return fmt.Errorf("rotate offsite generations: %w", err)
	}
	return copyAtomic(src, backupPath(destBase, 0))
}

// resolveDir returns an absolute, symlink-resolved directory path.
func resolveDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	// EvalSymlinks fails on a path that does not exist; the caller has already
	// established existence for the destination, and the database's directory
	// exists by construction. Fall back to the absolute path rather than
	// failing the whole backup over a resolution detail.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// copyAtomic writes src to dst through a .partial file, syncs it, and renames
// it into place. The rename is what makes a generation either complete or
// absent — never a truncated file wearing a generation's name.
//
// A failed copy removes its own partial, so a NAS that drops the connection
// every hour does not accumulate debris on the destination.
func copyAtomic(src, dst string) error {
	partial := dst + partialSuffix
	// A partial left by a killed daemon would make O_EXCL fail forever.
	if err := os.Remove(partial); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear a stale partial copy: %w", err)
	}

	in, err := os.Open(src) //nolint:gosec // src is a daemon-owned backup path
	if err != nil {
		return fmt.Errorf("open the local generation: %w", err)
	}
	defer in.Close() //nolint:errcheck

	out, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // partial is derived from the configured destination
	if err != nil {
		return fmt.Errorf("create the offsite copy: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("copy to the offsite destination: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("flush the offsite copy: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("close the offsite copy: %w", err)
	}
	if err := os.Rename(partial, dst); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("publish the offsite copy: %w", err)
	}
	return nil
}

// SameDevice reports whether dbPath and destDir live on the same filesystem.
//
// A destination on the same device is not refused: it is a weaker guarantee,
// not a destructive one, and there are legitimate reasons to want a second copy
// locally. But it survives nothing the local generations do not, while looking
// exactly like protection — so it is reported, and the daemon says so once.
func SameDevice(dbPath, destDir string) (bool, error) {
	dbInfo, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		return false, err
	}
	destInfo, err := os.Stat(destDir)
	if err != nil {
		return false, err
	}
	dbSys, ok1 := dbInfo.Sys().(*syscall.Stat_t)
	destSys, ok2 := destInfo.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		// No device information available: say nothing rather than guess.
		return false, nil
	}
	// Compared without conversion on purpose. Dev is int32 on darwin and
	// uint64 on linux, but both sides are the same type on any one platform,
	// so equality is exact — while widening to uint64 would be a signed
	// conversion gosec is right to flag on darwin.
	return dbSys.Dev == destSys.Dev, nil
}
