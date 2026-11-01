package claudeusage

import (
	"sort"
	"time"
)

const (
	SimStep    = 10 * time.Minute
	SimHorizon = 7 * 24 * time.Hour
	// TightShare: the minimum reserve under this share of one account's
	// selection ceiling (the default ceiling, DefaultSelectPct) reads "de
	// justesse".
	TightShare = 0.10
	// FastPoints: an account is "fast" when its availability is under the
	// share of its week still to run by more than this many points.
	FastPoints = 5.0

	simWeek     = 7 * 24 * time.Hour
	simFiveHour = 5 * time.Hour
)

// SimScoped is a per-model weekly limit entering the simulation.
type SimScoped struct {
	Model   string
	Percent float64
	Reset   *time.Time
}

// SimAccount is one account of the verdict: its usage now and its rhythm.
type SimAccount struct {
	Label                        string
	SevenDay, FiveHour           float64 // % used now (raw, 0 if the reset has passed)
	SevenDayReset, FiveHourReset *time.Time
	Scoped                       []SimScoped
	Rate                         Rates
	// Ceiling is the account's selection ceiling (Thresholds.Ceiling); 0 =
	// DefaultSelectPct. It blocks the 7d, 5h and per-model windows alike.
	Ceiling float64
}

type VerdictState string

const (
	VerdictOK    VerdictState = "ok"
	VerdictTight VerdictState = "tight"
	VerdictLater VerdictState = "later"
	VerdictNow   VerdictState = "now"
	VerdictNone  VerdictState = "none"
)

// Verdict is whether the week holds on the accounts, at the current rhythm.
type Verdict struct {
	State            VerdictState
	BlockedFrom      *time.Time
	FreesAt          *time.Time
	FreesLabel       string
	NextRenewalLabel string
	NextRenewalAt    *time.Time
	MinReserveShare  float64
}

type simScopedState struct {
	model string
	u     float64
	r     *time.Time
}

type simState struct {
	label   string
	ceiling float64
	u7, u5  float64
	r7, r5  *time.Time
	scoped  []simScopedState
}

func cp(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// advance renews a weekly window whose reset is due: usage back to 0, reset
// pushed a week at a time until it is in the future.
func advance(u *float64, r **time.Time, t time.Time) {
	for *r != nil && !(*r).After(t) {
		*u = 0
		n := (*r).Add(simWeek)
		*r = &n
	}
}

// Simulate walks the rotation over the next SimHorizon in SimStep steps: the
// account whose week renews first is served and takes the whole burn (the sum
// of the rates), until it reaches a margin. It reports the first moment no
// account is under its margins, or how tight the reserve gets.
func Simulate(accts []SimAccount, now time.Time) Verdict {
	if len(accts) == 0 {
		return Verdict{State: VerdictNone}
	}
	var d7, d5 float64
	ds := map[string]float64{}
	st := make([]*simState, len(accts))
	for i, a := range accts {
		d7 += a.Rate.SevenDay
		d5 += a.Rate.FiveHour
		for m, v := range a.Rate.Scoped {
			ds[m] += v
		}
		ceiling := a.Ceiling
		if ceiling <= 0 {
			ceiling = DefaultSelectPct
		}
		s := &simState{label: a.Label, ceiling: ceiling, u7: a.SevenDay, u5: a.FiveHour, r7: cp(a.SevenDayReset), r5: cp(a.FiveHourReset)}
		for _, l := range a.Scoped {
			s.scoped = append(s.scoped, simScopedState{model: l.Model, u: l.Percent, r: cp(l.Reset)})
		}
		st[i] = s
	}
	h := float64(SimStep) / float64(time.Hour)
	minReserve := -1.0
	var blockedFrom, freesAt *time.Time
	var freesLabel string
	inBlock := false
	for t := now; !t.After(now.Add(SimHorizon)); t = t.Add(SimStep) {
		var ok []*simState
		reserve := 0.0
		for _, s := range st {
			advance(&s.u7, &s.r7, t)
			if s.r5 != nil && !s.r5.After(t) {
				s.u5, s.r5 = 0, nil
			}
			for i := range s.scoped {
				advance(&s.scoped[i].u, &s.scoped[i].r, t)
			}
			if s.under() {
				ok = append(ok, s)
			}
			reserve += max(0, s.ceiling-s.u7)
		}
		if minReserve < 0 || reserve < minReserve {
			minReserve = reserve
		}
		if len(ok) == 0 {
			if !inBlock {
				inBlock = true
				if blockedFrom == nil {
					from := t
					blockedFrom = &from
					freesAt, freesLabel = blockFree(st)
				}
			}
			continue
		}
		inBlock = false
		sort.SliceStable(ok, func(i, j int) bool {
			a, b := ok[i].r7, ok[j].r7
			switch {
			case a == nil:
				return false
			case b == nil:
				return true
			}
			return a.Before(*b)
		})
		s := ok[0]
		s.u7 += d7 * h
		if d5 > 0 && s.r5 == nil {
			r := t.Add(simFiveHour)
			s.r5 = &r
		}
		s.u5 += d5 * h
		for i := range s.scoped {
			s.scoped[i].u += ds[s.scoped[i].model] * h
		}
	}
	v := Verdict{MinReserveShare: minReserve / DefaultSelectPct}
	v.NextRenewalLabel, v.NextRenewalAt = nextRenewal(accts, now)
	switch {
	case blockedFrom == nil && v.MinReserveShare < TightShare:
		v.State = VerdictTight
	case blockedFrom == nil:
		v.State = VerdictOK
	case blockedFrom.Equal(now):
		v.State, v.FreesAt, v.FreesLabel = VerdictNow, freesAt, freesLabel
	default:
		v.State, v.BlockedFrom, v.FreesAt, v.FreesLabel = VerdictLater, blockedFrom, freesAt, freesLabel
	}
	return v
}

func (s *simState) under() bool {
	if s.u7 >= s.ceiling || s.u5 >= s.ceiling {
		return false
	}
	for _, l := range s.scoped {
		if l.u >= s.ceiling {
			return false
		}
	}
	return true
}

// blockFree is when the first account frees itself: for each account, the
// latest reset among the limits that block it (an account with a blocking
// limit of unknown reset is ignored); the earliest of those.
func blockFree(st []*simState) (*time.Time, string) {
	var best *time.Time
	var label string
	for _, s := range st {
		var until *time.Time
		known := true
		see := func(blocking bool, r *time.Time) {
			if !blocking {
				return
			}
			if r == nil {
				known = false
				return
			}
			if until == nil || r.After(*until) {
				until = r
			}
		}
		see(s.u7 >= s.ceiling, s.r7)
		see(s.u5 >= s.ceiling, s.r5)
		for _, l := range s.scoped {
			see(l.u >= s.ceiling, l.r)
		}
		if !known || until == nil {
			continue
		}
		if best == nil || until.Before(*best) {
			c := *until
			best, label = &c, s.label
		}
	}
	return best, label
}

// nextRenewal is the account whose week renews first after now; a reset
// already passed counts as that reset plus whole weeks, never a past date.
func nextRenewal(accts []SimAccount, now time.Time) (string, *time.Time) {
	var best *time.Time
	var label string
	for _, a := range accts {
		if a.SevenDayReset == nil {
			continue
		}
		r := *a.SevenDayReset
		for !r.After(now) {
			r = r.Add(simWeek)
		}
		if best == nil || r.Before(*best) {
			c := r
			best, label = &c, a.Label
		}
	}
	return label, best
}
