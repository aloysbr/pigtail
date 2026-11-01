package sessbudget_test

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget/sessbudgettest"
)

const (
	gib = uint64(1) << 30
	mib = float64(1 << 20)
)

var w0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func thresholds() sessbudget.Thresholds {
	return sessbudget.Thresholds{MinAvailable: 6 * gib, CompressBytesPerSec: 16 * mib}
}

func green(at time.Time) sessbudget.Reading {
	return sessbudget.Reading{At: at, Available: 10 * gib, CompressorRate: 1 * mib}
}

func TestWindowGreenOnlyWhenWholeWindowGreen(t *testing.T) {
	w := sessbudget.NewWindow(5 * time.Minute)
	for i := 0; i < 10; i++ {
		w.Add(green(w0.Add(time.Duration(i) * 30 * time.Second)))
	}
	if p := w.Judge(thresholds()); !p.Green {
		t.Fatalf("all green readings judged red: %+v", p)
	}
	// One swap write anywhere in the window turns it red, until it leaves.
	r := green(w0.Add(5 * time.Minute))
	r.Swapouts = 4096
	w.Add(r)
	w.Add(green(w0.Add(5*time.Minute + 30*time.Second)))
	if p := w.Judge(thresholds()); p.Green || p.Cause != sessbudget.ReasonSwap {
		t.Fatalf("a swap write in the window: %+v, want red %q", p, sessbudget.ReasonSwap)
	}
	w.Add(green(w0.Add(10*time.Minute + 31*time.Second)))
	if p := w.Judge(thresholds()); !p.Green {
		t.Fatalf("the swap write left the window, still red: %+v", p)
	}
}

func TestMinAvailableThresholdHonoured(t *testing.T) {
	w := sessbudget.NewWindow(5 * time.Minute)
	r := green(w0)
	r.Available = 5 * gib
	w.Add(r)
	if p := w.Judge(thresholds()); p.Green {
		t.Fatal("5 GiB available under a 6 GiB minimum judged green")
	}
	w = sessbudget.NewWindow(5 * time.Minute)
	r.Available = 7 * gib
	w.Add(r)
	if p := w.Judge(thresholds()); !p.Green {
		t.Fatalf("7 GiB available over a 6 GiB minimum judged red: %+v", p)
	}
}

// research.md: the compressor's per-sample rate swings between ~1 and
// ~17 MiB/s on an idle machine; the window judges its mean.
func TestNoisyRateAveragedOut(t *testing.T) {
	w := sessbudget.NewWindow(5 * time.Minute)
	for i := 0; i < 10; i++ {
		r := green(w0.Add(time.Duration(i) * 30 * time.Second))
		r.CompressorRate = 1 * mib
		if i%2 == 1 {
			r.CompressorRate = 17 * mib
		}
		w.Add(r)
	}
	if p := w.Judge(thresholds()); !p.Green {
		t.Fatalf("mean 9 MiB/s under 16 MiB/s judged red: %+v", p)
	}
}

func TestBudgetFloorWhenRedCeilingWhenGreen(t *testing.T) {
	cases := []struct {
		name string
		edit func(*sessbudget.Reading)
		want int
	}{
		{"compressor above threshold", func(r *sessbudget.Reading) { r.CompressorRate = 18 * mib }, 6},
		{"available below min", func(r *sessbudget.Reading) { r.Available = 4 * gib }, 6},
		{"swap write, compressor and memory green", func(r *sessbudget.Reading) { r.Swapouts = 1 }, 6},
		{"green", func(*sessbudget.Reading) {}, 20},
	}
	for _, c := range cases {
		w := sessbudget.NewWindow(5 * time.Minute)
		r := green(w0)
		c.edit(&r)
		w.Add(r)
		if got := sessbudget.Budget(w.Judge(thresholds()), 6, 20); got != c.want {
			t.Errorf("%s: budget %d, want %d", c.name, got, c.want)
		}
	}
}

func TestWindowPartialJudgedOnReadings(t *testing.T) {
	w := sessbudget.NewWindow(5 * time.Minute)
	for i := 0; i < 3; i++ {
		w.Add(green(w0.Add(time.Duration(i) * 30 * time.Second)))
	}
	if p := w.Judge(thresholds()); !p.Green {
		t.Fatalf("3 green readings in a 5 min window judged red: %+v", p)
	}
	w = sessbudget.NewWindow(5 * time.Minute)
	for i := 0; i < 3; i++ {
		r := green(w0.Add(time.Duration(i) * 30 * time.Second))
		if i == 1 {
			r.Available = 2 * gib
		}
		w.Add(r)
	}
	if p := w.Judge(thresholds()); p.Green {
		t.Fatal("3 readings, one red, judged green")
	}
}

func TestWindowZeroReadingsIsRed(t *testing.T) {
	p := sessbudget.NewWindow(5 * time.Minute).Judge(thresholds())
	if p.Green || p.Cause != sessbudget.ReasonPending {
		t.Fatalf("empty window: %+v, want red %q", p, sessbudget.ReasonPending)
	}
	if m := sessbudget.Budget(p, 6, 20); m != 6 {
		t.Fatalf("empty window budget %d, want the floor 6", m)
	}
}

// in is a DecideInput in a quiet state: a sample observed, the last admission
// long ago and sampled since.
func in(kind sessbudget.Kind, n, m int) sessbudget.DecideInput {
	return sessbudget.DecideInput{
		Kind: kind, Busy: n, Budget: m, Floor: 6, MinGap: 40 * time.Second,
		Observed: true, HasAdmission: true, SinceAdmission: 5 * time.Minute, SampleSinceAdmission: true,
	}
}

func TestDecideTable(t *testing.T) {
	const swap, mem, comp = "écriture en swap", "mémoire disponible 4,2 Go < 6 Go", "compresseur 18 Mo/s"
	red := func(d sessbudget.DecideInput, cause string) sessbudget.DecideInput {
		d.Budget, d.RedCause = d.Floor, cause
		return d
	}
	gap := func(d sessbudget.DecideInput, since time.Duration, sampled bool) sessbudget.DecideInput {
		d.SinceAdmission, d.SampleSinceAdmission = since, sampled
		return d
	}
	unobserved := func(d sessbudget.DecideInput) sessbudget.DecideInput {
		d.Observed, d.HasAdmission, d.SampleSinceAdmission = false, false, false
		return d
	}
	missed := func(d sessbudget.DecideInput, k int) sessbudget.DecideInput {
		d.MissedPeriods = k
		return d
	}
	cases := []struct {
		name     string
		in       sessbudget.DecideInput
		admitted bool
		over     bool
		reason   string
		row      int
	}{
		// Row 1: human.
		{"row1 human under budget", in(sessbudget.KindHuman, 3, 20), true, false, "", 1},
		{"row1 human at n>=m flagged budget plein", in(sessbudget.KindHuman, 20, 20), true, true, "budget plein", 1},
		{"row1 human red at n=floor flagged with the red cause", red(in(sessbudget.KindHuman, 6, 20), comp), true, true, comp, 1},
		{"row1 human inside the gap, not flagged", gap(in(sessbudget.KindHuman, 8, 20), 5*time.Second, false), true, false, "", 1},
		{"row1 human before any sample, not flagged", unobserved(in(sessbudget.KindHuman, 0, 6)), true, false, "", 1},
		// Row 2: no sample observed.
		{"row2 agent before any sample", unobserved(in(sessbudget.KindAgent, 0, 6)), false, false, "mesure en attente", 2},
		{"row2 wake before any sample", unobserved(in(sessbudget.KindWake, 0, 6)), false, false, "mesure en attente", 2},
		// Row 3: under the floor.
		{"row3 under floor", in(sessbudget.KindAgent, 5, 20), true, false, "", 3},
		{"row3 under floor inside the gap, no sample since", gap(in(sessbudget.KindAgent, 5, 20), 5*time.Second, false), true, false, "", 3},
		// Row 4: full.
		{"row4 budget plein", in(sessbudget.KindAgent, 20, 20), false, false, "budget plein", 4},
		{"row4 red swap", red(in(sessbudget.KindAgent, 6, 20), swap), false, false, swap, 4},
		{"row4 red memory", red(in(sessbudget.KindAgent, 7, 20), mem), false, false, mem, 4},
		{"row4 red compressor", red(in(sessbudget.KindWake, 6, 20), comp), false, false, comp, 4},
		// Row 5: gap and freshness.
		{"row5 floor inside the gap refused", gap(in(sessbudget.KindAgent, 6, 20), 5*time.Second, false), false, false, "démarrage progressif, 35 s", 5},
		{"row5 gap wins over no sample", gap(in(sessbudget.KindAgent, 8, 20), 10*time.Second, false), false, false, "démarrage progressif, 30 s", 5},
		{"row5 gap with a sample since", gap(in(sessbudget.KindAgent, 8, 20), 10*time.Second, true), false, false, "démarrage progressif, 30 s", 5},
		{"row5 gap rounds seconds up", gap(in(sessbudget.KindAgent, 8, 20), 10*time.Second+300*time.Millisecond, true), false, false, "démarrage progressif, 30 s", 5},
		{"row5 past the gap, no sample since", gap(in(sessbudget.KindAgent, 8, 20), 50*time.Second, false), false, false, "mesure en attente", 5},
		{"row5 no sample waived after 3 missed periods", missed(gap(in(sessbudget.KindAgent, 8, 20), 95*time.Second, false), 3), true, false, "", 6},
		{"row5 waiver never lifts the gap", missed(gap(in(sessbudget.KindAgent, 8, 20), 10*time.Second, false), 3), false, false, "démarrage progressif, 30 s", 5},
		// Row 6.
		{"row6 admitted", in(sessbudget.KindAgent, 8, 20), true, false, "", 6},
		{"row6 first admission ever", func() sessbudget.DecideInput {
			d := in(sessbudget.KindAgent, 8, 20)
			d.HasAdmission, d.SampleSinceAdmission = false, false
			return d
		}(), true, false, "", 6},
	}
	for _, c := range cases {
		got := sessbudget.Decide(c.in)
		if got.Admitted != c.admitted || got.OverBudget != c.over || got.Reason != c.reason || got.Row != c.row {
			t.Errorf("%s: got %+v, want admitted=%v over=%v reason=%q row=%d", c.name, got, c.admitted, c.over, c.reason, c.row)
		}
		if c.in.Kind == sessbudget.KindAgent {
			w := c.in
			w.Kind = sessbudget.KindWake
			if gw := sessbudget.Decide(w); gw != got {
				t.Errorf("%s: wake %+v differs from agent %+v", c.name, gw, got)
			}
		}
	}
	// The waiver is reported, for the controller to log it.
	if d := sessbudget.Decide(missed(gap(in(sessbudget.KindAgent, 8, 20), 95*time.Second, false), 3)); !d.FreshWaived {
		t.Error("waived fresh-sample condition not reported")
	}
}

// Red-cause precedence: swap > low memory > compressor.
func TestDecideRedCausePrecedence(t *testing.T) {
	th := thresholds()
	judge := func(edit func(*sessbudget.Reading)) string {
		w := sessbudget.NewWindow(5 * time.Minute)
		r := green(w0)
		edit(&r)
		w.Add(r)
		return w.Judge(th).Cause
	}
	all := judge(func(r *sessbudget.Reading) { r.Swapouts, r.Available, r.CompressorRate = 1, 4*gib+gib/5, 18*mib })
	if all != "écriture en swap" {
		t.Errorf("swap + memory + compressor: %q", all)
	}
	two := judge(func(r *sessbudget.Reading) { r.Available, r.CompressorRate = 4*gib+gib/5, 18*mib })
	if two != "mémoire disponible 4,2 Go < 6 Go" {
		t.Errorf("memory + compressor: %q", two)
	}
	one := judge(func(r *sessbudget.Reading) { r.CompressorRate = 18 * mib })
	if one != "compresseur 18 Mo/s" {
		t.Errorf("compressor alone: %q", one)
	}
}

func TestDecideReasonsGolden(t *testing.T) {
	got := []string{
		sessbudget.ReasonFull,
		sessbudget.ReasonLowMemory(4*gib+gib/5, 6*gib),
		sessbudget.ReasonSwap,
		sessbudget.ReasonCompressor(18 * mib),
		sessbudget.ReasonPending,
		sessbudget.ReasonGap(40 * time.Second),
		sessbudget.GateLine(sessbudget.Decision{Admitted: true}),
		sessbudget.GateLine(sessbudget.Decision{Reason: "budget plein"}),
	}
	want := []string{
		"budget plein",
		"mémoire disponible 4,2 Go < 6 Go",
		"écriture en swap",
		"compresseur 18 Mo/s",
		"mesure en attente",
		"démarrage progressif, 40 s",
		"porte ouverte",
		"porte fermée (budget plein)",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reason %d: %q, want %q", i, got[i], want[i])
		}
	}
}

// Row 5 on the fake clock: the second slot waits for the gap AND a sample
// taken after the first admission.
func TestProgressiveDelay(t *testing.T) {
	f := sessbudgettest.AboveFloor
	clk := sessbudgettest.NewClock()
	last := f.Samples(clk)[0].TS
	first := clk.Now()
	if d := sessbudget.Decide(f.InputAt(time.Time{}, last, first)); !d.Admitted {
		t.Fatalf("first slot refused: %+v", d)
	}
	clk.Advance(10 * time.Second)
	d := sessbudget.Decide(f.InputAt(first, last, clk.Now()))
	if d.Admitted || d.Reason != "démarrage progressif, 30 s" {
		t.Fatalf("second slot inside the gap: %+v", d)
	}
	clk.Advance(sessbudgettest.MinGap) // past the gap, no sample since
	if d := sessbudget.Decide(f.InputAt(first, last, clk.Now())); d.Admitted || d.Reason != sessbudget.ReasonPending {
		t.Fatalf("past the gap without a sample: %+v, want refused %q", d, sessbudget.ReasonPending)
	}
	last = f.AdvanceSample(clk).TS
	if d := sessbudget.Decide(f.InputAt(first, last, clk.Now())); !d.Admitted || d.Row != 6 {
		t.Fatalf("past the gap with a sample since: %+v, want row 6", d)
	}
}
