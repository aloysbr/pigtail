//go:build !(darwin && cgo)

package inputprobe

import (
	"errors"
	"os"
	"testing"
)

func TestCounterStubUnsupported(t *testing.T) {
	if _, err := DefaultCounter(os.Getpid()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("stub counter: err = %v, want ErrUnsupported", err)
	}
}
