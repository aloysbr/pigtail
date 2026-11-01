package daemon

import (
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/testutil"
	"os"
	"testing"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/cmux"
)

// TestMain strips git's repository-scoped environment for this package.
//
// reconcile_live_test.go builds a real repository under t.TempDir() and drives
// it with raw exec.Command("git", …). An inherited GIT_DIR overrides each
// subprocess's working directory, so those commands target the inherited
// repository instead — measured 2026-09-04: run under the repo's own pre-push
// hook (which runs `make test-race`, and git exports GIT_DIR to every hook),
// `git worktree add` failed with "not a git repository" while the sibling
// backend/git suite silently rewrote the real repo's origin.
//
// See git.ScrubLocalEnv for why this belongs in TestMain rather than at each
// call site.
//
// It also points PIGTAIL_MAINTENANCE_DIR at a temporary dir: every Server.Run
// syncs the workspace-subject markers there, and must never touch the real ones.
// XDG_STATE_HOME and XDG_CONFIG_HOME go to empty temporary dirs too
// (TestServerNeverTouchesRealUsageState).
func TestMain(m *testing.M) {
	// Written against the former builtin scope table (perso master,
	// globex default): every test runs on it unless it installs another.
	scopetest.MainTwo()
	gitbackend.ScrubLocalEnv()
	// Never read the real Mac's cmux UserDefaults from a test.
	cmux.ReadDefaults = func(string) (string, bool, error) { return "", false, nil }
	md, cleanMd := testutil.MkdirTempMainShared("pgt-md-") // markers and maintenance never reach the real dir, whatever the shell exports
	_ = os.Setenv("PIGTAIL_MAINTENANCE_DIR", md)
	// A dispatch with a profile runs the launch margin guard: its trace goes
	// to a temporary spawns.jsonl, never the live one.
	// No default seam reaches the real ~/.local/state/pigtail nor
	// ~/.config/pigtail: spawnreg.DefaultPath, setup.ConfigDir (the cell table
	// the usage sweep and /usage/overview read: absent ⇒ no account ⇒ no
	// claude -p), the default socket. Tests that set their own keep it.
	xs, cleanXs := testutil.MkdirTempMainShared("pgt-xs-")
	xc, cleanXc := testutil.MkdirTempMainShared("pgt-xc-")
	_ = os.Setenv("XDG_STATE_HOME", xs)
	_ = os.Setenv("XDG_CONFIG_HOME", xc)
	code := m.Run()
	cleanMd()
	cleanXs()
	cleanXc()
	os.Exit(code)
}
