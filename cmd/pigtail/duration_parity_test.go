package main

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// shortDuration (cmd/pigtail, servers.go) and store.OffsiteShortDuration are two
// identical copies of the same formatting logic, kept apart on purpose: the
// two surfaces that print them — `pigtail db list`'s generation ages and its
// offsite "last copy"/"status" lines — sit three lines apart on the same
// screen, and pulling the shared body into one function was rejected as not
// worth the coupling. What was missing is a guard: nothing today breaks if
// one copy changes wording and the other does not, and a reader comparing
// "last copy: 1h30m ago" against "status: STALE — no copy for 1h29m" would
// have no way to tell a formatting drift from a real one-minute discrepancy.
// This test pins the two copies to each other across a representative table,
// boundaries included, so a change to either alone fails loudly here instead
// of quietly in someone's terminal.
func TestShortDurationParity(t *testing.T) {
	t.Parallel()
	durations := []time.Duration{
		0,
		1 * time.Second,
		30 * time.Second,
		59 * time.Second,
		60 * time.Second, // the second/minute boundary
		61 * time.Second,
		90 * time.Second,
		30 * time.Minute,
		59 * time.Minute,
		59*time.Minute + 59*time.Second,
		60 * time.Minute, // the minute/hour boundary
		61 * time.Minute,
		90 * time.Minute,
		3*time.Hour + 1*time.Minute,
		26 * time.Hour,
		100*time.Hour + 5*time.Minute,
	}

	for _, d := range durations {
		got := shortDuration(d)
		want := store.OffsiteShortDuration(d)
		if got != want {
			t.Errorf("shortDuration(%s) = %q, store.OffsiteShortDuration(%s) = %q — the two copies have drifted apart",
				d, got, d, want)
		}
	}
}
