package git

import (
	"context"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
)

// These tests cover the teardown half of issue #54. Selection learned to see a
// squash-merge (`clean --merged`); the branch-deletion policy inside Cleanup
// had not, so every teardown of a squash-merged workspace removed the worktree
// and left the branch behind — which is how a repo ends up with 88 orphan
// branches nobody deletes by hand.

// TestCleanupDeletesSquashMergedBranch is the regression: three commits
// collapsed into one squash commit in base. The branch tip never becomes an
// ancestor of base, so the narrow test this replaced said "keep".
func TestCleanupDeletesSquashMergedBranch(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "redesign the button", []string{"feat", "redesign"})

	commitFile(t, ws.Path, "a.txt", "a1\n", "step 1")
	commitFile(t, ws.Path, "b.txt", "b1\n", "step 2")
	commitFile(t, ws.Path, "a.txt", "a1\na2\n", "step 3")

	// Base moves on independently, then takes the branch as one squash.
	commitFile(t, mainDir, "unrelated.txt", "x\n", "other work")
	squashMerge(t, mainDir, "feat/redesign", "feat: redesign the button (#42)")

	if cherryDetects(t, mainDir, "main", "feat/redesign") {
		t.Fatal("test is not exercising the bug: git cherry already detects this merge")
	}

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Project: projectFor(mainDir), // no Force: this is the policy path
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if branchPresent(t, mainDir, "feat/redesign") {
		t.Error("a squash-merged branch should have been deleted")
	}
	if out.BranchDeleted != "feat/redesign" {
		t.Errorf("BranchDeleted = %q, want feat/redesign", out.BranchDeleted)
	}
	if out.BranchKept != "" {
		t.Errorf("BranchKept = %q, want empty", out.BranchKept)
	}
}

// TestCleanupKeepsUnmergedBranchSilently proves the broader check did not make
// the teardown greedier: real work still survives, and — since keeping it is
// documented policy rather than a surprise — the outcome stays quiet.
func TestCleanupKeepsUnmergedBranchSilently(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "pending work", []string{"feat", "pending"})

	commitFile(t, ws.Path, "a.txt", "branch work\n", "branch work")
	commitFile(t, mainDir, "a.txt", "something else entirely\n", "base work, same file")

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !branchPresent(t, mainDir, "feat/pending") {
		t.Fatal("unmerged branch was deleted — the containment check is not conservative")
	}
	if out.BranchDeleted != "" {
		t.Errorf("BranchDeleted = %q, want empty", out.BranchDeleted)
	}
	if out.BranchKept != "" {
		t.Errorf("BranchKept = %q — not-merged is documented policy and should stay quiet", out.BranchKept)
	}
}

// TestCleanupReportsUndecidedContainment covers the one outcome the user
// cannot predict: pigtail could not answer the question at all. Unlike
// "not merged", that is worth saying out loud (issue #54).
func TestCleanupReportsUndecidedContainment(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "unanswerable", []string{"feat", "unanswerable"})
	commitFile(t, ws.Path, "a.txt", "work\n", "work")

	// A base ref that does not exist locally: the check runs and cannot decide.
	ws.BackendData["base_branch"] = "origin/gone"

	b, logs := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !branchPresent(t, mainDir, "feat/unanswerable") {
		t.Fatal("an undecided branch must never be deleted")
	}
	if out.BranchKept != "feat/unanswerable" {
		t.Errorf("BranchKept = %q, want feat/unanswerable", out.BranchKept)
	}
	if !strings.Contains(out.BranchKeptReason, "could not tell") {
		t.Errorf("BranchKeptReason = %q, want it to say the question went unanswered", out.BranchKeptReason)
	}
	if !strings.Contains(out.BranchKeptReason, "origin/gone") {
		t.Errorf("BranchKeptReason = %q, want it to name the base it could not reach", out.BranchKeptReason)
	}
	if len(*logs) == 0 || !strings.Contains(strings.Join(*logs, "\n"), "undecided") {
		t.Errorf("expected a diagnostic in the log, got %v", *logs)
	}
}

// TestCleanupKeepBranchWinsOverContainment guards the explicit user choice:
// --keep-branch keeps the branch even when it is provably merged, and says
// nothing, because the user asked for exactly this.
func TestCleanupKeepBranchWinsOverContainment(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "kept on purpose", []string{"feat", "kept"})

	commitFile(t, ws.Path, "a.txt", "a1\n", "step 1")
	commitFile(t, ws.Path, "b.txt", "b1\n", "step 2")
	squashMerge(t, mainDir, "feat/kept", "feat: kept (#9)")

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Project: projectFor(mainDir), KeepBranch: true,
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !branchPresent(t, mainDir, "feat/kept") {
		t.Fatal("--keep-branch must keep the branch even when merged")
	}
	if out.BranchKept != "" || out.BranchDeleted != "" {
		t.Errorf("outcome = %+v, want both empty: the user asked for this", out)
	}
}
