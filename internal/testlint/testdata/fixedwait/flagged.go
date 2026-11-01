// Sources TestFindFixedWaits parses (never compiled): every line carrying a
// "want fixedwait" comment must be reported, and only those.
package fixedwait

import (
	"testing"
	"time"
)

func TestSleepThenIfFatal(t *testing.T) {
	time.Sleep(50 * time.Millisecond) // want fixedwait
	if got() != 1 {
		t.Fatal("not yet")
	}
}

func TestSleepThenIfErrorf(t *testing.T) {
	time.Sleep(time.Second) // want fixedwait
	if v := got(); v != 1 {
		t.Errorf("got %d", v)
	}
}

func TestSleepThenDirectFatalf(t *testing.T) {
	time.Sleep(time.Second) // want fixedwait
	t.Fatalf("got %d", got())
}

func TestSleepThenDirectError(t *testing.T) {
	time.Sleep(time.Second) // want fixedwait
	t.Error("late")
}

func TestSleepThenFailNow(t *testing.T) {
	time.Sleep(time.Second) // want fixedwait
	if got() != 1 {
		t.FailNow()
	}
}

func TestSleepThenFail(t *testing.T) {
	time.Sleep(time.Second) // want fixedwait
	if got() != 1 {
		t.Fail()
	}
}

func TestSleepInSubtest(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		time.Sleep(time.Second) // want fixedwait
		if got() != 1 {
			t.Fatal("not yet")
		}
	})
}

func TestSleepInNestedBlock(t *testing.T) {
	if got() == 0 {
		time.Sleep(time.Second) // want fixedwait
		if got() != 1 {
			t.Fatal("not yet")
		}
	}
}

func TestSleepAssertInElse(t *testing.T) {
	time.Sleep(time.Second) // want fixedwait
	if got() == 1 {
		return
	} else {
		t.Fatal("not yet")
	}
}

// A table loop whose subtest closure sleeps then asserts: the loop's own
// statements are exempt, the closure's are not.
func TestSleepInTableSubtest(t *testing.T) {
	for _, name := range []string{"a", "b"} {
		t.Run(name, func(t *testing.T) {
			time.Sleep(time.Second) // want fixedwait
			if got() != 1 {
				t.Fatal("not yet")
			}
		})
	}
}

func got() int { return 1 }
