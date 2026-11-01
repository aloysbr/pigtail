//go:build linux

package sysmem

import "testing"

// Linux reads VmRSS from /proc: no private ABI to watch, so no line (C2).
func TestSelfCheckLineSilentOnLinux(t *testing.T) {
	if line, ok := SelfCheckLine(); ok || line != "" {
		t.Fatalf("SelfCheckLine = (%q, %v), want (\"\", false) on linux", line, ok)
	}
}
