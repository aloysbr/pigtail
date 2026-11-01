package testrun

import (
	"math"
	"strconv"
	"strings"
)

// MinCeiling resolves one worker ceiling: never above budget, never above what
// the repo itself asked for.
//
// This is the invariant of the whole command, and it was violated until
// 2026-09-23: the generated wrapper passed `maxWorkers: <budget>` through
// vitest's mergeConfig, and mergeConfig *overwrites* scalars from its second
// argument. A repo that pinned `maxWorkers: 4` therefore ran at the machine
// budget instead — on a large monorepo, whose Storybook suite runs vitest
// in browser mode, that meant nine Chromium processes where the config pins
// four on purpose, with measurements in its own comment. `pigtail test` was
// not neutral there, it was actively harmful: it recreated the very memory
// incident the pin exists to prevent.
//
// So: ceiling = min(base ?? ∞, budget). A base that cannot be read as a
// positive count (absent, zero, negative, an unparseable string) is treated as
// "no ceiling of its own" and yields the budget — fail-open in the direction
// that still caps, never in the direction that uncaps.
//
// base accepts what a JS config can hold: a number (int or, after a JSON
// round-trip, a float64), a percentage string vitest also accepts ("50%"),
// or nil. cpuCount is what the percentage resolves against — os.availableParallelism()
// in the runtime that will actually run the suite.
//
// The JS half of this semantics is rendered by [ceilingHelperJS]; the two must
// agree, and clients/node/ceiling.test.mjs exercises the JS on the same cases
// the Go table above covers.
func MinCeiling(base any, budget, cpuCount int) int {
	n, ok := asCount(base, cpuCount)
	if !ok || n <= 0 {
		return budget
	}
	return min(n, budget)
}

// asCount reads a worker count the way vitest does: a plain number, or a
// percentage of the CPU count with a floor of one.
func asCount(v any, cpuCount int) (int, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case int:
		return t, true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return int(t), true
	case string:
		s := strings.TrimSpace(t)
		if !strings.HasSuffix(s, "%") {
			return 0, false
		}
		pct, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil || math.IsNaN(pct) || math.IsInf(pct, 0) {
			return 0, false
		}
		return max(int(float64(cpuCount)*pct/100), 1), true
	default:
		return 0, false
	}
}

// ceilingHelperJS renders the JS counterpart of [MinCeiling], bound to budget.
//
// It is a rendered string rather than a file in clients/node because the
// wrapper is written into the target repo's node_modules cache as a single
// self-contained module: an import of a second pigtail file would be one more
// path to resolve, in a directory whose resolution rules are exactly what the
// wrapper is delicate about.
func ceilingHelperJS(budget int) string {
	n := strconv.Itoa(budget)
	return `const cpuCount = (os.availableParallelism ? os.availableParallelism() : os.cpus().length) || 1;
  const asCount = (v) => {
    if (typeof v === 'number') return Number.isFinite(v) ? Math.trunc(v) : null;
    if (typeof v === 'string' && v.trim().endsWith('%')) {
      const pct = Number.parseFloat(v);
      if (Number.isFinite(pct)) return Math.max(Math.floor((cpuCount * pct) / 100), 1);
    }
    return null;
  };
  // ceiling = min(base ?? Infinity, ` + n + `) — pigtail only lowers a ceiling.
  // A base it cannot read as a positive count has no ceiling of its own, so
  // the budget applies; it must never *raise* one the repo set deliberately.
  const minCeiling = (base) => {
    const value = asCount(base);
    return value === null || value <= 0 ? ` + n + ` : Math.min(value, ` + n + `);
  };`
}
