// Package fsutil holds small filesystem helpers shared by the tests and the
// daemon's tmp sweeper.
package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// TestTempPrefix is the one name prefix of every temporary directory a
// pigtail test creates (testutil.MkdirTemp); the tmp sweeper reads it.
const TestTempPrefix = "pt-"

// RemoveTree deletes dir and everything under it, read-only files and
// directories included (a Go module cache is 0444/0555). It makes only the
// directories of the tree writable, never a file and never a symbolic link's
// target: WalkDir's DirEntry describes the link itself, so a link to a
// directory outside the tree is not chmod'ed.
func RemoveTree(dir string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory: try to open it up and keep going.
			if d != nil && d.IsDir() {
				_ = os.Chmod(p, 0o700) //nolint:gosec // a directory must be searchable to be emptied
			}
			return nil
		}
		if d.IsDir() {
			// A directory must be searchable before WalkDir reads it, but
			// WalkDir reads it right after this callback: chmod here is in time.
			_ = os.Chmod(p, 0o700) //nolint:gosec // a directory must be searchable to be emptied
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// NewestMTime walks dir without following links and returns the most recent
// modification time of any entry (the root and directories included) and the
// total size in bytes of the regular files.
func NewestMTime(dir string) (time.Time, int64, error) {
	var newest time.Time
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil // vanished or unreadable entry: skip it
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return newest, total, err
}
