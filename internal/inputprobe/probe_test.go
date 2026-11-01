package inputprobe

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

const surf = "3A23D5DD-59EE-471D-9270-32F0FB06E099"

const ttyPath = "/dev/ttys037"

// att scripts one attempt: b is the rise of its baseline window, q the rise
// of the query's window, pending makes the write answer ErrOutputPending.
type att struct {
	b, q    uint64
	pending bool
}

func reactive(n int) []att {
	out := make([]att, n)
	for i := range out {
		out[i] = att{q: 9}
	}
	return out
}
func silent(n int) []att { return make([]att, n) }
func noisy(n int) []att {
	out := make([]att, n)
	for i := range out {
		out[i] = att{b: 9}
	}
	return out
}
func cat(xs ...[]att) []att {
	var out []att
	for _, x := range xs {
		out = append(out, x...)
	}
	return out
}

type write struct {
	tty string
	b   string
}

// fake is a scripted world. One attempt reads the counter at c0, c1 (the
// baseline window), then, if the baseline is calm, TTYOf, c1' (the query's
// window start) and the write, then c2.
type fake struct {
	atts       []att
	i, phase   int
	cur        uint64
	reads      int
	log        []string
	writes     []write
	bump       uint64 // TTYOf takes time: the counter rises by it meanwhile
	counterErr func(read int) error
	backwards  bool
	writeErr   error
	ttyOfErr   error
	ttyOf      func(attempt int) TTYInfo
}

func (f *fake) at() att {
	if f.i < len(f.atts) {
		return f.atts[f.i]
	}
	return att{}
}

func (f *fake) deps() Deps {
	return Deps{
		Counter: func(pid int) (uint64, error) {
			f.reads++
			if f.counterErr != nil {
				if err := f.counterErr(f.reads); err != nil {
					return 0, err
				}
			}
			if f.backwards && f.phase == 1 {
				f.log = append(f.log, "c1")
				return f.cur - 1, nil
			}
			a := f.at()
			switch f.phase {
			case 0:
				f.log = append(f.log, "c0")
				f.phase = 1
			case 1:
				f.log = append(f.log, "c1")
				f.cur += a.b
				if a.b >= DefaultParams().Threshold {
					f.i, f.phase = f.i+1, 0
				} else {
					f.phase = 2
				}
			case 2:
				f.log = append(f.log, "c1p")
				f.phase = 3
			case 3:
				f.log = append(f.log, "c2")
				f.cur += a.q
				f.i, f.phase = f.i+1, 0
			}
			return f.cur, nil
		},
		TTYOf: func(pid int) (TTYInfo, error) {
			f.log = append(f.log, "ttyof")
			f.cur += f.bump
			if f.ttyOfErr != nil {
				return TTYInfo{}, f.ttyOfErr
			}
			if f.ttyOf != nil {
				return f.ttyOf(f.i), nil
			}
			return TTYInfo{TTY: ttyPath, Foreground: true}, nil
		},
		Write: func(tty string, b []byte) error {
			f.log = append(f.log, "write")
			f.writes = append(f.writes, write{tty, string(b)})
			if f.at().pending {
				f.i, f.phase = f.i+1, 0
				return ErrOutputPending
			}
			return f.writeErr
		},
		Sleep: func(d time.Duration) {
			switch d {
			case DefaultParams().Window:
				f.log = append(f.log, "sleep")
			case DefaultParams().Gap:
				f.log = append(f.log, "gap")
			}
		},
	}
}

func target() Target { return Target{Surface: surf, PID: 42, SessionID: "sid", TTY: ttyPath} }

func run(f *fake) Result { return Probe(f.deps(), DefaultParams(), target()) }

func TestProbeNoisyBaselineWritesNothing(t *testing.T) {
	f := &fake{atts: cat(noisy(1), reactive(8))}
	r := run(f)
	if len(f.writes) != 8 || r.Verdict != VerdictAlive {
		t.Fatalf("%d writes, verdict %s: the noisy first attempt writes nothing and is not counted", len(f.writes), r.Verdict)
	}
	// the noisy attempt read the counter twice and never TTYOf.
	if want := []string{"c0", "sleep", "c1", "gap"}; !reflect.DeepEqual(f.log[:4], want) {
		t.Fatalf("log %v, want it to start %v", f.log[:4], want)
	}
	if f.log[4] != "c0" {
		t.Fatalf("log %v: TTYOf read after a noisy baseline", f.log)
	}
	if len(r.Deltas) != 8 {
		t.Fatalf("deltas %v: a noisy attempt is not counted", r.Deltas)
	}
}

func TestProbeThresholdIsInclusive(t *testing.T) {
	// q = 7 is reactive; b = 7 is noisy.
	f := &fake{atts: cat([]att{{b: 7}}, []att{{q: 7}}, []att{{q: 6}}, silent(10))}
	r := run(f)
	if r.Verdict != VerdictDead {
		t.Fatalf("verdict %s (%s)", r.Verdict, r.Why)
	}
	if want := []uint64{7, 6, 0, 0, 0, 0}; !reflect.DeepEqual(r.Deltas, want[:len(r.Deltas)]) || len(r.Deltas) < 2 {
		t.Fatalf("deltas %v: b=7 is not counted, q=7 is", r.Deltas)
	}
	// q=7 counted as reactive: 1 reactive then 5 silent (6 and four 0): dead after 6 counted.
	if len(r.Deltas) != 6 {
		t.Fatalf("deltas %v, want 6 counted trials (7 reactive, then 5 silent)", r.Deltas)
	}
}

func TestProbeReadsTTYAfterBaselineRightBeforeWrite(t *testing.T) {
	f := &fake{atts: reactive(8)}
	run(f)
	want := []string{"c0", "sleep", "c1", "ttyof", "c1p", "write", "sleep", "c2"}
	if !reflect.DeepEqual(f.log[:len(want)], want) {
		t.Fatalf("log\n %v\nwant it to start\n %v", f.log[:len(want)], want)
	}
}

func TestProbeQueryWindowStartsAfterTTYOf(t *testing.T) {
	// TTYOf's own reads raise the counter by 20: they must not count as the
	// query's reaction (q = c2 - c1', never c2 - c1).
	f := &fake{atts: silent(5), bump: 20}
	r := run(f)
	if r.Verdict != VerdictDead {
		t.Fatalf("verdict %s (%s): the ps noise counted as a reaction", r.Verdict, r.Why)
	}
	for _, d := range r.Deltas {
		if d != 0 {
			t.Fatalf("deltas %v, want all 0", r.Deltas)
		}
	}
}

func TestProbeOutputPendingIsNoisyTrial(t *testing.T) {
	f := &fake{atts: cat([]att{{pending: true}}, reactive(8))}
	r := run(f)
	if r.Verdict != VerdictAlive || len(r.Deltas) != 8 {
		t.Fatalf("verdict %s deltas %v: ErrOutputPending is a noisy trial, not Unknown, not counted", r.Verdict, r.Deltas)
	}
	if len(f.writes) != 9 {
		t.Fatalf("%d write calls, want the refused one then 8", len(f.writes))
	}
}

func TestProbeAliveAtEightReactive(t *testing.T) {
	f := &fake{atts: reactive(12)}
	r := run(f)
	if r.Verdict != VerdictAlive || len(f.writes) != 8 {
		t.Fatalf("verdict %s, %d writes: want Alive at the 8th reactive, no 9th", r.Verdict, len(f.writes))
	}
}

func TestProbeDeadAtFiveSilent(t *testing.T) {
	f := &fake{atts: silent(12)}
	r := run(f)
	if r.Verdict != VerdictDead || len(f.writes) != 5 {
		t.Fatalf("verdict %s, %d writes: want Dead after 5 silent counted trials, no 6th", r.Verdict, len(f.writes))
	}
}

func TestProbeDeadAtFiveReactive(t *testing.T) {
	f := &fake{atts: cat(reactive(5), silent(10))}
	r := run(f)
	if r.Verdict != VerdictDead || len(r.Deltas) != 10 || len(f.writes) != 10 {
		t.Fatalf("verdict %s, deltas %d, writes %d: want Dead at the 10th counted trial", r.Verdict, len(r.Deltas), len(f.writes))
	}
}

func TestProbeUnknownBetweenBands(t *testing.T) {
	f := &fake{atts: cat(reactive(6), silent(5))}
	r := run(f)
	if r.Verdict != VerdictUnknown || !strings.Contains(r.Why, "between bands") || len(f.writes) != 9 {
		t.Fatalf("a=6: verdict %s (%s), %d writes: want between bands at the 9th counted trial", r.Verdict, r.Why, len(f.writes))
	}
	// a = 7: f = 2 is not yet between the bands, the 3rd silent trial is.
	f = &fake{atts: cat(reactive(7), silent(5))}
	r = run(f)
	if r.Verdict != VerdictUnknown || !strings.Contains(r.Why, "between bands") || len(f.writes) != 10 {
		t.Fatalf("a=7: verdict %s (%s), %d writes: want between bands at the 10th counted trial", r.Verdict, r.Why, len(f.writes))
	}
}

func TestProbeUnknownWhenNotSettled(t *testing.T) {
	// 9 noisy baselines interleaved with 9 counted trials (5 reactive, 4
	// silent), the 18th attempt noisy: no rule 1-3 can hold before the 18th.
	var atts []att
	counted := cat(reactive(5), silent(4))
	for i := 0; i < 9; i++ {
		atts = append(atts, counted[i], att{b: 9})
	}
	f := &fake{atts: atts}
	r := run(f)
	if r.Verdict != VerdictUnknown || !strings.Contains(r.Why, "not settled") || len(f.writes) != 9 {
		t.Fatalf("verdict %s (%s), %d writes: want not settled after 18 attempts", r.Verdict, r.Why, len(f.writes))
	}
}

func TestProbeUnknownOnIncoherentParams(t *testing.T) {
	bad := []func(*Params){
		func(p *Params) { p.Counted = 5 },
		func(p *Params) { p.DeadAtMost = p.AliveAtLeast },
		func(p *Params) { p.AliveAtLeast = p.Counted + 1 },
		func(p *Params) { p.MaxAttempts = p.Counted - 1 },
		func(p *Params) { p.Threshold = 0 },
	}
	for i, mutate := range bad {
		f := &fake{atts: reactive(10)}
		p := DefaultParams()
		mutate(&p)
		if r := Probe(f.deps(), p, target()); r.Verdict != VerdictUnknown || len(f.writes) != 0 || len(f.log) != 0 {
			t.Fatalf("case %d: verdict %s, log %v: want unknown, nothing called", i, r.Verdict, f.log)
		}
	}
}

func TestDefaultParamsMatchPlan(t *testing.T) {
	want := Params{Window: 80 * time.Millisecond, Gap: 400 * time.Millisecond, Threshold: 7,
		Counted: 10, MaxAttempts: 18, AliveAtLeast: 8, DeadAtMost: 5}
	if got := DefaultParams(); got != want {
		t.Fatalf("DefaultParams = %+v, want %+v: a recalibration goes through the plan", got, want)
	}
}

// outcomeProbs: exact probabilities of the three verdicts by recurrence on
// (attempt, reactive, silent). A baseline is noisy with probability pNoise
// (not counted); a counted trial is reactive with probability pReact.
func outcomeProbs(pr Params, pNoise, pReact float64) (dead, alive, unknown float64) {
	type state struct{ a, f int }
	cur := map[state]float64{{}: 1}
	for n := 1; n <= pr.MaxAttempts; n++ {
		next := map[state]float64{}
		for s, pb := range cur {
			for _, o := range []struct {
				s state
				p float64
			}{
				{s, pNoise},
				{state{s.a + 1, s.f}, (1 - pNoise) * pReact},
				{state{s.a, s.f + 1}, (1 - pNoise) * (1 - pReact)},
			} {
				q := pb * o.p
				switch {
				case q == 0:
				case o.s.a >= pr.AliveAtLeast:
					alive += q
				case o.s.f >= pr.Counted-pr.DeadAtMost:
					dead += q
				case o.s.a > pr.DeadAtMost && o.s.f > pr.Counted-pr.AliveAtLeast:
					unknown += q
				case n == pr.MaxAttempts:
					unknown += q
				default:
					next[o.s] += q
				}
			}
		}
		cur = next
	}
	return dead, alive, unknown
}

// TestDefaultParamsMeetTargets: the plan's reliability targets, computed
// exactly on DefaultParams (a parameter changed without redoing the
// arithmetic fails here).
func TestDefaultParamsMeetTargets(t *testing.T) {
	pr := DefaultParams()
	// a dead input: a counted trial reacts by noise alone (r = p).
	if d, _, _ := outcomeProbs(pr, 0.35, 0.35); d < 0.85 {
		t.Errorf("P(Dead | dead input, p=0.35) = %.4f, want >= 0.85", d)
	}
	// a healthy claude at the lowest measured reaction rate r = 0.86.
	for _, p := range []float64{0.25, 0.40} {
		q, a, _ := outcomeProbs(pr, p, 0.86)
		if p == 0.25 && q > 0.0075 {
			t.Errorf("P(Dead | healthy, p=0.25, r=0.86) = %.5f, want <= 0.0075", q)
		}
		// false pill: 20 idle sessions x 12 probes an hour, two Dead in a
		// row before any Alive: 240 q^2 / (q + A) per hour.
		if rate := 240 * q * q / (q + a); rate > 0.02 || math.IsNaN(rate) {
			t.Errorf("false pill rate (p=%.2f) = %.4f/h, want <= 0.02", p, rate)
		}
	}
}

func TestProbeWritesOnlyQueryToTargetTTY(t *testing.T) {
	for _, atts := range [][]att{reactive(9), silent(6), cat(reactive(3), silent(9))} {
		f := &fake{atts: atts}
		run(f)
		if len(f.writes) == 0 {
			t.Fatal("nothing written")
		}
		for _, w := range f.writes {
			if w.tty != ttyPath || w.b != Query {
				t.Fatalf("wrote %q to %s: only Query to the target's tty", w.b, w.tty)
			}
		}
	}
	if Query != "\x1b[5n" {
		t.Fatal("Query changed")
	}
}

func TestProbeWritesNothingWhenTTYChanged(t *testing.T) {
	f := &fake{atts: reactive(10), ttyOf: func(attempt int) TTYInfo {
		if attempt >= 1 {
			return TTYInfo{TTY: "/dev/ttys099", Foreground: true}
		}
		return TTYInfo{TTY: ttyPath, Foreground: true}
	}}
	r := run(f)
	if r.Verdict != VerdictUnknown || len(f.writes) != 1 {
		t.Fatalf("verdict %s, %d writes: want unknown and no write from the 2nd trial", r.Verdict, len(f.writes))
	}
}

func TestProbeUnknownWhenClaudeLeavesForeground(t *testing.T) {
	f := &fake{atts: reactive(10), ttyOf: func(attempt int) TTYInfo {
		if attempt >= 1 {
			return TTYInfo{TTY: ttyPath, Foreground: false, Why: "stopped"}
		}
		return TTYInfo{TTY: ttyPath, Foreground: true}
	}}
	r := run(f)
	if r.Verdict != VerdictUnknown || len(f.writes) != 1 || !strings.Contains(r.Why, "stopped") {
		t.Fatalf("verdict %s why %q, %d writes", r.Verdict, r.Why, len(f.writes))
	}
}

func TestProbeUnknownOnTTYOfError(t *testing.T) {
	f := &fake{ttyOfErr: errors.New("ps failed")}
	if r := run(f); r.Verdict != VerdictUnknown || len(f.writes) != 0 {
		t.Fatalf("verdict %s, %d writes", r.Verdict, len(f.writes))
	}
}

func TestProbeUnknownWhenTargetHasNoTTY(t *testing.T) {
	f := &fake{}
	tg := target()
	tg.TTY = ""
	if r := Probe(f.deps(), DefaultParams(), tg); r.Verdict != VerdictUnknown || len(f.writes) != 0 {
		t.Fatalf("verdict %s, %d writes", r.Verdict, len(f.writes))
	}
	d := f.deps()
	d.TTYOf = nil
	if r := Probe(d, DefaultParams(), target()); r.Verdict != VerdictUnknown || len(f.writes) != 0 {
		t.Fatalf("nil TTYOf: verdict %s", r.Verdict)
	}
}

func TestProbeUnknownOnWriteError(t *testing.T) {
	f := &fake{atts: silent(10), writeErr: errors.New("EAGAIN")}
	if r := run(f); r.Verdict != VerdictUnknown || len(f.writes) != 1 {
		t.Fatalf("verdict %s, %d writes", r.Verdict, len(f.writes))
	}
}

func TestProbeUnknownOnCounterError(t *testing.T) {
	for _, read := range []int{1, 2, 3, 4} {
		f := &fake{atts: silent(10), counterErr: func(n int) error {
			if n == read {
				return errors.New("no such pid")
			}
			return nil
		}}
		if r := run(f); r.Verdict != VerdictUnknown {
			t.Fatalf("read %d: verdict %s, want unknown", read, r.Verdict)
		}
	}
}

func TestProbeUnknownOnCounterGoingBackwards(t *testing.T) {
	f := &fake{backwards: true, cur: 100, atts: silent(10)}
	if r := run(f); r.Verdict != VerdictUnknown {
		t.Fatalf("verdict %s, want unknown", r.Verdict)
	}
}

// TestProbeWritesOnlyToTargetPidTTY: every write goes to TTYOf(t.PID), which
// is t.TTY, with Query; a target whose t.TTY is not its pid's tty gets none.
func TestProbeWritesOnlyToTargetPidTTY(t *testing.T) {
	ttys := map[int]string{42: "/dev/ttys037", 43: "/dev/ttys099"}
	build := func() (*fake, Deps) {
		f := &fake{atts: reactive(10)}
		d := f.deps()
		d.TTYOf = func(pid int) (TTYInfo, error) { return TTYInfo{TTY: ttys[pid], Foreground: true}, nil }
		return f, d
	}
	f, d := build()
	if r := Probe(d, DefaultParams(), Target{Surface: surf, PID: 42, SessionID: "sid", TTY: ttys[42]}); r.Verdict != VerdictAlive {
		t.Fatalf("verdict %s (%s)", r.Verdict, r.Why)
	}
	if len(f.writes) == 0 {
		t.Fatal("nothing written")
	}
	for _, w := range f.writes {
		if w.tty != ttys[42] || w.b != Query {
			t.Fatalf("write %+v: want only Query on %s", w, ttys[42])
		}
	}
	// t.TTY is another pid's: the pid's own tty differs, nothing is written.
	f, d = build()
	r := Probe(d, DefaultParams(), Target{Surface: surf, PID: 42, SessionID: "sid", TTY: ttys[43]})
	if len(f.writes) != 0 || r.Verdict != VerdictUnknown {
		t.Fatalf("%d writes, verdict %s: a tty that is not the pid's must get none", len(f.writes), r.Verdict)
	}
}
