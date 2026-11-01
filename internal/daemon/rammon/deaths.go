package rammon

import (
	"sort"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Death is a dev server that vanished without anyone announcing a stop — a
// crash, an OOM kill, a kill from outside pigtail. Until the activity tracker
// gave the daemon a memory from one sweep to the next, both of its loops just
// skipped a free port, so "died" and "never started" were the same picture.
//
// One death is one process. A project may list that process under several
// names sharing a port (monorepo: `dev` and `web`), and Servers carries all of
// them, sorted. Server is Servers[0], kept for the clients that only read it.
type Death struct {
	WorkspaceID string
	Server      string
	Servers     []string
	Port        int
	At          time.Time
}

// deathWindow bounds how long a death stays reportable.
const deathWindow = 30 * time.Minute

// maxDeaths bounds the ring so a flapping server cannot grow it without limit.
// The window alone is not enough: a server that dies and restarts every sweep
// would add a hundred entries inside it.
const maxDeaths = 64

// intentionalStopTTL is how long a "this stop was deliberate" mark stays valid.
// Two sweep intervals, floored at a minute: a Next.js server can take several
// seconds to release its port after SIGTERM, and a mark that expired first
// would turn every clean stop into a reported death.
func (m *RAMMonitor) intentionalStopTTL() time.Duration {
	m.mu.Lock()
	interval := m.cfg.Interval
	m.mu.Unlock()
	if d := 2 * interval; d > time.Minute {
		return d
	}
	return time.Minute
}

// MarkIntentionalStop records that (workspaceID, server) is being stopped on
// purpose, so its disappearance is not reported as a death. Every daemon stop
// path calls it, and the CLI announces its own stops through
// POST /servers/stopped. A missed call costs one false death; a spurious call
// costs one missed death — hence the deliberately short TTL.
//
// Stops kill by port, so the mark covers every tracked name last seen on the
// same port of the same workspace: stopping `dev` kills the process `web` is
// listed under too, and the twin must not come back as a death.
func (m *RAMMonitor) MarkIntentionalStop(workspaceID, server string) {
	key := activityKey(workspaceID, server)
	m.markIntentionalStop(key)
	port := m.activity.portFor(key)
	if port <= 0 {
		return
	}
	for _, twin := range m.activity.keysOnPort(workspaceID, port) {
		if twin != key {
			m.markIntentionalStop(twin)
		}
	}
}

// MarkIntentionalStopsForWorkspace marks every tracked server of a workspace.
// Used by stop paths that act on a whole worktree without naming its servers
// — the watchdog's runaway killer goes through devserver.Stop(ctx, wt, "").
func (m *RAMMonitor) MarkIntentionalStopsForWorkspace(workspaceID string) {
	prefix := workspaceID + ":"
	for _, key := range m.activity.keys() {
		if strings.HasPrefix(key, prefix) {
			m.markIntentionalStop(key)
		}
	}
}

// markIntentionalStop stamps a mark keyed exactly like the tracker, so death
// detection can look it up with the key it already holds.
func (m *RAMMonitor) markIntentionalStop(key string) {
	expiry := m.now().Add(m.intentionalStopTTL()) // both take m.mu — before the lock
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.intentional == nil {
		m.intentional = map[string]time.Time{}
	}
	m.intentional[key] = expiry
}

// consumeIntentionalStop reports whether a still-valid deliberate-stop mark
// covers this key, and drops it either way: a mark covers one shutdown, not
// the rest of the server's life.
func (m *RAMMonitor) consumeIntentionalStop(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	expiry, ok := m.intentional[key]
	if !ok {
		return false
	}
	delete(m.intentional, key)
	return now.Before(expiry)
}

// purgeIntentionalStops drops expired marks. Marks are normally consumed by
// the disappearance they cover; this collects the ones for stops that never
// took (the server survived), so the map stays bounded.
func (m *RAMMonitor) purgeIntentionalStops(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, expiry := range m.intentional {
		if !now.Before(expiry) {
			delete(m.intentional, key)
		}
	}
}

// recordDeath appends to the bounded ring, dropping the oldest entries when
// it overflows.
func (m *RAMMonitor) recordDeath(d Death) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deaths = append(m.deaths, d)
	if len(m.deaths) > maxDeaths {
		m.deaths = append(m.deaths[:0], m.deaths[len(m.deaths)-maxDeaths:]...)
	}
}

// RecentDeaths returns the deaths inside the reporting window, oldest first,
// and purges the ones that fell out of it on the way.
func (m *RAMMonitor) RecentDeaths(now time.Time) []Death {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.deaths[:0]
	for _, d := range m.deaths {
		if now.Sub(d.At) < deathWindow {
			kept = append(kept, d)
		}
	}
	m.deaths = kept
	out := make([]Death, len(kept))
	copy(out, kept)
	return out
}

// LastSweepAt is when an observation last judged which servers died: a
// server gone before it and absent from RecentDeaths was stopped on purpose;
// one gone after it is not judged yet. Zero until the first sweep (and for
// ever when the monitor is switched off: then nothing is ever judged).
func (m *RAMMonitor) LastSweepAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastSweep
}

// publishDeath announces a death on the bus so live consumers (TUI, menu bar)
// see it as it happens, not only when they next poll GET /servers.
func (m *RAMMonitor) publishDeath(d Death) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(pigtail.Event{
		Type: pigtail.EventServerDied,
		Data: map[string]interface{}{
			"workspace_id": d.WorkspaceID,
			"server":       d.Server,
			"servers":      d.Servers,
			"port":         d.Port,
			"at_unix":      d.At.Unix(),
		},
	})
}

// splitActivityKey is the inverse of activityKey. Workspace IDs are opaque
// colon-free identifiers, which the key scheme (and snoozeKey before it)
// already assumes.
func splitActivityKey(key string) (workspaceID, server string) {
	ws, srv, found := strings.Cut(key, ":")
	if !found {
		return key, ""
	}
	return ws, srv
}

// noteDeath records, publishes and logs one death. Single funnel so every
// death leaves the same three traces.
func (m *RAMMonitor) noteDeath(d Death) {
	m.recordDeath(d)
	m.publishDeath(d)
	m.logf("rammon: %s:%s (:%d) died on its own — no stop was announced",
		d.WorkspaceID, deathLabel(d), d.Port)
}

// deathLabel names every server a death covers, joined as `dev+web`. A death
// restored from a state file written before Servers existed falls back to
// Server.
func deathLabel(d Death) string {
	if len(d.Servers) == 0 {
		return d.Server
	}
	return strings.Join(d.Servers, "+")
}

// groupByPort partitions activity keys into one group per (workspace, port):
// the names one process was listed under. Names are sorted inside a group and
// groups are ordered by their first key, so what the detector logs is stable.
// A key whose port is unknown (0) stands alone — zero is not an identity, and
// merging on it would fold unrelated servers into one death. Pure.
func groupByPort(keys []string, portFor func(string) int) [][]string {
	if len(keys) == 0 {
		return nil
	}
	type slot struct {
		ws   string
		port int
	}
	at := map[slot]int{}
	var groups [][]string
	for _, key := range keys {
		ws, _ := splitActivityKey(key)
		port := portFor(key)
		if port > 0 {
			if i, ok := at[slot{ws, port}]; ok {
				groups[i] = append(groups[i], key)
				continue
			}
			at[slot{ws, port}] = len(groups)
		}
		groups = append(groups, []string{key})
	}
	for _, g := range groups {
		sort.Strings(g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}
