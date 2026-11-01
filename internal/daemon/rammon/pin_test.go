package rammon

import (
	"testing"
	"time"
)

// TestPinUnpinStop exercises the public Pin/Unpin/StopServer methods on the
// RAMMonitor using the existing newTestMonitor fixture.
// In twoWorkspaces(), ws1:dev → port 3030 → pid 100 (per newTestMonitor's portPIDs).
func TestPinUnpinStop(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, Defaults(), &killed)

	// Pin of a non-existent server must return false.
	if m.Pin("ws-nope", "dev", time.Hour) {
		t.Fatalf("pin of unknown server should return false")
	}

	// Pin of a running server must succeed.
	if !m.Pin("ws1", "dev", time.Hour) {
		t.Fatalf("pin of running ws1:dev should succeed")
	}
	if !m.isSnoozed(ServerStat{WorkspaceID: "ws1", Server: "dev"}, time.Now()) {
		t.Fatalf("ws1:dev should be snoozed after Pin")
	}

	// Unpin must clear the pin.
	m.Unpin("ws1", "dev")
	if m.isSnoozed(ServerStat{WorkspaceID: "ws1", Server: "dev"}, time.Now()) {
		t.Fatalf("ws1:dev should not be snoozed after Unpin")
	}

	// StopServer must kill pid 100 (ws1:dev → port 3030 → pid 100).
	if !m.StopServer("ws1", "dev") {
		t.Fatalf("StopServer ws1:dev should return true")
	}
	if len(killed) != 1 {
		t.Fatalf("expected 1 kill call, got %d", len(killed))
	}
	if len(killed[0]) != 1 || killed[0][0] != 100 {
		t.Fatalf("StopServer ws1:dev should kill pid 100, killed=%v", killed)
	}

	// StopServer for a non-existent server must return false.
	if m.StopServer("ws-nope", "dev") {
		t.Fatalf("StopServer of unknown server should return false")
	}
}

// TestStopAll exercises StopAll: both ws1:dev (pid 100) and ws2:dev (pid 200)
// must be killed and their ids returned.
func TestStopAll(t *testing.T) {
	st := &fakeStore{workspaces: twoWorkspaces()}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 40}, Defaults(), &killed)

	stopped := m.StopAll()
	if len(killed) != 2 {
		t.Fatalf("StopAll: expected 2 kills, got %d", len(killed))
	}
	if len(stopped) != 2 {
		t.Fatalf("StopAll: expected 2 entries in result, got %d", len(stopped))
	}
	// Build a set to check membership regardless of order.
	ids := map[[2]string]bool{}
	for _, p := range stopped {
		ids[p] = true
	}
	if !ids[[2]string{"ws1", "dev"}] {
		t.Errorf("StopAll missing ws1:dev in result: %v", stopped)
	}
	if !ids[[2]string{"ws2", "dev"}] {
		t.Errorf("StopAll missing ws2:dev in result: %v", stopped)
	}
}
