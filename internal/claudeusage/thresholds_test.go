package claudeusage

import (
	"math"
	"testing"
)

func fp(v float64) *float64 { return &v }

func TestResolveThresholds(t *testing.T) {
	cases := []struct {
		name    string
		sel, sw *float64
		want    Thresholds
	}{
		{"defaults", nil, nil, Thresholds{90, 98}},
		{"declared", fp(70), fp(78), Thresholds{70, 78}},
		{"zero select", fp(0), nil, Thresholds{90, 98}},
		{"over 100", fp(150), nil, Thresholds{90, 98}},
		{"NaN", fp(math.NaN()), nil, Thresholds{90, 98}},
		{"switch under select", fp(80), fp(70), Thresholds{80, 80}},
		{"switch under default select", nil, fp(85), Thresholds{90, 90}},
		{"select only", fp(70), nil, Thresholds{70, 98}},
	}
	for _, c := range cases {
		if got := ResolveThresholds(c.sel, c.sw); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func wv(p float64) *WindowView { return &WindowView{RawPercent: p} }

func TestThresholdsOverBoundaries(t *testing.T) {
	m := Thresholds{70, 78}
	d := ResolveThresholds(nil, nil)
	checks := []struct {
		name string
		got  bool
		want bool
	}{
		{"m select 69.9", m.OverSelect(wv(69.9)), false},
		{"m select 70", m.OverSelect(wv(70)), true},
		{"default select 89.9", d.OverSelect(wv(89.9)), false},
		{"default select 90", d.OverSelect(wv(90)), true},
		{"reset passed", d.OverSelect(&WindowView{RawPercent: 99, ResetPassed: true}), false},
		{"nil window", d.OverSelect(nil), false},
		{"default switch 97.9", d.OverSwitch(wv(97.9)), false},
		{"default switch 98", d.OverSwitch(wv(98)), true},
		{"m switch 77.9", m.OverSwitch(wv(77.9)), false},
		{"m switch 78", m.OverSwitch(wv(78)), true},
		{"switch reset passed", d.OverSwitch(&WindowView{RawPercent: 99, ResetPassed: true}), false},
		{"switch nil", d.OverSwitch(nil), false},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	if m.Ceiling() != 70 || d.Ceiling() != 90 || (Thresholds{}).Ceiling() != DefaultSelectPct {
		t.Errorf("Ceiling: m=%v d=%v zero=%v", m.Ceiling(), d.Ceiling(), Thresholds{}.Ceiling())
	}
}

func TestThresholdsZeroValueIsDefault(t *testing.T) {
	var z Thresholds
	if z.OverSelect(wv(89.9)) || !z.OverSelect(wv(90)) {
		t.Errorf("zero OverSelect: 89.9=%v 90=%v", z.OverSelect(wv(89.9)), z.OverSelect(wv(90)))
	}
	if z.OverSwitch(wv(97.9)) || !z.OverSwitch(wv(98)) {
		t.Errorf("zero OverSwitch: 97.9=%v 98=%v", z.OverSwitch(wv(97.9)), z.OverSwitch(wv(98)))
	}
	s := Thresholds{SelectPct: 70}
	if !s.OverSwitch(wv(98)) || s.OverSwitch(wv(97.9)) {
		t.Errorf("select-only OverSwitch: 98=%v 97.9=%v", s.OverSwitch(wv(98)), s.OverSwitch(wv(97.9)))
	}
}

func TestCellInputThresholds(t *testing.T) {
	c := CellInput{SelectPct: fp(70), SwitchPct: fp(78)}
	if got := c.Thresholds(); got != (Thresholds{70, 78}) {
		t.Fatalf("got %+v", got)
	}
	if got := (CellInput{}).Thresholds(); got != (Thresholds{90, 98}) {
		t.Fatalf("empty: got %+v", got)
	}
}

func TestResolveBudgetPct(t *testing.T) {
	cases := []struct {
		name string
		in   *float64
		want float64
	}{
		{"nil", nil, 70},
		{"declared", fp(60), 60},
		{"zero", fp(0), 70},
		{"negative", fp(-5), 70},
		{"over 100", fp(101), 70},
		{"NaN", fp(math.NaN()), 70},
		{"100", fp(100), 100},
	}
	for _, c := range cases {
		if got := ResolveBudgetPct(c.in); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAccountBudgetPctStrictest(t *testing.T) {
	snaps := []Snapshot{{Cell: CellInput{ID: "a", BudgetPct: fp(60)}}, {Cell: CellInput{ID: "b"}}}
	if got := AccountBudgetPct(snaps); got != 60 {
		t.Fatalf("got %v, want 60", got)
	}
	if got := AccountBudgetPct(nil); got != DefaultBudgetPct {
		t.Fatalf("no cell: got %v, want the default", got)
	}
}
