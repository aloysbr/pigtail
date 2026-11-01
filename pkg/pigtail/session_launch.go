package pigtail

import "time"

// SessionLaunch is one session launch on a subject (sub_x000000000000015):
// pigtaild opened a cmux tab running the launcher, then waits for the
// session to register. Every field is always on the wire; an empty string
// means unknown. Contract: docs/wire-protocol.md § Session launch.
type SessionLaunch struct {
	LaunchID       string      `json:"launch_id"`
	SubjectID      string      `json:"subject_id"`
	State          LaunchState `json:"state"`
	Cause          string      `json:"cause"` // set for LaunchFailed only
	Origin         string      `json:"origin"`
	WorkspaceID    string      `json:"workspace_id"`
	WorkspaceRef   string      `json:"workspace_ref"`
	Cell           string      `json:"cell"`
	PermissionMode string      `json:"permission_mode"`
	Cwd            string      `json:"cwd"`
	// TabTitle is the name the launcher gave the cmux tab: the subject's
	// ShortTitle. Set on the POST answer (new or deduplicated) only; empty
	// on GET /launches/{id} and the SSE event, which read the registry.
	TabTitle  string    `json:"tab_title"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Deduplicated: the answer is an API launch already in flight for the
	// subject, not a new one. Never set on the SSE event.
	Deduplicated bool `json:"deduplicated"`
}

// LaunchState is the state of a SessionLaunch.
type LaunchState string

// The states of a SessionLaunch.
const (
	LaunchPending  LaunchState = "pending"  // tab created, no session registered yet
	LaunchLaunched LaunchState = "launched" // the session registered on the tab
	LaunchFailed   LaunchState = "failed"   // see Cause
)

// EventSessionLaunchChanged is published when a launch is pending (tab
// created), then launched or failed: data SessionLaunch without
// deduplicated. Tagged with the subject's scope.
const EventSessionLaunchChanged EventType = "session.launch_changed"
