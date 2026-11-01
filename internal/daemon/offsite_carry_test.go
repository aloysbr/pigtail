package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// This file is the structural prerequisite for the rest of the offsite
// fallback exercise slice: seven persistent fields are about to be added to
// store.OffsiteState by later tasks (declared_at in task 3, six fallback_*
// fields in task 6). attempt() used to rebuild its state from scratch on
// every copy, so any field it did not explicitly know about would be wiped
// by the first successful copy — up to once an hour. These tests pin the
// fix: attempt() now reads-modifies-writes through updateState, which holds
// stateMu across the whole cycle, so a field it does not own survives.

// The guard for the whole slice. store.OffsiteState.DeclaredAt is a field
// attempt() does not own — it belongs to declare() — the same role the two
// scaffolding fields task 2 added played before task 3 gave it a real field
// to check against. A successful attempt must not move it.
//
// Red on the pre-updateState attempt(): it always started from a fresh
// store.OffsiteState{...} literal that carries only LastOKAt and
// ConsecutiveFailures forward, so an unowned field like this one was
// silently dropped.
func TestAttemptCarriesForwardFieldsItDoesNotOwn(t *testing.T) {
	db, dest := dbHome(t), t.TempDir()
	declaredAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:             dest,
		LastOKAt:            time.Now().Add(-time.Hour),
		LastTryAt:           time.Now().Add(-time.Hour),
		LastErr:             "previous failure",
		SameDevice:          true,
		AttemptStartedAt:    time.Now().Add(-time.Hour),
		ConsecutiveFailures: 2,
		DeclaredAt:          declaredAt,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{
		copy: func(string, string, int) error { return nil },
		logf: func(string, ...interface{}) {},
	}
	o.runSync(db, dest)

	got := store.ReadOffsiteState(db)
	if !got.DeclaredAt.Equal(declaredAt) {
		t.Errorf("DeclaredAt = %v, want %v (carried forward unchanged: attempt() does not own this field)",
			got.DeclaredAt, declaredAt)
	}
}

// Symmetry with declare(): a changed destination is a different backup
// relationship, and its failure history belongs to it, not to whatever the
// previous destination reported. declare() already applies this rule
// (prev.DestDir == destDir); attempt() did not.
//
// Red on the pre-updateState attempt(): it carried LastOKAt and
// ConsecutiveFailures forward whenever prev.Configured() (i.e. whenever any
// destination at all was persisted), never checking that it was the *same*
// destination.
func TestAttemptOnANewDestinationStartsFresh(t *testing.T) {
	db := dbHome(t)
	oldDest := filepath.Join(t.TempDir(), "old")
	newDest := t.TempDir()
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:             oldDest,
		LastOKAt:            time.Now().Add(-time.Hour),
		LastErr:             "stale failure from the old destination",
		ConsecutiveFailures: 5,
		SameDevice:          true,
		DeclaredAt:          time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// The copy fails here on purpose: if attempt() wrongly carried the old
	// destination's failure history forward, the streak would read 6 (5
	// carried + this one), not 1.
	o := &offsiteBackupper{
		copy: func(string, string, int) error { return errors.New("new destination unreachable") },
		logf: func(string, ...interface{}) {},
	}
	o.runSync(db, newDest)

	got := store.ReadOffsiteState(db)
	if got.DestDir != newDest {
		t.Fatalf("DestDir = %q, want %q", got.DestDir, newDest)
	}
	if got.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1: a new destination inherited the old one's streak", got.ConsecutiveFailures)
	}
	if got.LastErr != "new destination unreachable" {
		t.Errorf("LastErr = %q, want the new destination's own error, not the old one's", got.LastErr)
	}
	if !got.LastOKAt.IsZero() {
		t.Errorf("LastOKAt = %v, want zero: a new destination has never succeeded", got.LastOKAt)
	}
	if !got.DeclaredAt.IsZero() {
		t.Errorf("DeclaredAt = %v, want zero: it belonged to the old destination", got.DeclaredAt)
	}
}

// updateState is the only path allowed to touch offsite-state.json after
// this task, including the exerciser landing in task 8 as a third writer.
// Two concurrent read-modify-write cycles, run under -race, must both land:
// neither one's mutation may be silently lost to the other, and the file
// left behind must still parse.
func TestUpdateStateConcurrentCallsBothApply(t *testing.T) {
	db := dbHome(t)
	o := &offsiteBackupper{logf: func(string, ...interface{}) {}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		o.updateState(db, func(s *store.OffsiteState) bool {
			s.DestDir = "/dest"
			s.ConsecutiveFailures++
			return true
		})
	}()
	go func() {
		defer wg.Done()
		o.updateState(db, func(s *store.OffsiteState) bool {
			s.LastErr = "concurrent"
			s.ConsecutiveFailures++
			return true
		})
	}()
	wg.Wait()

	body, err := os.ReadFile(store.OffsiteStatePath(db))
	if err != nil {
		t.Fatalf("state file unreadable after concurrent updateState calls: %v", err)
	}
	var onWire store.OffsiteState
	if err := json.Unmarshal(body, &onWire); err != nil {
		t.Fatalf("state file is not valid JSON after concurrent updateState calls: %v", err)
	}

	got := store.ReadOffsiteState(db)
	if got.DestDir != "/dest" {
		t.Errorf("DestDir = %q, want /dest", got.DestDir)
	}
	if got.LastErr != "concurrent" {
		t.Errorf("LastErr = %q, want %q", got.LastErr, "concurrent")
	}
	if got.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2: both goroutines' increments must apply, "+
			"neither lost to the other", got.ConsecutiveFailures)
	}
}

// updateState skips the write entirely when fn declines to mutate — the
// abandon branch in attempt() (and any future caller) must leave the file
// exactly as found, not rewrite it with content that is byte-for-byte
// identical to what was already on disk.
//
// Red on a version of updateState that always writes after fn returns,
// regardless of what fn reports: mtime would change on the second call even
// though nothing about the content did.
func TestUpdateStateSkipsTheWriteWhenFnDeclinesToMutate(t *testing.T) {
	db := dbHome(t)
	o := &offsiteBackupper{logf: func(string, ...interface{}) {}}

	o.updateState(db, func(s *store.OffsiteState) bool {
		s.DestDir = "/dest"
		return true
	})
	path := store.OffsiteStatePath(db)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	state, applied, _ := o.updateState(db, func(s *store.OffsiteState) bool {
		return false
	})
	if applied {
		t.Fatal("applied = true, want false: fn declined to mutate")
	}
	if state.DestDir != "/dest" {
		t.Errorf("returned state DestDir = %q, want /dest: it should still be the state that was read",
			state.DestDir)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("state file mtime moved from %v to %v: a declined mutation must not write at all",
			before.ModTime(), after.ModTime())
	}
}

// The guard the abandon branch exists for. attempt()'s second updateState
// call abstains when the persisted destination no longer matches this
// attempt's — a concurrent redeclare landed mid-copy, which is exactly what
// the offsite fallback exerciser (a later task) does as a third writer.
// Abstaining must mean *no* transition is noted at all: noteTransition
// classifies the state it is handed and crosses it with this attempt's own
// err and destDir, so running it against the redeclared destination's state
// would log — and flip the in-RAM failing/alarming flags — in the wrong
// destination's name.
func TestAttemptNotesNoTransitionWhenDestinationChangedMidCopy(t *testing.T) {
	db := dbHome(t)
	dest := t.TempDir()
	otherDest := t.TempDir()
	otherOK := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	var mu sync.Mutex
	var lines []string
	o := &offsiteBackupper{
		copy: func(string, string, int) error {
			// Simulate a redeclare landing on a different destination while
			// this copy is still running.
			if err := store.WriteOffsiteState(db, store.OffsiteState{
				DestDir:  otherDest,
				LastOKAt: otherOK,
			}); err != nil {
				t.Fatal(err)
			}
			return errors.New("boom")
		},
		logf: func(format string, args ...interface{}) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf(format, args...))
		},
	}

	o.runSync(db, dest)

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("attempt logged %v on an abandoned outcome, want no lines at all", got)
	}

	o.mu.Lock()
	failing, alarming := o.failing, o.alarming
	o.mu.Unlock()
	if failing || alarming {
		t.Errorf("in-RAM flags changed from an abandoned attempt (failing=%v, alarming=%v), want both untouched",
			failing, alarming)
	}

	final := store.ReadOffsiteState(db)
	if final.DestDir != otherDest {
		t.Fatalf("DestDir = %q, want %q: the redeclared destination's own state must survive untouched",
			final.DestDir, otherDest)
	}
	if !final.LastOKAt.Equal(otherOK) {
		t.Errorf("LastOKAt = %v, want %v: the abandoned attempt must not have written anything over it",
			final.LastOKAt, otherOK)
	}
}

// The behaviour change this task assumes and must test: a same-device
// detection that fails must not reset the persisted value to false. A
// transiently failing stat must not make a real "shares a filesystem"
// warning silently disappear.
//
// Mutation: unconditionally assigning s.SameDevice = same (dropping the
// sameKnown gate) makes this fail, since same defaults to false when
// sameDeviceFn errors.
func TestAttemptKeepsThePersistedSameDeviceValueWhenDetectionFails(t *testing.T) {
	db, dest := dbHome(t), t.TempDir()
	if err := store.WriteOffsiteState(db, store.OffsiteState{DestDir: dest, SameDevice: true}); err != nil {
		t.Fatal(err)
	}
	o := &offsiteBackupper{
		copy:         func(string, string, int) error { return nil },
		logf:         func(string, ...interface{}) {},
		sameDeviceFn: func(string, string) (bool, error) { return false, errors.New("stat: transient failure") },
	}
	o.runSync(db, dest)

	if got := store.ReadOffsiteState(db); !got.SameDevice {
		t.Error("SameDevice = false after a failed detection, want the previously persisted true preserved")
	}
}

// The mirror case: a successful detection is applied even when it changes
// the value.
func TestAttemptAppliesANewSameDeviceValueWhenDetectionSucceeds(t *testing.T) {
	db, dest := dbHome(t), t.TempDir()
	if err := store.WriteOffsiteState(db, store.OffsiteState{DestDir: dest, SameDevice: false}); err != nil {
		t.Fatal(err)
	}
	o := &offsiteBackupper{
		copy:         func(string, string, int) error { return nil },
		logf:         func(string, ...interface{}) {},
		sameDeviceFn: func(string, string) (bool, error) { return true, nil },
	}
	o.runSync(db, dest)

	if got := store.ReadOffsiteState(db); !got.SameDevice {
		t.Error("SameDevice = false after a successful detection reporting true, want it applied")
	}
}
