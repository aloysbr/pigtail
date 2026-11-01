package admission

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/heavycmd"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ErrDisabled is what Enqueue returns when admission is switched off
// (-admit-max-jobs=0). Clients treat it exactly like an absent daemon: they
// run their command immediately.
var ErrDisabled = errors.New("admission: controller disabled")

// Config tunes the controller.
type Config struct {
	// MaxJobs caps concurrent leases. Zero or less disables the controller
	// entirely — that is the daemon-side kill switch, as -cpu-slots=0 is for
	// the CPU arbiter.
	MaxJobs int
	// FloorBytes is the available memory every admission must leave behind,
	// after the in-flight reservations and the newcomer's own cost.
	FloorBytes uint64
	// DiskFloorBytes is the free disk space every admission must find, even
	// on an empty machine. Zero turns the gate off. The daemon fills it from
	// -admit-disk-floor-gb.
	DiskFloorBytes uint64
	// PressureLevel is where the kernel's pressure starts braking admissions
	// (2 = warn): the queue keeps moving, with a tighter swap gate and more
	// headroom demanded. Zero turns the brake off.
	PressureLevel int
	// PressureCriticalLevel is where pressure stops admissions outright
	// (4 = critical). Zero turns that gate off.
	PressureCriticalLevel int
	// PressureFloorFactor is the extra headroom demanded while braking, as a
	// multiple of FloorBytes. 0 or 1 means none.
	PressureFloorFactor float64
	// MaxStarve is how long a waiter may be held back before only the plain
	// memory floor still stands between it and a lease. Zero disables the
	// guarantee, which is what produced the 2026-09-24 stall.
	MaxStarve time.Duration
	// SwapoutBytesPerSec holds the queue while the machine swaps out faster
	// than this. Zero turns the gate off.
	SwapoutBytesPerSec float64
	// CompressBytesPerSec holds the queue while compressions + decompressions
	// run faster than this. Zero turns the gate off.
	CompressBytesPerSec float64
	// CompressCalmWindow is the span over which CompressCalmClasses must also
	// find compressor traffic under CompressBytesPerSec before they start.
	// Zero or less turns that rule off: they are gated like any other class.
	CompressCalmWindow time.Duration
	// CompressCalmClasses are the classes held to it — heavycmd.CalmClasses()
	// by default.
	CompressCalmClasses []string
	// CompressCalmMaxStarve releases such a waiter from the rule after this
	// wait. Zero, the default, never does: it waits as long as the compressor
	// overflows (D1, 2026-10-28).
	CompressCalmMaxStarve time.Duration
	// ClassLimits caps concurrent leases per class, for classes whose scarce
	// resource is not memory — see Limits.ClassLimits. Set by
	// -admit-class-limit=<class>:<n>, repeatable.
	ClassLimits map[string]int
	// MaxHold is how long a lease stays on the books before its accounting is
	// revoked (the client is neither told nor stopped). Zero disables.
	MaxHold time.Duration
	// MaxWaiters bounds the queue; beyond it a lease is granted degraded
	// rather than letting the queue swell. Zero or less means unbounded.
	MaxWaiters int
	// ActiveTick paces sampling and re-evaluation while anything is held or
	// queued: memory moves without an event, so the queue must be looked at
	// again on a clock.
	ActiveTick time.Duration
	// IdleTick paces sampling when nothing is held or queued. It only keeps
	// the swap-rate window warm so that the first request after a quiet spell
	// is judged on a real rate.
	IdleTick time.Duration
	// SwapWindow is the span the swap-out rate is measured over.
	SwapWindow time.Duration
	// Stale is how old a reading may be before Enqueue takes a fresh one.
	Stale time.Duration
}

// Defaults are the out-of-the-box settings for this machine.
func Defaults() Config {
	return Config{
		MaxJobs:               DefaultMaxJobs(),
		FloorBytes:            6 << 30,
		PressureLevel:         sysmem.PressureWarn,
		PressureCriticalLevel: sysmem.PressureCritical,
		// Half again the floor while the kernel warns: enough to slow the
		// machine down without stopping it, since warn alone says nothing
		// about whether it is actually paging.
		PressureFloorFactor: 1.5,
		// Three minutes: long enough that the queue's ordinary turnover
		// resolves most waits on its own (a tsc runs one to two minutes),
		// short enough that a human watching a blocked session does not give
		// up on it — which is what happened on 2026-09-24.
		MaxStarve:          3 * time.Minute,
		SwapoutBytesPerSec: 4 << 20, // 256 pages/s at 16 KiB
		// 1024 pages/s at 16 KiB — provisional (2026-10-21).
		CompressBytesPerSec: 16 << 20,
		// Sixty seconds: the 60 s means before the seven ds-ui builds of
		// 2026-10-27 were 43-95 MiB/s, all over the gate, while the 10 s
		// window fell under it between two bursts.
		CompressCalmWindow:  time.Minute,
		CompressCalmClasses: calmClassNames(),
		// One browser-mode vitest run at a time: each of its workers is a
		// live Chromium, and a large monorepo measured 63 GB when that
		// went unbounded. This is the machine-wide equivalent
		// of the repo-local lock that incident produced. Likewise one
		// `go test ./...` fills the cores: two at once, each at -p 3 (the
		// Makefile), is what the 2026-10-30 measure allows (a -race -p 6 run
		// averaged 4.7 cores, 248 s and CPU / 4.4 without -race). The old
		// one-at-a-time cap came from three runs at -p 6 (2026-10-21, load
		// 240: tests failed on their 10 s bounds, `*.test` binaries ran into
		// their 10 min timeout) — the pre-push hook asks for this class.
		// Back to one: -admit-class-limit go-test:1. One JS build at a time too: a ds-ui build
		// bursts to 9 GiB within seconds, and builds last seconds to a few
		// minutes (ds-ui p50 16 s), so the queue they form is short.
		ClassLimits: map[string]int{string(heavycmd.ClassVitestBrowser): 1, string(heavycmd.ClassGoTest): 2, string(heavycmd.ClassBuild): 1},
		MaxHold:     30 * time.Minute,
		MaxWaiters:  256,
		ActiveTick:  2 * time.Second,
		IdleTick:    15 * time.Second,
		SwapWindow:  10 * time.Second,
		Stale:       time.Second,
	}
}

// calmClassNames is heavycmd.CalmClasses() as the class strings Config holds.
func calmClassNames() []string {
	cs := heavycmd.CalmClasses()
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

// Sampler reads the machine. Implemented by internal/sysmem, faked in tests.
type Sampler interface {
	ReadMemory() (sysmem.Memory, error)
	Procs() (map[int]sysmem.Proc, error)
}

type platformSampler struct{}

func (platformSampler) ReadMemory() (sysmem.Memory, error)  { return sysmem.ReadMemory() }
func (platformSampler) Procs() (map[int]sysmem.Proc, error) { return sysmem.Procs() }

// Request is what a client asks admission for.
type Request struct {
	// Group is the fairness unit — conventionally the worktree name.
	Group string
	// Class is a heavycmd.Class; unknown classes cost what a generic one does.
	Class string
	// Label names the command, for diagnostics only.
	Label string
	// Repo keys the learned costs together with Class: a tsc on one repo says
	// nothing about a tsc on another.
	Repo string
	// PID is the client process. Its tree's memory is what the lease is charged
	// with — the socket is trusted, so the PID is taken at its word.
	PID int
	// CostHint overrides the learned/prior cost; floored at half the prior.
	// It is the user's explicit --cost-mb, so it wins over what was learned.
	CostHint uint64
	// CostPrior replaces the class prior this request starts from, for a run
	// whose cost the client can size better than a class-wide constant can
	// (a browser run's cost follows its worker count). Unlike CostHint it
	// yields to a learned cost as soon as there are enough observations.
	CostPrior uint64
	// Workers is how many workers the client will run, when that number is
	// what the cost scales with (a browser-mode vitest run). It keys the
	// learned cost alongside Class and Repo; 0 means "does not scale".
	Workers int
	// Focus is true when the caller is in focus: its waiter is considered
	// before the non-focus ones (see nextToAdmit). Resolved by the client of
	// the controller, never here.
	Focus bool
	// ParentLease is the id of a held lease this request runs under: the
	// command it admits was started by a generic envelope's command. That
	// lease is not counted against it (see verdict); an unknown id is
	// ignored.
	ParentLease string
}

// lease is one admitted request on the books.
type lease struct {
	id     string
	req    Request
	cost   uint64
	since  time.Time
	waited time.Duration // how long it sat in the queue before it was admitted
	mem    uint64        // current memory of the client's tree
	peak   uint64

	// What the history line reports once the lease ends (see history.go).
	enqueuedAt time.Time
	waitBy     map[Reason]time.Duration
	availAtEnq uint64
	starved    bool // admitted by the MaxStarve guarantee, past the other gates
	// admitCompRate, admitCompCalmRate: the compressor rates read at the grant.
	admitCompRate, admitCompCalmRate float64
	// swapStart is the cumulative swap-out counter at the grant; swapDuring
	// what the machine swapped out since, as of the last reading. A
	// machine-wide figure: the lease is not the only one writing to swap,
	// only one of those that paid for it.
	swapStart   uint64
	swapDuring  uint64
	maxPressure int
	// compStart is the compressor traffic counter (compressions +
	// decompressions) at the grant; compDuring the traffic since, as of the
	// last reading; maxCompRate the highest rate read meanwhile.
	compStart, compDuring uint64
	maxCompRate           float64

	// exit is the command's exit status as its client reported it before
	// releasing (see ReportExit); nil when none did.
	exit *int

	// restored marks a lease reloaded from the previous daemon's leases file
	// (leases_persist.go): nobody holds its stream, so only the death of its
	// client (Tick) or the same client asking again (Enqueue) ends it.
	restored bool
}

// Ticket is one enqueued request. Ready is closed exactly once, when the
// request is granted.
type Ticket struct {
	req    Request
	cost   uint64
	since  time.Time
	ch     chan struct{}
	lease  *lease
	degr   bool
	reason Reason

	// The wait, split by what caused it: the time between two passes of
	// admitLocked goes to the reason the first of them left in force.
	// waitReason differs from reason for the tickets behind the head, which
	// wait for whatever stopped the head (reason stays "" for them, as
	// Status has always shown).
	waitBy     map[Reason]time.Duration
	waitReason Reason
	accruedAt  time.Time
	availAtEnq uint64
}

// accrue books the time since the last pass to the reason then in force.
func (t *Ticket) accrue(now time.Time) {
	if d := now.Sub(t.accruedAt); d > 0 {
		r := t.waitReason
		if r == "" {
			r = reasonOther
		}
		if t.waitBy == nil {
			t.waitBy = map[Reason]time.Duration{}
		}
		t.waitBy[r] += d
	}
	t.accruedAt = now
}

// Ready is closed when the ticket is granted.
func (t *Ticket) Ready() <-chan struct{} { return t.ch }

// Grant is one admitted lease, as the handler holds it.
type Grant struct {
	ID       string
	Cost     uint64
	Waited   time.Duration
	Degraded bool

	lease *lease
}

// QueueInfo is a ticket's place in the queue, for progress lines.
type QueueInfo struct {
	Position  int // 1-based; 0 once granted or gone
	Queued    int
	Running   int
	Available uint64
	Effective uint64
	Need      uint64
	Floor     uint64
	Reason    Reason
	// DiskFree and DiskFloor are the disk reading and its floor, for the
	// progress line of a request held on ReasonDisk (DiskFree is 0 when the
	// disk is unknown).
	DiskFree, DiskFloor uint64
}

type counterSample struct {
	at    time.Time
	bytes uint64
}

// Controller admits heavy commands against measured memory. Mirrors cpuslots'
// shape so the same testing habits apply: pure decisions in decide.go,
// mutable state behind one mutex here, injectable clock, waiter, sampler.
type Controller struct {
	bus   *events.Bus
	cfg   Config
	logf  func(format string, args ...any)
	costs *costTable

	now     func() time.Time
	wait    func(ctx context.Context, d time.Duration)
	sampler Sampler
	// disk reads the free disk space; nil = unknown.
	disk          func() (uint64, error)
	diskErrLogged bool
	// notify, when set, is called outside the mutex after every state change
	// a test may want to observe (enqueue, grant, release, revoke) — instead
	// of polling Status() on a timer, a flake vector under exactly the load
	// this feature exists to relieve.
	notify func()
	// history, when set, gets one line per ended command. nil disables it.
	history *History

	mu           sync.Mutex
	leases       map[*lease]struct{}
	queue        []*Ticket
	reading      Reading
	total        uint64
	readAt       time.Time
	readErr      error
	swapHist     []counterSample
	swapouts     uint64 // the cumulative counter at the last reading
	compHist     []counterSample
	compCalmHist []counterSample // the same counter, over cfg.CompressCalmWindow
	// compTraffic is compressions + decompressions at the last reading.
	compTraffic uint64
	seq         int

	// leasesPath, when set, is where the leases in flight persist (see
	// leases_persist.go); alive and comm probe a restored lease's client.
	leasesPath       string
	persistErrLogged bool
	alive            func(pid int) bool
	comm             func(pid int) string
	// writeLeases writes the leases file; a seam for tests (default writeLeasesFile).
	writeLeases func(path string, f leasesFile) error
}

// Option customises a Controller.
type Option func(*Controller)

// New constructs a Controller with platform defaults.
func New(bus *events.Bus, opts ...Option) *Controller {
	c := &Controller{
		bus:     bus,
		cfg:     Defaults(),
		logf:    log.Printf,
		costs:   newCostTable(""),
		now:     time.Now,
		sampler: platformSampler{},
		leases:  map[*lease]struct{}{},
		alive:   pidAlive,
		comm:    pidComm,
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
		o(c)
	}
	// After the options (MaxHold, clock, probes), before the daemon serves a
	// request: the restored leases count from the first Enqueue.
	c.loadLeases()
	return c
}

// WithConfig overrides the config.
func WithConfig(cfg Config) Option { return func(c *Controller) { c.cfg = cfg } }

// WithLogger overrides the log sink.
func WithLogger(f func(string, ...any)) Option { return func(c *Controller) { c.logf = f } }

// WithClock overrides the clock.
func WithClock(f func() time.Time) Option { return func(c *Controller) { c.now = f } }

// WithWaiter overrides the tick sleep (tests pass one they drive).
func WithWaiter(f func(context.Context, time.Duration)) Option {
	return func(c *Controller) { c.wait = f }
}

// WithSampler overrides the memory sampler.
func WithSampler(s Sampler) Option { return func(c *Controller) { c.sampler = s } }

// WithDiskReader sets how the free disk space is read. It is an option, not a
// Sampler method, so the Sampler fakes of the tests stay untouched. A nil
// reader means the disk is unknown, and the gate stays open.
func WithDiskReader(f func() (uint64, error)) Option { return func(c *Controller) { c.disk = f } }

// WithNotify installs the state-change hook described on Controller.notify.
func WithNotify(f func()) Option { return func(c *Controller) { c.notify = f } }

// WithCostsPath persists learned costs at path ("" keeps them in memory).
func WithCostsPath(path string) Option {
	return func(c *Controller) { c.costs = newCostTable(path) }
}

// WithHistory appends one line per ended command to h (nil disables it).
func WithHistory(h *History) Option { return func(c *Controller) { c.history = h } }

func (c *Controller) fireNotify() {
	if c.notify != nil {
		c.notify()
	}
}

// LoadCosts seeds the learned costs from disk. A failure is logged and
// ignored: the priors are a safe place to start from.
func (c *Controller) LoadCosts() {
	err := c.costs.load()
	switch {
	case err == nil:
	case errors.Is(err, errLegacyCosts), errors.Is(err, errNewerCosts):
		c.logf("admission: %v", err)
	default:
		c.logf("admission: ignoring unreadable learned costs: %v", err)
	}
}

// Enabled reports whether the controller admits anything at all.
func (c *Controller) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.MaxJobs > 0
}

// limitsLocked projects the config onto the decision's Limits.
func (c *Controller) limitsLocked() Limits {
	l := Limits{
		FloorBytes:            c.cfg.FloorBytes,
		DiskFloorBytes:        c.cfg.DiskFloorBytes,
		MaxJobs:               c.cfg.MaxJobs,
		PressureLevel:         c.cfg.PressureLevel,
		PressureCriticalLevel: c.cfg.PressureCriticalLevel,
		PressureFloorFactor:   c.cfg.PressureFloorFactor,
		SwapoutBytesPerSec:    c.cfg.SwapoutBytesPerSec,
		CompressBytesPerSec:   c.cfg.CompressBytesPerSec,
		MaxStarve:             c.cfg.MaxStarve,
		ClassLimits:           c.cfg.ClassLimits,
		CompressCalmMaxStarve: c.cfg.CompressCalmMaxStarve,
	}
	if c.cfg.CompressCalmWindow > 0 && len(c.cfg.CompressCalmClasses) > 0 {
		l.CompressCalmClasses = make(map[string]bool, len(c.cfg.CompressCalmClasses))
		for _, class := range c.cfg.CompressCalmClasses {
			l.CompressCalmClasses[class] = true
		}
	}
	return l
}

// costFor resolves what a request is expected to cost.
func (c *Controller) costFor(req Request) uint64 {
	classPrior := heavycmd.Prior(heavycmd.Class(req.Class))
	// The floor stays anchored to the class, never to the client's own
	// estimate: a client that guesses high must not thereby prevent its
	// run's measured peak from being believed later.
	floor := classPrior / 2
	prior := classPrior
	if req.CostPrior > 0 {
		prior = req.CostPrior
	}
	if req.CostHint > 0 {
		if req.CostHint < floor {
			return floor
		}
		return req.CostHint
	}
	return c.costs.estimate(req.Class, req.Repo, req.Workers, prior, floor)
}

// Enqueue puts a request in the queue and admits whatever now fits. The
// returned ticket is ready at once when it was admitted immediately. The
// caller must end every ticket with either Cancel (it gave up waiting) or,
// once Ready, Release(Grant(t)).
func (c *Controller) Enqueue(req Request) (*Ticket, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	c.refreshIfStale()

	now := c.now()
	t := &Ticket{req: req, cost: c.costFor(req), since: now, accruedAt: now, ch: make(chan struct{})}
	c.mu.Lock()
	t.availAtEnq = c.reading.Available
	if c.cfg.MaxJobs <= 0 { // disabled between the two locks: stay truthful
		c.mu.Unlock()
		return nil, ErrDisabled
	}
	if c.cfg.MaxWaiters > 0 && len(c.queue) >= c.cfg.MaxWaiters {
		// An arbiter that refuses is an arbiter that stops a command from
		// running; over the ceiling the command runs, simply uncounted.
		t.degr = true
		close(t.ch)
		c.mu.Unlock()
		return t, nil
	}
	// A client asking again with the pid of a restored lease lost its
	// "granted" to the restart: its old place goes, never two for one run.
	dropped := c.dropRestoredByPIDLocked(req.PID)
	c.queue = append(c.queue, t)
	granted := c.admitLocked()
	c.mu.Unlock()
	now = c.now()
	for _, l := range dropped {
		c.publishReleased(l)
		c.appendHistory(leaseRecord(l, pigtail.AdmissionOutcomeReleased, now))
	}
	c.publishGranted(granted)
	c.fireNotify()
	return t, nil
}

// Grant returns the lease of a ready ticket. Calling it before Ready is
// closed returns the zero Grant.
func (c *Controller) Grant(t *Ticket) Grant {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.degr {
		return Grant{Degraded: true, Cost: t.cost}
	}
	if t.lease == nil {
		return Grant{}
	}
	return Grant{ID: t.lease.id, Cost: t.cost, Waited: t.lease.waited, lease: t.lease}
}

// Cancel withdraws a ticket whose client gave up. A grant that raced the
// cancellation is released rather than left booked to a client that is gone.
func (c *Controller) Cancel(t *Ticket) {
	c.mu.Lock()
	queued := c.dequeueLocked(t)
	l := t.lease
	var rec pigtail.AdmissionRecord
	if queued && l == nil {
		now := c.now()
		t.accrue(now)
		rec = ticketRecord(t, now)
	}
	c.mu.Unlock()
	if l != nil {
		c.Release(Grant{lease: l})
		return
	}
	if queued {
		c.appendHistory(rec)
	}
	c.mu.Lock()
	granted := c.admitLocked() // the head may have been the one leaving
	c.mu.Unlock()
	c.publishGranted(granted)
	c.fireNotify()
}

// Release returns a lease: its peak is learned, its reservation freed, and
// the queue looked at again. Releasing the zero Grant, a degraded one, or one
// the sweep already revoked is a no-op on the books.
func (c *Controller) Release(g Grant) {
	if g.lease == nil {
		return
	}
	c.mu.Lock()
	_, booked := c.leases[g.lease]
	delete(c.leases, g.lease)
	if booked {
		c.persistLocked()
	}
	l := *g.lease
	granted := c.admitLocked()
	c.mu.Unlock()

	if booked {
		c.costs.observe(l.req.Class, l.req.Repo, l.req.Workers, l.peak)
		if err := c.costs.save(); err != nil {
			c.logf("admission: saving learned costs: %v", err)
		}
		c.publishReleased(l)
		c.appendHistory(leaseRecord(l, pigtail.AdmissionOutcomeReleased, c.now()))
	}
	c.publishGranted(granted)
	c.fireNotify()
}

// ReportExit records the exit status of the command a booked lease admitted,
// so the history line written at its release carries it. It returns false
// when no booked lease has that id (unknown, released or revoked already).
func (c *Controller) ReportExit(id string, code int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for l := range c.leases {
		if l.id == id {
			l.exit = &code
			return true
		}
	}
	return false
}

// Position reports a ticket's place in the queue.
func (c *Controller) Position(t *Ticket) QueueInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := QueueInfo{
		Queued:    len(c.queue),
		Running:   len(c.leases),
		Available: c.reading.Available,
		Effective: c.effectiveLocked(),
		Need:      t.cost,
		Floor:     c.cfg.FloorBytes,
		Reason:    t.reason,
		DiskFloor: c.cfg.DiskFloorBytes,
	}
	if c.reading.DiskKnown {
		info.DiskFree = c.reading.DiskFree
	}
	for i, q := range c.queue {
		if q == t {
			info.Position = i + 1
			break
		}
	}
	return info
}

// dequeueLocked removes t from the queue and reports whether it was there.
func (c *Controller) dequeueLocked(t *Ticket) bool {
	for i, q := range c.queue {
		if q == t {
			c.queue = append(c.queue[:i], c.queue[i+1:]...)
			return true
		}
	}
	return false
}

func (c *Controller) heldLocked() ([]Held, map[string]int) {
	held := make([]Held, 0, len(c.leases))
	per := map[string]int{}
	for l := range c.leases {
		held = append(held, Held{ID: l.id, Group: l.req.Group, Class: l.req.Class, Cost: l.cost, Mem: l.mem})
		per[l.req.Group]++
	}
	return held, per
}

func (c *Controller) effectiveLocked() uint64 {
	held, _ := c.heldLocked()
	out := outstanding(held)
	if c.reading.Available < out {
		return 0
	}
	return c.reading.Available - out
}

// admitLocked admits the queue's head for as long as it fits, and records
// why it stopped on the head it stopped at. Returns the leases it granted so
// the caller can publish them outside the mutex. Caller holds mu.
func (c *Controller) admitLocked() []*lease {
	now := c.now()
	for _, t := range c.queue {
		t.accrue(now)
		t.reason = ""
	}
	granted, stop := c.admitPassLocked()
	for _, t := range c.queue {
		t.waitReason = t.reason
		if t.waitReason == "" {
			t.waitReason = stop
		}
	}
	return granted
}

// admitPassLocked is admitLocked's loop. stop is the reason the queue
// stopped on, "" when it emptied or everything left is capped by its class.
func (c *Controller) admitPassLocked() (granted []*lease, stop Reason) {
	// Tickets a class limit holds back are set aside for this pass: they
	// cannot be helped by anything the rest of the queue does, and letting
	// them block it would turn one capped class into a machine-wide stall.
	// See nextToAdmit.
	skip := map[*Ticket]bool{}
	// calmHold: a calm-gated waiter past MaxStarve was set aside this pass.
	// It waits for the compressor, not for the queue, so it must not stop the
	// starving waiters behind it — but a fresh waiter passing it would feed
	// the very traffic it waits out, so only starving ones may (D1).
	calmHold := false
	for len(c.queue) > 0 && c.cfg.MaxJobs > 0 {
		held, per := c.heldLocked()
		at := c.now()
		q := make([]Waiter, len(c.queue))
		blocked := make([]bool, len(c.queue))
		for i, t := range c.queue {
			q[i] = Waiter{Group: t.req.Group, Class: t.req.Class, Cost: t.cost,
				Waited: at.Sub(t.since), Focus: t.req.Focus, Parent: t.req.ParentLease}
			blocked[i] = skip[t]
		}
		// The longest wait past MaxStarve is looked at before the fairness
		// pick: the gates are machine-wide, so a waiter fairness did not
		// choose gets refused for a reason that is not about it, and the
		// queue stops on it. See starvingIndex.
		i := starvingIndex(q, blocked, c.cfg.MaxStarve)
		if i < 0 {
			if calmHold {
				return granted, ReasonCompressCalm
			}
			i = nextToAdmit(q, per, blocked)
		}
		if i < 0 {
			return granted, ""
		}
		t := c.queue[i]
		if r := verdict(c.reading, c.limitsLocked(), held, q[i]); r != "" {
			t.reason = r
			if r == ReasonClassLimit {
				skip[t] = true
				continue
			}
			if r == ReasonCompressCalm && c.cfg.MaxStarve > 0 && q[i].Waited >= c.cfg.MaxStarve {
				skip[t] = true
				calmHold = true
				continue
			}
			return granted, r
		}
		c.queue = append(c.queue[:i], c.queue[i+1:]...)
		c.seq++
		now := c.now()
		l := &lease{id: fmt.Sprintf("adm-%d", c.seq), req: t.req, cost: t.cost,
			since: now, waited: now.Sub(t.since),
			enqueuedAt: t.since, waitBy: t.waitBy, availAtEnq: t.availAtEnq,
			starved:   c.cfg.MaxStarve > 0 && q[i].Waited >= c.cfg.MaxStarve,
			swapStart: c.swapouts, maxPressure: c.reading.Pressure,
			compStart: c.compTraffic, maxCompRate: c.reading.CompressBytesPerSec,
			admitCompRate: c.reading.CompressBytesPerSec, admitCompCalmRate: c.reading.CompressCalmBytesPerSec}
		c.leases[l] = struct{}{}
		t.lease = l
		// The file first, then the ticket: a daemon dying between the two
		// must not forget a run its client believes admitted.
		c.persistLocked()
		close(t.ch)
		granted = append(granted, l)
	}
	return granted, ""
}

// RefreshIfStale takes a reading when the last one is older than cfg.Stale,
// so that GET /admission never reports a machine it has not looked at.
func (c *Controller) RefreshIfStale() { c.refreshIfStale() }

// refreshIfStale takes a reading when the last one is older than cfg.Stale.
func (c *Controller) refreshIfStale() {
	c.mu.Lock()
	fresh := !c.readAt.IsZero() && c.now().Sub(c.readAt) < c.cfg.Stale
	c.mu.Unlock()
	if !fresh {
		c.refresh()
	}
}

// refresh samples the machine — outside the mutex, since it forks — and
// folds the reading in.
func (c *Controller) refresh() {
	m, err := c.sampler.ReadMemory()
	// The disk is read whether or not the memory was: the two fail
	// independently, and a failed memory read must not freeze the disk gate
	// on an old value.
	diskFree, diskErr := c.readDisk()
	at := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.foldDiskLocked(diskFree, diskErr)
	if err != nil {
		if c.readErr == nil {
			c.logf("admission: reading memory failed, keeping the last reading: %v", err)
		}
		c.readErr = err
		return
	}
	c.readErr = nil
	c.readAt = at
	c.total = m.Total
	c.reading.Available = m.Available
	c.reading.Pressure = m.Pressure
	c.reading.CompressorBytes = m.CompressorBytes
	c.reading.UnusedBytes = m.UnusedBytes
	c.swapHist, c.reading.SwapoutBytesPerSec = windowRate(c.swapHist, at, m.Swapouts, c.cfg.SwapWindow)
	c.swapouts = m.Swapouts
	traffic := m.Compressions + m.Decompressions
	c.compHist, c.reading.CompressBytesPerSec = windowRate(c.compHist, at, traffic, c.cfg.SwapWindow)
	if c.cfg.CompressCalmWindow > 0 {
		c.compCalmHist, c.reading.CompressCalmBytesPerSec = windowRate(c.compCalmHist, at, traffic, c.cfg.CompressCalmWindow)
	}
	c.compTraffic = traffic
	// What each running command lived through, for its history line. A
	// counter that was not there at the grant (or went backwards) says
	// nothing about this lease.
	for l := range c.leases {
		if m.Pressure > l.maxPressure {
			l.maxPressure = m.Pressure
		}
		if l.swapStart > 0 && m.Swapouts >= l.swapStart {
			l.swapDuring = m.Swapouts - l.swapStart
		}
		if c.reading.CompressBytesPerSec > l.maxCompRate {
			l.maxCompRate = c.reading.CompressBytesPerSec
		}
		if l.compStart > 0 && traffic >= l.compStart {
			l.compDuring = traffic - l.compStart
		}
	}
}

// readDisk reads the free disk space; a missing reader is "unknown".
func (c *Controller) readDisk() (uint64, error) {
	if c.disk == nil {
		return 0, errNoDiskReader
	}
	return c.disk()
}

var errNoDiskReader = errors.New("admission: no disk reader")

// foldDiskLocked records a disk reading. An error clears it: unlike memory,
// which keeps its last reading, a disk value is never reused once a read
// failed, so a reading below the floor cannot hold the queue (or keep a gate
// shut) on stale data. A first failure is logged once.
func (c *Controller) foldDiskLocked(free uint64, err error) {
	if err != nil {
		c.reading.DiskFree, c.reading.DiskKnown = 0, false
		if !errors.Is(err, errNoDiskReader) && !c.diskErrLogged {
			c.diskErrLogged = true
			c.logf("admission: reading the free disk space failed, the disk gate is open: %v", err)
		}
		return
	}
	c.diskErrLogged = false
	c.reading.DiskFree, c.reading.DiskKnown = free, true
}

// windowRate appends a cumulative-counter sample to hist and returns the
// updated history with the rate across the window. The oldest sample kept is
// the last one at or before the window's start, so a quiet spell between two
// samples yields a long average rather than no rate at all. A counter that
// reads 0 (this platform has none) keeps nothing and gives 0; one that went
// backwards (reboot under a live daemon, wrap) gives 0, never a huge rate.
func windowRate(hist []counterSample, at time.Time, bytes uint64, window time.Duration) ([]counterSample, float64) {
	if bytes == 0 {
		return hist, 0
	}
	hist = append(hist, counterSample{at: at, bytes: bytes})
	start := at.Add(-window)
	for len(hist) > 2 && !hist[1].at.After(start) {
		hist = hist[1:]
	}
	first, last := hist[0], hist[len(hist)-1]
	dt := last.at.Sub(first.at).Seconds()
	if dt <= 0 || last.bytes < first.bytes {
		return hist, 0
	}
	return hist, float64(last.bytes-first.bytes) / dt
}

// sampleLeases charges each lease with its process tree's current memory
// (physical footprint on darwin) and tracks its peak — the peak is what the cost table learns on release.
func (c *Controller) sampleLeases() {
	c.mu.Lock()
	n := len(c.leases)
	c.mu.Unlock()
	if n == 0 {
		return
	}
	snap, err := c.sampler.Procs()
	if err != nil {
		return // keep the previous reading: a missed sample is not a free one
	}
	c.mu.Lock()
	for l := range c.leases {
		if l.req.PID <= 0 {
			continue
		}
		l.mem = sysmem.SumTree(snap, []int{l.req.PID})
		if l.mem > l.peak {
			l.peak = l.mem
		}
	}
	c.mu.Unlock()
}

// Tick is one pass of the loop Run drives: sample, sweep, re-evaluate.
// Exported for tests, which drive it instead of a clock.
func (c *Controller) Tick() {
	c.refresh()
	c.sampleLeases()
	c.Sweep()
	c.releaseGoneRestored()
	c.mu.Lock()
	granted := c.admitLocked()
	c.mu.Unlock()
	c.publishGranted(granted)
	if len(granted) > 0 {
		c.fireNotify()
	}
}

// busy reports whether anything is held or queued.
func (c *Controller) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.leases) > 0 || len(c.queue) > 0
}

// Run ticks until ctx is cancelled: fast while anything is held or queued,
// slowly otherwise. No-op when the controller is disabled.
func (c *Controller) Run(ctx context.Context) {
	if !c.Enabled() {
		return
	}
	// A first reading at once: the swap-rate window starts filling now, not
	// one idle tick after startup.
	c.refresh()
	for {
		d := c.cfg.IdleTick
		if c.busy() {
			d = c.cfg.ActiveTick
		}
		if d <= 0 {
			return
		}
		c.wait(ctx, d)
		if ctx.Err() != nil {
			return
		}
		c.Tick()
	}
}

// Sweep revokes the *accounting* of every lease held past MaxHold. Like
// cpuslots, it kills nothing and tells no one: the command keeps running,
// only its reservation stops counting — so one wedged process cannot hold
// the queue for the rest of the day. No-op when MaxHold is zero or less.
func (c *Controller) Sweep() {
	c.mu.Lock()
	if c.cfg.MaxHold <= 0 {
		c.mu.Unlock()
		return
	}
	now := c.now()
	var revoked []lease
	for l := range c.leases {
		if now.Sub(l.since) >= c.cfg.MaxHold {
			revoked = append(revoked, *l)
			delete(c.leases, l)
		}
	}
	var granted []*lease
	if len(revoked) > 0 {
		c.persistLocked()
		granted = c.admitLocked()
	}
	c.mu.Unlock()
	for _, l := range revoked {
		held := now.Sub(l.since)
		c.logf("admission: revoking the accounting of %s (%s %s) held %s — the command is not told and keeps running",
			l.req.Group, l.req.Class, l.req.Label, held.Round(time.Second))
		c.publish(pigtail.EventAdmissionRevoked, l, map[string]interface{}{"held_seconds": int(held.Seconds())})
		c.appendHistory(leaseRecord(l, pigtail.AdmissionOutcomeRevoked, now))
	}
	c.publishGranted(granted)
	if len(revoked) > 0 {
		c.fireNotify()
	}
}

// LeaseStatus is one lease's line in Status.
type LeaseStatus struct {
	ID, Group, Class, Label string
	Cost, Mem, Peak         uint64
	Age, Waited             time.Duration
}

// WaiterStatus is one queued request's line in Status.
type WaiterStatus struct {
	Group, Class, Label string
	Cost                uint64
	Age                 time.Duration
	Reason              Reason
	Focus               bool
}

// CostStatus is one learned cost. Workers is 0 for the classes whose cost
// does not scale with a worker count.
type CostStatus struct {
	Class, Repo string
	Workers     int
	EWMA        uint64
	N           int
}

// Status is the controller's whole state, as GET /admission reports it.
type Status struct {
	Config    Config
	Reading   Reading
	Total     uint64
	Effective uint64
	ReadAt    time.Time
	Leases    []LeaseStatus
	Queue     []WaiterStatus
	Costs     []CostStatus
}

// Status reports the controller's state. Read-only.
func (c *Controller) Status() Status {
	costs := c.costs.snapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	st := Status{
		Config: c.cfg, Reading: c.reading, Total: c.total,
		Effective: c.effectiveLocked(), ReadAt: c.readAt,
		Leases: make([]LeaseStatus, 0, len(c.leases)),
		Queue:  make([]WaiterStatus, 0, len(c.queue)),
	}
	for l := range c.leases {
		st.Leases = append(st.Leases, LeaseStatus{
			ID: l.id, Group: l.req.Group, Class: l.req.Class, Label: l.req.Label,
			Cost: l.cost, Mem: l.mem, Peak: l.peak, Age: now.Sub(l.since), Waited: l.waited,
		})
	}
	sort.Slice(st.Leases, func(i, j int) bool { return st.Leases[i].Age > st.Leases[j].Age })
	for _, t := range c.queue {
		st.Queue = append(st.Queue, WaiterStatus{
			Group: t.req.Group, Class: t.req.Class, Label: t.req.Label,
			Cost: t.cost, Age: now.Sub(t.since), Reason: t.reason, Focus: t.req.Focus,
		})
	}
	for k, e := range costs {
		class, repo, workers := splitCostKey(k)
		st.Costs = append(st.Costs, CostStatus{Class: class, Repo: repo, Workers: workers, EWMA: uint64(e.EWMA), N: e.N})
	}
	sort.Slice(st.Costs, func(i, j int) bool {
		if st.Costs[i].Repo != st.Costs[j].Repo {
			return st.Costs[i].Repo < st.Costs[j].Repo
		}
		if st.Costs[i].Class != st.Costs[j].Class {
			return st.Costs[i].Class < st.Costs[j].Class
		}
		return st.Costs[i].Workers < st.Costs[j].Workers
	})
	return st
}

// splitCostKey undoes costKey. A two-part key (every class whose cost does
// not scale with a worker count, and every key learned before this existed)
// reports 0 workers.
func splitCostKey(k string) (class, repo string, workers int) {
	parts := strings.SplitN(k, "\x00", 3)
	class = parts[0]
	if len(parts) > 1 {
		repo = parts[1]
	}
	if len(parts) > 2 {
		workers, _ = strconv.Atoi(parts[2])
	}
	return class, repo, workers
}

func (c *Controller) publishGranted(ls []*lease) {
	for _, l := range ls {
		c.publish(pigtail.EventAdmissionGranted, *l, nil)
	}
}

func (c *Controller) publishReleased(l lease) {
	c.publish(pigtail.EventAdmissionReleased, l, map[string]interface{}{
		"peak_bytes":   l.peak,
		"held_seconds": int(c.now().Sub(l.since).Seconds()),
	})
}

func (c *Controller) publish(typ pigtail.EventType, l lease, extra map[string]interface{}) {
	if c.bus == nil {
		return
	}
	data := map[string]interface{}{
		"lease_id":   l.id,
		"group":      l.req.Group,
		"class":      l.req.Class,
		"label":      l.req.Label,
		"cost_bytes": l.cost,
		// How long this command waited for the machine. The number the whole
		// feature is judged on: an admission nobody waited for cost nothing,
		// and a queue nobody ever leaves is a misconfiguration.
		"waited_ms": l.waited.Milliseconds(),
	}
	for k, v := range extra {
		data[k] = v
	}
	c.bus.Publish(pigtail.Event{Type: typ, Data: data})
}
