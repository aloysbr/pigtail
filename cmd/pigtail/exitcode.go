package main

import "errors"

// exitCodeError carries a process exit code up to main through cobra's error return.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

func withExitCode(code int, err error) error { return &exitCodeError{code: code, err: err} }

// exitCode: 0 for nil, the carried code anywhere in the chain, 75 for a
// technical refusal (retryable), 1 otherwise.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return ec.code
	}
	if _, ok := retryAfter(err); ok {
		return exitTempFail
	}
	return 1
}
