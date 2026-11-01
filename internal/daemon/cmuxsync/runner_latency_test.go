package cmuxsync

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// A pass longer than SlowPassLog leaves one line naming each step's time;
// a quick one leaves none (D3 c).
func TestRunnerLogsSlowPass(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	var logs []string
	h.r.deps.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	h.world.onList = func() { h.now = h.now.Add(1500 * time.Millisecond) }
	h.step(0) // activation: slow listing (1.5 s) + reconcile + sync
	line := ""
	for _, l := range logs {
		if strings.HasPrefix(l, "cmuxsync: pass ") {
			line = l
		}
	}
	for _, want := range []string{"cmuxsync: pass 1.5s (", "slow 1.5s", "peers 0s", "reconcile 0s", "sync 0s", "arrange 0s", "claude_code 0s", "checkpoints 0"} {
		if !strings.Contains(line, want) {
			t.Fatalf("pass line %q lacks %q (logs %q)", line, want, logs)
		}
	}
	h.world.onList, logs = nil, nil
	h.step(time.Second)
	for _, l := range logs {
		if strings.HasPrefix(l, "cmuxsync: pass ") {
			t.Fatalf("quick pass logged: %q", l)
		}
	}
}

// latencyWorld opens wsA then n-1 more workspaces in winA, wsA listed first.
func latencyWorld(h *nativeHarness, n int) {
	h.world.add(winA, wsA, "a")
	for i := 1; i < n; i++ {
		h.world.add(winA, fmt.Sprintf("D0000000-0000-4000-8000-%012d", i), fmt.Sprintf("w%d", i))
	}
}

const callCost = 300 * time.Millisecond // one slow list-status

// D2 (H1): the dialog is answered while the anti-drift reconcile reads
// list-status on 20 workspaces at 300 ms each; the pill goes within
// FastInterval + WaitingHold (+ call granularity), not after the whole
// reconcile.
func TestRunnerPillClearNotBlockedBySlowReconcile(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, Config{Enabled: true, ReconcileInterval: 10 * time.Second})
	latencyWorld(h, 20)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "permission", "Bash", pigtail.WaitSourceAuto)
	h.nativeWait("s1", "waiting", "", h.now.Add(50*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Permission · Bash" {
		t.Fatalf("pill = %q", p)
	}
	var flipAt, clearAt time.Time
	calls := 0
	h.world.onStatus = func(string) {
		calls++
		if calls == 1 {
			flipAt = h.now
			h.nativeWait("s1", "busy", "", h.now) // the user answered
		}
		h.now = h.now.Add(callCost)
	}
	h.world.onWrite = func(call, ws, key, _ string) {
		if call == "clear-status" && ws == wsA && key == KeyAttention && clearAt.IsZero() {
			clearAt = h.now
		}
	}
	h.step(9 * time.Second) // 10 s after the start: the anti-drift reconcile runs
	for i := 0; i < 20 && clearAt.IsZero(); i++ {
		h.step(time.Second)
	}
	if calls < 20 {
		t.Fatalf("reconcile not run: %d list-status", calls)
	}
	if clearAt.IsZero() {
		t.Fatal("pill never cleared")
	}
	if got, max := clearAt.Sub(flipAt), Defaults().FastInterval+WaitingHold+3*callCost; got > max {
		t.Fatalf("pill cleared %s after the answer, want ≤ %s", got, max)
	}
}

// Review Focus 1: wsA's list-status is read (no pill) at the reconcile's
// first call; a checkpoint then lights its pill, the dialog is answered, and
// the hold ends before the reconcile does. The pass's own sync must clear the
// pill: the stale seed must not hide the key from Diff.
func TestRunnerReconcileKeepsCheckpointWrites(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, Config{Enabled: true, ReconcileInterval: 10 * time.Second})
	latencyWorld(h, 20)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.step(time.Second)
	calls := 0
	h.world.onStatus = func(string) {
		calls++
		switch calls {
		case 6: // T+1.5: the dialog opens; the checkpoint before call 9 (T+2.4) lights the pill
			h.wait("s1", "permission", "Bash", pigtail.WaitSourceAuto)
			h.nativeWait("s1", "waiting", "", h.now)
		case 10: // T+2.7: answered; out from the checkpoint at T+3.6, hold over before the reconcile ends (T+6)
			h.nativeWait("s1", "busy", "", h.now)
		}
		h.now = h.now.Add(callCost)
	}
	h.step(9 * time.Second)
	lit := false
	for _, c := range h.world.calls {
		lit = lit || strings.HasPrefix(c, "set-status "+wsA+" "+KeyAttention+" ")
	}
	if !lit {
		t.Fatalf("no checkpoint lit the pill: %q", h.world.calls)
	}
	h.world.onStatus = nil
	h.step(time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("pill stuck after the answer: %q", p)
	}
}

// Review Focus 2: between two list-status, only pill writes go out.
func TestRunnerCheckpointWritesPillsOnly(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, Config{Enabled: true, ReconcileInterval: 10 * time.Second})
	latencyWorld(h, 20)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.step(time.Second)
	calls := 0
	h.world.onStatus = func(string) {
		calls++
		if calls == 1 {
			h.bind(wsA) // a rename is owed from the middle of the reconcile on, not before its first call
			h.wait("s1", "permission", "Bash", pigtail.WaitSourceAuto)
			h.nativeWait("s1", "waiting", "", h.now)
		}
		h.now = h.now.Add(callCost)
	}
	h.world.calls = nil
	h.step(9 * time.Second)
	first, last := -1, -1
	for i, c := range h.world.calls {
		if strings.HasPrefix(c, "list-status") {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	sawPill := false
	for _, c := range h.world.calls[first:last] { // the rename becomes due after the first list-status: the mutation r.sync(now, false) sends it inside this window
		switch {
		case strings.HasPrefix(c, "set-status"), strings.HasPrefix(c, "clear-status"):
			sawPill = true
		case !strings.HasPrefix(c, "list-status"):
			t.Fatalf("checkpoint wrote %q", c)
		}
	}
	if !sawPill {
		t.Fatalf("no checkpoint pill write during the reconcile: %q", h.world.calls)
	}
}

// Review Focus 3: the one pass that owes a reconcile while cmux is available
// is the recovery after a failed listing (listingFailed sets needReconcile,
// available stays true). At activation available is false and listedAt zero,
// so ckptArmed is false whatever needReconcile says: that case proves nothing.
// A pill is due when the recovery pass starts; nothing may be written before
// its last list-status. Mutation: drop `!r.needReconcile` from the arming →
// the checkpoint at the top of slow() writes the pill → red.
func TestRunnerNoCheckpointWhileReconcileOwed(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	latencyWorld(h, 3)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.step(time.Second) // healthy: listed, reconciled, available
	h.world.listDown = errors.New("cmux: socket refused")
	h.step(30 * time.Second) // slow pass fails: degraded mode, reconcile owed
	if !h.r.needReconcile || !h.r.isAvailable() {
		t.Fatalf("not in the degraded mode under test: needReconcile=%v available=%v", h.r.needReconcile, h.r.isAvailable())
	}
	h.world.listDown = nil
	h.wait("s1", "arbitrage", "short title", pigtail.WaitSourceDeclared)
	h.nativeWait("s1", "idle", "", h.now) // the pill is now due
	h.world.calls = nil
	slowCost := func() { h.now = h.now.Add(callCost) }
	h.world.onList, h.world.onTree, h.world.onStatus = slowCost, slowCost, func(string) { slowCost() }
	h.step(10 * time.Second) // past BackoffMin: the recovery pass
	last, n := -1, 0
	for i, c := range h.world.calls {
		if strings.HasPrefix(c, "list-status") {
			last, n = i, n+1
		}
	}
	if n != 3 {
		t.Fatalf("recovery reconcile not run: %d list-status in %q", n, h.world.calls)
	}
	for _, c := range h.world.calls[:last] {
		if strings.HasPrefix(c, "set-status") || strings.HasPrefix(c, "clear-status") {
			t.Fatalf("written before the reconcile ended: %q", h.world.calls)
		}
	}
	if p := h.pill(); p != "Decision · short title" {
		t.Fatalf("pill after the pass = %q", p)
	}
}

// Checkpoints of slow() (listing loop): the dialog is answered while slow()
// lists two windows at 1.5 s a call; the hold started at the first checkpoint
// after the answer ends inside slow(), before the `cmux sessions` call.
// Red on the base (the pill goes with the pass's sync, after slow()).
// Mutation: remove the two checkpoints before ListWorkspaces / ListSurfaces →
// only the first-line and pre-refreshPeers ones remain, the hold starts at
// slow()'s end → red.
func TestRunnerPillClearNotBlockedBySlowListings(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winB, wsB, "b")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "permission", "Bash", pigtail.WaitSourceAuto)
	h.nativeWait("s1", "waiting", "", h.now.Add(50*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Permission · Bash" {
		t.Fatalf("pill = %q", p)
	}
	const listCost = 1500 * time.Millisecond
	var flipAt, clearAt, peersAt time.Time
	h.world.onList = func() { // list-windows, the first call of slow()
		flipAt = h.now
		h.nativeWait("s1", "busy", "", h.now) // the user answered
		h.now = h.now.Add(listCost)
	}
	h.world.onTree = func() { h.now = h.now.Add(listCost) }
	h.r.deps.RefreshPeers = func() ([]string, error) { peersAt = h.now; return nil, nil }
	h.world.onWrite = func(call, ws, key, _ string) {
		if call == "clear-status" && ws == wsA && key == KeyAttention && clearAt.IsZero() {
			clearAt = h.now
		}
	}
	h.step(30 * time.Second) // the slow pass
	if peersAt.IsZero() || clearAt.IsZero() {
		t.Fatalf("slow pass not run or pill never cleared (peers %v, clear %v)", peersAt, clearAt)
	}
	if !clearAt.Before(peersAt) {
		t.Fatalf("pill cleared at the end of slow() or later (%s after the answer); want inside it", clearAt.Sub(flipAt))
	}
	if got, max := clearAt.Sub(flipAt), WaitingHold+Defaults().FastInterval+2*listCost; got > max {
		t.Fatalf("pill cleared %s after the answer, want ≤ %s", got, max)
	}
}

// Checkpoint before refreshPeers: a wait declared during the last listing
// call shows before the `cmux sessions` call starts, not after it.
// Mutation: remove the checkpoint before r.refreshPeers → red.
func TestRunnerPillShownBeforeSlowPeersCall(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.step(time.Second)
	h.wait("s1", "arbitrage", "short title", pigtail.WaitSourceDeclared) // hidden while busy
	treeCalls := 0
	h.world.onTree = func() { // workspace-list, then tree (the last listing call)
		treeCalls++
		if treeCalls == 2 {
			h.nativeWait("s1", "idle", "", h.now) // hands back: the pill is due
		}
		h.now = h.now.Add(1500 * time.Millisecond)
	}
	shownAtPeers := ""
	h.r.deps.RefreshPeers = func() ([]string, error) { shownAtPeers = h.pill(); return nil, nil }
	h.step(30 * time.Second)
	if shownAtPeers != "Decision · short title" {
		t.Fatalf("pill when `cmux sessions` started = %q, want it already shown", shownAtPeers)
	}
}

// Checkpoint of fixClaudeCode: the claude_code patch reads list-status on 6
// idle workspaces at 1.5 s each, after the pass's own sync. A wait declared
// during its first read shows before the second read, not at the next pass.
// Red on the base (written only by the next pass: after 5 reads).
// Mutation: remove the checkpoint before ListStatus in fixClaudeCode → red.
func TestRunnerPillShownDuringSlowClaudeCodePatch(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, withFix())
	latencyWorld(h, 6)
	h.native("s0", wsA, "idle", h.now.Add(-time.Minute)) // pid 4242, idle for 1 min; the cheap check of fixClaudeCode uses a synthetic Running row, so wsA is read too (6 reads)
	stuck := cmux.StatusEntry{Key: "claude_code", Value: "Running", Icon: "bolt.fill", Color: "#4C8DFF"}
	var others []string
	for i := 1; i < 6; i++ {
		ws := fmt.Sprintf("D0000000-0000-4000-8000-%012d", i)
		h.nativeOn(fmt.Sprintf("s%d", i), ws, fmt.Sprintf("S-s%d", i), 4242+i, "idle", h.now.Add(-time.Minute))
		others = append(others, ws)
	}
	h.step(0)
	h.step(time.Second)
	for _, ws := range others { // the warm-up patch fixed them: stick them again
		h.world.status[ws]["claude_code"] = stuck
	}
	calls, callsAtWrite := 0, -1
	h.world.onStatus = func(string) {
		calls++
		if calls == 1 {
			h.wait("s0", "arbitrage", "short title", pigtail.WaitSourceDeclared)
		}
		h.now = h.now.Add(1500 * time.Millisecond)
	}
	h.world.onWrite = func(call, ws, key, _ string) {
		if call == "set-status" && ws == wsA && key == KeyAttention && callsAtWrite < 0 {
			callsAtWrite = calls
		}
	}
	h.step(30 * time.Second) // slow pass, then the patch
	h.step(time.Second)      // lets the base show its (late) write
	if calls != 6 {
		t.Fatalf("patch read %d list-status, want 6", calls)
	}
	if callsAtWrite != 1 {
		t.Fatalf("pill written after %d of 6 patch reads, want right after the first (-1 = never)", callsAtWrite)
	}
}

// Review Focus 4: a checkpoint write fails mid-reconcile: logged once, the
// reconcile goes on, the pass's own sync meets the failure and pauses
// writes as before.
func TestRunnerCheckpointFailureLeftToPass(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, Config{Enabled: true, ReconcileInterval: 10 * time.Second})
	latencyWorld(h, 20)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.step(time.Second)
	var logs []string
	h.r.deps.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	calls := 0
	h.world.onStatus = func(string) {
		calls++
		if calls == 1 {
			h.wait("s1", "permission", "Bash", pigtail.WaitSourceAuto)
			h.nativeWait("s1", "waiting", "", h.now)
			h.world.setDown = fmt.Errorf("cmux set-status: timeout")
		}
		h.now = h.now.Add(callCost)
	}
	h.step(9 * time.Second)
	if calls != 20 {
		t.Fatalf("reconcile cut short: %d list-status", calls)
	}
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "pill checkpoint") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("checkpoint failure logged %d times: %q", n, logs)
	}
	if h.r.State().CmuxAvailable {
		t.Fatal("the pass's own failed write did not pause writes")
	}
}

// Criterion 2, real UserPromptSubmit path: the declared wait is cleared AND
// the session goes busy, mid-reconcile. The shown pill is then held
// WaitingHold (Damper) — the hold starts at the first checkpoint after the
// clear (WaitsChanged lifts the FastInterval gate), not at the next tick — and
// the clear-status lands within WaitingHold + FastInterval of the clear.
// (An idle session would turn the pill into "Your turn", a live wait the
// Damper does not hold: that would pass on a mere change of value.)
// Mutation: remove the dirty flag from pillCheckpoint's gate → the hold starts
// a FastInterval late → clear after the upper bound → red.
func TestRunnerWaitsChangedSyncsPillsMidPass(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, Config{Enabled: true, ReconcileInterval: 10 * time.Second})
	latencyWorld(h, 20)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "arbitrage", "short title", pigtail.WaitSourceDeclared)
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Decision · short title" {
		t.Fatalf("pill = %q", p)
	}
	var clearedAt, clearAt time.Time
	var attnWrites []string // every pigtail-attn write after the clear, in order
	calls := 0
	h.world.onStatus = func(string) {
		calls++
		if calls == 2 { // UserPromptSubmit: wait cleared, session busy
			clearedAt = h.now
			if _, err := h.st.ClearSessionWaits("s1", pigtail.WaitSourceDeclared); err != nil {
				t.Fatal(err)
			}
			h.nativeWait("s1", "busy", "", h.now)
			h.r.WaitsChanged()
		}
		h.now = h.now.Add(callCost)
	}
	h.world.onWrite = func(call, ws, key, value string) {
		if ws == wsA && key == KeyAttention && !clearedAt.IsZero() {
			attnWrites = append(attnWrites, call)
			if call == "clear-status" && clearAt.IsZero() {
				clearAt = h.now
			}
		}
	}
	h.step(9 * time.Second)
	for i := 0; i < 5 && clearAt.IsZero(); i++ {
		h.step(time.Second)
	}
	if clearAt.IsZero() {
		t.Fatal("pill never cleared")
	}
	if len(attnWrites) != 1 {
		t.Fatalf("pill written %v after the clear, want one clear-status (held, then dropped)", attnWrites)
	}
	if got, min := clearAt.Sub(clearedAt), callCost+WaitingHold; got < min {
		t.Fatalf("pill dropped %s after the clear, before the %s hold ended", got, min)
	}
	if got, max := clearAt.Sub(clearedAt), WaitingHold+Defaults().FastInterval; got > max {
		t.Fatalf("pill dropped %s after the clear, want ≤ %s (hold started at the first checkpoint)", got, max)
	}
}

// Review Focus 5: WaitsChanged wakes the loop without a listing, and makes
// no cmux call while the loop is off.
func TestRunnerWaitsChangedNoListingNoCallWhenOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.step(0)
	h.world.calls = nil
	h.r.WaitsChanged()
	if len(h.r.wake) != 1 {
		t.Fatal("WaitsChanged did not wake the loop")
	}
	h.step(0)
	for _, c := range h.world.calls {
		if strings.HasPrefix(c, "list-windows") {
			t.Fatalf("WaitsChanged forced a listing: %q", h.world.calls)
		}
	}
	h.r.SetEnabled(false)
	h.step(0) // switch-off pass
	h.world.calls = nil
	h.r.WaitsChanged()
	h.step(0)
	if len(h.world.calls) != 0 {
		t.Fatalf("cmux called while off: %q", h.world.calls)
	}
}

// loopSim plays Run's clock in the simulated world: ticks every FastInterval
// from nextTick, plus the hold wake the loop arms at the nearest waiting
// hold's end, until `until`.
func loopSim(h *nativeHarness, nextTick *time.Time, until time.Time) {
	for {
		next, hold := *nextTick, false
		if d := h.r.holdDeadline(); !d.IsZero() && d.After(h.now) && d.Before(next) {
			next, hold = d, true
		}
		if next.After(until) {
			h.now = until
			return
		}
		h.now = next
		if hold {
			h.r.holdWake()
		} else {
			h.r.Step()
			*nextTick = nextTick.Add(Defaults().FastInterval)
		}
	}
}

// The waiting hold ends between two ticks: the pill goes at the end of the
// hold (WaitingHold after the first observation out of waiting, plus a small
// margin), not at the next tick, and the wake makes no listing nor any
// call other than a pill write.
func TestRunnerPillClearedAtHoldEndBetweenTicks(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, Config{Enabled: true, ReconcileInterval: time.Hour, SlowInterval: time.Hour})
	latencyWorld(h, 1)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "permission", "Bash", pigtail.WaitSourceAuto)
	h.nativeWait("s1", "waiting", "", h.now.Add(50*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Permission · Bash" {
		t.Fatalf("pill = %q", p)
	}
	nextTick := h.now.Add(Defaults().FastInterval)
	h.now = h.now.Add(300 * time.Millisecond)
	h.nativeWait("s1", "busy", "", h.now) // answered between two ticks
	flipAt := h.now
	h.r.WaitsChanged()
	<-h.r.wake
	h.r.Step() // first observation out of waiting: the hold starts here
	var clearAt time.Time
	var other []string
	h.world.onWrite = func(call, ws, key, _ string) {
		if call == "clear-status" && ws == wsA && key == KeyAttention && clearAt.IsZero() {
			clearAt = h.now
		}
	}
	listed := len(h.world.calls)
	loopSim(h, &nextTick, flipAt.Add(2*Defaults().FastInterval+WaitingHold))
	if clearAt.IsZero() {
		t.Fatal("pill never cleared")
	}
	if got, max := clearAt.Sub(flipAt), WaitingHold+200*time.Millisecond; got > max {
		t.Fatalf("pill cleared %s after leaving waiting, want ≤ %s (the hold's end, not the next tick)", got, max)
	}
	for _, c := range h.world.calls[listed:] {
		if !strings.HasPrefix(c, "set-status") && !strings.HasPrefix(c, "clear-status") {
			other = append(other, c)
		}
	}
	if len(other) != 0 {
		t.Fatalf("calls besides pill writes between the flip and the clear: %q", other)
	}
}

// The waiting hold ends during a slow pass, less than FastInterval after the
// last pill sync, with no wait changed: the next checkpoint syncs the pills
// (the hold's end is a due date of its own), the pill does not wait for
// FastInterval since the last sync nor for the pass's own sync.
// Calls cost 450 ms: the hold starts at the checkpoint S = answer + 1.35 s
// (the first one FastInterval after the pass's sync), syncs follow at +1.35
// (FastInterval) and the hold ends at S+2, between two of them: clear at
// answer + 3.6 s with the hold's end as a due date, + 4.05 s without.
// Red on 6e903d8 ("pill cleared 4.05s after the answer, want ≤ 3.9s").
// Mutation: remove the hold-deadline clause from pillsDue → red.
func TestRunnerCheckpointSyncsAtHoldEndMidPass(t *testing.T) {
	t.Parallel()
	const cost = 450 * time.Millisecond
	h := newNativeHarness(t, Config{Enabled: true, ReconcileInterval: 10 * time.Second})
	latencyWorld(h, 20)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "permission", "Bash", pigtail.WaitSourceAuto)
	h.nativeWait("s1", "waiting", "", h.now.Add(50*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Permission · Bash" {
		t.Fatalf("pill = %q", p)
	}
	var flipAt, clearAt time.Time
	calls := 0
	h.world.onStatus = func(string) {
		calls++
		if calls == 1 {
			flipAt = h.now
			h.nativeWait("s1", "busy", "", h.now) // answered, no WaitsChanged
		}
		h.now = h.now.Add(cost)
	}
	h.world.onWrite = func(call, ws, key, _ string) {
		if call == "clear-status" && ws == wsA && key == KeyAttention && clearAt.IsZero() {
			clearAt = h.now
		}
	}
	h.step(9 * time.Second) // the anti-drift reconcile runs, 20 calls
	for i := 0; i < 20 && clearAt.IsZero(); i++ {
		h.step(time.Second)
	}
	if calls < 20 {
		t.Fatalf("reconcile not run: %d list-status", calls)
	}
	if clearAt.IsZero() {
		t.Fatal("pill never cleared")
	}
	if got, max := clearAt.Sub(flipAt), Defaults().FastInterval+WaitingHold+2*cost; got > max {
		t.Fatalf("pill cleared %s after the answer, want ≤ %s (the checkpoint after the hold's end)", got, max)
	}
}
