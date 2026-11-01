package roles

import "time"

// Episode is an unrecovered alert of a role.
type Episode struct {
	Key   time.Time
	Acked bool
}

// Action is what the sweep writes for one pass.
type Action struct {
	Raise    time.Time   // zero = none
	Recover  []time.Time // keys to close
	Reclosed time.Time   // non-zero: the verdict's episode is a closed key; no write, the sweep logs it once
}

// Decide turns a verdict and the role's open and closed episodes into
// journal writes. At most one open episode per role; it closes only on an ok
// pass whose deadline lies in the future; a closed key is never raised again.
func Decide(open []Episode, closed []time.Time, v Verdict, held bool, now time.Time) Action {
	var a Action
	if held || v.State == StateUnknown {
		return a
	}
	if len(open) > 0 {
		if v.State == StateOK && v.Deadline.After(now) {
			for _, e := range open {
				a.Recover = append(a.Recover, e.Key)
			}
		}
		return a
	}
	if !v.Alerting() {
		return a
	}
	for _, k := range closed {
		if k.UnixMilli() == v.Episode.UnixMilli() {
			a.Reclosed = v.Episode
			return a
		}
	}
	a.Raise = v.Episode
	return a
}
