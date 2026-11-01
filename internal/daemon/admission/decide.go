// Package admission holds heavy commands (tsc, eslint, jest, vitest, nx, a
// storybook build) back while the machine does not have the memory to run
// one more of them, and lets them through, one at a time and fairly between
// worktrees, as soon as it does.
//
// It answers a different question from cpuslots, and deliberately does not
// merge with it: cpuslots shares a static budget of cores one test file at a
// time, over seconds; admission shares *measured* memory one process tree at
// a time, over minutes. The incident that motivated it (2026-09-22: load 124,
// swap 16.4/17.4 GB, 24.7 GB of node) was a memory incident on a machine with
// cores to spare.
//
// Same contract as cpuslots, on purpose: the daemon arbitrates and nothing
// else — it spawns nothing and kills nothing (docs/architecture.md §10). A
// lease lives exactly as long as the client's connection; the kernel closing
// that socket is the release, whatever ended the client.
package admission

import "time"

// Reason names what is holding the queue's head back. The empty Reason means
// "admit".
type Reason string

const (
	ReasonMemory   Reason = "memory"
	ReasonPressure Reason = "pressure"
	ReasonSwapout  Reason = "swapout"
	// ReasonCompress means the memory compressor is working faster than its
	// gate (compressions + decompressions per second).
	ReasonCompress Reason = "compress"
	ReasonMaxJobs  Reason = "max_jobs"
	// ReasonClassLimit means this class already has as many runs on the
	// machine as it is allowed, whatever the memory says.
	ReasonClassLimit Reason = "class_limit"
	// ReasonCompressCalm holds a class of Limits.CompressCalmClasses while the
	// compressor is over its gate over the last ten seconds or over the calm
	// window. Unlike ReasonCompress, a long wait does not lift it (D1).
	ReasonCompressCalm Reason = "compress_calm"
	// ReasonDisk means the free disk space is under Limits.DiskFloorBytes. A
	// lease buys no disk back and a long wait does not either: only space
	// freed does, so it holds the queue even on an empty machine.
	ReasonDisk Reason = "disk"
)

// Reading is one sample of the machine's memory state.
type Reading struct {
	// Available is what the kernel could reclaim right now (sysmem).
	Available uint64
	// Pressure is the kernel's verdict on the darwin scale: 1 normal,
	// 2 warn, 4 critical. 0 means unknown and never gates.
	Pressure int
	// SwapoutBytesPerSec is the swap-out rate over the recent window.
	SwapoutBytesPerSec float64
	// CompressBytesPerSec is the compressor traffic (compressions +
	// decompressions) over the recent window.
	CompressBytesPerSec float64
	// CompressCalmBytesPerSec is the same traffic over the calm window
	// (Config.CompressCalmWindow); 0 when that window is 0.
	CompressCalmBytesPerSec float64
	// CompressorBytes and UnusedBytes are for display only, no gate reads
	// them: the RAM the compressor holds and the pages the kernel lists free.
	CompressorBytes, UnusedBytes uint64
	// DiskFree is the free space of the tightest volume pigtail's commands
	// write to; meaningful only when DiskKnown.
	DiskFree uint64
	// DiskKnown is false when the disk was never read or the last read
	// failed: an unknown disk never gates, and a stale value is never reused.
	DiskKnown bool
}

// Limits are the thresholds verdict applies. A zero or negative value turns
// its gate off.
type Limits struct {
	FloorBytes uint64
	// DiskFloorBytes holds every admission while the free disk space is under
	// it. Zero turns the gate off.
	DiskFloorBytes uint64
	MaxJobs        int
	PressureLevel  int
	// PressureCriticalLevel is the level at which nothing more is admitted at
	// all (4 = critical). Below it, pressure only brakes — see verdict.
	PressureCriticalLevel int
	// PressureFloorFactor multiplies the floor while the kernel reports
	// pressure at PressureLevel: the queue keeps moving, with more headroom
	// demanded of it. 0 or 1 means "no extra margin".
	PressureFloorFactor float64
	SwapoutBytesPerSec  float64
	// CompressBytesPerSec holds the queue while the compressor traffic is
	// above it. Zero or negative turns the gate off.
	CompressBytesPerSec float64
	// MaxStarve is how long a waiter may be held back before the memory floor
	// is the only thing still standing between it and a lease. Zero turns the
	// guarantee off.
	MaxStarve time.Duration
	// ClassLimits caps how many leases one class may hold at once, for the
	// classes where the scarce resource is not a number of bytes. A
	// browser-mode vitest run spawns one live Chromium per worker: two such
	// runs can fit in memory and still thrash the machine, which is why
	// a large monorepo carries a repo-local lock (scripts/
	// with-test-lock.js) this gate is meant to replace. A class absent from
	// the map, or mapped to zero or less, is not gated.
	ClassLimits map[string]int
	// CompressCalmClasses are the classes that start only once the compressor
	// is under CompressBytesPerSec over ten seconds *and* over the calm
	// window, whatever their wait. Empty turns the rule off.
	CompressCalmClasses map[string]bool
	// CompressCalmMaxStarve releases a waiter from that rule once it has
	// waited this long; zero never does.
	CompressCalmMaxStarve time.Duration
}

// Held is one admitted lease as the decision sees it: what it was expected to
// cost, and what its process tree actually holds so far.
type Held struct {
	// ID is the lease's id, which a nested request names as its Parent.
	ID    string
	Group string
	Class string
	Cost  uint64
	Mem   uint64
}

// Waiter is one queued request as the decision sees it. The slice order
// carries the FIFO.
type Waiter struct {
	Group string
	Class string
	Cost  uint64
	// Parent is the id of the held lease this request runs under, when its
	// command was started by an admitted one (a generic envelope). That lease
	// is not counted against the request; see verdict.
	Parent string
	// Waited is how long it has been in the queue, which past Limits.MaxStarve
	// becomes an admission reason of its own.
	Waited time.Duration
	// Focus marks a caller in focus (an open focus of its subject or theme, or
	// a pinned workspace): considered before any non-focus waiter.
	Focus bool
}

// outstanding is the memory already promised to admitted leases but not yet
// taken (cost − current tree memory): a tsc admitted a second ago holds 50 MB and will hold 1.3 GB in
// thirty seconds. Without this, a burst of five would all be admitted against
// the same untouched "available".
func outstanding(held []Held) uint64 {
	var sum uint64
	for _, h := range held {
		if h.Cost > h.Mem {
			sum += h.Cost - h.Mem
		}
	}
	return sum
}

// withoutParent returns held without the lease whose id is parent.
func withoutParent(held []Held, parent string) []Held {
	if parent == "" {
		return held
	}
	out := make([]Held, 0, len(held))
	for _, h := range held {
		if h.ID != parent {
			out = append(out, h)
		}
	}
	return out
}

// classLimit reports the cap on w's class and how many leases of that class
// are already held. A class with no cap returns limit 0, which never gates.
func classLimit(l Limits, held []Held, w Waiter) (limit, n int) {
	limit = l.ClassLimits[w.Class]
	if limit <= 0 {
		return 0, 0
	}
	for _, h := range held {
		if h.Class == w.Class {
			n++
		}
	}
	return limit, n
}

// verdict decides whether request w may start now.
//
// Two rules override the gates, and both exist so the machine always makes
// progress:
//
//   - Anti-deadlock: with nothing admitted, the head goes through whatever
//     the machine says. Background load the controller does not own (48 agent
//     sessions, a browser) can keep "available" under the floor indefinitely;
//     refusing then would stop every heavy command on the machine for good.
//   - Anti-starvation: a waiter past MaxStarve owes nothing but the plain
//     memory floor. Measured in production on 2026-09-24 — four jobs queued
//     behind one lease with 13 GiB available and no swapping at all, because
//     "nothing admitted" is a rule that never fires while a single long job
//     runs. A user session sat there for minutes.
//
// Kernel pressure brakes, it does not switch off. `warn` (2) is a level this
// machine sits at for hours after a busy spell — 0.1 GiB free and 13.5 GiB
// inactive is a normal state for macOS, not a crisis — so treating it as a
// stop sign reproduced, one layer up, the mistake this package documented
// about swap *usage*: a signal that stays lit long after the thing it warned
// about is over. What distinguishes a warned machine that is coping from one
// that is thrashing is whether it is actually paging, so under warn the swap
// gate tightens to "any swapping at all" and the floor gains a margin. The
// compressor-traffic gate, whose rate is never zero on a loaded machine
// (decompressions never stop), keeps its own threshold under warn. Only
// `critical` (4) stops admissions outright.
//
// The disk floor is the one gate that also holds an empty machine.
//
// Pure: no I/O, no clock.
func verdict(r Reading, l Limits, held []Held, w Waiter) Reason {
	limit, n := classLimit(l, held, w)
	// A request running under a held lease (w.Parent) does not count that
	// lease: the envelope waits for its child, so charging the child for it
	// would deadlock the two. The class limit above is read before, on the
	// whole set. An unknown parent matches nothing: an ordinary request.
	held = withoutParent(held, w.Parent)
	if limit > 0 && n >= limit {
		// A class limit stands whatever else is true: what it rations is not
		// bytes, so neither free memory nor a long wait can buy it back. Only
		// a lease of that class ending does.
		return ReasonClassLimit
	}
	// The disk is a hard stop, before the anti-deadlock shortcut: an empty
	// machine with a full disk is exactly the 2026-10-31 outage, and a
	// command admitted there only fills it further. Neither a long wait nor
	// MaxStarve buys it back; only freed space does. An unknown disk (never
	// read, or the last read failed) never gates, so a broken statfs cannot
	// stop the machine for good.
	if l.DiskFloorBytes > 0 && r.DiskKnown && r.DiskFree < l.DiskFloorBytes {
		return ReasonDisk
	}
	if len(held) == 0 {
		return ""
	}
	if l.PressureCriticalLevel > 0 && r.Pressure >= l.PressureCriticalLevel {
		return ReasonPressure
	}
	warned := l.PressureLevel > 0 && r.Pressure >= l.PressureLevel
	starving := l.MaxStarve > 0 && w.Waited >= l.MaxStarve
	// The heavy classes wait for durable calm, and a long wait does not buy
	// it back (D1, 2026-10-28): on 2026-10-27 two builds released by
	// MaxStarve landed in the compressor's burst.
	calm := l.CompressBytesPerSec > 0 && l.CompressCalmClasses[w.Class] &&
		(l.CompressCalmMaxStarve <= 0 || w.Waited < l.CompressCalmMaxStarve)

	if !starving {
		if l.MaxJobs > 0 && len(held) >= l.MaxJobs {
			return ReasonMaxJobs
		}
		// Under warn, any measurable swapping means the machine is paying for
		// its memory; when calm, only a sustained rate counts.
		if l.SwapoutBytesPerSec > 0 {
			ceiling := l.SwapoutBytesPerSec
			if warned {
				ceiling = 0
			}
			if r.SwapoutBytesPerSec > ceiling {
				return ReasonSwapout
			}
		}
		// Compressor traffic keeps its own threshold under warn: unlike the
		// swap-out rate it is never zero on a loaded machine (decompressions
		// run all the time), so a zero ceiling would turn warn into a stop
		// until MaxStarve.
		if !calm && l.CompressBytesPerSec > 0 && r.CompressBytesPerSec > l.CompressBytesPerSec {
			return ReasonCompress
		}
	}
	if calm && (r.CompressBytesPerSec > l.CompressBytesPerSec || r.CompressCalmBytesPerSec > l.CompressBytesPerSec) {
		return ReasonCompressCalm
	}

	floor := l.FloorBytes
	if warned && !starving && l.PressureFloorFactor > 1 {
		floor = uint64(float64(floor) * l.PressureFloorFactor)
	}
	promised := outstanding(held) + w.Cost
	if r.Available < promised+floor {
		// Say which floor it fell short of: the plain one is the machine being
		// full, the reinforced one is pigtail being careful under pressure.
		if floor > l.FloorBytes && r.Available >= promised+l.FloorBytes {
			return ReasonPressure
		}
		return ReasonMemory
	}
	return ""
}

// starvingIndex returns the longest-waiting waiter past max, or -1.
//
// It runs before the fairness pick, and that ordering is the whole point: the
// gates verdict applies are machine-wide, so a waiter the fairness rule did
// not pick is refused for a reason that has nothing to do with it and the
// queue stops there. On 2026-09-24 that left four jobs queued behind one lease
// with 13 GiB free. Serving the longest wait first is also what keeps the
// guarantee a guarantee: whoever has waited most is looked at first, so no one
// can be passed over twice.
//
// Pure: no I/O, no clock.
func starvingIndex(queue []Waiter, blocked []bool, max time.Duration) int {
	if max <= 0 {
		return -1
	}
	best := -1
	for i, w := range queue {
		if i < len(blocked) && blocked[i] {
			continue
		}
		if w.Waited < max {
			continue
		}
		if best < 0 || w.Waited > queue[best].Waited {
			best = i
		}
	}
	return best
}

// nextToAdmit picks the waiter to consider next: the oldest waiter of the
// group holding the fewest leases, ignoring the ones marked in blocked.
// Focus waiters are considered before the others: the order is starving
// (starvingIndex, run first by the caller) -> focus -> fewest leases -> oldest.
// queue MUST be ordered oldest-first, and blocked MUST be the same length.
//
// Only that one waiter is considered — there is no backfill. If it does not
// fit, nobody behind it jumps ahead, which is what guarantees a large job is
// never starved by a stream of small ones.
//
// blocked is the one exception, and it is not backfill: a waiter held by
// ReasonClassLimit is not waiting for memory, so nothing the queue behind it
// does — finishing, shrinking, freeing gigabytes — can ever let it through.
// It waits for a lease of *its own class* to end. Letting it hold the queue
// would mean one browser-mode run stops every tsc and eslint on the machine
// for its whole duration, which is a worse outage than the one this gate
// exists to prevent. Order within a class is still FIFO, so no browser run
// is starved by another.
//
// Returns the index into queue, or -1 when none is eligible.
// Pure: no I/O, no clock.
func nextToAdmit(queue []Waiter, held map[string]int, blocked []bool) int {
	anyFocus := false
	for i, w := range queue {
		if w.Focus && (i >= len(blocked) || !blocked[i]) {
			anyFocus = true
			break
		}
	}
	best := -1
	for i, w := range queue {
		if i < len(blocked) && blocked[i] {
			continue
		}
		if anyFocus && !w.Focus {
			continue
		}
		if best < 0 || held[w.Group] < held[queue[best].Group] {
			best = i
		}
	}
	return best
}
