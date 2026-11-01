package sessbudget

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// PendingLeaseMax bounds a spawn lease the spawn never reports (a spawn CLI
// killed mid-way): the reserve until the tab's birth (spawnreg.ReserveCap,
// createWorktree included), the watcher's confirmation window
// (spawnreg.PendingWindow) and 2 min of registry lock wait (one holder; the
// lock has no timeout, so a longer queue may end the lease first, accepted).
// Since #253 the lock no longer covers createWorktree.
const PendingLeaseMax = spawnreg.ReserveCap + spawnreg.PendingWindow + 2*time.Minute

// DefaultConfig is pigtaild's session budget out of the box: the gate off
// (Ceiling 0) until `setup --session-budget`, the sampler at 30 s.
func DefaultConfig() Config {
	return Config{
		Floor: 6, Ceiling: 0,
		Window: 5 * time.Minute, MinGap: 40 * time.Second,
		// The admission controller's compressor threshold and floor.
		CompressBytesPerSec: 16 << 20, MinAvailable: 6 << 30,
		SamplePeriod: 30 * time.Second,
	}
}

// ValidateConfig refuses a configuration pigtaild must not start with: the
// gate on with the sampler off (no sample would ever admit an agent).
func ValidateConfig(cfg Config) error {
	var negative []string
	if cfg.Floor < 0 {
		negative = append(negative, fmt.Sprintf("-session-floor %d: must not be negative", cfg.Floor))
	}
	if cfg.Ceiling < 0 {
		negative = append(negative, fmt.Sprintf("-session-ceiling %d: must not be negative", cfg.Ceiling))
	}
	if len(negative) > 0 {
		return errors.New(strings.Join(negative, "; "))
	}
	if !cfg.Enabled() {
		return nil
	}
	if cfg.SamplePeriod <= 0 {
		return fmt.Errorf("-session-ceiling %d turns the session gate on, but -session-sample-seconds is 0: the gate needs the sampler", cfg.Ceiling)
	}
	if cfg.Floor < 1 || cfg.Floor > cfg.Ceiling {
		return fmt.Errorf("-session-floor %d: must be between 1 and -session-ceiling (%d)", cfg.Floor, cfg.Ceiling)
	}
	return nil
}

type pendingKind int

const (
	pendingSpawn pendingKind = iota
	pendingWake
)

// pending is one admission not yet seen by a sample: a spawn lease or a
// granted wake.
type pending struct {
	kind pendingKind
	at   time.Time
	// prev is lastAdmissionAt before this grant, restored by a release.
	prev time.Time
	// launched is when the spawn reported Claude launched (zero = not yet).
	launched time.Time
	// samplesAfter counts the samples taken after a wake's grant.
	samplesAfter int
	// owner is the scope of the caller that took a spawn lease ("" = none
	// named), for the release route to keep a lease to its own scope.
	owner string
}

// Answer is the controller's answer to an ask: Decide's decision plus n, m
// and, for an admitted agent, the lease id.
type Answer struct {
	Decision
	// Enabled is false when the gate is off: every ask is admitted.
	Enabled bool
	Busy    int
	Budget  int
	LeaseID string
}

// Snapshot is the controller's state for GET /session-budget and pigtail
// slots.
type Snapshot struct {
	Enabled bool
	Busy    int
	Budget  int
	// Gate is Check()'s decision: what an agent ask would get now.
	Gate Decision
	// DeferredWakes and DeferredOldestAge are the relay's last-pass gauge.
	DeferredWakes     int
	DeferredOldestAge time.Duration
}

// Controller grants launch slots from the sampler's samples. It knows no
// queue: an ask is admitted or refused at once, and a refusal stores nothing.
// Every rule is Decide's; the controller only builds its input.
type Controller struct {
	cfg  Config
	now  func() time.Time
	logf func(string, ...any)

	// OnFreed, when non-nil, is called once per sample whose busy count
	// dropped while the budget is no longer full. Set it before the sampler
	// runs; called outside the controller's lock.
	OnFreed func()

	mu             sync.Mutex
	window         *Window
	observed       bool
	lastSample     time.Time
	lastBusy       int
	lastAdmission  time.Time
	pending        map[string]*pending
	deferredCount  int
	deferredOldest time.Time
}

// NewController returns a controller for cfg; now is its clock, logf its log.
func NewController(cfg Config, now func() time.Time, logf func(string, ...any)) *Controller {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Controller{cfg: cfg, now: now, logf: logf, window: NewWindow(cfg.Window), pending: map[string]*pending{}}
}

// Enabled reports whether the gate is on.
func (c *Controller) Enabled() bool { return c != nil && c.cfg.Enabled() }

// Observe takes one sample: its memory reading feeds the pressure window, its
// busy count becomes n's base, and it ends the pending admissions it
// accounts for. A sample not later than the last one observed is ignored.
func (c *Controller) Observe(s Sample) {
	fire := c.observe(s)
	if fire && c.OnFreed != nil {
		c.OnFreed()
	}
}

func (c *Controller) observe(s Sample) (freed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.observed && !s.TS.After(c.lastSample) {
		return false
	}
	busyIDs := map[string]bool{}
	for _, row := range s.Sessions {
		if row.Status == "busy" {
			busyIDs[row.SessionID] = true
		}
	}
	for id, p := range c.pending {
		switch p.kind {
		case pendingSpawn:
			if !p.launched.IsZero() && s.TS.After(p.launched) {
				delete(c.pending, id) // the sample counts the session itself
			}
		case pendingWake:
			if !s.TS.After(p.at) {
				continue
			}
			p.samplesAfter++
			if busyIDs[id] || p.samplesAfter >= 2 {
				delete(c.pending, id) // counted by the sample, or no work came
			}
		}
	}
	prevBusy, hadPrev := c.lastBusy, c.observed
	c.window.Add(ReadingOf(s))
	c.observed, c.lastSample, c.lastBusy = true, s.TS, s.Busy
	if !c.cfg.Enabled() || !hadPrev || s.Busy >= prevBusy {
		return false
	}
	in := c.inputLocked(KindAgent)
	return in.Busy < in.Budget
}

// expireLocked drops the spawn leases never reported launched past
// PendingLeaseMax.
func (c *Controller) expireLocked(now time.Time) {
	for id, p := range c.pending {
		if p.kind == pendingSpawn && p.launched.IsZero() && now.Sub(p.at) >= PendingLeaseMax {
			c.logf("sessbudget: lease %s expired after %s without a launch report", id, PendingLeaseMax)
			delete(c.pending, id)
		}
	}
}

// Input is the DecideInput the controller would pass to Decide for kind now.
func (c *Controller) Input(kind Kind) DecideInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputLocked(kind)
}

func (c *Controller) inputLocked(kind Kind) DecideInput {
	now := c.now()
	c.expireLocked(now)
	p := c.window.Judge(c.cfg.Thresholds())
	in := DecideInput{
		Kind: kind, Busy: c.lastBusy + len(c.pending),
		Budget: Budget(p, c.cfg.Floor, c.cfg.Ceiling), Floor: c.cfg.Floor, RedCause: p.Cause,
		Observed: c.observed, MinGap: c.cfg.MinGap,
	}
	if !c.lastAdmission.IsZero() {
		in.HasAdmission = true
		in.SinceAdmission = now.Sub(c.lastAdmission)
		in.SampleSinceAdmission = c.observed && c.lastSample.After(c.lastAdmission)
		if !in.SampleSinceAdmission && c.cfg.SamplePeriod > 0 {
			in.MissedPeriods = int(in.SinceAdmission / c.cfg.SamplePeriod)
		}
	}
	return in
}

// decideLocked is Decide on the controller's input, with n and m; a
// disabled gate admits everything and flags nothing.
func (c *Controller) decideLocked(kind Kind) (Answer, DecideInput) {
	in := c.inputLocked(kind)
	if !c.cfg.Enabled() {
		return Answer{Decision: Decision{Admitted: true}, Busy: in.Busy, Budget: c.cfg.Ceiling}, in
	}
	d := Decide(in)
	if d.FreshWaived {
		c.logf("sessbudget: no sample for %d periods since the last admission: fresh-sample condition waived", in.MissedPeriods)
	}
	return Answer{Decision: d, Enabled: true, Busy: in.Busy, Budget: in.Budget}, in
}

// recordLocked records an admission of id: lastAdmissionAt and a pending
// entry (a wake already pending is refreshed, never counted twice).
func (c *Controller) recordLocked(id string, kind pendingKind) {
	now := c.now()
	c.pending[id] = &pending{kind: kind, at: now, prev: c.lastAdmission}
	c.lastAdmission = now
}

// TryAdmit is the non-blocking « admit now? » of a spawn (agent) or the user's
// launch (human). An admitted agent gets a lease id and counts in n until
// released, reported launched and sampled, or PendingLeaseMax; a human
// admission records nothing. A refusal stores nothing.
func (c *Controller) TryAdmit(kind Kind) Answer { return c.TryAdmitAs(kind, "") }

// TryAdmitAs is TryAdmit with the lease recorded as owner's (a scope name,
// read back by LeaseOwner).
func (c *Controller) TryAdmitAs(kind Kind, owner string) Answer {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, _ := c.decideLocked(kind)
	if kind == KindHuman {
		return a
	}
	if a.Admitted {
		a.LeaseID = newLeaseID()
		if c.cfg.Enabled() {
			c.recordLocked(a.LeaseID, pendingSpawn)
			c.pending[a.LeaseID].owner = owner
		}
	}
	return a
}

// LeaseOwner is the owner a pending spawn lease was taken as; ok is false
// for an unknown, released or ended lease.
func (c *Controller) LeaseOwner(id string) (owner string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[id]
	if !ok || p.kind != pendingSpawn {
		return "", false
	}
	return p.owner, true
}

// Check is TryAdmit(agent) without recording anything.
func (c *Controller) Check() Answer {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, _ := c.decideLocked(KindAgent)
	return a
}

// releaseLocked drops a pending entry and restores lastAdmissionAt when no
// admission came after it.
func (c *Controller) releaseLocked(id string) {
	p, ok := c.pending[id]
	if !ok {
		return
	}
	delete(c.pending, id)
	if c.lastAdmission.Equal(p.at) {
		c.lastAdmission = p.prev
	}
}

// ReleaseLease gives back a spawn lease that did not launch: dropped at once,
// the gap restored. Unknown, released or launched leases: no-op.
func (c *Controller) ReleaseLease(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pending[id]; ok && p.kind == pendingSpawn && p.launched.IsZero() {
		c.releaseLocked(id)
	}
}

// MarkLaunched records the spawn's launched report: the lease counts until
// the first sample taken after now. Unknown id: no-op.
func (c *Controller) MarkLaunched(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pending[id]; ok && p.kind == pendingSpawn && p.launched.IsZero() {
		p.launched = c.now()
	}
}

// BudgetAcquire is the relay's « may I wake sessionID now? ». Granted, the
// wake counts in n until the session shows busy in a sample, the second
// sample after the grant, or BudgetRelease; refused, nothing is stored.
func (c *Controller) BudgetAcquire(kind, sessionID string) (granted bool, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, _ := c.decideLocked(Kind(kind))
	if !a.Admitted {
		return false, a.Reason
	}
	if c.cfg.Enabled() {
		// A wake already pending for this session is refreshed (replaced),
		// never counted twice.
		c.recordLocked(sessionID, pendingWake)
	}
	return true, ""
}

// BudgetRelease gives back a granted wake that was not typed.
func (c *Controller) BudgetRelease(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pending[sessionID]; ok && p.kind == pendingWake {
		c.releaseLocked(sessionID)
	}
}

// SetDeferredWakes replaces the display gauge of the wakes the relay deferred
// for budget in its last pass (n = 0 and a zero time when none).
func (c *Controller) SetDeferredWakes(n int, oldest time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deferredCount, c.deferredOldest = n, oldest
}

// Snapshot is the state shown by GET /session-budget and pigtail slots.
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, _ := c.decideLocked(KindAgent)
	s := Snapshot{Enabled: a.Enabled, Busy: a.Busy, Budget: a.Budget, Gate: a.Decision, DeferredWakes: c.deferredCount}
	if c.deferredCount > 0 && !c.deferredOldest.IsZero() {
		s.DeferredOldestAge = c.now().Sub(c.deferredOldest)
	}
	return s
}

// LastAdmission is lastAdmissionAt (zero = none).
func (c *Controller) LastAdmission() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastAdmission
}

func newLeaseID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "sl_" + hex.EncodeToString(b[:])
}
