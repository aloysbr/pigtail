package main

import (
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/internal/sessionlaunch"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// The burst detector (D1, 2026-10-21) lives in internal/sessionlaunch,
// shared with pigtaild's launcher; the refusal's wording stays here.
const (
	spawnBurstWindow = sessionlaunch.BurstWindow
	spawnBurstMax    = sessionlaunch.BurstMax
)

// spawnBurst is sessionlaunch.Burst with the CLI's wording.
type spawnBurst sessionlaunch.Burst

func burstAt(entries []spawnreg.Entry, now time.Time) spawnBurst {
	return spawnBurst(sessionlaunch.BurstAt(entries, now, pidAlive))
}

// full: one more launch would make more than spawnBurstMax in the window.
func (b spawnBurst) full() bool { return sessionlaunch.Burst(b).Full() }

// freesIn is when the oldest launch leaves the window, rounded up, at least 1 s.
func (b spawnBurst) freesIn(now time.Time) time.Duration {
	d := sessionlaunch.Burst(b).FreesAt().Sub(now)
	if r := d % time.Second; r != 0 {
		d += time.Second - r
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}

func burstWindowText() string { return fmt.Sprintf("%d min", int(spawnBurstWindow/time.Minute)) }

// line is the dry-run's line, after "launches  ".
func (b spawnBurst) line() string {
	return fmt.Sprintf("%d in the last %s — the %dth within %s is refused (burst detector)", b.Recent, burstWindowText(), spawnBurstMax+1, burstWindowText())
}

// refusal is the burst refusal: a rule refusal (exit 1), never retryable; freesIn only feeds the message.
func (b spawnBurst) refusal(now time.Time) error {
	in := b.freesIn(now)
	return spawnErr("%d sessions launched in the last %s — more than %d in %s reads as a runaway, this launch is refused; nothing launched. Respawns do not count. Not a queue and not retried: nothing relaunches it — if this burst is intended, relaunch by hand once the oldest leaves the window (in %s), or ask the user",
		b.Recent, burstWindowText(), spawnBurstMax, burstWindowText(), in)
}
