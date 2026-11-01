package claudeconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func shortLock(t *testing.T) {
	t.Helper()
	w, s := lockWait, lockStale
	t.Cleanup(func() { lockWait, lockStale = w, s })
	lockWait, lockStale = 150*time.Millisecond, 10*time.Second
}

func TestLockLiveHolderTimesOut(t *testing.T) {
	shortLock(t)
	lock := filepath.Join(t.TempDir(), ".claude.json.lock")
	if err := os.Mkdir(lock, 0o755); err != nil { // a live holder: fresh mtime
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := acquire(lock); err == nil {
		t.Fatal("acquired a lock a live writer holds")
	}
	if d := time.Since(start); d < lockWait || d > lockWait+time.Second {
		t.Fatalf("waited %v, want about %v", d, lockWait)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("the holder's lock was removed: %v", err)
	}
}

func TestLockStaleIsTakenOver(t *testing.T) {
	shortLock(t)
	lock := filepath.Join(t.TempDir(), ".claude.json.lock")
	if err := os.Mkdir(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-11 * time.Second)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := acquire(lock)
	if err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	release()
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("lock left after release: %v", err)
	}
}

func TestWriteAtomicMode0600NoTmpLeft(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(p, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	fi, _ := os.Stat(p)
	if string(b) != `{"a":1}` || fi.Mode().Perm() != 0o600 {
		t.Fatalf("content %q mode %v", b, fi.Mode().Perm())
	}
	if m, _ := filepath.Glob(p + ".tmp.*"); len(m) != 0 {
		t.Fatalf("tmp files left: %v", m)
	}
}
