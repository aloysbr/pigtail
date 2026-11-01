//go:build darwin || linux

// Package sysdisk reads the free space of the volumes pigtail's heavy
// commands write to (the home directory and the temp directory).
package sysdisk

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Free returns the bytes an unprivileged process can still write on the
// volume holding path (Bavail * Bsize: the reserved blocks are not counted).
func Free(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec // both are non-negative
}

// FreeMin returns the smallest free space over the distinct volumes (by
// Fsid) holding paths. A path that cannot be read is skipped; the call fails
// only when none could be read (or none was given).
func FreeMin(paths ...string) (uint64, error) {
	seen := map[unix.Fsid]bool{}
	var (
		min     uint64
		ok      bool
		lastErr error
	)
	for _, p := range paths {
		var st unix.Statfs_t
		if err := unix.Statfs(p, &st); err != nil {
			lastErr = fmt.Errorf("statfs %s: %w", p, err)
			continue
		}
		if seen[st.Fsid] {
			continue
		}
		seen[st.Fsid] = true
		free := uint64(st.Bavail) * uint64(st.Bsize) //nolint:gosec // both are non-negative
		if !ok || free < min {
			min, ok = free, true
		}
	}
	if !ok {
		if lastErr == nil {
			lastErr = errors.New("sysdisk: no path to read")
		}
		return 0, lastErr
	}
	return min, nil
}

// FreeMinDefault is FreeMin over os.UserHomeDir() and os.TempDir(): where
// the build caches and the test temp dirs fill the disk.
func FreeMinDefault() (uint64, error) {
	paths := []string{os.TempDir()}
	if h, err := os.UserHomeDir(); err == nil {
		paths = append(paths, h)
	}
	return FreeMin(paths...)
}
