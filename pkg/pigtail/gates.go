package pigtail

import "time"

// GatesResponse is the body of GET /gates (docs/wire-protocol.md § GET /gates):
// the gates the user's gate tool declares, as the caller's scope may read them.
// Counts is absent whenever Error is set: an unreadable source is never a 0.
type GatesResponse struct {
	Configured          bool        `json:"configured"`
	Complete            bool        `json:"complete"`
	Error               *GatesError `json:"error,omitempty"`
	Counts              *GateCounts `json:"counts,omitempty"`
	Fired               []GateView  `json:"fired"`
	Open                []GateView  `json:"open"`
	LastCheckAt         *time.Time  `json:"last_check_at,omitempty"`
	LastCheckAgeSeconds *int        `json:"last_check_age_seconds,omitempty"`
	ReadAt              time.Time   `json:"read_at"`
}

// GatesError says why the gate source could not be read. Kind is stable:
// not_found, failed, timeout, invalid_output.
type GatesError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// GateCounts is the count Santé du system reads (D3).
type GateCounts struct {
	Fired  int `json:"fired"`
	Open   int `json:"open"`
	Broken int `json:"broken"`
}

// GateView is one gate. Scope is the canonical pigtail scope name.
type GateView struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Action          string     `json:"action"`
	Scope           string     `json:"scope"`
	Status          string     `json:"status"`
	AddedAt         time.Time  `json:"added_at"`
	FiredAt         *time.Time `json:"fired_at,omitempty"`
	FiredAgeSeconds *int       `json:"fired_age_seconds,omitempty"`
	SubjectID       string     `json:"subject_id,omitempty"`
	Value           string     `json:"value,omitempty"`
	Errors          int        `json:"errors"`
	NotBefore       string     `json:"not_before,omitempty"`
}
