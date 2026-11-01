package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// syncBuf is a mutex-guarded byte buffer, used wherever a test reads output
// (the redirected global `log` package, an offsiteBackupper.logf sink fed
// from a background goroutine) that a concurrently-running Run() or
// exerciseFallbackIfDue call is still writing to. bytes.Buffer's own methods
// are not safe for that: a bare bytes.Buffer read racing its writer is
// exactly the kind of race `go test -race` exists to catch.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// A successful exercise pins fallback_last_ok_at and fallback_via, and
// resets the failure streak to zero.
func TestExerciseFallbackIfDueRecordsASuccess(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:                     dest,
		FallbackDest:                fallback,
		FallbackConsecutiveFailures: 3,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{
		logf:  func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) { return "100.64.0.7", nil },
	}
	o.exerciseFallbackIfDue(db, dest, fallback)

	got := store.ReadOffsiteState(db)
	if got.FallbackLastOKAt.IsZero() {
		t.Error("FallbackLastOKAt is zero after a successful exercise")
	}
	if got.FallbackVia != "100.64.0.7" {
		t.Errorf("FallbackVia = %q, want the probe's reported address", got.FallbackVia)
	}
	if got.FallbackConsecutiveFailures != 0 {
		t.Errorf("FallbackConsecutiveFailures = %d, want 0 after a success", got.FallbackConsecutiveFailures)
	}
	if got.FallbackLastTryAt.IsZero() {
		t.Error("FallbackLastTryAt is zero after an exercise ran")
	}
}

// A failed exercise increments the streak, and two in a row read as warn
// through store.ClassifyOffsiteFallback — the pure function this package
// must never reimplement its own copy of.
func TestExerciseFallbackIfDueStreakReachesWarn(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	// A healthy primary, deliberately, to keep this test's own severity
	// assertion unambiguous: ClassifyOffsiteFallback mutes the fallback's
	// own severity to ok whenever the primary itself needs attention (see
	// store/offsite_fallback.go's doc comment, and
	// store.primaryOffsiteNeedsAttention specifically) — a bare, genuine
	// failure or a shared-filesystem destination, not a merely-quiet one:
	// a DestDir with no LastOKAt/LastTryAt on record reads as no_attempt,
	// which is routine and does NOT mute the fallback (ruling 2, final
	// whole-branch review), but there is no reason to exercise that here
	// too when a plainly healthy primary already isolates what this test
	// means to observe.
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:      dest,
		LastOKAt:     time.Now(),
		LastTryAt:    time.Now(),
		FallbackDest: fallback,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{
		logf:  func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) { return "", errors.New("connection refused") },
	}

	o.exerciseFallbackIfDue(db, dest, fallback)
	got := store.ReadOffsiteState(db)
	if got.FallbackConsecutiveFailures != 1 {
		t.Fatalf("after one failure, ConsecutiveFailures = %d, want 1", got.FallbackConsecutiveFailures)
	}

	// Back-date the try so the second call reads as due (see the dedicated
	// deadline test below for the case where it should NOT be due yet).
	got.FallbackLastTryAt = time.Now().Add(-offsiteFallbackDueInterval - time.Minute)
	if err := store.WriteOffsiteState(db, got); err != nil {
		t.Fatal(err)
	}

	o.exerciseFallbackIfDue(db, dest, fallback)
	got = store.ReadOffsiteState(db)
	if got.FallbackConsecutiveFailures != 2 {
		t.Fatalf("after two failures, ConsecutiveFailures = %d, want 2", got.FallbackConsecutiveFailures)
	}

	primary := store.ClassifyOffsite(got, time.Now())
	health := store.ClassifyOffsiteFallback(got, primary, time.Now())
	if health.Severity != store.SeverityWarn {
		t.Errorf("severity after two consecutive failures = %v, want warn", health.Severity)
	}
}

// The persisted deadline, not an in-process ticker: two calls back-to-back
// must probe only once, and the second call probes only once
// FallbackLastTryAt has been pushed more than six hours into the past.
//
// Mutation: a version of exerciseFallbackIfDue that gates on a bare ticker
// (or on no deadline check at all) makes this fail, since it would probe on
// every call regardless of what is persisted.
func TestExerciseFallbackIfDueOnlyProbesOnceUntilDeadlinePasses(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:      dest,
		FallbackDest: fallback,
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	calls := 0
	o := &offsiteBackupper{
		logf: func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return "127.0.0.1", nil
		},
	}

	o.exerciseFallbackIfDue(db, dest, fallback)
	o.exerciseFallbackIfDue(db, dest, fallback)
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("two back-to-back calls made %d probes, want exactly 1: not due yet", got)
	}

	// Push the last try 6h01 into the past: the exercise is due again.
	state := store.ReadOffsiteState(db)
	state.FallbackLastTryAt = time.Now().Add(-6*time.Hour - time.Minute)
	if err := store.WriteOffsiteState(db, state); err != nil {
		t.Fatal(err)
	}

	o.exerciseFallbackIfDue(db, dest, fallback)
	mu.Lock()
	got = calls
	mu.Unlock()
	if got != 2 {
		t.Fatalf("after the deadline passed, calls = %d, want 2: the second probe never ran", got)
	}
}

// The interval's lower bound, the half TestExerciseFallbackIfDueOnlyProbesOn-
// ceUntilDeadlinePasses above never exercises: a last try five hours
// fifty-nine minutes ago is not due yet, not even one minute short of the
// full six hours.
//
// store.OffsiteFallbackInterval had no test anywhere pinning a floor on the
// real threshold exerciseFallbackIfDue reads (verified by grep: before this
// test, store/offsite_fallback_test.go's own contract test was the only
// thing checking the constant's value, and nothing checked that a value
// just under it is refused) — lowering it to one second would have passed
// every other test in this file.
//
// Mutation: lowering offsiteFallbackDueInterval (equivalently,
// store.OffsiteFallbackInterval, its one source now) below 5h59m makes this
// test fail — a probe would fire.
func TestExerciseFallbackIfDueNotYetDueAtFiveHoursFiftyNine(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:           dest,
		FallbackDest:      fallback,
		FallbackLastTryAt: time.Now().Add(-(5*time.Hour + 59*time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}

	called := false
	o := &offsiteBackupper{
		logf:  func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) { called = true; return "127.0.0.1", nil },
	}
	o.exerciseFallbackIfDue(db, dest, fallback)
	if called {
		t.Fatal("the probe ran with the last try only 5h59m in the past: not due yet")
	}
}

// exerciseFallbackIfDue must never run against a database with no file
// beside it to keep a persisted deadline in: store.OffsiteStatePath(":memory:")
// is "", which makes ReadOffsiteState always the zero value and
// WriteOffsiteState a silent no-op (see offsite_state.go) — without this
// early return, the "not due yet" check would never see a persisted try and
// the exercise would run on every tick.
func TestExerciseFallbackIfDueReturnsEarlyWithNoFileBackedState(t *testing.T) {
	dest, fallback := "nas:/primary", "nas-fallback:/primary"

	called := false
	o := &offsiteBackupper{
		logf:  func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) { called = true; return "127.0.0.1", nil },
	}
	o.exerciseFallbackIfDue(":memory:", dest, fallback)
	if called {
		t.Fatal("the probe ran against an in-memory database with no state path to persist a deadline to")
	}
}

// The in-memory floor's whole reason to exist: a write that never lands
// must not make the very next call read the deadline as already passed.
// Simulated the same way store/offsite_fallback_probe_test.go's own
// TestOffsiteFallbackProbeScriptFailsOnReadOnlyDir simulates a read-only
// destination, in the neighboring package — chmod the state file's
// directory read-only after the first, successful write, so the second
// exercise's own WriteOffsiteState fails inside updateState (logged, then
// swallowed) while ReadOffsiteState keeps returning the state from before
// the directory was locked down.
//
// Mutation: removing the fbLastTryFloor read/write in exerciseFallbackIfDue
// (falling back to reading only the disk value) makes this test fail — the
// second call would see a stale, pre-write FallbackLastTryAt and probe
// again immediately.
func TestExerciseFallbackIfDueFloorSurvivesAWriteFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:      dest,
		FallbackDest: fallback,
	}); err != nil {
		t.Fatal(err)
	}

	// Make the directory unwritable: WriteOffsiteState's os.CreateTemp for
	// its own atomic rename will fail from here on, on the same model as a
	// NAS gone read-only mid-outage.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // let TempDir's own cleanup remove it

	var mu sync.Mutex
	calls := 0
	o := &offsiteBackupper{
		logf: func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return "127.0.0.1", nil
		},
	}

	o.exerciseFallbackIfDue(db, dest, fallback)
	o.exerciseFallbackIfDue(db, dest, fallback)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("two back-to-back calls made %d probes after the state write started failing, want exactly 1: "+
			"the in-memory floor must stand in for the persisted deadline that never landed", got)
	}
}

// The guard the whole slice depends on: the primary copy and the fallback
// exercise must never be able to block one another, in either direction.
// Two subtests, one per direction — a single test that only ever blocked
// the copy first would leave fbInFlight sitting at its zero value the
// whole time, so a future regression that made run()/runSync() check
// fbInFlight instead of inFlight (or vice versa) could still pass it.
// Each direction is independently mutation-tested — see task-8-report.md.
func TestOffsiteCopyAndFallbackExerciseNeverBlockEachOther(t *testing.T) {
	t.Run("a blocked primary copy does not block the fallback exercise", func(t *testing.T) {
		db := dbHome(t)
		primaryDest := "nas:/primary"
		fallbackDest := "nas-fallback:/primary"

		copyRelease := make(chan struct{})
		copyEntered := make(chan struct{})
		probeRelease := make(chan struct{})
		probeEntered := make(chan struct{})

		o := &offsiteBackupper{
			logf: func(string, ...interface{}) {},
			copy: func(string, string, int) error {
				close(copyEntered)
				<-copyRelease
				return nil
			},
			probe: func(context.Context, string) (string, error) {
				close(probeEntered)
				<-probeRelease
				return "127.0.0.1", nil
			},
		}

		copyDone := make(chan struct{})
		go func() {
			o.runSync(db, primaryDest)
			close(copyDone)
		}()
		<-copyEntered

		exerciseDone := make(chan struct{})
		go func() {
			o.exerciseFallbackIfDue(db, primaryDest, fallbackDest)
			close(exerciseDone)
		}()

		select {
		case <-probeEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("the fallback exercise never started while the primary copy was blocked: " +
				"they must not share a slot")
		}

		// Both are now blocked, deliberately, at the same time. Release the
		// probe first and confirm it can finish while the copy is STILL
		// stuck — proving the exercise never waited on the copy's slot.
		close(probeRelease)
		select {
		case <-exerciseDone:
		case <-time.After(2 * time.Second):
			t.Fatal("the fallback exercise did not finish even though its own probe returned; " +
				"it must not be blocked by the in-flight primary copy")
		}

		select {
		case <-copyDone:
			t.Fatal("the primary copy finished before its own release — the test is broken")
		default:
		}

		close(copyRelease)
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
			t.Fatal("the primary copy never finished")
		}
	})

	// The reverse direction, and the one a same-flag regression would
	// actually be caught by: fbInFlight sits at true for as long as the
	// probe below is blocked, which is exactly the state a copy that
	// mistakenly checked fbInFlight instead of inFlight would see.
	//
	// Mutation: swap run()/runSync()'s "if o.inFlight" guard (offsite.go)
	// for "if o.fbInFlight" and this subtest goes red — the copy waits on
	// the still-blocked exercise instead of starting immediately. See
	// task-8-report.md for the verified red run.
	t.Run("a blocked fallback exercise does not block the primary copy", func(t *testing.T) {
		db := dbHome(t)
		primaryDest := "nas:/primary"
		fallbackDest := "nas-fallback:/primary"

		copyRelease := make(chan struct{})
		copyEntered := make(chan struct{})
		probeRelease := make(chan struct{})
		probeEntered := make(chan struct{})

		o := &offsiteBackupper{
			logf: func(string, ...interface{}) {},
			copy: func(string, string, int) error {
				close(copyEntered)
				<-copyRelease
				return nil
			},
			probe: func(context.Context, string) (string, error) {
				close(probeEntered)
				<-probeRelease
				return "127.0.0.1", nil
			},
		}

		exerciseDone := make(chan struct{})
		go func() {
			o.exerciseFallbackIfDue(db, primaryDest, fallbackDest)
			close(exerciseDone)
		}()
		<-probeEntered

		copyDone := make(chan struct{})
		go func() {
			o.runSync(db, primaryDest)
			close(copyDone)
		}()

		select {
		case <-copyEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("the primary copy never started while the fallback exercise was blocked: " +
				"they must not share a slot")
		}

		// Both are now blocked, deliberately, at the same time. Release the
		// copy first and confirm it can finish while the exercise is STILL
		// stuck — proving the copy never waited on the exercise's slot.
		close(copyRelease)
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
			t.Fatal("the primary copy did not finish even though its own copy func returned; " +
				"it must not be blocked by the in-flight fallback exercise")
		}

		select {
		case <-exerciseDone:
			t.Fatal("the fallback exercise finished before its own release — the test is broken")
		default:
		}

		close(probeRelease)
		select {
		case <-exerciseDone:
		case <-time.After(2 * time.Second):
			t.Fatal("the fallback exercise never finished")
		}
	})
}

// The guard that protects the whole task-8 slice: an exercise that lands
// while a slow primary copy is mid-flight must not erase — or be erased by
// — the primary copy's own final write. Both write through updateState,
// which read-modify-writes rather than replacing the file, so each one's
// fields survive the other's completion.
//
// Mutation: revert attempt()'s second updateState mutation to reconstruct
// the whole store.OffsiteState by hand (a wholesale `*s = store.OffsiteState{...}`
// literal, the shape attempt() used before task 3's updateState refactor)
// instead of mutating only the fields it owns, and this test goes red: the
// fallback_* fields the exercise wrote mid-copy are wiped by the copy's own
// final write.
func TestFallbackExerciseMidCopySurvivesTheCopysFinalWrite(t *testing.T) {
	db := dbHome(t)
	dest := "nas:/primary"
	fallbackDest := "nas-fallback:/primary"
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:      dest,
		FallbackDest: fallbackDest,
	}); err != nil {
		t.Fatal(err)
	}

	copyRelease := make(chan struct{})
	copyEntered := make(chan struct{})
	o := &offsiteBackupper{
		logf: func(string, ...interface{}) {},
		copy: func(string, string, int) error {
			close(copyEntered)
			<-copyRelease // stands in for a slow primary copy
			return nil
		},
		probe: func(context.Context, string) (string, error) { return "203.0.113.9", nil },
	}

	copyDone := make(chan struct{})
	go func() {
		o.runSync(db, dest)
		close(copyDone)
	}()
	<-copyEntered // the copy has made its first (in-flight) updateState write

	// The exercise runs to completion entirely inside the copy's window.
	o.exerciseFallbackIfDue(db, dest, fallbackDest)

	mid := store.ReadOffsiteState(db)
	if mid.FallbackLastOKAt.IsZero() {
		t.Fatal("the exercise's own write did not land while the copy was mid-flight")
	}

	close(copyRelease)
	<-copyDone

	final := store.ReadOffsiteState(db)
	if final.FallbackLastOKAt.IsZero() {
		t.Error("FallbackLastOKAt was wiped by the primary copy's final write")
	}
	if final.FallbackVia != "203.0.113.9" {
		t.Errorf("FallbackVia = %q, want the exercise's own reported address preserved", final.FallbackVia)
	}
	if final.FallbackDest != fallbackDest {
		t.Errorf("FallbackDest = %q, want %q preserved", final.FallbackDest, fallbackDest)
	}
	if final.LastOKAt.IsZero() {
		t.Error("the primary copy's own LastOKAt did not land either")
	}
}

// The probe must run under a context whose deadline is strictly tighter
// than store.RemoteTimeout (store/offsite_remote.go's 10 minutes for an
// rsync transfer): the exercise writes and removes one marker file over
// ssh, not a transfer, and a probe bounded no tighter than a full backup
// could stall the exercise loop for as long as a hung rsync would stall the
// backup one.
//
// Read directly off store.RemoteTimeout rather than a literal or a local
// copy of the same duration pinned in this package — see this file's own
// comment on why a second copy of a cross-package constant is exactly what
// went stale on offsiteFallbackDueInterval/store.OffsiteFallbackInterval.
func TestExerciseFallbackProbeContextIsTighterThanRemoteTimeout(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"

	var deadline time.Time
	var hasDeadline bool
	o := &offsiteBackupper{
		logf: func(string, ...interface{}) {},
		probe: func(ctx context.Context, _ string) (string, error) {
			deadline, hasDeadline = ctx.Deadline()
			return "127.0.0.1", nil
		},
	}
	o.exerciseFallbackIfDue(db, dest, fallback)

	if !hasDeadline {
		t.Fatal("probe ran on a context with no deadline at all")
	}
	if d := time.Until(deadline); d >= store.RemoteTimeout {
		t.Errorf("probe context deadline is %s out, want strictly less than remoteTimeout (%s)",
			d, store.RemoteTimeout)
	}
}

// The transition log fires once entering warn, once returning — never once
// per failed attempt, and never on every successful one either.
func TestNoteFallbackTransitionLogsOnlyTheEdges(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	// A healthy primary — see TestExerciseFallbackIfDueStreakReachesWarn's
	// comment for why an unhealthy one would mute the very transition this
	// test observes.
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:      dest,
		LastOKAt:     time.Now(),
		LastTryAt:    time.Now(),
		FallbackDest: fallback,
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var lines []string
	fail := true
	o := &offsiteBackupper{
		logf: func(format string, args ...interface{}) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, format)
		},
		probe: func(context.Context, string) (string, error) {
			if fail {
				return "", errors.New("connection refused")
			}
			return "127.0.0.1", nil
		},
	}

	backdate := func() {
		s := store.ReadOffsiteState(db)
		s.FallbackLastTryAt = time.Now().Add(-offsiteFallbackDueInterval - time.Minute)
		if err := store.WriteOffsiteState(db, s); err != nil {
			t.Fatal(err)
		}
	}

	// Two failures: only the second crosses into warn.
	o.exerciseFallbackIfDue(db, dest, fallback)
	backdate()
	o.exerciseFallbackIfDue(db, dest, fallback)
	// A third failure, still in warn: must not log again.
	backdate()
	o.exerciseFallbackIfDue(db, dest, fallback)

	fail = false
	backdate()
	o.exerciseFallbackIfDue(db, dest, fallback)
	// A second success, still healthy: must not log again.
	backdate()
	o.exerciseFallbackIfDue(db, dest, fallback)

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()

	warnLines, backLines := 0, 0
	for _, l := range got {
		switch l {
		case "daemon: offsite fallback path to %s is not working: %s":
			warnLines++
		case "daemon: offsite fallback path to %s is working again":
			backLines++
		default:
			t.Errorf("unexpected log line format %q", l)
		}
	}
	if warnLines != 1 {
		t.Errorf("logged the warn transition %d times, want exactly 1: %v", warnLines, got)
	}
	if backLines != 1 {
		t.Errorf("logged the recovery transition %d times, want exactly 1: %v", backLines, got)
	}
}

// A fallback that is still failing must never log "working again" merely
// because a degraded primary muted its severity back down to ok. The
// recovery edge depends on the fallback's own raw fact — its failure streak
// reaching zero — never on the muted severity's own edge; see
// noteFallbackTransition's own doc comment for the full scenario
// this reproduces: two failures already logged "not working", the primary
// itself starts genuinely failing (which mutes the fallback's own classified
// severity back to ok on the read alone, per ClassifyOffsiteFallback's
// suppression rule — store.primaryOffsiteNeedsAttention, not primary
// severity alone: see that function's own doc comment for why a merely
// stale, no_attempt, or never_succeeded primary must NOT have the same
// effect, ruling 2 of the final whole-branch review), and a third fallback
// failure must still read as "still not working", not "working again".
//
// Mutation: reverting the recovery branch to the muted severity's own edge
// (`case !nowWarn && wasWarn`, i.e. dropping the `recovered` check and the
// fbWarned update that goes with it) makes this test fail — the failing
// primary mutes nowWarn to false on the third failure and the old
// condition logs a false recovery.
func TestNoteFallbackTransitionDoesNotLogRecoveryWhilePrimaryMutesAGenuineFailure(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:      dest,
		LastOKAt:     time.Now(),
		LastTryAt:    time.Now(),
		FallbackDest: fallback,
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var lines []string
	o := &offsiteBackupper{
		logf: func(format string, args ...interface{}) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, format)
		},
		probe: func(context.Context, string) (string, error) {
			return "", errors.New("connection refused")
		},
	}

	backdateFallback := func() {
		s := store.ReadOffsiteState(db)
		s.FallbackLastTryAt = time.Now().Add(-offsiteFallbackDueInterval - time.Minute)
		if err := store.WriteOffsiteState(db, s); err != nil {
			t.Fatal(err)
		}
	}

	// Two failures: crosses into warn, logged once.
	o.exerciseFallbackIfDue(db, dest, fallback)
	backdateFallback()
	o.exerciseFallbackIfDue(db, dest, fallback)

	// The primary itself starts genuinely failing (not merely stale — a
	// stale primary must NOT mute the fallback, see ruling 2 above), which
	// mutes the fallback's own severity back to ok on the next read.
	s := store.ReadOffsiteState(db)
	s.LastErr = "disk read error"
	if err := store.WriteOffsiteState(db, s); err != nil {
		t.Fatal(err)
	}
	backdateFallback()

	// A third failure, still failing, while the primary now reads failing:
	// must NOT log a recovery.
	o.exerciseFallbackIfDue(db, dest, fallback)

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()

	for _, l := range got {
		if l == "daemon: offsite fallback path to %s is working again" {
			t.Fatalf("logged a false recovery while the fallback was still failing "+
				"and the primary was itself genuinely failing: %v", got)
		}
	}

	final := store.ReadOffsiteState(db)
	if final.FallbackConsecutiveFailures != 3 {
		t.Fatalf("test setup: ConsecutiveFailures = %d, want 3", final.FallbackConsecutiveFailures)
	}
}

// declare() carries the fallback_* fields forward only when both the
// primary destination AND the fallback destination match what was already
// persisted; any other combination starts the exercise's own record fresh.
func TestDeclareCarriesOrResetsFallbackFields(t *testing.T) {
	dest, otherDest := "nas:/primary", "nas:/other-primary"
	fallback, otherFallback := "nas-fallback:/primary", "nas-fallback2:/primary"

	seed := func(t *testing.T, db string) {
		t.Helper()
		if err := store.WriteOffsiteState(db, store.OffsiteState{
			DestDir:                     dest,
			FallbackDest:                fallback,
			FallbackLastOKAt:            time.Now().Add(-time.Hour),
			FallbackConsecutiveFailures: 3,
			FallbackVia:                 "127.0.0.1",
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("same primary and same fallback: carried forward", func(t *testing.T) {
		db := dbHome(t)
		seed(t, db)
		o := &offsiteBackupper{logf: func(string, ...interface{}) {}}
		o.declare(db, dest, fallback)

		got := store.ReadOffsiteState(db)
		if got.FallbackConsecutiveFailures != 3 {
			t.Errorf("ConsecutiveFailures = %d, want 3 carried forward", got.FallbackConsecutiveFailures)
		}
		if got.FallbackVia != "127.0.0.1" {
			t.Errorf("FallbackVia = %q, want carried forward", got.FallbackVia)
		}
	})

	t.Run("same primary, changed fallback: fallback fields reset", func(t *testing.T) {
		db := dbHome(t)
		seed(t, db)
		o := &offsiteBackupper{logf: func(string, ...interface{}) {}}
		o.declare(db, dest, otherFallback)

		got := store.ReadOffsiteState(db)
		if got.FallbackDest != otherFallback {
			t.Fatalf("FallbackDest = %q, want %q", got.FallbackDest, otherFallback)
		}
		if got.FallbackConsecutiveFailures != 0 {
			t.Errorf("ConsecutiveFailures = %d, want 0 reset on a changed fallback dest", got.FallbackConsecutiveFailures)
		}
		if got.FallbackVia != "" {
			t.Errorf("FallbackVia = %q, want reset on a changed fallback dest", got.FallbackVia)
		}
		if !got.FallbackLastOKAt.IsZero() {
			t.Errorf("FallbackLastOKAt = %v, want reset on a changed fallback dest", got.FallbackLastOKAt)
		}
		// The primary's own destination did not change: its relationship
		// (DestDir) must still read as the same one.
		if got.DestDir != dest {
			t.Errorf("DestDir = %q, want %q unaffected by the fallback-only change", got.DestDir, dest)
		}
	})

	t.Run("changed primary: everything resets, including fallback", func(t *testing.T) {
		db := dbHome(t)
		seed(t, db)
		o := &offsiteBackupper{logf: func(string, ...interface{}) {}}
		o.declare(db, otherDest, fallback)

		got := store.ReadOffsiteState(db)
		if got.DestDir != otherDest {
			t.Fatalf("DestDir = %q, want %q", got.DestDir, otherDest)
		}
		if got.FallbackConsecutiveFailures != 0 {
			t.Errorf("ConsecutiveFailures = %d, want 0 reset on a changed primary dest", got.FallbackConsecutiveFailures)
		}
		if got.FallbackVia != "" {
			t.Errorf("FallbackVia = %q, want reset on a changed primary dest", got.FallbackVia)
		}
	})
}

// A malformed or same-as-primary fallback destination is never probed:
// exercising it would prove nothing about a redundant route.
func TestExerciseFallbackIfDueSkipsMisconfiguredDestinations(t *testing.T) {
	db := dbHome(t)
	dest := "nas:/primary"

	called := false
	o := &offsiteBackupper{
		logf:  func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) { called = true; return "", nil },
	}

	o.exerciseFallbackIfDue(db, dest, "")                   // not configured
	o.exerciseFallbackIfDue(db, dest, "/not/a/remote/spec") // does not parse
	o.exerciseFallbackIfDue(db, dest, dest)                 // same as primary

	if called {
		t.Fatal("the probe ran against a misconfigured or unconfigured fallback destination")
	}
}

// The ordering guarantee itself, exercised through a real Server.Run()
// rather than through declare() in isolation: declare() must have already
// persisted the primary destination by the time the fallback exercise's
// probe is ever invoked for the first time. This is server.go's own call
// order (declare(), then the two "go" statements) — nothing exercised
// elsewhere in this package's unit tests would catch a regression that
// reordered those three statements in Run() itself.
//
// It is the daemon's own version of the offsite-visibility incident
// offsite.go's declare() doc comment describes: a destination the daemon
// never wrote down first reads back as "nothing configured anywhere". Here
// the second writer is exerciseFallbackIfDue rather than attempt(), but the
// failure mode — and the fix — are the same.
//
// Mutation: move the declare() switch in server.go's Run() to after the
// "go s.runOffsiteFallback(ctx)" statement, and this test goes red — the
// probe fires against a state file whose DestDir is still empty. See
// task-8-report.md for the verified red run.
func TestRunDeclaresBeforeTheFallbackExerciseEverProbes(t *testing.T) {
	destDir := t.TempDir()
	fallbackDest := "fallback-host:/somewhere"

	st := openTestStore(t)
	cfg := Config{
		SocketPath:            testSocketPath(t),
		HTTPAddr:              "",
		DBOffsiteDir:          destDir,
		DBOffsiteFallbackDest: fallbackDest,
	}
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Injected before Run() starts anything: exerciseFallbackIfDue's own
	// immediate pass (runOffsiteFallback's "one pass before the ticker"
	// call) reaches this on the very first invocation, with no need to
	// wait for the 10-minute tick.
	probed := make(chan bool, 4)
	srv.offsite.probe = func(context.Context, string) (string, error) {
		state := store.ReadOffsiteState(st.Path())
		probed <- state.DestDir == destDir
		return "", nil
	}

	// destDir and the store's own directory are both t.TempDir()s, which the
	// testing framework starts removing the moment this function returns —
	// and the daemon writes into both. startDaemon (rundaemon_test.go) is
	// what makes returning safe: it cancels and then blocks until Run has
	// returned, which since Server.drainWorkers (server.go) means every
	// goroutine Run started has stopped too, the offsite copy included.
	// Deferred last, so it runs first (LIFO), ahead of the t.TempDir
	// cleanups registered above it.
	defer startDaemon(t, srv)()

	select {
	case ok := <-probed:
		if !ok {
			t.Fatal("the fallback exercise's own probe ran against a state file with no primary " +
				"destination on record: declare() had not run yet")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fallback exercise never probed at all")
	}
}

// The likeliest misconfiguration a person will actually make: writing the
// bare SSH alias — what -db-offsite-fallback-dest's own help text calls a
// "pinned alias" — instead of the full [user@]host:/path shape
// ParseRemoteDest actually requires. Before this fix Run() said nothing
// about it, ever, at any startup: the symmetric case (a fallback dest with
// no primary one) already logs "db-offsite-fallback-dest is set but
// db-offsite-dir is not" (verified by grep: server.go's own Run(), the
// switch case right above this one), this one did not.
//
// badFallback is exactly internal/setup/launchd_test.go's own
// -db-offsite-fallback-dest fixture (verified by grep for "backup-ssh"
// in that file): a bare alias with no path, which is precisely the shape
// that goes silent in a real service file.
func TestRunLogsWhenFallbackDestDoesNotParse(t *testing.T) {
	destDir := t.TempDir()
	badFallback := "backup-ssh"

	st := openTestStore(t)
	cfg := Config{
		SocketPath:            testSocketPath(t),
		HTTPAddr:              "",
		DBOffsiteDir:          destDir,
		DBOffsiteFallbackDest: badFallback,
	}
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// This test is about the startup log line, not the exercise itself —
	// and badFallback would never be probed anyway, since it does not
	// parse.
	srv.offsite.probe = func(context.Context, string) (string, error) { return "", nil }

	var buf syncBuf
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Same shutdown-ordering rationale as
	// TestRunDeclaresBeforeTheFallbackExerciseEverProbes above, and the
	// reason this test in particular is the one that failed: it waits on a
	// log line Run emits *before* it ever reaches `go s.runDBBackups(ctx)`,
	// so it could return with that goroutine not yet even started — which
	// is how its own store directory got a p.db.0 written into it after
	// RemoveAll had emptied it. Registered after the log.SetOutput restore
	// above, so it runs first (LIFO): keep capturing into buf until the
	// daemon has actually stopped, only then hand stderr back.
	defer startDaemon(t, srv)()

	waitFor(t, func() bool {
		return strings.Contains(buf.String(), "db-offsite-fallback-dest")
	})

	got := buf.String()
	if !strings.Contains(got, `db-offsite-fallback-dest "backup-ssh" does not parse`) {
		t.Errorf("Run() did not log the misconfigured fallback destination at startup: %q", got)
	}
}
