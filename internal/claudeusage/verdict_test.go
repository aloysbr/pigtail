package claudeusage

import (
	"math"
	"testing"
	"time"
)

var simNow = time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)

// at parses "dd/mm hh:mm" (UTC, 2026), like the mockup.
func at(t *testing.T, s string) *time.Time {
	t.Helper()
	if s == "" {
		return nil
	}
	v, err := time.Parse("02/01 15:04 2006", s+" 2026")
	if err != nil {
		t.Fatal(err)
	}
	return &v
}

func sacct(t *testing.T, label string, u7 float64, r7 string, u5 float64, r5 string, rate float64) SimAccount {
	return SimAccount{Label: label, SevenDay: u7, SevenDayReset: at(t, r7), FiveHour: u5,
		FiveHourReset: at(t, r5), Rate: Rates{SevenDay: rate}}
}

func wantTime(t *testing.T, name string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func normal(t *testing.T) []SimAccount {
	return []SimAccount{
		sacct(t, "m", 20, "25/10 17:59", 16, "20/10 23:19", 0.4),
		sacct(t, "i", 64, "25/10 08:59", 69, "20/10 23:29", 0.9), // 1.1 at the 95 % ceiling; 0.9 keeps the week ok at 90
		sacct(t, "j", 2, "27/10 02:59", 0, "", 0.2),
	}
}

func TestSimulateMockupNormal(t *testing.T) {
	v := Simulate(normal(t), simNow)
	if v.State != VerdictOK || math.Abs(v.MinReserveShare-0.247) > 1e-3 {
		t.Fatalf("verdict = %+v", v)
	}
	if v.NextRenewalLabel != "i" {
		t.Fatalf("renewal = %q", v.NextRenewalLabel)
	}
	wantTime(t, "renewal at", v.NextRenewalAt, *at(t, "25/10 08:59"))
}

func TestSimulateMockupTooFast(t *testing.T) {
	v := Simulate([]SimAccount{
		sacct(t, "m", 55, "25/10 17:59", 30, "20/10 23:19", 0.7),
		sacct(t, "i", 78, "23/10 08:59", 50, "20/10 23:29", 1.6),
		sacct(t, "j", 40, "27/10 02:59", 12, "21/10 00:40", 0.4),
	}, simNow)
	if v.State != VerdictLater || v.FreesLabel != "i" {
		t.Fatalf("verdict = %+v", v)
	}
	wantTime(t, "blocked from", v.BlockedFrom, time.Date(2026, 10, 22, 9, 10, 0, 0, time.UTC)) // 14:40 at 95
	wantTime(t, "frees", v.FreesAt, time.Date(2026, 10, 23, 8, 59, 0, 0, time.UTC))
}

func TestSimulateMockupWeekStart(t *testing.T) {
	v := Simulate([]SimAccount{
		sacct(t, "m", 9, "27/10 09:00", 30, "20/10 23:40", 9.0/12),
		sacct(t, "i", 30, "26/10 20:00", 45, "20/10 22:10", 30.0/25),
		sacct(t, "j", 3, "27/10 14:00", 10, "21/10 01:30", 3.0/7),
	}, simNow)
	if v.State != VerdictLater || v.FreesLabel != "i" {
		t.Fatalf("verdict = %+v", v)
	}
	wantTime(t, "blocked from", v.BlockedFrom, time.Date(2026, 10, 24, 21, 10, 0, 0, time.UTC)) // 25/10 03:20 at 95
	wantTime(t, "frees", v.FreesAt, time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC))
}

func TestSimulateMockupBlockedNow(t *testing.T) {
	a := normal(t)
	a[0].FiveHour, a[1].FiveHour, a[2].FiveHour = 92, 95, 91
	a[2].FiveHourReset = at(t, "20/10 21:52")
	v := Simulate(a, simNow)
	if v.State != VerdictNow || v.FreesLabel != "j" {
		t.Fatalf("verdict = %+v", v)
	}
	wantTime(t, "frees", v.FreesAt, time.Date(2026, 10, 20, 21, 52, 0, 0, time.UTC))
}

func TestSimulateNextRenewalPassedReset(t *testing.T) {
	past := simNow.Add(-10 * time.Minute)
	a := SimAccount{Label: "m", SevenDay: 0, SevenDayReset: &past}
	v := Simulate([]SimAccount{a}, simNow)
	wantTime(t, "renewal", v.NextRenewalAt, past.Add(7*24*time.Hour))
	near := simNow.Add(24 * time.Hour)
	b := SimAccount{Label: "i", SevenDay: 10, SevenDayReset: &near}
	v = Simulate([]SimAccount{a, b}, simNow)
	if v.NextRenewalLabel != "i" {
		t.Fatalf("renewal = %+v", v)
	}
}

func TestSimulateTight(t *testing.T) {
	r := simNow.Add(2 * time.Hour)
	v := Simulate([]SimAccount{{Label: "m", SevenDay: 85, SevenDayReset: &r}}, simNow) // 5 points under the 90 ceiling
	if v.State != VerdictTight {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestSimulateNoAccount(t *testing.T) {
	if v := Simulate(nil, simNow); v.State != VerdictNone {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestSimulateFiveHourConsumption(t *testing.T) {
	r7 := simNow.Add(5 * 24 * time.Hour)
	r5 := simNow.Add(4 * time.Hour)
	v := Simulate([]SimAccount{{Label: "m", SevenDay: 10, SevenDayReset: &r7, FiveHour: 80, FiveHourReset: &r5,
		Rate: Rates{FiveHour: 6}}}, simNow)
	if v.State != VerdictLater {
		t.Fatalf("verdict = %+v", v)
	}
	wantTime(t, "blocked from", v.BlockedFrom, simNow.Add(100*time.Minute))
	wantTime(t, "frees", v.FreesAt, r5)
}

func TestSimulateFiveHourWindowStartsOnUse(t *testing.T) {
	r7 := simNow.Add(5 * 24 * time.Hour)
	// u5 0 with no open window; 30 %/h = 5.0 per step. The first use opens the
	// window at t0+5 h; 90 is reached after 18 steps, so blocked at +180 min
	// until that window renews.
	v := Simulate([]SimAccount{{Label: "m", SevenDay: 0, SevenDayReset: &r7, Rate: Rates{FiveHour: 30}}}, simNow)
	if v.State != VerdictLater {
		t.Fatalf("verdict = %+v", v)
	}
	wantTime(t, "blocked from", v.BlockedFrom, simNow.Add(180*time.Minute))
	wantTime(t, "frees", v.FreesAt, simNow.Add(5*time.Hour))
}

func TestSimulateScopedLimit(t *testing.T) {
	r7 := simNow.Add(5 * 24 * time.Hour)
	rs := simNow.Add(3 * 24 * time.Hour)
	v := Simulate([]SimAccount{{Label: "m", SevenDay: 10, SevenDayReset: &r7,
		Scoped: []SimScoped{{Model: "Fable", Percent: 85, Reset: &rs}}, // 90 at the 95 ceiling
		Rate:   Rates{Scoped: map[string]float64{"Fable": 6}}}}, simNow)
	if v.State != VerdictLater {
		t.Fatalf("verdict = %+v", v)
	}
	wantTime(t, "blocked from", v.BlockedFrom, simNow.Add(50*time.Minute))
	wantTime(t, "frees", v.FreesAt, rs)
}

// A nil weekly reset sorts last: the account with a reset is served first.
func TestSimulateUnknownWeeklyResetLast(t *testing.T) {
	rb := simNow.Add(2 * time.Hour)
	a := SimAccount{Label: "a", Rate: Rates{SevenDay: 6}}
	b := SimAccount{Label: "b", SevenDayReset: &rb, Rate: Rates{SevenDay: 6}}
	v := Simulate([]SimAccount{a, b}, simNow)
	if v.State != VerdictLater {
		t.Fatalf("verdict = %+v", v)
	}
	wantTime(t, "blocked from", v.BlockedFrom, simNow.Add(1020*time.Minute)) // 1080 at 95
	if v.FreesLabel != "b" {
		t.Fatalf("frees = %q, a has no known reset", v.FreesLabel)
	}
}

func TestSimulateUnknownFreeTime(t *testing.T) {
	v := Simulate([]SimAccount{{Label: "m", FiveHour: 92}}, simNow)
	if v.State != VerdictNow || v.FreesAt != nil {
		t.Fatalf("verdict = %+v", v)
	}
}

// The verdict blocks an account at its own ceiling (SimAccount.Ceiling, 0 =
// the default).
func TestSimulateUsesAccountCeiling(t *testing.T) {
	r := simNow.Add(3 * 24 * time.Hour)
	if v := Simulate([]SimAccount{{Label: "m", SevenDay: 71, SevenDayReset: &r, Ceiling: 70}}, simNow); v.State != VerdictNow {
		t.Fatalf("ceiling 70 at 71: state %s, want now", v.State)
	}
	if v := Simulate([]SimAccount{{Label: "m", SevenDay: 71, SevenDayReset: &r}}, simNow); v.State != VerdictOK {
		t.Fatalf("default ceiling at 71: state %s, want ok", v.State)
	}
}
