package cpuslots

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ErrDisabled is what Acquire returns when the arbiter has no budget
// (-cpu-slots=0). Clients treat it exactly like an absent daemon: they run
// their work immediately, unarbitrated.
var ErrDisabled = errors.New("cpuslots: arbiter disabled")

// Config tunes the arbiter.
type Config struct {
	// Budget is the number of slots distributed. Zero or less disables the
	// arbiter entirely — that is the daemon-side kill switch.
	Budget int

	// MaxHold is how long a slot may stay on the books before its accounting
	// is revoked. Zero or less disables revocation, which is what tests that
	// script their own clock want. See Sweep for why revoking kills nothing.
	MaxHold time.Duration

	// MaxWaiters bounds the queue. Beyond it, Acquire grants a degraded slot
	// rather than letting the queue swell: an arbiter that grows without
	// bound is an arbiter that eventually stops answering. Zero or less means
	// unbounded.
	MaxWaiters int

	// SweepInterval paces the revocation sweep in Run. Zero or less stops Run
	// from looping at all.
	SweepInterval time.Duration
}

// Defaults are the out-of-the-box arbiter settings: the platform budget, a
// ten-minute holding ceiling, a 256-deep queue, swept every thirty seconds.
func Defaults() Config {
	return Config{
		Budget:        DefaultBudget(),
		MaxHold:       10 * time.Minute,
		MaxWaiters:    256,
		SweepInterval: 30 * time.Second,
	}
}

// holder is one slot currently on the books.
type holder struct {
	group string
	label string
	since time.Time
	// enqueuedAt and waited feed the history line when the hold ends.
	enqueuedAt time.Time
	waited     time.Duration
}

// waiter is one queued request. slot and ch are both written under the
// arbiter's mutex: ch is closed exactly once, at the moment slot is set.
type waiter struct {
	group string
	label string
	since time.Time
	slot  *holder
	ch    chan struct{}
}

// Grant is one acquired slot. Release returns it; releasing the zero Grant,
// or a degraded one, is a no-op.
type Grant struct {
	// Waited is how long the client sat in the queue.
	Waited time.Duration
	// Degraded reports a slot handed out without real arbitration — the queue
	// was at its ceiling. The work runs; it is simply not counted.
	Degraded bool

	slot *holder
}

// GroupStatus is one group's line in Status.
type GroupStatus struct {
	Group   string
	Held    int
	Waiting int
	Share   int
}

// Status is the arbiter's whole state, as GET /cpu/slots reports it.
type Status struct {
	Budget  int
	Held    int
	Waiting int
	Groups  []GroupStatus
}

// Arbiter distributes a fixed budget of CPU slots between groups, fairly and
// without preemption. Mirrors rammon's seam/option style so the same testing
// habits apply: pure decisions in decide.go, mutable state behind one mutex
// here, an injectable clock and waiter.
type Arbiter struct {
	bus  *events.Bus
	cfg  Config
	logf func(format string, args ...any)

	// now returns the current time. Injected so tests script exact
	// timestamps instead of racing the real clock.
	now func() time.Time
	// wait sleeps for d unless ctx is done. Injected for instant tests.
	wait func(ctx context.Context, d time.Duration)
	// notify, when set, is called after every state change a test might want
	// to observe synchronously — a waiter enqueued, a slot granted or
	// revoked — instead of polling Status() on a timer. nil in production:
	// the call site is a nil check, so it costs nothing when unset.
	notify func()
	// history, when set, gets one line per ended hold (see history.go).
	history *History
	// afterSelectHook, nil in production, runs in Acquire's cancellation
	// branch before it takes the lock: a test grants the waiter there to
	// force a grant racing a cancellation.
	afterSelectHook func()

	mu      sync.Mutex
	holders map[*holder]struct{}
	queue   []*waiter
}

// Option customises an Arbiter (daemon wiring and tests).
type Option func(*Arbiter)

// New constructs an Arbiter with platform defaults. Tests override the seams.
func New(bus *events.Bus, opts ...Option) *Arbiter {
	a := &Arbiter{
		bus:     bus,
		cfg:     Defaults(),
		logf:    log.Printf,
		now:     time.Now,
		holders: map[*holder]struct{}{},
		wait: func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// WithConfig overrides the arbiter config.
func WithConfig(c Config) Option { return func(a *Arbiter) { a.cfg = c } }

// WithLogger overrides the log sink.
func WithLogger(f func(string, ...any)) Option { return func(a *Arbiter) { a.logf = f } }

// WithClock overrides the arbiter's clock.
func WithClock(f func() time.Time) Option { return func(a *Arbiter) { a.now = f } }

// WithWaiter overrides the inter-sweep sleep (tests pass a no-op).
func WithWaiter(f func(context.Context, time.Duration)) Option {
	return func(a *Arbiter) { a.wait = f }
}

// WithNotify installs a hook the arbiter calls, outside its own mutex, after
// a waiter is enqueued and after assignLocked grants or a sweep revokes a
// slot. It exists so tests can block on a real state change instead of
// polling Status() on a timer — a poll loop is a flake vector under exactly
// the load this feature exists to relieve. f must not block or itself call
// back into the Arbiter while assuming a lock is held; it runs with none.
func WithNotify(f func()) Option { return func(a *Arbiter) { a.notify = f } }

// fireNotify calls the notify hook if one is set. Caller must not hold mu.
func (a *Arbiter) fireNotify() {
	if a.notify != nil {
		a.notify()
	}
}

// Enabled reports whether the arbiter has a budget to distribute.
func (a *Arbiter) Enabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Budget > 0
}

// Budget reports the total number of distributable slots (0 when disabled).
func (a *Arbiter) Budget() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Budget
}

// Acquire blocks until a slot is granted or ctx is done. The returned Grant
// must be released — in the handler, by a defer that fires when the client's
// connection drops.
//
// It never fails for lack of capacity: over the queue ceiling it hands out a
// degraded grant instead, because an arbiter that refuses is an arbiter that
// stops a test from running.
func (a *Arbiter) Acquire(ctx context.Context, group, label string) (Grant, error) {
	start := a.now()

	a.mu.Lock()
	if a.cfg.Budget <= 0 {
		a.mu.Unlock()
		return Grant{}, ErrDisabled
	}
	if a.cfg.MaxWaiters > 0 && len(a.queue) >= a.cfg.MaxWaiters {
		a.mu.Unlock()
		return Grant{Degraded: true}, nil
	}
	w := &waiter{group: group, label: label, since: start, ch: make(chan struct{})}
	a.queue = append(a.queue, w)
	a.assignLocked()
	slot := w.slot
	a.mu.Unlock()
	// Fires after the enqueue and, when assignLocked served someone
	// immediately (this waiter or another one already ahead of it), after
	// that grant too — both are state a test may be waiting to observe.
	a.fireNotify()

	if slot != nil {
		return Grant{slot: slot}, nil
	}

	select {
	case <-w.ch:
		a.mu.Lock()
		slot = w.slot
		a.mu.Unlock()
		return Grant{Waited: a.now().Sub(start), slot: slot}, nil
	case <-ctx.Done():
		if a.afterSelectHook != nil {
			a.afterSelectHook()
		}
		// A grant can race the cancellation: take back whatever we were given
		// on the way out, or the slot would be booked to a client that is
		// already gone.
		a.mu.Lock()
		a.dequeueLocked(w)
		slot = w.slot
		end := a.now()
		a.mu.Unlock()
		if slot != nil {
			a.free(slot) // free fires its own notify, and writes the one "released" line
		} else {
			a.appendHistory(cancelRecord(w, end))
			a.fireNotify() // the queue shrank even with nothing to free
		}
		return Grant{}, ctx.Err()
	}
}

// Release returns a slot to the pool and immediately reassigns it. Releasing
// the zero Grant or a degraded one is a no-op — neither booked anything.
// Releasing a slot the sweep already revoked is a no-op too.
func (a *Arbiter) Release(g Grant) {
	if g.slot == nil {
		return
	}
	a.free(g.slot)
}

// free drops one holder and hands what it freed to the queue.
//
// One history line per holder: a slot Sweep already revoked is no longer on
// the books, so its late Release writes nothing.
func (a *Arbiter) free(h *holder) {
	a.mu.Lock()
	_, onBooks := a.holders[h]
	delete(a.holders, h)
	a.assignLocked()
	end := a.now()
	a.mu.Unlock()
	if onBooks {
		a.appendHistory(holdRecord(h, outcomeReleased, end))
	}
	a.fireNotify()
}

// dequeueLocked removes w from the queue if it is still there. Caller holds mu.
func (a *Arbiter) dequeueLocked(w *waiter) {
	for i, q := range a.queue {
		if q == w {
			a.queue = append(a.queue[:i], a.queue[i+1:]...)
			return
		}
	}
}

// assignLocked hands out every free slot it can. Caller holds mu.
//
// The shares are recomputed on each grant because each grant changes them:
// serving a group moves it towards its share, which is exactly what lets the
// next iteration pick someone else. Budget and queue are both small (a
// double-digit budget, a queue capped at MaxWaiters), so the recomputation is
// noise next to the round trip it decides.
func (a *Arbiter) assignLocked() {
	for len(a.holders) < a.cfg.Budget && len(a.queue) > 0 {
		groups := a.demandLocked()
		share := shares(fairnessInput(groups, a.cfg.Budget), a.cfg.Budget)
		held := make(map[string]int, len(groups))
		for _, g := range groups {
			held[g.Group] = g.Held
		}
		q := make([]Waiter, len(a.queue))
		for i, w := range a.queue {
			q[i] = Waiter{Group: w.group}
		}
		i := nextToServe(q, held, share)
		if i < 0 {
			return
		}
		w := a.queue[i]
		a.queue = append(a.queue[:i], a.queue[i+1:]...)
		at := a.now()
		h := &holder{group: w.group, label: w.label, since: at, enqueuedAt: w.since, waited: at.Sub(w.since)}
		a.holders[h] = struct{}{}
		w.slot = h
		close(w.ch)
	}
}

// demandLocked builds the per-group demand, ordered oldest-first — the order
// shares() needs to hand the division's remainder to the oldest groups. A
// group's age is that of its oldest holder or waiter; groups of the same age
// are ordered by name so the result never wobbles between two calls.
// Caller holds mu.
func (a *Arbiter) demandLocked() []GroupDemand {
	type acc struct {
		held    int
		waiting int
		oldest  time.Time
	}
	m := map[string]*acc{}
	touch := func(group string, at time.Time) *acc {
		e, ok := m[group]
		if !ok {
			e = &acc{oldest: at}
			m[group] = e
			return e
		}
		if at.Before(e.oldest) {
			e.oldest = at
		}
		return e
	}
	for h := range a.holders {
		touch(h.group, h.since).held++
	}
	for _, w := range a.queue {
		touch(w.group, w.since).waiting++
	}
	out := make([]GroupDemand, 0, len(m))
	for group, e := range m {
		out = append(out, GroupDemand{Group: group, Held: e.held, Waiting: e.waiting})
	}
	sort.Slice(out, func(i, j int) bool {
		oi, oj := m[out[i].Group].oldest, m[out[j].Group].oldest
		if !oi.Equal(oj) {
			return oi.Before(oj)
		}
		return out[i].Group < out[j].Group
	})
	return out
}

// fairnessInput adapts demand for the shares() calculation. A group with
// zero waiters is provably not asking for more right now, so its literal
// (possibly modest) demand is exactly what shares() should see — that is the
// "frees its surplus" case shares() itself implements. A group with at least
// one waiter has proven only a lower bound: it is blocked on the arbiter, not
// satisfied, and nothing tells us its want stops at the literal outstanding
// count. Capping it there would let it "free" a surplus it never actually
// offered, so its Waiting is read as a full budget's worth instead — enough
// to keep it out of shares()'s early-satisfied branch.
//
// A boosted group can still land in shares()'s satisfied branch — its
// demand is Held+budget, and the branch fires when demand <= the fair split
// of what remains — but only in one case: Held == 0, remaining == budget,
// and len(pending) == 1, all at once. That combination means the boosted
// group is the sole active group with nothing left to divide, so budget is
// exactly the right answer for it; the branch being reachable here is not a
// bug to guard against.
func fairnessInput(groups []GroupDemand, budget int) []GroupDemand {
	out := make([]GroupDemand, len(groups))
	for i, g := range groups {
		if g.Waiting > 0 {
			g.Waiting = budget
		}
		out[i] = g
	}
	return out
}

// Status reports the arbiter's whole state. Read-only.
func (a *Arbiter) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	groups := a.demandLocked()
	share := shares(fairnessInput(groups, a.cfg.Budget), a.cfg.Budget)
	out := Status{
		Budget:  a.cfg.Budget,
		Held:    len(a.holders),
		Waiting: len(a.queue),
		Groups:  make([]GroupStatus, 0, len(groups)),
	}
	for _, g := range groups {
		out.Groups = append(out.Groups, GroupStatus{
			Group: g.Group, Held: g.Held, Waiting: g.Waiting, Share: share[g.Group],
		})
	}
	return out
}

// Sweep revokes the *accounting* of every slot held past MaxHold and
// redistributes it.
//
// This is deliberately not an eviction. Unlike rammon, the arbiter kills
// nothing: it does not know what process is behind a connection and has no
// business guessing. It drops the slot from its books, publishes
// cpu.slot_revoked, and says nothing to the client, which keeps working to
// the end of its file. The guarantee it buys is narrow and worth it: one test
// that wedges cannot hold a slot for the rest of the day.
//
// No-op when MaxHold is zero or less.
func (a *Arbiter) Sweep() {
	a.mu.Lock()
	if a.cfg.MaxHold <= 0 {
		a.mu.Unlock()
		return
	}
	now := a.now()
	var revoked []*holder
	for h := range a.holders {
		if now.Sub(h.since) >= a.cfg.MaxHold {
			revoked = append(revoked, h)
		}
	}
	for _, h := range revoked {
		delete(a.holders, h)
	}
	if len(revoked) > 0 {
		a.assignLocked()
	}
	a.mu.Unlock()
	if len(revoked) > 0 {
		a.fireNotify()
	}

	for _, h := range revoked {
		held := now.Sub(h.since)
		a.logf("cpuslots: revoking the accounting of %s (%s) held %s — the client is not told and keeps running",
			h.group, h.label, held.Round(time.Second))
		a.appendHistory(holdRecord(h, outcomeRevoked, now))
		a.publishRevoked(h, held)
	}
}

// Run sweeps until ctx is cancelled. No-op when the arbiter is disabled or
// when no sweep interval is configured.
func (a *Arbiter) Run(ctx context.Context) {
	a.mu.Lock()
	budget, interval := a.cfg.Budget, a.cfg.SweepInterval
	a.mu.Unlock()
	if budget <= 0 || interval <= 0 {
		return
	}
	for {
		a.wait(ctx, interval)
		if ctx.Err() != nil {
			return
		}
		a.Sweep()
	}
}

// publishRevoked emits cpu.slot_revoked for SSE consumers (TUI, menu bar).
func (a *Arbiter) publishRevoked(h *holder, held time.Duration) {
	if a.bus == nil {
		return
	}
	a.bus.Publish(pigtail.Event{
		Type: pigtail.EventCPUSlotRevoked,
		Data: map[string]interface{}{
			"group":        h.group,
			"label":        h.label,
			"held_seconds": int(held.Seconds()),
		},
	})
}
