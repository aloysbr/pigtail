package claudeusage

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

// The account rotation policy (sub_x000000000000008, D4 upstream): an
// account is under the margin while both its 5-hour and 7-day windows are
// below its own selection ceiling (Thresholds: select_pct of the cell table,
// default DefaultSelectPct); among those, the one whose 7-day window renews
// first is picked. A reading older than PickFreshFor is stale.
const PickFreshFor = 30 * time.Minute

// OverMarginRule is the one wording of "past the margin" every refusal and
// warning cites: the ceiling is per account (Thresholds).
const OverMarginRule = "5h or 7d at or above the account's ceiling"

// ErrNoCandidate: no logged-in, readable cell in the scope.
var ErrNoCandidate = errors.New("no candidate account")

// PickGroup is a candidate's tier, in pick order.
type PickGroup string

const (
	GroupFresh      PickGroup = "fresh"
	GroupStale      PickGroup = "stale"
	GroupNoReading  PickGroup = "no-reading"
	GroupOverMargin PickGroup = "over-margin"
)

var groupOrder = map[PickGroup]int{GroupFresh: 0, GroupStale: 1, GroupNoReading: 2, GroupOverMargin: 3}

// Candidate is one account of the scope, as Pick ranks it.
type Candidate struct {
	// Label is the account's letter, else other-<uuid8>: never a cell id
	// (D19), at most 14 runes.
	Label       string `json:"label"`
	AccountUUID string `json:"account_uuid"`
	// Cell is the account's lowest-ranked logged-in cell of the scope.
	Cell     CellInput      `json:"cell"`
	Group    PickGroup      `json:"group"`
	FiveHour *WindowView    `json:"five_hour"`
	SevenDay *WindowView    `json:"seven_day"`
	Age      *time.Duration `json:"age_ns"`
	// LimitedUntil: set when PickOptions.Limited holds a future instant.
	LimitedUntil *time.Time `json:"limited_until,omitempty"`
	// FreesAt (over-margin only, D9): the latest reset of its blocking
	// windows; nil = unknown.
	FreesAt *time.Time `json:"frees_at,omitempty"`
	// Thresholds: the account's (Account.Thresholds of the master Build).
	Thresholds Thresholds `json:"thresholds"`
	// Budget: the account's weekly budget, set only under PickOptions.Budgeted.
	Budget *BudgetView `json:"budget,omitempty"`
}

// Choice is the picked candidate, the whole ranking and the warnings a
// launch must show (D3/D9, a stale or no-reading pick).
type Choice struct {
	Candidate
	Ranking  []Candidate `json:"ranking"`
	Warnings []string    `json:"warnings"`
}

// PickOptions: the scope to pick in, and the accounts known to be limited
// (accountUuid → limited until, a StopFailure's resetsAt). Budgeted (Staffing
// only, D6) also blocks an account that is not within its weekly budget: an
// unmeasured one blocks with an unknown release.
type PickOptions struct {
	Scope    scope.Scope
	Limited  map[string]time.Time
	Budgeted bool
}

// Pick is the one account-choosing function (D7). Pure: snapshots in, choice
// out. The credits cap (CapReached) is never read: it says nothing of the
// plan quota (Spec 8).
func Pick(snaps []Snapshot, opt PickOptions, now time.Time) (Choice, error) {
	loc := now.Location()
	now = now.UTC()
	cells := map[string]CellInput{}
	var order, reasons []string
	for _, s := range snaps {
		sc, err := scope.Parse(s.Cell.Scope)
		if err != nil || sc != opt.Scope {
			continue // another scope's cells are never named (D19)
		}
		switch {
		case s.Err != nil:
			reasons = append(reasons, fmt.Sprintf("cell %s: %v", s.Cell.ID, s.Err))
			continue
		case s.AccountUUID == "":
			reasons = append(reasons, fmt.Sprintf("cell %s: not logged in", s.Cell.ID))
			continue
		}
		cur, ok := cells[s.AccountUUID]
		if !ok {
			order = append(order, s.AccountUUID)
		}
		if !ok || rankBefore(s.Cell, cur) {
			cells[s.AccountUUID] = s.Cell
		}
	}
	if len(order) == 0 {
		why := "no cell"
		if len(reasons) > 0 {
			why = strings.Join(reasons, ", ")
		}
		return Choice{}, fmt.Errorf("%w in scope %s (%s)", ErrNoCandidate, opt.Scope, why)
	}
	readings := accountsByUUID(Build(snaps, scope.Master(), now)) // every reading (build.go)
	labels := accountsByUUID(Build(snaps, opt.Scope, now))
	ranking := make([]Candidate, 0, len(order))
	for _, uuid := range order {
		c := Candidate{AccountUUID: uuid, Cell: cells[uuid], Label: "other-" + short(uuid)}
		if a, ok := labels[uuid]; ok && !a.LabelFromCells {
			c.Label = a.Label
		}
		var rd *Reading
		th := ResolveThresholds(nil, nil)
		if a, ok := readings[uuid]; ok {
			rd, th = a.Reading, a.Thresholds
		}
		c.Thresholds = th
		if rd != nil {
			c.FiveHour, c.SevenDay = rd.FiveHour, rd.SevenDay
			age := max(now.Sub(rd.FetchedAt), 0)
			c.Age = &age
		}
		var blocking []*time.Time
		unknown := false
		block := func(w *WindowView) {
			if !th.OverSelect(w) {
				return
			}
			if w.ResetsAt == nil {
				unknown = true
				return
			}
			blocking = append(blocking, w.ResetsAt)
		}
		block(c.FiveHour)
		block(c.SevenDay)
		if opt.Budgeted {
			b := BudgetFor(rd, ResolveBudgetPct(nil), now)
			if a, ok := readings[uuid]; ok {
				b = a.Budget
			}
			c.Budget = &b
			switch {
			case b.State == BudgetWithin:
			case b.FreesAt == nil:
				unknown = true
			default:
				blocking = append(blocking, b.FreesAt)
			}
		}
		if until, ok := opt.Limited[uuid]; ok && until.After(now) {
			u := until.UTC()
			c.LimitedUntil = &u
			blocking = append(blocking, &u)
		}
		switch {
		case len(blocking) > 0 || unknown:
			c.Group = GroupOverMargin
			if !unknown {
				latest := *blocking[0]
				for _, t := range blocking[1:] {
					if t.After(latest) {
						latest = *t
					}
				}
				c.FreesAt = &latest
			}
		case rd == nil || c.FiveHour == nil || c.SevenDay == nil:
			c.Group = GroupNoReading
		case *c.Age <= PickFreshFor:
			c.Group = GroupFresh
		default:
			c.Group = GroupStale
		}
		ranking = append(ranking, c)
	}
	sort.SliceStable(ranking, func(i, j int) bool { return pickBefore(&ranking[i], &ranking[j]) })
	ch := Choice{Candidate: ranking[0], Ranking: ranking, Warnings: []string{}}
	switch ch.Group {
	case GroupStale:
		ch.Warnings = append(ch.Warnings, fmt.Sprintf("%s's reading is %s old — no fresh account under the margin", ch.Label, FormatAge(*ch.Age)))
	case GroupNoReading:
		ch.Warnings = append(ch.Warnings, fmt.Sprintf("%s has no usage reading — no account under the margin has one", ch.Label))
	case GroupOverMargin:
		when := "free-up time unknown"
		if ch.FreesAt != nil {
			when = ch.FreesAt.In(loc).Format("Mon 15:04")
		}
		ch.Warnings = append(ch.Warnings, fmt.Sprintf("every account of scope %s is over the margin (%s) — %s, whose limits free up first (%s)",
			opt.Scope, OverMarginRule, ch.Label, when))
	}
	return ch, nil
}

func accountsByUUID(r Report) map[string]Account {
	out := make(map[string]Account, len(r.Accounts))
	for _, a := range r.Accounts {
		out[a.AccountUUID] = a
	}
	return out
}

// pickBefore: group; then the 7-day reset (groups 1–3, unknown or passed
// last) or the free-up time (over-margin, unknown last); then rank, then id.
func pickBefore(a, b *Candidate) bool {
	if ga, gb := groupOrder[a.Group], groupOrder[b.Group]; ga != gb {
		return ga < gb
	}
	var ta, tb *time.Time
	if a.Group == GroupOverMargin {
		ta, tb = a.FreesAt, b.FreesAt
	} else {
		ta, tb = sevenDayReset(a), sevenDayReset(b)
	}
	switch {
	case ta != nil && tb != nil && !ta.Equal(*tb):
		return ta.Before(*tb)
	case (ta == nil) != (tb == nil):
		return ta != nil
	}
	return rankBefore(a.Cell, b.Cell)
}

func sevenDayReset(c *Candidate) *time.Time {
	if c.SevenDay == nil || c.SevenDay.ResetPassed {
		return nil
	}
	return c.SevenDay.ResetsAt
}
