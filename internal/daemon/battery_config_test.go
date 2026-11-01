package daemon

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// openTestStore opens an in-temp store for config wiring tests.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/p.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestBatteryConfigMapping(t *testing.T) {
	st := openTestStore(t)
	cfg := Config{
		SocketPath:          testSocketPath(t),
		HTTPAddr:            "",
		BatteryDisabled:     true,
		BatteryGraceSeconds: 300,
	}
	s, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := s.ramMonitor.BatteryStatus()
	if v.Enabled {
		t.Errorf("BatteryDisabled=true should disable battery mode")
	}
	if v.GraceSeconds != 300 {
		t.Errorf("GraceSeconds=%d, want 300", v.GraceSeconds)
	}
	// Defaults when zero: a second config leaves grace at the 600s default.
	cfg2 := Config{SocketPath: testSocketPath(t), HTTPAddr: ""}
	s2, err := New(cfg2, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v2 := s2.ramMonitor.BatteryStatus(); v2.GraceSeconds != 600 || !v2.Enabled {
		t.Errorf("defaults: GraceSeconds=%d Enabled=%v, want 600,true", v2.GraceSeconds, v2.Enabled)
	}
	_ = time.Second
}
