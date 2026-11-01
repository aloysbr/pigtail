package daemon

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/thermal"
)

// newThermalLidServer builds a server with fake pmset, thermal and lid
// readers: the thermal state is critical, the lid closed or open.
func newThermalLidServer(t *testing.T, bus *events.Bus, lidClosed bool) *Server {
	t.Helper()
	s, err := New(Config{SocketPath: testSocketPath(t), LidRunner: &fakeLidRunner{},
		LidThermal: func() (thermal.State, error) { return thermal.Critical, nil },
		LidClosed:  func() (bool, error) { return lidClosed, nil },
	}, openTestStore(t), bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLidThermalWired(t *testing.T) {
	bus := events.New()
	s := newThermalLidServer(t, bus, true)
	sub := bus.Subscribe("lid.*")
	defer sub.Close()
	if _, err := s.lid.On(8); err != nil {
		t.Fatal(err)
	}
	wantLidChange(t, nextLidChange(t, sub), true, "on")
	s.lid.Tick()
	wantLidChange(t, nextLidChange(t, sub), false, "thermal critical")
	st, err := s.lid.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.LastOff == nil || st.LastOff.Reason != "thermal critical" {
		t.Fatalf("last off = %+v", st.LastOff)
	}
}

func TestLidThermalWiredLidOpen(t *testing.T) {
	s := newThermalLidServer(t, events.New(), false)
	if _, err := s.lid.On(8); err != nil {
		t.Fatal(err)
	}
	s.lid.Tick()
	st, err := s.lid.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.LastOff != nil || st.LidClosed == nil || *st.LidClosed || st.ThermalState != "critical" {
		t.Fatalf("status = %+v (lid %v): want on, lid open read, no off", st, st.LidClosed)
	}
}
