package cmuxcheck

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	release, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); err == nil {
		t.Fatal("a second check while one runs must be refused")
	}
	release()
	r2, err := Lock(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	r2()
}

// T-f: the marker names this process while a typing check runs, gone after
// release; touch keeps it fresh.
func TestMarkRunning(t *testing.T) {
	dir := t.TempDir()
	touch, release, err := MarkRunning(dir)
	if err != nil {
		t.Fatal(err)
	}
	self := func(pid int) bool { return pid == os.Getpid() }
	if !Running(dir, self, time.Now()) {
		t.Fatal("running file must name this process")
	}
	touch()
	release()
	if Running(dir, func(int) bool { return true }, time.Now()) {
		t.Fatal("running after release")
	}
}

// Review Focus 4: a file left by a killed check — dead pid, unreadable pid,
// or older than RunningMaxAge (its pid reused by another process) — holds
// nothing.
func TestRunningStaleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RunningFile)
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	write("999999")
	if Running(dir, func(int) bool { return false }, now) {
		t.Fatal("a dead pid holds nothing")
	}
	write("garbage")
	if Running(dir, func(int) bool { return true }, now) {
		t.Fatal("an unreadable pid holds nothing")
	}
	write("4242")
	if !Running(dir, func(int) bool { return true }, now) { // revue 2 M8: positive control before the mtime branch
		t.Fatal("a fresh file naming a live pid must hold")
	}
	if err := os.Chtimes(path, now.Add(-RunningMaxAge-time.Minute), now.Add(-RunningMaxAge-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if Running(dir, func(int) bool { return true }, now) {
		t.Fatal("a file older than RunningMaxAge holds nothing (pid reuse)")
	}
}
