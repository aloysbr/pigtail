//go:build darwin

package sysmem

import (
	"os/exec"
	"testing"
)

// TestReadMemoryReadsTheCompressor: the real vm_stat of this Mac carries the
// counters (a fixture cannot prove the names match what macOS prints). The
// parser erring is the proof a name is missing: right after a boot the
// compressor has not run yet and its counters are legitimately zero.
func TestReadMemoryReadsTheCompressor(t *testing.T) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseVMStatCompressor(string(out))
	if err != nil {
		t.Fatalf("compressor counters not read: %v", err)
	}
	if c.UnusedBytes == 0 {
		t.Fatalf("free pages not read: %+v", c)
	}
}
