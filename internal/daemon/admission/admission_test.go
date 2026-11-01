package admission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeSampler serves whatever memory state a test sets.
type fakeSampler struct {
	mu    sync.Mutex
	mem   sysmem.Memory
	procs map[int]sysmem.Proc
	err   error
}

func (f *fakeSampler) ReadMemory() (sysmem.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mem, f.err
}

func (f *fakeSampler) Procs() (map[int]sysmem.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]sysmem.Proc{}
	for k, v := range f.procs {
		out[k] = v
	}
	return out, nil
}

func (f *fakeSampler) set(avail uint64, pressure int) {
	f.mu.Lock()
	f.mem.Available, f.mem.Pressure = avail, pressure
	f.mu.Unlock()
}

// setCompress sets the cumulative compressor counters (bytes).
func (f *fakeSampler) setCompress(comp, decomp uint64) {
	f.mu.Lock()
	f.mem.Compressions, f.mem.Decompressions = comp, decomp
	f.mu.Unlock()
}

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testConfig: explicit, no platform probing, the floor at 6 GiB, and a Stale
// of zero so every Enqueue reads the fake sampler afresh.
func testConfig() Config {
	return Config{
		MaxJobs:               6,
		FloorBytes:            6 * gib,
		PressureLevel:         2,
		PressureCriticalLevel: 4,
		PressureFloorFactor:   1.5,
		SwapoutBytesPerSec:    4 << 20,
		// Off by default in tests: the ones that exercise it set it, and the
		// others are about what the gates do before it ever fires.
		MaxStarve:  0,
		MaxHold:    time.Hour,
		SwapWindow: 10 * time.Second,
	}
}

type harness struct {
	c     *Controller
	s     *fakeSampler
	clock *fakeClock
	bus   *events.Bus
}

func newHarness(t *testing.T, cfg Config, avail uint64) *harness {
	t.Helper()
	s := &fakeSampler{mem: sysmem.Memory{Available: avail, Pressure: 1, Total: 64 * gib}}
	clk := &fakeClock{t: time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC)}
	bus := events.New()
	c := New(bus,
		WithConfig(cfg),
		WithSampler(s),
		WithClock(clk.now),
		WithLogger(func(string, ...any) {}),
	)
	return &harness{c: c, s: s, clock: clk, bus: bus}
}

func ready(t *Ticket) bool {
	select {
	case <-t.Ready():
		return true
	default:
		return false
	}
}

func (h *harness) enqueue(t *testing.T, group, class string) *Ticket {
	t.Helper()
	tk, err := h.c.Enqueue(Request{Group: group, Class: class, Repo: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestAdmitsWhileItFitsThenQueues(t *testing.T) {
	// 11 GiB available, floor 6: room for two 1.5 GiB tsc reservations plus
	// headroom, not for a 3 GiB jest on top of them.
	h := newHarness(t, testConfig(), 11*gib)
	a := h.enqueue(t, "wt-a", "tsc")
	b := h.enqueue(t, "wt-b", "tsc")
	c := h.enqueue(t, "wt-c", "jest")
	if !ready(a) || !ready(b) {
		t.Fatal("the first two must be admitted at once")
	}
	if ready(c) {
		t.Fatal("jest must wait: 11 - 3 reserved - 3 needed < 6 floor")
	}
	if info := h.c.Position(c); info.Position != 1 || info.Reason != ReasonMemory || info.Running != 2 {
		t.Fatalf("queue info = %+v", info)
	}
	h.c.Release(h.c.Grant(a))
	if !ready(c) {
		t.Fatal("releasing a reservation must admit the head")
	}
}

func TestAntiDeadlockAdmitsWhenNothingIsHeld(t *testing.T) {
	h := newHarness(t, testConfig(), 2*gib) // far under the floor
	h.s.set(2*gib, sysmem.PressureCritical)
	a := h.enqueue(t, "wt-a", "jest")
	if !ready(a) {
		t.Fatal("with nothing held the head must be admitted whatever memory says")
	}
	b := h.enqueue(t, "wt-b", "tsc")
	if ready(b) {
		t.Fatal("the second one must wait: the machine is under pressure")
	}
	if got := h.c.Position(b).Reason; got != ReasonPressure {
		t.Fatalf("reason = %q, want pressure", got)
	}
	h.c.Release(h.c.Grant(a))
	if !ready(b) {
		t.Fatal("once the only lease is gone, the head runs — serialised, never blocked")
	}
}

func TestMaxJobsCeiling(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 2
	h := newHarness(t, cfg, 60*gib)
	h.enqueue(t, "a", "tsc")
	h.enqueue(t, "b", "tsc")
	c := h.enqueue(t, "c", "tsc")
	if ready(c) || h.c.Position(c).Reason != ReasonMaxJobs {
		t.Fatalf("third job must wait on the job ceiling, got %+v", h.c.Position(c))
	}
}

func TestFairnessBetweenWorktrees(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 2
	h := newHarness(t, cfg, 60*gib)
	a1 := h.enqueue(t, "busy", "tsc")
	h.enqueue(t, "busy", "tsc")
	a3 := h.enqueue(t, "busy", "tsc") // queued first
	q := h.enqueue(t, "quiet", "tsc") // queued second
	h.c.Release(h.c.Grant(a1))
	if !ready(q) || ready(a3) {
		t.Fatal("the freed job must go to the group holding the fewest leases, not the oldest waiter")
	}
}

func TestNoBackfill(t *testing.T) {
	h := newHarness(t, testConfig(), 12*gib)
	h.enqueue(t, "a", "tsc")                                                // 1.5 reserved
	big := h.enqueue(t, "b", "jest")                                        // 12 - 1.5 - 3 = 7.5 >= 6: fits
	huge := h.enqueue(t, "c", "jest")                                       // 12 - 4.5 - 3 = 4.5 < 6: waits
	small, _ := h.c.Enqueue(Request{Group: "d", Class: "tsc", CostHint: 1}) // would fit alone
	if !ready(big) || ready(huge) {
		t.Fatal("setup: second jest must wait")
	}
	if ready(small) {
		t.Fatal("no backfill: a small job must not overtake a waiting head")
	}
}

func TestCancel(t *testing.T) {
	h := newHarness(t, testConfig(), 12*gib)
	h.enqueue(t, "a", "tsc")
	h.enqueue(t, "b", "tsc")
	waiting := h.enqueue(t, "c", "jest")
	next := h.enqueue(t, "d", "tsc")
	if ready(next) {
		t.Fatal("setup: d waits behind c")
	}
	h.c.Cancel(waiting)
	if !ready(next) {
		t.Fatal("cancelling the head must let the next one be considered")
	}
	if st := h.c.Status(); len(st.Queue) != 0 || len(st.Leases) != 3 {
		t.Fatalf("status after cancel: %d queued, %d leases", len(st.Queue), len(st.Leases))
	}
	// Cancelling a ticket that was granted in the meantime releases it.
	h.c.Cancel(next)
	if st := h.c.Status(); len(st.Leases) != 2 {
		t.Fatalf("a granted-then-cancelled ticket must be released, %d leases left", len(st.Leases))
	}
}

func TestTickChargesMemAndLearnsPeakOnRelease(t *testing.T) {
	cfg := testConfig()
	h := newHarness(t, cfg, 40*gib)
	h.c.costs = newCostTable(filepath.Join(t.TempDir(), "costs.json"))
	sub := h.bus.Subscribe(string(pigtail.EventAdmissionReleased))
	defer sub.Close()

	for i := 0; i < 3; i++ {
		tk, err := h.c.Enqueue(Request{Group: "a", Class: "tsc", Repo: "repo", PID: 100})
		if err != nil || !ready(tk) {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		h.s.mu.Lock()
		h.s.procs = map[int]sysmem.Proc{
			100: {PID: 100, PPID: 1, MemBytes: 100 << 20},
			101: {PID: 101, PPID: 100, MemBytes: 900 << 20}, // tsc under the admit wrapper
		}
		h.s.mu.Unlock()
		h.c.Tick()
		st := h.c.Status()
		if len(st.Leases) != 1 || st.Leases[0].Mem != 1000<<20 {
			t.Fatalf("lease not charged with its tree: %+v", st.Leases)
		}
		h.c.Release(h.c.Grant(tk))
		select {
		case e := <-sub.C:
			if e.Data["peak_bytes"] != uint64(1000<<20) {
				t.Fatalf("released event peak = %v", e.Data["peak_bytes"])
			}
		case <-time.After(time.Second):
			t.Fatal("no admission.released event")
		}
	}
	// Three 1000 MiB peaks: the learned cost is 1.2 × that, above half the
	// 1.5 GiB prior.
	margin := costMargin
	if got, want := h.c.costFor(Request{Class: "tsc", Repo: "repo"}), uint64(margin*float64(1000<<20)); got != want {
		t.Fatalf("learned cost = %d, want %d", got, want)
	}
	reloaded := newCostTable(h.c.costs.path)
	if err := reloaded.load(); err != nil || reloaded.estimate("tsc", "repo", 0, 0, 0) == 0 {
		t.Fatalf("costs were not persisted on release: %v", err)
	}
}

func TestSwapoutGate(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib)
	h.enqueue(t, "a", "tsc")
	// 64 MiB swapped out over 2 s = 32 MiB/s, well above the 4 MiB/s gate.
	h.s.mu.Lock()
	h.s.mem.Swapouts = 1 << 30
	h.s.mu.Unlock()
	h.c.refresh()
	h.clock.advance(2 * time.Second)
	h.s.mu.Lock()
	h.s.mem.Swapouts = 1<<30 + 64<<20
	h.s.mu.Unlock()
	b := h.enqueue(t, "b", "tsc")
	if ready(b) || h.c.Position(b).Reason != ReasonSwapout {
		t.Fatalf("active swapping must hold the queue, got %+v", h.c.Position(b))
	}
	// Swapping stops: past the window, the rate falls to zero.
	h.clock.advance(20 * time.Second)
	h.c.Tick()
	h.clock.advance(1 * time.Second)
	h.c.Tick()
	if !ready(b) {
		t.Fatalf("once swapping stops the head must be admitted, got %+v", h.c.Position(b))
	}
}

func TestSweepRevokesAccountingOnly(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	cfg.MaxHold = 30 * time.Minute
	h := newHarness(t, cfg, 40*gib)
	a := h.enqueue(t, "a", "tsc")
	b := h.enqueue(t, "b", "tsc")
	h.clock.advance(31 * time.Minute)
	h.c.Sweep()
	if !ready(b) {
		t.Fatal("a lease held past MaxHold must stop counting")
	}
	h.c.Release(h.c.Grant(a)) // the late release of a revoked lease is a no-op
	if st := h.c.Status(); len(st.Leases) != 1 {
		t.Fatalf("leases = %d, want only b's", len(st.Leases))
	}
}

func TestDisabledAndOverflow(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 0
	h := newHarness(t, cfg, 40*gib)
	if _, err := h.c.Enqueue(Request{Group: "a"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}

	cfg = testConfig()
	cfg.MaxJobs = 1
	cfg.MaxWaiters = 1
	h = newHarness(t, cfg, 40*gib)
	h.enqueue(t, "a", "tsc")
	h.enqueue(t, "b", "tsc") // queued: fills the queue
	over := h.enqueue(t, "c", "tsc")
	if !ready(over) || !h.c.Grant(over).Degraded {
		t.Fatal("over the queue ceiling a degraded grant must be handed out at once")
	}
	h.c.Release(h.c.Grant(over)) // no-op, must not panic
}

func TestCostHintFlooredAtHalfPrior(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib)
	if got := h.c.costFor(Request{Class: "jest", CostHint: 1}); got != 3*gib/2 {
		t.Fatalf("got %d, want half the 3 GiB prior", got)
	}
	if got := h.c.costFor(Request{Class: "jest", CostHint: 8 * gib}); got != 8*gib {
		t.Fatalf("a larger hint is taken at its word, got %d", got)
	}
}

func TestReadErrorKeepsLastReading(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib)
	h.c.refresh()
	h.s.mu.Lock()
	h.s.err = errors.New("vm_stat: boom")
	h.s.mu.Unlock()
	h.c.refresh()
	if got := h.c.Status().Reading.Available; got != 40*gib {
		t.Fatalf("a failed read must keep the last good reading, got %d", got)
	}
}

func TestRefreshIfStaleOnStatusPath(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib)
	if got := h.c.Status().Reading.Available; got != 0 {
		t.Fatalf("setup: no reading yet, got %d", got)
	}
	h.c.RefreshIfStale()
	if got := h.c.Status().Reading.Available; got != 40*gib {
		t.Fatalf("RefreshIfStale must take a first reading, got %d", got)
	}
}

// The queue wait is the number this feature is judged on, so it must be
// readable from outside: on the grant, in the status, and in the event.
func TestWaitedIsObservable(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	h := newHarness(t, cfg, 40*gib)
	sub := h.bus.Subscribe(string(pigtail.EventAdmissionGranted))
	defer sub.Close()

	first := h.enqueue(t, "a", "tsc")
	second := h.enqueue(t, "b", "tsc")
	if e := <-sub.C; e.Data["waited_ms"] != int64(0) {
		t.Fatalf("an immediate grant waited 0, got %v", e.Data["waited_ms"])
	}
	h.clock.advance(42 * time.Second)
	h.c.Release(h.c.Grant(first))
	if !ready(second) {
		t.Fatal("setup: the second must be admitted")
	}
	if got := h.c.Grant(second).Waited; got != 42*time.Second {
		t.Fatalf("Grant.Waited = %s, want 42s", got)
	}
	st := h.c.Status()
	if len(st.Leases) != 1 || st.Leases[0].Waited != 42*time.Second {
		t.Fatalf("status lease = %+v", st.Leases)
	}
	e := <-sub.C
	if e.Data["waited_ms"] != int64(42000) {
		t.Fatalf("admission.granted waited_ms = %v, want 42000", e.Data["waited_ms"])
	}
}

// TestClassLimitSerialisesBrowserRuns is the controller-level half of
// TestVerdictClassLimit: with memory to spare, the second browser run must
// still wait for the first, and a tsc alongside must not.
func TestClassLimitSerialisesBrowserRuns(t *testing.T) {
	cfg := testConfig()
	cfg.ClassLimits = map[string]int{"vitest-browser": 1}
	h := newHarness(t, cfg, 60*gib) // memory is never the constraint here

	a := h.enqueue(t, "wt-a", "vitest-browser")
	b := h.enqueue(t, "wt-b", "vitest-browser")
	other := h.enqueue(t, "wt-c", "tsc")

	if !ready(a) {
		t.Fatal("the first browser run must be admitted at once")
	}
	if ready(b) {
		t.Fatal("the second browser run must wait for the first")
	}
	if !ready(other) {
		t.Fatal("another class must not be held back by the browser cap")
	}
	if info := h.c.Position(b); info.Reason != ReasonClassLimit {
		t.Fatalf("queued for %q, want %q", info.Reason, ReasonClassLimit)
	}

	h.c.Release(h.c.Grant(a))
	if !ready(b) {
		t.Fatal("releasing the first browser run must admit the second")
	}
}

// TestLearnedCostsSplitByWorkerCount: a browser run's peak scales with the
// worker count pigtail itself chose, so one key per (class, repo) would make
// the EWMA chase a moving target — a 2-worker run teaching the estimate for
// an 8-worker one.
func TestLearnedCostsSplitByWorkerCount(t *testing.T) {
	tbl := newCostTable("")
	for i := 0; i < minObservations; i++ {
		tbl.observe("vitest-browser", "repo", 2, 4*gib)
		tbl.observe("vitest-browser", "repo", 8, 16*gib)
	}
	small := tbl.estimate("vitest-browser", "repo", 2, gib, 0)
	big := tbl.estimate("vitest-browser", "repo", 8, gib, 0)
	if small >= big {
		t.Fatalf("2-worker estimate %d must stay below the 8-worker one %d", small, big)
	}
	if unseen := tbl.estimate("vitest-browser", "repo", 4, 3*gib, 0); unseen != 3*gib {
		t.Fatalf("an unseen worker count must fall back to the prior, got %d", unseen)
	}
}

// TestCostPriorYieldsToWhatWasLearned: a client-computed prior sizes the
// first runs of a shape the daemon has never seen; it must not keep
// overriding the observed peaks afterwards, or the browser class would never
// benefit from the learning every other class gets.
func TestCostPriorYieldsToWhatWasLearned(t *testing.T) {
	h := newHarness(t, testConfig(), 60*gib)
	req := Request{Group: "wt", Class: "vitest-browser", Repo: "repo", Workers: 4, CostPrior: 9 * gib}
	if got := h.c.costFor(req); got != 9*gib {
		t.Fatalf("before learning, the client prior must stand: got %d", got)
	}
	for i := 0; i < minObservations; i++ {
		h.c.costs.observe("vitest-browser", "repo", 4, 2*gib)
	}
	margin := costMargin
	if got := h.c.costFor(req); got != uint64(margin*float64(2*gib)) {
		t.Fatalf("after learning, the observed peak must win: got %d", got)
	}
	// An explicit --cost-mb stays an override, learning or not.
	req.CostHint = 12 * gib
	if got := h.c.costFor(req); got != 12*gib {
		t.Fatalf("--cost-mb must override: got %d", got)
	}
}

// TestWarnBrakesButDoesNotStop reproduces the state measured in production on
// 2026-09-24: one lease held, the kernel at warn, no swapping, 13 GiB
// available — and four jobs queued behind it, one of them a user's session.
// Warn alone must not stop the queue.
func TestWarnBrakesButDoesNotStop(t *testing.T) {
	h := newHarness(t, testConfig(), 13*gib)
	h.s.set(13*gib, sysmem.PressureWarn)
	a := h.enqueue(t, "wt-a", "tsc")
	b := h.enqueue(t, "wt-b", "tsc")
	if !ready(a) || !ready(b) {
		t.Fatalf("warn without swapping must keep admitting: a=%v b=%v", ready(a), ready(b))
	}
	// The brake is real, though: the reinforced floor (6 × 1.5 = 9 GiB) stops
	// the queue earlier than the plain one would have.
	c := h.enqueue(t, "wt-c", "jest")
	if ready(c) {
		t.Fatal("the reinforced floor must still hold something back under warn")
	}
	if got := h.c.Position(c).Reason; got != ReasonPressure {
		t.Fatalf("reason = %q, want pressure — the caller must see it is the margin, not the machine", got)
	}
}

// TestWarnWithSwappingStops: the difference between a machine that is coping
// and one that is thrashing is whether it is actually paging.
func TestWarnWithSwappingStops(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib)
	h.enqueue(t, "wt-a", "tsc")
	h.s.mu.Lock()
	h.s.mem.Pressure = sysmem.PressureWarn
	h.s.mu.Unlock()
	// Two samples a second apart: a rate barely above zero, which is enough
	// under warn and would not be when calm.
	h.s.mu.Lock()
	h.s.mem.Swapouts = 1 << 20
	h.s.mu.Unlock()
	h.c.Tick()
	h.clock.advance(time.Second)
	h.s.mu.Lock()
	h.s.mem.Swapouts = 2 << 20
	h.s.mu.Unlock()
	h.c.Tick()
	b := h.enqueue(t, "wt-b", "tsc")
	if ready(b) {
		t.Fatal("warn plus active swapping must hold the queue")
	}
	if got := h.c.Position(b).Reason; got != ReasonSwapout {
		t.Fatalf("reason = %q, want swapout", got)
	}
}

// TestStarvationGuaranteeAdmitsTheLongestWait: the job ceiling holds a waiter
// back, and time alone eventually lets it through — the guarantee that was
// missing when four jobs sat behind one lease for minutes.
func TestStarvationGuaranteeAdmitsTheLongestWait(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	cfg.MaxStarve = 3 * time.Minute
	h := newHarness(t, cfg, 40*gib)
	h.enqueue(t, "wt-a", "tsc")
	b := h.enqueue(t, "wt-b", "tsc")
	if ready(b) {
		t.Fatal("the job ceiling must hold the second one at first")
	}
	h.clock.advance(2 * time.Minute)
	h.c.Tick()
	if ready(b) {
		t.Fatal("two minutes is not starving yet")
	}
	h.clock.advance(2 * time.Minute)
	h.c.Tick()
	if !ready(b) {
		t.Fatal("past MaxStarve the waiter must be admitted, ceiling or not")
	}
	// And it is booked like any other lease, not waved through unaccounted.
	if g := h.c.Grant(b); g.ID == "" || g.Degraded || g.Waited < 3*time.Minute {
		t.Fatalf("grant = %+v, want a real lease with its wait recorded", g)
	}
}

// TestStarvationStillOwesTheFloor: the guarantee lifts the gates that are
// about policy, never the one that is about the machine actually being full.
func TestStarvationStillOwesTheFloor(t *testing.T) {
	cfg := testConfig()
	cfg.MaxStarve = 3 * time.Minute
	h := newHarness(t, cfg, 8*gib)
	h.enqueue(t, "wt-a", "jest") // reserves 3 GiB, leaving 5 under the 6 GiB floor
	b := h.enqueue(t, "wt-b", "jest")
	h.clock.advance(10 * time.Minute)
	h.c.Tick()
	if ready(b) {
		t.Fatal("no wait is long enough to invent memory the machine does not have")
	}
	if got := h.c.Position(b).Reason; got != ReasonMemory {
		t.Fatalf("reason = %q, want memory", got)
	}
}

// TestLoadCostsSaysWhyLegacyCostsAreDropped: the reset after the RSS →
// footprint switch must be visible in the daemon log, not a silent loss.
func TestLoadCostsSaysWhyLegacyCostsAreDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission-costs.json")
	if err := os.WriteFile(path, []byte(`{"tsc\u0000repo":{"ewma_bytes":1,"n":9}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs []string
	c := New(events.New(), WithConfig(testConfig()), WithCostsPath(path),
		WithLogger(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }))
	c.LoadCosts()
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "predate format 2") || strings.Contains(joined, "unreadable") {
		t.Fatalf("log = %q, want the legacy-format message", joined)
	}
	fresh := New(events.New(), WithConfig(testConfig()))
	if got, want := c.costFor(Request{Class: "tsc", Repo: "repo"}), fresh.costFor(Request{Class: "tsc", Repo: "repo"}); got != want {
		t.Fatalf("cost after a legacy file = %d, want the prior %d", got, want)
	}
}

func TestDefaultsCompressGate(t *testing.T) {
	if got := Defaults().CompressBytesPerSec; got != 16<<20 {
		t.Fatalf("default compress gate = %v, want 16 MiB/s", got)
	}
}

// TestCompressGate: the 2026-10-21 saturation — no swapping, normal pressure,
// the compressor churning — holds the queue, and lets go once it calms.
func TestCompressGate(t *testing.T) {
	cfg := testConfig()
	cfg.CompressBytesPerSec = 16 << 20
	h := newHarness(t, cfg, 40*gib)
	h.s.setCompress(1*gib, 1*gib)
	h.enqueue(t, "a", "tsc")
	h.clock.advance(2 * time.Second)
	h.s.setCompress(1*gib+40<<20, 1*gib+40<<20) // 80 MiB in 2 s = 40 MiB/s
	b := h.enqueue(t, "b", "tsc")
	if ready(b) || h.c.Position(b).Reason != ReasonCompress {
		t.Fatalf("compressor traffic must hold the queue, got %+v", h.c.Position(b))
	}
	if got := h.c.Status().Reading.CompressBytesPerSec; got != 40<<20 {
		t.Fatalf("compress rate = %v, want 40 MiB/s", got)
	}
	h.clock.advance(20 * time.Second)
	h.c.Tick()
	h.clock.advance(1 * time.Second)
	h.c.Tick()
	if !ready(b) {
		t.Fatalf("once the compressor calms the head must be admitted, got %+v", h.c.Position(b))
	}
}

func TestStatusReportsTheCompressor(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib)
	h.s.mu.Lock()
	h.s.mem.CompressorBytes, h.s.mem.UnusedBytes = 19*gib, 1*gib
	h.s.mu.Unlock()
	h.c.refresh()
	if r := h.c.Status().Reading; r.CompressorBytes != 19*gib || r.UnusedBytes != 1*gib {
		t.Fatalf("reading = %+v", r)
	}
}

// focusEnqueue enqueues a request flagged as focus or not.
func (h *harness) focusEnqueue(t *testing.T, group, class string, focus bool) *Ticket {
	t.Helper()
	tk, err := h.c.Enqueue(Request{Group: group, Class: class, Repo: "repo", Focus: focus})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestFocusWaiterAdmittedBeforeEarlierNonFocus(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	h := newHarness(t, cfg, 60*gib)
	held := h.enqueue(t, "x", "tsc")
	a := h.focusEnqueue(t, "a", "tsc", false)
	b := h.focusEnqueue(t, "b", "tsc", true)
	h.c.Release(h.c.Grant(held))
	if !ready(b) || ready(a) {
		t.Fatal("the focus waiter must be admitted before the earlier non-focus one")
	}
}

// The lease is held by the focus waiter's own group: focus still goes first
// whatever the held-lease counts.
func TestFocusWaiterAdmittedFirstWhenItsGroupHoldsTheLease(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	h := newHarness(t, cfg, 60*gib)
	held := h.enqueue(t, "b", "tsc")
	a := h.focusEnqueue(t, "a", "tsc", false)
	b := h.focusEnqueue(t, "b", "tsc", true)
	h.c.Release(h.c.Grant(held))
	if !ready(b) || ready(a) {
		t.Fatal("the focus waiter must beat the non-focus one although its group held the lease")
	}
}

func TestStarvedNonFocusBeatsFocus(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	cfg.MaxStarve = 3 * time.Minute
	h := newHarness(t, cfg, 60*gib)
	held := h.enqueue(t, "x", "tsc")
	a := h.focusEnqueue(t, "a", "tsc", false)
	h.clock.advance(4 * time.Minute)
	b := h.focusEnqueue(t, "b", "tsc", true)
	h.c.Release(h.c.Grant(held))
	if !ready(a) || ready(b) {
		t.Fatal("a non-focus waiter past MaxStarve must beat a focus waiter")
	}
}

// TestFocusHeadBlocksNonFocusUntilStarvation: a focus head that does not fit
// holds the queue (no backfill); the non-focus waiter gets through only by
// the starvation guarantee.
func TestFocusHeadBlocksNonFocusUntilStarvation(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 2
	cfg.MaxStarve = 3 * time.Minute
	h := newHarness(t, cfg, 11*gib)
	a := h.enqueue(t, "a", "tsc") // 1.5 reserved
	h.enqueue(t, "b", "jest")     // fits, then MaxJobs is reached
	small, err := h.c.Enqueue(Request{Group: "d", Class: "tsc", CostHint: 1, Repo: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	huge, err := h.c.Enqueue(Request{Group: "c", Class: "jest", Repo: "repo", Focus: true})
	if err != nil {
		t.Fatal(err)
	}
	h.c.Release(h.c.Grant(a))
	if ready(huge) || ready(small) {
		t.Fatal("setup: the focus head does not fit and nobody may overtake it")
	}
	h.clock.advance(4 * time.Minute)
	h.c.Tick()
	if !ready(small) {
		t.Fatal("past MaxStarve the non-focus waiter must get through")
	}
}

func TestStatusQueueCarriesFocus(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	h := newHarness(t, cfg, 60*gib)
	h.enqueue(t, "x", "tsc")
	h.focusEnqueue(t, "a", "tsc", false)
	h.focusEnqueue(t, "b", "tsc", true)
	st := h.c.Status()
	if len(st.Queue) != 2 || st.Queue[0].Focus || !st.Queue[1].Focus {
		t.Fatalf("queue = %+v", st.Queue)
	}
}

// calmConfig: the compressor gate at 16 MiB/s, the calm rule on for build and
// vitest-browser over 60 s, and the starvation guarantee at 3 min.
func calmConfig() Config {
	cfg := testConfig()
	cfg.CompressBytesPerSec = 16 << 20
	cfg.CompressCalmWindow = time.Minute
	cfg.CompressCalmClasses = []string{"build", "vitest-browser"}
	cfg.MaxStarve = 3 * time.Minute
	return cfg
}

// churn moves the clock in 2 s ticks for d, adding bytesPerSec of compressor
// traffic (decompressions) per second, with a Tick after each step.
func (h *harness) churn(t *testing.T, d time.Duration, bytesPerSec uint64) {
	t.Helper()
	for elapsed := time.Duration(0); elapsed < d; elapsed += 2 * time.Second {
		h.clock.advance(2 * time.Second)
		h.s.mu.Lock()
		h.s.mem.Decompressions += 2 * bytesPerSec
		h.s.mu.Unlock()
		h.c.Tick()
	}
}

func TestDefaultsCompressCalm(t *testing.T) {
	d := Defaults()
	if d.CompressCalmWindow != time.Minute || d.CompressCalmMaxStarve != 0 {
		t.Fatalf("calm window %v, max starve %v; want 1m and never", d.CompressCalmWindow, d.CompressCalmMaxStarve)
	}
	if got := strings.Join(d.CompressCalmClasses, ","); got != "build,vitest-browser" {
		t.Fatalf("calm classes = %q", got)
	}
}

// The 2026-10-27 shape: a burst, then ten calm seconds. A tsc would start
// there; a build waits until the burst has left the 60 s window.
func TestCompressCalmHoldsABuildUntilTheBurstLeavesTheWindow(t *testing.T) {
	h := newHarness(t, calmConfig(), 60*gib)
	h.s.setCompress(1*gib, 1*gib)
	h.enqueue(t, "a", "tsc") // one lease held: the gates apply from now on
	h.churn(t, 2*time.Second, 600<<20)
	b := h.enqueue(t, "b", "build")
	if ready(b) || h.c.Position(b).Reason != ReasonCompressCalm {
		t.Fatalf("a build must wait out the burst, got %+v", h.c.Position(b))
	}
	h.churn(t, 12*time.Second, 0)
	if r := h.c.Status().Reading; r.CompressBytesPerSec != 0 || r.CompressCalmBytesPerSec <= 16<<20 {
		t.Fatalf("reading = %+v, want a calm 10 s window and a loaded 60 s one", r)
	}
	if ready(b) || h.c.Position(b).Reason != ReasonCompressCalm {
		t.Fatalf("ten calm seconds are not durable calm, got %+v", h.c.Position(b))
	}
	h.churn(t, 50*time.Second, 0)
	if !ready(b) {
		t.Fatalf("once the burst left the window the build must start, got %+v", h.c.Position(b))
	}
}

// D1: past MaxStarve a build is not released from the compressor; a starving
// tsc still is, a fresh tsc waits behind the build, and durable calm admits
// the build first.
func TestCompressCalmIsNotLiftedByStarvation(t *testing.T) {
	h := newHarness(t, calmConfig(), 60*gib)
	h.s.setCompress(1*gib, 1*gib)
	h.enqueue(t, "a", "tsc")
	h.churn(t, 2*time.Second, 40<<20)
	b := h.enqueue(t, "b", "build")
	h.clock.advance(time.Second)
	c := h.enqueue(t, "c", "tsc")
	h.churn(t, 4*time.Minute, 40<<20)
	if !ready(c) {
		t.Fatalf("a starving tsc must still pass the compressor, got %+v", h.c.Position(c))
	}
	if ready(b) || h.c.Position(b).Reason != ReasonCompressCalm {
		t.Fatalf("a starving build must keep waiting on the compressor, got %+v", h.c.Position(b))
	}
	h.churn(t, 6*time.Minute, 40<<20)
	if ready(b) {
		t.Fatal("ten minutes of overflow and the build must still wait")
	}
	d := h.enqueue(t, "d", "tsc")
	h.churn(t, 12*time.Second, 0) // 10 s calm, 60 s still loaded
	if ready(d) || ready(b) {
		t.Fatalf("a fresh tsc does not pass a build held for calm: b %+v d %+v", h.c.Position(b), h.c.Position(d))
	}
	h.churn(t, 60*time.Second, 0)
	if !ready(b) || !ready(d) {
		t.Fatalf("durable calm admits the build, then the tsc: b %+v d %+v", h.c.Position(b), h.c.Position(d))
	}
}

// diskFake is a disk reader a test steers.
type diskFake struct {
	mu   sync.Mutex
	free uint64
	err  error
}

func (d *diskFake) read() (uint64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.free, d.err
}

func (d *diskFake) set(free uint64, err error) {
	d.mu.Lock()
	d.free, d.err = free, err
	d.mu.Unlock()
}

func newDiskHarness(t *testing.T, floor uint64, d *diskFake) *harness {
	t.Helper()
	cfg := testConfig()
	cfg.DiskFloorBytes = floor
	h := newHarness(t, cfg, 40*gib)
	WithDiskReader(d.read)(h.c)
	return h
}

func TestDiskUnderFloorHoldsAnEmptyMachine(t *testing.T) {
	d := &diskFake{free: 10 * gib}
	h := newDiskHarness(t, 20*gib, d)
	a := h.enqueue(t, "wt-a", "tsc")
	if ready(a) {
		t.Fatal("under the disk floor nothing is admitted, even on an empty machine")
	}
	if got := h.c.Position(a).Reason; got != ReasonDisk {
		t.Fatalf("reason = %q, want disk", got)
	}
	st := h.c.Status()
	if !st.Reading.DiskKnown || st.Reading.DiskFree != 10*gib {
		t.Fatalf("status reading = %+v", st.Reading)
	}
	d.set(50*gib, nil)
	h.c.Tick()
	if !ready(a) {
		t.Fatal("once the disk is back above the floor the head is admitted")
	}
}

// V1: a read error after a reading below the floor must not leave the gate
// open on the stale value, and must not hold the queue either: unknown never
// gates, so the head is admitted.
func TestDiskReadErrorAfterLowReadingOpensTheGate(t *testing.T) {
	d := &diskFake{free: 10 * gib}
	h := newDiskHarness(t, 20*gib, d)
	a := h.enqueue(t, "wt-a", "tsc")
	if ready(a) {
		t.Fatal("setup: the low reading must hold the queue")
	}
	d.set(0, errors.New("statfs: boom"))
	h.c.Tick()
	if st := h.c.Status(); st.Reading.DiskKnown || st.Reading.DiskFree != 0 {
		t.Fatalf("a failed disk read must clear the reading, got %+v", st.Reading)
	}
	if !ready(a) {
		t.Fatal("a disk read error must open the gate (fail-open), not keep the old low value")
	}
}

// The disk is read even when the memory reading failed.
func TestDiskIsReadWhenMemoryReadFails(t *testing.T) {
	d := &diskFake{free: 10 * gib}
	h := newDiskHarness(t, 20*gib, d)
	h.s.mu.Lock()
	h.s.err = errors.New("vm_stat: boom")
	h.s.mu.Unlock()
	h.c.refresh()
	st := h.c.Status()
	if !st.Reading.DiskKnown || st.Reading.DiskFree != 10*gib {
		t.Fatalf("disk must be read though memory failed, got %+v", st.Reading)
	}
}

func TestNoDiskReaderMeansUnknown(t *testing.T) {
	cfg := testConfig()
	cfg.DiskFloorBytes = 20 * gib
	h := newHarness(t, cfg, 40*gib)
	a := h.enqueue(t, "wt-a", "tsc")
	if !ready(a) {
		t.Fatal("without a disk reader the gate stays open")
	}
	if h.c.Status().Reading.DiskKnown {
		t.Fatal("no reader: disk is unknown")
	}
}

func TestDiskReadErrorLogsOnce(t *testing.T) {
	d := &diskFake{err: errors.New("statfs: boom")}
	cfg := testConfig()
	cfg.DiskFloorBytes = 20 * gib
	var n int
	h := newHarness(t, cfg, 40*gib)
	WithLogger(func(string, ...any) { n++ })(h.c)
	WithDiskReader(d.read)(h.c)
	h.c.refresh()
	h.c.refresh()
	h.c.refresh()
	if n != 1 {
		t.Fatalf("a failing disk read logs once, logged %d", n)
	}
}
