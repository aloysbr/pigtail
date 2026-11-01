package daemon

import "testing"

// Final review M4: the real signal seam never reaches kill(0) or kill(-1).
// (Only refused pids are passed: the guard is what keeps this test safe.)
func TestPauseSignalRefusesGroupPids(t *testing.T) {
	for _, pid := range []int{-1, 0, 1} {
		if err := pauseSignal(pid); err == nil {
			t.Fatalf("pauseSignal(%d) = nil, want a refusal", pid)
		}
	}
}
