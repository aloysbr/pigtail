package claudeusage

import "time"

// A weekly token budget (Staffing only): how much of its 7-day window an
// account may have consumed at a given point of the week. It follows the pace
// curve, so an account does not burn its budget on the Monday:
//
//	allowed = budget_pct × (elapsed share of the 7-day window) + FastPoints
//
// A reading that cannot say (absent, older than PickFreshFor, no 7-day window,
// no reset time, no 5-hour window) is unmeasured: never within the budget. A
// 5-hour window that has not started is no absence: it reads as percent 0 with
// a null reset. Only a null window leaves the ceiling unread, and the state
// says so here rather than in Pick: the figure shown is the figure that decides.

// budgetWindow is the length of the window the budget is a percent of.
const budgetWindow = 7 * 24 * time.Hour

// budgetClockSkew is how far ahead of the clock a reading may be dated and stay
// measured: a small clock skew must not close Staffing, a large one must.
const budgetClockSkew = time.Minute

// BudgetState says where an account stands against its budget.
type BudgetState string

const (
	BudgetWithin     BudgetState = "within"
	BudgetOver       BudgetState = "over"
	BudgetUnmeasured BudgetState = "unmeasured"
)

// BudgetView is one account's budget at an instant: the one figure shown by
// pigtail usage and read by Staffing's gate.
type BudgetView struct {
	State     BudgetState `json:"state"`
	BudgetPct float64     `json:"budget_pct"`
	// Used is the 7-day window's percent (0 once its reset has passed), Left
	// max(BudgetPct-Used, 0), Allowed the curve right now; all three stay 0
	// when unmeasured, the reading saying nothing.
	Used    float64 `json:"used"`
	Left    float64 `json:"left"`
	Allowed float64 `json:"allowed"`
	// FreesAt (over only): when the curve catches Used, bounded by the reset.
	FreesAt *time.Time `json:"frees_at,omitempty"`
	// Why (unmeasured only): what could not be read.
	Why string `json:"why,omitempty"`
}

// BudgetFor is the budget of an account whose reading is rd. Pure. A budget
// that is <= 0, NaN or over 100 reads as the default.
func BudgetFor(rd *Reading, budgetPct float64, now time.Time) BudgetView {
	v := BudgetView{BudgetPct: ResolveBudgetPct(&budgetPct)}
	unmeasured := func(why string) BudgetView {
		v.State, v.Why = BudgetUnmeasured, why
		return v
	}
	if rd == nil {
		return unmeasured("no reading")
	}
	if rd.FetchedAt.Sub(now) > budgetClockSkew { // it would read as fresh until the clock catches up
		return unmeasured("reading dated in the future")
	}
	if now.Sub(rd.FetchedAt) > PickFreshFor { // the exact instant, never AgeSeconds
		// Stable: the journal folds its Tours on it; the age is in pigtail usage's AGE.
		return unmeasured("reading older than " + FormatAge(PickFreshFor))
	}
	w := rd.SevenDay
	if w == nil {
		return unmeasured("no 7-day window in the reading")
	}
	if w.ResetsAt == nil {
		return unmeasured("no reset time")
	}
	if rd.FiveHour == nil {
		return unmeasured("no 5-hour window in the reading")
	}
	v.Used = w.RawPercent
	if w.ResetPassed {
		v.Used = 0 // the window has renewed
	}
	v.Left = max(v.BudgetPct-v.Used, 0)
	elapsed := 1 - float64(w.ResetsAt.Sub(now))/float64(budgetWindow)
	v.Allowed = v.BudgetPct*min(max(elapsed, 0), 1) + FastPoints
	if v.Used <= v.Allowed {
		v.State = BudgetWithin
		return v
	}
	v.State = BudgetOver
	frees := *w.ResetsAt
	if f := (v.Used - FastPoints) / v.BudgetPct; f < 1 {
		frees = w.ResetsAt.Add(-time.Duration((1 - f) * float64(budgetWindow)))
	}
	v.FreesAt = &frees
	return v
}
