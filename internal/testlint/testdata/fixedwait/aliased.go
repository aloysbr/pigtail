package fixedwait

import (
	"testing"
	clock "time"
)

// The import is aliased: clock.Sleep is time.Sleep.
func TestAliasedSleep(t *testing.T) {
	clock.Sleep(clock.Second) // want fixedwait
	if got2() != 1 {
		t.Fatal("not yet")
	}
}

func got2() int { return 1 }
