package claudeusage

import "math"

// Per-account usage thresholds. The defaults live here; an account's own
// values come from the cell table (claude-cells.yaml select_pct / switch_pct,
// written by the launcher tooling), never from code.
const (
	// DefaultSelectPct is the selection ceiling, on both the 5h and the 7d
	// window: an account at or above it is not picked.
	DefaultSelectPct = 90.0
	// DefaultSwitchPct is the anticipatory-switch threshold, on either
	// window: an interactive session whose account reaches it moves at its
	// turn end.
	DefaultSwitchPct = 98.0
	// DefaultBudgetPct is the weekly token budget, as a percent of the 7-day
	// window: what Staffing may consume on an account over a week. It is not a
	// field of Thresholds: those are built from positional literals, and only
	// Staffing reads the budget (see BudgetFor).
	DefaultBudgetPct = 70.0
)

// CeilingHelp names the selection ceiling in help texts ("under " +
// CeilingHelp, "at or above " + CeilingHelp), so no help writes a fixed
// margin. TestCeilingHelpMatchesDefault keeps its default in step.
const CeilingHelp = "its account's ceiling on both the 5h and 7d windows (select_pct in claude-cells.yaml, default 90%)"

// Thresholds is one account's selection ceiling and switch threshold. The
// zero value reads as the defaults (a Thresholds{} left in a literal never
// blocks every account at 0 %).
type Thresholds struct {
	SelectPct float64 `json:"select_pct"`
	SwitchPct float64 `json:"switch_pct"`
}

// ResolveThresholds turns the table's optional values into thresholds: nil,
// NaN, <= 0 or > 100 → the default; then a switch threshold under the
// ceiling is raised to the ceiling.
func ResolveThresholds(selectPct, switchPct *float64) Thresholds {
	t := Thresholds{SelectPct: pctOr(selectPct, DefaultSelectPct), SwitchPct: pctOr(switchPct, DefaultSwitchPct)}
	if t.SwitchPct < t.SelectPct {
		t.SwitchPct = t.SelectPct
	}
	return t
}

func pctOr(v *float64, def float64) float64 {
	if v == nil || math.IsNaN(*v) || *v <= 0 || *v > 100 {
		return def
	}
	return *v
}

// ResolveBudgetPct turns the table's optional budget into a percent: nil, NaN,
// <= 0 or > 100 → DefaultBudgetPct.
func ResolveBudgetPct(v *float64) float64 { return pctOr(v, DefaultBudgetPct) }

func (t Thresholds) selectPct() float64 {
	if t.SelectPct <= 0 {
		return DefaultSelectPct
	}
	return t.SelectPct
}

func (t Thresholds) switchPct() float64 {
	if t.SwitchPct <= 0 {
		return DefaultSwitchPct
	}
	return t.SwitchPct
}

// Ceiling is the selection ceiling with the default applied — the only way
// to read it outside OverSelect.
func (t Thresholds) Ceiling() float64 { return t.selectPct() }

// SwitchAt is the switch threshold with the default applied, for what a
// switch says (the hook lines); deciding goes through OverSwitch.
func (t Thresholds) SwitchAt() float64 { return t.switchPct() }

// OverSelect: the window (reset not passed) is at or above the ceiling.
func (t Thresholds) OverSelect(w *WindowView) bool {
	return w != nil && !w.ResetPassed && w.RawPercent >= t.selectPct()
}

// OverSwitch: the window (reset not passed) is at or above the switch
// threshold.
func (t Thresholds) OverSwitch(w *WindowView) bool {
	return w != nil && !w.ResetPassed && w.RawPercent >= t.switchPct()
}

// Thresholds resolves the cell's own values (defaults when absent).
func (c CellInput) Thresholds() Thresholds { return ResolveThresholds(c.SelectPct, c.SwitchPct) }
