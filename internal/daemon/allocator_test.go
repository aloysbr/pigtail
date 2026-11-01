package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The allocator the daemon really builds skips a port named in a live
// .env.local (AC2) and one the listener inventory reports held (AC1's lsof half).
func TestDaemonAllocatorSkipsEnvLocalAndBusyPorts(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	root := t.TempDir()
	live := filepath.Join(root, "a")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, ".env.local"), []byte("PORT=39700\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := &pigtail.Project{ID: "prj_w", Name: "w", RootPath: root, DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {39700, 39702}}}
	if err := st.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(&pigtail.Workspace{ID: "ws_a", ProjectID: "prj_w", Path: live,
		Ports: map[string]int{"dev": 39650}, State: pigtail.StateBuilding, BackendKind: "git"}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{SocketPath: testSocketPath(t),
		ListListeners: func() (map[int][]int, error) { return map[int][]int{39701: {1}}, nil }},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := srv.allocator.Allocate(proj.PortRanges, "dev")
	if err != nil || p != 39702 {
		t.Fatalf("got %d (%v), want 39702: 39700 is named in a live .env.local, 39701 is held", p, err)
	}
}
