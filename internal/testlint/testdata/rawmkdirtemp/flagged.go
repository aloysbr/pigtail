// Source TestFindRawMkdirTemp parses (never compiled): every line carrying a
// "want rawmkdirtemp" comment must be reported, and only those.
package rawmkdirtemp

import (
	"os"
	osx "os"
	"testing"
)

func TestRaw(t *testing.T) {
	d, _ := os.MkdirTemp("", "x") // want rawmkdirtemp
	_ = d
}

func TestAliased(t *testing.T) {
	d, _ := osx.MkdirTemp("/tmp", "x") // want rawmkdirtemp
	_ = d
}
