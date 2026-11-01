package pigtail

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// StaffingMode is how the staffing scheduler acts (sub_x000000000000004, D6):
// off does nothing, advise journals what it would launch, launch launches.
type StaffingMode string

// Staffing modes.
const (
	StaffingOff    StaffingMode = "off"
	StaffingAdvise StaffingMode = "advise"
	StaffingLaunch StaffingMode = "launch"
)

// EventStaffingDecision carries one line the scheduler wrote to its decision
// journal (a StaffingDecision). It names no scope: the handler masks.
const EventStaffingDecision EventType = "staffing.decision"

// Outcomes of a StaffingDecision.
const (
	StaffingOutcomeIdle            = "idle"
	StaffingOutcomeWouldLaunch     = "would_launch"
	StaffingOutcomeLaunchStarted   = "launch_started"
	StaffingOutcomeLaunchRefused   = "launch_refused"
	StaffingOutcomeLaunched        = "launched"
	StaffingOutcomeLaunchFailed    = "launch_failed"
	StaffingOutcomeSettingsChanged = "settings_changed"
	StaffingOutcomeObservedLaunch  = "observed_launch" // a launch made by another origin
)

// Gates of an idle StaffingDecision: why nothing left.
const (
	StaffingGateNoFreePlace       = "no_free_place"
	StaffingGateNoReadySubject    = "no_ready_subject"
	StaffingGateOverMargin        = "over_margin"
	StaffingGateOverBudget        = "over_budget"
	StaffingGateMaintenance       = "maintenance"
	StaffingGateSessionBudgetOff  = "session_budget_off"
	StaffingGateSessionBudgetFull = "session_budget_full"
	StaffingGateMachineBusy       = "machine_busy"
	StaffingGateBurst             = "burst"
	StaffingGateUnreadable        = "unreadable"
)

// Skips of a StaffingCandidate: why an eligible subject is not ready.
const (
	StaffingSkipHeld       = "held"
	StaffingSkipPhantom    = "phantom"
	StaffingSkipPaused     = "paused"
	StaffingSkipPending    = "pending"
	StaffingSkipWaitsHuman = "waits_human"
	StaffingSkipDeps       = "deps"
	StaffingSkipCooldown   = "cooldown"
	StaffingSkipOverMargin = "over_margin"
	StaffingSkipOverBudget = "over_budget"
)

// Values of StaffingState.Launcher: who launches the next subject (D5).
const (
	StaffingLauncherScheduler = "scheduler"
	StaffingLauncherPilot     = "pilot"
)

// StaffingSettings is a change asked of the scheduler; a zero field keeps
// the current value.
type StaffingSettings struct {
	Mode    StaffingMode `json:"mode,omitempty"`
	Ceiling *int         `json:"ceiling,omitempty"`
}

// StaffingCandidate is one eligible subject of a scope's queue. Skip "" =
// ready; otherwise one of the StaffingSkip… values, Detail its precision.
type StaffingCandidate struct {
	SubjectID  string `json:"subject_id"`
	ShortTitle string `json:"short_title"`
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	Rank       int    `json:"rank"`
	Skip       string `json:"skip"`
	Detail     string `json:"detail"`
}

// StaffingPlace is one place taken under the ceiling: a live session that
// works (native status other than idle/waiting, or inside the launch grace)
// bound to a subject, or a pending launch whose session has not registered yet.
type StaffingPlace struct {
	SessionID  string `json:"session_id"`
	SubjectID  string `json:"subject_id"`
	ShortTitle string `json:"short_title"`
	Scope      string `json:"scope"`
	Label      string `json:"label"`
	Wait       string `json:"wait"`
	LaunchID   string `json:"launch_id"`
	Pending    bool   `json:"pending"`
	Phantom    bool   `json:"phantom"`
}

// StaffingDecision is one line of the decision journal. Repeats counts the
// identical tours that followed it, LastAt the time of the last of them;
// Origin is set on an observed_launch.
type StaffingDecision struct {
	TS         time.Time           `json:"ts"`
	LastAt     time.Time           `json:"last_at"`
	Repeats    int                 `json:"repeats"`
	Mode       StaffingMode        `json:"mode"`
	PlacesUsed int                 `json:"places_used"`
	Ceiling    int                 `json:"ceiling"`
	Outcome    string              `json:"outcome"`
	Gate       string              `json:"gate,omitempty"`
	Reason     string              `json:"reason,omitempty"`
	SubjectID  string              `json:"subject_id,omitempty"`
	ShortTitle string              `json:"short_title,omitempty"`
	Scope      string              `json:"scope,omitempty"`
	LaunchID   string              `json:"launch_id,omitempty"`
	Origin     string              `json:"origin,omitempty"`
	Cause      string              `json:"cause,omitempty"`
	Skipped    []StaffingCandidate `json:"skipped,omitempty"`
}

// StaffingState is what GET /staffing answers: the settings, the places
// taken, the ready queues per scope, the last decision.
type StaffingState struct {
	Mode          StaffingMode                   `json:"mode"`
	Ceiling       int                            `json:"ceiling"`
	PlacesUsed    int                            `json:"places_used"`
	Places        []StaffingPlace                `json:"places"`
	Queues        map[string][]StaffingCandidate `json:"queues"`
	Last          *StaffingDecision              `json:"last"`
	CheckedAt     *time.Time                     `json:"checked_at"`
	Launcher      string                         `json:"launcher"`
	SettingsError string                         `json:"settings_error"`
}

// Staffing reads the scheduler's state (GET /staffing).
func (c *Client) Staffing(ctx context.Context) (StaffingState, error) {
	var out StaffingState
	_, err := c.do(ctx, "GET", "/staffing", nil, &out)
	return out, err
}

// SetStaffing changes the scheduler's settings (POST /staffing): the master
// only, with WithCrossScopeWrite.
func (c *Client) SetStaffing(ctx context.Context, in StaffingSettings) (StaffingState, error) {
	var out StaffingState
	_, err := c.do(ctx, "POST", "/staffing", in, &out)
	return out, err
}

// StaffingDecisions reads the decision journal (GET /staffing/decisions):
// a zero since and limit ≤ 0 take the daemon's defaults (24 h, 200 lines).
func (c *Client) StaffingDecisions(ctx context.Context, since time.Time, limit int) ([]StaffingDecision, error) {
	q := url.Values{}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/staffing/decisions"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []StaffingDecision
	_, err := c.do(ctx, "GET", path, nil, &out)
	return out, err
}
