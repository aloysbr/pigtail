package rammon

import (
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// pinEntry is one pin. A pin protects a server from every eviction pigtail
// performs on its own — idle purge, the server cap, battery eviction and
// `stop --idle` — but not from the RAM floor (mains and battery, D9/D11:
// under the floor the machine comes first). Rule D8 makes pins the only way to keep a
// server that is consulted without computing: use is not guessed from the
// network.
//
// A durable pin ("until unpinned") never expires; until is zero for it. A
// timed pin ends at until.
type pinEntry struct {
	until   time.Time
	durable bool
}

// snoozeKey identifies a server in the pin set: workspace and server name, not
// the port. A server restarted by `pigtail dev` keeps its pin — the user
// pinned "this worktree's storybook", not a socket.
func (m *RAMMonitor) snoozeKey(s ServerStat) string { return s.WorkspaceID + ":" + s.Server }

// snooze pins a server until `until` (a timed pin). Replaces any previous pin,
// durable included: the last choice wins.
func (m *RAMMonitor) snooze(s ServerStat, until time.Time) {
	m.setPin(m.snoozeKey(s), pinEntry{until: until})
}

// snoozeDurable pins a server until it is unpinned.
func (m *RAMMonitor) snoozeDurable(s ServerStat) {
	m.setPin(m.snoozeKey(s), pinEntry{durable: true})
}

func (m *RAMMonitor) setPin(key string, e pinEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pins == nil {
		m.pins = map[string]pinEntry{}
	}
	m.pins[key] = e
}

// unsnooze clears a server's pin, whatever its kind. No-op if absent.
func (m *RAMMonitor) unsnooze(s ServerStat) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pins, m.snoozeKey(s))
}

// isSnoozed reports whether a server is pinned at `now`. An expired timed
// entry is dropped so it re-enters the eviction paths; a durable one never
// expires.
func (m *RAMMonitor) isSnoozed(s ServerStat, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.pins[m.snoozeKey(s)]
	if !ok {
		return false
	}
	if e.durable {
		return true
	}
	if !now.Before(e.until) {
		delete(m.pins, m.snoozeKey(s))
		return false
	}
	return true
}

// processPinned reports whether the process behind s is pinned under any of
// its names. A project may declare several servers on one port (monorepo: `dev`
// and `web`): one process wearing two labels, so killing it through the
// unpinned label kills the pinned one too. The port is the process identity,
// as in distinctProcesses; `stats` is the sweep's snapshot the labels are
// looked up in. Every eviction a pin protects from goes through this, never
// through isSnoozed on one label — but not the RAM floor, which ignores pins
// (D9/D11).
func (m *RAMMonitor) processPinned(s ServerStat, stats []ServerStat, now time.Time) bool {
	if m.isSnoozed(s, now) {
		return true
	}
	if s.Port <= 0 {
		return false // no resolved listener: the label stands alone
	}
	for _, o := range stats {
		if o.Port == s.Port && m.snoozeKey(o) != m.snoozeKey(s) && m.isSnoozed(o, now) {
			return true
		}
	}
	return false
}

// pinState returns a server's pin for display: (expiry, false) for a timed
// pin, (zero, true) for a durable one, (zero, false) when not pinned.
func (m *RAMMonitor) pinState(s ServerStat) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.pins[m.snoozeKey(s)]
	return e.until, e.durable
}

// Pin pins (workspaceID, server) for d. Returns false if that server is not
// currently running.
func (m *RAMMonitor) Pin(workspaceID, server string, d time.Duration) bool {
	for _, s := range m.snapshot() {
		if s.WorkspaceID == workspaceID && s.Server == server {
			// m.now(), not time.Now(): SaveState filters expired pins on the
			// monitor's clock, and the two must agree under an injected test clock.
			m.snooze(s, m.now().Add(d))
			m.persist() // a restart within one interval must not lose the pin
			m.logf("rammon: pinned %s:%s for %s", workspaceID, server, d)
			return true
		}
	}
	return false
}

// PinUntilUnpinned pins (workspaceID, server) until Unpin. Returns false if
// that server is not currently running — a pin is set on something the user
// can see, never on a name typed blind.
func (m *RAMMonitor) PinUntilUnpinned(workspaceID, server string) bool {
	for _, s := range m.snapshot() {
		if s.WorkspaceID == workspaceID && s.Server == server {
			m.snoozeDurable(s)
			m.persist()
			m.logf("rammon: pinned %s:%s until unpinned", workspaceID, server)
			return true
		}
	}
	return false
}

// Unpin clears a server's pin, durable or timed (idempotent).
func (m *RAMMonitor) Unpin(workspaceID, server string) {
	m.unsnooze(ServerStat{WorkspaceID: workspaceID, Server: server})
	m.persist() // a restart right after must not resurrect it
}

// prunePins drops the pins of workspaces that are no longer live — the row was
// deleted, or the worktree is gone from disk (`pigtail clean`). A durable pin
// would otherwise sit in the state file forever. Liveness is judged on the
// workspace, never on its configured server names: those fall back to port
// kinds when a pigtail.yaml is momentarily unreadable, and a pin on
// `storybook` must not vanish on a parse error.
func (m *RAMMonitor) prunePins(live []pigtail.Workspace) {
	alive := make(map[string]bool, len(live))
	for _, ws := range live {
		alive[ws.ID] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.pins {
		if ws, _ := splitActivityKey(k); !alive[ws] {
			delete(m.pins, k)
			m.logf("rammon: dropped pin %s (workspace gone)", k)
		}
	}
}
