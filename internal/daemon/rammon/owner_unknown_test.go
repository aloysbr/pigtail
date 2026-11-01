package rammon

import (
	"errors"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// One failed cwd probe (lsof under load) is retried: the worktree's own
// server is still attributed.
func TestSnapshotRetriesCwdProbeOnce(t *testing.T) {
	dir := t.TempDir()
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws1", Path: dir, Ports: map[string]int{"dev": 3030}}}}
	var killed [][]int
	calls := 0
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, Defaults(), &killed,
		WithCwds(func(pids []int) (map[int]string, error) {
			calls++
			if calls%2 == 1 {
				return nil, errors.New("lsof cwd: EMFILE")
			}
			got := map[int]string{}
			for _, p := range pids {
				got[p] = dir
			}
			return got, nil
		}))
	if stats, _, _ := m.Inspect(); len(stats) != 1 || stats[0].WorkspaceID != "ws1" {
		t.Fatalf("stats %+v, want ws1's own server after the retry", stats)
	}
}

// Two failed probes: the owner is unknown. No conflict names a holder, and
// nothing is killed.
func TestSnapshotOwnerUnknownRecordsNoHolder(t *testing.T) {
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws1", Path: t.TempDir(), Ports: map[string]int{"dev": 3030}}}}
	var killed [][]int
	m := newTestMonitor(st, fakeMem{avail: 1 << 30}, Defaults(), &killed,
		WithCwds(func([]int) (map[int]string, error) { return nil, errors.New("lsof cwd: EMFILE") }))
	m.Inspect()
	for _, c := range m.Conflicts() {
		if c.Reason == ConflictForeignListener {
			t.Fatalf("an unknown owner was recorded as a foreign listener: %+v", c)
		}
	}
	m.StopAll()
	if len(killed) != 0 {
		t.Fatalf("killed %v on an unknown owner", killed)
	}
}
