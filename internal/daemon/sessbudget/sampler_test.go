package sessbudget

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const mib = 1 << 20

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// fixture is a Deps whose every seam is a fake: peers, memory sensor,
// process table, native status, liveness, workspace projects and the clock.
type fixture struct {
	mu       sync.Mutex
	peers    []pigtail.PeerSession
	mem      sysmem.Memory
	procs    map[int]sysmem.Proc
	native   map[int]claudesession.Native // by pid; absent = unreadable
	dead     map[int]bool
	projects map[string]string
	now      time.Time
}

func newFixture() *fixture {
	return &fixture{
		mem:      sysmem.Memory{Available: 8 << 30, CompressorBytes: 2 << 30},
		procs:    map[int]sysmem.Proc{},
		native:   map[int]claudesession.Native{},
		dead:     map[int]bool{},
		projects: map[string]string{},
		now:      t0,
	}
}

// addPeer registers a live session of the given native status whose process
// tree weighs mb MiB and burns cpu percent.
func (f *fixture) addPeer(sid string, pid int, status string, mb uint64, cpu float64) *pigtail.PeerSession {
	f.peers = append(f.peers, pigtail.PeerSession{
		SessionID: sid, ConfigDir: "/cfg", PID: pid, Cwd: "/nowhere/" + sid, Scope: "perso",
	})
	f.native[pid] = claudesession.Native{PID: pid, SessionID: sid, Status: status}
	f.procs[pid] = sysmem.Proc{PID: pid, PPID: 1, MemBytes: mb * mib / 2, CPUPercent: cpu / 2}
	f.procs[pid+1000] = sysmem.Proc{PID: pid + 1000, PPID: pid, MemBytes: mb * mib / 2, CPUPercent: cpu / 2}
	return &f.peers[len(f.peers)-1]
}

func (f *fixture) deps() Deps {
	return Deps{
		Peers: func() ([]pigtail.PeerSession, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]pigtail.PeerSession(nil), f.peers...), nil
		},
		Memory: func() (sysmem.Memory, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.mem, nil
		},
		Procs: func() (map[int]sysmem.Proc, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.procs, nil
		},
		ReadNative: func(cfg string, pid int) (claudesession.Native, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			n, ok := f.native[pid]
			return n, ok
		},
		SessionAlive: func(cfg string, pid int, sid string) bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return !f.dead[pid]
		},
		WorkspaceProject: func(ws string) string {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.projects[ws]
		},
		Now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.now
		},
		// A clock that never ticks: Wait returns only when ctx is done.
		Wait: func(ctx context.Context, d time.Duration) { <-ctx.Done() },
	}
}

func (f *fixture) sampler(t *testing.T) *Sampler {
	t.Helper()
	return New(f.deps(), NewHistory(filepath.Join(t.TempDir(), "session-samples.jsonl")), 30*time.Second)
}

func TestSamplerRecordsBusyAndFootprint(t *testing.T) {
	f := newFixture()
	f.addPeer("s-busy", 100, "busy", 300, 40)
	f.addPeer("s-idle", 200, "idle", 390, 2)
	f.peers[0].SubjectID = "sub_1"
	f.peers[0].WorkspaceID = "ws1"
	f.projects["ws1"] = "pigtail"
	s := f.sampler(t)

	smp, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if smp.Live != 2 || smp.Busy != 1 {
		t.Fatalf("live/busy = %d/%d, want 2/1", smp.Live, smp.Busy)
	}
	if smp.AvailableBytes != 8<<30 || smp.CompressorBytes != 2<<30 {
		t.Fatalf("levels: %+v", smp)
	}
	byID := map[string]pigtail.SessionSampleRow{}
	for _, r := range smp.Sessions {
		byID[r.SessionID] = r
	}
	b, i := byID["s-busy"], byID["s-idle"]
	if b.Status != "busy" || b.FootprintBytes != 300*mib || b.CPUPercent != 40 || b.PID != 100 {
		t.Fatalf("busy row: %+v", b)
	}
	if b.Scope != "perso" || b.Repo != "pigtail" || b.SubjectID != "sub_1" {
		t.Fatalf("busy row scope/repo/subject: %+v", b)
	}
	if i.Status != "idle" || i.FootprintBytes != 390*mib || i.CPUPercent != 2 {
		t.Fatalf("idle row: %+v", i)
	}
	recs, err := s.History().Read(time.Time{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("history: %v %v", recs, err)
	}
	if !recs[0].TS.Equal(t0) {
		t.Fatalf("ts %v", recs[0].TS)
	}
}

func TestSamplerCallsOnSampleAfterAppend(t *testing.T) {
	f := newFixture()
	f.addPeer("s1", 100, "busy", 100, 1)
	s := f.sampler(t)

	// nil OnSample: no panic.
	if _, err := s.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}

	var calls int
	var inHistory int
	s.OnSample = func(smp Sample) {
		calls++
		recs, _ := s.History().Read(time.Time{})
		inHistory = len(recs)
		if smp.Busy != 1 {
			t.Errorf("OnSample got busy %d", smp.Busy)
		}
	}
	if _, err := s.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("OnSample called %d times", calls)
	}
	if inHistory != 2 {
		t.Fatalf("history held %d records when OnSample ran, want 2 (the one just appended included)", inHistory)
	}
}

func TestSamplerFirstSampleImmediate(t *testing.T) {
	f := newFixture()
	f.addPeer("s1", 100, "busy", 100, 1)
	s := f.sampler(t)
	got := make(chan Sample, 1)
	s.OnSample = func(smp Sample) { got <- smp }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case smp := <-got:
		if smp.Busy != 1 {
			t.Fatalf("busy %d", smp.Busy)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no sample before any tick")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

func TestSamplerSkipsUnreadablePeerStatus(t *testing.T) {
	f := newFixture()
	f.addPeer("s1", 100, "busy", 100, 1)
	delete(f.native, 100) // unreadable
	s := f.sampler(t)
	smp, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if smp.Live != 1 || smp.Busy != 0 {
		t.Fatalf("unreadable status: live/busy = %d/%d, want 1/0", smp.Live, smp.Busy)
	}
}

func TestSamplerExcludesDeadPidWithStaleBusyFile(t *testing.T) {
	f := newFixture()
	f.addPeer("s-dead", 100, "busy", 100, 1)
	f.addPeer("s-live", 200, "busy", 100, 1)
	f.dead[100] = true // its native file still says busy
	s := f.sampler(t)
	smp, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if smp.Live != 1 || smp.Busy != 1 || len(smp.Sessions) != 1 || smp.Sessions[0].SessionID != "s-live" {
		t.Fatalf("dead pid counted: %+v", smp)
	}
}

func TestSamplerWaitingNotCountedBusy(t *testing.T) {
	f := newFixture()
	f.addPeer("s1", 100, "waiting", 100, 1)
	s := f.sampler(t)
	smp, _ := s.Sample(context.Background())
	if smp.Live != 1 || smp.Busy != 0 || smp.Sessions[0].Status != "waiting" {
		t.Fatalf("waiting: %+v", smp)
	}
}

func TestSamplerShellCountsIdle(t *testing.T) {
	f := newFixture()
	f.addPeer("s1", 100, "shell", 100, 1)
	s := f.sampler(t)
	smp, _ := s.Sample(context.Background())
	if smp.Live != 1 || smp.Busy != 0 || smp.Sessions[0].Status != "idle" {
		t.Fatalf("shell: %+v", smp)
	}
}

func TestSamplerReadsNativeStatusNotStoredColumn(t *testing.T) {
	f := newFixture()
	f.addPeer("s1", 100, "busy", 100, 1)
	f.peers[0].Status = "idle" // the stored column lags
	s := f.sampler(t)
	smp, _ := s.Sample(context.Background())
	if smp.Busy != 1 || smp.Sessions[0].Status != "busy" {
		t.Fatalf("stored column trusted: %+v", smp)
	}
}

func TestSamplerCompressorRateOverWindow(t *testing.T) {
	f := newFixture()
	f.addPeer("s1", 100, "busy", 100, 1)
	s := f.sampler(t)
	f.mem.Compressions, f.mem.Decompressions = 0, 0
	first, _ := s.Sample(context.Background())
	if first.CompressBytesPerSec != 0 {
		t.Fatalf("first sample has no rate, got %v", first.CompressBytesPerSec)
	}
	f.mu.Lock()
	f.now = t0.Add(30 * time.Second)
	f.mem.Compressions, f.mem.Decompressions = 300*mib, 300*mib
	f.mem.Swapouts = 60 * mib
	f.mu.Unlock()
	second, _ := s.Sample(context.Background())
	if second.CompressBytesPerSec != 20*mib {
		t.Fatalf("compress rate %v, want %v", second.CompressBytesPerSec, 20*mib)
	}
	if second.SwapoutBytesPerSec != 2*mib {
		t.Fatalf("swapout rate %v, want %v", second.SwapoutBytesPerSec, 2*mib)
	}
}

func TestRepoFromWorkspaceThenCwdWalk(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "myrepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFixture()
	f.addPeer("s-ws", 100, "busy", 10, 1)
	f.addPeer("s-cwd", 200, "busy", 10, 1)
	f.addPeer("s-none", 300, "busy", 10, 1)
	f.peers[0].WorkspaceID, f.peers[0].Cwd = "ws1", deep
	f.projects["ws1"] = "fromworkspace"
	f.peers[1].Cwd = deep
	f.peers[2].Cwd = root // no .git above (t.TempDir is outside any repo)
	s := f.sampler(t)
	smp, _ := s.Sample(context.Background())
	got := map[string]string{}
	for _, r := range smp.Sessions {
		got[r.SessionID] = r.Repo
	}
	if got["s-ws"] != "fromworkspace" {
		t.Errorf("workspace project first: %q", got["s-ws"])
	}
	if got["s-cwd"] != "myrepo" {
		t.Errorf("cwd walk: %q", got["s-cwd"])
	}
	if got["s-none"] != "" {
		t.Errorf("no repo: %q", got["s-none"])
	}
}

func TestRepoFallbackLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "mainrepo")
	gitdir := filepath.Join(main, ".git", "worktrees", "wt1")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "branch-folder")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFixture()
	f.addPeer("s1", 100, "busy", 10, 1)
	f.peers[0].Cwd = wt
	s := f.sampler(t)
	smp, _ := s.Sample(context.Background())
	if smp.Sessions[0].Repo != "mainrepo" {
		t.Fatalf("linked worktree reports %q, want mainrepo", smp.Sessions[0].Repo)
	}
}
