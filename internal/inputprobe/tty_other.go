//go:build !unix

package inputprobe

import "errors"

// ErrNotOwnTTY and ErrOutputPending exist on every build; the stubs below
// never return them.
var (
	ErrNotOwnTTY     = errors.New("inputprobe: tty is not a character device owned by this user")
	ErrOutputPending = errors.New("inputprobe: output pending on the tty")
)

// TTYOf is the stub of builds without a unix tty (the probe then answers
// Unknown, never Dead).
func TTYOf(int) (TTYInfo, error) { return TTYInfo{}, ErrUnsupported }

// WriteTTY is the stub of builds without a unix tty.
func WriteTTY(string, []byte) error { return ErrUnsupported }
