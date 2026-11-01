package runtimeclaim

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ExpiredName is the marker a lift at the deadline leaves; pigtaild journals
// it. Only the last expiry is kept.
const ExpiredName = "runtime-claim-expired.json"

// Expiry is the marker: the lease that was lifted, and when.
type Expiry struct {
	Lease State     `json:"lease"`
	At    time.Time `json:"at"`
}

// Expire lifts the lease when its deadline has been reached, like a release.
// No lease file → false without taking the lock (nothing is created). An
// unreadable file is never lifted: the Load error comes back, nothing touched.
func Expire(dir string, now time.Time) (Expiry, bool, error) {
	return expire(dir, now, lockWait)
}

// ExpireNoWait is Expire for a reader that must not wait: the lock is tried
// once, a busy lock is ErrLockTimeout and nothing is lifted.
func ExpireNoWait(dir string, now time.Time) (Expiry, bool, error) {
	return expire(dir, now, 0)
}

func expire(dir string, now time.Time, wait time.Duration) (Expiry, bool, error) {
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Expiry{}, false, nil
		}
		return Expiry{}, false, causeOnly(err)
	}
	unlock, err := lockWithin(dir, wait)
	if err != nil {
		return Expiry{}, false, err
	}
	defer unlock()
	cur, ok, err := Load(dir)
	if err != nil {
		return Expiry{}, false, err
	}
	if !ok || !cur.Expired(now) {
		return Expiry{}, false, nil
	}
	exp, err := expireLocked(dir, cur, now)
	if err != nil {
		return Expiry{}, false, err
	}
	return exp, true, nil
}

// expireLocked writes the marker, then lifts. The caller holds the lock.
func expireLocked(dir string, cur State, now time.Time) (Expiry, error) {
	exp := Expiry{Lease: cur, At: now}
	raw, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		return Expiry{}, err
	}
	if err := writeAtomic(filepath.Join(dir, ExpiredName), append(raw, '\n')); err != nil {
		return Expiry{}, err
	}
	if err := lift(dir, cur, now); err != nil {
		return Expiry{}, err
	}
	return exp, nil
}

// ReadExpiry reads the marker; absent or corrupt → false.
func ReadExpiry(dir string) (Expiry, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, ExpiredName))
	if err != nil {
		return Expiry{}, false
	}
	var exp Expiry
	if json.Unmarshal(raw, &exp) != nil || exp.Lease.ID == "" {
		return Expiry{}, false
	}
	return exp, true
}
