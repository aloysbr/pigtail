package proccpu

import (
	"math"
	"testing"
)

func TestParsePSTime(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		err  bool
	}{
		{"0:00.05", 0.05, false},
		{"7:02", 422, false},
		{"07:02.34", 422.34, false},
		{"1:00:23", 3623, false},
		{"19:15:23", 69323, false},
		{"2-03:04:05", 2*86400 + 3*3600 + 4*60 + 5, false},
		{" 0:01.50 ", 1.5, false},
		{"", 0, true},
		{"abc", 0, true},
		{"1:2:3:4", 0, true},
		{"x:00", 0, true},
	}
	for _, c := range cases {
		got, err := ParsePSTime(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParsePSTime(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePSTime(%q): %v", c.in, err)
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("ParsePSTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSubtreeCPU(t *testing.T) {
	// 1 → 10 → 100, 101 ; 2 is unrelated.
	procs := map[int]ProcStat{
		1:   {PID: 1, PPID: 0, CPUSeconds: 1},
		10:  {PID: 10, PPID: 1, CPUSeconds: 2},
		100: {PID: 100, PPID: 10, CPUSeconds: 40},
		101: {PID: 101, PPID: 10, CPUSeconds: 0.5},
		2:   {PID: 2, PPID: 0, CPUSeconds: 99},
	}
	if got := SubtreeCPU(procs, 10); math.Abs(got-42.5) > 1e-9 {
		t.Errorf("SubtreeCPU(10) = %v, want 42.5", got)
	}
	if got := SubtreeCPU(procs, 1); math.Abs(got-43.5) > 1e-9 {
		t.Errorf("SubtreeCPU(1) = %v, want 43.5", got)
	}
	// Missing root → 0, but children linked under it still count if present.
	if got := SubtreeCPU(procs, 999); got != 0 {
		t.Errorf("SubtreeCPU(999) = %v, want 0", got)
	}
}

func TestSubtreeCPUCycleSafe(t *testing.T) {
	// Defensive: a PPID cycle (can transiently appear from a racy sample)
	// must not hang or double-count.
	procs := map[int]ProcStat{
		5: {PID: 5, PPID: 6, CPUSeconds: 1},
		6: {PID: 6, PPID: 5, CPUSeconds: 2},
	}
	if got := SubtreeCPU(procs, 5); math.Abs(got-3) > 1e-9 {
		t.Errorf("SubtreeCPU(cycle) = %v, want 3", got)
	}
}

func TestSubtreeCPUMissingRoot(t *testing.T) {
	procs := map[int]ProcStat{1: {PID: 1, PPID: 0, CPUSeconds: 5}}
	if got := SubtreeCPU(procs, 42); got != 0 {
		t.Fatalf("missing root: got %v, want 0", got)
	}
}

func TestSubtreeCPUCycleTerminates(t *testing.T) {
	// A PPID cycle must not loop forever (the `seen` set guards it).
	procs := map[int]ProcStat{
		1: {PID: 1, PPID: 2, CPUSeconds: 1},
		2: {PID: 2, PPID: 1, CPUSeconds: 2},
	}
	if got := SubtreeCPU(procs, 1); got != 3 {
		t.Fatalf("cycle: got %v, want 3", got)
	}
}
