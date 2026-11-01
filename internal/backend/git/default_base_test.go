package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/ports"
)

// The friction these tests pin down, from the session journal (~20 occurrences
// since June 2026): a worktree created from the LOCAL main starts N commits in
// the past, because a local `main` only advances on an explicit pull and
// nothing ever pulls it when every task lives in its own worktree. Measured
// cost: an `audit-ci` red that was a dependency bump already on origin/main,
// and work reported "not merged" that had been merged for days.

// TestDefaultBaseIsRemoteTrackingWhenUnset is the regression test for that
// friction: with no base named anywhere (no intent, no project defaults), a
// worktree created after the remote moved must start at the NEW remote tip.
// Before the fix the default was a bare "main", which also silently disarmed
// the auto-fetch — so this asserts both halves at once.
func TestDefaultBaseIsRemoteTrackingWhenUnset(t *testing.T) {
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

	// fetchTestProject declares no Defaults, so base resolution is exercised.
	ws, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if got := gitRun(t, ws.Path, "rev-parse", "HEAD"); got != secondSHA {
		t.Errorf("new worktree HEAD = %s, want the fresh remote tip %s (started behind)", got, secondSHA)
	}
}

// TestDefaultBaseFallsBackToLocalWithoutRemote proves a repo with no remote at
// all still creates worktrees. The remote-tracking default must not become a
// hard requirement — a purely local project is a legitimate case.
func TestDefaultBaseFallsBackToLocalWithoutRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir := mustInitRepo(t, container) // no origin configured

	if got := defaultBaseRef(context.Background(), mainDir); got != "main" {
		t.Fatalf("defaultBaseRef without a remote = %q, want %q", got, "main")
	}

	ws, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello"})
	if err != nil {
		t.Fatalf("create in a remote-less repo must still work, got: %v", err)
	}
	if _, err := os.Stat(ws.Path); err != nil {
		t.Errorf("worktree not created: %v", err)
	}
}

// TestCreateFastForwardsCleanMainWorktree covers the second half of the same
// friction: fetching updates refs, not the working tree. A correct new worktree
// next to a main directory still N commits behind leaves the stale checkout
// that agents are pointed at to answer "what does main look like?".
func TestCreateFastForwardsCleanMainWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	secondSHA := advanceRemote(t, seed)

	if _, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != secondSHA {
		t.Errorf("main worktree HEAD = %s, want fast-forwarded to %s", got, secondSHA)
	}
}

// TestCreateLeavesDirtyMainWorktreeAlone is the safety half. A main checkout
// carrying uncommitted work belongs to whoever put it in that state; moving it
// is exactly the accident the shared-checkout guards exist to prevent. The
// refresh must skip, silently, and creation must still succeed.
func TestCreateLeavesDirtyMainWorktreeAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
	secondSHA := advanceRemote(t, seed)

	// An untracked file is enough: `status --porcelain` reports it, and losing
	// it to a checkout is precisely the measured 2026-08-06 incident.
	if err := os.WriteFile(filepath.Join(mainDir, "wip.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello"})
	if err != nil {
		t.Fatalf("create must still succeed over a dirty main, got: %v", err)
	}

	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != firstSHA {
		t.Errorf("dirty main worktree was moved to %s, must stay at %s", got, firstSHA)
	}
	if _, err := os.Stat(filepath.Join(mainDir, "wip.txt")); err != nil {
		t.Errorf("uncommitted file lost from the main worktree: %v", err)
	}
	// The new worktree is still branched from the fresh tip — the skipped
	// refresh is a convenience, never a correctness condition.
	if got := gitRun(t, ws.Path, "rev-parse", "HEAD"); got != secondSHA {
		t.Errorf("new worktree HEAD = %s, want %s", got, secondSHA)
	}
}

// TestCreateLeavesRepurposedMainWorktreeAlone covers the other unsafe shape:
// the main checkout parked on someone's feature branch. Fast-forwarding it onto
// origin/main would move HEAD under that session — the 2026-08-06 failure mode.
func TestCreateLeavesRepurposedMainWorktreeAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
	advanceRemote(t, seed)
	gitRun(t, mainDir, "checkout", "-q", "-b", "feat/someone-elses-work")

	if _, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if got := gitRun(t, mainDir, "branch", "--show-current"); got != "feat/someone-elses-work" {
		t.Errorf("main worktree branch = %q, must be left where the other session put it", got)
	}
	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != firstSHA {
		t.Errorf("repurposed main worktree HEAD moved to %s, must stay at %s", got, firstSHA)
	}
}

// TestCreateExplicitBaseStillWins guards the precedence order: an explicit base
// (intent, or project defaults) must never be overridden by the new resolution.
func TestCreateExplicitBaseStillWins(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "HEAD")
	advanceRemote(t, seed)

	// A bare local base is asked for explicitly: it must be honoured verbatim,
	// which also means no fetch and no refresh — the caller said "local".
	ws, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello", BaseRef: "main"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := gitRun(t, ws.Path, "rev-parse", "HEAD"); got != firstSHA {
		t.Errorf("explicit local base gave HEAD %s, want the local tip %s", got, firstSHA)
	}
	if got := gitRun(t, mainDir, "rev-parse", "HEAD"); got != firstSHA {
		t.Errorf("main worktree moved to %s under an explicit local base, want %s", got, firstSHA)
	}
}
