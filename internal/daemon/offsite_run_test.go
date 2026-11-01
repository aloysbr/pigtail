package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// RunOffsiteNow starts once, reads false while that copy is still in
// flight, and reads true again once it has finished — the exact shape
// task-8-brief.md's "Tests" section asks for.
func TestRunOffsiteNowReportsInFlight(t *testing.T) {
	db, dest := dbHome(t), t.TempDir()
	release := make(chan struct{})
	entered := make(chan struct{})
	var enterOnce sync.Once
	o := &offsiteBackupper{
		logf: func(string, ...interface{}) {},
		copy: func(string, string, int) error {
			enterOnce.Do(func() { close(entered) })
			// release is closed once, below, and stays closed: the third
			// call's own copy invocation reads from it too, and a closed
			// channel always receives immediately.
			<-release
			return nil
		},
	}

	if !o.RunOffsiteNow(db, dest) {
		t.Fatal("first call: RunOffsiteNow = false, want true (nothing in flight yet)")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the copy never started")
	}

	if o.RunOffsiteNow(db, dest) {
		t.Fatal("second call while the first copy is still in flight: RunOffsiteNow = true, want false")
	}

	close(release)
	waitFor(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return !o.inFlight
	})

	if !o.RunOffsiteNow(db, dest) {
		t.Fatal("after the first copy finished: RunOffsiteNow = false, want true again")
	}
	// Let the third call's own background goroutine finish before this test
	// returns and t.TempDir() starts removing db's directory out from under
	// it — same ordering rationale as offsite_fallback_test.go's own
	// TestRunDeclaresBeforeTheFallbackExerciseEverProbes.
	waitFor(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return !o.inFlight
	})
}

// RunOffsiteFallbackNow's own in-flight guard, fbInFlight — the field
// deliberately distinct from RunOffsiteNow's inFlight above. Same shape as
// TestRunOffsiteNowReportsInFlight, on the fallback route.
func TestRunOffsiteFallbackNowReportsInFlight(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	if err := store.WriteOffsiteState(db, store.OffsiteState{DestDir: dest, FallbackDest: fallback}); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	entered := make(chan struct{})
	var enterOnce sync.Once
	o := &offsiteBackupper{
		logf: func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) {
			enterOnce.Do(func() { close(entered) })
			<-release
			return "127.0.0.1", nil
		},
	}

	started, configured := o.RunOffsiteFallbackNow(db, dest, fallback)
	if !started || !configured {
		t.Fatalf("first call: started=%v configured=%v, want true,true", started, configured)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the probe never started")
	}

	if started2, _ := o.RunOffsiteFallbackNow(db, dest, fallback); started2 {
		t.Fatal("second call while the first exercise is still in flight: started = true, want false")
	}

	close(release)
	waitFor(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return !o.fbInFlight
	})

	if started3, _ := o.RunOffsiteFallbackNow(db, dest, fallback); !started3 {
		t.Fatal("after the exercise finished: started = false, want true again")
	}
	waitFor(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return !o.fbInFlight
	})
}

// The behaviour task 8 exists to add: RunOffsiteFallbackNow ignores the 6h
// due-check exerciseFallbackIfDue applies — a last try seconds ago would
// make the scheduled path skip it entirely (see
// TestExerciseFallbackIfDueOnlyProbesOnceUntilDeadlinePasses), but a human
// pressing the button gets the probe anyway.
func TestRunOffsiteFallbackNowIgnoresTheDeadline(t *testing.T) {
	db := dbHome(t)
	dest, fallback := "nas:/primary", "nas-fallback:/primary"
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:           dest,
		FallbackDest:      fallback,
		FallbackLastTryAt: time.Now(), // just tried: the scheduled path would skip this
	}); err != nil {
		t.Fatal(err)
	}

	probed := make(chan struct{})
	o := &offsiteBackupper{
		logf:  func(string, ...interface{}) {},
		probe: func(context.Context, string) (string, error) { close(probed); return "127.0.0.1", nil },
	}

	started, configured := o.RunOffsiteFallbackNow(db, dest, fallback)
	if !started || !configured {
		t.Fatalf("started=%v configured=%v, want true,true even though the last try was seconds ago", started, configured)
	}
	select {
	case <-probed:
	case <-time.After(2 * time.Second):
		t.Fatal("the probe never ran: RunOffsiteFallbackNow must ignore the 6h due-check")
	}
	// The exercise runs in its own goroutine (see RunOffsiteFallbackNow's own
	// doc comment) and keeps writing to db's directory after probed closes;
	// wait for it to finish before this test returns and t.TempDir()'s own
	// cleanup starts removing that directory out from under it — same
	// ordering rationale as offsite_fallback_test.go's own
	// TestRunDeclaresBeforeTheFallbackExerciseEverProbes.
	waitFor(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return !o.fbInFlight
	})
}

// The other half of the same design decision, and — per task-8-brief.md's
// rule C — a deliberately separate test from
// TestRunOffsiteFallbackNowIgnoresTheDeadline above, since it is a second,
// independent behaviour: bypassing the deadline must never bypass the three
// correction guards fallbackGuardsOK applies. Each subtest is its own named
// mutation target.
func TestRunOffsiteFallbackNowStillAppliesCorrectionGuards(t *testing.T) {
	const primary = "nas:/primary"

	t.Run("empty fallback destination", func(t *testing.T) {
		db := dbHome(t)
		called := false
		o := &offsiteBackupper{
			logf:  func(string, ...interface{}) {},
			probe: func(context.Context, string) (string, error) { called = true; return "127.0.0.1", nil },
		}
		started, configured := o.RunOffsiteFallbackNow(db, primary, "")
		if started {
			t.Error("started = true, want false: no fallback destination configured")
		}
		if configured {
			t.Error("configured = true, want false: an empty destination is not configured")
		}
		if called {
			t.Error("the probe ran against an unconfigured destination")
		}
	})

	t.Run("malformed alias", func(t *testing.T) {
		db := dbHome(t)
		called := false
		o := &offsiteBackupper{
			logf:  func(string, ...interface{}) {},
			probe: func(context.Context, string) (string, error) { called = true; return "127.0.0.1", nil },
		}
		started, configured := o.RunOffsiteFallbackNow(db, primary, "/not/a/remote/spec")
		if started {
			t.Error("started = true, want false: the alias does not parse")
		}
		if !configured {
			t.Error("configured = false, want true: a (malformed) destination string is still set")
		}
		if called {
			t.Error("the probe ran against a malformed alias")
		}
	})

	t.Run("same as primary", func(t *testing.T) {
		db := dbHome(t)
		called := false
		o := &offsiteBackupper{
			logf:  func(string, ...interface{}) {},
			probe: func(context.Context, string) (string, error) { called = true; return "127.0.0.1", nil },
		}
		started, configured := o.RunOffsiteFallbackNow(db, primary, primary)
		if started {
			t.Error("started = true, want false: the fallback names the primary destination")
		}
		if !configured {
			t.Error("configured = false, want true")
		}
		if called {
			t.Error("the probe ran against a destination equal to the primary")
		}
	})

	t.Run("no file-backed state", func(t *testing.T) {
		called := false
		o := &offsiteBackupper{
			logf:  func(string, ...interface{}) {},
			probe: func(context.Context, string) (string, error) { called = true; return "127.0.0.1", nil },
		}
		started, configured := o.RunOffsiteFallbackNow(":memory:", primary, "nas-fallback:/primary")
		if started {
			t.Error("started = true, want false: no state path to persist to")
		}
		if !configured {
			t.Error("configured = false, want true: a fallback destination is set, even with nowhere to record it")
		}
		if called {
			t.Error("the probe ran with no state file to record the outcome in")
		}
	})
}

// exerciseFallbackIfDue itself must be unaffected by this task's refactor:
// a due exercise still runs, an exercise that fails the guards still does
// not, on the same model as before task 8 touched this file at all. This is
// covered in full by the pre-existing tests in offsite_fallback_test.go
// (TestExerciseFallbackIfDueSkipsMisconfiguredDestinations,
// TestExerciseFallbackIfDueOnlyProbesOnceUntilDeadlinePasses, etc.), which
// this task must leave passing unmodified — see task-8-report.md for the
// full suite run confirming that.

// The guard the whole handler layer depends on and the one explicitly
// called out as "the concurrency test" in task-8-brief.md, run under
// -race -count=12 (see task-8-report.md for the pasted raw output): POST
// /backup/offsite/run must respond before either route's own I/O — the
// primary copy and the fallback probe — has completed. Exercised end to end
// through a real *Server and its real router (buildRouter), not a fake, so
// the mutation this test is meant to catch is a real one: replacing
// RunOffsiteNow's/RunOffsiteFallbackNow's async dispatch
// (internal/daemon/offsite_run.go) with a synchronous call would make this
// test hang until its own 2s timeout.
func TestOffsiteRunHandlerRespondsWithoutWaitingForTheCopy(t *testing.T) {
	destDir := t.TempDir()
	fallbackDest := "nas-fallback:/primary"

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

	copyRelease := make(chan struct{})
	copyEntered := make(chan struct{})
	probeRelease := make(chan struct{})
	probeEntered := make(chan struct{})
	srv.offsite.copy = func(string, string, int) error {
		close(copyEntered)
		<-copyRelease
		return nil
	}
	srv.offsite.probe = func(context.Context, string) (string, error) {
		close(probeEntered)
		<-probeRelease
		return "127.0.0.1", nil
	}

	router := srv.buildRouter()
	req := httptest.NewRequest("POST", "/backup/offsite/run", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(rec, req)
		close(done)
	}()

	// Neither copyRelease nor probeRelease has been closed yet, so if the
	// handler waited on either route's completion before responding, done
	// would never close and this select would time out.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never responded — it appears to be waiting on the copy or the fallback exercise")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// The response is back; now confirm both routes were actually launched
	// in the background rather than skipped outright.
	select {
	case <-copyEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("the primary copy never started in the background")
	}
	select {
	case <-probeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("the fallback exercise never started in the background")
	}

	close(copyRelease)
	close(probeRelease)
	waitFor(t, func() bool {
		srv.offsite.mu.Lock()
		defer srv.offsite.mu.Unlock()
		return !srv.offsite.inFlight && !srv.offsite.fbInFlight
	})
}

// The 400 path: no primary destination configured at all is INVALID_REQUEST,
// exercised the same way TestOffsiteRunHandlerRespondsWithoutWaitingForTheCopy
// is, through the real router, so this pins the actual route registration
// (server.go's r.Post("/backup/offsite/run", ...)) rather than only the
// handler function in isolation (already covered in
// internal/daemon/handlers/offsite_run_test.go).
func TestOffsiteRunHandlerNoDestinationConfiguredIs400(t *testing.T) {
	st := openTestStore(t)
	cfg := Config{SocketPath: testSocketPath(t), HTTPAddr: ""}
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	router := srv.buildRouter()
	req := httptest.NewRequest("POST", "/backup/offsite/run", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: body=%s", rec.Code, rec.Body.String())
	}
}
