package testutil

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventuallyReturnsOnceTrue(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	Eventually(t, 30*time.Second, "third call", func() bool {
		return calls.Add(1) >= 3
	})
	if got := calls.Load(); got != 3 {
		t.Fatalf("cond called %d times, want 3", got)
	}
}

// fatalTB captures Fatalf instead of failing the real test. Its Fatalf ends
// the calling goroutine, as testing.T's does.
type fatalTB struct {
	testing.TB
	msgs []string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msgs = append(f.msgs, fmt.Sprintf(format, args...))
	panic(fatalStop{})
}

type fatalStop struct{}

func TestEventuallyFailsWithWhatAndElapsed(t *testing.T) {
	t.Parallel()
	f := &fatalTB{TB: t}
	start := time.Now()
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fatalStop); !ok {
					panic(r)
				}
			}
		}()
		Eventually(f, 50*time.Millisecond, "the door opens", func() bool { return false })
	}()
	if el := time.Since(start); el < 50*time.Millisecond {
		t.Fatalf("Eventually gave up after %s, before its 50ms", el)
	}
	if len(f.msgs) != 1 {
		t.Fatalf("Fatalf called %d times, want 1: %q", len(f.msgs), f.msgs)
	}
	msg := f.msgs[0]
	if !strings.Contains(msg, "the door opens") || !strings.Contains(msg, "50ms") {
		t.Fatalf("Fatalf message %q does not name what and the duration", msg)
	}
}

func TestSerialOnlyPanicsInAParallelTest(t *testing.T) {
	t.Run("parallel", func(t *testing.T) {
		t.Parallel()
		defer func() {
			r := recover()
			if r == nil {
				t.Error("SerialOnly did not panic in a parallel test")
				return
			}
			msg := fmt.Sprint(r)
			want := "testutil.SerialOnly: " + t.Name() + " touches process state"
			if !strings.Contains(msg, want) || !strings.Contains(msg, "must not run in parallel") {
				t.Errorf("panic %q does not name SerialOnly and the test (want %q ...)", msg, want)
			}
		}()
		SerialOnly(t)
	})
}

func TestSerialOnlyPassesInASerialTest(t *testing.T) {
	SerialOnly(t)
}
