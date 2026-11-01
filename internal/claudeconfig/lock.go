package claudeconfig

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// Claude Code guards its config with proper-lockfile: a directory <file>.lock
// made by mkdir, refreshed while held, considered dead after 10 s. pigtail
// takes the same lock so neither side loses the other's write.
var (
	lockWait  = 2 * time.Second  // how long a live holder is waited for
	lockStale = 10 * time.Second // proper-lockfile's default stale age
)

func acquire(lock string) (func(), error) {
	deadline := time.Now().Add(lockWait)
	delay := 25 * time.Millisecond
	for {
		err := os.Mkdir(lock, 0o700) // only its existence matters; the config dir is private
		if err == nil {
			return func() { _ = os.Remove(lock) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > lockStale {
			if os.Remove(lock) == nil { // a dead holder's lock, as proper-lockfile treats it
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s held by another writer for %s", lock, lockWait)
		}
		time.Sleep(delay)
		if delay < 400*time.Millisecond {
			delay *= 2
		}
	}
}

// writeAtomic replaces path the way Claude Code does: a tmp file in the same
// directory, fsync, rename, mode 0600. A failure leaves path untouched.
func writeAtomic(path string, data []byte) (err error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d.%x", path, os.Getpid(), b)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
