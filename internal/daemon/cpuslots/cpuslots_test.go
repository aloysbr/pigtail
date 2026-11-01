package cpuslots

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// testConfig is a fully explicit arbiter config: no platform probing, no
// revocation unless a test asks for it, no queue ceiling unless a test asks.
func testConfig(budget int) Config {
	return Config{Budget: budget, MaxHold: time.Hour, MaxWaiters: 0, SweepInterval: 0}
}

// newTestArbiter wires an arbiter with a silent log, no real waiting, and a
// notify hook (see WithNotify) that feeds the returned channel — tests block
// on it instead of polling Status() on a timer; see waitForNotify.
func newTestArbiter(t *testing.T, cfg Config, opts ...Option) (*Arbiter, <-chan struct{}) {
	t.Helper()
	notify := make(chan struct{}, 1)
	signal := func() {
		select {
		case notify <- struct{}{}:
		default: // a pending signal already covers "something changed"
		}
	}
	base := []Option{
		WithConfig(cfg),
		WithLogger(func(string, ...any) {}),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithNotify(signal),
	}
	return New(events.New(), append(base, opts...)...), notify
}

// waitForNotify blocks until cond is true, waking only on a real state
// change reported through notify (WithNotify) rather than a sleep timer.
// Everything cond observes crosses the arbiter's own mutex, so this stays
// race-detector clean. The deadline is a safety net against a genuine test
// bug — a hung arbiter, a cond that can never become true — never the
// synchronization mechanism itself.
func waitForNotify(t *testing.T, notify <-chan struct{}, what string, cond func() bool) {
	t.Helper()
	if cond() {
		return
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-notify:
			if cond() {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestAcquireGrantsImmediatelyUnderBudget(t *testing.T) {
	a, _ := newTestArbiter(t, testConfig(2))
	g, err := a.Acquire(context.Background(), "ws1:vitest", "Button.test.tsx")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if g.Degraded {
		t.Errorf("grant is degraded, want a real slot")
	}
	if st := a.Status(); st.Held != 1 || st.Waiting != 0 || st.Budget != 2 {
		t.Fatalf("status = %+v, want held 1 waiting 0 budget 2", st)
	}
	a.Release(g)
	if st := a.Status(); st.Held != 0 {
		t.Fatalf("held after release = %d, want 0", st.Held)
	}
}

func TestAcquireOnDisabledArbiterFailsOpen(t *testing.T) {
	a, _ := newTestArbiter(t, testConfig(0))
	if a.Enabled() {
		t.Fatalf("Enabled() = true for budget 0")
	}
	_, err := a.Acquire(context.Background(), "ws1:vitest", "x")
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Acquire error = %v, want ErrDisabled", err)
	}
}

func TestReleaseHandsTheSlotToTheOldestWaiter(t *testing.T) {
	a, notify := newTestArbiter(t, testConfig(1))
	first, err := a.Acquire(context.Background(), "ws1:vitest", "a")
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}

	granted := make(chan Grant, 1)
	go func() {
		g, aerr := a.Acquire(context.Background(), "ws2:vitest", "b")
		if aerr == nil {
			granted <- g
		}
		close(granted)
	}()
	waitForNotify(t, notify, "the second acquire to queue", func() bool { return a.Status().Waiting == 1 })

	a.Release(first)
	g, ok := <-granted
	if !ok {
		t.Fatalf("second acquire failed instead of being served")
	}
	if g.Degraded {
		t.Errorf("second grant is degraded, want a real slot")
	}
	if st := a.Status(); st.Held != 1 || st.Waiting != 0 {
		t.Fatalf("status = %+v, want held 1 waiting 0", st)
	}
	a.Release(g)
}

func TestCancelledAcquireLeavesNothingBooked(t *testing.T) {
	a, notify := newTestArbiter(t, testConfig(1))
	held, err := a.Acquire(context.Background(), "ws1:vitest", "a")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, aerr := a.Acquire(ctx, "ws2:vitest", "b")
		done <- aerr
	}()
	waitForNotify(t, notify, "the second acquire to queue", func() bool { return a.Status().Waiting == 1 })

	cancel()
	if aerr := <-done; !errors.Is(aerr, context.Canceled) {
		t.Fatalf("cancelled Acquire returned %v, want context.Canceled", aerr)
	}
	// No wait needed here: Acquire's ctx.Done() branch dequeues (and, if it
	// won a race with a grant, frees) before it returns, and done already
	// carries that return. The queue is provably drained already.
	if st := a.Status(); st.Waiting != 0 {
		t.Fatalf("waiting after cancellation = %d, want 0 (queue not drained)", st.Waiting)
	}
	if st := a.Status(); st.Held != 1 {
		t.Fatalf("held = %d, want 1 (only the first slot)", st.Held)
	}
	a.Release(held)
}

// The ceiling is a bookkeeping revocation, not a kill: the arbiter drops the
// slot from its books, publishes an event, tells the client nothing, and the
// client's own Release stays harmless afterwards.
func TestSweepRevokesAccountingPastMaxHold(t *testing.T) {
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	bus := events.New()
	sub := bus.Subscribe(string(pigtail.EventCPUSlotRevoked))
	defer sub.Close()

	a := New(bus,
		WithConfig(Config{Budget: 1, MaxHold: 10 * time.Minute}),
		WithLogger(func(string, ...any) {}),
		WithWaiter(func(context.Context, time.Duration) {}),
		WithClock(func() time.Time { return clock }),
	)
	g, err := a.Acquire(context.Background(), "ws1:vitest", "wedged.test.tsx")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Not yet past the ceiling.
	clock = clock.Add(9 * time.Minute)
	a.Sweep()
	if st := a.Status(); st.Held != 1 {
		t.Fatalf("held before the ceiling = %d, want 1", st.Held)
	}

	clock = clock.Add(2 * time.Minute)
	a.Sweep()
	if st := a.Status(); st.Held != 0 {
		t.Fatalf("held after the ceiling = %d, want 0 (revoked)", st.Held)
	}

	select {
	case e := <-sub.C:
		if e.Type != pigtail.EventCPUSlotRevoked {
			t.Fatalf("event type = %s, want %s", e.Type, pigtail.EventCPUSlotRevoked)
		}
		if e.Data["group"] != "ws1:vitest" {
			t.Fatalf("event group = %v, want ws1:vitest", e.Data["group"])
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no cpu.slot_revoked event published")
	}

	// The client is none the wiser and its own release must stay a no-op.
	a.Release(g)
	if st := a.Status(); st.Held != 0 {
		t.Fatalf("held after a released-then-revoked slot = %d, want 0", st.Held)
	}
}

// A revoked slot is redistributed: the waiter behind it is served without
// anything being killed.
func TestRevocationServesTheWaitingQueue(t *testing.T) {
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	a, notify := newTestArbiter(t, Config{Budget: 1, MaxHold: 10 * time.Minute},
		WithClock(func() time.Time { return clock }))
	if _, err := a.Acquire(context.Background(), "ws1:vitest", "wedged"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	granted := make(chan struct{})
	go func() {
		if _, err := a.Acquire(context.Background(), "ws2:vitest", "next"); err == nil {
			close(granted)
		}
	}()
	waitForNotify(t, notify, "the second acquire to queue", func() bool { return a.Status().Waiting == 1 })

	clock = clock.Add(11 * time.Minute)
	a.Sweep()
	select {
	case <-granted:
	case <-time.After(2 * time.Second):
		t.Fatalf("revoked slot was not handed to the waiting queue")
	}
}

// Beyond MaxWaiters the arbiter grants a degraded slot rather than letting
// its queue swell: it must never be the reason a test does not run.
func TestQueueOverflowGrantsDegraded(t *testing.T) {
	a, notify := newTestArbiter(t, Config{Budget: 1, MaxHold: time.Hour, MaxWaiters: 1})
	if _, err := a.Acquire(context.Background(), "ws1:vitest", "a"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	go func() { _, _ = a.Acquire(context.Background(), "ws2:vitest", "b") }()
	waitForNotify(t, notify, "the queue to reach its ceiling", func() bool { return a.Status().Waiting == 1 })

	g, err := a.Acquire(context.Background(), "ws3:vitest", "c")
	if err != nil {
		t.Fatalf("Acquire past the ceiling: %v", err)
	}
	if !g.Degraded {
		t.Fatalf("grant past the ceiling is not degraded")
	}
	if st := a.Status(); st.Held != 1 || st.Waiting != 1 {
		t.Fatalf("status = %+v, want held 1 waiting 1 (the degraded grant books nothing)", st)
	}
	a.Release(g) // must stay a no-op
	if st := a.Status(); st.Held != 1 {
		t.Fatalf("held after releasing a degraded grant = %d, want 1", st.Held)
	}
}

// Status reports the same shares the attribution uses, so `pigtail slots`
// explains what the arbiter is actually doing.
func TestStatusReportsPerGroupShares(t *testing.T) {
	a, notify := newTestArbiter(t, testConfig(4))
	for i := 0; i < 4; i++ {
		if _, err := a.Acquire(context.Background(), "ws1:vitest", "a"); err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
	}
	go func() { _, _ = a.Acquire(context.Background(), "ws2:vitest", "b") }()
	waitForNotify(t, notify, "the second group to queue", func() bool { return a.Status().Waiting == 1 })

	st := a.Status()
	if st.Budget != 4 || st.Held != 4 || st.Waiting != 1 {
		t.Fatalf("status = %+v, want budget 4 held 4 waiting 1", st)
	}
	if len(st.Groups) != 2 {
		t.Fatalf("groups = %d, want 2: %+v", len(st.Groups), st.Groups)
	}
	if st.Groups[0].Group != "ws1:vitest" || st.Groups[0].Held != 4 || st.Groups[0].Share != 2 {
		t.Errorf("group[0] = %+v, want ws1:vitest held 4 share 2", st.Groups[0])
	}
	if st.Groups[1].Group != "ws2:vitest" || st.Groups[1].Waiting != 1 || st.Groups[1].Share != 2 {
		t.Errorf("group[1] = %+v, want ws2:vitest waiting 1 share 2", st.Groups[1])
	}
}

// TestFairnessInput pins down fairnessInput itself, in isolation from
// shares(): the two branches that decide whether a group's literal demand is
// trusted (Waiting == 0) or replaced with a full-budget sentinel
// (Waiting > 0), plus the degenerate budget <= 0 input.
func TestFairnessInput(t *testing.T) {
	cases := []struct {
		name   string
		groups []GroupDemand
		budget int
		want   []GroupDemand
	}{
		{
			name:   "budget zero collapses any boosted waiting to zero",
			groups: []GroupDemand{{Group: "a", Held: 2, Waiting: 3}},
			budget: 0,
			want:   []GroupDemand{{Group: "a", Held: 2, Waiting: 0}},
		},
		{
			name:   "zero waiters keeps the literal, possibly modest, demand",
			groups: []GroupDemand{{Group: "a", Held: 2, Waiting: 0}},
			budget: 9,
			want:   []GroupDemand{{Group: "a", Held: 2, Waiting: 0}},
		},
		{
			name:   "held and waiting both nonzero boosts waiting to the full budget",
			groups: []GroupDemand{{Group: "a", Held: 3, Waiting: 1}},
			budget: 9,
			want:   []GroupDemand{{Group: "a", Held: 3, Waiting: 9}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fairnessInput(tc.groups, tc.budget)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got) = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestAssignLockedServesAccordingToFairnessBoost pins down the one thing
// fairnessInput actually changes in the allocation path (not just in
// Status's display): the *order* in which assignLocked serves two groups —
// both already holding a slot AND both queued for another — when two slots
// free up at once.
//
// b is older, already holds 5 of the 8-slot budget, and has one more
// request queued. a holds only 1 and also has one queued. Both waiters
// eventually get served either way — the budget has room for both — but the
// order differs:
//
//   - Fed literal demand, shares() satisfies a's queued request outright
//     (a's tiny literal demand of 2 is <= the fair split of 4), which frees
//     enough surplus (6, computed directly against what's left) to put b's
//     literal share (6) above its own held count (5). b — older, checked
//     first in queue order — then qualifies and is served *first*.
//   - Fed the boosted demand fairnessInput actually produces (both groups'
//     Waiting reads as the full budget), neither group can be satisfied
//     below the fair split; the even split (a=4, b=4) puts b's share back
//     under its held count, disqualifying it, so a is served *first*
//     instead.
//
// Reverting fairnessInput to a pass-through must flip which group is served
// first, from a back to b.
func TestAssignLockedServesAccordingToFairnessBoost(t *testing.T) {
	var tick int64
	fakeClock := func() time.Time {
		tick++
		return time.Unix(0, tick)
	}
	a, _ := newTestArbiter(t, Config{Budget: 8, MaxHold: time.Hour}, WithClock(fakeClock))

	bHolders := make([]*holder, 5)
	for i := range bHolders {
		bHolders[i] = &holder{group: "b", label: "held", since: time.Unix(-1, int64(i))}
	}
	aHolder := &holder{group: "a", label: "held", since: time.Unix(-1, 10)}

	bWaiter := &waiter{group: "b", label: "next", since: time.Unix(-1, 5), ch: make(chan struct{})}
	aWaiter := &waiter{group: "a", label: "next", since: time.Unix(-1, 11), ch: make(chan struct{})}

	a.mu.Lock()
	for _, h := range bHolders {
		a.holders[h] = struct{}{}
	}
	a.holders[aHolder] = struct{}{}
	a.queue = []*waiter{bWaiter, aWaiter} // b is older: queue is oldest-first
	a.assignLocked()
	a.mu.Unlock()

	if bWaiter.slot == nil || aWaiter.slot == nil {
		t.Fatalf("expected both waiters served (budget has room for both): b=%v a=%v", bWaiter.slot, aWaiter.slot)
	}
	// fakeClock is called once per grant, in serve order, with a strictly
	// increasing value: whichever holder's `since` is smaller was served
	// first.
	if !aWaiter.slot.since.Before(bWaiter.slot.since) {
		t.Errorf("b was served before a; want a served first (the fairness boost should have made b, already over its fair share, wait)")
	}
}
