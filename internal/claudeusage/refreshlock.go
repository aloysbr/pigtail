package claudeusage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// RefreshLockName is the file, in pigtail's state directory, whose flock
// serialises every claude -p /usage across processes: pigtail usage --refresh
// and the daemon's sweep never run one at the same time (a concurrent rewrite
// of .claude.json is not proven safe, see Refresh).
const RefreshLockName = "usage-refresh.lock"

// RefreshLockPath is the lock file in stateDir.
func RefreshLockPath(stateDir string) string { return filepath.Join(stateDir, RefreshLockName) }

// TryRefreshLock takes the refresh lock without waiting (flock LOCK_EX|LOCK_NB,
// model runtimeclaim's witness). held=true, err=nil: another taker has it —
// the caller runs nothing and says so. err != nil: the lock could not be
// taken at all — the caller refuses too, never refreshes unlocked. Otherwise
// unlock releases it; hold it for the whole Refresh call.
func TryRefreshLock(stateDir string) (unlock func(), held bool, err error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, false, fmt.Errorf("refresh lock: %w", err)
	}
	path := RefreshLockPath(stateDir)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("refresh lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("refresh lock %s: %w", path, err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, false, nil
}
