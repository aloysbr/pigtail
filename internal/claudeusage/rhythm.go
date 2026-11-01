package claudeusage

import (
	"sort"
	"time"
)

const (
	// RhythmWindow: the rhythm is what an account consumed over this span.
	RhythmWindow = 24 * time.Hour
	// RhythmMinSpan: the history needed (the span of the samples' At, never
	// the reading's FetchedAt) before the 24 h rhythm replaces the mean.
	RhythmMinSpan = 48 * time.Hour
)

// Rates is an account's consumption in % of each limit per hour.
type Rates struct {
	SevenDay    float64
	FiveHour    float64            // 0 while provisional (I12)
	Scoped      map[string]float64 // per model; empty while provisional (I12)
	FromHistory bool               // false = provisional: week = mean since renewal, 5 h and per-model = 0
}

// RatesFor computes one account's rhythm. samples are the account's own (any
// order); cur is its current reading. With RhythmMinSpan of history it is the
// consumption of the last RhythmWindow; otherwise the week's mean since its
// renewal, with the 5 h and per-model rates at 0 (I12).
func RatesFor(samples []Sample, cur *Reading, now time.Time) Rates {
	s := dedupSamples(samples)
	if len(s) > 0 && now.Sub(s[0].At) >= RhythmMinSpan {
		if r, ok := historyRates(s, now); ok {
			return r
		}
	}
	return provisionalRates(cur, now)
}

// dedupSamples sorts by At and keeps the first sample of each FetchedAt.
func dedupSamples(in []Sample) []Sample {
	s := append([]Sample(nil), in...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].At.Before(s[j].At) })
	seen := map[time.Time]bool{}
	out := s[:0]
	for _, x := range s {
		if seen[x.FetchedAt] {
			continue
		}
		seen[x.FetchedAt] = true
		out = append(out, x)
	}
	return out
}

func historyRates(s []Sample, now time.Time) (Rates, bool) {
	cut := now.Add(-RhythmWindow)
	anchor := -1
	for i, x := range s {
		if !x.At.After(cut) {
			anchor = i
		}
	}
	if anchor < 0 {
		return Rates{}, false
	}
	series := s[anchor:]
	hours := series[len(series)-1].At.Sub(series[0].At).Hours()
	if hours <= 0 {
		return Rates{}, false
	}
	r := Rates{FromHistory: true, Scoped: map[string]float64{}}
	var seven, five rise
	scoped := map[string]*rise{}
	for _, x := range series {
		if x.SevenDay != nil {
			seven.add(x.SevenDay.Percent)
		}
		if x.FiveHour != nil {
			five.add(x.FiveHour.Percent)
		}
		for _, m := range x.Scoped {
			if scoped[m.Model] == nil {
				scoped[m.Model] = &rise{}
			}
			scoped[m.Model].add(m.Percent)
		}
	}
	r.SevenDay = seven.sum / hours
	r.FiveHour = five.sum / hours
	for m, v := range scoped {
		r.Scoped[m] = v.sum / hours
	}
	return r, true
}

// rise sums the increases of a series; a drop is a renewal, and the new value
// is what was consumed since (90 -> 92 -> 1 -> 3 counts 2 + 1 + 2).
type rise struct {
	prev float64
	have bool
	sum  float64
}

func (r *rise) add(v float64) {
	if r.have {
		if v >= r.prev {
			r.sum += v - r.prev
		} else {
			r.sum += v
		}
	}
	r.prev, r.have = v, true
}

func provisionalRates(cur *Reading, now time.Time) Rates {
	r := Rates{Scoped: map[string]float64{}}
	if cur == nil || cur.SevenDay == nil || cur.SevenDay.ResetsAt == nil {
		return r
	}
	reset, pct := *cur.SevenDay.ResetsAt, cur.SevenDay.RawPercent
	for !reset.After(now) { // renewed: a fresh window, nothing used yet
		reset = reset.Add(7 * 24 * time.Hour)
		pct = 0
	}
	since := now.Sub(reset.Add(-7 * 24 * time.Hour)).Hours()
	r.SevenDay = pct / max(1, since)
	return r
}
