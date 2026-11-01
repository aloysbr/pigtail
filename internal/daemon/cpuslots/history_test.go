package cpuslots

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// tclock is a manually advanced clock, safe across goroutines.
type tclock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tclock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *tclock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func historyArbiter(t *testing.T, cfg Config) (*Arbiter, *tclock, *History, <-chan struct{}) {
	t.Helper()
	clk := &tclock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	h := NewHistory(filepath.Join(t.TempDir(), historyFileName))
	a, notify := newTestArbiter(t, cfg, WithClock(clk.now), WithHistory(h))
	return a, clk, h, notify
}

func records(t *testing.T, h *History) []pigtail.CPUSlotRecord {
	t.Helper()
	recs, err := h.Read(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestCPUSlotHistoryLineOnRelease(t *testing.T) {
	a, clk, h, notify := historyArbiter(t, testConfig(1))
	g1, err := a.Acquire(context.Background(), "g1", "first")
	if err != nil {
		t.Fatal(err)
	}
	enq := clk.now()
	done := make(chan Grant, 1)
	go func() {
		g, _ := a.Acquire(context.Background(), "g2", "second")
		done <- g
	}()
	waitForNotify(t, notify, "the second acquire to queue", func() bool { return a.Status().Waiting == 1 })
	clk.advance(2 * time.Second)
	a.Release(g1)
	g2 := <-done
	clk.advance(5 * time.Second)
	a.Release(g2)

	recs := records(t, h)
	if len(recs) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	r := recs[1]
	if r.Group != "g2" || r.Label != "second" || r.Outcome != "released" || r.WaitedMS != 2000 || r.HeldMS != 5000 || !r.EnqueuedAt.Equal(enq) {
		t.Fatalf("record = %+v", r)
	}
	if recs[0].Group != "g1" || recs[0].WaitedMS != 0 || recs[0].HeldMS != 2000 {
		t.Fatalf("first record = %+v", recs[0])
	}
}

func TestCPUSlotHistoryCancelled(t *testing.T) {
	a, clk, h, notify := historyArbiter(t, testConfig(1))
	if _, err := a.Acquire(context.Background(), "g1", "held"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := a.Acquire(ctx, "g2", "gives up"); errc <- err }()
	waitForNotify(t, notify, "the second acquire to queue", func() bool { return a.Status().Waiting == 1 })
	clk.advance(3 * time.Second)
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("want the context error")
	}
	recs := records(t, h)
	if len(recs) != 1 || recs[0].Outcome != "cancelled" || recs[0].Group != "g2" || recs[0].WaitedMS != 3000 || recs[0].HeldMS != 0 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestCPUSlotHistoryRevoked(t *testing.T) {
	a, clk, h, _ := historyArbiter(t, Config{Budget: 1, MaxHold: 10 * time.Minute})
	if _, err := a.Acquire(context.Background(), "g1", "wedged"); err != nil {
		t.Fatal(err)
	}
	clk.advance(11 * time.Minute)
	a.Sweep()
	recs := records(t, h)
	if len(recs) != 1 || recs[0].Outcome != "revoked" || recs[0].HeldMS != (11*time.Minute).Milliseconds() {
		t.Fatalf("records = %+v", recs)
	}
}

// One line per holder: the late Release of a revoked slot adds nothing.
func TestCPUSlotHistoryRevokedThenReleasedOneLine(t *testing.T) {
	a, clk, h, _ := historyArbiter(t, Config{Budget: 1, MaxHold: 10 * time.Minute})
	g, err := a.Acquire(context.Background(), "g1", "wedged")
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(11 * time.Minute)
	a.Sweep()
	a.Release(g)
	recs := records(t, h)
	if len(recs) != 1 || recs[0].Outcome != "revoked" {
		t.Fatalf("records = %+v, want exactly the revoked line", recs)
	}
}

// A grant racing the cancellation: the slot was booked, the client is gone;
// exactly one "released" line with a hold of about nothing.
func TestCPUSlotHistoryGrantRacingCancelReleased(t *testing.T) {
	a, _, h, notify := historyArbiter(t, testConfig(1))
	g1, err := a.Acquire(context.Background(), "g1", "held")
	if err != nil {
		t.Fatal(err)
	}
	// Runs in Acquire's ctx.Done branch, before it takes the lock: the select
	// picked cancellation, then the slot frees and is assigned to the waiter.
	a.afterSelectHook = func() { a.Release(g1) }
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := a.Acquire(ctx, "g2", "racer"); errc <- err }()
	waitForNotify(t, notify, "the racer to queue", func() bool { return a.Status().Waiting == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire err = %v, want context.Canceled", err)
	}
	var racer []pigtail.CPUSlotRecord
	for _, r := range records(t, h) {
		if r.Group == "g2" {
			racer = append(racer, r)
		}
	}
	if len(racer) != 1 || racer[0].Outcome != "released" || racer[0].HeldMS != 0 {
		t.Fatalf("racer records = %+v, want one released line with held 0", racer)
	}
	if st := a.Status(); st.Held != 0 {
		t.Fatalf("held = %d: the raced slot must be given back", st.Held)
	}
}

func TestCPUSlotHistoryDegradedWritesNothing(t *testing.T) {
	a, _, h, notify := historyArbiter(t, Config{Budget: 1, MaxHold: time.Hour, MaxWaiters: 1})
	g1, _ := a.Acquire(context.Background(), "g1", "held")
	go func() { _, _ = a.Acquire(context.Background(), "g2", "queued") }()
	waitForNotify(t, notify, "one waiter", func() bool { return a.Status().Waiting == 1 })
	g, err := a.Acquire(context.Background(), "g3", "overflow")
	if err != nil || !g.Degraded {
		t.Fatalf("grant = %+v err = %v, want degraded", g, err)
	}
	a.Release(g)
	if recs := records(t, h); len(recs) != 0 {
		t.Fatalf("a degraded grant booked nothing, got %+v", recs)
	}
	a.Release(g1)
}

func TestCPUSlotHistoryRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), historyFileName)
	h := NewHistory(path)
	if err := h.Append(pigtail.CPUSlotRecord{TS: time.Now(), Group: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Rotate(1 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("under the cap the file stays")
	}
	if err := h.Rotate(10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("past the cap .1 must exist: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the live file moved: %v", err)
	}
	if err := h.Append(pigtail.CPUSlotRecord{TS: time.Now(), Group: "new"}); err != nil {
		t.Fatal(err)
	}
	if recs := records(t, h); len(recs) != 2 || recs[0].Group != "old" || recs[1].Group != "new" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestCPUSlotHistoryWriteFailureNeverBlocksRelease(t *testing.T) {
	var logs []string
	h := NewHistory(filepath.Join(t.TempDir(), "no", "such", "dir", historyFileName))
	a, _ := newTestArbiter(t, testConfig(1), WithHistory(h),
		WithLogger(func(f string, _ ...any) { logs = append(logs, f) }))
	g, err := a.Acquire(context.Background(), "g1", "x")
	if err != nil {
		t.Fatal(err)
	}
	a.Release(g)
	if st := a.Status(); st.Held != 0 {
		t.Fatalf("held = %d", st.Held)
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %q, want one line", logs)
	}
}

func TestCPUSlotHistoryPathAndNil(t *testing.T) {
	if HistoryPath("") != "" || HistoryPath("/x/pigtail.db") != "/x/cpu-slots-history.jsonl" {
		t.Fatal("HistoryPath")
	}
	var h *History
	if recs, err := h.Read(time.Time{}); err != nil || len(recs) != 0 {
		t.Fatalf("nil history: %v %v", recs, err)
	}
	if err := h.Append(pigtail.CPUSlotRecord{}); err != nil {
		t.Fatal(err)
	}
}
