package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The friction pinned down here: PR #96 taught `pigtail new` to fast-forward
// the main worktree, and nothing else ever does. Measured 2026-09-07, the
// globex main checkout sat 23 commits behind origin/main with a pristine
// tree — every condition for a safe fast-forward met, no `pigtail new` in the
// window to trigger one, and every agent sent to read it answering from days
// earlier. RefreshMain is the read-side trigger; these tests hold it to the
// same four abstentions as the create path, because a periodic pass that
// loosened any of them would be strictly worse than the staleness it fixes.

func mustRefresh(t *testing.T, container string, p *pigtail.Project) backend.MainRefresh {
	t.Helper()
	res, err := New(ports.New(nilUsed{})).RefreshMain(context.Background(), p)
	if err != nil {
		t.Fatalf("RefreshMain: %v", err)
	}
	return res
}

// TestRefreshMainFastForwardsCleanCheckout is the nominal case: the exact
// shape measured on 2026-09-07 — behind, clean, on its own branch — must move.
func TestRefreshMainFastForwardsCleanCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
	secondSHA := advanceRemote(t, seed)
	if firstSHA == secondSHA {
		t.Fatal("remote tip did not advance")
	}

	res := mustRefresh(t, container, fetchTestProject(container))

	if res.Action != backend.MainRefreshUpdated {
		t.Fatalf("action = %q (%s), want %q", res.Action, res.Reason, backend.MainRefreshUpdated)
	}
	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != secondSHA {
		t.Errorf("main worktree HEAD = %s, want the fresh remote tip %s", got, secondSHA)
	}
	if res.From == "" || res.To == "" || res.From == res.To {
		t.Errorf("outcome does not name the move: from=%q to=%q", res.From, res.To)
	}
	if !strings.HasPrefix(firstSHA, res.From) || !strings.HasPrefix(secondSHA, res.To) {
		t.Errorf("outcome names %s → %s, want the abbreviations of %s → %s",
			res.From, res.To, firstSHA, secondSHA)
	}
}

// TestRefreshMainOnAnAlreadyCurrentCheckout: nothing to do is not an
// abstention. The daemon sweep uses the distinction to decide whether the next
// refusal is worth a log line, so a "no-op" reported as "skipped" would make
// the log lie about which state the checkout is in.
func TestRefreshMainOnAnAlreadyCurrentCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, _ := mustInitRepoWithRemote(t, container)
	head := gitRun(t, mainDir, "rev-parse", "HEAD")

	res := mustRefresh(t, container, fetchTestProject(container))

	if res.Action != backend.MainRefreshUpToDate {
		t.Fatalf("action = %q (%s), want %q", res.Action, res.Reason, backend.MainRefreshUpToDate)
	}
	if res.Reason != "" {
		t.Errorf("a no-op carries a reason %q; reasons belong to abstentions", res.Reason)
	}
	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s from %s with nothing to fast-forward onto", got, head)
	}
}

// --- the four abstentions, one test each ----------------------------------

// (1) The base is not remote-tracking. A purely local project, or one that
// declares `base_ref: main`, has no fresher ref to move onto: the pass must
// not invent origin/ and must not touch anything.
func TestRefreshMainSkipsLocalBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
	advanceRemote(t, seed)

	p := fetchTestProject(container)
	p.Defaults = &pigtail.ProjectDefaults{BaseRef: "main"}
	res := mustRefresh(t, container, p)

	if res.Action != backend.MainRefreshSkipped {
		t.Fatalf("action = %q, want %q for a local base", res.Action, backend.MainRefreshSkipped)
	}
	if !strings.Contains(res.Reason, "local") {
		t.Errorf("reason = %q, want it to name the local base", res.Reason)
	}
	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != firstSHA {
		t.Errorf("HEAD moved to %s under a local base, must stay at %s", got, firstSHA)
	}
}

// (2) The checkout is parked on another branch, or detached. Fast-forwarding
// it onto origin/main would move HEAD under whoever put it there — the
// 2026-08-06 incident, where four crossed checkouts rolled five files back.
func TestRefreshMainSkipsRepurposedCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("on someone else's branch", func(t *testing.T) {
		container := t.TempDir()
		mainDir, _, seed := mustInitRepoWithRemote(t, container)
		firstSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
		advanceRemote(t, seed)
		gitRun(t, mainDir, "checkout", "-q", "-b", "feat/someone-elses-work")

		res := mustRefresh(t, container, fetchTestProject(container))

		if res.Action != backend.MainRefreshSkipped {
			t.Fatalf("action = %q, want %q", res.Action, backend.MainRefreshSkipped)
		}
		if !strings.Contains(res.Reason, "feat/someone-elses-work") {
			t.Errorf("reason = %q, want it to name the branch it found", res.Reason)
		}
		if got := gitRun(t, mainDir, "branch", "--show-current"); got != "feat/someone-elses-work" {
			t.Errorf("branch = %q, must be left where the other session put it", got)
		}
		if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != firstSHA {
			t.Errorf("HEAD moved to %s, must stay at %s", got, firstSHA)
		}
	})

	t.Run("detached", func(t *testing.T) {
		container := t.TempDir()
		mainDir, _, seed := mustInitRepoWithRemote(t, container)
		firstSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
		advanceRemote(t, seed)
		gitRun(t, mainDir, "checkout", "-q", "--detach")

		res := mustRefresh(t, container, fetchTestProject(container))

		if res.Action != backend.MainRefreshSkipped {
			t.Fatalf("action = %q, want %q", res.Action, backend.MainRefreshSkipped)
		}
		if !strings.Contains(res.Reason, "detached") {
			t.Errorf("reason = %q, want it to say the HEAD is detached", res.Reason)
		}
		if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != firstSHA {
			t.Errorf("detached HEAD moved to %s, must stay at %s", got, firstSHA)
		}
	})
}

// (3) The working tree is not pristine. An untracked file is enough: losing it
// to a fast-forward is exactly the measured 2026-08-06 damage, and a
// background sweep would do it with nobody watching.
func TestRefreshMainSkipsDirtyCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
	advanceRemote(t, seed)

	wip := filepath.Join(mainDir, "wip.txt")
	if err := os.WriteFile(wip, []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustRefresh(t, container, fetchTestProject(container))

	if res.Action != backend.MainRefreshSkipped {
		t.Fatalf("action = %q, want %q over a dirty tree", res.Action, backend.MainRefreshSkipped)
	}
	if !strings.Contains(res.Reason, "pristine") {
		t.Errorf("reason = %q, want it to name the dirty tree", res.Reason)
	}
	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != firstSHA {
		t.Errorf("dirty checkout moved to %s, must stay at %s", got, firstSHA)
	}
	if _, err := os.Stat(wip); err != nil {
		t.Errorf("uncommitted file lost from the main worktree: %v", err)
	}
}

// (4) The move would not be a fast-forward: the local branch carries commits
// of its own. --ff-only is what makes this pass unable to create a merge
// commit or rewrite history, and its refusal must stay a refusal.
func TestRefreshMainSkipsWhenNotAFastForward(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	// A local commit on main that the remote has never seen, then a remote
	// that moves too: the histories have diverged, the tree is still clean.
	gitRun(t, mainDir, "-c", "user.email=t@t", "-c", "user.name=t",
		"commit", "--allow-empty", "-m", "local-only")
	localSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
	advanceRemote(t, seed)

	res := mustRefresh(t, container, fetchTestProject(container))

	if res.Action != backend.MainRefreshSkipped {
		t.Fatalf("action = %q, want %q on a diverged branch", res.Action, backend.MainRefreshSkipped)
	}
	if !strings.Contains(res.Reason, "fast-forward") {
		t.Errorf("reason = %q, want it to say it was not a fast-forward", res.Reason)
	}
	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != localSHA {
		t.Errorf("HEAD moved to %s, must stay on the local commit %s", got, localSHA)
	}
	// No merge commit, no second parent: --ff-only did not fall back.
	if parents := gitRun(t, mainDir, "rev-list", "--parents", "-n", "1", "HEAD"); len(strings.Fields(parents)) > 2 {
		t.Errorf("a merge commit was created: %q", parents)
	}
}

// TestRefreshMainOnAMissingDirectory: a registered project whose container was
// moved or deleted is a skip with a reason, not an error that would abort the
// sweep for every project after it.
func TestRefreshMainOnAMissingDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	res := mustRefresh(t, "", fetchTestProject(filepath.Join(t.TempDir(), "gone")))
	if res.Action != backend.MainRefreshSkipped {
		t.Fatalf("action = %q, want %q", res.Action, backend.MainRefreshSkipped)
	}
	if !strings.Contains(res.Reason, "missing") {
		t.Errorf("reason = %q, want it to say the directory is missing", res.Reason)
	}
}

// TestRefreshMainDowngradesUpToDateWhenTheFetchFailed: offline, the local
// origin/main is whatever was last fetched. Fast-forwarding onto it is still
// worth doing, but reaching it and reporting "up-to-date" would assert
// freshness that was never checked — a stale checkout that looks current is
// precisely the failure this pass exists to end.
func TestRefreshMainDowngradesUpToDateWhenTheFetchFailed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, remoteDir, _ := mustInitRepoWithRemote(t, container)
	head := gitRun(t, mainDir, "rev-parse", "HEAD")

	// Point origin at a path that does not exist: the fetch fails, the
	// remote-tracking ref survives, and HEAD is already on it.
	gitRun(t, mainDir, "remote", "set-url", "origin", remoteDir+"-gone")

	res := mustRefresh(t, container, fetchTestProject(container))

	if res.Action != backend.MainRefreshSkipped {
		t.Fatalf("action = %q, want %q when the fetch failed", res.Action, backend.MainRefreshSkipped)
	}
	if !strings.Contains(res.Reason, "could not fetch") {
		t.Errorf("reason = %q, want it to say the fetch failed", res.Reason)
	}
	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s, want %s", got, head)
	}
}

// --- how far behind ---------------------------------------------------------

// The verdict now carries the SIZE of the drift, not just its existence.
// "left as-is because the tree is dirty" is a fact about pigtail; "23 commits
// behind and left as-is because the tree is dirty" is a fact about the
// checkout an agent is about to read, and it is the second one that would have
// surfaced this hole months earlier. Counted read-only, before any guard, so
// it cannot loosen the four conditions.

// TestRefreshMainCountsTheDriftItClosed: the nominal move reports how far
// behind the checkout was, measured before the fast-forward.
func TestRefreshMainCountsTheDriftItClosed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	_, _, seed := mustInitRepoWithRemote(t, container)
	advanceRemote(t, seed)
	advanceRemote(t, seed)

	res := mustRefresh(t, container, fetchTestProject(container))

	if res.Action != backend.MainRefreshUpdated {
		t.Fatalf("action = %q (%s), want %q", res.Action, res.Reason, backend.MainRefreshUpdated)
	}
	if res.Behind != 2 {
		t.Fatalf("behind = %d, want the 2 commits the checkout was missing", res.Behind)
	}
}

// TestRefreshMainCountsTheDriftItLeftInPlace: the abstention paths are where
// the count matters most — nothing moves, so the number is the only thing
// saying how bad it is. Each of the three measurable refusals must carry it.
func TestRefreshMainCountsTheDriftItLeftInPlace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("dirty tree", func(t *testing.T) {
		container := t.TempDir()
		mainDir, _, seed := mustInitRepoWithRemote(t, container)
		advanceRemote(t, seed)
		advanceRemote(t, seed)
		if err := os.WriteFile(filepath.Join(mainDir, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		res := mustRefresh(t, container, fetchTestProject(container))

		if res.Action != backend.MainRefreshSkipped || res.Behind != 2 {
			t.Fatalf("got %q behind %d, want a skip that names 2 commits of drift (%s)",
				res.Action, res.Behind, res.Reason)
		}
	})

	t.Run("parked on a feature branch", func(t *testing.T) {
		container := t.TempDir()
		mainDir, _, seed := mustInitRepoWithRemote(t, container)
		advanceRemote(t, seed)
		advanceRemote(t, seed)
		gitRun(t, mainDir, "checkout", "-q", "-b", "feat/someone-elses-work")

		res := mustRefresh(t, container, fetchTestProject(container))

		// HEAD..origin/main counts the same drift whatever branch HEAD is on,
		// which is what makes the count usable on this path at all.
		if res.Action != backend.MainRefreshSkipped || res.Behind != 2 {
			t.Fatalf("got %q behind %d, want a skip that names 2 commits of drift (%s)",
				res.Action, res.Behind, res.Reason)
		}
	})

	t.Run("detached", func(t *testing.T) {
		container := t.TempDir()
		mainDir, _, seed := mustInitRepoWithRemote(t, container)
		advanceRemote(t, seed)
		gitRun(t, mainDir, "checkout", "-q", "--detach")

		res := mustRefresh(t, container, fetchTestProject(container))

		if res.Action != backend.MainRefreshSkipped || res.Behind != 1 {
			t.Fatalf("got %q behind %d, want a skip that names 1 commit of drift (%s)",
				res.Action, res.Behind, res.Reason)
		}
	})
}

// TestRefreshMainReportsNoDriftWhenLevel: 0 must mean level here, so a caller
// can safely stay silent on it. (0 also covers "unmeasurable" — see
// MainRefresh.Behind — which is why no caller may read it as proven freshness.)
func TestRefreshMainReportsNoDriftWhenLevel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mustInitRepoWithRemote(t, container)

	res := mustRefresh(t, container, fetchTestProject(container))

	if res.Action != backend.MainRefreshUpToDate || res.Behind != 0 {
		t.Fatalf("got %q behind %d, want up-to-date with no drift", res.Action, res.Behind)
	}
}

// TestRefreshMainReportsNoDriftForALocalBase: guard (1) returns before there
// is anything remote to count against. Inventing a number there would be worse
// than the silence — see the local-base skip, which is a normal request, not a
// problem.
func TestRefreshMainReportsNoDriftForALocalBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	_, _, seed := mustInitRepoWithRemote(t, container)
	advanceRemote(t, seed)

	p := fetchTestProject(container)
	p.Defaults = &pigtail.ProjectDefaults{BaseRef: "main"}
	res := mustRefresh(t, container, p)

	if res.Action != backend.MainRefreshSkipped || res.Behind != 0 {
		t.Fatalf("got %q behind %d, want a local-base skip with no count", res.Action, res.Behind)
	}
}
