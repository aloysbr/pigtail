package cmuxcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Lock takes <dir>/lock exclusively (non-blocking): the daemon's run and a
// hand-typed one never type into the same panes at once.
func Lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another pigtail cmux check is running")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// RunningFile holds the pid of a check that types into panes (--fix):
// pigtaild's relay reads it to hold its idle wake meanwhile (T-f). Its mtime
// is refreshed before each row: a file older than RunningMaxAge is stale —
// the check was killed and its pid may name another process now.
const (
	RunningFile   = "running"
	RunningMaxAge = 20 * time.Minute
)

// MarkRunning writes the running file (after Lock, only when the pass
// types); touch refreshes its mtime, release removes it.
func MarkRunning(dir string) (touch func(), release func(), err error) {
	path := filepath.Join(dir, RunningFile)
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return nil, nil, err
	}
	touch = func() { now := time.Now(); _ = os.Chtimes(path, now, now) }
	release = func() { _ = os.Remove(path) }
	return touch, release, nil
}

// Running: a typing check runs now — its running file names a live pid and
// was refreshed within RunningMaxAge. Reads a file, never cmux.
func Running(dir string, alive func(int) bool, now time.Time) bool {
	path := filepath.Join(dir, RunningFile)
	fi, err := os.Stat(path)
	if err != nil || now.Sub(fi.ModTime()) > RunningMaxAge {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && alive(pid)
}
