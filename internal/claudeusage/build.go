package claudeusage

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

// StaleAfter: Claude Code itself stops trusting its usage cache after 1 h.
const StaleAfter = time.Hour

// Report is the whole view, rendered as text by the CLI and as JSON as is.
type Report struct {
	GeneratedAt       time.Time        `json:"generated_at"`
	CallerScope       string           `json:"caller_scope"`
	SessionsAvailable bool             `json:"sessions_available"`
	SessionsComplete  bool             `json:"sessions_complete"`
	Weighted7dUsed    *int             `json:"weighted_7d_used_percent"`
	Accounts          []Account        `json:"accounts"`
	Unreadable        []UnreadableCell `json:"unreadable_cells"`
	Warnings          []string         `json:"warnings"`
	// RefreshFailures: --refresh failures whose account is not in the report
	// (its only cell unreadable or logged out after the launch). Absent
	// otherwise, so E1's contract is unchanged.
	RefreshFailures []RefreshFailure `json:"refresh_failures,omitempty"`
	// CellsRead counts the cells read, every scope included: the CLI fails
	// only when none could be. Never shown (D19: it would count other scopes).
	CellsRead int `json:"-"`
}

// Account is one Claude account (one oauthAccount.accountUuid).
type Account struct {
	Label          string        `json:"label"`
	LabelFromCells bool          `json:"label_from_cells"`
	AccountUUID    string        `json:"account_uuid"`
	Capacity       *int          `json:"capacity"`
	Tier           string        `json:"tier"`
	Usable         bool          `json:"usable"`
	Counted        bool          `json:"counted"`
	Flags          []string      `json:"flags"`
	SessionsBusy   *int          `json:"sessions_busy"`
	SessionsOpen   *int          `json:"sessions_open"`
	Reading        *Reading      `json:"reading"`
	Cells          []AccountCell `json:"cells"`
	// Thresholds: the account's selection ceiling and switch threshold —
	// the minimum, field by field, over all its cells (visible or not: a
	// value, never a cell name), so two disagreeing cells keep the stricter.
	Thresholds Thresholds `json:"thresholds"`
	// Budget: the account's weekly token budget right now (BudgetFor), on the
	// strictest budget_pct over all its cells. Read by Staffing only.
	Budget BudgetView `json:"budget"`
	// Refresh is set by AttachRefresh (pigtail usage --refresh) only: absent
	// from the JSON otherwise, so E1's contract is unchanged.
	Refresh *RefreshResult `json:"refresh,omitempty"`
	// rank: the smallest declaration rank among all the account's cells,
	// hidden ones included (D9: the same order from every view). Unexported:
	// never in the JSON, never shown — it would hint at hidden cells (D19).
	rank *int
}

// Reading is the account's most recent cached reading.
type Reading struct {
	Cell         string          `json:"cell,omitempty"` // "" when that cell is not visible to the caller (D19)
	FetchedAt    time.Time       `json:"fetched_at"`
	AgeSeconds   int64           `json:"age_seconds"`
	Stale        bool            `json:"stale"`
	SevenDay     *WindowView     `json:"seven_day"`
	FiveHour     *WindowView     `json:"five_hour"`
	WeeklyScoped []ScopedLimit   `json:"weekly_scoped"`
	ExtraUsage   json.RawMessage `json:"extra_usage"`
	Spend        json.RawMessage `json:"spend"`
	CapReached   bool            `json:"cap_reached"`
}

// WindowView is a window as shown: 0 once its reset has passed (D1).
type WindowView struct {
	Percent     int        `json:"percent"`
	RawPercent  float64    `json:"raw_percent"`
	ResetsAt    *time.Time `json:"resets_at"`
	ResetPassed bool       `json:"reset_passed"`
}

// AccountCell is one cell logged into the account.
type AccountCell struct {
	ID         string     `json:"id"`
	Scope      string     `json:"scope"`
	Account    string     `json:"account,omitempty"`
	HasReading bool       `json:"has_reading"`
	FetchedAt  *time.Time `json:"fetched_at"`
}

// UnreadableCell is a cell whose .claude.json could not be read or parsed.
type UnreadableCell struct {
	ID        string `json:"id"`
	ConfigDir string `json:"config_dir"`
	Error     string `json:"error"`
}

// Build folds the cells' snapshots (in cell-id order) into one view per
// account, for a caller of the given scope. now is compared in UTC.
func Build(snaps []Snapshot, caller scope.Scope, now time.Time) Report {
	now = now.UTC()
	r := Report{GeneratedAt: now, CallerScope: string(caller),
		Accounts: []Account{}, Unreadable: []UnreadableCell{}, Warnings: []string{}}
	var order []string
	groups := map[string][]Snapshot{}
	for _, s := range snaps {
		vis := visible(caller, s.Cell)
		if s.Err == nil {
			r.CellsRead++
		}
		if vis {
			r.Warnings = append(r.Warnings, s.Warnings...)
		}
		switch {
		case s.Err != nil:
			if vis {
				r.Unreadable = append(r.Unreadable, UnreadableCell{ID: s.Cell.ID, ConfigDir: s.Cell.ConfigDir, Error: s.Err.Error()})
				r.Warnings = append(r.Warnings, fmt.Sprintf("cell %s: %v — not counted", s.Cell.ID, s.Err))
			}
			continue
		case s.AccountUUID == "":
			if vis {
				r.Warnings = append(r.Warnings, fmt.Sprintf("cell %s: not logged in (no oauthAccount.accountUuid) — not counted", s.Cell.ID))
			}
			continue
		case s.Mismatch && vis:
			r.Warnings = append(r.Warnings, fmt.Sprintf("cell %s: usage cache is for account %s, cell is logged in as %s — reading ignored",
				s.Cell.ID, short(s.CacheUUID), short(s.AccountUUID)))
		}
		if _, ok := groups[s.AccountUUID]; !ok {
			order = append(order, s.AccountUUID)
		}
		groups[s.AccountUUID] = append(groups[s.AccountUUID], s)
	}
	for _, uuid := range order {
		r.Accounts = append(r.Accounts, buildAccount(groups[uuid], caller, now, &r.Warnings))
	}
	sort.SliceStable(r.Accounts, func(i, j int) bool { return listedBefore(&r.Accounts[i], &r.Accounts[j]) })
	// R2: the weighted 7-day used, Σ used × N / Σ N over the counted accounts
	// (a passed reset is 0 % used), rounded to the nearest integer.
	var used, total float64
	for i := range r.Accounts {
		a := &r.Accounts[i]
		if !a.Counted {
			continue
		}
		n := float64(*a.Capacity)
		used += float64(clampPct(a.Reading.SevenDay.Percent)) * n
		total += n
	}
	if total > 0 {
		w := int(math.Round(used / total))
		r.Weighted7dUsed = &w
	}
	return r
}

func buildAccount(snaps []Snapshot, caller scope.Scope, now time.Time, warn *[]string) Account {
	a := Account{AccountUUID: snaps[0].AccountUUID, Flags: []string{}, Cells: []AccountCell{}}
	var latest *Snapshot
	var ids, conflicts []string
	letter, letterCell, anyLetter := "", "", ""
	a.Thresholds = AccountThresholds(snaps)
	for i := range snaps {
		s := &snaps[i]
		if s.HasReading && (latest == nil || s.FetchedAt.After(latest.FetchedAt)) {
			latest = s // the % are the account's, whichever cell read them
		}
		if s.Cell.Rank != nil && (a.rank == nil || *s.Cell.Rank < *a.rank) {
			rk := *s.Cell.Rank
			a.rank = &rk // every cell, visible or not: order only, never shown
		}
		if anyLetter == "" {
			anyLetter = s.Cell.Account
		}
		if !visible(caller, s.Cell) {
			continue // D19: nothing of another scope's cells below
		}
		ids = append(ids, s.Cell.ID)
		ac := AccountCell{ID: s.Cell.ID, Scope: s.Cell.Scope, Account: s.Cell.Account, HasReading: s.HasReading}
		if s.HasReading {
			t := s.FetchedAt
			ac.FetchedAt = &t
		}
		a.Cells = append(a.Cells, ac)
		switch {
		case s.Cell.Account == "":
		case letter == "":
			letter, letterCell = s.Cell.Account, s.Cell.ID
		case s.Cell.Account != letter:
			conflicts = append(conflicts, s.Cell.ID+"="+s.Cell.Account)
		}
	}
	masterCell := false
	for _, s := range snaps {
		if scope.Scope(s.Cell.Scope) == scope.Master() {
			masterCell = true
		}
	}
	// D6/D25: the master caller never runs on an account whose cells are all
	// of another scope; the master is read from the scope table.
	a.Usable = !scope.SeesAll(caller) || masterCell
	switch {
	case letter != "":
		a.Label = letter
	case len(ids) > 0:
		a.Label, a.LabelFromCells = strings.Join(ids, "+"), true
	case anyLetter != "":
		a.Label = anyLetter
	default:
		a.Label = "other-" + short(a.AccountUUID) // never a cell id (D19)
	}
	if len(conflicts) > 0 {
		*warn = append(*warn, fmt.Sprintf("account %s: cells disagree on its letter (%s=%s, %s) — using %s",
			short(a.AccountUUID), letterCell, letter, strings.Join(conflicts, ", "), letter))
	}
	tierSrc := &snaps[0]
	if latest != nil {
		tierSrc = latest
	}
	if n, tier, ok := Capacity(tierSrc.UserTier, tierSrc.OrgTier); ok {
		a.Capacity, a.Tier = &n, tier
	} else {
		a.Tier = tier
	}
	if latest != nil {
		cellName := ""
		if visible(caller, latest.Cell) {
			cellName = latest.Cell.ID
		}
		age := now.Sub(latest.FetchedAt)
		if age < 0 {
			if cellName != "" {
				*warn = append(*warn, fmt.Sprintf("cell %s: usage reading is %s in the future (clock skew?) — age shown as 0", cellName, FormatAge(-age)))
			}
			age = 0
		}
		a.Reading = &Reading{
			Cell: cellName, FetchedAt: latest.FetchedAt,
			AgeSeconds: int64(age / time.Second), Stale: age > StaleAfter,
			SevenDay:     view(latest.SevenDay, now),
			FiveHour:     view(latest.FiveHour, now),
			WeeklyScoped: latest.WeeklyScoped,
			ExtraUsage:   latest.ExtraUsage,
			Spend:        latest.Spend,
			CapReached:   latest.CapReached,
		}
	}
	if !a.Usable {
		a.Flags = append(a.Flags, FlagNotMaster())
	}
	if a.Reading == nil { // no cell of the account has a reading (Spec 13)
		a.Flags = append(a.Flags, "no-reading")
	}
	if a.Capacity == nil {
		a.Flags = append(a.Flags, "unknown-capacity")
	}
	if a.Reading != nil && a.Reading.CapReached {
		a.Flags = append(a.Flags, "cap")
	}
	// D8: counted in the weighted total whatever the caller's scope — not-perso
	// only flags the account, it no longer takes it out of the total.
	a.Counted = a.Capacity != nil && a.Reading != nil && a.Reading.SevenDay != nil
	a.Budget = BudgetFor(a.Reading, AccountBudgetPct(snaps), now)
	return a
}

func view(w *Window, now time.Time) *WindowView {
	if w == nil {
		return nil
	}
	v := &WindowView{RawPercent: w.Percent, Percent: int(math.Round(w.Percent))}
	if !w.ResetsAt.IsZero() {
		t := w.ResetsAt
		v.ResetsAt = &t
		if !t.After(now) {
			v.ResetPassed, v.Percent = true, 0
		}
	}
	return v
}

// FormatAge: 0m, 42m, 12h36, 3d.
func FormatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "0m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02d", int(d/time.Hour), int(d%time.Hour/time.Minute))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// short is an account uuid's first 8 characters, for messages.
func short(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}

// visible: may caller see this cell? Perso (the master scope) sees all; any
// other scope sees its own cells only (D19). A cell whose scope does not
// parse is shown to perso only.
func visible(caller scope.Scope, c CellInput) bool {
	if scope.SeesAll(caller) {
		return true
	}
	sc, err := scope.Parse(c.Scope)
	return err == nil && scope.CanRead(caller, sc)
}

// CellVisible is visible, exported: may a caller of scope caller see a cell
// of scope cellScope (D19)? Readers of the daemon's background refresh state
// (seen from the master) name a cell only when it holds.
func CellVisible(caller scope.Scope, cellScope string) bool {
	return visible(caller, CellInput{Scope: cellScope})
}

// FlagNotMaster marks, in the master view, an account no master-scope cell is
// logged into (D6/D24): "not-<master>", today "not-perso". Read from the scope
// table, never spelt.
func FlagNotMaster() string { return "not-" + string(scope.Master()) }

func clampPct(p int) int { return min(max(p, 0), 100) }

// listedBefore orders accounts (D9): by their first declared cell (rank), the
// ranked ones first; ties and unranked accounts by label.
func listedBefore(a, b *Account) bool {
	switch {
	case a.rank != nil && b.rank != nil && *a.rank != *b.rank:
		return *a.rank < *b.rank
	case (a.rank == nil) != (b.rank == nil):
		return a.rank != nil
	}
	return a.Label < b.Label
}

// AccountThresholds is the minimum, field by field, of the resolved
// thresholds of the given cells (the strictest wins); the defaults when
// there is none.
func AccountThresholds(snaps []Snapshot) Thresholds {
	cells := make([]CellInput, 0, len(snaps))
	for _, s := range snaps {
		cells = append(cells, s.Cell)
	}
	return StrictestThresholds(cells)
}

// AccountBudgetPct is the minimum of the resolved budgets of the given cells
// (the strictest wins); the default when there is none.
func AccountBudgetPct(snaps []Snapshot) float64 {
	out := DefaultBudgetPct
	for i, s := range snaps {
		b := ResolveBudgetPct(s.Cell.BudgetPct)
		if i == 0 || b < out {
			out = b
		}
	}
	return out
}

// StrictestThresholds is the minimum, field by field, of the cells' resolved
// thresholds; the defaults for no cell.
func StrictestThresholds(cells []CellInput) Thresholds {
	if len(cells) == 0 {
		return ResolveThresholds(nil, nil)
	}
	out := cells[0].Thresholds()
	for _, c := range cells[1:] {
		t := c.Thresholds()
		out.SelectPct = min(out.SelectPct, t.SelectPct)
		out.SwitchPct = min(out.SwitchPct, t.SwitchPct)
	}
	return out
}
