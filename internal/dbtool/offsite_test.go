package dbtool

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// `db list` is where someone looks to find out whether their backups are any
// good. Three healthy local generations answer "a bad write cannot hurt you";
// they say nothing at all about losing the disk. The offsite line is what makes
// the second guarantee checkable — and, far more often, what makes its
// *absence* visible.

func TestOffsiteLineIsAbsentWhenNotConfigured(t *testing.T) {
	t.Parallel()
	out := formatOffsite(store.OffsiteState{}, time.Now())
	if out != "" {
		t.Fatalf("an unconfigured offsite copy must print nothing, got: %q", out)
	}
}

func TestOffsiteLineShowsTheDestinationAndAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastOKAt:  now.Add(-90 * time.Minute),
		LastTryAt: now,
	}, now)

	if !strings.Contains(out, "/Volumes/nas/pigtail") {
		t.Fatalf("the destination must be named:\n%s", out)
	}
	if !strings.Contains(out, "1h30m") {
		t.Fatalf("the age of the last successful copy must be shown:\n%s", out)
	}
}

// The whole reason this line exists. A NAS unplugged three weeks ago must not
// look like a working backup, and the failure has to name itself.
func TestOffsiteLineReportsAStandingFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastOKAt:  now.Add(-21 * 24 * time.Hour),
		LastTryAt: now,
		LastErr:   "offsite destination unreachable: /Volumes/nas/pigtail",
	}, now)

	if !strings.Contains(out, "unreachable") {
		t.Fatalf("the failure must be named:\n%s", out)
	}
	if !strings.Contains(out, "504h") {
		t.Fatalf("the age of the last good copy must stay visible while failing:\n%s", out)
	}
}

// "Configured but never succeeded" is its own state, and the most misleading
// one to render as an age: there is no last copy to be old.
func TestOffsiteLineSaysNeverRatherThanAnAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastTryAt: now,
		LastErr:   "offsite destination unreachable",
	}, now)

	if !strings.Contains(strings.ToLower(out), "never") {
		t.Fatalf("a copy that never landed must say so:\n%s", out)
	}
}

// A destination on the same filesystem is accepted but is not the guarantee it
// looks like. `db list` is the one place that can say so to someone reading it
// as protection.
func TestOffsiteLineFlagsASameDeviceDestination(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:    "/Users/me/backups",
		LastOKAt:   now.Add(-time.Hour),
		LastTryAt:  now,
		SameDevice: true,
	}, now)

	if !strings.Contains(out, "same disk") {
		t.Fatalf("a same-device destination must be flagged:\n%s", out)
	}
}
