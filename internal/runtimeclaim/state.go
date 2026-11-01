// Package runtimeclaim is the narrow lease on the installed pigtail runtime
// (binaries, pigtaild, its LaunchAgent plist), D3 of sub_x000000000000005: a
// session about to install a branch build declares it with `pigtail runtime
// claim`, and a runtime-claim guard (a user hook) refuses the runtime gestures of
// every other session while it holds. One JSON file plus a one-line token,
// next to maintenance.json (same directory rule), written under one flock by
// the daemon and, when it is unreachable, by the CLI directly: the file is the
// truth, readable with the daemon down — the very moment of a bootout. A
// holder that is gone reads « stale »; it is never cleared in silence. A lease
// lasts TTL (4 h) from its holder's last claim: at its deadline it is lifted
// like a release (pigtaild's minute sweep, or any caller under the flock) and
// leaves runtime-claim-expired.json, which pigtaild journals (kind
// runtime-claim-expired).
package runtimeclaim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	FileName       = "runtime-claim.json"
	TokenName      = "runtime-claim.token"
	LockName       = "runtime-claim.lock"
	SeenDirName    = "runtime-claim-seen"
	MaxReasonBytes = 500
)

// TTL is how long a lease lasts from its holder's last claim.
const TTL = 4 * time.Hour

// State is the lease. Session == "" = the user outside Claude Code. PID and
// ConfigDir locate the holder's Claude process for the liveness check.
type State struct {
	ID         string    `json:"id"`
	Reason     string    `json:"reason"`
	PR         int       `json:"pr,omitempty"`
	Session    string    `json:"session,omitempty"`
	Label      string    `json:"label,omitempty"`
	LabelScope string    `json:"label_scope,omitempty"`
	PID        int       `json:"pid,omitempty"`
	ConfigDir  string    `json:"config_dir,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	SHA        string    `json:"sha,omitempty"`
	Dirty      bool      `json:"dirty,omitempty"`
	Since      time.Time `json:"since"`
	ExpiresAt  time.Time `json:"expires_at,omitzero"`
}

// Deadline is when the lease expires: ExpiresAt, or Since+TTL for a file
// written before the field existed.
func (s State) Deadline() time.Time {
	if !s.ExpiresAt.IsZero() {
		return s.ExpiresAt
	}
	return s.Since.Add(TTL)
}

// Expired: the deadline has been reached.
func (s State) Expired(now time.Time) bool { return !now.Before(s.Deadline()) }

type Liveness string

const (
	Alive   Liveness = "alive"
	Stale   Liveness = "stale"
	Unknown Liveness = "unknown"
)

// AliveFunc says whether the Claude process pid, under configDir, still hosts
// session (claudesession.SessionAlive in production).
type AliveFunc func(configDir string, pid int, session string) bool

// Liveness: the user is never stale; a holder without a located process is
// unknown (never taken over, never force-released by a session).
func (s State) Liveness(alive AliveFunc) Liveness {
	switch {
	case s.Session == "":
		return Alive
	case s.PID == 0 || s.ConfigDir == "" || alive == nil:
		return Unknown
	case alive(s.ConfigDir, s.PID, s.Session):
		return Alive
	}
	return Stale
}

// HeldError: another session holds the lease.
type HeldError struct {
	Cur      State
	Liveness Liveness
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("runtime claim %s held by session %q (%s)", e.Cur.ID, e.Cur.Session, e.Liveness)
}

// NotHolderError: a release by a caller that does not hold the lease.
type NotHolderError struct {
	Cur      State
	Liveness Liveness
}

func (e *NotHolderError) Error() string {
	return fmt.Sprintf("runtime claim %s is held by session %q (%s), not by the caller", e.Cur.ID, e.Cur.Session, e.Liveness)
}

// ErrLockTimeout: the lock stayed busy for lockWait — nothing was written.
var ErrLockTimeout = errors.New("runtime claim lock busy")

// lockWait bounds the wait for the lock (a seam for the tests).
var lockWait = 5 * time.Second

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "rtc_" + hex.EncodeToString(b[:])
}

// Load: absent → false, nil; unreadable or corrupt → an error without the
// path (the caller names it).
func Load(dir string) (State, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, causeOnly(err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, false, err
	}
	if st.ID == "" {
		return State{}, false, errors.New("no id")
	}
	return st, true, nil
}

// lock takes runtime-claim.lock exclusively, retrying a non-blocking flock
// every 10 ms for at most lockWait.
func lock(dir string) (func(), error) { return lockWithin(dir, lockWait) }

// lockWithin is lock with its own wait; 0 tries the flock exactly once.
func lockWithin(dir string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, causeOnly(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, LockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, causeOnly(err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrLockTimeout
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Claim sets or updates the lease for req.Session. The same session keeps its
// ID and Since; another holder refuses with *HeldError, unless takeOver and
// that holder is stale. An unreadable file is never overwritten.
func Claim(dir string, req State, takeOver bool, alive AliveFunc, now time.Time) (State, error) {
	unlock, err := lock(dir)
	if err != nil {
		return State{}, err
	}
	defer unlock()
	cur, ok, err := Load(dir)
	if err != nil {
		return State{}, err
	}
	if ok && cur.Expired(now) {
		if _, err := expireLocked(dir, cur, now); err != nil {
			return State{}, err
		}
		ok = false
	}
	st := req
	switch {
	case !ok:
		st.ID, st.Since = newID(), now
	case cur.Session == req.Session:
		st.ID, st.Since = cur.ID, cur.Since
	default:
		lv := cur.Liveness(alive)
		if !takeOver || lv != Stale {
			return State{}, &HeldError{Cur: cur, Liveness: lv}
		}
		st.ID, st.Since = newID(), now
		st.Reason = "took over from " + cur.Session + " — " + req.Reason
	}
	st.ExpiresAt = now.Add(TTL)
	if err := save(dir, st); err != nil {
		return State{}, err
	}
	return st, nil
}

// Release lifts the lease: by its holder, or with force by the user (session "")
// or over a stale holder. Nothing held → ok=false, nil. An unreadable file is
// deleted by the user's force only (repair). The token says « <id> off <epoch> »
// before the JSON goes: a crash in between leaves the lease held (safe side).
func Release(dir, session string, force bool, alive AliveFunc, now time.Time) (State, bool, error) {
	unlock, err := lock(dir)
	if err != nil {
		return State{}, false, err
	}
	defer unlock()
	cur, ok, err := Load(dir)
	switch {
	case err != nil && session == "" && force:
		cur = State{ID: rawTokenID(dir)}
	case err != nil:
		return State{}, false, err
	case ok && cur.Expired(now):
		_, err := expireLocked(dir, cur, now)
		return State{}, false, err
	case !ok:
		return State{}, false, nil
	case cur.Session == session:
	default:
		lv := cur.Liveness(alive)
		if !force || (session != "" && lv != Stale) {
			return State{}, false, &NotHolderError{Cur: cur, Liveness: lv}
		}
	}
	return cur, true, lift(dir, cur, now)
}

// lift is the Release sequence: token off, JSON removed, directory synced.
// The caller holds the lock.
func lift(dir string, cur State, now time.Time) error {
	if err := writeAtomic(filepath.Join(dir, TokenName), []byte(fmt.Sprintf("%s off %d\n", cur.ID, now.Unix()))); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, FileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return causeOnly(err)
	}
	return syncDir(dir)
}

func save(dir string, st State) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, FileName), append(raw, '\n')); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, TokenName), []byte(fmt.Sprintf("%s on %d\n", st.ID, st.Since.Unix())))
}

// unknownID names a lease whose token cannot be read (a valid token id, so
// the off notice still reaches the sessions that saw it).
const unknownID = "rtc_unknown"

func rawTokenID(dir string) string {
	if id, _, _, ok := readToken(dir); ok {
		return id
	}
	return unknownID
}

// causeOnly drops the path from a filesystem error (maintenance.causeOnly).
func causeOnly(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return fmt.Errorf("%s: %w", le.Op, le.Err)
	}
	return err
}

// writeAtomic: temp file in the same directory, fsync, rename, fsync of the
// directory (maintenance.writeAtomic).
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return causeOnly(err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return causeOnly(err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return causeOnly(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return causeOnly(err)
	}
	if err := tmp.Close(); err != nil {
		return causeOnly(err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return causeOnly(err)
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return causeOnly(err)
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
