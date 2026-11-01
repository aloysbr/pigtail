// Package claudeusage reads the plan-usage cache Claude Code keeps in each
// cell's .claude.json (cachedUsageUtilization) and folds it into one view per
// Claude account. Pure: no network, no clock, no filesystem of its own — the
// caller injects readFile and now, so the CLI today and the daemon or
// Pigtail later read the same thing the same way.
package claudeusage

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

// FileName is the per-cell Claude config file, inside the cell's config dir.
// Only cells are read: the bare ~/.claude.json (no cell) never is.
const FileName = ".claude.json"

// CellInput is one cell of the pigtail cell table. Rank is its declaration
// index in the launcher config (nil when the table has none): it orders accounts,
// and is never shown. SelectPct / SwitchPct are the account's thresholds as
// the table declares them (nil = default, see Thresholds), BudgetPct its weekly
// budget (nil = default, see ResolveBudgetPct); kept out of the JSON of
// `usage pick --json`.
type CellInput struct {
	ID, ConfigDir, Scope, Account string
	Rank                          *int
	SelectPct                     *float64 `json:"-"`
	SwitchPct                     *float64 `json:"-"`
	BudgetPct                     *float64 `json:"-"`
}

// Window is one cached usage window: utilization 0-100 and its reset. A zero
// ResetsAt means absent or unreadable.
type Window struct {
	Percent  float64
	ResetsAt time.Time
}

// ScopedLimit is a per-model weekly limit (limits[kind=weekly_scoped]).
type ScopedLimit struct {
	Model    string     `json:"model"`
	Percent  float64    `json:"percent"`
	Severity string     `json:"severity"`
	ResetsAt *time.Time `json:"resets_at"`
}

// Snapshot is what one cell's .claude.json says about its account's usage.
type Snapshot struct {
	Cell         CellInput
	Err          error  // unreadable or invalid: never shown as 0 %
	AccountUUID  string // oauthAccount.accountUuid; "" = not logged in
	UserTier     string
	OrgTier      string
	HasReading   bool   // a cache written for AccountUUID
	Mismatch     bool   // a cache written for another account: dropped
	CacheUUID    string // the cache's accountUuid when Mismatch
	FetchedAt    time.Time
	SevenDay     *Window
	FiveHour     *Window
	WeeklyScoped []ScopedLimit
	ExtraUsage   json.RawMessage
	Spend        json.RawMessage
	CapReached   bool
	Warnings     []string
}

type rawFile struct {
	OAuthAccount *struct {
		AccountUUID string  `json:"accountUuid"`
		OrgTier     *string `json:"organizationRateLimitTier"`
		UserTier    *string `json:"userRateLimitTier"`
	} `json:"oauthAccount"`
	Cache *struct {
		FetchedAtMs int64  `json:"fetchedAtMs"`
		AccountUUID string `json:"accountUuid"`
		Utilization *struct {
			FiveHour   *rawWindow      `json:"five_hour"`
			SevenDay   *rawWindow      `json:"seven_day"`
			ExtraUsage json.RawMessage `json:"extra_usage"`
			Spend      json.RawMessage `json:"spend"`
			Limits     []rawLimit      `json:"limits"`
		} `json:"utilization"`
	} `json:"cachedUsageUtilization"`
	DisabledReason string `json:"cachedExtraUsageDisabledReason"`
}

type rawWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type rawLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	Severity string   `json:"severity"`
	ResetsAt *string  `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// ReadTable reads every cell, in the given order (callers pass cell-id order).
func ReadTable(cells []CellInput, readFile func(string) ([]byte, error)) []Snapshot {
	out := make([]Snapshot, 0, len(cells))
	for _, c := range cells {
		out = append(out, ReadCell(c, readFile))
	}
	return out
}

// ReadCell reads <c.ConfigDir>/.claude.json and nothing else.
func ReadCell(c CellInput, readFile func(string) ([]byte, error)) Snapshot {
	s := Snapshot{Cell: c}
	path := filepath.Join(c.ConfigDir, FileName)
	b, err := readFile(path)
	if err != nil {
		s.Err = fmt.Errorf("read %s: %w", path, err)
		return s
	}
	var f rawFile
	if err := json.Unmarshal(b, &f); err != nil {
		s.Err = fmt.Errorf("parse %s: %w", path, err)
		return s
	}
	if f.OAuthAccount == nil || f.OAuthAccount.AccountUUID == "" {
		return s
	}
	s.AccountUUID = f.OAuthAccount.AccountUUID
	s.UserTier = deref(f.OAuthAccount.UserTier)
	s.OrgTier = deref(f.OAuthAccount.OrgTier)
	if f.Cache == nil || f.Cache.Utilization == nil {
		return s
	}
	if f.Cache.AccountUUID != s.AccountUUID {
		s.Mismatch, s.CacheUUID = true, f.Cache.AccountUUID
		return s
	}
	u := f.Cache.Utilization
	s.HasReading = true
	s.FetchedAt = time.UnixMilli(f.Cache.FetchedAtMs).UTC()
	s.SevenDay = s.window("seven_day", u.SevenDay)
	s.FiveHour = s.window("five_hour", u.FiveHour)
	s.WeeklyScoped = []ScopedLimit{}
	for _, l := range u.Limits {
		if l.Kind != "weekly_scoped" || l.Percent == nil {
			continue
		}
		sl := ScopedLimit{Percent: *l.Percent, Severity: l.Severity}
		if l.Scope != nil && l.Scope.Model != nil {
			sl.Model = l.Scope.Model.DisplayName
		}
		if l.ResetsAt != nil {
			if t, err := time.Parse(time.RFC3339Nano, *l.ResetsAt); err == nil {
				t = t.UTC()
				sl.ResetsAt = &t
			}
		}
		s.WeeklyScoped = append(s.WeeklyScoped, sl)
	}
	s.ExtraUsage, s.Spend = u.ExtraUsage, u.Spend
	s.CapReached = capReached(f.DisabledReason, u.ExtraUsage, u.Spend)
	return s
}

func (s *Snapshot) window(name string, w *rawWindow) *Window {
	if w == nil || w.Utilization == nil {
		return nil
	}
	out := &Window{Percent: *w.Utilization}
	if w.ResetsAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *w.ResetsAt)
		if err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("cell %s: %s.resets_at %q unreadable — reset unknown", s.Cell.ID, name, *w.ResetsAt))
		} else {
			out.ResetsAt = t.UTC()
		}
	}
	return out
}

// capReached: the credits cap is hit (a Team seat). A Max
// account's org_level_disabled only means extra usage is off.
func capReached(reason string, extra, spend json.RawMessage) bool {
	if reason == "org_spend_cap_reached" {
		return true
	}
	var e struct {
		SpendLimitReached bool `json:"spend_limit_reached"`
	}
	if json.Unmarshal(extra, &e) == nil && e.SpendLimitReached {
		return true
	}
	var sp struct {
		Enabled  bool   `json:"enabled"`
		Severity string `json:"severity"`
	}
	return json.Unmarshal(spend, &sp) == nil && sp.Enabled && sp.Severity == "critical"
}

var tierCapacity = map[string]int{
	"default_claude_max_20x": 20,
	"default_claude_max_5x":  5,
}

// Capacity is the account's ×N: userRateLimitTier when set — a Team seat
// carries its ×5 there, its org tier (default_raven) says nothing — else
// organizationRateLimitTier. An unknown tier is never guessed: ok=false.
func Capacity(userTier, orgTier string) (n int, tier string, ok bool) {
	tier = orgTier
	if userTier != "" {
		tier = userTier
	}
	n, ok = tierCapacity[tier]
	return n, tier, ok
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
