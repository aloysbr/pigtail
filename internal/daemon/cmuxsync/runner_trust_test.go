package cmuxsync

import (
	"testing"
	"time"
)

// D14 / A2 / A29: a spawn stopped on "trust this folder" shows a red
// "Blocked · trust folder" on its workspace, still there after two slow
// passes, and gone once its session registers (native file read).
func TestRunnerTrustPendingLifecycle(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	pidAlive := true
	h.r.deps.PidAlive = func(int) bool { return pidAlive }
	h.world.add(winA, wsA, "fresh")
	h.r.TrustPending(wsA, 5000)
	h.step(0)
	e := h.world.status[wsA][KeyAttention]
	if e.Value != "Blocked · trust folder" || e.Icon != "hand.raised.fill" || e.Color != "#FF3B30" || e.Priority != 90 {
		t.Fatalf("pill = %+v", e)
	}
	h.step(35 * time.Second) // two slow passes later (A2)
	if p := h.pill(); p != "Blocked · trust folder" {
		t.Fatalf("after 35 s: %q", p)
	}
	h.native("s1", wsA, "busy", h.now) // trust accepted: the session registers
	h.step(time.Second)
	h.step(WaitingHold + time.Second) // A29: gone within 3 s
	if p := h.pill(); p != "" {
		t.Fatalf("after registration: %q", p)
	}
}

// Pane closed without accepting: its Claude is gone, so is the pill.
func TestRunnerTrustPendingDiesWithItsClaude(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	pidAlive := true
	h.r.deps.PidAlive = func(int) bool { return pidAlive }
	h.world.add(winA, wsA, "fresh")
	h.r.TrustPending(wsA, 5000)
	h.step(0)
	pidAlive = false
	h.step(time.Second)
	h.step(WaitingHold + time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("pane closed: %q", p)
	}
}

// Review Focus 4: a listing that is not later than the registration does
// not prove the workspace closed. Red without the listedAt.After(since)
// guard: the pass right after the registration lists the windows at the same
// instant, before cmux shows the new workspace, and would drop the spawn.
func TestRunnerTrustPendingSurvivesUntilListed(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	h.step(0)                   // loop running, wsA not created yet
	h.r.TrustPending(wsA, 5000) // since = h.now
	h.step(0)                   // nudged slow pass at the same instant: wsA not listed, listing not after since → kept
	h.world.add(winA, wsA, "fresh")
	h.step(31 * time.Second) // next slow pass lists wsA
	if p := h.pill(); p != "Blocked · trust folder" {
		t.Fatalf("pill = %q (dropped before its workspace was listed)", p)
	}
}

// Switching the loop off forgets the pending spawns: switched on again, no
// pill comes back without a new registration.
func TestRunnerSwitchOffForgetsTrustPending(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "fresh")
	h.r.TrustPending(wsA, 5000)
	h.step(0)
	if p := h.pill(); p != "Blocked · trust folder" {
		t.Fatalf("pill = %q", p)
	}
	h.r.SetEnabled(false)
	h.step(time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("switched off: pill kept %q", p)
	}
	h.r.SetEnabled(true)
	h.step(time.Second)
	h.step(time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("switched on again: pending spawn came back %q", p)
	}
}
