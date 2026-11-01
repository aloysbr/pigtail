package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The friction, in one sentence: "agents read a stale main and answer wrong"
// went unfixed for two months *under a fix that was believed applied*, because
// nothing ever said out loud what the main checkout was doing. The refresh
// itself landed in #96/#98. What follows is the part that makes the next drift
// visible instead of merely fixed — the two places a human is already looking.

// mainRefreshNotice renders what `pigtail new` should say about the main
// worktree it just branched from, or "" when there is nothing worth saying.
//
// Silence is the common case and it is deliberate. A create prints a line only
// when the main checkout was actually behind:
//
//   - it MOVED: name the gap that was there. "fast-forwarded" alone leaves the
//     reader unable to tell a no-op from the day it caught up on 23 commits,
//     which is precisely the difference that stayed invisible for two months.
//   - it was LEFT BEHIND: name the gap and the reason. This is the line that
//     would have surfaced the hole months earlier — the checkout that agents
//     read is stale, pigtail knows it, pigtail declined to fix it, and until
//     now it said so only in a daemon log nobody greps.
//   - it was already level, or the gap could not be measured: nothing. A
//     create that reports the absence of a problem every single time teaches
//     the eye to skip the line that one day reports one.
func mainRefreshNotice(n *pigtail.MainRefreshNote) string {
	if n == nil || n.Behind <= 0 {
		return ""
	}
	behind := fmt.Sprintf("%s behind %s", commitCount(n.Behind), refName(n.Base))
	switch n.Action {
	case pigtail.MainRefreshUpdated:
		move := ""
		if n.From != "" && n.To != "" {
			move = fmt.Sprintf(" (%s → %s)", n.From, n.To)
		}
		return fmt.Sprintf("✓ main was %s — fast-forwarded%s", behind, move)
	case pigtail.MainRefreshSkipped:
		return fmt.Sprintf("⚠ main is %s and was left as-is — %s", behind, n.Reason)
	default:
		return ""
	}
}

// mainWorktreeLine renders one project's freshness for `pigtail servers`.
//
// Unlike the create notice, this one always speaks: the question being asked
// is "is my main fresh?", asked before sending an agent to read it, and "yes,
// as of two minutes ago" is the answer that was wanted. A section that
// appeared only on bad news would answer it with silence, which reads the same
// as a daemon that stopped sweeping.
func mainWorktreeLine(m pigtail.MainWorktreeView) string {
	age := checkedAge(m.CheckedAgoSeconds)
	base := refName(m.Base)
	switch m.Action {
	case pigtail.MainRefreshUpdated:
		if m.Behind > 0 {
			return fmt.Sprintf("✓ caught up on %s onto %s (%s)", commitCount(m.Behind), base, age)
		}
		return fmt.Sprintf("✓ fast-forwarded onto %s (%s)", base, age)
	case pigtail.MainRefreshUpToDate:
		return fmt.Sprintf("✓ level with %s (%s)", base, age)
	case pigtail.MainRefreshSkipped:
		if m.Behind > 0 {
			return fmt.Sprintf("⚠ %s behind %s, left as-is — %s (%s)", commitCount(m.Behind), base, m.Reason, age)
		}
		// No known drift, but nothing was proven either: the reason is the
		// whole content of the line, and dressing it up as freshness would be
		// the lie this feature exists to stop.
		return fmt.Sprintf("· not refreshed — %s (%s)", m.Reason, age)
	default:
		return fmt.Sprintf("· unknown (%s)", age)
	}
}

// commitCount pluralises a commit count: "1 commit", "23 commits".
func commitCount(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

// refName falls back to a readable placeholder when the daemon reported no
// base — better a vague sentence than one that reads "behind  ".
func refName(base string) string {
	if strings.TrimSpace(base) == "" {
		return "its base"
	}
	return base
}

// mainWorktreeStaleAfter is how old a verdict may be before `pigtail servers`
// says so. Three sweeps at the default ten-minute cadence: one missed tick is
// normal, half an hour of them means the sweep is not running, and a freshness
// claim from a daemon that stopped checking is exactly the kind of confident
// wrong answer this whole feature exists to prevent.
const mainWorktreeStaleAfter = 30 * time.Minute

// checkedAge dates the verdict, and flags one old enough that the sweep has
// probably stopped. Every line here is a claim as of a moment, never as of
// now: without the date, a daemon killed an hour ago would keep answering
// "level with origin/main" forever, in the confident present tense.
func checkedAge(agoSeconds int) string {
	age := fmt.Sprintf("checked %s ago", formatAgo(agoSeconds))
	if time.Duration(agoSeconds)*time.Second >= mainWorktreeStaleAfter {
		age += ", sweep may have stopped"
	}
	return age
}
