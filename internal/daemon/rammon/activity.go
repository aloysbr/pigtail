package rammon

import (
	"sync"
	"time"
)

// isActive reports whether a process subtree consumed enough CPU over
// interval — the wall-clock span the work was measured over — to count as in
// use. deltaCPU is in CPU-seconds;
// thresholdPercent is a percentage of one core (2.0 = 2% of one core).
// A non-positive interval reads as idle: with no elapsed time there is no
// rate to compare, and inventing one would risk killing a live server.
func isActive(deltaCPU float64, interval time.Duration, thresholdPercent float64) bool {
	if interval <= 0 {
		return false
	}
	if deltaCPU <= 0 {
		return false
	}
	percent := deltaCPU / interval.Seconds() * 100
	return percent >= thresholdPercent
}

// activityEntry is one server's CPU accounting across sweeps.
type activityEntry struct {
	lastCPU float64 // cumulative subtree CPU seconds at the last observation
	// lastActive is the last observation at which the server counted as in
	// use: the window still filling (for an entry this daemon first saw), a
	// mean CPU at or above the threshold, or — with the opt-in network
	// signal — a navigation burst inside the window. IdleFor counts from here.
	lastActive time.Time
	// samples is the history the verdict is drawn from: everything within the
	// window plus one anchor at or before its start. Restarted whenever
	// lastCPU is (first sighting, new listeners).
	samples []activitySample
	// peers is the set of client endpoints connected to the server at the last
	// observation whose connection inventory succeeded; peersKnown says
	// whether there has been one. Only a peer absent from this set is a new
	// connection — an open tab's websocket stays in it and never counts.
	peers      []string
	peersKnown bool
	// fresh marks an entry born from this daemon's own first sighting or
	// rebase. Its window, until full, counts as active: nothing is idle on
	// less than one window of evidence. A restored entry is not fresh: its
	// lastActive is the previous daemon's verdict, which stands while the
	// window refills unless the partial window shows activity. Persisted, so
	// a redeploy in the middle of a fill does not end the fill.
	fresh bool
	// port is the port the server was listening on at the last observation.
	// It lives here, on the very entry whose disappearance defines a death,
	// because by the time a death is noticed the port is free and no longer
	// discoverable. A parallel key→port map would be a second lifetime to keep
	// in step with forget(); one entry cannot disagree with itself.
	port int
	// pids is the listener set at the last observation. It answers the one
	// question the CPU figure cannot: is this the same process? lastCPU is a
	// *cumulative* counter, so a restarted server reads as a large negative
	// delta — indistinguishable from "went quiet" unless the pids are compared.
	// Recorded at every observation, not only on a restored entry: a server
	// restarted between two sweeps of a running daemon falls into the same trap
	// as one restarted while the daemon was down, and it is also what makes the
	// set persisted for the next daemon non-empty.
	pids []int
	// restored marks an entry read from the previous daemon's state file and
	// not yet observed by this one. Such an entry is reported but never acted
	// on; see ServerStat.IdleConfirmed.
	restored bool
}

// activityTracker derives per-server idleness from the CPU consumed between
// sweeps. It replaces the previous log-mtime heuristic, which read a file only
// the background-daemon launch path ever writes — so a cmux sidecar was either
// immortal (no log: idle 0 forever) or killed on sight (stale log: idle read as
// days). CPU is written by whoever runs the server, so both paths agree.
type activityTracker struct {
	mu      sync.Mutex
	entries map[string]activityEntry
}

func newActivityTracker() *activityTracker {
	return &activityTracker{entries: map[string]activityEntry{}}
}

// observation is what one sweep knows about one server.
type observation struct {
	port       int
	pids       []int
	cpu        float64  // cumulative subtree CPU-seconds
	peers      []string // client endpoints of accepted connections
	peersKnown bool     // false when the connection inventory was unavailable
	// netOff says the network signal is off: there is no inventory at all,
	// so the peer baseline is dropped rather than kept. A baseline kept
	// through an off period would be stale when the signal comes back on,
	// and every connection opened meanwhile would read as new.
	netOff bool
}

// observe is observeWith without connection evidence: CPU only.
func (t *activityTracker) observe(key string, port int, pids []int, cpu float64, now time.Time, window time.Duration, thresholdPercent float64) {
	t.observeWith(key, observation{port: port, pids: pids, cpu: cpu}, now, window, thresholdPercent)
}

// observeWith records one sweep for a server and judges it over the last
// `window` of samples: in use if the mean CPU rate over the window (real
// elapsed time) reaches the threshold, or if one sweep inside it saw a
// navigation burst — navigationBurst new connections at once. Connection
// evidence only arrives when Config.NetSignal is on; by default (rule D8)
// every observation is CPU-only.
//
// The first sighting counts as active, and so does every observation of a
// fresh entry until the samples span a full window: a server we have never
// measured has no idle history, and assuming it idle would evict it on sight.
// A window always fills: the first sample stays as anchor until a newer one is
// window-old, so one observation after a long gap completes it.
//
// A server whose listeners changed since the last observation is a new process
// wearing an old key, and is treated as that same first sighting: its CPU
// counter restarted with it, so keeping the previous figure would produce a
// negative delta that cannot be told from "went quiet" — the entry would carry
// the dead process's lastActive and go on ageing while the new one runs. Its
// connections belong to the new process too.
//
// window <= 0 means defaultIdleWindow: a Config literal that never set it must
// not mean "judge on nothing".
func (t *activityTracker) observeWith(key string, o observation, now time.Time, window time.Duration, thresholdPercent float64) {
	if window <= 0 {
		window = defaultIdleWindow
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, seen := t.entries[key]
	if !seen || prev.processChanged(o.pids) {
		t.entries[key] = activityEntry{
			lastCPU: o.cpu, lastActive: now, port: o.port, pids: clonePIDs(o.pids),
			samples: []activitySample{{at: now, cpu: o.cpu}},
			peers:   clonePeers(o.peers), peersKnown: o.peersKnown,
			fresh: true,
		}
		return
	}

	peers, peersKnown, newConns := prev.peers, prev.peersKnown, 0
	switch {
	case o.netOff:
		peers, peersKnown = nil, false
	case o.peersKnown:
		if prev.peersKnown {
			newConns = countNewPeers(prev.peers, o.peers)
		}
		peers, peersKnown = clonePeers(o.peers), true
	}

	// A fresh slice every time: the ring must never share a backing array
	// with a snapshot being written to disk.
	samples := make([]activitySample, 0, len(prev.samples)+1)
	samples = append(samples, prev.samples...)
	samples = append(samples, activitySample{at: now, cpu: o.cpu, newConns: newConns})
	samples = trimWindow(samples, now, window)

	e := activityEntry{
		lastCPU: o.cpu, lastActive: prev.lastActive, port: o.port, pids: clonePIDs(o.pids),
		samples: samples, peers: peers, peersKnown: peersKnown, fresh: prev.fresh,
	}
	work, span, burst := windowWork(samples)
	filling := prev.fresh && span < window
	if filling || burst >= navigationBurst || isActive(work, span, thresholdPercent) {
		e.lastActive = now
	}
	// restored deliberately not carried over: this observation is the fresh
	// look that the mark was waiting for.
	t.entries[key] = e
}

// clonePeers detaches the peer set from the caller's slice.
func clonePeers(p []string) []string {
	if len(p) == 0 {
		return nil
	}
	return append([]string(nil), p...)
}

// processChanged reports whether the listeners seen now contradict the ones
// recorded at the last observation.
//
// No pids to compare with means no evidence, so nothing changes: an observation
// made without a listener set (a caller that has none) must not reset a counter
// on a guess. An entry holding no pids is the other way round — a state file
// written before observations recorded them, whose CPU figure belongs to a
// process nobody can vouch for. Distrusting it costs one idle counter reset and
// keeps the guarantee that a restart cannot evict.
func (e activityEntry) processChanged(pids []int) bool {
	if len(pids) == 0 {
		return false
	}
	return !samePIDs(e.pids, pids)
}

// clonePIDs detaches the listener set from the caller's slice: the tracker
// keeps it until the next observation, the prober is free to reuse its buffer.
func clonePIDs(pids []int) []int {
	if len(pids) == 0 {
		return nil
	}
	out := make([]int, len(pids))
	copy(out, pids)
	return out
}

// restore seeds the tracker from the previous daemon's state file. Every entry
// lands marked restored, so its idle time is reported from the first sweep
// while no eviction can rest on it until this daemon has observed the server
// itself. Only called before the first sweep.
func (t *activityTracker) restore(entries map[string]activityEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range entries {
		e.restored = true
		t.entries[k] = e
	}
}

// confirmed reports whether this daemon has observed the server itself, so a
// decision may rest on its idle time. An unknown key is confirmed: nothing was
// restored for it, so there is nothing to distrust — which is what keeps every
// path that never persisted anything behaving exactly as before.
func (t *activityTracker) confirmed(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	return !ok || !e.restored
}

// entriesSnapshot copies the tracked state out for persistence.
func (t *activityTracker) entriesSnapshot() map[string]activityEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]activityEntry, len(t.entries))
	for k, e := range t.entries {
		e.samples = append([]activitySample(nil), e.samples...)
		e.peers = clonePeers(e.peers)
		out[k] = e
	}
	return out
}

// portFor returns the port a server was last seen listening on, or 0 for an
// unknown key. Read after the fact, when the port itself is already free.
func (t *activityTracker) portFor(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.entries[key].port
}

// idleFor reports how long a server has gone without measurable activity.
// An unknown key reads as 0 — never evict on uncertainty.
func (t *activityTracker) idleFor(key string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return 0
	}
	if d := now.Sub(e.lastActive); d > 0 {
		return d
	}
	return 0
}

// forget drops a server's state once it is gone, bounding the map.
func (t *activityTracker) forget(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// keysOnPort returns the tracked keys of one workspace last seen on port: the
// names a single process is listed under when a project declares several
// servers on one port kind.
func (t *activityTracker) keysOnPort(workspaceID string, port int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for k, e := range t.entries {
		if ws, _ := splitActivityKey(k); ws == workspaceID && e.port == port {
			out = append(out, k)
		}
	}
	return out
}

// keys returns the currently tracked server keys.
func (t *activityTracker) keys() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.entries))
	for k := range t.entries {
		out = append(out, k)
	}
	return out
}

// samePIDs reports whether two listener sets describe the same process group.
// Order is not significant: lsof's output order is not a contract.
func samePIDs(a, b []int) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	seen := make(map[int]int, len(a))
	for _, p := range a {
		seen[p]++
	}
	for _, p := range b {
		seen[p]--
		if seen[p] < 0 {
			return false
		}
	}
	return true
}
