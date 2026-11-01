// Mentions that must not be reported.
package rawmkdirtemp

import "testing"

// os.MkdirTemp in a comment is not a call.
func TestComment(t *testing.T) {
	_ = "os.MkdirTemp(\"\", \"x\")"
	t.TempDir()
}
