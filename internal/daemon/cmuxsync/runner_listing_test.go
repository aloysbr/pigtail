package cmuxsync

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func countPrefix(calls []string, p string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, p) {
			n++
		}
	}
	return n
}

func hasWarning(ws []string, p string) bool {
	for _, w := range ws {
		if strings.HasPrefix(w, p) {
			return true
		}
	}
	return false
}

// D1·A: the listings fail (cmux killed at the bound under load); a session
// that starts waiting gets its pill from the last good listing, the loop
// stays "available", and the listings are retried on a backoff, not on
// every 1 s pass. Red on 9b4158c: the failed slow pass paused every write.
func TestRunnerWritesPillsFromCachedListingWhileListingsFail(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "busy", true)
	h.step(0)
	h.world.listDown = errors.New("cmux list-windows: signal: killed")
	h.step(30 * time.Second) // slow pass due: the listing fails
	h.setStatus("s1", wsA, "waiting")
	h.world.calls = nil
	h.step(time.Second)
	if v := h.world.status[wsA][KeyAttention].Value; v != "Waiting for you" {
		t.Fatalf("pill not written from the cached listing: %q (calls %q)", v, h.world.calls)
	}
	st := h.r.State()
	if !st.CmuxAvailable || !hasWarning(st.Warnings, "cmux listings failing since ") {
		t.Fatalf("state %+v", st)
	}
	h.world.calls = nil
	for i := 0; i < 3; i++ {
		h.step(time.Second) // inside the 5 s listing backoff
	}
	if n := countPrefix(h.world.calls, "list-windows"); n != 0 {
		t.Fatalf("listing retried on every pass: %q", h.world.calls)
	}
	h.world.listDown = nil
	h.step(5 * time.Second)
	if st := h.r.State(); hasWarning(st.Warnings, "cmux listings failing since ") {
		t.Fatalf("warning left after recovery: %+v", st.Warnings)
	}
}

// D1·A: the `cmux sessions` snapshot failing (30 of 55 pauses on
// 2026-10-04) never pauses the pills. Red on 9b4158c.
func TestRunnerPeersSnapshotFailureDoesNotPause(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "waiting", true)
	h.peersErr = errors.New("cmux sessions: signal: killed")
	h.step(0)
	if v := h.world.status[wsA][KeyAttention].Value; v != "Waiting for you" {
		t.Fatalf("pill %q, calls %q", v, h.world.calls)
	}
	if !h.r.State().CmuxAvailable {
		t.Fatal("a failed snapshot marked cmux unreachable")
	}
}

// D1·A: a failing reconcile (list-status) keeps the in-memory record and
// writes on. Red on 9b4158c.
func TestRunnerReconcileFailureKeepsWriting(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "waiting", true)
	h.world.statusDown = errors.New("cmux list-status: signal: killed")
	h.step(0)
	if v := h.world.status[wsA][KeyAttention].Value; v != "Waiting for you" {
		t.Fatalf("pill %q, calls %q", v, h.world.calls)
	}
	h.world.statusDown = nil
	h.world.calls = nil
	h.step(5 * time.Second) // listing retry due: listings and reconcile run again
	if n := countPrefix(h.world.calls, "list-status"); n != 1 {
		t.Fatalf("reconcile not retried after recovery: %q", h.world.calls)
	}
}

// Degraded mode writes pills only: a title waits for a fresh listing (a
// stale listing cannot tell a manual rename). Red on 9b4158c (nothing at
// all was written while listings failed, so the pill assertion fails).
func TestRunnerDegradedModeWritesNoText(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "busy", true)
	h.step(0)
	h.world.listDown = errors.New("timeout")
	h.step(30 * time.Second)
	h.bind(wsA)
	h.setStatus("s1", wsA, "waiting")
	h.world.calls = nil
	h.step(time.Second)
	if countPrefix(h.world.calls, "rename") != 0 || countPrefix(h.world.calls, "set-description") != 0 {
		t.Fatalf("text written from a stale listing: %q", h.world.calls)
	}
	if v := h.world.status[wsA][KeyAttention].Value; v != "Waiting for you" {
		t.Fatalf("pill %q", v)
	}
	h.world.listDown = nil
	h.step(5 * time.Second)
	if countPrefix(h.world.calls, "rename") != 1 {
		t.Fatalf("title not written once listings answer: %q", h.world.calls)
	}
}

// Guard (green on the base too): past ListingMaxAge the cache is no longer
// trusted and the loop pauses, as today.
func TestRunnerListingCacheExpires(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "busy", true)
	h.step(0)
	h.world.listDown = errors.New("timeout")
	for i := 0; i < 11; i++ {
		h.step(time.Minute)
	}
	if st := h.r.State(); st.CmuxAvailable {
		t.Fatalf("still writing from a %v-old listing: %+v", 11*time.Minute, st)
	}
	h.setStatus("s1", wsA, "waiting")
	h.step(time.Second)
	if v := h.world.status[wsA][KeyAttention].Value; v != "" {
		t.Fatalf("pill written past the cache bound: %q", v)
	}
	// The pause is left as soon as a listing answers again.
	h.world.listDown = nil
	h.step(time.Minute)
	if v := h.world.status[wsA][KeyAttention].Value; v != "Waiting for you" {
		t.Fatalf("stuck in the pause after listings recovered: %q (calls %q)", v, h.world.calls)
	}
}

// Review focus 2: in degraded mode a write that fails (workspace closed,
// or cmux fully down) pauses as today. Guard.
func TestRunnerDegradedWriteFailurePauses(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "busy", true)
	h.step(0)
	h.world.down = errors.New("cmux: socket not found") // every call, writes included
	h.setStatus("s1", wsA, "waiting")
	h.step(30 * time.Second)
	if st := h.r.State(); st.CmuxAvailable {
		t.Fatalf("a failed write left cmux available: %+v", st)
	}
	h.world.down = nil
	h.step(6 * time.Second)
	if v := h.world.status[wsA][KeyAttention].Value; v != "Waiting for you" {
		t.Fatalf("no recovery: %q", v)
	}
}

// Review focus 1: a workspace closed while the listings fail. Its session
// starts waiting, the write from the cached listing fails: the loop pauses
// (as today), then resumes at the next good listing, which no longer shows
// the workspace. A guard of the new path: green on 9b4158c too (there the
// failing listing paused the loop before any write).
func TestRunnerDegradedClosedWorkspacePauses(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.session("sa", wsA, "busy", true)
	h.session("sb", wsB, "busy", true)
	h.step(0)
	h.world.listDown = errors.New("timeout")
	h.step(30 * time.Second) // degraded from here
	h.world.close(wsB)
	h.setStatus("sb", wsB, "waiting")
	h.step(time.Second)
	if st := h.r.State(); st.CmuxAvailable {
		t.Fatalf("a write to a closed workspace left cmux available: %+v", st)
	}
	h.world.listDown = nil
	h.setStatus("sa", wsA, "waiting")
	h.step(6 * time.Second)
	if st := h.r.State(); !st.CmuxAvailable {
		t.Fatalf("no recovery at the next good listing: %+v", st)
	}
	if v := h.world.status[wsA][KeyAttention].Value; v != "Waiting for you" {
		t.Fatalf("open workspace not written after recovery: %q (calls %q)", v, h.world.calls)
	}
}

// Review focus 2: once writes are paused, a degraded pass that writes
// nothing proves nothing — the loop must stay unavailable, so the D2 popover
// line does not go away while cmux may still refuse writes. The first
// assertion is a guard (green on 9b4158c, red on this plan's round-1
// design). The second is red on 9b4158c: there the failing listing pauses
// again at t=47 (retry at 67) and the t=48 pass writes nothing.
func TestRunnerDegradedPassWithoutWriteKeepsThePause(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "busy", true)
	h.step(0)
	h.world.down = errors.New("cmux: socket not found")
	h.setStatus("s1", wsA, "waiting")
	h.step(30 * time.Second) // listing fails, then the write fails: paused
	// Back to busy while cmux is still down: the damper holds "waiting" for
	// WaitingHold, so that pass still tries (and fails) to write it.
	h.setStatus("s1", wsA, "busy")
	h.step(6 * time.Second)
	h.world.down = nil
	h.world.listDown = errors.New("timeout") // writes would work, listings still fail
	h.step(11 * time.Second)                 // retry due, hold over: nothing left to write
	if st := h.r.State(); st.CmuxAvailable {
		t.Fatalf("a pass that wrote nothing declared cmux reachable: %+v", st)
	}
	h.setStatus("s1", wsA, "waiting")
	h.step(time.Second)
	if st := h.r.State(); !st.CmuxAvailable {
		t.Fatalf("a write that went through must lift the pause: %+v", st)
	}
}

// Review finding: listings answer but the reconcile (list-status, 3 s
// bound) keeps failing. The degraded mode must keep its backoff and its
// start time instead of being left and re-entered on every retry (a full
// listing plus one list-status per workspace every 5 s against a saturated
// cmux, and a "since" that always reads the last 5 s).
func TestRunnerFailingReconcileKeepsTheListingBackoff(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "waiting", true) // a pill write goes through: cmux takes writes
	h.world.statusDown = errors.New("cmux list-status: signal: killed")
	h.step(0) // listing fine, reconcile fails: degraded from t0
	since := "cmux listings failing since " + h.now.Local().Format("15:04")
	h.world.calls = nil
	for i := 0; i < 20; i++ {
		h.step(time.Second)
	}
	if n := countPrefix(h.world.calls, "list-windows"); n > 2 {
		t.Fatalf("listing retried %d times in 20 s, want the 5 s, 10 s… backoff (≤ 2): %q", n, h.world.calls)
	}
	h.step(2 * time.Minute)
	if st := h.r.State(); !hasWarning(st.Warnings, since) {
		t.Fatalf("degraded-mode start moved: %+v", st.Warnings)
	}
}

// Verify round 1 of #176: a listing that hangs up to its 10 s bound must
// start its backoff when the attempt ends, not when the pass began — else
// the 5 s and 10 s backoffs are spent inside the hung call and the listing
// is retried on the very next pass (measured: list-windows every 10 s, loop
// held ~30 s). Red on 7377fd6.
func TestRunnerListingBackoffStartsAfterTheAttempt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "busy", true)
	h.step(0)
	h.world.listDown = errors.New("cmux list-windows: signal: killed")
	h.world.onList = func() { h.now = h.now.Add(10 * time.Second) } // killed at the 10 s bound
	h.step(30 * time.Second)                                        // slow pass: the listing hangs 10 s, then fails
	h.world.calls = nil
	h.step(time.Second)
	if n := countPrefix(h.world.calls, "list-windows"); n != 0 {
		t.Fatalf("hung listing retried on the next pass, its backoff spent inside the call: %q", h.world.calls)
	}
}
