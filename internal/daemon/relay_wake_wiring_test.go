package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
)

// The idle wake (lot 2) is wired with every seam, and reads the same frozen
// sessions as the sidebar (D32).
func TestServerWiresRelayWake(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := s.relay.WakeDepsForTest()
	if w == nil || w.ReadNative == nil || w.SessionAlive == nil || w.PidAlive == nil || w.Frozen == nil ||
		w.CmuxSessions == nil || w.CmuxSessionsAtSend == nil || w.Focused == nil || w.ReadScreen == nil || w.SendLine == nil {
		t.Fatalf("server.go must wire every wake seam: %+v", w)
	}
	if w.APIReachable == nil || w.LastAPIError == nil || w.HumanPromptsSince == nil {
		t.Fatalf("server.go must wire the auto-retry seams: %+v", w)
	}
	_ = maintenance.Save(filepath.Dir(s.cfg.SocketPath), maintenance.State{ID: "mnt_w", On: true, Freeze: true, Reason: "wiring", Since: time.Now()})
	_ = maintenance.MarkFrozen(filepath.Dir(s.cfg.SocketPath), "s-w", "mnt_w")
	if got := w.Frozen(); !got["s-w"] {
		t.Fatalf("wake Frozen = %v, want s-w (D32)", got)
	}
}

// The idle session close threshold reaches the relay: Config seconds -> the
// relay's duration; unset = off.
func TestServerWiresIdlePause(t *testing.T) {
	for _, c := range []struct {
		secs int
		want time.Duration
	}{{3600, time.Hour}, {0, 0}} {
		st, _ := store.Open(":memory:")
		s, err := New(Config{SocketPath: testSocketPath(t), IdleSessionPauseSeconds: c.secs}, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.relay.IdlePauseForTest(); got != c.want {
			t.Errorf("IdleSessionPauseSeconds %d: relay IdlePause = %v, want %v", c.secs, got, c.want)
		}
		if s.relay.RelaunchDirForTest() == "" {
			t.Error("RelaunchDir empty")
		}
		w := s.relay.WakeDepsForTest()
		if w == nil || w.Background == nil || w.SessionAlive == nil || w.Signal == nil || w.WaitGone == nil || w.ProcessEnviron == nil {
			t.Errorf("pause seams not all wired: %+v", w)
		}
		st.Close()
	}
}

// Session budget (Tâche 8): the production construction hands the relay the
// controller's two-argument admission, its release and the deferred gauge.
func TestServerWiresBudgetAcquireIntoWakeDeps(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := s.relay.WakeDepsForTest()
	if w == nil || w.BudgetAcquire == nil || w.BudgetRelease == nil || w.BudgetDeferred == nil {
		t.Fatalf("server.go must wire BudgetAcquire, BudgetRelease and BudgetDeferred: %+v", w)
	}
	// Gate off by default: every ask is granted, and nothing is stored.
	if ok, why := w.BudgetAcquire("wake", "s-wired"); !ok {
		t.Fatalf("a disabled gate refused a wake: %s", why)
	}
	w.BudgetRelease("s-wired")
	w.BudgetDeferred(2, time.Now().Add(-time.Minute))
	if got := s.sessionBudget.Snapshot().DeferredWakes; got != 2 {
		t.Fatalf("snapshot deferred = %d, want the relay's gauge (2)", got)
	}
	w.BudgetDeferred(0, time.Time{})
	if got := s.sessionBudget.Snapshot().DeferredWakes; got != 0 {
		t.Fatalf("snapshot deferred = %d, want the gauge replaced by zero", got)
	}
}

// OnFreed is nil out of Task 4; server.go sets it to the relay's KickWake here
// and only here.
func TestServerWiresFreedToKickWake(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.sessionBudget.OnFreed == nil {
		t.Fatal("server.go must set sessionBudget.OnFreed to relay.KickWake")
	}
	if s.relay.WakeKickPendingForTest() {
		t.Fatal("a kick is pending before anything freed")
	}
	s.sessionBudget.OnFreed()
	if !s.relay.WakeKickPendingForTest() {
		t.Fatal("OnFreed did not reach the relay's KickWake")
	}
}

// The handoff drafts live next to the socket, where the Stop hook writes them
// (cmd/pigtail handoffDraftDir): a test daemon on its own socket never sees
// the real daemon's drafts.
func TestServerWiresHandoffDraftDir(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	sock := testSocketPath(t)
	s, err := New(Config{SocketPath: sock}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.relay.DraftDirForTest(), filepath.Join(filepath.Dir(sock), "handoff-draft"); got != want {
		t.Fatalf("DraftDir = %q, want %q", got, want)
	}
}
