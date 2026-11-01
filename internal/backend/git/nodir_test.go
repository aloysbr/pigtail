package git

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestGitCMinusCEmptyIsANoOp records why the guard below has to exist: an empty
// path is not an error to git, so a command aimed at nowhere lands on whatever
// repository the process happens to be standing in — here, this checkout.
func TestGitCMinusCEmptyIsANoOp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	withC, err := exec.Command("git", "-C", "", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not inside a git repo: %v", err)
	}
	plain, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("plain rev-parse: %v", err)
	}
	if string(withC) != string(plain) {
		t.Fatalf("`git -C \"\"` resolved to %q, plain git to %q — the premise of "+
			"the empty-dir guard no longer holds", withC, plain)
	}
}

// TestRunGitRefusesEmptyDir is the guard's own test. Without it the guard is a
// claim, not a check: a refactor could drop it and every other test would stay
// green, because they all pass a real path.
func TestRunGitRefusesEmptyDir(t *testing.T) {
	// `status` is read-only, so a regression here reports the wrong repo
	// rather than modifying it.
	err := runGit(context.Background(), "", "status")
	if err == nil {
		t.Fatal("runGit with an empty dir returned nil — it ran against the process working directory")
	}
	if !strings.Contains(err.Error(), "no working directory") {
		t.Fatalf("runGit error = %v, want it to name the missing working directory", err)
	}

	out, err := captureGit(context.Background(), "", "status")
	if err == nil {
		t.Fatalf("captureGit with an empty dir returned nil and %q", out)
	}
	if out != "" {
		t.Fatalf("captureGit returned output %q on a refused call", out)
	}
}
