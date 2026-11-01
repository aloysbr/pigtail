//go:build darwin

package proccpu

import (
	"math"
	"testing"
)

func TestParsePSOutput(t *testing.T) {
	out := `    1     0   7:16.96
67802 67783   0:00.12
67808 67802 105:02.50
garbage line
99999 1 not-a-time
`
	procs, err := parsePSOutput(out)
	if err != nil {
		t.Fatalf("parsePSOutput: %v", err)
	}
	if len(procs) != 3 {
		t.Fatalf("parsed %d procs, want 3 (bad lines skipped)", len(procs))
	}
	if procs[67808].PPID != 67802 {
		t.Errorf("ppid: %d, want 67802", procs[67808].PPID)
	}
	if math.Abs(procs[67808].CPUSeconds-(105*60+2.5)) > 1e-9 {
		t.Errorf("cpu seconds: %v", procs[67808].CPUSeconds)
	}
}

func TestParsePSOutputEmpty(t *testing.T) {
	if _, err := parsePSOutput("\n\n"); err == nil {
		t.Fatal("expected error on empty ps output")
	}
}

// TestRealSampler smoke-tests the live ps sampler: it must at least see
// this test process and init.
func TestRealSampler(t *testing.T) {
	procs, err := NewSampler().Sample()
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(procs) < 2 {
		t.Fatalf("only %d processes sampled", len(procs))
	}
}
