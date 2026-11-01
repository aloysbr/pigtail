package rammon

import "time"

// defaultIdleWindow is how far back the tracker looks before calling a server
// idle. Per-sweep judgement read 0.01 s of ps quantization as activity (one
// tick over 15 s is 0.067 %) and a resting storybook's ~1 % background as use;
// over five minutes both average out. Also the fallback for a Config literal
// that leaves IdleWindow at zero.
const defaultIdleWindow = 5 * time.Minute

// activitySample is one observation of a server.
type activitySample struct {
	at       time.Time
	cpu      float64 // cumulative CPU-seconds of the listener subtree
	newConns int     // inbound connections first seen at this observation
}

// trimWindow drops the samples the window no longer needs: it keeps the newest
// sample at or before now-window (the anchor, so the span can reach the full
// window) and everything after it. While no sample is that old it keeps them
// all — the window is still filling. The result aliases samples.
func trimWindow(samples []activitySample, now time.Time, window time.Duration) []activitySample {
	cutoff := now.Add(-window)
	anchor := 0
	for i, s := range samples {
		if !s.at.After(cutoff) {
			anchor = i
		}
	}
	return samples[anchor:]
}

// navigationBurst is K, used only when the opt-in network signal is on
// (Config.NetSignal, pigtaild -idle-net-signal): the number of new connections
// one sweep must see on a server's port to count as a page navigation or
// reload. A page load opens many connections at once; a tab polling on its own
// opened at most POLL = 2 per sweep in the main measurement (2026-10-02,
// 18:50-19:20: untouched Arc tabs on monorepo vite :3019 and :3075; an earlier
// spot measure on :3028 gave 3). The signal is off by default (rule D8)
// because the same tabs, sampled at another sweep phase, reached 4, and two
// polling tabs on one server reach it at almost any relative timing. The weakest reload seen opened RELOAD_MIN = 6 (the user's
// Cmd-R on monorepo vite :3019; next dev :3041 7-8, scratch vite with 40 modules
// 7-8, scratch storybook 9-10), so K = max(2, ceil((POLL + RELOAD_MIN) / 2)) = 4.
// Calibrated on those measurements.
const navigationBurst = 4

// K must stay >= 2: with K = 1 any single connection — a polling tab's — would
// count as a navigation. This line fails to compile if it ever drops below.
const _ = uint(navigationBurst - 2)

// windowWork returns, over samples, the CPU-seconds consumed, the wall-clock
// span they cover — the real elapsed time, not a nominal sweep interval — and
// the largest number of new connections seen in any one sweep (max, not sum:
// a polling tab's few connections per sweep must never add up to a navigation).
//
// Each CPU step is clamped at zero: the subtree sum drops when a child exits,
// taking its counter with it, and that drop is not negative work. A
// non-positive span (one sample, or a clock stepped backwards) is no CPU
// evidence at all. Connections are read on every sample but the anchor,
// whose step ended before the window began; they are evidence by themselves,
// whatever the span.
func windowWork(samples []activitySample) (work float64, span time.Duration, maxBurst int) {
	for i := 1; i < len(samples); i++ {
		maxBurst = max(maxBurst, samples[i].newConns)
	}
	if len(samples) < 2 {
		return 0, 0, maxBurst
	}
	span = samples[len(samples)-1].at.Sub(samples[0].at)
	if span <= 0 {
		return 0, 0, maxBurst
	}
	for i := 1; i < len(samples); i++ {
		if d := samples[i].cpu - samples[i-1].cpu; d > 0 {
			work += d
		}
	}
	return work, span, maxBurst
}

// countNewPeers counts the client endpoints in cur that were not in prev. A
// connection that stays open (an open tab's HMR websocket) is in both and does
// not count; a reload tears it down and opens a new one, which does.
func countNewPeers(prev, cur []string) int {
	seen := make(map[string]bool, len(prev))
	for _, p := range prev {
		seen[p] = true
	}
	n := 0
	for _, c := range cur {
		if !seen[c] {
			n++
		}
	}
	return n
}
