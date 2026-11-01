// Package testutil holds the helpers shared by the tests of the module.
//
// Eventually waits for an event; SerialOnly keeps a test that touches
// process-wide state out of t.Parallel. Neither guesses a duration: a test
// that sleeps a fixed delay then asserts goes red under load.
package testutil

import (
	"fmt"
	"testing"
	"time"
)

// Eventually polls cond until it returns true, and fails the test with
// t.Fatalf when it is still false after within.
//
// The deadline is wide on purpose (callers pass 30 s by default): it waits
// for an event (a goroutine registered, a file written, a process gone), and
// only a broken test reaches it. Never use it to wait a guessed duration.
//
// It polls every 10 ms at first, then slower, up to every 100 ms. Call it
// from the test's own goroutine: t.Fatalf may not run on another one.
func Eventually(t testing.TB, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	interval := 10 * time.Millisecond
	for {
		if cond() {
			return
		}
		left := time.Until(deadline)
		if left <= 0 {
			t.Fatalf("%s: not true after %s", what, within)
			return
		}
		time.Sleep(min(interval, left))
		interval = min(interval*3/2, 100*time.Millisecond)
	}
}

// SerialOnly panics when t (or one of its parents) runs in parallel: it sets
// an environment variable through t.Setenv, which Go refuses in a parallel
// test. Call it in every helper that touches process state — a package
// variable, os.Stdout/os.Stderr/os.Stdin, the scope table — so that a
// t.Parallel added later fails at once instead of racing.
func SerialOnly(t testing.TB) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			panic(fmt.Sprintf("testutil.SerialOnly: %s touches process state (package var, os.Std*, scope table); it must not run in parallel (%v)", t.Name(), r))
		}
	}()
	t.Setenv("PIGTAIL_TEST_SERIAL_ONLY", t.Name())
}
