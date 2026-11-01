package admission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/sysmem"
)

// procWorld is the fake machine a restored lease's client lives in.
type procWorld struct {
	mu    sync.Mutex
	alive map[int]bool
	comm  map[int]string
}

func newWorld(pids ...int) *procWorld {
	w := &procWorld{alive: map[int]bool{}, comm: map[int]string{}}
	for _, p := range pids {
		w.alive[p] = true
		w.comm[p] = "/Users/x/.local/bin/pigtail"
	}
	return w
}
func (w *procWorld) isAlive(p int) bool  { w.mu.Lock(); defer w.mu.Unlock(); return w.alive[p] }
func (w *procWorld) commOf(p int) string { w.mu.Lock(); defer w.mu.Unlock(); return w.comm[p] }
func (w *procWorld) kill(p int)          { w.mu.Lock(); w.alive[p] = false; w.mu.Unlock() }

func persistCfg(goTest int) Config {
	cfg := testConfig()
	cfg.ClassLimits = map[string]int{"go-test": goTest}
	return cfg
}

// bootDaemon builds a controller on the leases file at path, as New does at a
// daemon start.
func bootDaemon(t *testing.T, path string, cfg Config, w *procWorld, clk *fakeClock, s *fakeSampler) *Controller {
	t.Helper()
	return New(events.New(),
		WithConfig(cfg), WithSampler(s), WithClock(clk.now),
		WithLogger(func(string, ...any) {}),
		WithLeasesPath(path), WithProcessProbe(w.isAlive, w.commOf))
}

func persistEnv(t *testing.T) (string, *fakeClock, *fakeSampler) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	s := &fakeSampler{mem: sysmem.Memory{Available: 60 * gib, Pressure: 1, Total: 64 * gib}}
	return filepath.Join(t.TempDir(), "admission-leases.json"), clk, s
}

func enqueueGoTest(t *testing.T, c *Controller, group string, pid int) *Ticket {
	t.Helper()
	tk, err := c.Enqueue(Request{Group: group, Class: "go-test", Repo: "repo", PID: pid, Label: "pre-push"})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestRestoredLeaseCountsAgainstClassLimit(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242, 5, 6)
	c1 := bootDaemon(t, path, persistCfg(2), w, clk, s)
	if !ready(enqueueGoTest(t, c1, "a", 4242)) {
		t.Fatal("setup: first go-test must start")
	}
	c2 := bootDaemon(t, path, persistCfg(2), w, clk, s)
	if n := len(c2.Status().Leases); n != 1 {
		t.Fatalf("restored leases = %d, want 1", n)
	}
	if !ready(enqueueGoTest(t, c2, "b", 5)) {
		t.Fatal("a second go-test fits under the cap of 2")
	}
	third := enqueueGoTest(t, c2, "c", 6)
	if ready(third) || c2.Position(third).Reason != ReasonClassLimit {
		t.Fatalf("third go-test must wait on class_limit, got %+v", c2.Position(third))
	}
}

func TestRestoredLeaseWithDeadPIDIsDropped(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242)
	c1 := bootDaemon(t, path, persistCfg(2), w, clk, s)
	enqueueGoTest(t, c1, "a", 4242)
	w.kill(4242)
	c2 := bootDaemon(t, path, persistCfg(2), w, clk, s)
	if n := len(c2.Status().Leases); n != 0 {
		t.Fatalf("a lease whose client is gone must be dropped, got %d", n)
	}
}

func TestRestoredLeaseWrongCommIsDropped(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(10, 11)
	c1 := bootDaemon(t, path, persistCfg(3), w, clk, s)
	enqueueGoTest(t, c1, "a", 10)
	enqueueGoTest(t, c1, "b", 11)
	w.comm[10] = "/bin/zsh" // the pid was reused by something else
	w.comm[11] = "/x/y/pigtail"
	c2 := bootDaemon(t, path, persistCfg(3), w, clk, s)
	ls := c2.Status().Leases
	if len(ls) != 1 || ls[0].Group != "b" {
		t.Fatalf("only the pigtail client stays, got %+v", ls)
	}
}

func TestRestoredLeaseReleasedWhenPIDGone(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242, 5)
	c1 := bootDaemon(t, path, persistCfg(1), w, clk, s)
	enqueueGoTest(t, c1, "a", 4242)
	c2 := bootDaemon(t, path, persistCfg(1), w, clk, s)
	next := enqueueGoTest(t, c2, "b", 5)
	if ready(next) {
		t.Fatal("setup: the restored lease must hold the cap")
	}
	w.kill(4242)
	c2.Tick()
	if !ready(next) {
		t.Fatal("the restored lease must be released at the Tick after its client died")
	}
}

func TestRestoredLeaseDoesNotFeedCosts(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242)
	c1 := bootDaemon(t, path, persistCfg(1), w, clk, s)
	enqueueGoTest(t, c1, "a", 4242)
	s.mu.Lock()
	s.procs = map[int]sysmem.Proc{4242: {PID: 4242, PPID: 1, MemBytes: 2 * gib}}
	s.mu.Unlock()
	c2 := bootDaemon(t, path, persistCfg(1), w, clk, s)
	c2.Tick() // samples a peak for the restored lease
	w.kill(4242)
	c2.Tick()
	if n := len(c2.Status().Costs); n != 0 {
		t.Fatalf("a restored lease taught the costs: %+v", c2.Status().Costs)
	}
}

func TestRestoredLeaseIDsNeverReused(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242, 5)
	writeLeases(t, path, leasesFile{Version: leasesFormatVersion, Seq: 3, Leases: []leaseRec{
		{LeaseID: "adm-7", Class: "go-test", Group: "a", PID: 4242, CostBytes: gib, GrantedAt: clk.now()}}})
	c := bootDaemon(t, path, persistCfg(2), w, clk, s)
	tk := enqueueGoTest(t, c, "b", 5)
	if id := c.Grant(tk).ID; id != "adm-8" {
		t.Fatalf("next id = %q, want adm-8 (above the restored adm-7)", id)
	}
}

func TestEnqueueSamePIDDropsRestoredLease(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242)
	c1 := bootDaemon(t, path, persistCfg(1), w, clk, s)
	enqueueGoTest(t, c1, "a", 4242)
	c2 := bootDaemon(t, path, persistCfg(1), w, clk, s)
	tk := enqueueGoTest(t, c2, "a", 4242) // the client reconnected
	if !ready(tk) {
		t.Fatal("the reconnecting client must not queue behind its own restored lease")
	}
	if n := len(c2.Status().Leases); n != 1 {
		t.Fatalf("leases = %d, want 1 (never two places for one run)", n)
	}
}

func TestRevokedLeaseNotRestored(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242)
	cfg := persistCfg(2)
	cfg.MaxHold = time.Minute
	c1 := bootDaemon(t, path, cfg, w, clk, s)
	enqueueGoTest(t, c1, "a", 4242)
	clk.advance(2 * time.Minute)
	c1.Sweep()
	c2 := bootDaemon(t, path, cfg, w, clk, s)
	if n := len(c2.Status().Leases); n != 0 {
		t.Fatalf("a revoked lease came back: %d", n)
	}
}

func TestReleasedLeaseNotRestored(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242)
	c1 := bootDaemon(t, path, persistCfg(2), w, clk, s)
	tk := enqueueGoTest(t, c1, "a", 4242)
	c1.Release(c1.Grant(tk))
	c2 := bootDaemon(t, path, persistCfg(2), w, clk, s)
	if n := len(c2.Status().Leases); n != 0 {
		t.Fatalf("a released lease came back: %d", n)
	}
}

func TestSeqResumesFromFileWithoutLeases(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(5)
	writeLeases(t, path, leasesFile{Version: leasesFormatVersion, Seq: 9})
	c := bootDaemon(t, path, persistCfg(2), w, clk, s)
	if id := c.Grant(enqueueGoTest(t, c, "a", 5)).ID; id != "adm-10" {
		t.Fatalf("id = %q, want adm-10", id)
	}
}

func TestLeasesFileNewerVersionIgnored(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242, 5)
	writeLeases(t, path, leasesFile{Version: leasesFormatVersion + 1, Seq: 50, Leases: []leaseRec{
		{LeaseID: "adm-50", Class: "go-test", Group: "a", PID: 4242, GrantedAt: clk.now()}}})
	var logged []string
	c := New(events.New(), WithConfig(persistCfg(2)), WithSampler(s), WithClock(clk.now),
		WithLogger(func(f string, _ ...any) { logged = append(logged, f) }),
		WithLeasesPath(path), WithProcessProbe(w.isAlive, w.commOf))
	if n := len(c.Status().Leases); n != 0 {
		t.Fatalf("a newer-format file must be ignored, got %d leases", n)
	}
	if len(logged) == 0 {
		t.Fatal("the ignored file must be logged")
	}
	if id := c.Grant(enqueueGoTest(t, c, "b", 5)).ID; id != "adm-1" {
		t.Fatalf("id = %q, want adm-1 (the ignored file's seq is not read)", id)
	}
}

// TestGrantIsPersistedBeforeItIsObserved proves the order without timing: the
// write seam runs synchronously inside persistLocked (under mu), so when it
// writes the file that holds the waiter's lease, the waiter's channel must
// still be open. Moving persistLocked after close(t.ch) makes it ready there.
func TestGrantIsPersistedBeforeItIsObserved(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242, 5)
	var waiter *Ticket
	var sawGrantWrite, readyAtGrantWrite bool
	c := New(events.New(), WithConfig(persistCfg(1)), WithSampler(s), WithClock(clk.now),
		WithLogger(func(string, ...any) {}),
		WithLeasesPath(path), WithProcessProbe(w.isAlive, w.commOf),
		func(c *Controller) {
			c.writeLeases = func(p string, f leasesFile) error {
				for _, r := range f.Leases {
					if r.Group == "b" && waiter != nil {
						sawGrantWrite = true
						readyAtGrantWrite = ready(waiter)
					}
				}
				return writeLeasesFile(p, f)
			}
		})
	first := enqueueGoTest(t, c, "a", 4242)
	waiter = enqueueGoTest(t, c, "b", 5)
	if ready(waiter) {
		t.Fatal("setup: the second go-test must wait (cap 1)")
	}
	c.Release(c.Grant(first)) // grants the waiter inside the release
	if !ready(waiter) {
		t.Fatal("setup: the waiter must be granted by the release")
	}
	if !sawGrantWrite {
		t.Fatal("the waiter's grant was never written to the file")
	}
	if readyAtGrantWrite {
		t.Fatal("the ticket was already closed when its lease was written: file must come first")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), c.Grant(waiter).ID) {
		t.Fatalf("the file lacks the granted lease: %v %s", err, data)
	}
}

func TestStaleLeaseTempFilesRemovedAtLoad(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld()
	stale := []string{path + ".tmp-111", path + ".tmp-222"}
	other := filepath.Join(filepath.Dir(path), "other.tmp-1")
	for _, p := range append(stale, other) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bootDaemon(t, path, persistCfg(2), w, clk, s)
	for _, p := range stale {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed at load (err %v)", p, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("an unrelated file must stay: %v", err)
	}
}

func TestRestoredLeasePastMaxHoldIsDroppedAtLoad(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242, 5)
	cfg := persistCfg(2)
	cfg.MaxHold = time.Minute
	writeLeases(t, path, leasesFile{Version: leasesFormatVersion, Seq: 2, Leases: []leaseRec{
		{LeaseID: "adm-1", Class: "go-test", Group: "old", PID: 4242, GrantedAt: clk.now().Add(-2 * time.Minute)},
		{LeaseID: "adm-2", Class: "go-test", Group: "fresh", PID: 5, GrantedAt: clk.now().Add(-10 * time.Second)}}})
	c := bootDaemon(t, path, cfg, w, clk, s)
	ls := c.Status().Leases
	if len(ls) != 1 || ls[0].Group != "fresh" {
		t.Fatalf("only the lease younger than MaxHold stays, got %+v", ls)
	}
}

func TestCorruptLeasesFileIsLoggedAndDoesNotBlockNew(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(5)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged []string
	c := New(events.New(), WithConfig(persistCfg(2)), WithSampler(s), WithClock(clk.now),
		WithLogger(func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }),
		WithLeasesPath(path), WithProcessProbe(w.isAlive, w.commOf))
	if len(logged) != 1 || !strings.Contains(logged[0], "ignoring the persisted leases") {
		t.Fatalf("the corrupt file must be logged once, got %q", logged)
	}
	if n := len(c.Status().Leases); n != 0 {
		t.Fatalf("leases = %d, want 0", n)
	}
	if id := c.Grant(enqueueGoTest(t, c, "a", 5)).ID; id != "adm-1" {
		t.Fatalf("id = %q, want adm-1 (seq starts clean)", id)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"adm-1"`) {
		t.Fatalf("the next write must replace the corrupt file: %v %s", err, data)
	}
}

func TestPidAliveCountsEPERMAsAlive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can signal pid 1: EPERM cannot be provoked")
	}
	if !pidAlive(1) {
		t.Fatal("pid 1 belongs to root: kill -0 answers EPERM, which is alive")
	}
}

func TestRestoredLeaseReleasedWhenPIDReused(t *testing.T) {
	path, clk, s := persistEnv(t)
	w := newWorld(4242, 5)
	c1 := bootDaemon(t, path, persistCfg(1), w, clk, s)
	enqueueGoTest(t, c1, "a", 4242)
	c2 := bootDaemon(t, path, persistCfg(1), w, clk, s)
	next := enqueueGoTest(t, c2, "b", 5)
	if ready(next) {
		t.Fatal("setup: the restored lease must hold the cap")
	}
	w.mu.Lock()
	w.comm[4242] = "/bin/zsh" // alive, but the pid now belongs to something else
	w.mu.Unlock()
	c2.Tick()
	if !ready(next) {
		t.Fatal("a restored lease whose pid was reused must be released at the Tick")
	}
	if n := len(c2.Status().Leases); n != 1 {
		t.Fatalf("leases = %d, want 1 (the waiter's)", n)
	}
}

func TestCommOfSelfIsBaseName(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(pidComm(os.Getpid())), filepath.Base(exe); got != want {
		t.Fatalf("base of ps comm = %q, want %q", got, want)
	}
	if !pidAlive(os.Getpid()) || pidAlive(0) {
		t.Fatal("pidAlive: self must live, 0 must not")
	}
}

func writeLeases(t *testing.T, path string, f leasesFile) {
	t.Helper()
	if err := writeLeasesFile(path, f); err != nil {
		t.Fatal(err)
	}
}
