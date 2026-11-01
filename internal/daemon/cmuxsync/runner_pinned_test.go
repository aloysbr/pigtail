package cmuxsync

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPinnedWorkspacesFromLastPass(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a").Pinned = true
	h.world.add(winA, wsB, "b")
	h.step(0)
	got := h.r.PinnedWorkspaces()
	if len(got) != 1 || !got[wsA] {
		t.Fatalf("pinned = %v, want only %s", got, wsA)
	}
	got[wsB] = true // a copy: the runner's own set is untouched
	if h.r.PinnedWorkspaces()[wsB] {
		t.Fatal("PinnedWorkspaces must return a copy")
	}
}

func TestPinnedWorkspacesNilBeforeFirstPass(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	if got := h.r.PinnedWorkspaces(); got != nil {
		t.Fatalf("pinned = %v, want nil", got)
	}
	h2 := newHarness(t, Config{})
	h2.world.add(winA, wsA, "a").Pinned = true
	h2.step(0)
	if got := h2.r.PinnedWorkspaces(); got != nil {
		t.Fatalf("a loop that never ran: pinned = %v, want nil", got)
	}
}

// The accessor adds no cmux call: the same scenario, read or not, makes the
// same calls (CLAUDE.md: pigtaild executes cmux in four places, none of them
// this accessor).
func TestPinnedWorkspacesNoExtraCmuxCall(t *testing.T) {
	t.Parallel()
	run := func(read bool) []string {
		h := newHarness(t, on())
		h.world.add(winA, wsA, "a").Pinned = true
		h.session("s1", wsA, "waiting", true)
		for i := 0; i < 3; i++ {
			h.step(time.Duration(i) * 20 * time.Second)
			if read {
				for j := 0; j < 10; j++ {
					h.r.PinnedWorkspaces()
				}
			}
		}
		return h.world.calls
	}
	without, with := run(false), run(true)
	if strings.Join(without, "|") != strings.Join(with, "|") {
		t.Fatalf("the accessor changed the cmux calls:\n%q\n%q", without, with)
	}
}

func TestPinnedWorkspacesClearedOnSwitchOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a").Pinned = true
	h.step(0)
	if h.r.PinnedWorkspaces() == nil {
		t.Fatal("setup: pinned set expected")
	}
	h.r.SetEnabled(false)
	h.step(time.Second)
	if got := h.r.PinnedWorkspaces(); got != nil {
		t.Fatalf("pinned = %v after switch-off, want nil", got)
	}
}

// A pinned group's members are plain rows (pinned=false) in the listing the
// loop reads: they do not count (decision D9).
func TestPinnedWorkspacesIgnoresPinnedGroupMembers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "member of a pinned group")
	h.step(0)
	if got := h.r.PinnedWorkspaces(); len(got) != 0 {
		t.Fatalf("pinned = %v, want none", got)
	}
}

func TestPinnedWorkspacesIgnoresDisallowedWindows(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Config{Enabled: true, Windows: []string{winB}})
	h.world.add(winA, wsA, "a").Pinned = true
	h.world.add(winB, wsB, "b").Pinned = true
	h.step(0)
	got := h.r.PinnedWorkspaces()
	if len(got) != 1 || !got[wsB] {
		t.Fatalf("pinned = %v, want only %s", got, wsB)
	}
}

func TestPinnedWorkspacesUpperCased(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, strings.ToLower(wsA), "a").Pinned = true
	h.step(0)
	if got := h.r.PinnedWorkspaces(); !got[wsA] {
		t.Fatalf("pinned = %v, want the upper-cased id", got)
	}
}

// After a good listing, a failing write puts the loop in `unavailable`, and
// the ticks then return early for up to BackoffMax: the pins must not stay
// live meanwhile.
func TestPinnedWorkspacesClearedWhenUnavailable(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a").Pinned = true
	h.step(0)
	if h.r.PinnedWorkspaces() == nil {
		t.Fatal("setup: pinned set expected")
	}
	h.session("s1", wsA, "waiting", true)
	h.world.writeDown = errors.New("timeout")
	h.step(time.Second) // not a slow pass: the write fails, unavailable() runs
	if h.r.State().CmuxAvailable {
		t.Fatal("setup: the failed write must make the loop unavailable")
	}
	if got := h.r.PinnedWorkspaces(); got != nil {
		t.Fatalf("pinned = %v while unavailable, want nil", got)
	}
}

func TestPinnedWorkspacesClearedWhenListingFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a").Pinned = true
	h.step(0)
	h.world.listDown = errors.New("cmux busy")
	h.step(40 * time.Second)
	if got := h.r.PinnedWorkspaces(); got != nil {
		t.Fatalf("pinned = %v after a failed listing, want nil", got)
	}
}
