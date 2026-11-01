package dbtool

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// The primary offsite row answers "did the backup run". It cannot answer "does
// the redundant route still work" — the host-outage incident is exactly
// a case where the primary kept reading ok while the pinned fallback route had
// been dead for days. `pigtail db list` is the only surface a human sees this
// on before the fallback's own verdict ever reaches an alarm (it plateaus at
// warn), so these tests are the whole reason the tranche is checkable at all.

// No fallback configured: nothing to add. This is the case every fixture in
// offsite_test.go and offsite_visibility_test.go already exercises
// (none of them set FallbackDest), so this test only makes the rule explicit.
func TestOffsiteFallbackLinesAreAbsentWhenNotConfigured(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastOKAt:  now.Add(-20 * time.Minute),
		LastTryAt: now.Add(-20 * time.Minute),
	}, now)

	if strings.Contains(out, "fallback:") || strings.Contains(out, "exercise:") {
		t.Fatalf("an unconfigured fallback must not print a fallback or exercise line:\n%s", out)
	}
}

// A fallback configured and exercised successfully: both lines appear, and the
// destination is named.
func TestOffsiteFallbackLinesAppearWhenConfigured(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:           "/Volumes/nas/pigtail",
		LastOKAt:          now.Add(-20 * time.Minute),
		LastTryAt:         now.Add(-20 * time.Minute),
		FallbackDest:      "backup-b:/srv/backups",
		FallbackLastOKAt:  now.Add(-2 * time.Hour),
		FallbackLastTryAt: now.Add(-2 * time.Hour),
		FallbackVia:       "127.0.0.1",
	}, now)

	if !strings.Contains(out, "fallback:  backup-b:/srv/backups") {
		t.Fatalf("the fallback destination must be named:\n%s", out)
	}
	// Two spaces after "exercise:", matching "fallback:" above — pinned here,
	// independently of TestOffsiteFallbackLinesRenderInPlaceBeforeSameDeviceWarning,
	// so a one-column misalignment on this specific line has a test of its
	// own to fail rather than sharing the position test's.
	if !strings.Contains(out, "exercise:  ok — exercised 2h00m ago via 127.0.0.1") {
		t.Fatalf("the exercise verdict must be rendered, aligned like fallback::\n%s", out)
	}
}

// A failing fallback must name the error, not just say "failing" — the same
// bar the primary route's `failures:` line already meets.
func TestOffsiteFallbackLineNamesTheErrorWhenFailing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:                     "/Volumes/nas/pigtail",
		LastOKAt:                    now.Add(-20 * time.Minute),
		LastTryAt:                   now.Add(-20 * time.Minute),
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastOKAt:            now.Add(-30 * time.Hour),
		FallbackLastTryAt:           now,
		FallbackConsecutiveFailures: 2,
		FallbackLastErr:             "ssh: connect to host backup-b port 22: operation timed out",
	}, now)

	if !strings.Contains(out, "operation timed out") {
		t.Fatalf("a failing fallback exercise must name its error:\n%s", out)
	}
}

// The source address a successful exercise last saw is the one fact that
// distinguishes the pinned route from an LAN fallback — it must show up when
// known. 127.0.0.1 is the healthy reading on a host's userspace-VPN
// route (see store.OffsiteState.FallbackVia), not a placeholder to be wary of.
func TestOffsiteFallbackLineShowsTheSourceAddressWhenKnown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	out := formatOffsite(store.OffsiteState{
		DestDir:           "/Volumes/nas/pigtail",
		LastOKAt:          now.Add(-20 * time.Minute),
		LastTryAt:         now.Add(-20 * time.Minute),
		FallbackDest:      "backup-b:/srv/backups",
		FallbackLastOKAt:  now.Add(-2 * time.Hour),
		FallbackLastTryAt: now.Add(-2 * time.Hour),
		FallbackVia:       "127.0.0.1",
	}, now)

	if !strings.Contains(out, "127.0.0.1") {
		t.Fatalf("the source address must be shown when known:\n%s", out)
	}
}

// Pins the fallback lines' exact position and alignment: between `failures:`
// (or `status:` when there is no streak) and the same-device warning. A
// mutation that moves the two lines elsewhere, or shifts their column
// alignment, must fail this test even though the substring checks above
// would not notice.
func TestOffsiteFallbackLinesRenderInPlaceBeforeSameDeviceWarning(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	state := store.OffsiteState{
		DestDir:           "/Volumes/nas/pigtail",
		LastOKAt:          now.Add(-20 * time.Minute),
		LastTryAt:         now.Add(-20 * time.Minute),
		SameDevice:        true,
		FallbackDest:      "backup-b:/srv/backups",
		FallbackLastOKAt:  now.Add(-2 * time.Hour),
		FallbackLastTryAt: now.Add(-2 * time.Hour),
		FallbackVia:       "127.0.0.1",
	}

	got := formatOffsite(state, now)
	want := "\noffsite: /Volumes/nas/pigtail\n" +
		"  last copy: 20m ago\n" +
		"  status:    ok\n" +
		"  fallback:  backup-b:/srv/backups\n" +
		"  exercise:  ok — exercised 2h00m ago via 127.0.0.1\n" +
		"  ⚠ this destination is on the same disk as the database; it does not survive losing it\n"

	if got != want {
		t.Fatalf("the fallback lines must sit, aligned, right after the primary status/failures block and before the same-device warning:\ngot:  %q\nwant: %q", got, want)
	}
}

// The non-regression guard that matters most: a state with no fallback
// configured must render byte-for-byte identical to what it produced before
// this task added anything. Moving the two lines, changing their alignment,
// or dropping the fb.Configured guard must all break this.
func TestOffsiteRowWithoutFallbackIsByteForByteUnchanged(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	state := store.OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastOKAt:  now.Add(-90 * time.Minute),
		LastTryAt: now,
	}

	got := formatOffsite(state, now)
	want := "\noffsite: /Volumes/nas/pigtail\n" +
		"  last copy: 1h30m ago\n" +
		"  status:    ok\n"

	if got != want {
		t.Fatalf("a state with no fallback configured must render byte-for-byte as before task 11:\ngot:  %q\nwant: %q", got, want)
	}
}
