package handlers

import (
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/testutil"
	"os"
	"testing"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
)

// TestMain strips git's repository-scoped environment (GIT_DIR …) for the
// whole package: settle_ports_test.go drives a throwaway repo with raw `git`,
// and git exports GIT_DIR to the pre-push hook that runs it — without the
// scrub, `git init` reinitialised the real repository (core.bare = true) and
// `git commit` committed into the branch being pushed (2026-10-05).
//
// It also points XDG_STATE_HOME at a temporary dir: a dispatch with a profile
// runs the launch margin guard, whose real sinks append to spawns.jsonl there
// — never the live one.
func TestMain(m *testing.M) {
	// Written against the former builtin scope table (perso master,
	// globex default): every test runs on it unless it installs another.
	scopetest.MainTwo()
	gitbackend.ScrubLocalEnv()
	sd, cleanSd := testutil.MkdirTempMainShared("pgt-state-")
	_ = os.Setenv("XDG_STATE_HOME", sd)
	code := m.Run()
	cleanSd()
	os.Exit(code)
}
