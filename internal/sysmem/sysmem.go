// Package sysmem reads system and per-process memory: how much the kernel
// could hand out right now, how hard it is working to do so, and how much
// each process tree holds.
//
// It is the one place both memory consumers read from: rammon (which evicts
// idle dev servers under pressure) and the admission controller (which holds
// heavy commands back before pressure builds). Sharing the measurement is what
// keeps `pigtail servers` and `pigtail slots` telling the same story about the
// same machine.
//
// Parsers are pure and live here; the platform readers live in the _darwin
// and _linux files.
package sysmem

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Proc is one process's memory at a point in time.
//
// MemBytes is what the process costs the machine. On darwin it is the
// physical footprint (ri_phys_footprint: top's MEM column, compressed pages
// included); on Linux it is VmRSS. It was ps RSS until 2026-10, which leaves
// compressed memory out: a storybook read 187 MiB there against 2.3 GiB in top.
type Proc struct {
	PID, PPID int
	MemBytes  uint64
	// CPUPercent is ps's pcpu (percent of one core, cumulative over the
	// process's life). Read on darwin only: 0 on Linux, whose Procs reads
	// /proc without ps.
	CPUPercent float64
}

// Pressure levels, on the kernel's own scale (the values of darwin's
// kern.memorystatus_vm_pressure_level). Linux readings are mapped onto it.
const (
	PressureNormal   = 1
	PressureWarn     = 2
	PressureCritical = 4
)

var (
	vmPageSizeRE = regexp.MustCompile(`page size of (\d+) bytes`)
	vmLineRE     = regexp.MustCompile(`^(.+?):\s+(\d+)\.`)
)

// parseVMStat decodes `vm_stat` output into its page size and a map of
// lower-cased counter name to page count.
func parseVMStat(out string) (pageSize uint64, pages map[string]uint64, err error) {
	m := vmPageSizeRE.FindStringSubmatch(out)
	if m == nil {
		return 0, nil, fmt.Errorf("vm_stat: no page size header")
	}
	pageSize, err = strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, nil, fmt.Errorf("vm_stat: page size: %w", err)
	}
	pages = map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		lm := vmLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if lm == nil {
			continue
		}
		n, err := strconv.ParseUint(lm[2], 10, 64)
		if err != nil {
			continue
		}
		pages[strings.ToLower(lm[1])] = n
	}
	return pageSize, pages, nil
}

// ParseVMStatAvailable computes reclaimable bytes from `vm_stat` output:
// (free + inactive + speculative + purgeable) * page-size.
//
// Known optimism, kept on purpose for continuity with what `pigtail servers`
// has always shown: "inactive" includes anonymous pages the kernel can only
// reclaim by compressing or swapping them. The admission controller corrects
// for that with the kernel's own pressure verdict rather than by changing
// this number.
func ParseVMStatAvailable(out string) (uint64, error) {
	pageSize, pages, err := parseVMStat(out)
	if err != nil {
		return 0, err
	}
	avail := pages["pages free"] + pages["pages inactive"] +
		pages["pages speculative"] + pages["pages purgeable"]
	if avail == 0 {
		return 0, fmt.Errorf("vm_stat: no reclaimable pages parsed")
	}
	return avail * pageSize, nil
}

// ParseVMStatSwapouts returns the cumulative bytes swapped out since boot,
// from `vm_stat`'s "Swapouts" counter. A counter, not a level: callers
// derive a rate from two readings.
func ParseVMStatSwapouts(out string) (uint64, error) {
	pageSize, pages, err := parseVMStat(out)
	if err != nil {
		return 0, err
	}
	n, ok := pages["swapouts"]
	if !ok {
		return 0, fmt.Errorf("vm_stat: no Swapouts counter")
	}
	return n * pageSize, nil
}

// Compressor is the memory compressor's state read from one `vm_stat`, in
// bytes: two cumulative counters (since boot) and two levels.
type Compressor struct {
	Compressions   uint64 // cumulative bytes compressed since boot
	Decompressions uint64 // cumulative bytes decompressed since boot
	OccupiedBytes  uint64 // RAM the compressor holds ("occupied", not "stored in")
	UnusedBytes    uint64 // "Pages free"
}

// ParseVMStatCompressor reads the compressor counters from `vm_stat` output.
// A vm_stat without Compressions or Decompressions is an error, never a zero.
func ParseVMStatCompressor(out string) (Compressor, error) {
	pageSize, pages, err := parseVMStat(out)
	if err != nil {
		return Compressor{}, err
	}
	comp, ok := pages["compressions"]
	if !ok {
		return Compressor{}, fmt.Errorf("vm_stat: no Compressions counter")
	}
	decomp, ok := pages["decompressions"]
	if !ok {
		return Compressor{}, fmt.Errorf("vm_stat: no Decompressions counter")
	}
	return Compressor{
		Compressions:   comp * pageSize,
		Decompressions: decomp * pageSize,
		OccupiedBytes:  pages["pages occupied by compressor"] * pageSize,
		UnusedBytes:    pages["pages free"] * pageSize,
	}, nil
}

// ParsePSRss decodes `ps -axo pid=,ppid=,rss=,pcpu=` (rss in KiB, pcpu with a
// decimal point: run ps under LC_ALL=C) into Procs whose MemBytes is the RSS
// — the darwin reader then replaces it with the footprint (applyFootprints).
func ParsePSRss(out string) (map[int]Proc, error) {
	procs := map[int]Proc{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		rssKB, err3 := strconv.ParseUint(f[2], 10, 64)
		cpu, err4 := strconv.ParseFloat(f[3], 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		procs[pid] = Proc{PID: pid, PPID: ppid, MemBytes: rssKB * 1024, CPUPercent: cpu}
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("ps: no parseable processes")
	}
	return procs, nil
}

// SumTree returns the total MemBytes of the given root PIDs plus all descendants,
// walking the ppid graph in the snapshot.
func SumTree(snap map[int]Proc, roots []int) uint64 {
	mem, _ := sumTree(snap, roots)
	return mem
}

// SumTreeCPU returns the total CPUPercent of the same tree SumTree walks.
func SumTreeCPU(snap map[int]Proc, roots []int) float64 {
	_, cpu := sumTree(snap, roots)
	return cpu
}

func sumTree(snap map[int]Proc, roots []int) (uint64, float64) {
	children := map[int][]int{}
	for pid, st := range snap {
		children[st.PPID] = append(children[st.PPID], pid)
	}
	seen := map[int]bool{}
	var total uint64
	var cpu float64
	var walk func(pid int)
	walk = func(pid int) {
		if seen[pid] {
			return
		}
		seen[pid] = true
		if st, ok := snap[pid]; ok {
			total += st.MemBytes
			cpu += st.CPUPercent
		}
		for _, c := range children[pid] {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return total, cpu
}

// applyFootprints replaces each process's ps RSS with the footprint read
// returns for it, in place (decision A4).
//
// A process that exited since ps (ESRCH) weighs 0 — its memory is freed —
// but keeps its entry: SumTree reaches children through their parent's
// entry, and dropping it would cut a live subtree out of the total. EPERM
// (another user's process: expected, hundreds of them) keeps the RSS
// silently — the best figure there is; 0 would make a root-owned child look
// free. Any other error keeps the RSS too, and goes to report: it is not
// expected, and is what a changed private ABI would look like.
func applyFootprints(procs map[int]Proc, read func(pid int) (uint64, error), report func(pid int, err error)) {
	for pid, p := range procs {
		fp, err := read(pid)
		switch {
		case err == nil:
			p.MemBytes = fp
		case errors.Is(err, syscall.ESRCH):
			p.MemBytes = 0
		case errors.Is(err, syscall.EPERM):
			continue
		default:
			report(pid, err)
			continue
		}
		procs[pid] = p
	}
}

// oncePerErrno logs an unexpected footprint error once per distinct error
// for the life of the process — never once per process or per sample.
type oncePerErrno struct {
	logf func(format string, args ...any)

	mu   sync.Mutex
	seen map[string]bool
}

func (o *oncePerErrno) report(pid int, err error) {
	key := err.Error()
	o.mu.Lock()
	if o.seen == nil {
		o.seen = map[string]bool{}
	}
	first := !o.seen[key]
	o.seen[key] = true
	o.mu.Unlock()
	if first {
		o.logf("sysmem: footprint unreadable (%v, first on pid %d): falling back to ps RSS for every process failing this way (logged once per error)", err, pid)
	}
}

// ParsePSIMemory maps Linux PSI (/proc/pressure/memory) onto the darwin
// pressure scale, from the "some avg10" percentage: the share of the last ten
// seconds during which at least one task stalled on memory.
//
// Thresholds: under 10% is normal, under 40% is warn, beyond is critical.
// They are coarse by design — the only consumer asks "is the kernel already
// struggling", not "by how much".
func ParsePSIMemory(out string) (int, error) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "some" {
			continue
		}
		for _, kv := range f[1:] {
			v, ok := strings.CutPrefix(kv, "avg10=")
			if !ok {
				continue
			}
			pct, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return 0, fmt.Errorf("psi: avg10: %w", err)
			}
			switch {
			case pct < 10:
				return PressureNormal, nil
			case pct < 40:
				return PressureWarn, nil
			default:
				return PressureCritical, nil
			}
		}
	}
	return 0, fmt.Errorf("psi: no \"some avg10\" field")
}

// ParseProcVMStatSwapouts returns cumulative bytes swapped out, from Linux's
// /proc/vmstat "pswpout" counter (in pages).
func ParseProcVMStatSwapouts(out string, pageSize uint64) (uint64, error) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "pswpout" {
			n, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("vmstat: pswpout: %w", err)
			}
			return n * pageSize, nil
		}
	}
	return 0, fmt.Errorf("vmstat: no pswpout counter")
}

// Memory is one reading of the system-wide memory state.
type Memory struct {
	// Available is what the kernel could reclaim right now.
	Available uint64
	// Swapouts is the cumulative bytes swapped out since boot (a counter).
	Swapouts uint64
	// Compressions and Decompressions are the cumulative bytes the memory
	// compressor handled since boot (counters, in bytes); 0 = unread.
	Compressions, Decompressions uint64
	// CompressorBytes is the RAM the compressor holds; UnusedBytes the pages
	// the kernel lists as free (levels, in bytes); 0 = unread.
	CompressorBytes, UnusedBytes uint64
	// Pressure is the kernel's verdict on the 1/2/4 scale, 0 when this
	// platform cannot say (a Linux kernel without PSI).
	Pressure int
	// Total is the machine's physical memory.
	Total uint64
}

// SelfFootprint is one reading of this process's own memory, taken both ways
// at daemon start so a broken footprint reader shows up in the log (decision
// D7). Err is the footprint call's error; RSSErr is ps's.
type SelfFootprint struct {
	Footprint uint64 // ri_phys_footprint
	Resident  uint64 // ri_resident_size, from the same call
	RSS       uint64 // ps RSS of the same pid
	Err       error
	RSSErr    error
}

// Bounds of a healthy self-reading, measured 2026-10-02 on macOS 25.6:
//   - footprint/RSS was 0.61-0.63 for a fresh Go binary and 1.06 for a
//     pigtaild up 2h23m. Machine-wide it spans 0.19-25 (compressed memory of
//     long-lived processes), which is why the check applies to pigtaild
//     itself at start only. [1/8, 4] leaves ~5x below and ~4x above.
//   - ri_resident_size matched ps RSS exactly for pigtaild (34.3 MiB both).
//     It cross-checks the struct layout, which the ratio alone cannot: a
//     shifted layout would stop matching ps. [1/2, 2] leaves 2x either way.
const (
	selfRatioMin    = 0.125
	selfRatioMax    = 4.0
	selfResidentMin = 0.5
	selfResidentMax = 2.0
)

// JudgeSelfFootprint decides whether the footprint reader looks healthy.
// Order: call error, zero footprint (needs no reference: a live process never
// weighs 0), ps unavailable, resident cross-check, ratio.
// ok=true with an empty reason is healthy; ok=true with a reason starting
// "unverified" means a non-zero footprint with nothing to compare against
// (ps failed) — not a warning, since a ps failure is not an ABI break. ok=false is a broken
// reader; fallsBack reports whether the figures then fall back to ps RSS
// (the call errors, so applyFootprints keeps RSS) rather than being wrong
// (the call succeeds with a bad value, which applyFootprints uses). Pure.
func JudgeSelfFootprint(r SelfFootprint) (ok bool, reason string, fallsBack bool) {
	if r.Err != nil {
		return false, fmt.Sprintf("footprint call failed: %v", r.Err), true
	}
	// Before the ps check on purpose: a live pigtaild never weighs 0, so this
	// needs no reference figure (D7: the warning is unconditional).
	if r.Footprint == 0 {
		return false, "footprint is 0", false
	}
	if r.RSSErr != nil || r.RSS == 0 {
		why := "ps RSS is 0"
		if r.RSSErr != nil {
			why = fmt.Sprintf("ps RSS unavailable: %v", r.RSSErr)
		}
		return true, "unverified: " + why, false
	}
	rss := float64(r.RSS)
	if res := float64(r.Resident); res < rss*selfResidentMin || res > rss*selfResidentMax {
		return false, fmt.Sprintf("resident %dMiB disagrees with ps RSS (struct layout changed?)", r.Resident>>20), false
	}
	if ratio := float64(r.Footprint) / rss; ratio < selfRatioMin || ratio > selfRatioMax {
		return false, fmt.Sprintf("footprint/RSS ratio %.2f outside [%.3g, %.3g]", ratio, selfRatioMin, selfRatioMax), false
	}
	return true, "", false
}

// FormatSelfCheck renders the startup line. Always prefixed "sysmem: " so
// `grep sysmem daemon.log` finds it next to the reader's own error line. Pure.
func FormatSelfCheck(r SelfFootprint) string {
	fp, rss := "?", "?"
	if r.Err == nil {
		fp = fmt.Sprintf("%dMiB", r.Footprint>>20)
	}
	if r.RSSErr == nil && r.RSS > 0 {
		rss = fmt.Sprintf("%dMiB", r.RSS>>20)
	}
	ok, reason, fallsBack := JudgeSelfFootprint(r)
	switch {
	case ok && reason == "":
		return fmt.Sprintf("sysmem: self footprint=%s rss=%s (footprint reader ok)", fp, rss)
	case ok:
		return fmt.Sprintf("sysmem: self footprint=%s rss=%s (footprint reader %s)", fp, rss, reason)
	}
	consequence := "memory figures are wrong"
	if fallsBack {
		consequence = "memory figures fall back to ps RSS"
	}
	return fmt.Sprintf("sysmem: WARNING self footprint=%s rss=%s — footprint reader may be broken (macOS update?): %s; %s",
		fp, rss, reason, consequence)
}
