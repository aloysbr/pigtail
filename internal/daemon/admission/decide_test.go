package admission

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/heavycmd"
)

const gib = uint64(1) << 30

func baseLimits() Limits {
	return Limits{
		FloorBytes: 6 * gib, MaxJobs: 6,
		PressureLevel: 2, PressureCriticalLevel: 4, PressureFloorFactor: 1.5,
		SwapoutBytesPerSec:  4 << 20,
		CompressBytesPerSec: 16 << 20,
		MaxStarve:           3 * time.Minute,
	}
}

func TestVerdict(t *testing.T) {
	calm := Reading{Available: 30 * gib, Pressure: 1}
	oneHeld := []Held{{Group: "a", Cost: 2 * gib, Mem: 2 * gib}}

	cases := []struct {
		name   string
		r      Reading
		l      Limits
		held   []Held
		need   uint64
		waited time.Duration
		want   Reason
	}{
		{"fits comfortably", calm, baseLimits(), oneHeld, 2 * gib, 0, ""},
		{"nothing held admits whatever the memory says (anti-deadlock)",
			Reading{Available: 1 * gib, Pressure: 4, SwapoutBytesPerSec: 1e9}, baseLimits(), nil, 3 * gib, 0, ""},
		{"floor breached", Reading{Available: 7 * gib, Pressure: 1}, baseLimits(), oneHeld, 2 * gib, 0, ReasonMemory},
		{"exactly at the floor is admitted", Reading{Available: 8 * gib, Pressure: 1}, baseLimits(), oneHeld, 2 * gib, 0, ""},
		{"in-flight reservations count against available",
			calm, baseLimits(),
			[]Held{{Group: "a", Cost: 20 * gib, Mem: 1 * gib}}, // 19 GiB still to come
			6 * gib, 0, ReasonMemory},
		{"a lease above its cost reserves nothing more",
			calm, baseLimits(), []Held{{Group: "a", Cost: 1 * gib, Mem: 5 * gib}}, 2 * gib, 0, ""},
		{"job ceiling", calm, Limits{FloorBytes: 0, MaxJobs: 1}, oneHeld, 1, 0, ReasonMaxJobs},
		{"no job ceiling when MaxJobs is negative", calm, Limits{MaxJobs: -1}, oneHeld, 1, 0, ""},
		{"pressure gate off at level 0", Reading{Available: 30 * gib, Pressure: 4}, Limits{MaxJobs: 6}, oneHeld, 1, 0, ""},
		{"active swapping", Reading{Available: 30 * gib, Pressure: 1, SwapoutBytesPerSec: 5 << 20}, baseLimits(), oneHeld, 1, 0, ReasonSwapout},
		{"swap gate off at 0", Reading{Available: 30 * gib, Pressure: 1, SwapoutBytesPerSec: 5 << 20}, Limits{MaxJobs: 6}, oneHeld, 1, 0, ""},
		{"need larger than available does not underflow",
			Reading{Available: 1 * gib, Pressure: 1}, baseLimits(), oneHeld, 3 * gib, 0, ReasonMemory},

		// --- warn is a brake, not a switch (2026-09-24) ---
		{"warn with room to spare still admits",
			Reading{Available: 30 * gib, Pressure: 2}, baseLimits(), oneHeld, 1, 0, ""},
		{"the production state of 2026-09-24: 13 GiB free, warn, no swapping",
			Reading{Available: 13 * gib, Pressure: 2}, baseLimits(),
			[]Held{{Group: "a", Cost: 2 * gib, Mem: 2 * gib}}, 3 * gib, 0, ""},
		{"warn shrinks the room: fits the plain floor, not the reinforced one",
			Reading{Available: 10 * gib, Pressure: 2}, baseLimits(), oneHeld, 2 * gib, 0, ReasonPressure},
		{"warn and short of the plain floor is plain memory",
			Reading{Available: 7 * gib, Pressure: 2}, baseLimits(), oneHeld, 2 * gib, 0, ReasonMemory},
		{"warn plus any swapping at all is thrashing",
			Reading{Available: 30 * gib, Pressure: 2, SwapoutBytesPerSec: 1 << 10}, baseLimits(), oneHeld, 1, 0, ReasonSwapout},
		{"critical stops everything",
			Reading{Available: 30 * gib, Pressure: 4}, baseLimits(), oneHeld, 1, 0, ReasonPressure},
		{"a trickle of swapping alone is not thrashing",
			Reading{Available: 30 * gib, Pressure: 1, SwapoutBytesPerSec: 1 << 10}, baseLimits(), oneHeld, 1, 0, ""},

		// --- compressor traffic (2026-10-21) ---
		{"compressor churning holds the queue",
			Reading{Available: 30 * gib, Pressure: 1, CompressBytesPerSec: 20 << 20}, baseLimits(), oneHeld, 1, 0, ReasonCompress},
		{"compressor traffic under its gate",
			Reading{Available: 30 * gib, Pressure: 1, CompressBytesPerSec: 8 << 20}, baseLimits(), oneHeld, 1, 0, ""},
		{"compress gate off at 0",
			Reading{Available: 30 * gib, Pressure: 1, CompressBytesPerSec: 1e9}, Limits{MaxJobs: 6}, oneHeld, 1, 0, ""},
		{"warn keeps the compressor gate at its threshold",
			Reading{Available: 30 * gib, Pressure: 2, CompressBytesPerSec: 8 << 20}, baseLimits(), oneHeld, 1, 0, ""},
		{"warn and compressor over its gate",
			Reading{Available: 30 * gib, Pressure: 2, CompressBytesPerSec: 20 << 20}, baseLimits(), oneHeld, 1, 0, ReasonCompress},
		{"swapout first when both fire",
			Reading{Available: 30 * gib, Pressure: 1, SwapoutBytesPerSec: 5 << 20, CompressBytesPerSec: 20 << 20}, baseLimits(), oneHeld, 1, 0, ReasonSwapout},
		{"starving past compressor traffic",
			Reading{Available: 30 * gib, Pressure: 1, CompressBytesPerSec: 20 << 20}, baseLimits(), oneHeld, 1, 4 * time.Minute, ""},
		{"nothing held admits through compressor traffic",
			Reading{Available: 30 * gib, Pressure: 1, CompressBytesPerSec: 1e9}, baseLimits(), nil, 1, 0, ""},

		// --- starvation: waiting long enough beats every gate but critical ---
		{"starving past the job ceiling",
			Reading{Available: 30 * gib, Pressure: 1}, baseLimits(),
			[]Held{{Group: "a"}, {Group: "b"}, {Group: "c"}, {Group: "d"}, {Group: "e"}, {Group: "f"}},
			1, 4 * time.Minute, ""},
		{"starving past warn and its reinforced floor",
			Reading{Available: 10 * gib, Pressure: 2}, baseLimits(), oneHeld, 2 * gib, 4 * time.Minute, ""},
		{"starving past active swapping",
			Reading{Available: 30 * gib, Pressure: 2, SwapoutBytesPerSec: 9 << 20}, baseLimits(), oneHeld, 1, 4 * time.Minute, ""},
		{"starving still owes the plain floor",
			Reading{Available: 7 * gib, Pressure: 1}, baseLimits(), oneHeld, 2 * gib, 4 * time.Minute, ReasonMemory},
		{"starving does not override critical",
			Reading{Available: 30 * gib, Pressure: 4}, baseLimits(), oneHeld, 1, 4 * time.Minute, ReasonPressure},
		{"not yet starving",
			Reading{Available: 30 * gib, Pressure: 1}, baseLimits(),
			[]Held{{Group: "a"}, {Group: "b"}, {Group: "c"}, {Group: "d"}, {Group: "e"}, {Group: "f"}},
			1, 30 * time.Second, ReasonMaxJobs},
		{"starvation guarantee off at 0",
			Reading{Available: 30 * gib, Pressure: 1}, Limits{MaxJobs: 1}, oneHeld, 1, time.Hour, ReasonMaxJobs},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := verdict(c.r, c.l, c.held, Waiter{Cost: c.need, Waited: c.waited}); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

// A class limit is about a resource memory cannot buy back — a second live
// Chromium fleet — so neither the starvation guarantee nor a calm machine may
// lift it. Only a lease of that class ending does.
func TestVerdictClassLimitSurvivesStarvation(t *testing.T) {
	l := baseLimits()
	l.ClassLimits = map[string]int{"vitest-browser": 1}
	held := []Held{{Group: "a", Class: "vitest-browser"}}
	w := Waiter{Class: "vitest-browser", Cost: gib, Waited: time.Hour}
	if got := verdict(Reading{Available: 60 * gib, Pressure: 1}, l, held, w); got != ReasonClassLimit {
		t.Fatalf("got %q want %q", got, ReasonClassLimit)
	}
}

func TestOutstanding(t *testing.T) {
	held := []Held{
		{Cost: 3 * gib, Mem: 1 * gib}, // 2 GiB to come
		{Cost: 1 * gib, Mem: 4 * gib}, // already above: 0
		{Cost: 2 * gib},               // not sampled yet: all of it
	}
	if got, want := outstanding(held), 4*gib; got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestNextToAdmit(t *testing.T) {
	cases := []struct {
		name  string
		queue []string
		held  map[string]int
		want  int
	}{
		{"empty queue", nil, nil, -1},
		{"plain FIFO when nobody holds anything", []string{"a", "b", "a"}, nil, 0},
		{"the group holding the least goes first", []string{"a", "a", "b"}, map[string]int{"a": 2}, 2},
		{"ties go to the oldest waiter", []string{"b", "a"}, map[string]int{"a": 1, "b": 1}, 0},
		{"FIFO inside the chosen group", []string{"a", "c", "c"}, map[string]int{"a": 3, "c": 1}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := make([]Waiter, len(c.queue))
			for i, g := range c.queue {
				q[i] = Waiter{Group: g}
			}
			if got := nextToAdmit(q, c.held, nil); got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
		})
	}
}

// TestNextToAdmitNoStarvation: a group that keeps queueing cannot hold back
// another group's waiter forever — each admission to the busy group raises
// its held count, which is exactly what hands the next turn to the other.
func TestNextToAdmitNoStarvation(t *testing.T) {
	queue := []Waiter{{Group: "busy"}, {Group: "busy"}, {Group: "busy"}, {Group: "quiet"}}
	held := map[string]int{}
	for turn := 0; turn < 3; turn++ {
		i := nextToAdmit(queue, held, nil)
		if queue[i].Group == "quiet" {
			return
		}
		held[queue[i].Group]++
		queue = append(queue[:i], queue[i+1:]...)
	}
	t.Fatal("the quiet group's waiter was never admitted within the busy group's first three")
}

// TestVerdictClassLimit covers the gate that replaces the repo-local lock
// a large monorepo used to need: at most one browser-mode run on the
// machine, whatever the memory says. The scarce resource there is a live
// Chromium per worker, not a number of gigabytes.
func TestVerdictClassLimit(t *testing.T) {
	calm := Reading{Available: 60 * gib, Pressure: 1}
	limits := func() Limits {
		l := baseLimits()
		l.ClassLimits = map[string]int{"vitest-browser": 1}
		return l
	}
	browser := Held{Group: "a", Class: "vitest-browser", Cost: 4 * gib, Mem: 4 * gib}
	tsc := Held{Group: "b", Class: "tsc", Cost: gib, Mem: gib}

	cases := []struct {
		name string
		held []Held
		want Reason
	}{
		{"a second browser run waits for the first", []Held{browser}, ReasonClassLimit},
		{"two of another class do not block a browser run", []Held{tsc, tsc}, ""},
		{"a browser run does not block another class", []Held{browser}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			class := "vitest-browser"
			if c.name == "a browser run does not block another class" {
				class = "tsc"
			}
			got := verdict(calm, limits(), c.held, Waiter{Group: "z", Class: class, Cost: gib})
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}

	t.Run("anti-deadlock still wins: nothing held admits a browser run", func(t *testing.T) {
		if got := verdict(Reading{Available: gib}, limits(), nil, Waiter{Class: "vitest-browser", Cost: 8 * gib}); got != "" {
			t.Fatalf("got %q want admit", got)
		}
	})

	t.Run("a class without a limit is never gated by one", func(t *testing.T) {
		held := []Held{{Group: "a", Class: "tsc"}, {Group: "a", Class: "tsc"}}
		if got := verdict(calm, limits(), held, Waiter{Class: "tsc", Cost: gib}); got != "" {
			t.Fatalf("got %q want admit", got)
		}
	})

	t.Run("a zero limit turns the gate off for that class", func(t *testing.T) {
		l := baseLimits()
		l.ClassLimits = map[string]int{"vitest-browser": 0}
		held := []Held{{Group: "a", Class: "vitest-browser"}}
		if got := verdict(calm, l, held, Waiter{Class: "vitest-browser", Cost: gib}); got != "" {
			t.Fatalf("got %q want admit", got)
		}
	})
}

func TestNextToAdmitSkipsBlocked(t *testing.T) {
	queue := []Waiter{
		{Group: "a", Class: "vitest-browser"},
		{Group: "b", Class: "tsc"},
	}
	if got := nextToAdmit(queue, map[string]int{}, []bool{true, false}); got != 1 {
		t.Fatalf("got %d, want the tsc behind the blocked browser run", got)
	}
	if got := nextToAdmit(queue, map[string]int{}, []bool{true, true}); got != -1 {
		t.Fatalf("got %d, want -1 when every waiter is blocked", got)
	}
}

func TestStarvingIndex(t *testing.T) {
	q := []Waiter{
		{Group: "a", Waited: 30 * time.Second},
		{Group: "b", Waited: 10 * time.Minute},
		{Group: "c", Waited: 4 * time.Minute},
	}
	cases := []struct {
		name    string
		queue   []Waiter
		blocked []bool
		max     time.Duration
		want    int
	}{
		{"nobody starving yet", q[:1], nil, 3 * time.Minute, -1},
		{"the longest wait wins, not the oldest place in line", q, nil, 3 * time.Minute, 1},
		{"a blocked waiter is skipped", q, []bool{false, true, false}, 3 * time.Minute, 2},
		{"guarantee off", q, nil, 0, -1},
		{"empty queue", nil, nil, time.Minute, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := starvingIndex(c.queue, c.blocked, c.max); got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestDefaultsCapTwoGoTestRuns(t *testing.T) {
	// Defaults() is a Config; verdict reads a Limits — the same projection
	// the controller makes under its lock.
	l := (&Controller{cfg: Defaults()}).limitsLocked()
	// The quantity under test is ClassLimits: the jobs ceiling is machine
	// dependent (min(cores, RAM/10 GiB), 1 on a 16 GB Mac or a CI runner)
	// and would turn the tsc assertion below into max_jobs there.
	l.MaxJobs = 0
	calm := Reading{Available: 30 * gib, Pressure: 1}
	held := []Held{{Group: "wt-a", Class: "go-test", Cost: 3 * gib, Mem: 3 * gib}}
	if got := verdict(calm, l, held, Waiter{Group: "wt-b", Class: "go-test", Cost: 3 * gib}); got != "" {
		t.Fatalf("second go-test must start (cap 2), got %q", got)
	}
	held = append(held, Held{Group: "wt-b", Class: "go-test", Cost: 3 * gib, Mem: 3 * gib})
	if got := verdict(calm, l, held, Waiter{Group: "wt-c", Class: "go-test", Cost: 3 * gib}); got != ReasonClassLimit {
		t.Fatalf("third go-test: reason %q, want %q", got, ReasonClassLimit)
	}
	if got := verdict(calm, l, held, Waiter{Group: "wt-b", Class: "tsc", Cost: gib}); got != "" {
		t.Fatalf("a tsc behind a held go-test must start, got %q", got)
	}
	if l.ClassLimits["go-test"] != 2 {
		t.Fatalf("go-test cap must be 2, got %d", l.ClassLimits["go-test"])
	}
	if l.ClassLimits["vitest-browser"] != 1 {
		t.Fatalf("vitest-browser cap must stay at 1, got %d", l.ClassLimits["vitest-browser"])
	}
}

// TestWindowRate: the one window both rates use (swap-out, compressor).
func TestWindowRate(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 16, 0, 0, 0, time.UTC)
	w := 10 * time.Second
	var h []counterSample
	var r float64
	if h, r = windowRate(h, t0, 0, w); r != 0 || len(h) != 0 {
		t.Fatalf("no counter: rate %v, %d samples kept", r, len(h)) // Linux, unread counter
	}
	if h, r = windowRate(h, t0, 1000, w); r != 0 {
		t.Fatalf("one sample: rate %v", r)
	}
	if h, r = windowRate(h, t0.Add(2*time.Second), 3000, w); r != 1000 {
		t.Fatalf("rate = %v, want 1000", r)
	}
	if h, r = windowRate(h, t0.Add(20*time.Second), 3000, w); r != 0 || len(h) != 2 {
		t.Fatalf("quiet past the window: rate %v, %d samples", r, len(h))
	}
	if _, r = windowRate(h, t0.Add(22*time.Second), 10, w); r != 0 {
		t.Fatalf("backwards counter: rate %v, want 0", r)
	}
}

func TestNextToAdmitFocusFirst(t *testing.T) {
	q := []Waiter{{Group: "a"}, {Group: "b", Focus: true}}
	if got := nextToAdmit(q, nil, nil); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
	// Whatever the held-lease counts of their groups.
	if got := nextToAdmit(q, map[string]int{"b": 3}, nil); got != 1 {
		t.Fatalf("held counts must not outrank focus: got %d want 1", got)
	}
}

func TestNextToAdmitFocusKeepsFairnessInside(t *testing.T) {
	// The non-focus head holds nothing and would win on fairness alone; the
	// focus pass restricts the pick to focus waiters, then fairness applies
	// inside: c (0 held) beats b (2 held).
	q := []Waiter{{Group: "a"}, {Group: "b", Focus: true}, {Group: "c", Focus: true}}
	if got := nextToAdmit(q, map[string]int{"a": 0, "b": 2, "c": 0}, nil); got != 2 {
		t.Fatalf("got %d want 2", got)
	}
}

func TestNextToAdmitFocusBlockedFallsBack(t *testing.T) {
	// A blocked focus head is skipped, and the focus waiter behind it still
	// beats the non-focus one (this case discriminates the focus pass).
	q := []Waiter{{Group: "a", Focus: true}, {Group: "b"}, {Group: "c", Focus: true}}
	if got := nextToAdmit(q, nil, []bool{true, false, false}); got != 2 {
		t.Fatalf("got %d want 2", got)
	}
	// No unblocked focus waiter left: the pick falls back to the others. This
	// half cannot discriminate by construction (without the focus pass the
	// blocked head is skipped too and the answer is the same); it pins the
	// fallback itself.
	q = []Waiter{{Group: "a", Focus: true}, {Group: "b"}}
	if got := nextToAdmit(q, nil, []bool{true, false}); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
}

func TestNextToAdmitNoFocusUnchanged(t *testing.T) {
	q := []Waiter{{Group: "a"}, {Group: "a"}, {Group: "b"}}
	if got := nextToAdmit(q, map[string]int{"a": 2}, nil); got != 2 {
		t.Fatalf("got %d want 2", got)
	}
	if got := nextToAdmit(q, nil, nil); got != 0 {
		t.Fatalf("got %d want 0", got)
	}
}

func TestDefaultsCapOneBuild(t *testing.T) {
	l := (&Controller{cfg: Defaults()}).limitsLocked()
	l.MaxJobs = 0 // machine dependent; the quantity under test is the class cap
	calm := Reading{Available: 60 * gib, Pressure: 1}
	held := []Held{{Group: "wt-a", Class: string(heavycmd.ClassBuild), Cost: 8 * gib, Mem: 8 * gib}}
	if got := verdict(calm, l, held, Waiter{Group: "wt-b", Class: string(heavycmd.ClassBuild), Cost: 8 * gib}); got != ReasonClassLimit {
		t.Fatalf("second build: reason %q, want %q", got, ReasonClassLimit)
	}
}

func TestVerdictCompressCalm(t *testing.T) {
	calmLimits := func() Limits {
		l := baseLimits()
		l.CompressCalmClasses = map[string]bool{"build": true, "vitest-browser": true}
		return l
	}
	with := func(f func(*Limits)) Limits { l := calmLimits(); f(&l); return l }
	oneHeld := []Held{{Group: "a", Cost: 2 * gib, Mem: 2 * gib}}
	loaded60 := Reading{Available: 30 * gib, Pressure: 1, CompressCalmBytesPerSec: 30 << 20}
	loaded10 := Reading{Available: 30 * gib, Pressure: 1, CompressBytesPerSec: 30 << 20}
	calm := Reading{Available: 30 * gib, Pressure: 1, CompressBytesPerSec: 8 << 20, CompressCalmBytesPerSec: 8 << 20}
	cases := []struct {
		name   string
		r      Reading
		l      Limits
		held   []Held
		class  string
		waited time.Duration
		want   Reason
	}{
		{"a build waits on the 60 s mean though 10 s is calm", loaded60, calmLimits(), oneHeld, "build", 0, ReasonCompressCalm},
		{"a build waits on the 10 s rate too", loaded10, calmLimits(), oneHeld, "build", 0, ReasonCompressCalm},
		{"vitest-browser is calm-gated as well", loaded60, calmLimits(), oneHeld, "vitest-browser", 0, ReasonCompressCalm},
		{"a build starts on durable calm", calm, calmLimits(), oneHeld, "build", 0, ""},
		{"a tsc ignores the 60 s mean", loaded60, calmLimits(), oneHeld, "tsc", 0, ""},
		{"a tsc keeps the plain 10 s gate", loaded10, calmLimits(), oneHeld, "tsc", 0, ReasonCompress},
		{"D1: a starving build still waits on the compressor", loaded10, calmLimits(), oneHeld, "build", time.Hour, ReasonCompressCalm},
		{"a starving tsc still passes the compressor", loaded10, calmLimits(), oneHeld, "tsc", 4 * time.Minute, ""},
		{"rollback: past CompressCalmMaxStarve a starving build is an ordinary waiter",
			loaded60, with(func(l *Limits) { l.CompressCalmMaxStarve = 5 * time.Minute }), oneHeld, "build", 6 * time.Minute, ""},
		{"released from the calm rule but not starving: the plain gate holds",
			loaded10, with(func(l *Limits) { l.CompressCalmMaxStarve = time.Minute }), oneHeld, "build", 2 * time.Minute, ReasonCompress},
		{"rule off (no classes): a build is gated like a tsc", loaded60, baseLimits(), oneHeld, "build", 0, ""},
		{"compressor gate off: no calm rule either", loaded10, with(func(l *Limits) { l.CompressBytesPerSec = 0 }), oneHeld, "build", 0, ""},
		{"D2 nothing held: the anti-deadlock rule still admits a build", loaded10, calmLimits(), nil, "build", 0, ""},
		{"swapout is named before the calm rule",
			Reading{Available: 30 * gib, Pressure: 1, SwapoutBytesPerSec: 5 << 20, CompressCalmBytesPerSec: 30 << 20}, calmLimits(), oneHeld, "build", 0, ReasonSwapout},
		{"durable calm still owes the floor", Reading{Available: 6 * gib, Pressure: 1}, calmLimits(), oneHeld, "build", 0, ReasonMemory},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := verdict(c.r, c.l, c.held, Waiter{Class: c.class, Cost: 1, Waited: c.waited}); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

// A command admitted under a generic envelope (`pigtail admit -- git push`)
// asks for its own lease with ParentLease set: the envelope's lease is not
// another command the machine is carrying, or the child would wait on its
// own parent.
func TestNestedUnderGenericAdmittedDespiteLowMemory(t *testing.T) {
	l := baseLimits()
	l.ClassLimits = map[string]int{"go-test": 1}
	held := []Held{{ID: "adm-1", Group: "a", Class: "generic", Cost: 2 * gib, Mem: 2 * gib}}
	low := Reading{Available: 1 * gib, Pressure: 1}
	if got := verdict(low, l, held, Waiter{Group: "a", Class: "go-test", Cost: gib, Parent: "adm-1"}); got != "" {
		t.Fatalf("the child of the only held lease must be admitted, got %q", got)
	}
	// An unknown parent is an ordinary request: the memory floor applies.
	if got := verdict(low, l, held, Waiter{Group: "a", Class: "go-test", Cost: gib, Parent: "adm-9"}); got != ReasonMemory {
		t.Fatalf("an unknown parent must be ignored, got %q", got)
	}
}

func TestNestedChildStillHitsClassLimit(t *testing.T) {
	l := baseLimits()
	l.ClassLimits = map[string]int{"go-test": 1}
	held := []Held{
		{ID: "adm-1", Group: "a", Class: "generic", Cost: gib, Mem: gib},
		{ID: "adm-2", Group: "b", Class: "go-test", Cost: gib, Mem: gib},
	}
	calm := Reading{Available: 60 * gib, Pressure: 1}
	if got := verdict(calm, l, held, Waiter{Group: "a", Class: "go-test", Cost: gib, Parent: "adm-1"}); got != ReasonClassLimit {
		t.Fatalf("got %q want %q", got, ReasonClassLimit)
	}
}

func TestVerdictDiskFloor(t *testing.T) {
	oneHeld := []Held{{Group: "a", Cost: 2 * gib, Mem: 2 * gib}}
	lim := func() Limits { l := baseLimits(); l.DiskFloorBytes = 20 * gib; return l }
	calm := func(disk uint64, known bool) Reading {
		return Reading{Available: 30 * gib, Pressure: 1, DiskFree: disk, DiskKnown: known}
	}
	cases := []struct {
		name   string
		r      Reading
		l      Limits
		held   []Held
		waited time.Duration
		want   Reason
	}{
		{"under the floor holds even an empty machine", calm(10*gib, true), lim(), nil, 0, ReasonDisk},
		{"under the floor holds with leases held", calm(10*gib, true), lim(), oneHeld, 0, ReasonDisk},
		{"a long wait does not buy the disk back", calm(10*gib, true), lim(), nil, time.Hour, ReasonDisk},
		{"past MaxStarve with leases held is still held", calm(10*gib, true), lim(), oneHeld, 10 * time.Minute, ReasonDisk},
		{"unknown disk never gates", calm(0, false), lim(), nil, 0, ""},
		{"unknown disk with a stale low value never gates", calm(1*gib, false), lim(), oneHeld, 0, ""},
		{"floor 0 turns the gate off", calm(1*gib, true), baseLimits(), nil, 0, ""},
		{"exactly at the floor is admitted", calm(20*gib, true), lim(), nil, 0, ""},
		{"above the floor is admitted", calm(100*gib, true), lim(), oneHeld, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verdict(tc.r, tc.l, tc.held, Waiter{Group: "b", Class: "tsc", Cost: 1 * gib, Waited: tc.waited})
			if got != tc.want {
				t.Fatalf("verdict = %q, want %q", got, tc.want)
			}
		})
	}
}
