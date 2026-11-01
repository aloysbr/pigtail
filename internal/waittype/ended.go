package waittype

import "time"

// HeldOver: a held wait is over at its Until, or as soon as its holder pid
// is dead (D4). No holder pid: judged on Until only. Shared by the sidebar
// (cmuxsync.WaitTracker) and the idle wake (relay, D38). alive is always
// given by the caller: this package does no I/O.
func HeldOver(now, until time.Time, holderPID int, alive func(int) bool) bool {
	if !until.IsZero() && now.After(until) {
		return true
	}
	if holderPID <= 0 {
		return false
	}
	return !alive(holderPID)
}

// FailureReset: a failure told by StopFailure is over at its own end (a
// usage limit's reset); zero until = not on its own.
func FailureReset(now, until time.Time) bool { return !until.IsZero() && now.After(until) }
