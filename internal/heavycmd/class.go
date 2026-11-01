// Package heavycmd recognises the commands whose memory footprint is worth
// arbitrating — type checkers, linters, test runners, monorepo task runners,
// static storybook builds — in an argv or in a shell command line.
//
// It is shared by the three places that must agree on what "heavy" means:
// `pigtail admit` (which class to ask the daemon for), `pigtail hook
// claude-bash` (which commands to route through admission), and the daemon
// (what a class is expected to cost before it has learned better).
package heavycmd

import "sort"

// Class names a family of heavy commands. It is also the key the daemon
// learns observed costs under, so renaming one discards what was learned.
type Class string

const (
	ClassTSC    Class = "tsc"
	ClassESLint Class = "eslint"
	ClassJest   Class = "jest"
	ClassVitest Class = "vitest"
	// ClassVitestBrowser is a vitest run in browser mode: every worker is a
	// live Chromium, so it costs several times what the same suite costs in
	// node and its scarce resource is the browser, not the byte. It is
	// resolved from what a previous run of that config reported (`pigtail
	// test`), or from a script named test-storybook[:…] whose body runs
	// vitest — Storybook's vitest addon runs only in browser mode. The bare
	// argv never tells: a browser run's and a node run's are identical.
	ClassVitestBrowser  Class = "vitest-browser"
	ClassNx             Class = "nx"
	ClassStorybookBuild Class = "storybook-build"
	// ClassBuild is a JS production build — vite build, next build, bunchee,
	// tsup, webpack, rollup — or a workspace member's `build` script, judged
	// by its name. Its peak is a burst seconds after it starts: on 2026-10-27
	// seven ds-ui builds, each admitted on a calm reading, sent the
	// compressor to 582-1127 MiB/s. Not `go build` nor `make build`.
	ClassBuild Class = "build"
	// ClassPrecommit is a `git commit` whose repo runs lint-staged from a
	// pre-commit hook: the commit itself is nothing, the lint it triggers is
	// a heavy command wearing a cheap command's argv.
	ClassPrecommit Class = "precommit"
	// ClassGoTest is a heavy `go test` suite. It rations the cores: one
	// run fills all 18 by itself, and three at once (load 240, measured
	// 2026-10-21) failed tests on their 10 s bounds. It is inferred for
	// `go test -race`, a `...` pattern or several packages, and for a make
	// target whose recipe runs one; never for a targeted single-package run,
	// which would put every agent's quick test behind the daemon's cap. The
	// pre-push hook also names it (`--class go-test`).
	ClassGoTest Class = "go-test"
	// ClassGeneric is anything admitted without a recognised class — an
	// explicit `pigtail admit -- some-script.sh`.
	ClassGeneric Class = "generic"
)

const gib = uint64(1) << 30

// priors are the costs assumed before anything is learned, from the RSS
// measured on 2026-09-22 (tsc and eslint ~1.3 GB each on the globex
// repos) rounded up, and a worker pool's worth for the test runners.
var priors = map[Class]uint64{
	ClassTSC:    gib * 3 / 2,
	ClassESLint: gib * 3 / 2,
	ClassJest:   3 * gib,
	ClassVitest: 2 * gib,
	// A browser worker carries a Chromium: a large monorepo pins four of
	// them on purpose. The prior covers a run of that shape until the table
	// has learned this repo's own peak at this worker count.
	ClassVitestBrowser:  4 * gib,
	ClassNx:             3 * gib,
	ClassStorybookBuild: 3 * gib,
	// Peaks measured on this machine up to 2026-10-27, charged as generic
	// then: ds-ui `npm run build -w` (bunchee) n=103 p50 4.9 p90 7.2 max
	// 9.2 GiB; a large monorepo `npm run build` (next build) n=45 p50 5.3
	// p90 7.0; a large monorepo `yarn build` n=8 p50 11.0. 8 GiB covers the frequent
	// ones at p90; the table learns the rest after three runs.
	ClassBuild: 8 * gib,
	// A pre-commit lint runs over the staged files, not the tree, so it
	// starts below a full eslint; the table learns the repo's real peak.
	ClassPrecommit: gib,
	// Parallel compilation plus -race binaries; the table learns the real peak.
	ClassGoTest:  3 * gib,
	ClassGeneric: gib * 3 / 2,
}

// Prior returns the cost assumed for class before any observation. An
// unknown class costs what a generic one does.
func Prior(c Class) uint64 {
	if p, ok := priors[c]; ok {
		return p
	}
	return priors[ClassGeneric]
}

// Known reports whether c is one of the classes above.
func Known(c Class) bool {
	_, ok := priors[c]
	return ok
}

// calmPriorFloor is the prior from which a class must find the compressor
// calm over a whole window, not only the last ten seconds, before it starts —
// and is not released from that by a long wait (2026-10-28, D1). A class this
// costly peaks in a burst seconds after it starts: admitted in a lull
// between two bursts, it makes the next one.
const calmPriorFloor = 4 * gib

// CalmClasses returns the classes held to that rule, sorted.
func CalmClasses() []Class {
	var out []Class
	for c, p := range priors {
		if p >= calmPriorFloor {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Heavier returns whichever of a and b is expected to cost more; the empty
// Class loses to anything. Used to pick one class for a script that runs
// several heavy commands in a row.
func Heavier(a, b Class) Class {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if Prior(b) > Prior(a) {
		return b
	}
	return a
}

// Browser-run cost model. A browser-mode run's footprint is dominated by one
// live browser per worker, so a single class-wide constant fits it badly: the
// same suite costs three times more at eight workers than at two, and pigtail
// itself picks that number.
const (
	// browserRunBase covers the vitest process, the Vite dev server it runs
	// the tests through, and the browser provider.
	browserRunBase = gib
	// browserPerWorker is one browser instance with a page open. Chromium
	// under test sits around a gigabyte; this is deliberately not shaved
	// thinner, because under-reserving is what the offsite-visibility incident was.
	browserPerWorker = gib
)

// BrowserRunCost is what a browser-mode run of n workers is expected to cost
// before anything has been learned about that repo at that worker count. It
// is sent as the request's cost *prior*, not as an override: the daemon still
// replaces it with the observed peak once it has enough samples.
//
// A worker count of zero or less (nothing recorded yet) falls back to the
// class prior, which is what the daemon would have used anyway.
func BrowserRunCost(workers int) uint64 {
	if workers <= 0 {
		return Prior(ClassVitestBrowser)
	}
	return browserRunBase + uint64(workers)*browserPerWorker //nolint:gosec // G115: guarded positive
}
