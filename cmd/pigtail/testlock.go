package main

import (
	"strings"

	"github.com/aloysbr/pigtail/internal/heavycmd"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// envTestLockDisable is not pigtail's variable: it belongs to the tested
// repo. big-monorepo guards its browser-mode Storybook suite with
// scripts/with-test-lock.js — a file lock that keeps a second browser run
// off the machine — and documents TEST_LOCK_DISABLE=1 as its own escape
// hatch, alongside CI and watch mode.
//
// pigtail sets it, rather than that repo calling `pigtail admit`, on
// purpose. The repo is shared with other developers; pigtail is one
// person's tool. Making a shared repo's test script depend on a binary only
// one machine has would push private plumbing into everyone's checkout, for
// a benefit only that machine gets. Using the hatch the repo already
// publishes keeps the change entirely on this side: nothing to review, and
// the lock keeps working unchanged for CI, for colleagues, and here
// whenever pigtail is not the one running the suite.
//
// The two mechanisms otherwise stack: with the lease taken first, a run can
// hold its memory reservation (5 GiB, measured) while it waits on the file
// lock — memory pinned for nothing, since `vitest-browser` is capped at one
// concurrent run anyway and already serialises them.
const envTestLockDisable = "TEST_LOCK_DISABLE"

// testLockEnv returns the environment entries to append for a command about
// to run, given the lease pigtail holds for it and that lease's class.
//
// It returns nothing unless a lease is actually held: without one the run is
// unarbitrated — fail-open, PIGTAIL_ADMIT=0, admission switched off, or
// nested under an outer lease that already made this call — and disarming
// the repo's lock then would remove the only serialisation left, which is
// strictly worse than doing nothing. An existing value, whatever it is
// (including 0, or empty), belongs to the user and is never overridden.
func testLockEnv(lease *pigtail.AdmissionLease, class string, environ []string) []string {
	if lease == nil || class != string(heavycmd.ClassVitestBrowser) {
		return nil
	}
	for _, kv := range environ {
		if name, _, ok := strings.Cut(kv, "="); ok && name == envTestLockDisable {
			return nil
		}
	}
	return []string{envTestLockDisable + "=1"}
}
