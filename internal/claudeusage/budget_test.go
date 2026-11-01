package claudeusage

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

// budgetReading is a reading of the given age whose 7-day window is at used
// and resets at pickNow+reset.
func budgetReading(age time.Duration, used float64, reset time.Duration) *Reading {
	t := pickNow.Add(reset)
	return &Reading{FetchedAt: pickNow.Add(-age),
		FiveHour: &WindowView{},
		SevenDay: &WindowView{RawPercent: used, ResetsAt: &t, ResetPassed: !t.After(pickNow)}}
}

const midWeek = 84 * time.Hour // 3.5 days to the reset: half the window elapsed

func TestBudgetFor(t *testing.T) {
	t.Run("on the curve is within", func(t *testing.T) {
		v := BudgetFor(budgetReading(time.Minute, 40, midWeek), 70, pickNow)
		if v.State != BudgetWithin || v.Allowed != 40 || v.Used != 40 || v.Left != 30 || v.BudgetPct != 70 {
			t.Fatalf("%+v", v)
		}
		if v.FreesAt != nil || v.Why != "" {
			t.Fatalf("within carries no release: %+v", v)
		}
	})
	t.Run("past the curve is over with its release", func(t *testing.T) {
		v := BudgetFor(budgetReading(time.Minute, 40.1, midWeek), 70, pickNow)
		if v.State != BudgetOver || v.FreesAt == nil {
			t.Fatalf("%+v", v)
		}
		// f* = (40.1-5)/70: the curve catches the used percent there.
		fStar := (40.1 - FastPoints) / 70
		want := pickNow.Add(midWeek).Add(-time.Duration((1 - fStar) * float64(7*24*time.Hour)))
		if d := v.FreesAt.Sub(want); d < -time.Second || d > time.Second {
			t.Fatalf("FreesAt %v, want %v", v.FreesAt, want)
		}
		if !v.FreesAt.After(pickNow) {
			t.Fatalf("release in the past: %v", v.FreesAt)
		}
	})
	t.Run("the tolerance holds at the start of the window", func(t *testing.T) {
		v := BudgetFor(budgetReading(time.Minute, 4, 7*24*time.Hour), 70, pickNow)
		if v.State != BudgetWithin || v.Allowed != FastPoints {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("a release past the reset is bounded by it", func(t *testing.T) {
		v := BudgetFor(budgetReading(time.Minute, 89, midWeek), 70, pickNow)
		if v.State != BudgetOver || v.FreesAt == nil || !v.FreesAt.Equal(pickNow.Add(midWeek)) {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("a passed reset with a fresh reading is within, nothing used", func(t *testing.T) {
		v := BudgetFor(budgetReading(time.Minute, 95, -time.Hour), 70, pickNow)
		if v.State != BudgetWithin || v.Used != 0 || v.Left != 70 {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("no reading is unmeasured", func(t *testing.T) {
		v := BudgetFor(nil, 70, pickNow)
		if v.State != BudgetUnmeasured || !strings.HasPrefix(v.Why, "no reading") || v.BudgetPct != 70 {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("no 7-day window is unmeasured", func(t *testing.T) {
		v := BudgetFor(&Reading{FetchedAt: pickNow}, 70, pickNow)
		if v.State != BudgetUnmeasured || v.Why == "" {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("a reading dated in the future is unmeasured, past a minute", func(t *testing.T) {
		rd := budgetReading(-time.Minute-time.Second, 10, midWeek)
		if v := BudgetFor(rd, 70, pickNow); v.State != BudgetUnmeasured || v.Why != "reading dated in the future" {
			t.Fatalf("%+v", v)
		}
		rd = budgetReading(-time.Minute, 10, midWeek)
		if v := BudgetFor(rd, 70, pickNow); v.State != BudgetWithin {
			t.Fatalf("one minute ahead stays measured: %+v", v)
		}
	})
	t.Run("no 5-hour window is unmeasured, whatever the 7-day says", func(t *testing.T) {
		for _, used := range []float64{10, 89} { // within, then over the curve
			rd := budgetReading(time.Minute, used, midWeek)
			rd.FiveHour = nil
			v := BudgetFor(rd, 70, pickNow)
			if v.State != BudgetUnmeasured || v.Why != "no 5-hour window in the reading" || v.FreesAt != nil {
				t.Fatalf("used %v: %+v", used, v)
			}
		}
	})
	t.Run("a 5-hour window not started is measured", func(t *testing.T) {
		rd := budgetReading(time.Minute, 10, midWeek)
		rd.FiveHour = &WindowView{RawPercent: 0} // percent read, null reset
		if v := BudgetFor(rd, 70, pickNow); v.State != BudgetWithin {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("no reset time is unmeasured, never within", func(t *testing.T) {
		rd := &Reading{FetchedAt: pickNow, SevenDay: &WindowView{RawPercent: 1}}
		v := BudgetFor(rd, 70, pickNow)
		if v.State != BudgetUnmeasured || v.Why != "no reset time" {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("a reading over 30 min is unmeasured, 30 min sharp is measured", func(t *testing.T) {
		v := BudgetFor(budgetReading(31*time.Minute, 1, midWeek), 70, pickNow)
		if v.State != BudgetUnmeasured || v.Why != "reading older than 30m" {
			t.Fatalf("%+v", v)
		}
		if v := BudgetFor(budgetReading(PickFreshFor, 1, midWeek), 70, pickNow); v.State != BudgetWithin {
			t.Fatalf("30 min sharp: %+v", v)
		}
	})
	t.Run("left is never negative", func(t *testing.T) {
		if v := BudgetFor(budgetReading(time.Minute, 85, midWeek), 70, pickNow); v.Left != 0 {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("an invalid budget resolves to the default", func(t *testing.T) {
		for _, b := range []float64{0, -3, math.NaN()} {
			v := BudgetFor(budgetReading(time.Minute, 40, midWeek), b, pickNow)
			if v.BudgetPct != DefaultBudgetPct || v.Allowed != 40 || v.State != BudgetWithin {
				t.Fatalf("budget %v: %+v", b, v)
			}
		}
	})
}

func TestBuildAccountCarriesBudget(t *testing.T) {
	b := 60.0
	c := rankedCell("mp", "perso", "m", 0)
	c.BudgetPct = &b
	files := map[string][]byte{fileOf(c): usageFile(uuidM, time.Minute, &win{10, 2 * hour}, &win{50, midWeek})}
	r := Build(snapshots([]CellInput{c}, files), scope.Master(), pickNow)
	got := r.Accounts[0].Budget
	if got.State != BudgetOver || got.BudgetPct != 60 || got.Used != 50 {
		t.Fatalf("%+v", got)
	}
}
