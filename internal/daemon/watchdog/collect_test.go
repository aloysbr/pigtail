package watchdog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestCollectTargetsSkipsForeignListener(t *testing.T) {
	own := t.TempDir()
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws1", Path: own, Ports: map[string]int{"dev": 3030}}}}
	w := New(events.New(), st,
		WithConfigResolver(func(pigtail.Workspace) *pigtail.WatchdogConfig { return pigtail.WatchdogDefaults() }),
		WithPortPIDs(func(int) (bool, []int, error) { return true, []int{100, 101}, nil }),
		WithCwds(func([]int) (map[int]string, error) { return map[int]string{100: "/elsewhere", 101: own}, nil }),
	)
	got := w.collectTargets()
	if len(got) != 1 || len(got[0].PIDs) != 1 || got[0].PIDs[0] != 101 {
		t.Fatalf("targets %+v, want only the owned pid 101", got)
	}
	w2 := New(events.New(), st,
		WithConfigResolver(func(pigtail.Workspace) *pigtail.WatchdogConfig { return pigtail.WatchdogDefaults() }),
		WithPortPIDs(func(int) (bool, []int, error) { return true, []int{100}, nil }),
		WithCwds(func([]int) (map[int]string, error) { return map[int]string{100: "/elsewhere"}, nil }),
	)
	if got := w2.collectTargets(); len(got) != 0 {
		t.Fatalf("a foreign-only port became a target: %+v", got)
	}
}

// The store says 3030, .env.local says 3031 and the server runs on 3031:
// the watchdog must watch 3031 (red on the base, which probes ws.Ports only).
func TestCollectTargetsUsesEnvLocalPort(t *testing.T) {
	own := t.TempDir()
	if err := os.WriteFile(filepath.Join(own, ".env.local"), []byte("PORT=3031\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws1", Path: own, Ports: map[string]int{"dev": 3030}}}}
	w := New(events.New(), st,
		WithConfigResolver(func(pigtail.Workspace) *pigtail.WatchdogConfig { return pigtail.WatchdogDefaults() }),
		WithPortPIDs(func(p int) (bool, []int, error) { return p == 3031, []int{7}, nil }),
	)
	got := w.collectTargets()
	if len(got) != 1 || got[0].Port != 3031 {
		t.Fatalf("targets %+v, want the .env.local port 3031", got)
	}
}
