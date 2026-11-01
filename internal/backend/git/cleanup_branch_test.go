package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
)

// These tests cover issue #53: the branch recorded in BackendData is a
// snapshot taken at creation time, so a `git switch` inside the worktree
// leaves the store describing a branch the worktree no longer holds. A
// teardown that trusts that snapshot deletes the wrong branch — unconditionally
// (`branch -D`) and, before this change, silently.

// quietBackend builds a Backend whose diagnostics are captured instead of
// reaching the daemon log, and returns both.
func quietBackend(t *testing.T) (*Backend, *[]string) {
	t.Helper()
	var logs []string
	b := New(nil, WithLogger(func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}))
	return b, &logs
}

// switchTo checks out a new branch inside the worktree at path, the way a
// human does mid-task. The workspace's BackendData is deliberately NOT
// updated — that divergence is the whole subject of issue #53.
func switchTo(t *testing.T, path, branch string) {
	t.Helper()
	mustBeScratch(t, path)
	if out, err := exec.Command("git", "-C", path, "switch", "-q", "-c", branch).CombinedOutput(); err != nil {
		t.Fatalf("git switch -c %s: %v: %s", branch, err, out)
	}
}

// branchPresent reports whether refs/heads/<branch> exists in the repo at dir.
func branchPresent(t *testing.T, dir, branch string) bool {
	t.Helper()
	mustBeScratch(t, dir)
	return exec.Command("git", "-C", dir, "show-ref", "--verify", "--quiet",
		"refs/heads/"+branch).Run() == nil
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// TestCleanupDeletesBranchStillCheckedOut is the baseline the rest of this
// file contrasts with: when the worktree still holds the branch the store
// recorded, --force deletes it and says so.
func TestCleanupDeletesBranchStillCheckedOut(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "still on it", []string{"feat", "still-on-it"})

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force: true, Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if branchPresent(t, mainDir, "feat/still-on-it") {
		t.Error("branch feat/still-on-it should have been deleted")
	}
	if out.BranchDeleted != "feat/still-on-it" {
		t.Errorf("BranchDeleted = %q, want feat/still-on-it", out.BranchDeleted)
	}
	if out.BranchKept != "" {
		t.Errorf("BranchKept = %q, want empty", out.BranchKept)
	}
}

// TestCleanupRefusesStaleStoredBranch is the core of issue #53. The worktree
// was switched away from the branch pigtail created; the recorded name now
// describes something else entirely. Deleting it would be silent work loss, so
// the teardown must delete neither branch and report what it left.
func TestCleanupRefusesStaleStoredBranch(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "stale record", []string{"feat", "recorded"})
	switchTo(t, ws.Path, "fix/actually-here")

	b, logs := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force: true, Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// The worktree itself must still be gone — refusing to delete a branch
	// is not a reason to leave the directory behind.
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Errorf("worktree directory should be gone, stat err = %v", err)
	}
	// Neither branch may be touched: the recorded one because it no longer
	// belongs to this workspace, the live one because it holds the work.
	if !branchPresent(t, mainDir, "feat/recorded") {
		t.Error("recorded branch feat/recorded was deleted — that is the #53 hazard")
	}
	if !branchPresent(t, mainDir, "fix/actually-here") {
		t.Error("live branch fix/actually-here was deleted")
	}
	if out.BranchDeleted != "" {
		t.Errorf("BranchDeleted = %q, want empty", out.BranchDeleted)
	}
	if out.BranchKept != "fix/actually-here" {
		t.Errorf("BranchKept = %q, want fix/actually-here", out.BranchKept)
	}
	// The reason has to name both branches, otherwise it does not explain
	// anything the user can act on.
	for _, want := range []string{"fix/actually-here", "feat/recorded"} {
		if !strings.Contains(out.BranchKeptReason, want) {
			t.Errorf("BranchKeptReason %q does not mention %q", out.BranchKeptReason, want)
		}
	}
	if len(*logs) == 0 {
		t.Error("the skipped deletion left no trace in the log")
	}
}

// TestCleanupRefusesStaleStoredBranchWhenNameCollides pins the specific
// scenario from the issue: the recorded name happens to match a live,
// unrelated branch. `branch -D` is unconditional, so trusting the store here
// destroys someone else's work.
func TestCleanupRefusesStaleStoredBranchWhenNameCollides(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "collision", []string{"feat", "collides"})
	switchTo(t, ws.Path, "fix/real-work")

	// Someone else re-creates the recorded name for unrelated work. It now
	// carries a commit that exists nowhere else.
	if out, err := exec.Command("git", "-C", mainDir, "branch", "-f", "feat/collides", "main").CombinedOutput(); err != nil {
		t.Fatalf("recreate branch: %v: %s", err, out)
	}

	b, _ := quietBackend(t)
	if _, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force: true, Project: projectFor(mainDir),
	}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !branchPresent(t, mainDir, "feat/collides") {
		t.Fatal("an unrelated branch that merely shares the recorded name was deleted")
	}
}

// TestCleanupKeepsBranchOnDetachedHead covers the other way the recorded name
// stops describing the worktree: a detached HEAD holds no branch at all.
func TestCleanupKeepsBranchOnDetachedHead(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "detached", []string{"feat", "detached-head"})
	if out, err := exec.Command("git", "-C", ws.Path, "switch", "-q", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("git switch --detach: %v: %s", err, out)
	}

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force: true, Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !branchPresent(t, mainDir, "feat/detached-head") {
		t.Error("branch deleted although the worktree was detached from it")
	}
	if out.BranchKept != "feat/detached-head" {
		t.Errorf("BranchKept = %q, want feat/detached-head", out.BranchKept)
	}
	if !strings.Contains(out.BranchKeptReason, "detached") {
		t.Errorf("BranchKeptReason %q does not mention the detached HEAD", out.BranchKeptReason)
	}
}

// TestCleanupResolvesBranchOfPrunableWorktree is why the resolution reads
// git's administrative record rather than running `branch --show-current`
// inside the worktree: an interrupted teardown (#51) destroys the .git file,
// so the worktree can no longer answer for itself — but .git/worktrees/<n>/HEAD
// survives and still names the live branch.
func TestCleanupResolvesBranchOfPrunableWorktree(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "prunable", []string{"feat", "prunable-record"})
	switchTo(t, ws.Path, "fix/survived-in-record")
	// The .git file is one of the first things `worktree remove` deletes.
	if err := os.Remove(filepath.Join(ws.Path, ".git")); err != nil {
		t.Fatalf("remove worktree .git: %v", err)
	}

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force: true, Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !branchPresent(t, mainDir, "feat/prunable-record") {
		t.Error("recorded branch deleted although the admin record named another")
	}
	if out.BranchKept != "fix/survived-in-record" {
		t.Errorf("BranchKept = %q, want fix/survived-in-record", out.BranchKept)
	}
}

// TestCleanupFallsBackToStoredBranchWhenRecordIsGone keeps the legitimate
// case working: the worktree was removed by hand, git has pruned its record,
// and nothing but the store can name the branch. Falling back is what lets
// `clean` finish a half-manual teardown.
func TestCleanupFallsBackToStoredBranchWhenRecordIsGone(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "record gone", []string{"feat", "record-gone"})
	if out, err := exec.Command("git", "-C", mainDir, "worktree", "remove", "--force", ws.Path).CombinedOutput(); err != nil {
		t.Fatalf("worktree remove: %v: %s", err, out)
	}
	if !branchPresent(t, mainDir, "feat/record-gone") {
		t.Fatal("precondition: worktree remove should leave the branch")
	}

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force: true, Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if branchPresent(t, mainDir, "feat/record-gone") {
		t.Error("branch should have been deleted from the stored name")
	}
	if out.BranchDeleted != "feat/record-gone" {
		t.Errorf("BranchDeleted = %q, want feat/record-gone", out.BranchDeleted)
	}
}

// TestCleanupReportsFailedBranchDeletion covers defect 2 of the issue: the
// `_ = err` that logged nothing. A `branch -D` that fails must surface both in
// the log and in the outcome.
func TestCleanupReportsFailedBranchDeletion(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "delete fails", []string{"feat", "delete-fails"})

	// Remove the worktree by hand and drop the branch too, so the teardown's
	// `branch -D` runs against a name that no longer exists and fails.
	if out, err := exec.Command("git", "-C", mainDir, "worktree", "remove", "--force", ws.Path).CombinedOutput(); err != nil {
		t.Fatalf("worktree remove: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", mainDir, "branch", "-D", "feat/delete-fails").CombinedOutput(); err != nil {
		t.Fatalf("branch -D: %v: %s", err, out)
	}

	b, logs := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force: true, Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup should not fail on a branch that is already gone: %v", err)
	}
	if out.BranchDeleted != "" {
		t.Errorf("BranchDeleted = %q, want empty — nothing was deleted", out.BranchDeleted)
	}
	if out.BranchKept != "feat/delete-fails" {
		t.Errorf("BranchKept = %q, want feat/delete-fails", out.BranchKept)
	}
	if len(*logs) == 0 {
		t.Fatal("a failing branch -D produced no log line — this is the `_ = err` defect")
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "feat/delete-fails") {
		t.Errorf("log does not name the branch: %v", *logs)
	}
}

// TestCleanupMergedPolicyUsesResolvedBranch checks the non-force path too:
// "is it safe to delete?" must be asked about the branch the worktree holds,
// not the one the store remembers.
func TestCleanupMergedPolicyUsesResolvedBranch(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "merged policy", []string{"feat", "merged-policy"})
	// The live branch carries an unmerged commit; the recorded one does not.
	switchTo(t, ws.Path, "fix/unmerged-work")
	if err := os.WriteFile(filepath.Join(ws.Path, "work.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", ws.Path, "add", "work.txt"},
		{"-C", ws.Path, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "work"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// Commit so the worktree is clean and the non-force guard lets us through.

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if branchPresent(t, mainDir, "feat/merged-policy") == false {
		t.Error("recorded branch deleted on the non-force path")
	}
	if !branchPresent(t, mainDir, "fix/unmerged-work") {
		t.Error("unmerged live branch deleted")
	}
	if out.BranchDeleted != "" {
		t.Errorf("BranchDeleted = %q, want empty", out.BranchDeleted)
	}
}

// TestCleanupKeepBranchStaysSilent guards against noise: an explicitly
// requested --keep-branch is not a surprise and must not be reported as one.
func TestCleanupKeepBranchStaysSilent(t *testing.T) {
	requireGit(t)
	mainDir, ws := scratchRepoAndWorkspace(t, "keep branch", []string{"feat", "keep-branch"})
	// Switch away, so there IS something the teardown would otherwise report.
	switchTo(t, ws.Path, "fix/elsewhere")

	b, _ := quietBackend(t)
	out, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{
		Force: true, KeepBranch: true, Project: projectFor(mainDir),
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !branchPresent(t, mainDir, "feat/keep-branch") {
		t.Fatal("--keep-branch deleted the branch")
	}
	if out.BranchKept != "" || out.BranchKeptReason != "" {
		t.Errorf("--keep-branch reported as a surprise: %+v", out)
	}
}

// TestResolveWorktreeBranchUnknownPath pins the fallback contract directly:
// a path the repo has no record of yields "not known", which is what makes
// Cleanup fall back to the store instead of refusing.
func TestResolveWorktreeBranchUnknownPath(t *testing.T) {
	requireGit(t)
	mainDir, _ := scratchRepoAndWorkspace(t, "unknown path", []string{"feat", "unknown-path"})

	res := resolveWorktreeBranch(context.Background(), mainDir, filepath.Join(mainDir, "..", "nope"))
	if res.known {
		t.Errorf("resolution claims to know an unregistered path: %+v", res)
	}
}

// mustBeScratch refuses a path that would send a git command at the repository
// the tests are running inside.
//
// `git -C ""` is not an error: the empty string is a no-op, so git falls
// through to the process working directory — the package directory, inside the
// pigtail checkout. Every helper here passes a path straight to `git -C`, so
// one empty workspace path is enough to create branches, move HEAD and rewrite
// remotes in the real repo. Measured before this guard existed: ~34 stray
// branches, a detached HEAD, two empty commits pushed onto a live branch, and
// origin repointed at a temp dir.
//
// A path under t.TempDir() is the only acceptable target. Fail loudly rather
// than let the suite reach outside its scratch space.
func mustBeScratch(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		t.Fatal("empty git path: `git -C \"\"` runs against the repo under test, not a scratch one")
	}
	if !strings.HasPrefix(path, os.TempDir()) && !strings.HasPrefix(path, "/private"+os.TempDir()) {
		t.Fatalf("git path %q is outside the scratch dir — refusing to touch the real repo", path)
	}
}
