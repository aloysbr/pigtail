package sessbudgettest_test

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget/sessbudgettest"
)

// Each named fixture meets the row of « Table d'admission » it stands for.
func TestFixturesMeetTheirRows(t *testing.T) {
	above := sessbudgettest.AboveFloor
	if d := sessbudget.Decide(above.Input()); d.Admitted || d.Row != 5 {
		t.Errorf("AboveFloor inside the gap: %+v, want a row 5 refusal", d)
	}
	if d := sessbudget.Decide(above.InputPastGap()); !d.Admitted || d.Row != 6 {
		t.Errorf("AboveFloor past the gap with a later sample: %+v, want row 6", d)
	}
	if m := above.Budget(); m != sessbudgettest.Ceiling {
		t.Errorf("AboveFloor budget %d, want the ceiling %d", m, sessbudgettest.Ceiling)
	}

	below := sessbudgettest.BelowFloor
	noSample := below.Input()
	noSample.SampleSinceAdmission = false
	for _, in := range []sessbudget.DecideInput{below.Input(), noSample} {
		if d := sessbudget.Decide(in); !d.Admitted || d.Row != 3 {
			t.Errorf("BelowFloor: %+v, want row 3", d)
		}
	}

	red := sessbudgettest.RedFull
	d := sessbudget.Decide(red.InputPastGap())
	if d.Admitted || d.Row != 4 || d.Reason != sessbudgettest.RedCause {
		t.Errorf("RedFull: %+v, want a row 4 refusal %q", d, sessbudgettest.RedCause)
	}
	if m := red.Budget(); m != sessbudgettest.Floor {
		t.Errorf("RedFull budget %d, want the floor %d", m, sessbudgettest.Floor)
	}
}

// WithBusy changes n only; a sample carries n busy rows.
func TestFixtureWithBusyAndSamples(t *testing.T) {
	f := sessbudgettest.RedFull.WithBusy(sessbudgettest.Floor - 1)
	if f.Input().Busy != sessbudgettest.Floor-1 || f.Budget() != sessbudgettest.Floor {
		t.Fatalf("WithBusy: %+v", f.Input())
	}
	clk := sessbudgettest.NewClock()
	s := f.Samples(clk)
	if len(s) == 0 || s[len(s)-1].Busy != sessbudgettest.Floor-1 {
		t.Fatalf("samples: %+v", s)
	}
	busy := 0
	for _, r := range s[len(s)-1].Sessions {
		if r.Status == "busy" {
			busy++
		}
	}
	if busy != sessbudgettest.Floor-1 {
		t.Fatalf("busy rows %d", busy)
	}
	before := clk.Now()
	next := f.AdvanceSample(clk)
	if !next.TS.After(before) || !clk.Now().Equal(next.TS) {
		t.Fatalf("AdvanceSample: sample at %v, clock %v (before %v)", next.TS, clk.Now(), before)
	}
}
