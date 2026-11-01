package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// offsite-state.json changed criticality without changing how it is written.
// It used to be a display aid; since the previous slice it carries an alarm
// (ConsecutiveFailures), and the current slice adds more fields on top. A
// truncated read of it does not fail — ReadOffsiteState degrades to the zero
// value, Configured() reads that as "no destination", and the next declare()
// resets the failure counter it exists to protect. These tests guard the fix:
// WriteOffsiteState must never let a reader observe a state that is neither
// the previous nor the next complete write.

// TestWriteOffsiteStateAtomicUnderConcurrentReads is the test that matters for
// this task. A reader loops on ReadOffsiteState while a writer loops on
// WriteOffsiteState with a large LastErr (widening the write window so a torn
// read isn't a matter of luck); the reader must never see an unconfigured
// state, because DestDir never actually goes away here — only the bytes on
// disk are momentarily mid-flight.
//
// Under the old os.WriteFile, O_TRUNC opens a zero-byte window between
// truncation and the new bytes landing; a read in that window sees an empty
// file, which json.Unmarshal errors on, which ReadOffsiteState turns into the
// zero value, which reads as unconfigured. That failure mode is what this
// test is for: it reproduces in a few milliseconds under the old
// implementation and must never reproduce under the new one. Run with -race.
func TestWriteOffsiteStateAtomicUnderConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")

	bigErr := strings.Repeat("x", 256*1024)
	state := OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastTryAt: time.Now(),
		LastErr:   bigErr,
	}

	// Establish a first complete write synchronously: the reader must not
	// race the very first write, or it would report a false failure for a
	// reason unrelated to atomicity (no file exists yet).
	if err := WriteOffsiteState(db, state); err != nil {
		t.Fatalf("WriteOffsiteState (initial): %v", err)
	}

	var stop atomic.Bool
	var sawUnconfigured atomic.Bool
	var writeErr atomic.Value // stores error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			if got := ReadOffsiteState(db); !got.Configured() {
				sawUnconfigured.Store(true)
				stop.Store(true)
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) && !stop.Load() {
			if err := WriteOffsiteState(db, state); err != nil {
				writeErr.Store(err)
				stop.Store(true)
				return
			}
		}
		stop.Store(true)
	}()

	wg.Wait()

	if err, ok := writeErr.Load().(error); ok && err != nil {
		t.Fatalf("WriteOffsiteState: %v", err)
	}
	if sawUnconfigured.Load() {
		t.Fatal("ReadOffsiteState observed an unconfigured state while writes were in flight")
	}
}

// A crashed or interrupted write must not leave *.tmp-* debris beside the
// real file: nothing sweeps this directory, so any temp file that survives a
// successful write accumulates forever.
func TestWriteOffsiteStateLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")

	state := OffsiteState{DestDir: "/Volumes/nas/pigtail", LastTryAt: time.Now()}
	for i := 0; i < 20; i++ {
		if err := WriteOffsiteState(db, state); err != nil {
			t.Fatalf("WriteOffsiteState (%d): %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), offsiteStateFileName+".tmp-") {
			t.Errorf("leftover temp file after successful writes: %s", e.Name())
		}
	}
}

// The published file must keep the same mode as before: it can carry
// destination paths and error text, not secrets, but there is no reason to
// widen its permissions in the course of making the write atomic.
func TestWriteOffsiteStateFileMode(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")

	if err := WriteOffsiteState(db, OffsiteState{DestDir: "/Volumes/nas/pigtail"}); err != nil {
		t.Fatalf("WriteOffsiteState: %v", err)
	}

	info, err := os.Stat(OffsiteStatePath(db))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

// A state file left truncated by a past crash — exactly the failure this
// task closes off — must be fully replaced by the next successful write, not
// merged with or padded around.
func TestWriteOffsiteStateReplacesATruncatedFile(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	path := OffsiteStatePath(db)

	// Simulate the old failure mode directly: a state file cut off mid-write.
	if err := os.WriteFile(path, []byte(`{"version":1,"dest_d`), 0o600); err != nil {
		t.Fatalf("seed truncated file: %v", err)
	}
	if got := ReadOffsiteState(db); got.Configured() {
		t.Fatal("precondition: truncated seed file must read as unconfigured")
	}

	want := OffsiteState{
		DestDir:             "/Volumes/nas/pigtail",
		LastTryAt:           time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC),
		ConsecutiveFailures: 2,
	}
	if err := WriteOffsiteState(db, want); err != nil {
		t.Fatalf("WriteOffsiteState: %v", err)
	}

	got := ReadOffsiteState(db)
	if !got.Configured() {
		t.Fatal("state still reads as unconfigured after a successful write")
	}
	if got.DestDir != want.DestDir {
		t.Errorf("DestDir = %q, want %q", got.DestDir, want.DestDir)
	}
	if got.ConsecutiveFailures != want.ConsecutiveFailures {
		t.Errorf("ConsecutiveFailures = %d, want %d", got.ConsecutiveFailures, want.ConsecutiveFailures)
	}
	if !got.LastTryAt.Equal(want.LastTryAt) {
		t.Errorf("LastTryAt = %v, want %v", got.LastTryAt, want.LastTryAt)
	}
}
