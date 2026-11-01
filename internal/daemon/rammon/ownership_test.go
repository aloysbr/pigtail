package rammon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The port rammon probes is the one .env.local names, through the default
// serversFor. Red on the base, whose defaultServersFor reads ws.Ports (3030)
// and so sees nothing listening.
func TestSnapshotProbesTheEnvLocalPort(t *testing.T) {
	dir := t.TempDir() // no pigtail.yaml: the default variable names apply (dev → PORT)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("PORT=3031\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws1", Path: dir, Ports: map[string]int{"dev": 3030}}}}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, Defaults(), &killed,
		WithPortPIDs(func(port int) (bool, []int, error) {
			if port == 3031 {
				return true, []int{100}, nil
			}
			return false, nil, nil
		}))
	stats, _, _ := m.Inspect()
	if len(stats) != 1 || stats[0].Port != 3031 || stats[0].WorkspaceID != "ws1" {
		t.Fatalf("stats %+v, want exactly one ServerStat on ws1:3031", stats)
	}
}

// Replay of 2026-10-05: ws_392f9b's store port is 3013, but the listener on
// 3013 runs in ws_efb2e9. It must not be listed (nor evicted) under ws_392f9b.
func TestSnapshotSkipsForeignListenerAndRecordsConflict(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "efb2e9"), filepath.Join(root, "392f9b")
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws_392f9b", Path: b, Ports: map[string]int{"dev": 3030}},
		{ID: "ws_efb2e9", Path: a, Ports: map[string]int{"dev": 3040}},
	}}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, Defaults(), &killed,
		WithCwds(func([]int) (map[int]string, error) {
			return map[int]string{100: a, 200: filepath.Join(a, "apps", "web")}, nil
		}))
	stats, _, _ := m.Inspect()
	for _, s := range stats {
		if s.WorkspaceID == "ws_392f9b" {
			t.Fatalf("foreign listener listed under ws_392f9b: %+v", s)
		}
	}
	c := m.Conflicts()
	if len(c) != 1 || c[0].WorkspaceID != "ws_392f9b" || c[0].Port != 3030 || c[0].HolderWorkspaceID != "ws_efb2e9" {
		t.Fatalf("conflicts %+v", c)
	}
}

func TestSnapshotUnknownCwdIsForeign(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws1", Path: t.TempDir(), Ports: map[string]int{"dev": 3030}}}}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, Defaults(), &killed,
		WithCwds(func([]int) (map[int]string, error) { return map[int]string{}, nil }))
	if stats, _, _ := m.Inspect(); len(stats) != 0 {
		t.Fatalf("a listener of unknown cwd was attributed: %+v", stats)
	}
	if c := m.Conflicts(); len(c) != 1 || c[0].HolderPath != "" {
		t.Fatalf("conflicts %+v, want one with an unknown holder", c)
	}
}

// Every killing path reads snapshot(): none may reach a foreign pid.
func TestStopAllNeverKillsForeignListener(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws1", Path: t.TempDir(), Ports: map[string]int{"dev": 3030}}}}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, Defaults(), &killed,
		WithCwds(func([]int) (map[int]string, error) { return map[int]string{100: "/elsewhere"}, nil }))
	m.StopAll()
	m.StopIdle(0)
	if len(killed) != 0 {
		t.Fatalf("killed %v", killed)
	}
}

func TestSnapshotRecordsEnvDivergence(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws_efb2e9", Path: "/tmp/efb2e9", Ports: map[string]int{"dev": 3000}}}}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, Defaults(), &killed,
		WithServersFunc(func(pigtail.Workspace) []ServerProc {
			return []ServerProc{{Name: "dev", Kind: "dev", Port: 3030, DBPort: 3000}}
		}))
	m.Inspect()
	c := m.Conflicts()
	if len(c) != 1 || c[0].Reason != "env_diverges" || c[0].Port != 3030 || c[0].RegisteredPort != 3000 {
		t.Fatalf("conflicts %+v", c)
	}
}

// Every conflict names its workspace's path: the fix planner needs it for
// the resync predicate (self) and for "cd <path> && pigtail stop".
func TestSnapshotRecordsConflictPath(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "efb2e9"), filepath.Join(root, "392f9b")
	st := &fakeStore{workspaces: []pigtail.Workspace{
		{ID: "ws_392f9b", Path: b, Ports: map[string]int{"dev": 3030}},
		{ID: "ws_efb2e9", Path: a, Ports: map[string]int{"dev": 3040}},
	}}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, Defaults(), &killed,
		WithCwds(func([]int) (map[int]string, error) { return map[int]string{100: a, 200: a}, nil }),
		WithServersFunc(func(ws pigtail.Workspace) []ServerProc {
			if ws.ID == "ws_efb2e9" {
				return []ServerProc{{Name: "dev", Kind: "dev", Port: 3030, DBPort: 3040}}
			}
			return []ServerProc{{Name: "dev", Kind: "dev", Port: 3030, DBPort: 3030}}
		}))
	m.Inspect()
	got := map[string]string{}
	for _, c := range m.Conflicts() {
		got[c.WorkspaceID+"/"+c.Reason] = c.Path
	}
	if got["ws_efb2e9/env_diverges"] != a || got["ws_392f9b/foreign_listener"] != b {
		t.Fatalf("conflict paths %v", got)
	}
}
