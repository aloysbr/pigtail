package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/testutil"
)

// TestMain strips git's repository-scoped environment (GIT_DIR …) for the
// whole package: tests here drive throwaway repos with raw `git`, and git
// exports GIT_DIR to the pre-push hook that runs them — without the scrub,
// they commit into the real repository (see internal/backend/git/env_test.go).
//
// It also makes the package hermetic (TestPackageIsHermetic): no test runs
// the real cmux nor writes the real state dir, and the package stays off the
// real Mac's cmux preferences: HOME is a temporary dir (no
// ~/.config/cmux/settings.json) and the UserDefaults read is stubbed, so no
// test forks `defaults`.
func TestMain(m *testing.M) {
	// Written against the former builtin scope table (perso master,
	// globex default): every test runs on it unless it installs another.
	scopetest.MainTwo()
	gitbackend.ScrubLocalEnv()
	cmux.ReadDefaults = func(string) (string, bool, error) { return "", false, nil }
	_, cleanup, err := hermeticEnv()
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup() // explicit: a defer would not run before os.Exit
	os.Exit(code)
}

// hermeticEnv points the package away from the developer's machine: every
// CMUX_* variable of the session running the tests is dropped (the
// workspace, surface and socket a test would otherwise act on), the cmux CLI
// becomes a stand-in that fails every call (an empty CMUX_BUNDLED_CLI_PATH
// would not do: a `cmux` on PATH is tried next), and XDG_STATE_HOME and HOME
// move to a temp dir (the usage refresh lock, the spawn registry, cmux's
// settings.json). A test that wants
// another cmux keeps its own t.Setenv. Returns the dir and the function that
// removes it (a no-op for a helper process).
//
// The real Go caches are pinned BEFORE HOME moves: go derives GOMODCACHE and
// GOCACHE from HOME, so a test running `go build` or `go list` under the temp
// HOME re-downloaded the whole module cache (read-only files) into it and left
// it behind when killed (incident of 2026-10-31: 87 + 69 GB of /tmp).
//
// A helper process (the test binary re-executed by a test, -test.run=^TestHelper…)
// inherits an env already made hermetic, plus what its parent test set
// (its own XDG_STATE_HOME): it keeps it as is and returns "".
func hermeticEnv() (string, func(), error) {
	noop := func() {}
	if os.Getenv(hermeticMarker) != "" {
		return "", noop, nil
	}
	pinGoCaches()
	dir, cleanup := testutil.MkdirTempMain("cli-test-")
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "CMUX_") {
			_ = os.Unsetenv(k)
		}
	}
	fake := filepath.Join(dir, "cmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec // an executable stand-in
		return dir, cleanup, err
	}
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return dir, cleanup, err
	}
	if err := os.Setenv(cmux.EnvBundledCLI, fake); err != nil {
		return dir, cleanup, err
	}
	if err := os.Setenv("XDG_STATE_HOME", state); err != nil {
		return dir, cleanup, err
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return dir, cleanup, err
	}
	if err := os.Setenv("HOME", home); err != nil {
		return dir, cleanup, err
	}
	return dir, cleanup, os.Setenv(hermeticMarker, dir)
}

// pinGoCaches exports the Go caches `go env` resolves from the real HOME, so
// they survive the move of HOME. Bounded at 10 s; on failure the variables
// already set are left as they are.
func pinGoCaches() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "env", "GOMODCACHE", "GOCACHE", "GOPATH").Output()
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 3 {
		return
	}
	for i, k := range []string{"GOMODCACHE", "GOCACHE", "GOPATH"} {
		if lines[i] != "" {
			_ = os.Setenv(k, lines[i])
		}
	}
}

// hermeticMarker tells a re-executed test binary that its env is already
// hermetic (set by the parent's TestMain).
const hermeticMarker = "PIGTAIL_TEST_HERMETIC_DIR"
