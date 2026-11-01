//go:build !(darwin && cgo)

package inputprobe

import "errors"

// ErrUnsupported: no syscall counter on this build (the probe then answers
// Unknown, never Dead).
var ErrUnsupported = errors.New("inputprobe: syscall counter unsupported on this build")

// DefaultCounter is the stub of builds without libproc (Linux, CGO_ENABLED=0).
func DefaultCounter(int) (uint64, error) { return 0, ErrUnsupported }
