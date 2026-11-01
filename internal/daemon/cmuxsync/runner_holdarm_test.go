package cmuxsync

import (
	"testing"
	"time"
)

// A hold end that passed while a pass (or a wake) ran must wake the loop at
// once, not at the next tick.
func TestHoldArmPastDeadlineWakesAtOnce(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	var a holdArm
	if _, ok := a.next(time.Time{}, now); ok {
		t.Fatal("armed with no hold running")
	}
	if w, ok := a.next(now.Add(time.Second), now); !ok || w != time.Second {
		t.Fatalf("future deadline: wait=%s ok=%v, want 1s true", w, ok)
	}
	past := now.Add(-50 * time.Millisecond)
	if w, ok := a.next(past, now); !ok || w != 0 {
		t.Fatalf("deadline passed during a pass: wait=%s ok=%v, want an immediate wake (0, true), else it is lost until the next tick", w, ok)
	}
}

// The immediate wake is not a hot loop: once woken for a deadline that the
// wake (skipped, or the damper not releasing yet) left standing, it is not
// armed again for that same deadline; a new deadline arms again.
func TestHoldArmNoHotLoopOnSameDeadline(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	var a holdArm
	past := now.Add(-time.Millisecond)
	wakes := 0
	for i := 0; i < 1000; i++ { // 1000 loop turns, the deadline never clears
		if _, ok := a.next(past, now); ok {
			wakes++
			a.fired(past)
		}
	}
	if wakes != 1 {
		t.Fatalf("wakes = %d for one standing deadline, want 1", wakes)
	}
	if _, ok := a.next(now.Add(-time.Microsecond), now); !ok {
		t.Fatal("a new passed deadline must wake again")
	}
}
