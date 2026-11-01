package claudeusage

import (
	"math"
	"testing"
	"time"
)

var rhythmNow = time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)

func tp(t time.Time) *time.Time { return &t }

// smp is a sample at now+off with a distinct FetchedAt (one per call).
var smpSeq int

func smp(off time.Duration, d7 float64) Sample {
	smpSeq++
	at := rhythmNow.Add(off)
	return Sample{At: at, AccountUUID: "u", FetchedAt: at.Add(-time.Duration(smpSeq) * time.Second),
		SevenDay: &SampleWindow{Percent: d7}}
}

func curReading(raw float64, reset time.Time) *Reading {
	return &Reading{FetchedAt: rhythmNow, SevenDay: &WindowView{RawPercent: raw, Percent: int(raw), ResetsAt: tp(reset)}}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRatesMeanBeforeTwoDays(t *testing.T) {
	reset := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	s := []Sample{smp(-47*time.Hour, 5), smp(0, 9)}
	r := RatesFor(s, curReading(9, reset), rhythmNow)
	if r.FromHistory || r.SevenDay != 9.0/12 {
		t.Fatalf("rates = %+v, want provisional 9/12", r)
	}
}

func TestRatesHistoryAfterTwoDays(t *testing.T) {
	var s []Sample
	for h := -50; h <= 0; h++ {
		s = append(s, smp(time.Duration(h)*time.Hour, 10+0.5*float64(h+50)))
	}
	r := RatesFor(s, curReading(35, rhythmNow.Add(24*time.Hour)), rhythmNow)
	if !r.FromHistory || !near(r.SevenDay, 0.5) {
		t.Fatalf("rates = %+v, want 0.5 from history", r)
	}
}

func TestRatesCountAfterResetDrop(t *testing.T) {
	s := []Sample{smp(-50*time.Hour, 90), smp(-24*time.Hour, 90), smp(-20*time.Hour, 92), smp(-10*time.Hour, 1), smp(0, 3)}
	r := RatesFor(s, curReading(3, rhythmNow.Add(48*time.Hour)), rhythmNow)
	if !r.FromHistory || !near(r.SevenDay, 5.0/24) {
		t.Fatalf("rates = %+v, want 5/24", r)
	}
}

func TestRatesDedupFetchedAt(t *testing.T) {
	a := smp(-50*time.Hour, 10)
	var s []Sample
	s = append(s, a)
	for h := -49; h <= 0; h++ { // same reading re-recorded each pass
		d := a
		d.At = rhythmNow.Add(time.Duration(h) * time.Hour)
		s = append(s, d)
	}
	b := smp(0, 22)
	b.FetchedAt = rhythmNow.Add(-time.Minute)
	s = append(s, b)
	r := RatesFor(s, curReading(22, rhythmNow.Add(48*time.Hour)), rhythmNow)
	// deduped: 10 at -50h (kept first), 22 at 0 -> anchor is the -50h one? no: last At <= now-24h is -50h
	// so the series is 10 -> 22 over 50h.
	if !r.FromHistory || !near(r.SevenDay, 12.0/50) {
		t.Fatalf("rates = %+v, want 12/50", r)
	}
}

func TestRatesFiveHourFromHistory(t *testing.T) {
	var s []Sample
	for h := -50; h <= 0; h++ {
		x := smp(time.Duration(h)*time.Hour, 1)
		x.FiveHour = &SampleWindow{Percent: 2 * float64(h+50)}
		s = append(s, x)
	}
	r := RatesFor(s, curReading(1, rhythmNow.Add(time.Hour)), rhythmNow)
	if !near(r.FiveHour, 2) {
		t.Fatalf("five hour = %v, want 2", r.FiveHour)
	}
}

func TestRatesScopedFromHistory(t *testing.T) {
	var s []Sample
	for h := -50; h <= 0; h++ {
		x := smp(time.Duration(h)*time.Hour, 1)
		x.Scoped = []SampleScoped{{Model: "Fable", Percent: 0.25 * float64(h+50)}}
		s = append(s, x)
	}
	r := RatesFor(s, curReading(1, rhythmNow.Add(time.Hour)), rhythmNow)
	if !near(r.Scoped["Fable"], 0.25) {
		t.Fatalf("scoped = %v, want Fable 0.25", r.Scoped)
	}
}

// A passed reset advances by 7 days: the window is fresh, so 0 %, rate 0.
func TestRatesProvisionalPassedReset(t *testing.T) {
	reset := time.Date(2026, 10, 20, 20, 50, 0, 0, time.UTC)
	s := []Sample{smp(-47*time.Hour, 5), smp(0, 9)}
	r := RatesFor(s, curReading(9, reset), rhythmNow)
	if r.FromHistory || r.SevenDay != 0 {
		t.Fatalf("rates = %+v, want provisional 0 (fresh window)", r)
	}
}

func TestRatesProvisionalFiveHourAndScopedZero(t *testing.T) {
	reset := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	x := smp(-47*time.Hour, 5)
	y := smp(0, 9)
	y.FiveHour = &SampleWindow{Percent: 69}
	y.Scoped = []SampleScoped{{Model: "Fable", Percent: 40}}
	cur := curReading(9, reset)
	cur.FiveHour = &WindowView{RawPercent: 69, Percent: 69}
	cur.WeeklyScoped = []ScopedLimit{{Model: "Fable", Percent: 40}}
	r := RatesFor([]Sample{x, y}, cur, rhythmNow)
	if r.FiveHour != 0 || len(r.Scoped) != 0 || r.SevenDay != 9.0/12 {
		t.Fatalf("rates = %+v", r)
	}
}

// A reading 48 h old right after a restart (one sample) is not 48 h of history.
func TestRatesSpanUsesSampleAtNotFetchedAt(t *testing.T) {
	s := smp(0, 9)
	s.FetchedAt = rhythmNow.Add(-48 * time.Hour)
	r := RatesFor([]Sample{s}, curReading(9, rhythmNow.Add(24*time.Hour)), rhythmNow)
	if r.FromHistory {
		t.Fatalf("rates = %+v, want provisional", r)
	}
}

func TestRatesUnknownReset(t *testing.T) {
	cur := &Reading{FetchedAt: rhythmNow, SevenDay: &WindowView{RawPercent: 9, Percent: 9}}
	r := RatesFor(nil, cur, rhythmNow)
	if r.FromHistory || r.SevenDay != 0 {
		t.Fatalf("rates = %+v, want 0", r)
	}
	if r := RatesFor(nil, nil, rhythmNow); r.SevenDay != 0 || r.FromHistory {
		t.Fatalf("nil reading: %+v", r)
	}
}
