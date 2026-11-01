package claudeusage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

// NoUsableCell: the account has cells of the caller's scope, none readable
// and logged in. No launch, and no fallback to a cell of another scope (S2).
const NoUsableCell = "no usable cell in the caller's scope"

// RefreshTarget is the one cell pigtail usage --refresh launches for an
// account, with that cell's reading before the launch. A non-empty Reason
// means no launch: the account fails with it (S2 case 2).
type RefreshTarget struct {
	AccountUUID string
	Label       string // the cell's Account letter, else other-<uuid8> (Build's rule)
	Cell        CellInput
	Before      Snapshot
	Reason      string
}

// RefreshTargets picks one cell per account the caller may see. An
// account is the accountUuid of a readable, logged-in visible cell, or the one
// the launcher config files an unusable cell of the caller's scope under (same
// Account letter). The letter → accountUuid link is learnt from every usable
// cell, hidden ones included — internal only: nothing of a hidden cell is
// returned, the target is the visible cell (it unreadable, ip readable, seen
// from globex: i fails on it, never skipped). Per account: the lowest-ranked
// usable cell of the caller's own scope; else, if it has cells of that scope,
// none usable, a failed target with NoUsableCell on the lowest-ranked of them
// (never a cell of another scope); else — no cell of that scope at all — the
// lowest-ranked usable visible cell (t seen from perso). Ordered by the
// chosen cell's rank, then id. No scope name is spelt.
func RefreshTargets(snaps []Snapshot, caller scope.Scope) []RefreshTarget {
	type acct struct{ own, ownUnusable, other []int }
	accts := map[string]*acct{}
	byLetter := map[string]string{} // Account letter → accountUuid, from every usable cell (internal)
	var order []string
	usable := func(s Snapshot) bool { return s.Err == nil && s.AccountUUID != "" }
	get := func(uuid string) *acct {
		a := accts[uuid]
		if a == nil {
			a = &acct{}
			accts[uuid] = a
			order = append(order, uuid)
		}
		return a
	}
	for i, s := range snaps {
		if !usable(s) {
			continue
		}
		if s.Cell.Account != "" {
			byLetter[s.Cell.Account] = s.AccountUUID // hidden cells too: the link only, never shown
		}
		if !visible(caller, s.Cell) {
			continue
		}
		a := get(s.AccountUUID)
		if ownScope(caller, s.Cell) {
			a.own = append(a.own, i)
		} else {
			a.other = append(a.other, i)
		}
	}
	for i, s := range snaps {
		if usable(s) || s.Cell.Account == "" || !visible(caller, s.Cell) || !ownScope(caller, s.Cell) {
			continue
		}
		if uuid, ok := byLetter[s.Cell.Account]; ok {
			a := get(uuid) // may be new: the account's only usable cells are hidden
			a.ownUnusable = append(a.ownUnusable, i)
		}
	}
	best := func(idx []int) int {
		b := idx[0]
		for _, i := range idx[1:] {
			if rankBefore(snaps[i].Cell, snaps[b].Cell) {
				b = i
			}
		}
		return b
	}
	out := make([]RefreshTarget, 0, len(order))
	for _, uuid := range order {
		a := accts[uuid]
		var i int
		reason := ""
		switch {
		case len(a.own) > 0:
			i = best(a.own)
		case len(a.ownUnusable) > 0:
			i, reason = best(a.ownUnusable), NoUsableCell
		default:
			i = best(a.other)
		}
		s := snaps[i]
		label := s.Cell.Account
		if label == "" {
			label = "other-" + short(uuid)
		}
		out = append(out, RefreshTarget{AccountUUID: uuid, Label: label, Cell: s.Cell, Before: s, Reason: reason})
	}
	sort.SliceStable(out, func(i, j int) bool { return rankBefore(out[i].Cell, out[j].Cell) })
	return out
}

func ownScope(caller scope.Scope, c CellInput) bool {
	sc, err := scope.Parse(c.Scope)
	return err == nil && sc == caller
}

func rankBefore(a, b CellInput) bool {
	switch {
	case a.Rank != nil && b.Rank != nil && *a.Rank != *b.Rank:
		return *a.Rank < *b.Rank
	case (a.Rank == nil) != (b.Rank == nil):
		return a.Rank != nil
	}
	return a.ID < b.ID
}

// FreshWithin: Claude Code does not rewrite an account's usage cache read
// less than 60 s ago (vnt=60000, Claude Code 2.1.292). Such a cell is fresh.
const FreshWithin = 60 * time.Second

// LaunchFunc runs claude -p /usage on one cell (its config dir) and returns
// when it exits. Injected: this package never starts a process.
type LaunchFunc func(ctx context.Context, configDir string) error

type RefreshDeps struct {
	Launch   LaunchFunc
	ReadFile func(string) ([]byte, error)
	Now      func() time.Time
	Timeout  time.Duration       // per account
	Progress func(RefreshTarget) // called before each launch; may be nil
}

type RefreshStatus string

const (
	RefreshOK      RefreshStatus = "refreshed"
	RefreshFresh   RefreshStatus = "fresh"
	RefreshFailed  RefreshStatus = "failed"
	RefreshSkipped RefreshStatus = "skipped"
)

// RefreshResult is one account's refresh, shown on the account (text and JSON).
type RefreshResult struct {
	Status        RefreshStatus `json:"status"`
	Cell          string        `json:"cell,omitempty"`
	Error         string        `json:"error,omitempty"`
	FetchedBefore *time.Time    `json:"fetched_before"`
	FetchedAfter  *time.Time    `json:"fetched_after"`
	AccountUUID   string        `json:"-"`
	Label         string        `json:"-"` // the target's Label: names a failure whose account left the report
}

// RefreshFailure is a failed refresh whose account is not in the report.
type RefreshFailure struct {
	Label       string `json:"label"`
	AccountUUID string `json:"account_uuid"`
	RefreshResult
}

// Refresh launches the targets one after the other (never two at once: a
// concurrent rewrite of .claude.json is not proven safe) and says, per
// account, whether its cache moved. A failure never stops the next account.
func Refresh(ctx context.Context, targets []RefreshTarget, d RefreshDeps) []RefreshResult {
	out := make([]RefreshResult, 0, len(targets))
	for _, tg := range targets {
		res := RefreshResult{AccountUUID: tg.AccountUUID, Cell: tg.Cell.ID, Label: tg.Label}
		var before time.Time
		if tg.Before.HasReading {
			before = tg.Before.FetchedAt
			b := before
			res.FetchedBefore = &b
		}
		switch {
		case tg.Reason != "": // S2 case 2: no launch, no fallback
			res.Status, res.Error = RefreshFailed, tg.Reason
			out = append(out, res)
			continue
		case tg.Before.HasReading && d.Now().UTC().Sub(before) < FreshWithin:
			res.Status = RefreshFresh
			a := before // not launched: the reading after is the one before
			res.FetchedAfter = &a
			out = append(out, res)
			continue
		}
		if d.Progress != nil {
			d.Progress(tg)
		}
		lctx, cancel := context.WithTimeout(ctx, d.Timeout)
		err := d.Launch(lctx, tg.Cell.ConfigDir)
		timedOut := err != nil && errors.Is(lctx.Err(), context.DeadlineExceeded)
		cancel()
		// Re-read after every launch, failed or not: fetched_after says what
		// the cell holds now (null only when it holds no reading).
		after := ReadCell(tg.Cell, d.ReadFile)
		if after.Err == nil && after.HasReading {
			a := after.FetchedAt
			res.FetchedAfter = &a
		}
		res.Status = RefreshFailed
		switch {
		case timedOut:
			res.Error = timeoutReason(d.Timeout)
		case err != nil:
			res.Error = err.Error()
		case after.Err != nil:
			res.Error = "re-read failed: " + after.Err.Error()
		case after.Mismatch:
			res.Error = fmt.Sprintf("usage cache written for another account (%s)", short(after.CacheUUID))
		case after.HasReading && (!tg.Before.HasReading || after.FetchedAt.After(before)):
			res.Status = RefreshOK
		default:
			res.Error = "usage cache unchanged after claude -p /usage"
		}
		out = append(out, res)
	}
	return out
}

func timeoutReason(d time.Duration) string {
	return fmt.Sprintf("timed out after %ds", int(d/time.Second))
}

// AttachRefresh sets every account's refresh: its result, or skipped when no
// cell of the account was visible to the caller. A failed result
// whose account is not in the report (its only cell unreadable or logged out
// at the final read) goes to RefreshFailures: never dropped.
func (r *Report) AttachRefresh(res []RefreshResult) {
	by := map[string]RefreshResult{}
	for _, x := range res {
		by[x.AccountUUID] = x
	}
	listed := map[string]bool{}
	for i := range r.Accounts {
		listed[r.Accounts[i].AccountUUID] = true
		x, ok := by[r.Accounts[i].AccountUUID]
		if !ok {
			x = RefreshResult{Status: RefreshSkipped, Error: "no visible cell", AccountUUID: r.Accounts[i].AccountUUID}
		}
		r.Accounts[i].Refresh = &x
	}
	for _, x := range res {
		if !listed[x.AccountUUID] && x.Status == RefreshFailed {
			r.RefreshFailures = append(r.RefreshFailures, RefreshFailure{Label: x.Label, AccountUUID: x.AccountUUID, RefreshResult: x})
		}
	}
}

// RefreshCounts: results that failed, out of all results (Refresh never
// returns skipped). Counted on the results, not on the report: an account
// that left the report still counts.
func RefreshCounts(res []RefreshResult) (failed, attempted int) {
	for _, x := range res {
		if x.Status == RefreshFailed {
			failed++
		}
	}
	return failed, len(res)
}

// StaleTargets keeps the targets the background sweep must launch: those
// whose account has no reading, or one older than olderThan — judged on the
// account's reading (accounts from Build in the master view: the newest of
// all its cells, build.go), never on the target cell's own cache (Before):
// mp stale but mt of the same account fresh = nothing to do. A target with a
// Reason is kept: Refresh fails it without a launch, and the sweep says so.
func StaleTargets(targets []RefreshTarget, accounts []Account, now time.Time, olderThan time.Duration) []RefreshTarget {
	fetched := make(map[string]time.Time, len(accounts))
	for _, a := range accounts {
		if a.Reading != nil {
			fetched[a.AccountUUID] = a.Reading.FetchedAt
		}
	}
	now = now.UTC()
	out := make([]RefreshTarget, 0, len(targets))
	for _, tg := range targets {
		at, ok := fetched[tg.AccountUUID]
		if tg.Reason != "" || !ok || now.Sub(at) > olderThan {
			out = append(out, tg)
		}
	}
	return out
}

// BackgroundStateName is the daemon sweep's last pass, in pigtail's state
// directory (next to spawns.jsonl): what pigtail usage and the rotation's
// stale warning read to say a background refresh failed.
const BackgroundStateName = "usage-refresh.json"

// BackgroundState is one pass of the daemon's background refresh. Skipped
// ("locked": another refresh held the lock) and Error (the pass could not
// run: no cell table, lock error) keep the previous pass's Results, and
// ResultsAt, the instant of the pass that produced them.
type BackgroundState struct {
	At        time.Time          `json:"at"`
	Skipped   string             `json:"skipped,omitempty"`
	Error     string             `json:"error,omitempty"`
	ResultsAt *time.Time         `json:"results_at"`
	Results   []BackgroundResult `json:"results"`
}

// BackgroundResult is one account's background refresh. Cell is seen from
// the master scope: a reader shows it only when its own scope sees the cell
// .
type BackgroundResult struct {
	Label        string        `json:"label"`
	AccountUUID  string        `json:"account_uuid"`
	Cell         string        `json:"cell"`
	CellScope    string        `json:"cell_scope"`
	Status       RefreshStatus `json:"status"`
	Error        string        `json:"error,omitempty"`
	FetchedAfter *time.Time    `json:"fetched_after"`
}
