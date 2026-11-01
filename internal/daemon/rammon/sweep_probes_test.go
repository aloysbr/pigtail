package rammon

import (
	"context"
	"sync"
	"testing"
	"time"
)

// probeCounter records every port the monitor probes, so a sweep's shell-out
// cost can be asserted rather than assumed.
type probeCounter struct {
	mu    sync.Mutex
	ports []int
}

func (p *probeCounter) probe(port int) (bool, []int, error) {
	p.mu.Lock()
	p.ports = append(p.ports, port)
	p.mu.Unlock()
	switch port {
	case 3030:
		return true, []int{100}, nil
	case 3040:
		return true, []int{200}, nil
	}
	return false, nil, nil
}

func (p *probeCounter) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ports)
}

// TestSweepProbesEachPortOncePerPass pins down what a sweep costs.
//
// purgeIdle and capServers each used to call snapshot(), and the mains/battery
// paths a third time, so one decision cost three or four lsof calls per port.
// On a machine with 67 workspaces that was ~400 forks every sweep, forever.
// Two passes is the floor with the current design: observeActivity samples CPU
// for the tracker, snapshot attributes RSS. Anything above that is redundant.
func TestSweepProbesEachPortOncePerPass(t *testing.T) {
	pc := &probeCounter{}
	st := &fakeStore{workspaces: twoWorkspaces()} // ws1 :3030, ws2 :3040

	cfg := Defaults()
	cfg.Enabled = true
	cfg.FloorBytes = 1 << 30 // avail is far above: no mains eviction path
	// Both optional stages ON, but tuned to evict nothing, so the count
	// reflects probing alone and not a stage that bailed out early.
	cfg.IdlePurgeGrace = time.Hour // ws1 idles 30m in the fixture — under it
	cfg.MaxServers = 5             // two servers, well under the cap

	m := newTestMonitor(st, fakeMem{avail: 32 << 30}, cfg, &[][]int{},
		WithPortPIDs(pc.probe),
		WithCPUSampler(fixedCPU{secs: 10}),
	)
	m.Sweep(context.Background())

	const ports, passes = 2, 2
	if got, want := pc.count(), ports*passes; got != want {
		t.Fatalf("sweep made %d port probes, want %d (%d ports x %d passes); "+
			"a stage is snapshotting again", got, want, ports, passes)
	}
}

// TestSweepSkipsWorkspacesWhoseWorktreeIsGone: a removed worktree keeps its row
// and its recorded ports, and defaultServersFor still resolves servers for it
// by walking up to the container's config — so without the on-disk check every
// deleted worktree kept costing probes for the life of the daemon.
func TestSweepSkipsWorkspacesWhoseWorktreeIsGone(t *testing.T) {
	pc := &probeCounter{}
	st := &fakeStore{workspaces: twoWorkspaces()}

	cfg := Defaults()
	cfg.Enabled = true
	cfg.FloorBytes = 1 << 30

	m := newTestMonitor(st, fakeMem{avail: 32 << 30}, cfg, &[][]int{},
		WithPortPIDs(pc.probe),
		WithCPUSampler(fixedCPU{secs: 10}),
		// ws2's worktree was deleted out from under the daemon.
		WithPathExists(func(path string) bool { return path != "/tmp/ws2" }),
	)
	m.Sweep(context.Background())

	pc.mu.Lock()
	defer pc.mu.Unlock()
	for _, port := range pc.ports {
		if port == 3040 {
			t.Fatalf("probed :3040, but ws2's worktree is gone; probes = %v", pc.ports)
		}
	}
	if len(pc.ports) == 0 {
		t.Fatal("probed nothing at all — the live workspace was skipped too")
	}
}

// TestDefaultPathExistsIsConservative: only a definite ENOENT means gone. An
// unmounted volume or a permission error must read as present, because hiding a
// live server is worse than probing a dead one.
func TestDefaultPathExistsIsConservative(t *testing.T) {
	if defaultPathExists("/definitely/not/here/at/all") {
		t.Error("a missing path must read as gone")
	}
	if !defaultPathExists("") {
		t.Error("an empty path must read as present (nothing to check)")
	}
	if !defaultPathExists(t.TempDir()) {
		t.Error("an existing path must read as present")
	}
}
