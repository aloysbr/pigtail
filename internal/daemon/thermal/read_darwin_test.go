//go:build darwin

package thermal

import (
	"os"
	"testing"
)

// TestReadDarwinReal is the positive control on a real Mac: notifyutil runs,
// echoes the exact key and a level the parse accepts (read only, no root).
func TestReadDarwinReal(t *testing.T) {
	if _, err := os.Stat("/usr/bin/notifyutil"); err != nil {
		t.Skipf("no notifyutil: %v", err)
	}
	s, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s < Nominal || s > Critical {
		t.Fatalf("Read = %d, want 0..3", int(s))
	}
	t.Logf("thermal state now: %s", s)
}
