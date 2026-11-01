package dbtool

import (
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// `pigtail db list` is where someone looks to find out whether their backups
// are any good, and three healthy generations are a confident answer to the
// wrong question: they say a bad write cannot hurt you, and nothing at all
// about losing the disk.
//
// This line is what makes the second guarantee checkable. Mostly, though, it is
// what makes its *absence* visible: an offsite destination unreachable for
// three weeks is otherwise one log line an hour that nobody reads, under a
// listing that looks entirely healthy.
//
// It reads the daemon's own report from offsite-state.json rather than probing
// the destination, for two reasons: `db list` runs with the daemon stopped, and
// touching a hung network mount is exactly how a read-only command hangs.

// formatOffsite renders the offsite row, or "" when no destination is
// configured — the same rule the deaths section follows in `pigtail servers`. A
// permanent line saying "off" would train the eye to skip the one place this
// information lives.
//
// The thresholds and the status decision itself live in
// store.ClassifyOffsite / store.RenderOffsiteDetail: this function only
// composes their output, so a future HTTP client cannot read a different
// answer than this CLI does.
func formatOffsite(s store.OffsiteState, now time.Time) string {
	if !s.Configured() {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\noffsite: %s\n", s.DestDir)

	// The age of the last *successful* copy, printed whether or not the last
	// attempt failed: while a destination is unreachable, "last landed 3 weeks
	// ago" is the number that decides whether to care.
	if s.LastOKAt.IsZero() {
		fmt.Fprintf(&b, "  last copy: never\n")
	} else {
		fmt.Fprintf(&b, "  last copy: %s ago\n", shortDuration(now.Sub(s.LastOKAt)))
	}

	health := store.ClassifyOffsite(s, now)
	fmt.Fprintf(&b, "  status:    %s\n", store.RenderOffsiteDetail(health))

	// `db list` is the detailed surface an alarm elsewhere sends people to; an
	// unqualified FAILING status gives them strictly less than they already
	// saw, with no way to tell one bad attempt from a standing streak.
	if health.ConsecutiveFailures > 0 {
		fmt.Fprintf(&b, "  failures:  %d in a row\n", health.ConsecutiveFailures)
	}

	// The fallback exercise answers a different question than everything
	// above: not "did the backup run", but "does the redundant route still
	// work". Silent when unconfigured, on the same rule as the rest of this
	// row — see store.ClassifyOffsiteFallback's doc comment.
	fb := store.ClassifyOffsiteFallback(s, health, now)
	if fb.Configured {
		fmt.Fprintf(&b, "  fallback:  %s\n", fb.Dest)
		fmt.Fprintf(&b, "  exercise:  %s\n", store.RenderOffsiteFallbackDetail(fb))
	}

	if s.SameDevice {
		fmt.Fprintf(&b, "  ⚠ this destination is on the same disk as the database; "+
			"it does not survive losing it\n")
	}
	return b.String()
}
