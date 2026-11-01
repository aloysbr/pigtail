package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/cmuxwatch"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/wsmarker"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The relay wakes the sidebar loop on a server death or eviction, and on
// nothing else: "dead" must not wait for the slow tick, and an unrelated
// event must not cost a port probe.
func TestServerEventsWakeTheSidebarLoop(t *testing.T) {
	s := &Server{bus: events.New()}
	woken := make(chan struct{}, 8)
	watch := s.watchServerEvents(func() { woken <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { watch(ctx); close(done) }()

	s.bus.Publish(pigtail.Event{Type: pigtail.EventResourcesWarning})
	s.bus.Publish(pigtail.Event{Type: pigtail.EventServerDied})
	s.bus.Publish(pigtail.Event{Type: pigtail.EventServerEvicted})

	for i := 0; i < 2; i++ {
		select {
		case <-woken:
		case <-time.After(2 * time.Second):
			t.Fatalf("woken %d times, want 2", i)
		}
	}
	select {
	case <-woken:
		t.Fatal("woken by an unrelated event")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay kept running after the context ended")
	}
}

// TestServerWiresMaintenanceSidebar is the sidebar half of the wiring guard:
// it breaks if cmuxsync_wiring.go stops giving the state watch to the loop.
func TestServerWiresMaintenanceSidebar(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "") // the daemon dir must come from the socket, whatever verify exports
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = maintenance.Save(filepath.Dir(s.cfg.SocketPath), maintenance.State{ID: "mnt_w", On: true, Freeze: true, Reason: "wiring", Since: time.Now()})
	deps := s.cmuxSyncDeps()
	if deps.Maintenance == nil {
		t.Fatal("cmuxsync_wiring.go lost Deps.Maintenance")
	}
	if deps.MaintenanceDir != filepath.Dir(s.cfg.SocketPath) {
		t.Fatalf("Deps.MaintenanceDir = %q, want the daemon's state dir", deps.MaintenanceDir)
	}
	if st, ok, err := deps.Maintenance(); err != nil || !ok || st.ID != "mnt_w" {
		t.Fatalf("sidebar deps read %+v %v %v", st, ok, err)
	}
	// D4: the frozen markers reach the loop.
	if deps.FrozenSessions == nil {
		t.Fatal("cmuxsync_wiring.go lost Deps.FrozenSessions")
	}
	if err := maintenance.MarkFrozen(filepath.Dir(s.cfg.SocketPath), "s-w", "mnt_w"); err != nil {
		t.Fatal(err)
	}
	if got := deps.FrozenSessions(); len(got) != 1 || !got["s-w"] {
		t.Fatalf("FrozenSessions = %v", got)
	}
}

// D1: the loop reads the cell ids from the cell table; no table, no
// recognition and no panic. XDG_CONFIG_HOME keeps the real one out.
func TestServerWiresKnownCells(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.KnownCells == nil {
		t.Fatal("cmuxsync_wiring.go lost Deps.KnownCells")
	}
	if got := deps.KnownCells(); got != nil {
		t.Fatalf("no table: %v", got)
	}
	if err := os.MkdirAll(filepath.Join(cfg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	table := "cells:\n  mp:\n    config_dir: /tmp/x-mp\n    scope: perso\n  jt:\n    config_dir: /tmp/x-jt\n    scope: globex\n"
	if err := os.WriteFile(filepath.Join(cfg, "pigtail", "claude-cells.yaml"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := deps.KnownCells(); len(got) != 2 || !got["mp"] || !got["jt"] {
		t.Fatalf("KnownCells = %v", got)
	}
}

func TestConflictPillFindsCmuxWorkspaceWithoutMarker(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	wt := t.TempDir() // no server marker anywhere
	seedWorkspace(t, st, &pigtail.Workspace{ID: "ws_392f9b", ProjectID: "prj_c", Path: wt,
		Ports: map[string]int{"dev": 3013}, State: pigtail.StateBuilding, BackendKind: "git"}) // existing helper, dispatch_integration_test.go:422
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "sess-1", Cwd: wt, WorkspaceID: "ws_392f9b",
		CmuxWorkspaceID: "a1b2c3d4-0000-0000-0000-000000000001"}); err != nil {
		t.Fatal(err)
	}
	srv := newServerWithRAM(t, st, rammon.WithServersFunc(func(pigtail.Workspace) []rammon.ServerProc {
		return []rammon.ServerProc{{Name: "dev", Kind: "dev", Port: 3013, DBPort: 3013}}
	}), rammon.WithPortPIDs(func(int) (bool, []int, error) { return true, []int{4242}, nil }),
		rammon.WithCwds(func([]int) (map[int]string, error) { return map[int]string{4242: "/elsewhere"}, nil }),
		rammon.WithPathExists(func(string) bool { return true }))
	snap := srv.cmuxServers()
	if len(snap.Conflicts) != 1 || snap.Conflicts[0].Owner != "A1B2C3D4-0000-0000-0000-000000000001" {
		t.Fatalf("conflicts %+v, want one under the session's cmux workspace", snap.Conflicts)
	}
}

// newServerWithRAM builds a daemon on st whose RAM monitor is built from opts
// (silent log first), for wiring tests that drive rammon's seams.
func newServerWithRAM(t *testing.T, st *store.Store, opts ...rammon.Option) *Server {
	t.Helper()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.ramMonitor = rammon.New(events.New(), st, append([]rammon.Option{rammon.WithLogger(func(string, ...any) {})}, opts...)...)
	return s
}

// A held wait the loop judges over goes through the relay (marker and
// session.wait_changed), like ClearWaits; before the relay exists it falls
// back to the store.
func TestServerWiresClearHeldWait(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.ClearHeldWait == nil {
		t.Fatal("cmuxsync_wiring.go lost Deps.ClearHeldWait")
	}
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	held := pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, HolderPID: 4242, Since: time.Now(), Until: &until}
	if err := st.SetSessionWait("s1", held); err != nil {
		t.Fatal(err)
	}
	if gone, err := deps.ClearHeldWait("s1", 4242); err != nil || !gone { // no relay yet: store
		t.Fatalf("fallback = %v %v", gone, err)
	}

	// With a relay: the marker follows.
	mk := filepath.Join(t.TempDir(), "wait")
	s.relay = handlers.NewRelay(&handlers.Services{Store: st, Bus: s.bus}, handlers.RelayConfig{WaitMarkerDir: mk})
	if err := st.SetSessionWait("s1", held); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mk, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mk, "s1"), []byte("mac\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := s.bus.Subscribe(string(pigtail.EventSessionWaitChanged))
	defer sub.Close()
	if gone, err := deps.ClearHeldWait("s1", 4242); err != nil || !gone {
		t.Fatalf("via relay = %v %v", gone, err)
	}
	if _, err := os.Stat(filepath.Join(mk, "s1")); err == nil {
		t.Fatal("marker left: the clear did not go through the relay")
	}
	select {
	case e := <-sub.C:
		if e.Data["source"] != "held" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no session.wait_changed")
	}
}

// TestServerWiresMarkersChangedSidebar breaks if cmuxsync_wiring.go stops handing the marker rewrite to the sidebar loop (S6).
func TestServerWiresMarkersChangedSidebar(t *testing.T) {
	md := t.TempDir()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", md) // read by New: the markers land under md
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.MarkersChanged == nil {
		t.Fatal("cmuxsync_wiring.go lost Deps.MarkersChanged")
	}
	if err := st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: probeCmuxWS, Scope: "perso", DeclaredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deps.MarkersChanged()
	marker := filepath.Join(md, wsmarker.DirName, probeCmuxWS)
	if got, err := os.ReadFile(marker); err != nil || string(got) != "off-topic\n" {
		t.Fatalf("marker after Deps.MarkersChanged = %q %v, want \"off-topic\\n\"", got, err)
	}
}

// Task 16: the loop purges the Standby of a closed workspace through the
// relay; without a relay yet, the store alone.
func TestServerWiresStandbyGone(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.StandbyGone == nil {
		t.Fatal("cmuxsync_wiring.go lost Deps.StandbyGone")
	}
	if err := st.SetStandby("s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := deps.StandbyGone("s1", probeCmuxWS); err != nil {
		t.Fatal(err)
	}
	if sb, _ := st.GetStandby("s1"); sb != nil {
		t.Fatalf("row kept: %+v", sb)
	}
}

// Every recorded or cleared wait wakes the sidebar loop; a server event
// does not (it has its own relay).
func TestWaitEventsWakeTheSidebarLoop(t *testing.T) {
	s := &Server{bus: events.New()}
	woken := make(chan struct{}, 8)
	watch := s.watchWaitEvents(func() { woken <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { watch(ctx); close(done) }()

	s.bus.Publish(pigtail.Event{Type: pigtail.EventServerDied})
	s.bus.Publish(pigtail.Event{Type: pigtail.EventSessionWaitChanged})
	s.bus.Publish(pigtail.Event{Type: pigtail.EventWaitClosed})
	for i := 0; i < 2; i++ {
		select {
		case <-woken:
		case <-time.After(2 * time.Second):
			t.Fatalf("woken %d times, want 2", i)
		}
	}
	select {
	case <-woken:
		t.Fatal("woken by a server event")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay kept running after the context ended")
	}
}

// D1: the auto-resume drift warning knows whether pigtaild starts sessions
// (fix mode): cmux's resume off is expected there, a drift anywhere else.
func TestCmuxSyncWarningsPassFixMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "cmux"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "cmux", "settings.json"), []byte(`{"terminal":{"autoResumeAgentSessions":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for mode, wantWarn := range map[string]bool{cmuxwatch.ModeFix: false, cmuxwatch.ModePilot: true} {
		st, _ := store.Open(":memory:")
		s, err := New(Config{SocketPath: testSocketPath(t), CmuxCheckMode: mode}, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(s.cmuxSyncDeps().Warnings(), "|")
		st.Close()
		if has := strings.Contains(got, "tabs stay at the shell"); has != wantWarn {
			t.Errorf("mode %s: warnings %q, want tabs-stay warning = %v", mode, got, wantWarn)
		}
	}
}
