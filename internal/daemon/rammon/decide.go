// Package rammon evicts forgotten/idle dev servers when system memory
// pressure crosses a floor, and exposes their memory/idle for introspection.
package rammon

import (
	"sort"
	"time"
)

// Action is the monitor's decision for one sweep.
type Action int

const (
	ActionNone  Action = iota // headroom is fine
	ActionEvict               // evict Victim
	ActionWarn                // over budget but nothing safely evictable
)

// ServerStat is a running dev server's resource snapshot.
type ServerStat struct {
	WorkspaceID string
	Server      string
	Branch      string // backend-resolved work-ref label (DisplayRef); never read from BackendData
	Port        int
	PIDs        []int
	MemBytes    uint64 // physical footprint of the server's process tree on darwin (VmRSS on Linux)
	IdleFor     time.Duration
	PinnedUntil time.Time // zero if not pinned; else the snooze expiry
	PinDurable  bool      // pinned "until unpinned": never expires; PinnedUntil is zero then

	// Owner is the session claiming this server, read from its marker file;
	// "" when nobody claims it. Read-only here — the daemon never writes a
	// marker, it only reports one.
	Owner string

	// IdleConfirmed reports whether IdleFor rests on at least one observation
	// made by *this* daemon, as opposed to a value restored from the previous
	// one's state file. Restored values are good enough to report and not good
	// enough to kill on: the silent-death design traded idle continuity for the
	// guarantee that a restart never triggers a wrongful eviction, and
	// persisting state must not buy the first back by giving up the second.
	// Every eviction path funnels through the three deciders below, so the
	// guard lives there once rather than in each killer.
	IdleConfirmed bool
}

// Decision is the outcome of decide().
type Decision struct {
	Action Action
	Victim *ServerStat
}

// decide chooses the action for one sweep. Pure: no I/O.
//
// On mains (onBattery=false) — UNCHANGED behaviour:
//   - available >= floor            → None
//   - available < floor, some idle  → Evict the most-idle (tie: largest memory)
//   - available < floor, none idle  → Warn
//
// On battery (onBattery=true) — this call ignores the floor; Sweep then applies the floor separately (floorStep, D11):
//   - some server IdleFor >= batteryGrace → Evict the most-idle (tie: largest memory)
//   - no idle-enough server               → None (no warn spam)
func decide(servers []ServerStat, available, floor uint64, grace, batteryGrace time.Duration, onBattery bool) Decision {
	if !onBattery && available >= floor {
		return Decision{Action: ActionNone}
	}
	threshold := grace
	if onBattery {
		threshold = batteryGrace
	}
	var victim *ServerStat
	for i := range servers {
		s := &servers[i]
		if !idleEnoughToEvict(*s, threshold) {
			continue
		}
		if victim == nil ||
			s.IdleFor > victim.IdleFor ||
			(s.IdleFor == victim.IdleFor && s.MemBytes > victim.MemBytes) {
			victim = s
		}
	}
	if victim == nil {
		if onBattery {
			return Decision{Action: ActionNone}
		}
		return Decision{Action: ActionWarn}
	}
	return Decision{Action: ActionEvict, Victim: victim}
}

// idlePurgeVictims returns every server idle at least `grace`. It is the
// always-on idle-purge decision: independent of power source and the RAM floor.
// A non-positive grace disables the purge (returns no victims). Pure: no I/O,
// no mutable state.
func idlePurgeVictims(servers []ServerStat, grace time.Duration) []ServerStat {
	if grace <= 0 {
		return nil
	}
	var out []ServerStat
	for i := range servers {
		if idleEnoughToEvict(servers[i], grace) {
			out = append(out, servers[i])
		}
	}
	return out
}

// idleEnoughToEvict is the single gate every eviction decision passes through:
// idle past the threshold, and resting on an observation this daemon made
// itself. Keeping it in one predicate is what makes "a restart cannot evict"
// checkable in one place instead of in each killer.
func idleEnoughToEvict(s ServerStat, threshold time.Duration) bool {
	return s.IdleConfirmed && s.IdleFor >= threshold
}

// distinctProcesses collapses the stats that describe the same running process
// into one representative each, keyed by the port.
//
// A project may declare several servers on the same port_kind — monorepo declares
// `dev` and `web` on the dev port, `storybook` and `ui-styleguide` on the
// storybook port — and snapshot() emits one ServerStat per *declaration* whose
// port is listening. Two such entries carry different names and the very same
// PIDs: one process wearing two labels. Anything that counts a population over
// a sweep counts it twice, and the server cap is where that count decides who
// dies — a repo declaring aliases spends two of the cap's slots per running
// server, so the cap trips while the machine sits well under it and evicts
// servers that were never over any budget.
//
// The port is the identity, not (workspace, server): only one process can
// listen on a port, whichever workspace claims it.
//
// The representative takes the group's *least* idle reading and is confirmed
// only if every label is. A label the traffic never used, or one this daemon
// has not observed yet, reads as more idle than the process actually is; it
// must never be the reason a live process is killed. Pinning is unioned for
// the same reason — protecting one label protects the process. Order of first
// appearance is preserved, so a caller sorting the result stays deterministic.
// Pure: no I/O, no mutable state.
func distinctProcesses(stats []ServerStat) []ServerStat {
	if len(stats) < 2 {
		return stats
	}
	out := make([]ServerStat, 0, len(stats))
	at := make(map[int]int, len(stats))
	for _, s := range stats {
		// A port of zero is not an identity — no listener was resolved — so
		// such an entry stands alone rather than merging with every other one.
		i, seen := at[s.Port]
		if s.Port <= 0 || !seen {
			if s.Port > 0 {
				at[s.Port] = len(out)
			}
			out = append(out, s)
			continue
		}
		rep := &out[i]
		confirmed := rep.IdleConfirmed && s.IdleConfirmed
		pinned := rep.PinnedUntil
		if s.PinnedUntil.After(pinned) {
			pinned = s.PinnedUntil
		}
		durable := rep.PinDurable || s.PinDurable
		if s.IdleFor < rep.IdleFor {
			*rep = s
		}
		rep.IdleConfirmed = confirmed
		rep.PinnedUntil = pinned
		rep.PinDurable = durable
	}
	return out
}

// overCapVictims returns up to n servers to evict — the most-idle first (tie:
// larger memory, matching decide()) — considering only servers idle at least
// minIdle. It returns fewer than n when too few are idle-enough, so an
// actively-used server is never killed to satisfy the cap. A non-positive n
// returns no victims. Pure: no I/O, no mutable state.
func overCapVictims(servers []ServerStat, n int, minIdle time.Duration) []ServerStat {
	if n <= 0 {
		return nil
	}
	var eligible []ServerStat
	for i := range servers {
		if idleEnoughToEvict(servers[i], minIdle) {
			eligible = append(eligible, servers[i])
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].IdleFor != eligible[j].IdleFor {
			return eligible[i].IdleFor > eligible[j].IdleFor
		}
		return eligible[i].MemBytes > eligible[j].MemBytes
	})
	if n > len(eligible) {
		n = len(eligible)
	}
	return eligible[:n]
}
