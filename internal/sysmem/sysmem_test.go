package sysmem

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

func TestParseVMStatAvailable(t *testing.T) {
	const out = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                3839.
Pages active:                           1418454.
Pages inactive:                         1414865.
Pages speculative:                         1377.
Pages wired down:                        538199.
Pages purgeable:                           4370.
`
	got, err := ParseVMStatAvailable(out)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(3839+1414865+1377+4370) * 16384
	if got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestParsePSRssFourFields(t *testing.T) {
	const out = "100 1 500000 12.5\n200 100 1000 0.0\n300 200 250\nabc 1 2 3.0\n400 1 7 x\n"
	snap, err := ParsePSRss(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) != 2 {
		t.Fatalf("want 2 parsed lines (3-field and non-numeric skipped), got %d: %v", len(snap), snap)
	}
	if snap[100].CPUPercent != 12.5 || snap[100].MemBytes != 500000*1024 {
		t.Fatalf("pid 100: %+v", snap[100])
	}
}

func TestSumTreeCPU(t *testing.T) {
	snap := map[int]Proc{
		100: {PID: 100, PPID: 1, CPUPercent: 10},
		200: {PID: 200, PPID: 100, CPUPercent: 5.5},
		300: {PID: 300, PPID: 200, CPUPercent: 1},
		400: {PID: 400, PPID: 1, CPUPercent: 99},
	}
	if got := SumTreeCPU(snap, []int{100}); got != 16.5 {
		t.Fatalf("SumTreeCPU = %v, want 16.5", got)
	}
}

func TestParsePSRssAndSumTree(t *testing.T) {
	const out = "100 1 500000 1.5\n200 100 1000000 0.0\n300 200 250000 2.5\n"
	snap, err := ParsePSRss(out)
	if err != nil {
		t.Fatal(err)
	}
	if snap[100].MemBytes != 500000*1024 {
		t.Fatalf("rss parse: %d", snap[100].MemBytes)
	}
	got := SumTree(snap, []int{100})
	want := uint64(500000+1000000+250000) * 1024
	if got != want {
		t.Fatalf("SumTree got %d want %d", got, want)
	}
}

// TestApplyFootprints pins the merge policy of the darwin reader (A4): a
// footprint replaces the ps RSS; a process gone since ps (ESRCH) weighs 0 but
// stays in the map so its children are still reached; EPERM (another user's
// process) keeps the ps RSS silently; any other errno keeps the ps RSS and is
// reported — an under-count, never a free process.
func TestApplyFootprints(t *testing.T) {
	procs := map[int]Proc{
		100: {PID: 100, PPID: 1, MemBytes: 10 << 20},
		200: {PID: 200, PPID: 100, MemBytes: 20 << 20},
		300: {PID: 300, PPID: 100, MemBytes: 30 << 20},
		400: {PID: 400, PPID: 300, MemBytes: 40 << 20},
		500: {PID: 500, PPID: 100, MemBytes: 50 << 20},
	}
	read := func(pid int) (uint64, error) {
		switch pid {
		case 200:
			return 0, syscall.EPERM
		case 300:
			return 0, syscall.ESRCH
		case 500:
			return 0, syscall.EINVAL
		}
		return uint64(pid) << 21, nil // 100 → 200 MiB, 400 → 800 MiB
	}
	var reported []int
	applyFootprints(procs, read, func(pid int, err error) {
		if !errors.Is(err, syscall.EINVAL) {
			t.Errorf("pid %d: reported %v — EPERM and ESRCH are expected, never reported", pid, err)
		}
		reported = append(reported, pid)
	})
	if len(reported) != 1 || reported[0] != 500 {
		t.Fatalf("reported = %v, want only the EINVAL pid 500", reported)
	}

	want := map[int]uint64{100: 200 << 20, 200: 20 << 20, 300: 0, 400: 800 << 20, 500: 50 << 20}
	for pid, w := range want {
		p, ok := procs[pid]
		if !ok {
			t.Fatalf("pid %d dropped from the map: its subtree would vanish", pid)
		}
		if p.MemBytes != w {
			t.Errorf("pid %d: MemBytes = %d, want %d", pid, p.MemBytes, w)
		}
		if p.PID != pid {
			t.Errorf("pid %d: PID rewritten to %d", pid, p.PID)
		}
	}
	if got, w := SumTree(procs, []int{100}), uint64(200+20+0+800+50)<<20; got != w {
		t.Fatalf("SumTree through a dead parent = %d, want %d", got, w)
	}
}

// TestOncePerErrnoLogsEachErrnoOnce: an unexpected errno is logged, but once
// per errno for the daemon's life — Procs runs every 2 s over ~1300
// processes, a per-process line would flood daemon.log.
func TestOncePerErrnoLogsEachErrnoOnce(t *testing.T) {
	var lines []string
	o := &oncePerErrno{logf: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	o.report(11, syscall.EINVAL)
	o.report(12, syscall.EINVAL)
	o.report(13, syscall.EIO)
	o.report(14, syscall.EINVAL)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one per errno (2):\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "sysmem: footprint unreadable") ||
		!strings.Contains(lines[0], syscall.EINVAL.Error()) || !strings.Contains(lines[0], "pid 11") {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], syscall.EIO.Error()) {
		t.Errorf("second line = %q", lines[1])
	}
}

func TestParseVMStatSwapouts(t *testing.T) {
	const out = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                3839.
Swapins:                                 120000.
Swapouts:                                4696263.
`
	got, err := ParseVMStatSwapouts(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(4696263) * 16384; got != want {
		t.Fatalf("got %d want %d", got, want)
	}
	if _, err := ParseVMStatSwapouts("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 1.\n"); err == nil {
		t.Fatal("a vm_stat without Swapouts must be an error, not zero")
	}
}

func TestParsePSIMemory(t *testing.T) {
	cases := []struct {
		avg10 string
		want  int
	}{
		{"0.00", PressureNormal},
		{"9.99", PressureNormal},
		{"10.00", PressureWarn},
		{"39.90", PressureWarn},
		{"40.00", PressureCritical},
		{"87.12", PressureCritical},
	}
	for _, c := range cases {
		out := "some avg10=" + c.avg10 + " avg60=0.00 avg300=0.00 total=0\nfull avg10=99.00 avg60=0.00 avg300=0.00 total=0\n"
		got, err := ParsePSIMemory(out)
		if err != nil {
			t.Fatalf("%s: %v", c.avg10, err)
		}
		if got != c.want {
			t.Fatalf("avg10=%s: got %d want %d", c.avg10, got, c.want)
		}
	}
	if _, err := ParsePSIMemory("full avg10=1.00\n"); err == nil {
		t.Fatal("PSI without a \"some\" line must be an error")
	}
}

func TestParseProcVMStatSwapouts(t *testing.T) {
	got, err := ParseProcVMStatSwapouts("pswpin 10\npswpout 250\n", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if got != 250*4096 {
		t.Fatalf("got %d", got)
	}
	if _, err := ParseProcVMStatSwapouts("pswpin 10\n", 4096); err == nil {
		t.Fatal("missing pswpout must be an error")
	}
}

// TestParseVMStatCompressor: the compressor counters, in bytes. "occupied"
// is the RAM the compressor holds; "stored in" (what it holds, uncompressed)
// must not be read in its place.
func TestParseVMStatCompressor(t *testing.T) {
	const out = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                    48280.
Pages stored in compressor:                  2402670.
Pages occupied by compressor:                 976718.
Decompressions:                            606357584.
Compressions:                              787960379.
Swapouts:                                    1520417.
`
	got, err := ParseVMStatCompressor(out)
	if err != nil {
		t.Fatal(err)
	}
	want := Compressor{Compressions: 787960379 * 16384, Decompressions: 606357584 * 16384,
		OccupiedBytes: 976718 * 16384, UnusedBytes: 48280 * 16384}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	small, err := ParseVMStatCompressor(strings.Replace(out, "16384", "4096", 1))
	if err != nil || small.Compressions != 787960379*4096 {
		t.Fatalf("page size must scale the counters: %+v, %v", small, err)
	}
	if _, err := ParseVMStatCompressor("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 1.\nDecompressions: 2.\n"); err == nil {
		t.Fatal("a vm_stat without Compressions must be an error, not zero")
	}
}
