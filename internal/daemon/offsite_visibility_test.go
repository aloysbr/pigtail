package daemon

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// Observed in production, on the very first real use of the
// offsite copy: the destination was configured, the daemon took its local
// backup, and then **nothing** — no copy on the NAS, no line in the log, no
// offsite-state.json, and therefore no row in `pigtail db list`. The feature
// whose entire purpose was to make the guarantee checkable was silently
// inert, and `db list` kept showing three healthy generations as though
// everything were fine.
//
// The cause is structural rather than incidental: the state was only ever
// written *after* the copy returned. Anything that stops the copy from
// returning — a hung network mount, a permission wall the syscall never
// reports, a goroutine that dies — reports nothing at all. The absence of a
// report was indistinguishable from "no destination configured".
//
// These tests pin the three properties that close it.

// The load-bearing one. A copy that never returns must still leave a readable
// state behind, because that is the exact shape of the production failure.
func TestOffsiteStateIsWrittenBeforeTheCopyRuns(t *testing.T) {
	db := dbHome(t)
	dest := filepath.Join(t.TempDir(), "dest")

	entered := make(chan struct{})
	release := make(chan struct{})
	o := &offsiteBackupper{
		copy: func(string, string, int) error {
			close(entered)
			<-release // stands in for a copy that never comes back
			return nil
		},
	}
	go o.runSync(db, dest)
	<-entered

	// While the copy is still in flight, the state must already say so.
	got := store.ReadOffsiteState(db)
	if !got.Configured() {
		t.Fatal("no state written before the copy ran; a copy that never returns would report nothing")
	}
	if got.DestDir != dest {
		t.Errorf("DestDir = %q, want %q", got.DestDir, dest)
	}
	if got.AttemptStartedAt.IsZero() {
		t.Fatal("AttemptStartedAt is zero while a copy is in flight; " +
			"nothing distinguishes a hung attempt from an idle one")
	}
	if !got.Attempting() {
		t.Fatal("Attempting() = false while a copy is in flight")
	}

	close(release)
	waitFor(t, func() bool { return !store.ReadOffsiteState(db).Attempting() })

	// And once it finishes, the in-flight marker is cleared rather than left
	// to make every later reader think a copy is stuck.
	done := store.ReadOffsiteState(db)
	if done.AttemptStartedAt != (time.Time{}) {
		t.Errorf("AttemptStartedAt = %v after completion, want cleared", done.AttemptStartedAt)
	}
	if done.LastOKAt.IsZero() {
		t.Error("LastOKAt not stamped after a successful copy")
	}
}

// A failed copy must clear the in-flight marker too, or the next reader sees a
// stuck attempt that is really just a plain failure.
func TestOffsiteFailureClearsTheInFlightMarker(t *testing.T) {
	db := dbHome(t)
	dest := filepath.Join(t.TempDir(), "dest")
	o := &offsiteBackupper{
		copy: func(string, string, int) error { return errors.New("nope") },
		logf: func(string, ...interface{}) {},
	}
	o.runSync(db, dest)

	got := store.ReadOffsiteState(db)
	if got.Attempting() {
		t.Fatal("still marked as attempting after a failed copy")
	}
	if got.LastErr == "" {
		t.Fatal("LastErr empty after a failed copy")
	}
}

// The other half of the production failure: if `attempt` is never reached at
// all, there is no file, and `db list` cannot tell "configured but nothing ever
// happened" from "not configured". Declaring the destination at startup is what
// makes the absence of a report impossible.
func TestOffsiteDeclaresItsDestinationBeforeAnyAttempt(t *testing.T) {
	db := dbHome(t)
	dest := filepath.Join(t.TempDir(), "dest")

	o := &offsiteBackupper{copy: func(string, string, int) error { return nil }}
	o.declare(db, dest, "")

	got := store.ReadOffsiteState(db)
	if !got.Configured() {
		t.Fatal("declaring a destination wrote no state; its absence stays ambiguous")
	}
	if got.DestDir != dest {
		t.Errorf("DestDir = %q, want %q", got.DestDir, dest)
	}
	if !got.LastOKAt.IsZero() || got.Attempting() {
		t.Errorf("a declared-but-unattempted destination must look untried, got %+v", got)
	}
}

// Declaring must not erase what a previous daemon already knew: a restart is
// not evidence that the last copy never landed.
func TestOffsiteDeclarePreservesAPreviousSuccess(t *testing.T) {
	db := dbHome(t)
	dest := filepath.Join(t.TempDir(), "dest")
	landed := time.Now().Add(-2 * time.Hour).Round(time.Millisecond)

	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir: dest, LastOKAt: landed, LastTryAt: landed,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{}
	o.declare(db, dest, "")

	got := store.ReadOffsiteState(db)
	if got.LastOKAt.IsZero() {
		t.Fatal("declaring the destination wiped the previous success")
	}
	// A restart also clears any in-flight marker: whatever goroutine it
	// described died with the previous process.
	if got.Attempting() {
		t.Error("a restart must not inherit an in-flight marker from a dead process")
	}
}

// A destination that changed must not keep reporting the old one's success.
func TestOffsiteDeclareResetsOnANewDestination(t *testing.T) {
	db := dbHome(t)
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir: "/old/place", LastOKAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{}
	o.declare(db, "/new/place", "")

	got := store.ReadOffsiteState(db)
	if got.DestDir != "/new/place" {
		t.Fatalf("DestDir = %q, want the new destination", got.DestDir)
	}
	if !got.LastOKAt.IsZero() {
		t.Error("a new destination inherited the old one's success timestamp")
	}
}

// Concurrency guard: declare and attempt both write the same file.
func TestOffsiteStateWritesAreSerialised(t *testing.T) {
	db := dbHome(t)
	dest := filepath.Join(t.TempDir(), "dest")
	o := &offsiteBackupper{
		copy: func(string, string, int) error { return nil },
		logf: func(string, ...interface{}) {},
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); o.declare(db, dest, "") }()
		wg.Add(1)
		go func() { defer wg.Done(); o.runSync(db, dest) }()
	}
	wg.Wait()

	if got := store.ReadOffsiteState(db); !got.Configured() {
		t.Fatal("concurrent writers left an unreadable state file")
	}
}

// A 38-minute copy is what production actually produced (first
// real use, SMB mount waking up). It exposed a second reporting flaw: the
// success was stamped with the time the attempt *started*, so `db list` said
// "last copy: 1h05m ago" for a copy that had finished 27 minutes earlier.
//
// The distinction matters precisely when it is largest — a slow destination is
// also the one whose freshness you want to judge — so the stamp is the moment
// the copy landed, while LastTryAt stays the moment it began.
func TestOffsiteStampsSuccessAtCompletionNotAtStart(t *testing.T) {
	db := dbHome(t)
	dest := filepath.Join(t.TempDir(), "dest")

	const slow = 120 * time.Millisecond
	o := &offsiteBackupper{
		copy: func(string, string, int) error { time.Sleep(slow); return nil },
	}
	o.runSync(db, dest)

	got := store.ReadOffsiteState(db)
	if got.LastOKAt.IsZero() || got.LastTryAt.IsZero() {
		t.Fatalf("timestamps missing: %+v", got)
	}
	elapsed := got.LastOKAt.Sub(got.LastTryAt)
	if elapsed < slow {
		t.Fatalf("LastOKAt - LastTryAt = %v, want at least the %v the copy took; "+
			"the success is being stamped when the attempt started", elapsed, slow)
	}
}
