//go:build linux

package cpuslots

// defaultBudget on linux (CI, containers) is the portable rule. Linux exposes
// no equivalent of darwin's perf levels that is worth shelling out for, and
// CI runners are homogeneous anyway.
func defaultBudget() int { return numCPUBudget() }
