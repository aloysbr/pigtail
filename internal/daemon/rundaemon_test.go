package daemon

import (
	"context"
	"testing"
	"time"
)

// daemonStopTimeout is how long startDaemon's stop waits for Run to return
// after cancellation before calling it a failure. Generous on purpose:
// Server.Run's own drain is bounded at workerDrainTimeout (5s, server.go),
// so anything past that is Run not honouring its own bound — a bug in the
// daemon, which is worth a red test, and not a slow machine, which is not.
const daemonStopTimeout = 15 * time.Second

// startDaemon runs srv.Run on its own goroutine and returns the stop every
// test that starts a real daemon needs — and that most of them used to do
// with a bare `defer cancel()`, which is the bug this helper exists to make
// unrepeatable.
//
// cancel alone only *asks* the daemon to stop. It does not wait for it to
// have stopped, and a test function that returns on that promise is racing
// the daemon's own writes: t.TempDir's cleanup starts removing the store's
// directory the moment the test returns, while runDBBackups' first, immediate
// backupDBOnce (backup.go) can still be mid-VACUUM-INTO into that same
// directory. That is measured, not hypothetical — 4 failures in 60 runs of
// TestRunLogsWhenFallbackDestDoesNotParse, each leaving a p.db.0 behind in
// the directory RemoveAll had just emptied ("directory not empty"), or the
// SQLITE_IOERR_DELETE_NOENT the vacuum returns when the directory is pulled
// out from under it mid-write ("disk I/O error (5898)").
//
// Waiting for Run to return is now sufficient, which it was not before:
// Run joins every goroutine it starts, the offsite copy included, before
// returning (see Server.drainWorkers, server.go). So this helper needs no
// per-goroutine knowledge — no waitFor on inFlight/fbInFlight, nothing to
// extend the day a new background loop is added — which is exactly the
// property worth having in one place rather than restated at each call site.
//
// stop is returned rather than registered with t.Cleanup so the call site
// keeps control of the ordering: a test holding its own `defer st.Close()`
// wants the daemon stopped first, and only a defer registered after that one
// gets that.
func startDaemon(t *testing.T, srv *Server) (stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(ctx)
	}()

	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(daemonStopTimeout):
			t.Errorf("Run did not return within %s of cancellation", daemonStopTimeout)
		}
	}
}
