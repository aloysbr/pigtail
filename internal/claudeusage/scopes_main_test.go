package claudeusage

import (
	"os"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

// TestMain runs the package's tests on the former builtin scope table
// (perso master, globex default), the one they were written against.
func TestMain(m *testing.M) {
	scopetest.MainTwo()
	os.Exit(m.Run())
}
