package pigtail

import "time"

// Lid error codes (pigtail lid: keep the Mac awake with the lid closed).
const (
	// ErrLidNotSetUp: sudo refuses pmset without a password — the sudoers
	// rule of `pigtail lid setup` is missing (412).
	ErrLidNotSetUp ErrorCode = "LID_NOT_SET_UP"
	// ErrLidUnavailable: sleep control is not wired (not macOS) (501).
	ErrLidUnavailable ErrorCode = "LID_UNAVAILABLE"
)

// EventLidChanged is published at every Travel Mode state change (POST /lid,
// auto-off by deadline, battery, heat with the lid closed or an outside
// pmset, a failed auto-off);
// payload {on, reason}. Unscoped: every subscriber gets it.
const EventLidChanged EventType = "lid.changed"

// LidRequest is the body of POST /lid.
type LidRequest struct {
	On    bool `json:"on"`
	Hours int  `json:"hours,omitempty"` // 0 with on=true = lid.DefaultHours
}

// LidOff is the last time sleep control was turned off, and why.
type LidOff struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"` // "deadline" | "battery N%" | "thermal serious" | "thermal critical" | "manual" | "external"
}

// LidResponse is the answer of GET and POST /lid.
type LidResponse struct {
	SleepDisabled bool       `json:"sleep_disabled"`
	Deadline      *time.Time `json:"deadline,omitempty"` // nil = no pigtail timer
	BatteryFloor  int        `json:"battery_floor"`
	LastOff       *LidOff    `json:"last_off,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	// GuardError: a guard reading (battery, thermal, lid) failed at the last
	// tick, mode on ("battery|thermal|lid reading failed: …", joined by "; ").
	GuardError string `json:"guard_error,omitempty"`
	// ThermalState: the last thermal reading, mode on ("nominal" | "fair" |
	// "serious" | "critical"); absent off or never read.
	ThermalState string `json:"thermal_state,omitempty"`
	// LidClosed: the last lid reading, mode on; absent = unknown (reading
	// failed or never read) or mode off. Present false = lid open.
	LidClosed *bool `json:"lid_closed,omitempty"`
}

// LidProbeResponse is the answer of POST /lid/probe.
type LidProbeResponse struct {
	RuleOK  bool   `json:"rule_ok"`           // D7 probe run by pigtaild itself, under the manager lock
	Warning string `json:"warning,omitempty"` // "sleep is disabled with no timer — run pigtail lid off"
}
