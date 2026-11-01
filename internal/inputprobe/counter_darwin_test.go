package inputprobe

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// The counter must rise when the process makes unix syscalls (its own pid).
func TestDefaultCounterRisesWithSyscalls(t *testing.T) {
	pid := os.Getpid()
	before, err := DefaultCounter(pid)
	if err != nil {
		t.Fatalf("DefaultCounter(self): %v", err)
	}
	for i := 0; i < 200; i++ {
		_ = syscall.Getppid()
		_, _ = os.Stat(".")
	}
	after, err := DefaultCounter(pid)
	if err != nil {
		t.Fatalf("DefaultCounter(self): %v", err)
	}
	if after <= before {
		t.Fatalf("counter %d -> %d: want a rise after 200 syscalls", before, after)
	}
}

func TestDefaultCounterUnknownPidErrors(t *testing.T) {
	if _, err := DefaultCounter(-1); err == nil {
		t.Fatal("an unknown pid must be an error")
	}
	if _, err := DefaultCounter(99999999); err == nil {
		t.Fatal("an unknown pid must be an error")
	}
}

// A CGO_ENABLED=0 build on a Mac would compile the stub and silence the
// probe (always Unknown): this test fails then.
func TestDefaultCounterIsNotTheStubOnDarwin(t *testing.T) {
	if _, err := DefaultCounter(os.Getpid()); errors.Is(err, ErrUnsupported) {
		t.Fatal("the darwin build runs the stub counter: build with CGO_ENABLED=1")
	}
}
