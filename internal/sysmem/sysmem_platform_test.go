//go:build darwin || linux

package sysmem

import (
	"os"
	"testing"
)

// TestPlatformReaders reads the real machine once. It asserts shapes, not
// values: the point is that each reader parses what this OS actually
// prints, which a fixture cannot prove.
func TestPlatformReaders(t *testing.T) {
	if a, err := Available(); err != nil || a == 0 {
		t.Fatalf("Available() = %d, %v", a, err)
	}
	if _, err := Swapouts(); err != nil {
		t.Fatalf("Swapouts(): %v", err)
	}
	p, err := Pressure()
	if err != nil {
		// Containers without PSI (/proc/pressure absent) are a real CI shape.
		t.Logf("Pressure() unavailable here: %v", err)
	} else if p != PressureNormal && p != PressureWarn && p != PressureCritical {
		t.Fatalf("Pressure() = %d, not on the 1/2/4 scale", p)
	}
	m, err := ReadMemory()
	if err != nil || m.Available == 0 || m.Total < m.Available {
		t.Fatalf("ReadMemory() = %+v, %v", m, err)
	}
	procs, err := Procs()
	if err != nil {
		t.Fatalf("Procs(): %v", err)
	}
	self, ok := procs[os.Getpid()]
	if !ok {
		t.Fatal("Procs() does not see the test process itself")
	}
	if self.MemBytes == 0 {
		t.Fatal("Procs() charges the test process nothing")
	}
}
