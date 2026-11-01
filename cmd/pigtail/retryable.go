package main

import (
	"errors"
	"fmt"
	"time"
)

// retryableError marks a technical refusal (D2, sub_x000000000000022): nothing
// was done, and the same call may pass once the condition clears (a cap, a
// freeze, cmux or the daemon not answering, a respawn already armed). It exits
// exitTempFail (75, as `pigtail admit`) and says so in the --json error object.
// A rule or proof refusal stays a plain error: exit 1.
type retryableError struct {
	after time.Duration
	err   error
}

func (e *retryableError) Error() string {
	return fmt.Sprintf("%s [retryable after %ds]", e.err.Error(), int(e.after.Seconds()))
}

func (e *retryableError) Unwrap() error { return e.err }

// Advised delays before the one retry the method allows.
const (
	retryShort = 10 * time.Second // a cmux, ps, registry or daemon call that failed
	retryArm   = 30 * time.Second // the respawn worker did not arm, cmux did not open the tab
	retryLong  = 60 * time.Second // a cap, a freeze, a respawn already armed
)

// retryable wraps err as a technical refusal; nil stays nil.
func retryable(after time.Duration, err error) error {
	if err == nil {
		return nil
	}
	return &retryableError{after: after, err: err}
}

// retryAfter reports whether err is a technical refusal, and its delay.
func retryAfter(err error) (time.Duration, bool) {
	var r *retryableError
	if errors.As(err, &r) {
		return r.after, true
	}
	return 0, false
}

// retryFields adds the D2 keys to a --json error object.
func retryFields(obj map[string]any, err error) {
	after, ok := retryAfter(err)
	obj["retryable"] = ok
	if ok {
		obj["retry_after_s"] = int(after.Seconds())
	}
}
