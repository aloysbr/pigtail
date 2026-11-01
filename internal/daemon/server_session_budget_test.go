package daemon

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget/sessbudgettest"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// budgetRig drives the session sampler of a running daemon: the load of a
// sessbudgettest fixture (its busy peers and its memory), a fake clock that
// moves past the pressure window at each tick (so each sample is judged
// alone), and a Wait that hands control back to the test after each sample.
type budgetRig struct {
	mu       sync.Mutex
	fixture  sessbudgettest.Fixture
	now      time.Time
	compress uint64 // cumulative compressor traffic
	taken    chan struct{}
	next     chan struct{}
}

const rigTick = sessbudgettest.WindowLength + time.Minute

func (r *budgetRig) wrap(d *sessbudget.Deps) {
	d.Peers = func() ([]pigtail.PeerSession, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		var out []pigtail.PeerSession
		for i := 1; i <= r.fixture.Busy; i++ {
			out = append(out, pigtail.PeerSession{SessionID: fmt.Sprintf("s%d", i), PID: 100 + i, ConfigDir: "/cfg"})
		}
		return out, nil
	}
	d.Memory = func() (sysmem.Memory, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return sysmem.Memory{Available: r.fixture.Memory.Available, Compressions: r.compress}, nil
	}
	d.Procs = func() (map[int]sysmem.Proc, error) { return map[int]sysmem.Proc{}, nil }
	d.ReadNative = func(_ string, pid int) (claudesession.Native, bool) {
		return claudesession.Native{SessionID: fmt.Sprintf("s%d", pid-100), Status: "busy"}, true
	}
	d.SessionAlive = func(string, int, string) bool { return true }
	d.Now = func() time.Time {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.now
	}
	d.Wait = func(ctx context.Context, _ time.Duration) {
		select {
		case r.taken <- struct{}{}:
		case <-ctx.Done():
			return
		}
		select {
		case <-r.next:
		case <-ctx.Done():
		}
	}
}

// tick sets the load for the next sample and lets the sampler take it.
func (r *budgetRig) tick(f sessbudgettest.Fixture) {
	r.mu.Lock()
	r.fixture = f
	r.now = r.now.Add(rigTick)
	r.compress += uint64(f.Memory.CompressorRate * rigTick.Seconds())
	r.mu.Unlock()
	r.next <- struct{}{}
	<-r.taken
}

// Samples reach the budget through Sampler.OnSample: n is the sampled busy
// count, m the floor after red samples and the ceiling after green ones.
func TestDaemonSampleReachesBudget(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, _ := store.Open(":memory:")
	defer st.Close()
	rig := &budgetRig{fixture: sessbudgettest.AboveFloor, now: sessbudgettest.Start,
		taken: make(chan struct{}), next: make(chan struct{})}
	cfg := sessbudgettest.Config()
	s, err := New(Config{
		SocketPath: testSocketPath(t), SessionSampleSeconds: int(sessbudgettest.Period / time.Second),
		SessionBudget: cfg, SessionSamplerWrap: rig.wrap,
	}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := startDaemon(t, s)
	defer stop()
	<-rig.taken // the first sample, taken at once

	red := sessbudgettest.RedFull
	rig.tick(red)
	if a := s.sessionBudget.Check(); a.Busy != red.Busy || a.Budget != sessbudgettest.Floor {
		t.Fatalf("after red samples: %d/%d, want %d/%d", a.Busy, a.Budget, red.Busy, sessbudgettest.Floor)
	}
	green := sessbudgettest.AboveFloor
	rig.tick(green)
	if a := s.sessionBudget.Check(); a.Busy != green.Busy || a.Budget != sessbudgettest.Ceiling {
		t.Fatalf("after green samples: %d/%d, want %d/%d", a.Busy, a.Budget, green.Busy, sessbudgettest.Ceiling)
	}
}

func TestServerWiresSessionBudgetService(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.services == nil || s.services.SessionBudget == nil || s.services.SessionBudget != s.sessionBudget {
		t.Fatal("New must set Services.SessionBudget to the server's controller")
	}
}
