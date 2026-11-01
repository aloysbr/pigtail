package testrun

import (
	"strings"
	"testing"
)

// The invariant this whole file exists for: pigtail only ever *lowers* a
// worker ceiling, never raises one. A repo that pinned maxWorkers: 4 did so
// for a reason — on a large monorepo, four browser workers are four
// Chromium processes, measured and deliberate. Raising that to the machine
// budget is not arbitration, it is the memory incident the pin prevents.
func TestMinCeiling(t *testing.T) {
	const cpu = 18 // availableParallelism on the machine these defaults target
	cases := []struct {
		name   string
		base   any
		budget int
		want   int
	}{
		{"absent yields the budget", nil, 9, 9},
		{"base below the budget is kept", 4, 9, 4},
		{"base above the budget is lowered", 20, 9, 9},
		{"base equal to the budget is kept", 9, 9, 9},
		{"float from JSON is honoured", float64(4), 9, 4},
		{"percentage below the budget is kept", "25%", 9, 4},
		{"percentage above the budget is lowered", "100%", 9, 9},
		{"percentage never resolves below one", "1%", 9, 1},
		{"unreadable string yields the budget", "many", 9, 9},
		{"zero is not a ceiling, the budget wins", 0, 9, 9},
		{"negative is not a ceiling, the budget wins", -2, 9, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MinCeiling(tc.base, tc.budget, cpu); got != tc.want {
				t.Errorf("MinCeiling(%v, %d, %d) = %d, want %d", tc.base, tc.budget, cpu, got, tc.want)
			}
		})
	}
}

// The Go function above is the spec; the generated JS is what actually runs.
// They are two implementations of one semantics, so the rendered helper must
// be present in the wrapper and carry the same name the JS tests exercise.
func TestCeilingHelperIsRendered(t *testing.T) {
	js := ceilingHelperJS(9)
	for _, want := range []string{"const minCeiling", "endsWith('%')", "Math.min", "9"} {
		if !strings.Contains(js, want) {
			t.Errorf("ceilingHelperJS missing %q:\n%s", want, js)
		}
	}
}
