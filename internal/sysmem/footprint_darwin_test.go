//go:build darwin

package sysmem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestRusageInfoV4Layout pins the mirror of struct rusage_info_v4
// (<sys/resource.h>): 16-byte uuid + 35 uint64, ri_phys_footprint the 8th.
// A wrong layout reads a neighbour field and nothing else would say so.
func TestRusageInfoV4Layout(t *testing.T) {
	var ri rusageInfoV4
	if got := unsafe.Sizeof(ri); got != 296 {
		t.Fatalf("sizeof(rusage_info_v4) = %d, want 296", got)
	}
	if got := unsafe.Offsetof(ri.PhysFootprint); got != 72 {
		t.Fatalf("offsetof(ri_phys_footprint) = %d, want 72", got)
	}
}

// touchAnon maps n bytes of anonymous private memory OUTSIDE the Go heap and
// writes one byte per page, so every page is private, resident and charged to
// the footprint; the mapping is released at test cleanup. Outside the heap on
// purpose: a make([]byte) is subject to the GC and the scavenger, which grow
// and return memory between two reads and made a heap-based version fail 2-4
// times out of 5 under -count=5 (growth 124 MiB for 256 touched).
func touchAnon(t testing.TB, n int) []byte {
	t.Helper()
	b, err := unix.Mmap(-1, 0, n, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		t.Fatalf("mmap %d bytes: %v", n, err)
	}
	t.Cleanup(func() { _ = unix.Munmap(b) })
	for i := 0; i < n; i += os.Getpagesize() {
		b[i] = 1
	}
	return b
}

// TestPhysFootprintTracksTouchedAllocation is the integration proof: the
// footprint of this very process grows by what it touched, and Procs()
// reports that footprint, not the ps RSS. (Footprint >= RSS does NOT hold in
// general: RSS counts shared library pages the footprint does not.)
func TestPhysFootprintTracksTouchedAllocation(t *testing.T) {
	const n = 256 << 20
	before, err := physFootprint(os.Getpid())
	if err != nil {
		t.Fatalf("physFootprint(self): %v", err)
	}
	touchAnon(t, n)
	after, err := physFootprint(os.Getpid())
	if err != nil {
		t.Fatalf("physFootprint(self): %v", err)
	}
	if after < before || after-before < n*8/10 {
		t.Fatalf("footprint grew by %d MiB after touching %d MiB (before=%d after=%d)",
			(after-before)>>20, n>>20, before, after)
	}
	procs, err := Procs()
	if err != nil {
		t.Fatalf("Procs(): %v", err)
	}
	self := procs[os.Getpid()].MemBytes
	if self < n*8/10 {
		t.Fatalf("Procs() self MemBytes = %d MiB, want >= %d MiB: not the footprint", self>>20, (n*8/10)>>20)
	}
}

// parseTopMem reads top's MEM cell ("266M", "1024K", "2G", "512B", with an
// optional trailing +/-) into bytes.
func parseTopMem(s string) (uint64, error) {
	s = strings.TrimRight(s, "+-")
	if s == "" {
		return 0, fmt.Errorf("empty MEM cell")
	}
	mult := map[byte]float64{'B': 1, 'K': 1 << 10, 'M': 1 << 20, 'G': 1 << 30}
	m, ok := mult[s[len(s)-1]]
	if !ok {
		return 0, fmt.Errorf("unknown unit in %q", s)
	}
	v, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil {
		return 0, err
	}
	return uint64(v * m), nil
}

// TestProcsAppliesFootprintToAChild proves Procs() wires applyFootprints in.
// It uses a process whose two figures differ: an idle `sleep` (measured
// 2026-10-01: ps RSS 1264K, top MEM 976K — RSS counts shared library pages).
// The touched-allocation test above cannot tell them apart: after 256 MiB
// both RSS and footprint pass its threshold.
func TestProcsAppliesFootprintToAChild(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	pid := child.Process.Pid

	// Wait until the fork has exec'd sleep: before that, the child is a copy
	// of this test binary.
	var rss uint64
	for i := 0; ; i++ {
		out, err := exec.Command("ps", "-o", "comm=,rss=", "-p", strconv.Itoa(pid)).Output()
		f := strings.Fields(string(out))
		if err == nil && len(f) == 2 && strings.HasSuffix(f[0], "sleep") {
			kb, perr := strconv.ParseUint(f[1], 10, 64)
			if perr != nil {
				t.Fatal(perr)
			}
			rss = kb * 1024
			break
		}
		if i == 50 {
			t.Fatalf("child %d never showed up as sleep: %q", pid, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A just-exec'd process's footprint moves while dyld maps and touches
	// pages (measured 2026-10-24 under -race: one sleep went 81992 -> 114760
	// -> ... -> 1278192 over its first milliseconds). Comparing a transient
	// read to a later Procs() is a race, so wait until two reads 20 ms apart
	// agree: that is the settled footprint both sides will see.
	var fp uint64
	for i := 0; ; i++ {
		a, err := physFootprint(pid)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		b, err := physFootprint(pid)
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			fp = b
			break
		}
		if i == 100 {
			t.Fatalf("footprint of child %d never settled (%d then %d)", pid, a, b)
		}
	}
	if fp == rss {
		t.Skipf("footprint == RSS (%d) for this sleep: the test cannot discriminate here", fp)
	}
	procs, err := Procs()
	if err != nil {
		t.Fatal(err)
	}
	got := procs[pid].MemBytes
	diff := int64(got) - int64(fp)
	if diff < 0 {
		diff = -diff
	}
	if diff > 64<<10 {
		t.Fatalf("Procs()[sleep].MemBytes = %d, physFootprint = %d (ps RSS = %d): Procs is not applying the footprint", got, fp, rss)
	}
	if got == rss {
		t.Fatalf("Procs()[sleep].MemBytes = %d = ps RSS: the footprint was not applied", got)
	}
}

// TestPhysFootprintMatchesTop checks we read the figure the user compares
// against: top's MEM column. top rounds and samples ~1 s after we do, and the
// Go runtime may return or take memory meanwhile. The bracket is what makes
// this robust: our footprint is read before AND after top, and top must fall
// in [min-slack, max+slack]. GC + FreeOSMemory only cut the runtime's churn
// during that second; they guarantee nothing on their own.
func TestPhysFootprintMatchesTop(t *testing.T) {
	if testing.Short() {
		t.Skip("forks top (~1 s)")
	}
	runtime.GC()
	debug.FreeOSMemory()
	touchAnon(t, 128<<20)
	before, err := physFootprint(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("top", "-l", "1", "-pid", strconv.Itoa(os.Getpid()), "-stats", "pid,mem").Output()
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	var topMem uint64
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == strconv.Itoa(os.Getpid()) {
			if topMem, err = parseTopMem(f[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if topMem == 0 {
		t.Fatalf("no line for pid %d in top output:\n%s", os.Getpid(), out)
	}
	after, err := physFootprint(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := min(before, after), max(before, after)
	slack := max(hi/10, 8<<20)
	if topMem+slack < lo || topMem > hi+slack {
		t.Fatalf("top MEM = %d MiB outside [%d, %d] MiB ± %d MiB (physFootprint before/after top)",
			topMem>>20, lo>>20, hi>>20, slack>>20)
	}
}

// TestPhysFootprintErrors pins the two errors applyFootprints branches on.
func TestPhysFootprintErrors(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := physFootprint(cmd.Process.Pid); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("reaped pid %d: err = %v, want ESRCH", cmd.Process.Pid, err)
	}
	if os.Geteuid() != 0 {
		if _, err := physFootprint(1); !errors.Is(err, syscall.EPERM) {
			t.Errorf("launchd (root) as a user: err = %v, want EPERM", err)
		}
	}
}

func BenchmarkProcs(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := Procs(); err != nil {
			b.Fatal(err)
		}
	}
}

// TestReadSelfFootprintOnThisProcessIsOK: on a healthy macOS the self-reading
// of the test binary passes the very verdict pigtaild logs at start — the
// proof the bounds do not warn on a normal process. Red here after a macOS
// update is the signal this feature exists for.
func TestReadSelfFootprintOnThisProcessIsOK(t *testing.T) {
	r := ReadSelfFootprint()
	if r.Err != nil || r.RSSErr != nil {
		t.Fatalf("ReadSelfFootprint errors: footprint %v, ps %v", r.Err, r.RSSErr)
	}
	if ok, reason, _ := JudgeSelfFootprint(r); !ok || reason != "" {
		t.Fatalf("self-reading judged (%v, %q): footprint=%d resident=%d rss=%d",
			ok, reason, r.Footprint, r.Resident, r.RSS)
	}
}

// rusageV4 and physFootprint must agree: physFootprint is now a view on it.
func TestPhysFootprintIsRusageV4Footprint(t *testing.T) {
	ri, err := rusageV4(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	fp, err := physFootprint(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	// Two reads microseconds apart; allow the runtime a little churn.
	diff := int64(fp) - int64(ri.PhysFootprint)
	if diff < -(8<<20) || diff > 8<<20 {
		t.Fatalf("physFootprint %d vs rusageV4 %d", fp, ri.PhysFootprint)
	}
	if ri.ResidentSize == 0 {
		t.Fatal("ri_resident_size is 0 for a live process")
	}
}

// The ps fork is bounded (C6): a hung ps makes the reading "unverified"
// within the timeout instead of holding up the daemon's start.
func TestReadSelfFootprintPSTimeoutIsUnverified(t *testing.T) {
	prevCmd, prevTimeout := selfCheckPSCommand, selfCheckPSTimeout
	t.Cleanup(func() { selfCheckPSCommand, selfCheckPSTimeout = prevCmd, prevTimeout })
	selfCheckPSTimeout = 100 * time.Millisecond
	selfCheckPSCommand = func(ctx context.Context, _ int) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "5")
	}

	start := time.Now()
	r := ReadSelfFootprint()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ReadSelfFootprint took %s with a 100ms ps timeout", elapsed)
	}
	if r.RSSErr == nil || !strings.Contains(r.RSSErr.Error(), "timed out") {
		t.Fatalf("RSSErr = %v, want a timeout", r.RSSErr)
	}
	if ok, reason, _ := JudgeSelfFootprint(r); !ok || !strings.HasPrefix(reason, "unverified") {
		t.Fatalf("verdict (%v, %q), want unverified (footprint is non-zero)", ok, reason)
	}
}

func TestSelfCheckLineOnDarwin(t *testing.T) {
	line, ok := SelfCheckLine()
	if !ok || !strings.HasPrefix(line, "sysmem: self footprint=") || !strings.Contains(line, "(footprint reader ok)") {
		t.Fatalf("SelfCheckLine = (%q, %v), want an ok line", line, ok)
	}
}
