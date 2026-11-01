package setup

import (
	"strings"
	"testing"
)

// launchd waits 15 s between two starts of pigtaild (default 10 s):
// a quick but bounded restart for role supervision.
func TestPlistThrottleInterval(t *testing.T) {
	got, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "<key>ThrottleInterval</key>\n  <integer>15</integer>") {
		t.Fatalf("plist has no ThrottleInterval 15:\n%s", got)
	}
}
