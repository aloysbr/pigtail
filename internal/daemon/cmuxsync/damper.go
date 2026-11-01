package cmuxsync

import "time"

// Hysteresis of the attention pill (spec §4.3).
const (
	// WaitingHold keeps "waiting" up until the sessions have been seen out
	// of waiting this long, continuously, counted from the first observation
	// out: an answer followed by a new question does not blink.
	WaitingHold = 2 * time.Second
	// DeadConfirm is how long, across at least two observations, a surface
	// must read dead before the pill shows: a pid being replaced is absorbed.
	DeadConfirm = time.Second
)

// Damper smooths the raw inputs of the pill on two independent channels:
// the live wait, up immediately and down after WaitingHold; the dead
// surfaces, counted only after two observations at least DeadConfirm apart.
// The winner is resolved after both (Desired.resolve). The clock is the
// caller's (now), so tests drive it. Not safe for concurrent use; the runner
// owns one.
type Damper struct {
	ws map[string]*damp
}

type damp struct {
	live      liveSummary // shown while held
	held      bool
	outSince  time.Time // first observation with no live wait, zero while in
	deadSince time.Time
	deadSeen  int
}

// Apply smooths the two inputs of every desired workspace in place — the
// live wait (up at once, down after WaitingHold) and the dead surfaces
// (after two observations DeadConfirm apart) — then resolves its pill, and
// forgets the workspaces that are gone.
func (dm *Damper) Apply(now time.Time, desired map[string]Desired) {
	if dm.ws == nil {
		dm.ws = map[string]*damp{}
	}
	for id := range dm.ws {
		if _, ok := desired[id]; !ok {
			delete(dm.ws, id)
		}
	}
	for id, d := range desired {
		st := dm.ws[id]
		if st == nil {
			st = &damp{}
			dm.ws[id] = st
		}
		switch {
		case d.Live.any:
			st.live, st.held, st.outSince = d.Live, true, time.Time{}
		case st.held:
			if st.outSince.IsZero() {
				st.outSince = now
			}
			if now.Sub(st.outSince) < WaitingHold {
				d.Live = st.live
			} else {
				st.live, st.held, st.outSince = liveSummary{}, false, time.Time{}
			}
		}
		if d.DeadSurfaces > 0 {
			if st.deadSeen == 0 {
				st.deadSince = now
			}
			st.deadSeen++
			if st.deadSeen < 2 || now.Sub(st.deadSince) < DeadConfirm {
				d.DeadSurfaces = 0
			}
		} else {
			st.deadSince, st.deadSeen = time.Time{}, 0
		}
		d.resolve()
		desired[id] = d
	}
}

// NextDeadline is when the earliest waiting hold ends, counted from its
// first observation out of waiting; zero when no hold is running. The loop
// wakes then, so the pill does not wait for the next tick.
func (dm *Damper) NextDeadline() time.Time {
	var next time.Time
	for _, st := range dm.ws {
		if !st.held || st.outSince.IsZero() {
			continue
		}
		if d := st.outSince.Add(WaitingHold); next.IsZero() || d.Before(next) {
			next = d
		}
	}
	return next
}
