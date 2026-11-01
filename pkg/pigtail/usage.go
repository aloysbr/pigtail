package pigtail

import "time"

// ErrUsageUnavailable: GET /usage/overview cannot answer — not wired (501)
// or the cell table / readings could not be read (503).
const ErrUsageUnavailable ErrorCode = "USAGE_UNAVAILABLE"

// UsageWindow is one usage window as Pigtail shows it.
type UsageWindow struct {
	// Percent is the shown percent: rounded, 0 once the reset has passed.
	Percent  int        `json:"percent"`
	ResetsAt *time.Time `json:"resets_at"`
	// Full: the raw percent is at or above the account's selection ceiling
	// (the same for 5 h, 7 days and per-model) and the reset has not passed.
	Full bool `json:"full"`
}

// UsageScoped is a per-model weekly limit (Fable, …).
type UsageScoped struct {
	Model string `json:"model"`
	UsageWindow
}

// UsageAccount is one Claude account of GET /usage/overview. It never names
// a cell (D19 of the rotation): AccountUUID is the account's own id.
type UsageAccount struct {
	// AccountUUID is the key next_session[] designates an account by (I11).
	AccountUUID string `json:"account_uuid"`
	// Label is the account's letter, else other-<short uuid>.
	Label string `json:"label"`
	// InVerdict: a master-scope account with a 7-day reading and no credits
	// cap reached — counted in the verdict.
	InVerdict bool `json:"in_verdict"`
	// Unusable: "no-reading" | "cap" | "" (first that holds).
	Unusable  string        `json:"unusable"`
	FetchedAt *time.Time    `json:"fetched_at"`
	SevenDay  *UsageWindow  `json:"seven_day"`
	FiveHour  *UsageWindow  `json:"five_hour"`
	Scoped    []UsageScoped `json:"scoped"`
	// WindowLeft is the share of the 7-day window still to run, 0..1; a
	// passed reset counts as a fresh window. nil without a 7-day reset.
	WindowLeft *float64 `json:"window_left"`
	// Fast: the 7-day availability is more than 5 points below WindowLeft.
	Fast bool `json:"fast"`
	// RefreshFailed: the daemon's last background refresh of the account failed.
	RefreshFailed bool `json:"refresh_failed"`
	// HistorySince is the oldest sample of the account the daemon keeps
	// (≤ 72 h); nil without history.
	HistorySince *time.Time `json:"history_since"`
}

// UsageLabelAt is an account letter and an instant.
type UsageLabelAt struct {
	Label string    `json:"label"`
	At    time.Time `json:"at"`
}

// UsageVerdict says whether the week holds on the accounts.
type UsageVerdict struct {
	State           string        `json:"state"` // "ok" | "tight" | "later" | "now" | "none"
	Provisional     bool          `json:"provisional"`
	OldestReadingAt *time.Time    `json:"oldest_reading_at"`
	NextRenewal     *UsageLabelAt `json:"next_renewal"` // ok, tight
	BlockedFrom     *time.Time    `json:"blocked_from"` // later
	Frees           *UsageLabelAt `json:"frees"`        // later, now; nil = unknown
}

// UsageScopePick is the account the next session of a scope starts on.
type UsageScopePick struct {
	Scope   string `json:"scope"`   // canonical name, never an alias
	Display string `json:"display"` // the scope's first alias in the table, else its name
	Master  bool   `json:"master"`
	// AccountUUID designates an entry of accounts[] (I11); the label shown
	// is that entry's.
	AccountUUID string `json:"account_uuid,omitempty"`
	// Error: "no-candidate" only — never a cell id.
	Error string `json:"error,omitempty"`
}

// UsageOverview is GET /usage/overview: the master's view of the Claude
// accounts, for Pigtail.
type UsageOverview struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Accounts    []UsageAccount   `json:"accounts"`
	Verdict     *UsageVerdict    `json:"verdict"`
	NextSession []UsageScopePick `json:"next_session"`
	// UnreadableCount is the number of cells whose .claude.json could not be
	// read or parsed (I13); absent at 0. Never an id, a path or an error text.
	UnreadableCount int `json:"unreadable_count,omitempty"`
}
