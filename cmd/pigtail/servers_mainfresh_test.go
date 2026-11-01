package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func renderServers(t *testing.T, mains ...pigtail.MainWorktreeView) string {
	t.Helper()
	var buf bytes.Buffer
	printServers(&buf, pigtail.ServersResponse{
		Servers:       []pigtail.ServerView{{WorkspaceID: "ws1", Server: "dev", Port: 3005}},
		MainWorktrees: mains,
	}, nil)
	return buf.String()
}

// TestServersAnswersTheFreshnessQuestion: "is my main fresh?" is asked BEFORE
// pointing an agent at the checkout, and `pigtail servers` is where one
// already looks. The good news counts here — unlike the deaths section, whose
// absence is a non-event, this section's absence would be indistinguishable
// from a daemon that stopped sweeping.
func TestServersAnswersTheFreshnessQuestion(t *testing.T) {
	t.Parallel()
	out := renderServers(t, pigtail.MainWorktreeView{
		ProjectID: "prj_1", ProjectName: "globex", Base: "origin/main",
		Action: pigtail.MainRefreshUpToDate, CheckedAgoSeconds: 120,
	})
	for _, want := range []string{"main worktrees", "globex", "level with origin/main", "2m ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}
}

// TestServersShowsDriftLeftInPlace: the whole point of the section — a stale
// main, how stale, and why pigtail did not fix it, at the moment someone is
// deciding whether to read that directory.
func TestServersShowsDriftLeftInPlace(t *testing.T) {
	t.Parallel()
	out := renderServers(t, pigtail.MainWorktreeView{
		ProjectID: "prj_1", ProjectName: "globex", Base: "origin/main",
		Action: pigtail.MainRefreshSkipped, Behind: 23,
		Reason:            `checked out on "feat/x", not "main"`,
		CheckedAgoSeconds: 60,
	})
	for _, want := range []string{"⚠", "23 commits behind", `checked out on "feat/x"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}
}

// TestServersDatesEveryVerdict: a claim of freshness is a claim as of a
// moment. Without the date, a daemon killed an hour ago keeps answering
// "level with origin/main" in the present tense — the confident wrong answer
// this feature exists to prevent — so an old verdict says so itself.
func TestServersDatesEveryVerdict(t *testing.T) {
	t.Parallel()
	fresh := renderServers(t, pigtail.MainWorktreeView{
		ProjectName: "globex", Base: "origin/main",
		Action: pigtail.MainRefreshUpToDate, CheckedAgoSeconds: 120,
	})
	if strings.Contains(fresh, "sweep may have stopped") {
		t.Errorf("a two-minute-old verdict must not be flagged:\n%s", fresh)
	}
	stale := renderServers(t, pigtail.MainWorktreeView{
		ProjectName: "globex", Base: "origin/main",
		Action: pigtail.MainRefreshUpToDate, CheckedAgoSeconds: 3600,
	})
	if !strings.Contains(stale, "sweep may have stopped") {
		t.Errorf("an hour-old verdict must say the sweep may be gone:\n%s", stale)
	}
}

// TestServersSaysNothingWithoutAVerdict: no project, a daemon that just
// started, `-refresh-main=false`. Nothing is KNOWN — which must not be
// rendered as nothing being WRONG.
func TestServersSaysNothingWithoutAVerdict(t *testing.T) {
	t.Parallel()
	if out := renderServers(t); strings.Contains(out, "main worktrees") {
		t.Fatalf("an empty registry printed a section:\n%s", out)
	}
}

// TestServersFallsBackToTheProjectID: a project registered without a name is
// still a project someone reads from; it must not print a nameless line.
func TestServersFallsBackToTheProjectID(t *testing.T) {
	t.Parallel()
	out := renderServers(t, pigtail.MainWorktreeView{
		ProjectID: "prj_1", Base: "origin/main", Action: pigtail.MainRefreshUpToDate,
	})
	if !strings.Contains(out, "prj_1") {
		t.Fatalf("a nameless project must still be identified:\n%s", out)
	}
}

// TestMainWorktreeLineNeverPretendsFreshness: an abstention with no measured
// drift (the fetch failed, the tree was unreadable) proves nothing. Rendering
// it with a ✓ would be the exact lie the three-valued verdict exists to avoid.
func TestMainWorktreeLineNeverPretendsFreshness(t *testing.T) {
	t.Parallel()
	line := mainWorktreeLine(pigtail.MainWorktreeView{
		Base: "origin/main", Action: pigtail.MainRefreshSkipped,
		Reason: "could not fetch origin/main, so freshness is unproven", CheckedAgoSeconds: 30,
	})
	if strings.Contains(line, "✓") || strings.Contains(line, "level") {
		t.Fatalf("an unproven checkout was rendered as fresh: %q", line)
	}
	if !strings.Contains(line, "could not fetch") {
		t.Fatalf("line %q drops the reason, which is its whole content", line)
	}
}
