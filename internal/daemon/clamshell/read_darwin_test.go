//go:build darwin

package clamshell

import (
	"os"
	"testing"
)

// TestClosedDarwinReal is the positive control on a real Mac: ioreg runs and
// the key is found (the value depends on the lid; not asserted).
func TestClosedDarwinReal(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/ioreg"); err != nil {
		t.Skipf("no ioreg: %v", err)
	}
	closed, err := Closed()
	if err != nil {
		t.Fatalf("Closed: %v", err)
	}
	t.Logf("lid closed now: %v", closed)
}
