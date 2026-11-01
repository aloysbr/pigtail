package pigtail

import (
	"context"
	"net/url"
	"time"
)

// SessionSample is one tick of the session sampler, as the daemon appends it
// to session-samples.jsonl and serves it on GET /session-budget/samples.
type SessionSample struct {
	TS time.Time `json:"ts"`
	// Live counts the sessions whose process is alive; Busy those among them
	// whose native status is busy (waiting and idle are not busy).
	Live int `json:"live"`
	Busy int `json:"busy"`
	// AvailableBytes and CompressorBytes are the memory sensor's levels;
	// CompressBytesPerSec is the compressor's traffic averaged over the last
	// 60 s, SwapoutBytesPerSec the swap-out rate over the same window.
	AvailableBytes      uint64  `json:"available_bytes"`
	CompressorBytes     uint64  `json:"compressor_bytes"`
	CompressBytesPerSec float64 `json:"compress_bytes_per_sec"`
	SwapoutBytesPerSec  float64 `json:"swapout_bytes_per_sec"`
	// Sessions is one row per live session.
	Sessions []SessionSampleRow `json:"sessions"`
}

// SessionSampleRow is one live session in a SessionSample.
type SessionSampleRow struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	Cwd       string `json:"cwd,omitempty"`
	// Scope is the session's canonical scope; "" = none registered (seen by
	// the master only).
	Scope     string `json:"scope,omitempty"`
	Repo      string `json:"repo,omitempty"`
	SubjectID string `json:"subject_id,omitempty"`
	// Status is busy|idle|waiting (the native status; a native shell is idle).
	Status         string  `json:"status"`
	FootprintBytes uint64  `json:"footprint_bytes"`
	CPUPercent     float64 `json:"cpu_percent"`
}

// SessionSamples is the body of GET /session-budget/samples.
type SessionSamples struct {
	Samples []SessionSample `json:"samples"`
}

// SessionCostType is one line of the cost report: a repo and a status.
type SessionCostType struct {
	Repo         string  `json:"repo"`
	Status       string  `json:"status"`
	Sessions     int     `json:"sessions"`
	CPUAvgPct    float64 `json:"cpu_avg_percent"`
	MemoryAvgGB  float64 `json:"memory_avg_gb"`
	MemoryPeakGB float64 `json:"memory_peak_gb"`
}

// SessionCost is the report `pigtail session cost` prints (and --json emits).
type SessionCost struct {
	Window  string            `json:"window"`
	Samples int               `json:"samples"`
	Types   []SessionCostType `json:"types"`
}

// SessionSamples returns the samples taken at or after since
// (GET /session-budget/samples), rows of other scopes dropped for a
// non-master caller.
func (c *Client) SessionSamples(ctx context.Context, since time.Time) (SessionSamples, error) {
	var out SessionSamples
	path := "/session-budget/samples?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// SessionBudgetState is the body of GET /session-budget: the gate's state
// for an agent ask, as aggregate counts (no session row, no queue).
type SessionBudgetState struct {
	// Enabled is false when the gate is off (ceiling 0): every ask admitted.
	Enabled bool `json:"enabled"`
	// Busy is n: the last sample's busy sessions plus the pending
	// admissions (spawns and wakes); Budget is m of the moment.
	Busy   int `json:"busy"`
	Budget int `json:"budget"`
	// Gate is "open" or "closed"; Reason the closed gate's D8 label.
	Gate          string        `json:"gate"`
	Reason        string        `json:"reason"`
	DeferredWakes DeferredWakes `json:"deferred_wakes"`
}

// DeferredWakes is the relay's last-pass gauge of the wakes it deferred for
// budget (a display gauge, never a queue).
type DeferredWakes struct {
	Count            int `json:"count"`
	OldestAgeSeconds int `json:"oldest_age_seconds"`
}

// SessionLeaseRequest is the body of POST /session-budget/leases.
type SessionLeaseRequest struct {
	// Kind is human or agent; an agent ask must set NoWait.
	Kind   string `json:"kind"`
	Origin string `json:"origin,omitempty"`
	Repo   string `json:"repo,omitempty"`
	NoWait bool   `json:"no_wait,omitempty"`
}

// SessionLeaseAnswer is the one answer of POST /session-budget/leases.
type SessionLeaseAnswer struct {
	Admitted bool   `json:"admitted"`
	Busy     int    `json:"busy"`
	Budget   int    `json:"budget"`
	Reason   string `json:"reason"`
	// OverBudget is set on a human answer only: admitted while n ≥ m.
	OverBudget *bool `json:"over_budget,omitempty"`
	// LeaseID names an admitted agent lease, to give back or report launched.
	LeaseID string `json:"lease_id,omitempty"`
}

// SessionLeaseRelease is the body of POST /session-budget/leases/release.
type SessionLeaseRelease struct {
	LeaseID string `json:"lease_id"`
	// Launched reports that the spawn launched Claude (the place is kept
	// until the next sample) instead of giving the place back.
	Launched bool `json:"launched,omitempty"`
}

// SessionBudget reads the session gate's state (GET /session-budget).
func (c *Client) SessionBudget(ctx context.Context) (SessionBudgetState, error) {
	var out SessionBudgetState
	_, err := c.do(ctx, "GET", "/session-budget", nil, &out)
	return out, err
}

// AdmitSessionNow asks the session gate once, without waiting
// (POST /session-budget/leases): admitted or refused, never queued.
func (c *Client) AdmitSessionNow(ctx context.Context, req SessionLeaseRequest) (SessionLeaseAnswer, error) {
	var out SessionLeaseAnswer
	_, err := c.do(ctx, "POST", "/session-budget/leases", req, &out)
	return out, err
}

// ReleaseSessionLease gives an admitted lease back, or with launched reports
// that the spawn launched Claude (POST /session-budget/leases/release).
func (c *Client) ReleaseSessionLease(ctx context.Context, leaseID string, launched bool) error {
	_, err := c.do(ctx, "POST", "/session-budget/leases/release", SessionLeaseRelease{LeaseID: leaseID, Launched: launched}, nil)
	return err
}
