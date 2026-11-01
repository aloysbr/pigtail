package sessbudget_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget/sessbudgettest"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const floor = sessbudgettest.Floor

// newCtl is a gate-on controller on a fake clock, put in fixture f's state.
func newCtl(t *testing.T, f sessbudgettest.Fixture) (*sessbudget.Controller, *sessbudgettest.Clock) {
	t.Helper()
	clk := sessbudgettest.NewClock()
	c := sessbudget.NewController(sessbudgettest.Config(), clk.Now, t.Logf)
	for _, s := range f.Samples(clk) {
		c.Observe(s)
	}
	return c, clk
}

// pastGap moves the clock past the gap and observes a later sample of f.
func pastGap(c *sessbudget.Controller, clk *sessbudgettest.Clock, f sessbudgettest.Fixture) {
	clk.Advance(sessbudgettest.MinGap)
	c.Observe(f.AdvanceSample(clk))
}

func busy(c *sessbudget.Controller) int { return c.Input(sessbudget.KindAgent).Busy }

func TestDecideInputFromController(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	in := c.Input(sessbudget.KindAgent)
	if in.Busy != f.Busy || in.Budget != f.Budget() || !in.Observed || in.HasAdmission {
		t.Fatalf("input before any admission: %+v", in)
	}
	a := c.TryAdmit(sessbudget.KindAgent)
	if !a.Admitted || a.LeaseID == "" {
		t.Fatalf("first agent ask: %+v", a)
	}
	admittedAt := clk.Now()
	clk.Advance(10 * time.Second)
	in = c.Input(sessbudget.KindAgent)
	if in.Busy != f.Busy+1 || !in.HasAdmission || in.SinceAdmission != 10*time.Second || in.SampleSinceAdmission {
		t.Fatalf("input after the admission: %+v", in)
	}
	if !c.LastAdmission().Equal(admittedAt) {
		t.Fatalf("lastAdmissionAt %v, want %v", c.LastAdmission(), admittedAt)
	}
	if got, want := c.Check().Decision, sessbudget.Decide(in); got != want {
		t.Fatalf("Check %+v is not Decide of the controller's input %+v", got, want)
	}
	c.Observe(f.AdvanceSample(clk))
	if in = c.Input(sessbudget.KindAgent); !in.SampleSinceAdmission || in.Busy != f.Busy+1 {
		t.Fatalf("input after a later sample: %+v", in)
	}
}

func TestHumanTryAdmitNeverRecords(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, _ := newCtl(t, f)
	if a := c.TryAdmit(sessbudget.KindHuman); !a.Admitted {
		t.Fatalf("human refused: %+v", a)
	}
	if !c.LastAdmission().IsZero() || busy(c) != f.Busy {
		t.Fatalf("human admission recorded: last %v, busy %d", c.LastAdmission(), busy(c))
	}
}

func TestAgentRefusalCarriesBusyAndBudgetStoresNothing(t *testing.T) {
	f := sessbudgettest.RedFull
	c, _ := newCtl(t, f)
	a := c.TryAdmit(sessbudget.KindAgent)
	if a.Admitted || a.Row != 4 || a.Reason != sessbudgettest.RedCause || a.LeaseID != "" {
		t.Fatalf("refusal: %+v", a)
	}
	if a.Busy != f.Busy || a.Budget != f.Budget() {
		t.Fatalf("refusal carries %d/%d, want %d/%d", a.Busy, a.Budget, f.Busy, f.Budget())
	}
	if !c.LastAdmission().IsZero() || busy(c) != f.Busy {
		t.Fatalf("refusal stored something: last %v, busy %d", c.LastAdmission(), busy(c))
	}
}

func TestCheckIsReadOnly(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, _ := newCtl(t, f)
	for i := 0; i < 3; i++ {
		if a := c.Check(); !a.Admitted {
			t.Fatalf("check: %+v", a)
		}
	}
	if !c.LastAdmission().IsZero() || busy(c) != f.Busy {
		t.Fatalf("Check recorded: last %v, busy %d", c.LastAdmission(), busy(c))
	}
}

func TestWakeRefusedStoresNothing(t *testing.T) {
	c, clk := newCtl(t, sessbudgettest.RedFull)
	if ok, reason := c.BudgetAcquire("wake", "idle-1"); ok || reason != sessbudgettest.RedCause {
		t.Fatalf("wake under RedFull: %v %q", ok, reason)
	}
	if !c.LastAdmission().IsZero() {
		t.Fatal("a refused wake recorded an admission")
	}
	c.Observe(sessbudgettest.BelowFloor.AdvanceSample(clk))
	if a := c.TryAdmit(sessbudget.KindAgent); !a.Admitted || a.Row != 3 {
		t.Fatalf("agent once BelowFloor: %+v", a)
	}
	if ok, reason := c.BudgetAcquire("wake", "idle-1"); !ok {
		t.Fatalf("second wake ask not decided like a first: %q", reason)
	}
}

func TestWakeRefusedDoesNotBlockAnotherWake(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	if a := c.TryAdmit(sessbudget.KindAgent); !a.Admitted {
		t.Fatalf("first: %+v", a)
	}
	clk.Advance(5 * time.Second)
	if ok, reason := c.BudgetAcquire("wake", "idle-1"); ok || !strings.HasPrefix(reason, "démarrage progressif") {
		t.Fatalf("wake A inside the gap: %v %q", ok, reason)
	}
	pastGap(c, clk, f)
	if ok, reason := c.BudgetAcquire("wake", "idle-2"); !ok {
		t.Fatalf("wake B past the gap with a sample since: %q", reason)
	}
}

func TestReleaseRestoresGap(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	a1 := c.TryAdmit(sessbudget.KindAgent)
	t1 := clk.Now()
	pastGap(c, clk, f)
	a2 := c.TryAdmit(sessbudget.KindAgent)
	if !a1.Admitted || !a2.Admitted {
		t.Fatalf("admissions: %+v %+v", a1, a2)
	}
	c.ReleaseLease(a2.LeaseID)
	if !c.LastAdmission().Equal(t1) {
		t.Fatalf("lastAdmissionAt %v after release, want %v", c.LastAdmission(), t1)
	}
	if a := c.TryAdmit(sessbudget.KindAgent); !a.Admitted {
		t.Fatalf("next ask delayed by the released grant: %+v", a)
	}
	// No-op when a later admission exists.
	pastGap(c, clk, f)
	a3 := c.TryAdmit(sessbudget.KindAgent)
	pastGap(c, clk, f)
	a4 := c.TryAdmit(sessbudget.KindAgent)
	t4 := clk.Now()
	if !a3.Admitted || !a4.Admitted {
		t.Fatalf("admissions: %+v %+v", a3, a4)
	}
	c.ReleaseLease(a3.LeaseID)
	if !c.LastAdmission().Equal(t4) {
		t.Fatalf("release of an older grant moved lastAdmissionAt to %v, want %v", c.LastAdmission(), t4)
	}
}

func TestDeferredGaugeReplacedEachCall(t *testing.T) {
	c, clk := newCtl(t, sessbudgettest.AboveFloor)
	c.SetDeferredWakes(2, clk.Now().Add(-time.Minute))
	if s := c.Snapshot(); s.DeferredWakes != 2 {
		t.Fatalf("gauge %+v", s)
	}
	c.SetDeferredWakes(0, time.Time{})
	if s := c.Snapshot(); s.DeferredWakes != 0 || s.DeferredOldestAge != 0 {
		t.Fatalf("gauge not replaced: %+v", s)
	}
}

func TestSnapshotCarriesDeferredGauge(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	c.SetDeferredWakes(2, clk.Now().Add(-3*time.Minute))
	s := c.Snapshot()
	if !s.Enabled || s.DeferredWakes != 2 || s.DeferredOldestAge != 3*time.Minute {
		t.Fatalf("snapshot %+v", s)
	}
	if s.Busy != f.Busy || s.Budget != f.Budget() {
		t.Fatalf("snapshot n/m %d/%d", s.Busy, s.Budget)
	}
}

// freedCounter wires OnFreed to a counter.
func freedCounter(c *sessbudget.Controller) *atomic.Int32 {
	var n atomic.Int32
	c.OnFreed = func() { n.Add(1) }
	return &n
}

func TestFreedFiresOnBusyDrop(t *testing.T) {
	c, clk := newCtl(t, sessbudgettest.AboveFloor.WithBusy(2*floor))
	freed := freedCounter(c)
	c.Observe(sessbudgettest.AboveFloor.WithBusy(2*floor - 1).AdvanceSample(clk))
	if freed.Load() != 1 {
		t.Fatalf("OnFreed called %d times, want 1", freed.Load())
	}
}

func TestFreedAtMostOncePerSample(t *testing.T) {
	c, clk := newCtl(t, sessbudgettest.AboveFloor.WithBusy(2*floor))
	freed := freedCounter(c)
	s := sessbudgettest.AboveFloor.WithBusy(2*floor - 1).AdvanceSample(clk)
	c.Observe(s)
	c.Observe(s)
	if freed.Load() != 1 {
		t.Fatalf("OnFreed called %d times for one sample", freed.Load())
	}
}

func TestFreedNotFiredWhileStillFull(t *testing.T) {
	c, clk := newCtl(t, sessbudgettest.RedFull.WithBusy(floor+1))
	freed := freedCounter(c)
	c.Observe(sessbudgettest.RedFull.AdvanceSample(clk)) // dropped to n = m: still full
	c.Observe(sessbudgettest.RedFull.WithBusy(floor + 2).AdvanceSample(clk))
	if freed.Load() != 0 {
		t.Fatalf("OnFreed called %d times while full or rising", freed.Load())
	}
}

func TestControllerRedSamplesLowerBudgetToFloor(t *testing.T) {
	c, _ := newCtl(t, sessbudgettest.RedFull)
	if m := c.TryAdmit(sessbudget.KindAgent).Budget; m != floor {
		t.Errorf("TryAdmit m %d", m)
	}
	if m := c.Check().Budget; m != floor {
		t.Errorf("Check m %d", m)
	}
	if m := c.Snapshot().Budget; m != floor {
		t.Errorf("Snapshot m %d", m)
	}
	if m := c.Input(sessbudget.KindAgent).Budget; m != floor {
		t.Errorf("input m %d", m)
	}
	g, _ := newCtl(t, sessbudgettest.AboveFloor)
	if m := g.Snapshot().Budget; m != sessbudgettest.Ceiling {
		t.Errorf("green m %d, want the ceiling", m)
	}
	if m := g.Check().Budget; m != sessbudgettest.Ceiling {
		t.Errorf("green Check m %d, want the ceiling", m)
	}
}

func disabledCtl(t *testing.T, f sessbudgettest.Fixture) *sessbudget.Controller {
	clk := sessbudgettest.NewClock()
	cfg := sessbudgettest.Config()
	cfg.Ceiling = 0
	c := sessbudget.NewController(cfg, clk.Now, t.Logf)
	for _, s := range f.Samples(clk) {
		c.Observe(s)
	}
	return c
}

func TestDisabledControllerAdmitsAll(t *testing.T) {
	// Before any sample too.
	clk := sessbudgettest.NewClock()
	cfg := sessbudgettest.Config()
	cfg.Ceiling = 0
	if a := sessbudget.NewController(cfg, clk.Now, t.Logf).TryAdmit(sessbudget.KindAgent); !a.Admitted {
		t.Fatalf("disabled, no sample: %+v", a)
	}
	c := disabledCtl(t, sessbudgettest.RedFull.WithBusy(4*sessbudgettest.Ceiling))
	for i := 0; i < 3; i++ {
		if a := c.TryAdmit(sessbudget.KindAgent); !a.Admitted {
			t.Fatalf("disabled agent ask %d: %+v", i, a)
		}
		if ok, reason := c.BudgetAcquire("wake", fmt.Sprintf("idle-%d", i)); !ok {
			t.Fatalf("disabled wake %d: %q", i, reason)
		}
	}
	if a := c.Check(); !a.Admitted || a.Enabled {
		t.Fatalf("disabled check: %+v", a)
	}
	if s := c.Snapshot(); s.Enabled {
		t.Fatalf("disabled snapshot: %+v", s)
	}
}

func TestDisabledGateNeverFlags(t *testing.T) {
	c := disabledCtl(t, sessbudgettest.RedFull.WithBusy(4*sessbudgettest.Ceiling))
	if a := c.TryAdmit(sessbudget.KindHuman); !a.Admitted || a.OverBudget || a.Reason != "" {
		t.Fatalf("disabled human: %+v", a)
	}
}

func TestPendingLeaseCountsUntilReleased(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	a := c.TryAdmit(sessbudget.KindAgent)
	for i := 0; i < 3; i++ {
		c.Observe(f.AdvanceSample(clk))
		if busy(c) != f.Busy+1 {
			t.Fatalf("pending lease not counted after %d samples: %d", i+1, busy(c))
		}
	}
	c.ReleaseLease(a.LeaseID)
	if busy(c) != f.Busy {
		t.Fatalf("released lease still counted: %d", busy(c))
	}
}

func TestPendingLeaseCountsUntilSampleAfterLaunched(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	a := c.TryAdmit(sessbudget.KindAgent)
	c.Observe(f.AdvanceSample(clk))
	clk.Advance(5 * time.Second)
	c.MarkLaunched(a.LeaseID)
	if busy(c) != f.Busy+1 {
		t.Fatalf("launched lease not counted before a later sample: %d", busy(c))
	}
	c.Observe(f.Sample(clk.Now().Add(-time.Second))) // taken before the report
	if busy(c) != f.Busy+1 {
		t.Fatalf("a sample taken before the report ended the lease: %d", busy(c))
	}
	c.Observe(f.AdvanceSample(clk))
	if busy(c) != f.Busy {
		t.Fatalf("launched lease still counted after a later sample: %d", busy(c))
	}
	c.MarkLaunched("unknown") // no panic
}

func TestPendingWakeSecondAskRefusedBeforeNewSample(t *testing.T) {
	c, _ := newCtl(t, sessbudgettest.RedFull.WithBusy(floor-1))
	if ok, reason := c.BudgetAcquire("wake", "idle-1"); !ok {
		t.Fatalf("first wake: %q", reason)
	}
	if ok, reason := c.BudgetAcquire("wake", "idle-2"); ok || reason != sessbudgettest.RedCause {
		t.Fatalf("second wake under red: %v %q, want refused %q", ok, reason, sessbudgettest.RedCause)
	}
	g, _ := newCtl(t, sessbudgettest.BelowFloor.WithBusy(floor-1))
	if ok, reason := g.BudgetAcquire("wake", "idle-1"); !ok {
		t.Fatalf("first wake: %q", reason)
	}
	if ok, reason := g.BudgetAcquire("wake", "idle-2"); ok || !strings.HasPrefix(reason, "démarrage progressif") {
		t.Fatalf("second wake under green: %v %q, want the gap", ok, reason)
	}
}

func TestPendingWakeCountedUntilSessionBusy(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	// busy-<n+1> is not busy in f's samples: an idle recipient.
	recipient := fmt.Sprintf("busy-%d", f.Busy+1)
	if ok, reason := c.BudgetAcquire("wake", recipient); !ok {
		t.Fatalf("wake: %q", reason)
	}
	if busy(c) != f.Busy+1 {
		t.Fatalf("granted wake not counted: %d", busy(c))
	}
	c.Observe(f.WithBusy(f.Busy + 1).AdvanceSample(clk)) // the recipient now busy
	if busy(c) != f.Busy+1 {
		t.Fatalf("busy recipient counted %d, want once (%d)", busy(c), f.Busy+1)
	}
}

func TestPendingWakeIdleDroppedAtSecondSample(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	if ok, _ := c.BudgetAcquire("wake", "idle-1"); !ok {
		t.Fatal("wake refused")
	}
	c.Observe(f.AdvanceSample(clk))
	if busy(c) != f.Busy+1 {
		t.Fatalf("idle wake dropped at the first sample: %d", busy(c))
	}
	c.Observe(f.AdvanceSample(clk))
	if busy(c) != f.Busy {
		t.Fatalf("idle wake still counted at the second sample: %d", busy(c))
	}
}

func TestPendingWakeReleasedDroppedAtOnce(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, _ := newCtl(t, f)
	if ok, _ := c.BudgetAcquire("wake", "idle-1"); !ok {
		t.Fatal("wake refused")
	}
	c.BudgetRelease("idle-1")
	if busy(c) != f.Busy || !c.LastAdmission().IsZero() {
		t.Fatalf("released wake: busy %d, last %v", busy(c), c.LastAdmission())
	}
	c.BudgetRelease("idle-1") // idempotent
}

func TestPendingSpawnAndWakeAddUp(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	if a := c.TryAdmit(sessbudget.KindAgent); !a.Admitted {
		t.Fatal("spawn refused")
	}
	pastGap(c, clk, f)
	if ok, reason := c.BudgetAcquire("wake", "idle-1"); !ok {
		t.Fatalf("wake: %q", reason)
	}
	if busy(c) != f.Busy+2 {
		t.Fatalf("n %d, want sample busy + 2", busy(c))
	}
}

func TestPendingWakeDropNeverFiresFreed(t *testing.T) {
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	freed := freedCounter(c)
	recipient := fmt.Sprintf("busy-%d", f.Busy+1)
	if ok, _ := c.BudgetAcquire("wake", recipient); !ok {
		t.Fatal("wake refused")
	}
	up := f.WithBusy(f.Busy + 1)
	c.Observe(up.AdvanceSample(clk)) // dropped: recipient busy
	clk.Advance(sessbudgettest.MinGap)
	if ok, _ := c.BudgetAcquire("wake", "idle-1"); !ok {
		t.Fatal("second wake refused")
	}
	c.Observe(up.AdvanceSample(clk))
	c.Observe(up.AdvanceSample(clk)) // dropped: second sample, idle
	if freed.Load() != 0 {
		t.Fatalf("OnFreed fired %d times on pending-wake drops", freed.Load())
	}
	c.Observe(f.AdvanceSample(clk)) // a real busy drop
	if freed.Load() != 1 {
		t.Fatalf("OnFreed fired %d times on a real busy drop", freed.Load())
	}
}

func TestPendingLeaseSafetyExpiry(t *testing.T) {
	got := sessbudget.PendingLeaseMax
	if want := spawnreg.ReserveCap + spawnreg.PendingWindow + 2*time.Minute; got != want {
		t.Fatalf("PendingLeaseMax %v, want reserve cap + pending window + 2 min (%v)", got, want)
	}
	if got != 24*time.Minute {
		t.Fatalf("PendingLeaseMax %v, want 24 min", got)
	}
	f := sessbudgettest.AboveFloor
	c, clk := newCtl(t, f)
	if a := c.TryAdmit(sessbudget.KindAgent); !a.Admitted {
		t.Fatal("refused")
	}
	clk.Advance(sessbudget.PendingLeaseMax - time.Second)
	if busy(c) != f.Busy+1 {
		t.Fatalf("lease gone before its expiry: %d", busy(c))
	}
	clk.Advance(time.Second)
	if busy(c) != f.Busy {
		t.Fatalf("lease counted past its expiry: %d", busy(c))
	}
}

// A sample the real sampler takes reaches the controller through OnSample.
func TestObserveFeedsControllerFromSampler(t *testing.T) {
	f := sessbudgettest.AboveFloor
	clk := sessbudgettest.NewClock()
	c := sessbudget.NewController(sessbudgettest.Config(), clk.Now, t.Logf)
	var peers []pigtail.PeerSession
	for i := 1; i <= f.Busy; i++ {
		peers = append(peers, pigtail.PeerSession{SessionID: fmt.Sprintf("s%d", i), PID: 100 + i, ConfigDir: "/cfg"})
	}
	s := sessbudget.New(sessbudget.Deps{
		Peers:  func() ([]pigtail.PeerSession, error) { return peers, nil },
		Memory: func() (sysmem.Memory, error) { return sysmem.Memory{Available: f.Memory.Available}, nil },
		ReadNative: func(_ string, pid int) (claudesession.Native, bool) {
			return claudesession.Native{SessionID: fmt.Sprintf("s%d", pid-100), Status: "busy"}, true
		},
		SessionAlive: func(string, int, string) bool { return true },
		Now:          clk.Now,
		Wait:         func(ctx context.Context, _ time.Duration) { <-ctx.Done() },
	}, sessbudget.NewHistory(filepath.Join(t.TempDir(), "s.jsonl")), sessbudgettest.Period)
	s.OnSample = c.Observe
	if _, err := s.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := c.Check(); a.Busy != f.Busy || a.Budget != f.Budget() {
		t.Fatalf("Check reads %d/%d, want the sample's %d/%d", a.Busy, a.Budget, f.Busy, f.Budget())
	}
}

func TestGateOnWithSamplerOffRefused(t *testing.T) {
	cfg := sessbudgettest.Config()
	cfg.SamplePeriod = 0
	err := sessbudget.ValidateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "-session-ceiling") || !strings.Contains(err.Error(), "-session-sample-seconds") {
		t.Fatalf("gate on, sampler off: %v", err)
	}
	cfg.Ceiling = 0
	if err := sessbudget.ValidateConfig(cfg); err != nil {
		t.Fatalf("gate off, sampler off: %v", err)
	}
	if err := sessbudget.ValidateConfig(sessbudgettest.Config()); err != nil {
		t.Fatalf("fixture config: %v", err)
	}
	if err := sessbudget.ValidateConfig(sessbudget.DefaultConfig()); err != nil {
		t.Fatalf("default config: %v", err)
	}
}

func TestValidateConfigNamesFlagAndValue(t *testing.T) {
	cases := []struct {
		name        string
		floor, ceil int
		sample      time.Duration
		want        string
	}{
		{"floor negative", -1, 2, 30 * time.Second, "-session-floor -1: must not be negative"},
		{"ceiling negative", 1, -3, 30 * time.Second, "-session-ceiling -3: must not be negative"},
		{"both negative", -1, -3, 30 * time.Second, "-session-floor -1: must not be negative; -session-ceiling -3: must not be negative"},
		{"floor above ceiling", 5, 2, 30 * time.Second, "-session-floor 5: must be between 1 and -session-ceiling (2)"},
		{"floor zero", 0, 2, 30 * time.Second, "-session-floor 0: must be between 1 and -session-ceiling (2)"},
		{"sampler off", 1, 2, 0, "-session-ceiling 2 turns the session gate on, but -session-sample-seconds is 0: the gate needs the sampler"},
	}
	for _, c := range cases {
		cfg := sessbudgettest.Config()
		cfg.Floor, cfg.Ceiling, cfg.SamplePeriod = c.floor, c.ceil, c.sample
		err := sessbudget.ValidateConfig(cfg)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}
