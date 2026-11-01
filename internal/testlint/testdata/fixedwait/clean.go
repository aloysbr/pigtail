package fixedwait

import (
	"testing"
	"testing/synctest"
	"time"
)

// A bounded polling loop: the Sleep sits in the body of a for.
func TestPollingLoop(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
		if ready() {
			return
		}
	}
	t.Fatal("never ready")
}

// A range loop is a loop too.
func TestRangeLoop(t *testing.T) {
	for range 3 {
		time.Sleep(time.Millisecond)
		if !ready() {
			t.Fatal("not ready")
		}
	}
}

// The Sleep runs in a goroutine, not before the test's assertion.
func TestSleepInGoroutine(t *testing.T) {
	done := make(chan struct{})
	go func() {
		time.Sleep(time.Millisecond)
		if !ready() {
			t.Error("not ready")
		}
		close(done)
	}()
	<-done
}

// A helper that is not a test.
func waitThenCheck(t *testing.T) {
	time.Sleep(time.Millisecond)
	if !ready() {
		t.Fatal("not ready")
	}
}

// A Sleep followed by something other than an assertion.
func TestSleepThenAct(t *testing.T) {
	time.Sleep(time.Millisecond)
	_ = ready()
	if !ready() {
		t.Fatal("not ready")
	}
}

// The Sleep is the last statement of its block: the assertion after it
// lives in another block.
func TestSleepLastInBlock(t *testing.T) {
	if ready() {
		time.Sleep(time.Millisecond)
	}
	if !ready() {
		t.Fatal("not ready")
	}
}

// An if whose body asserts nothing.
func TestSleepThenIfWithoutAssertion(t *testing.T) {
	time.Sleep(time.Millisecond)
	if !ready() {
		return
	}
}

// A Sleep of another package is not time.Sleep.
func TestOtherSleep(t *testing.T) {
	var c fakeClock
	c.Sleep(time.Second)
	if !ready() {
		t.Fatal("not ready")
	}
}

type fakeClock struct{}

func (fakeClock) Sleep(time.Duration) {}

func ready() bool { return true }

// synctest runs on virtual time: its Sleep costs nothing and waits for
// nothing real.
func TestSynctestSleep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Second)
		if !ready() {
			t.Fatal("not ready")
		}
	})
}
