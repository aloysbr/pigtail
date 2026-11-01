package daemon

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
)

// -idle-net-signal reaches the RAM monitor; left unset, the network signal is
// off (rule D8).
func TestIdleNetSignalConfigMapping(t *testing.T) {
	s, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: "", IdleNetSignal: true}, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ramMonitor.NetSignal() {
		t.Error("IdleNetSignal=true did not turn the network signal on")
	}
	s2, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: ""}, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s2.ramMonitor.NetSignal() {
		t.Error("the network signal is on by default, want off")
	}
}
