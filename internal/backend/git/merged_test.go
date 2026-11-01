package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- helpers ---------------------------------------------------------------

// requireGit lives in cleanup_branch_test.go — same package, one definition.

// git runs a git command in dir with a deterministic identity, failing the
// test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// commitFile writes a file and commits it.
func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", name)
	git(t, dir, "commit", "-m", msg)
}

// wsAt builds a workspace pointing at dir, with the given branch and base.
func wsAt(dir, branch, base string) *pigtail.Workspace {
	return &pigtail.Workspace{
		ID:          "ws_test",
		Path:        dir,
		BackendKind: "git",
		BackendData: map[string]interface{}{"branch": branch, "base_branch": base},
	}
}

// cherryDetects reports whether the pre-#54 detection (git cherry: every
// commit patch-equivalent to one in base) would have found the branch merged.
// Used to prove a test really exercises the case the old code missed.
func cherryDetects(t *testing.T, dir, base, branch string) bool {
	t.Helper()
	out := git(t, dir, "cherry", base, branch)
	saw := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		saw = true
		if !strings.HasPrefix(line, "-") {
			return false
		}
	}
	return saw
}

// squashMerge reproduces a GitHub squash-merge of branch into the currently
// checked-out branch: N commits collapse into one brand-new commit.
func squashMerge(t *testing.T, dir, branch, msg string) {
	t.Helper()
	git(t, dir, "merge", "--squash", branch)
	git(t, dir, "commit", "-m", msg)
}

// --- tests -----------------------------------------------------------------

// TestMergeStateDetectsMultiCommitSquash is the issue #54 regression: a branch
// squash-merged from four commits. `git cherry` marks all four '+' because the
// squash has a brand-new patch-id, so the pre-#54 check reported "not merged"
// and clean --merged silently skipped the workspace.
func TestMergeStateDetectsMultiCommitSquash(t *testing.T) {
	requireGit(t)
	dir := mustInitRepo(t, t.TempDir())

	commitFile(t, dir, "README.md", "hello\n", "seed")

	git(t, dir, "checkout", "-b", "feat/redesign")
	commitFile(t, dir, "a.txt", "a1\n", "step 1")
	commitFile(t, dir, "b.txt", "b1\n", "step 2")
	commitFile(t, dir, "c.txt", "c1\n", "step 3")
	commitFile(t, dir, "a.txt", "a1\na2\n", "step 4")

	git(t, dir, "checkout", "main")
	// Base moves on independently, on files the branch never touches — the
	// realistic case, and what makes a whole-range patch-id scan expensive.
	commitFile(t, dir, "unrelated-1.txt", "x\n", "other work")
	squashMerge(t, dir, "feat/redesign", "feat: redesign (#2994)")
	commitFile(t, dir, "unrelated-2.txt", "y\n", "more other work")

	if cherryDetects(t, dir, "main", "feat/redesign") {
		t.Fatal("test is not exercising the bug: git cherry already detects this merge")
	}

	b := New(nil)
	st, err := b.MergeState(context.Background(), wsAt(dir, "feat/redesign", "main"))
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if st.Status != backend.MergeContained {
		t.Fatalf("status = %q (%s), want %q", st.Status, st.Note, backend.MergeContained)
	}
	if st.How != "squash-merged" {
		t.Errorf("how = %q, want squash-merged", st.How)
	}
	if st.BaseRef != "main" {
		t.Errorf("base ref = %q, want main", st.BaseRef)
	}
	if !strings.Contains(st.Note, "carried by") {
		t.Errorf("note should point at the carrying commit, got %q", st.Note)
	}
}

// TestMergeStateDetectsAncestor covers the plain merge / fast-forward path.
func TestMergeStateDetectsAncestor(t *testing.T) {
	requireGit(t)
	dir := mustInitRepo(t, t.TempDir())
	commitFile(t, dir, "README.md", "hello\n", "seed")

	git(t, dir, "checkout", "-b", "feat/ff")
	commitFile(t, dir, "a.txt", "a\n", "work")
	git(t, dir, "checkout", "main")
	git(t, dir, "merge", "--no-ff", "feat/ff", "-m", "merge")

	b := New(nil)
	st, err := b.MergeState(context.Background(), wsAt(dir, "feat/ff", "main"))
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if st.Status != backend.MergeContained || st.How != "merged" {
		t.Fatalf("got %+v, want contained/merged", st)
	}
}

// TestMergeStateDetectsSingleCommitSquash covers the path git cherry already
// handled before #54 — it must keep working, and keep its own label.
func TestMergeStateDetectsSingleCommitSquash(t *testing.T) {
	requireGit(t)
	dir := mustInitRepo(t, t.TempDir())
	commitFile(t, dir, "README.md", "hello\n", "seed")

	git(t, dir, "checkout", "-b", "fix/one")
	commitFile(t, dir, "a.txt", "a\n", "single change")
	git(t, dir, "checkout", "main")
	squashMerge(t, dir, "fix/one", "fix: one (#1)")

	b := New(nil)
	st, err := b.MergeState(context.Background(), wsAt(dir, "fix/one", "main"))
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if st.Status != backend.MergeContained || st.How != "squashed" {
		t.Fatalf("got %+v, want contained/squashed", st)
	}
}

// TestMergeStateAbsentForUnmergedBranch proves the check stays conservative:
// real, unmerged work must never be reported as contained, even when base has
// moved on over the same files.
func TestMergeStateAbsentForUnmergedBranch(t *testing.T) {
	requireGit(t)
	dir := mustInitRepo(t, t.TempDir())
	commitFile(t, dir, "README.md", "hello\n", "seed")
	commitFile(t, dir, "a.txt", "a0\n", "seed a")

	git(t, dir, "checkout", "-b", "feat/pending")
	commitFile(t, dir, "a.txt", "a0\nbranch work\n", "branch work")
	commitFile(t, dir, "b.txt", "b\n", "more branch work")

	git(t, dir, "checkout", "main")
	commitFile(t, dir, "a.txt", "a0\nsomething else entirely\n", "base work on the same file")

	b := New(nil)
	st, err := b.MergeState(context.Background(), wsAt(dir, "feat/pending", "main"))
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if st.Status != backend.MergeAbsent {
		t.Fatalf("status = %q (%s), want %q", st.Status, st.Note, backend.MergeAbsent)
	}
}

// TestMergeStateUnknownWhenBranchMissing proves a question we cannot answer
// reports unknown rather than absent — the distinction clean --merged reports.
func TestMergeStateUnknownWhenBranchMissing(t *testing.T) {
	requireGit(t)
	dir := mustInitRepo(t, t.TempDir())
	commitFile(t, dir, "README.md", "hello\n", "seed")

	b := New(nil)
	st, err := b.MergeState(context.Background(), wsAt(dir, "feat/ghost", "main"))
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if st.Status != backend.MergeUnknown {
		t.Fatalf("status = %q, want %q", st.Status, backend.MergeUnknown)
	}
	if !strings.Contains(st.Note, "feat/ghost") {
		t.Errorf("note should name the missing branch, got %q", st.Note)
	}
}

// TestMergeStateUnknownWhenNoBranchRecorded covers a workspace whose backend
// data carries no branch at all (a non-git-shaped record).
func TestMergeStateUnknownWhenNoBranchRecorded(t *testing.T) {
	b := New(nil)
	st, err := b.MergeState(context.Background(), &pigtail.Workspace{ID: "ws_x", Path: "/tmp"})
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if st.Status != backend.MergeUnknown {
		t.Fatalf("status = %q, want %q", st.Status, backend.MergeUnknown)
	}
}

// TestMergeStateUnknownWhenScanBudgetExhausted proves a truncated scan reports
// unknown, never a silent "absent" — the whole point of the three-valued
// answer (issue #54, point 4).
func TestMergeStateUnknownWhenScanBudgetExhausted(t *testing.T) {
	requireGit(t)
	dir := mustInitRepo(t, t.TempDir())
	commitFile(t, dir, "README.md", "hello\n", "seed")

	git(t, dir, "checkout", "-b", "feat/budget")
	commitFile(t, dir, "a.txt", "a1\n", "step 1")
	commitFile(t, dir, "a.txt", "a1\na2\n", "step 2")

	git(t, dir, "checkout", "main")
	squashMerge(t, dir, "feat/budget", "feat: budget (#7)")
	// Newer commits over the same file push the squash out of the scan window.
	commitFile(t, dir, "a.txt", "a1\na2\na3\n", "later work")
	commitFile(t, dir, "a.txt", "a1\na2\na3\na4\n", "later work again")

	prev := squashMaxCandidates
	squashMaxCandidates = 1
	t.Cleanup(func() { squashMaxCandidates = prev })

	b := New(nil)
	st, err := b.MergeState(context.Background(), wsAt(dir, "feat/budget", "main"))
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if st.Status != backend.MergeUnknown {
		t.Fatalf("status = %q (%s), want %q", st.Status, st.Note, backend.MergeUnknown)
	}
	if !strings.Contains(st.Note, "budget") {
		t.Errorf("note should explain the truncation, got %q", st.Note)
	}
}

// TestMergeStateFallsBackToUpstreamBase covers the stale-local-base case named
// in the issue: the squash exists on the remote and the user has not pulled.
// Comparing against main alone says "not merged"; the configured upstream
// answers correctly.
func TestMergeStateFallsBackToUpstreamBase(t *testing.T) {
	requireGit(t)
	container := t.TempDir()
	dir := mustInitRepo(t, container)
	commitFile(t, dir, "README.md", "hello\n", "seed")

	remote := filepath.Join(container, "remote.git")
	git(t, dir, "init", "--bare", remote)
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-u", "origin", "main")

	stale := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))

	git(t, dir, "checkout", "-b", "feat/remote-squash")
	commitFile(t, dir, "a.txt", "a1\n", "step 1")
	commitFile(t, dir, "b.txt", "b1\n", "step 2")
	commitFile(t, dir, "c.txt", "c1\n", "step 3")

	git(t, dir, "checkout", "main")
	squashMerge(t, dir, "feat/remote-squash", "feat: remote squash (#12)")
	git(t, dir, "push", "origin", "main")
	// The user's local main is now behind what was actually merged.
	git(t, dir, "reset", "--hard", stale)

	b := New(nil)
	st, err := b.MergeState(context.Background(), wsAt(dir, "feat/remote-squash", "main"))
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if st.Status != backend.MergeContained {
		t.Fatalf("status = %q (%s), want %q", st.Status, st.Note, backend.MergeContained)
	}
	if st.BaseRef != "origin/main" {
		t.Errorf("base ref = %q, want origin/main (the fresher upstream)", st.BaseRef)
	}
}

// TestMergeStateHonorsContext proves the check is cancellable — it runs in the
// selection phase of clean --merged, before any deletion, where a hang would
// be invisible to the per-deletion timeout (issue #54).
func TestMergeStateHonorsContext(t *testing.T) {
	requireGit(t)
	dir := mustInitRepo(t, t.TempDir())
	commitFile(t, dir, "README.md", "hello\n", "seed")
	git(t, dir, "checkout", "-b", "feat/cancel")
	commitFile(t, dir, "a.txt", "a\n", "work")
	git(t, dir, "checkout", "main")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := New(nil)
	st, err := b.MergeState(ctx, wsAt(dir, "feat/cancel", "main"))
	if err == nil {
		t.Fatalf("expected a context error, got state %+v", st)
	}
	if st.Status != backend.MergeUnknown {
		t.Errorf("status = %q, want %q on cancellation", st.Status, backend.MergeUnknown)
	}
}
