//go:build darwin

package cpuslots

import (
	"os/exec"
	"strconv"
	"strings"
)

// defaultBudget reads the CPU's perf levels from sysctl and weights them with
// perfBudget. An Intel mac has no hw.perflevel0.logicalcpu at all, and a
// sysctl that cannot be read is not a reason to refuse to arbitrate — both
// fall back to the portable rule.
func defaultBudget() int {
	perf, ok := sysctlInt("hw.perflevel0.logicalcpu")
	if !ok || perf <= 0 {
		return numCPUBudget()
	}
	eff, ok := sysctlInt("hw.perflevel1.logicalcpu")
	if !ok {
		eff = 0
	}
	return perfBudget(perf, eff)
}

// sysctlInt reads one integer sysctl. Reports ok=false for anything it cannot
// parse, so every caller has to state its fallback explicitly.
func sysctlInt(name string) (int, bool) {
	out, err := exec.Command("sysctl", "-n", name).Output()
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return n, true
}
