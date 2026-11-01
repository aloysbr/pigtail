package sessionlaunch

import (
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// The burst detector (D1, 2026-10-21) replaces --max-live and the alive
// breaker: more than BurstMax launches within BurstWindow reads as a runaway
// and the next one is refused. Measured 3–20 Oct: 144 launches, at most 6 in
// any 10 minutes. Respawns do not count (a respawn replaces); a --relay
// launch does (a chain that relays at once is the runaway to stop).
const (
	BurstWindow = 10 * time.Minute
	BurstMax    = 10
)

// Burst is the launches of the last BurstWindow.
type Burst struct {
	Recent int       // launches inside the window
	Oldest time.Time // the oldest of them (zero when none)
}

// BurstAt counts the launches of entries inside the window ending at now;
// alive tells whether a reserve's owner pid still runs (spawnreg.PendingOpts).
func BurstAt(entries []spawnreg.Entry, now time.Time, alive func(int) bool) Burst {
	var b Burst
	for _, e := range spawnreg.Launches(entries, now.Add(-BurstWindow), now, spawnreg.PendingOpts{Window: spawnreg.PendingWindow, Cap: spawnreg.ReserveCap, Alive: alive}) {
		b.Recent++
		if b.Oldest.IsZero() || e.TS.Before(b.Oldest) {
			b.Oldest = e.TS
		}
	}
	return b
}

// Full: one more launch would make more than BurstMax in the window.
func (b Burst) Full() bool { return b.Recent >= BurstMax }

// FreesAt is when the oldest launch leaves the window.
func (b Burst) FreesAt() time.Time { return b.Oldest.Add(BurstWindow) }
