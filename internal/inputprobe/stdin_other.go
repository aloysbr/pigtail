//go:build !(darwin && cgo)

package inputprobe

// stdinTTYImpl is the stub of builds without libproc: reading a process's
// fd 0 is impossible, so no process can be proven to leave the tty alone
// (TTYOf then fails, the probe answers Unknown).
func stdinTTYImpl(int) (uint64, bool, error) { return 0, false, ErrUnsupported }

// vnodeRdevsImpl: same stub as stdinTTYImpl.
func vnodeRdevsImpl(int) ([]uint64, error) { return nil, ErrUnsupported }
