package rammon

import (
	"sort"
	"strconv"
	"testing"
	"time"
)

func TestIsActive(t *testing.T) {
	const interval = 15 * time.Second
	cases := []struct {
		name      string
		delta     float64 // CPU seconds consumed over the interval
		threshold float64 // percent of one core
		want      bool
	}{
		// 15s of wall clock, 0.30s of CPU = 2% of one core.
		{"exactly at threshold is active", 0.30, 2.0, true},
		{"just under threshold is idle", 0.29, 2.0, false},
		{"well over threshold is active", 3.0, 2.0, true},
		{"no CPU consumed is idle", 0, 2.0, false},
		{"negative delta (counter reset) is idle", -1, 2.0, false},
		{"zero threshold makes any consumption active", 0.001, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isActive(tc.delta, interval, tc.threshold); got != tc.want {
				t.Fatalf("isActive(%v, %v, %v) = %v, want %v", tc.delta, interval, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestIsActiveZeroIntervalIsIdle(t *testing.T) {
	// Guard against a division by zero producing +Inf and a spurious "active".
	if isActive(1, 0, 2.0) {
		t.Fatal("zero interval must read as idle, not active")
	}
}

func TestActivityTrackerFirstObservationIsNotIdle(t *testing.T) {
	tr := newActivityTracker()
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 10.0, now, 15*time.Second, 2.0)
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("first observation: idle %v, want 0", got)
	}
}

func TestActivityTrackerUnknownKeyIsNotIdle(t *testing.T) {
	tr := newActivityTracker()
	if got := tr.idleFor("nope:dev", time.Unix(1000, 0)); got != 0 {
		t.Fatalf("unknown key: idle %v, want 0", got)
	}
}

func TestActivityTrackerAccumulatesIdleWhileQuiet(t *testing.T) {
	tr := newActivityTracker()
	const interval = 15 * time.Second
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 10.0, now, interval, 2.0) // first sighting
	for i := 1; i <= 4; i++ {
		now = now.Add(interval)
		tr.observe("ws1:dev", 3030, []int{100}, 10.0, now, interval, 2.0) // no CPU consumed
	}
	if got, want := tr.idleFor("ws1:dev", now), 4*interval; got != want {
		t.Fatalf("idle after 4 quiet sweeps: got %v, want %v", got, want)
	}
}

func TestActivityTrackerResetsIdleOnActivity(t *testing.T) {
	tr := newActivityTracker()
	const interval = 15 * time.Second
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 10.0, now, interval, 2.0)
	now = now.Add(interval)
	tr.observe("ws1:dev", 3030, []int{100}, 10.0, now, interval, 2.0) // quiet
	now = now.Add(interval)
	tr.observe("ws1:dev", 3030, []int{100}, 13.0, now, interval, 2.0) // 3 CPU-seconds → active
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle right after activity: got %v, want 0", got)
	}
}

func TestActivityTrackerForgetDropsState(t *testing.T) {
	tr := newActivityTracker()
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 10.0, now, 15*time.Second, 2.0)
	tr.forget("ws1:dev")
	if keys := tr.keys(); len(keys) != 0 {
		t.Fatalf("after forget: keys %v, want none", keys)
	}
}

// TestActivityTrackerRemembersThePort: the port is captured at observation
// time because it is unrecoverable afterwards — once a server dies its port is
// free and nothing on the machine can say what it was.
func TestActivityTrackerRemembersThePort(t *testing.T) {
	tr := newActivityTracker()
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 10.0, now, 15*time.Second, 2.0)
	tr.observe("ws1:dev", 3030, []int{100}, 11.0, now.Add(15*time.Second), 15*time.Second, 2.0)
	if got := tr.portFor("ws1:dev"); got != 3030 {
		t.Fatalf("portFor = %d, want 3030", got)
	}
	if got := tr.portFor("nope:dev"); got != 0 {
		t.Fatalf("portFor of an unknown key = %d, want 0", got)
	}
}

func TestActivityTrackerKeysListsObserved(t *testing.T) {
	tr := newActivityTracker()
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 1, now, 15*time.Second, 2.0)
	tr.observe("ws2:storybook", 6006, []int{200}, 1, now, 15*time.Second, 2.0)
	keys := tr.keys()
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "ws1:dev" || keys[1] != "ws2:storybook" {
		t.Fatalf("keys = %v", keys)
	}
}

// --- process identity, checked at every observation ---

// The counter is cumulative, so a restarted server reports far less CPU than
// its predecessor. Read as a delta that is merely negative, that is "went
// quiet"; read next to the listener set, it is a different process, and the
// entry must start over instead of ageing on a dead process's timestamp.
func TestObserveRebasesWhenTheListenersChanged(t *testing.T) {
	tr := newActivityTracker()
	const interval = 15 * time.Second
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 500, now, interval, 2.0)
	for i := 0; i < 4; i++ {
		now = now.Add(interval)
		tr.observe("ws1:dev", 3030, []int{100}, 500, now, interval, 2.0) // quiet
	}
	if got, want := tr.idleFor("ws1:dev", now), 4*interval; got != want {
		t.Fatalf("idle before the restart = %v, want %v", got, want)
	}

	now = now.Add(interval)
	tr.observe("ws1:dev", 3030, []int{999}, 0.2, now, interval, 2.0) // restarted
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle after a restart = %v, want 0", got)
	}
	// Rebased, not merely reset: the next quiet sweep must age normally rather
	// than read the new counter against the old one.
	now = now.Add(interval)
	tr.observe("ws1:dev", 3030, []int{999}, 0.2, now, interval, 2.0)
	if got := tr.idleFor("ws1:dev", now); got != interval {
		t.Fatalf("idle one quiet sweep after the restart = %v, want %v", got, interval)
	}
}

// The control: a stable listener set must not lose the idle it accumulated,
// which is what the whole idle purge rests on.
func TestObserveKeepsIdleWhileTheListenersAreStable(t *testing.T) {
	tr := newActivityTracker()
	const interval = 15 * time.Second
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100, 101}, 10, now, interval, 2.0)
	for i := 0; i < 3; i++ {
		now = now.Add(interval)
		// Same set, reported in another order: lsof's output order is not a
		// contract, and reading it as a restart would make idle unaccumulable.
		tr.observe("ws1:dev", 3030, []int{101, 100}, 10, now, interval, 2.0)
	}
	if got, want := tr.idleFor("ws1:dev", now), 3*interval; got != want {
		t.Fatalf("idle after 3 quiet sweeps under stable PIDs = %v, want %v", got, want)
	}
}

// An observation with no listener set is not evidence of anything: the entry
// keeps ageing rather than being reset on a guess.
func TestObserveWithoutPIDsIsNotReadAsARestart(t *testing.T) {
	tr := newActivityTracker()
	const interval = 15 * time.Second
	now := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 10, now, interval, 2.0)
	now = now.Add(interval)
	tr.observe("ws1:dev", 3030, nil, 10, now, interval, 2.0)
	if got := tr.idleFor("ws1:dev", now); got != interval {
		t.Fatalf("idle after a pid-less observation = %v, want %v", got, interval)
	}
}

// The pids are recorded, not just compared: they are what the next daemon reads
// out of the state file to run the same comparison across a restart.
func TestObserveRecordsTheListenersItSaw(t *testing.T) {
	tr := newActivityTracker()
	pids := []int{100, 101}
	tr.observe("ws1:dev", 3030, pids, 10, time.Unix(1000, 0), 15*time.Second, 2.0)
	got := tr.entriesSnapshot()["ws1:dev"].pids
	if len(got) != 2 || got[0] != 100 || got[1] != 101 {
		t.Fatalf("recorded pids = %v, want [100 101]", got)
	}
	// Detached from the caller's slice: the prober may reuse its buffer.
	pids[0] = 42
	if tr.entriesSnapshot()["ws1:dev"].pids[0] != 100 {
		t.Fatal("the tracker must not alias the caller's slice")
	}
}

// --- restored state: continuity of the reading, without its authority ---

func TestRestoredEntryKeepsItsIdleButIsUnconfirmed(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	tr := newActivityTracker()
	tr.restore(map[string]activityEntry{
		"ws1:dev": {lastCPU: 42.0, lastActive: now.Add(-90 * time.Minute), port: 3030, pids: []int{100}},
	})

	// The reading survives — this is the whole point: a routine daemon redeploy
	// no longer resets every idle counter to a few seconds.
	if got := tr.idleFor("ws1:dev", now); got != 90*time.Minute {
		t.Fatalf("idleFor after restore = %s, want 90m", got)
	}
	// But it carries no authority until this daemon has looked itself.
	if tr.confirmed("ws1:dev") {
		t.Fatal("a restored entry must not be confirmed before any observation")
	}
}

func TestObservationConfirmsARestoredEntry(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	tr := newActivityTracker()
	tr.restore(map[string]activityEntry{
		"ws1:dev": {lastCPU: 42.0, lastActive: now.Add(-90 * time.Minute), port: 3030, pids: []int{100}},
	})

	// Same process, no CPU burnt: still idle, and now confirmed.
	tr.observe("ws1:dev", 3030, []int{100}, 42.0, now, 15*time.Second, 2.0)

	if !tr.confirmed("ws1:dev") {
		t.Fatal("an observed entry must be confirmed")
	}
	if got := tr.idleFor("ws1:dev", now); got != 90*time.Minute {
		t.Fatalf("idleFor = %s, want the restored 90m to carry through", got)
	}
}

func TestUnknownKeyIsConfirmed(t *testing.T) {
	// Nothing was restored, so there is nothing to distrust. This keeps every
	// path that never persisted anything — and every test injecting its own
	// idle seam — behaving exactly as before.
	if !newActivityTracker().confirmed("ws1:dev") {
		t.Fatal("an unknown key must be confirmed, not treated as restored")
	}
}

// A restored entry whose recorded listeners no longer match what is on the port
// is the previous daemon's CPU figure attached to a process that is gone: the
// observation rebases it rather than reading the drop as silence.
func TestRestoredEntryUnderNewPIDsIsRebased(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tr := newActivityTracker()
	tr.restore(map[string]activityEntry{
		"ws1:dev": {lastCPU: 500, lastActive: now.Add(-2 * time.Hour), port: 3030, pids: []int{100}},
	})
	tr.observe("ws1:dev", 3030, []int{999}, 0.2, now, 15*time.Second, 2.0)
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idleFor after a PID change = %s, want 0", got)
	}
}

// A state file written before observations recorded their listeners carries no
// identity at all. Nothing vouches for its CPU figure, so the first observation
// starts over — the behaviour PR #60 had, kept for the files it wrote.
func TestRestoredEntryWithoutPIDsIsRebased(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tr := newActivityTracker()
	tr.restore(map[string]activityEntry{
		"ws1:dev": {lastCPU: 500, lastActive: now.Add(-2 * time.Hour), port: 3030},
	})
	tr.observe("ws1:dev", 3030, []int{100}, 0.2, now, 15*time.Second, 2.0)
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idleFor after observing an entry of unknown identity = %s, want 0", got)
	}
}

// A restored entry that has already been observed is a normal entry: its pids
// are no longer a guard, because the guard fired (or passed) at that observation.
func TestObserveClearsTheRestoredMark(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	tr := newActivityTracker()
	tr.restore(map[string]activityEntry{"ws1:dev": {lastCPU: 1, port: 3030, pids: []int{100}}})
	tr.observe("ws1:dev", 3030, []int{100}, 1, now, 15*time.Second, 2.0)
	if !tr.confirmed("ws1:dev") {
		t.Fatal("restored mark must be cleared by an observation")
	}
}

func TestNavigationBurstIsAboveOne(t *testing.T) {
	if navigationBurst < 2 {
		t.Fatalf("navigationBurst = %d: with K < 2 a single polling connection reads as a navigation", navigationBurst)
	}
}

// observeEvery feeds CPU-only observations, one per step, cumulative CPU cpus[i].
func observeEvery(tr *activityTracker, t0 time.Time, step, window time.Duration, threshold float64, cpus ...float64) time.Time {
	now := t0
	for i, c := range cpus {
		now = t0.Add(time.Duration(i) * step)
		tr.observe("ws1:dev", 3030, []int{100}, c, now, window, threshold)
	}
	return now
}

// observePeersEvery feeds quiet observations (constant CPU) with a known peer
// set peers[i] at each step.
func observePeersEvery(tr *activityTracker, t0 time.Time, step, window time.Duration, peers ...[]string) time.Time {
	now := t0
	for i, p := range peers {
		now = t0.Add(time.Duration(i) * step)
		tr.observeWith("ws1:dev", observation{port: 3030, pids: []int{100}, cpu: 10, peers: p, peersKnown: true}, now, window, 2.0)
	}
	return now
}

// endpoints returns n distinct client endpoints starting at port from.
func endpoints(from, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "127.0.0.1:" + strconv.Itoa(from+i)
	}
	return out
}

func repeatPeers(p []string, n int) [][]string {
	out := make([][]string, n)
	for i := range out {
		out[i] = p
	}
	return out
}

func TestObserveWindowNotFullCountsAsActive(t *testing.T) {
	tr := newActivityTracker()
	now := observeEvery(tr, time.Unix(1000, 0), 16*time.Second, 5*time.Minute, 2.0, 10, 10, 10)
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle after 32 s of a 5-min window = %s, want 0", got)
	}
}

// The 2026-10-01 bug: a resting storybook burns ~1 % of a core, above the old
// per-sweep threshold every sweep. Over the window it is idle.
func TestObserveRestingTrickleBecomesIdleOnceTheWindowIsFull(t *testing.T) {
	tr := newActivityTracker()
	cpus := make([]float64, 41) // 640 s at 16 s
	for i := range cpus {
		cpus[i] = 10 + 0.2*float64(i) // 0.2 CPU-s per 16 s = 1.25 %
	}
	now := observeEvery(tr, time.Unix(1000, 0), 16*time.Second, 5*time.Minute, 2.0, cpus...)
	// Not full until i=19 (304 s): the last "filling" observation is i=18
	// (288 s); 1.25 % < 2 % from then on: idle = 640 - 288 = 352 s.
	if got, want := tr.idleFor("ws1:dev", now), 352*time.Second; got != want {
		t.Fatalf("idle = %s, want %s", got, want)
	}
}

func TestObserveBurstKeepsActiveWhileInWindow(t *testing.T) {
	tr := newActivityTracker()
	cpus := make([]float64, 0, 33)
	for i := 0; i < 25; i++ { // fill quietly (0..384 s)
		cpus = append(cpus, 10)
	}
	for i := 0; i < 8; i++ { // a 9 CPU-s compile at 400 s (9/300 = 3 %), then quiet to 512 s
		cpus = append(cpus, 19)
	}
	now := observeEvery(tr, time.Unix(1000, 0), 16*time.Second, 5*time.Minute, 2.0, cpus...)
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle 112 s after a 9 CPU-s burst = %s, want 0 (still in the window)", got)
	}
}

// A1: 0.9 CPU-s over 60 s is 1.5 % — idle at 2 %. Divided by 15 s it would read 6 %.
func TestObserveDividesByElapsedTime(t *testing.T) {
	tr := newActivityTracker()
	t0 := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 10, t0, 15*time.Second, 2.0)
	now := t0.Add(60 * time.Second)
	tr.observe("ws1:dev", 3030, []int{100}, 10.9, now, 15*time.Second, 2.0)
	if got := tr.idleFor("ws1:dev", now); got != 60*time.Second {
		t.Fatalf("idle = %s, want 60s", got)
	}
}

func TestObserveChildExitIsNotNegativeWork(t *testing.T) {
	tr := newActivityTracker()
	cpus := make([]float64, 0, 23)
	for i := 0; i < 20; i++ { // fill quietly (0..304 s)
		cpus = append(cpus, 10)
	}
	cpus = append(cpus, 19, 12, 12) // +9 in a worker, then the worker exits
	now := observeEvery(tr, time.Unix(1000, 0), 16*time.Second, 5*time.Minute, 2.0, cpus...)
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle = %s, want 0: the worker's 9 CPU-s are still in the window", got)
	}
}

// Never blocks: one observation after a long gap fills the window at once.
func TestObserveFillsAfterAGap(t *testing.T) {
	tr := newActivityTracker()
	t0 := time.Unix(1000, 0)
	tr.observe("ws1:dev", 3030, []int{100}, 10, t0, 5*time.Minute, 2.0)
	now := t0.Add(10 * time.Minute)
	tr.observe("ws1:dev", 3030, []int{100}, 10, now, 5*time.Minute, 2.0)
	if got := tr.idleFor("ws1:dev", now); got != 10*time.Minute {
		t.Fatalf("idle after a quiet 10-min gap = %s, want 10m", got)
	}
}

func TestObserveZeroWindowFallsBackToDefault(t *testing.T) {
	tr := newActivityTracker()
	now := observeEvery(tr, time.Unix(1000, 0), 16*time.Second, 0, 2.0, 10, 10)
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle with a zero window after 16 s = %s, want 0 (5-min default, not full yet)", got)
	}
}

// D6: a reload with ~0 CPU (next dev in service measured at 0.333 %) is use:
// it opens navigationBurst new connections in one sweep.
func TestObserveNavigationBurstIsActivity(t *testing.T) {
	tr := newActivityTracker()
	t0 := time.Unix(1000, 0)
	tab := []string{"127.0.0.1:50001"}
	reloaded := endpoints(51000, navigationBurst)
	steps := repeatPeers(tab, 26)                       // 0..400 s, one open tab, quiet
	steps = append(steps, reloaded)                     // 416 s: reload
	steps = append(steps, repeatPeers(reloaded, 15)...) // to 656 s
	now := observePeersEvery(tr, t0, 16*time.Second, 5*time.Minute, steps...)
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle 240 s after a reload = %s, want 0 (the burst is in the window)", got)
	}
	// 16 more quiet sweeps (to 912 s): the reload's sample has left the window.
	for i := 1; i <= 16; i++ {
		now = now.Add(16 * time.Second)
		tr.observeWith("ws1:dev", observation{port: 3030, pids: []int{100}, cpu: 10, peers: reloaded, peersKnown: true}, now, 5*time.Minute, 2.0)
	}
	if got := tr.idleFor("ws1:dev", now); got <= 0 || got > 4*time.Minute {
		// The reload sample leaves the window at 720 s; last active 704 s; idle 208 s.
		t.Fatalf("idle 8 min after the reload = %s, want in (0, 4m]", got)
	}
}

// D6 acceptance: a tab that polls the server on its own — up to
// navigationBurst-1 new connections per sweep, every sweep — is not use.
func TestObservePollingTabIsNotActivity(t *testing.T) {
	tr := newActivityTracker()
	steps := make([][]string, 41) // 640 s
	next := 52000
	for i := range steps {
		n := 1 + i%(navigationBurst-1) // 1 .. navigationBurst-1; navigationBurst >= 2 is a compile-time guard
		steps[i] = endpoints(next, n)  // all different from the previous sweep's
		next += n
	}
	now := observePeersEvery(tr, time.Unix(1000, 0), 16*time.Second, 5*time.Minute, steps...)
	if got, want := tr.idleFor("ws1:dev", now), 352*time.Second; got != want {
		t.Fatalf("idle of a server polled by an open tab = %s, want %s", got, want)
	}
}

// An open tab's websocket, untouched, is not use.
func TestObservePersistentConnectionIsNotActivity(t *testing.T) {
	tr := newActivityTracker()
	now := observePeersEvery(tr, time.Unix(1000, 0), 16*time.Second, 5*time.Minute, repeatPeers([]string{"127.0.0.1:50001"}, 41)...)
	if got, want := tr.idleFor("ws1:dev", now), 352*time.Second; got != want {
		t.Fatalf("idle with one stable connection = %s, want %s", got, want)
	}
}

// A sweep whose connection inventory failed is not "no connections": the
// baseline is kept, so the next good sweep does not read every open socket as new.
func TestObserveUnknownPeersKeepTheBaseline(t *testing.T) {
	tr := newActivityTracker()
	t0 := time.Unix(1000, 0)
	tab := endpoints(50001, navigationBurst)                                                 // enough open sockets to look like a burst if re-counted
	now := observePeersEvery(tr, t0, 16*time.Second, 5*time.Minute, repeatPeers(tab, 25)...) // 0..384 s
	now = now.Add(16 * time.Second)
	tr.observe("ws1:dev", 3030, []int{100}, 10, now, 5*time.Minute, 2.0) // inventory failed
	now = now.Add(16 * time.Second)
	tr.observeWith("ws1:dev", observation{port: 3030, pids: []int{100}, cpu: 10, peers: tab, peersKnown: true}, now, 5*time.Minute, 2.0)
	if got := tr.idleFor("ws1:dev", now); got == 0 {
		t.Fatal("the open tab was read as a new connection after one failed inventory")
	}
}

// The first known peer set is a baseline: before it, nothing was known.
func TestObserveFirstKnownPeersAreABaseline(t *testing.T) {
	tr := newActivityTracker()
	t0 := time.Unix(1000, 0)
	observeEvery(tr, t0, 16*time.Second, 5*time.Minute, 2.0, make([]float64, 25)...) // CPU-only, 0..384 s
	now := t0.Add(400 * time.Second)
	tr.observeWith("ws1:dev", observation{port: 3030, pids: []int{100}, cpu: 0, peers: endpoints(50001, navigationBurst), peersKnown: true}, now, 5*time.Minute, 2.0)
	if got := tr.idleFor("ws1:dev", now); got == 0 {
		t.Fatal("connections present at the first known inventory were read as new")
	}
}

// A restored entry keeps its verdict while the window refills — the contract
// of TestObservationConfirmsARestoredEntry — but real activity still shows.
func TestRestoredEntryActivityIsStillSeenWhileTheWindowRefills(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tr := newActivityTracker()
	tr.restore(map[string]activityEntry{
		"ws1:dev": {lastCPU: 42, lastActive: now.Add(-90 * time.Minute), port: 3030, pids: []int{100}},
	})
	tr.observe("ws1:dev", 3030, []int{100}, 42, now, 5*time.Minute, 2.0)
	now = now.Add(16 * time.Second)
	tr.observe("ws1:dev", 3030, []int{100}, 51, now, 5*time.Minute, 2.0) // 9 CPU-s in 16 s
	if got := tr.idleFor("ws1:dev", now); got != 0 {
		t.Fatalf("idle after a 9 CPU-s burst on a restored entry = %s, want 0", got)
	}
}

func TestEntriesSnapshotCopiesSamples(t *testing.T) {
	tr := newActivityTracker()
	observeEvery(tr, time.Unix(1000, 0), 16*time.Second, 5*time.Minute, 2.0, 1, 2)
	snap := tr.entriesSnapshot()
	snap["ws1:dev"].samples[0].cpu = 999
	if tr.entriesSnapshot()["ws1:dev"].samples[0].cpu == 999 {
		t.Fatal("snapshot aliases the tracker's samples")
	}
}
