package cpuslots

import "runtime"

// DefaultBudget is the number of slots the arbiter distributes when
// -cpu-slots is left at its platform default. It is always >= 1: a zero
// budget is how the arbiter is *disabled*, never how it is sized.
func DefaultBudget() int { return defaultBudget() }

// numCPUBudget is the portable rule: one slot per logical CPU, minus one left
// for the machine itself (the daemon, the editor, the shell), floored at 1.
// This is the whole rule on linux, and the fallback on a mac whose CPU has no
// perf levels.
func numCPUBudget() int {
	n := runtime.NumCPU() - 1
	if n < 1 {
		return 1
	}
	return n
}

// perfBudget is the asymmetric-core rule: every performance core is worth a
// slot, every efficiency core a quarter of one. Measured on Apple Silicon,
// efficiency cores run JS two to four times slower than performance cores, so
// counting them whole is exactly the oversubscription this arbiter exists to
// stop — hw.ncpu says 18 on the reference machine, which has 6 fast cores.
//
// Floored at 1 for the same reason as numCPUBudget. Pure: no I/O.
func perfBudget(perf, eff int) int {
	if eff < 0 {
		eff = 0
	}
	n := perf + eff/4
	if n < 1 {
		return 1
	}
	return n
}
