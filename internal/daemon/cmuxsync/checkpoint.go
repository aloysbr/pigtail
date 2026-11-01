package cmuxsync

import "time"

// pillCheckpoint is the pill pass between two slow cmux calls (D3 b): once
// the pills are due (pillsDue), it runs sync with
// statuses only — no title, no description, no folder — from the listing
// the loop already holds. Run goroutine only, at points where r.workspaces,
// r.allowed and r.applied are whole: before each call of slow(), reconcile()
// and fixClaudeCode(); never inside arrange (a focus read guards each folder
// write there, correction 188). A failure disarms it for the rest of the
// pass: the pass's own sync decides about cmux's availability, as before.
func (r *Runner) pillCheckpoint() {
	if !r.ckptArmed {
		return
	}
	now := r.deps.Now()
	if !r.pillsDue(now) {
		return
	}
	r.ckptArmed = false // no re-entry; re-armed once it went through
	if _, err := r.sync(now, true); err != nil {
		r.deps.Logf("cmuxsync: pill checkpoint: %v (left to the pass)", err)
		return
	}
	r.timing.ckpts++
	r.timing.ckpt += r.deps.Now().Sub(now)
	r.ckptArmed = true
}

// pillsDue is the one predicate of « the pills are due »: a wait changed
// (the flag is consumed here), or FastInterval has passed since the last sync,
// or a waiting hold has ended (holdDeadline reached: its pill must go with it,
// not at the next FastInterval since a sync that came a moment before).
func (r *Runner) pillsDue(now time.Time) bool {
	dirty := r.takeWaitsDirty()
	if d := r.holdDeadline(); !d.IsZero() && !d.After(now) {
		return true
	}
	return dirty || now.Sub(r.lastSync) >= r.cfg.FastInterval
}

// WaitsChanged asks for a pass now: a wait was recorded or cleared
// (session.wait_changed, wait.*), and its pill must wait neither for the
// tick nor, mid-pass, for FastInterval. Unlike Nudge, no cmux listing.
func (r *Runner) WaitsChanged() {
	r.mu.Lock()
	r.waitsDirty = true
	r.mu.Unlock()
	r.poke()
}

func (r *Runner) takeWaitsDirty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.waitsDirty
	r.waitsDirty = false
	return d
}
