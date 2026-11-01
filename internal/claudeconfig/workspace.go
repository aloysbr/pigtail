package claudeconfig

import (
	"os"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
)

// PreTrustWorkspace runs PreTrust for a pigtail workspace: the key is the
// repository's main checkout, ancestors are searched up to (not including) $HOME.
func PreTrustWorkspace(configDir, worktree string) Result {
	key, err := gitbackend.CanonicalRoot(worktree)
	if err != nil {
		return Result{Outcome: NotEligible, Reason: "no main checkout for " + worktree + ": " + err.Error()}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Result{Outcome: Failed, Key: key, Reason: "home directory: " + err.Error()}
	}
	return PreTrust(Request{ConfigDir: configDir, Key: key, Worktree: resolved(worktree), Home: home})
}
