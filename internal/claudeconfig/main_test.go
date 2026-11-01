package claudeconfig

import (
	"os"
	"testing"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
)

// TestMain strips git's repository-scoped environment (GIT_DIR …) for the
// whole package: these tests drive throwaway repos with raw `git`, and git
// exports GIT_DIR to the pre-push hook that runs them — without the scrub,
// they commit into the real repository (see internal/backend/git/env_test.go).
func TestMain(m *testing.M) {
	gitbackend.ScrubLocalEnv()
	os.Exit(m.Run())
}
