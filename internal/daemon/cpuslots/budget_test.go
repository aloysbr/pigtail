package cpuslots

import (
	"runtime"
	"testing"
)

// The reference machine of the design (§3): 6 performance cores, 12
// efficiency cores → 6 + 12/4 = 9.
func TestPerfBudgetReferenceMachine(t *testing.T) {
	if got := perfBudget(6, 12); got != 9 {
		t.Fatalf("perfBudget(6, 12) = %d, want 9", got)
	}
}

func TestPerfBudgetCountsEfficiencyCoresAsAQuarter(t *testing.T) {
	if got := perfBudget(4, 4); got != 5 {
		t.Fatalf("perfBudget(4, 4) = %d, want 5", got)
	}
	// Fewer than four efficiency cores round down to nothing rather than
	// inventing a fractional slot.
	if got := perfBudget(4, 3); got != 4 {
		t.Fatalf("perfBudget(4, 3) = %d, want 4", got)
	}
}

// The budget is a permission to run at least one thing: it is never zero, or
// the arbiter would deadlock every client instead of arbitrating them.
func TestPerfBudgetFlooredAtOne(t *testing.T) {
	if got := perfBudget(0, 0); got != 1 {
		t.Fatalf("perfBudget(0, 0) = %d, want 1", got)
	}
	if got := perfBudget(-3, -8); got != 1 {
		t.Fatalf("perfBudget(-3, -8) = %d, want 1", got)
	}
}

func TestNumCPUBudgetLeavesOneCore(t *testing.T) {
	want := runtime.NumCPU() - 1
	if want < 1 {
		want = 1
	}
	if got := numCPUBudget(); got != want {
		t.Fatalf("numCPUBudget() = %d, want %d", got, want)
	}
}

// Whatever the platform, the default must be usable: at least one slot and
// no more than the logical CPU count.
func TestDefaultBudgetIsSane(t *testing.T) {
	got := DefaultBudget()
	if got < 1 {
		t.Fatalf("DefaultBudget() = %d, want >= 1", got)
	}
	if got > runtime.NumCPU() {
		t.Fatalf("DefaultBudget() = %d, want <= NumCPU (%d)", got, runtime.NumCPU())
	}
}
