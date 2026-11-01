package dbtool

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// `db list` is the only place the offsite guarantee can be checked, so every
// state it cannot express is a state that reads as "fine". Production produced
// three of them on the first real use; these are the renderings that close
// them.

// A destination configured but never once copied to. Before this, the row said
// "last copy: never / status: ok" — a reading that is actively reassuring about
// a backup that does not exist.
func TestOffsiteLineFlagsADestinationThatNeverWorked(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/drive/pigtail-backups",
		LastTryAt: now.Add(-30 * time.Minute),
	}, now)

	if !strings.Contains(strings.ToLower(out), "never") {
		t.Fatalf("a destination that never received a copy must say so:\n%s", out)
	}
	if strings.Contains(out, "status:    ok") {
		t.Fatalf("a destination that never received a copy must not read as ok:\n%s", out)
	}
}

// The exact production failure: nothing at all happened. No attempt, no error.
// Silence must not render as health.
func TestOffsiteLineFlagsADestinationNothingEverTouched(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir: "/Volumes/drive/pigtail-backups",
	}, now)

	if strings.Contains(out, "status:    ok") {
		t.Fatalf("a destination the daemon never even attempted must not read as ok:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "no attempt") {
		t.Fatalf("the absence of any attempt must be stated:\n%s", out)
	}
}

// A copy still running after hours is stuck, and that is the one thing a
// polled backup cannot report on its own.
func TestOffsiteLineReportsAStuckAttempt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:          "/Volumes/drive/pigtail-backups",
		AttemptStartedAt: now.Add(-3 * time.Hour),
		LastTryAt:        now.Add(-3 * time.Hour),
		LastOKAt:         now.Add(-4 * time.Hour),
	}, now)

	if !strings.Contains(strings.ToLower(out), "in progress") {
		t.Fatalf("an attempt still in flight must be visible:\n%s", out)
	}
	if !strings.Contains(out, "3h00m") {
		t.Fatalf("how long it has been in flight is the actionable part:\n%s", out)
	}
}

// A copy in flight for a few seconds is normal and must not be alarming.
func TestOffsiteLineIsCalmAboutAFreshAttempt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:          "/Volumes/drive/pigtail-backups",
		AttemptStartedAt: now.Add(-2 * time.Second),
		LastOKAt:         now.Add(-time.Hour),
	}, now)

	if strings.Contains(strings.ToLower(out), "stuck") {
		t.Fatalf("a two-second-old attempt must not be called stuck:\n%s", out)
	}
}

// No error, no stuck attempt — but the last successful copy is a day old, so
// the backups have quietly stopped. The rotation is hourly, so hours of
// silence is a symptom even with nothing recorded as failing.
func TestOffsiteLineFlagsAStaleSuccess(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/drive/pigtail-backups",
		LastOKAt:  now.Add(-26 * time.Hour),
		LastTryAt: now.Add(-26 * time.Hour),
	}, now)

	if strings.Contains(out, "status:    ok") {
		t.Fatalf("a day-old copy must not read as ok:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "stale") {
		t.Fatalf("the staleness must be named:\n%s", out)
	}
}

// A recent copy with no error is the one case that should read as plainly fine.
func TestOffsiteLineStaysQuietWhenHealthy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/drive/pigtail-backups",
		LastOKAt:  now.Add(-20 * time.Minute),
		LastTryAt: now.Add(-20 * time.Minute),
	}, now)

	if !strings.Contains(out, "status:    ok") {
		t.Fatalf("a fresh successful copy must read as ok:\n%s", out)
	}
	for _, word := range []string{"stale", "never", "in progress", "no attempt"} {
		if strings.Contains(strings.ToLower(out), word) {
			t.Fatalf("a healthy row must not mention %q:\n%s", word, out)
		}
	}
}
