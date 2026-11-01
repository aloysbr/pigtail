package dbtool

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// `pigtail db list` is the detailed surface the previous slice's plan points
// people at after an alarm elsewhere. Without the streak, it answers with
// strictly less than that alarm already showed: no way to tell one failed
// attempt from fifty from this row alone.

// Three failures in a row must show up as a streak, not just as a status
// word.
func TestOffsiteLineShowsAFailureStreak(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:             "/Volumes/drive/pigtail-backups",
		LastOKAt:            now.Add(-21 * 24 * time.Hour),
		LastTryAt:           now,
		LastErr:             "offsite destination unreachable",
		ConsecutiveFailures: 3,
	}, now)

	if !strings.Contains(out, "failures:  3 in a row") {
		t.Fatalf("a streak of 3 must be named:\n%s", out)
	}
}

// Zero failures must not print a streak line at all.
func TestOffsiteLineOmitsTheStreakAtZero(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/drive/pigtail-backups",
		LastOKAt:  now.Add(-20 * time.Minute),
		LastTryAt: now.Add(-20 * time.Minute),
	}, now)

	if strings.Contains(out, "failures:") {
		t.Fatalf("a zero streak must not be printed:\n%s", out)
	}
}

// The guard that counts: a healthy row's output must stay byte-for-byte what
// it was before this change. Catches an alignment shift or a reordered line
// that a mere substring check on the failing side would miss.
func TestOffsiteLineHealthyRenderingIsUnchanged(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/drive/pigtail-backups",
		LastOKAt:  now.Add(-20 * time.Minute),
		LastTryAt: now.Add(-20 * time.Minute),
	}, now)

	want := "\noffsite: /Volumes/drive/pigtail-backups\n" +
		"  last copy: 20m ago\n" +
		"  status:    ok\n"

	if out != want {
		t.Fatalf("healthy rendering changed:\ngot:  %q\nwant: %q", out, want)
	}
}
