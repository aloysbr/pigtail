package pigtail

import "time"

// SessionPause is one idle pause: pigtaild stopped the claude process of an
// idle session by SIGTERM, without closing it,
// so it can be resumed in its tab (Enter at the launcher's ↻, or
// `pigtail session resume`). The row goes when the same session registers
// alive again or another session takes its surface.
type SessionPause struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"` // the claude process signalled
	SurfaceID string `json:"surface_id"`
	// CmuxWorkspaceID is the cmux workspace UUID (upper case), never the
	// pigtail worktree (PeerSession.WorkspaceID): the sidebar pill is drawn
	// on it.
	CmuxWorkspaceID string `json:"cmux_workspace_id,omitempty"`
	// SidPath is the launcher's <key>.sid pigtaild wrote ("" = no launcher).
	SidPath string `json:"sid_path,omitempty"`
	State   string `json:"state"` // PauseStopping | PausePaused
	// EnteredStandby: the pause put the session in Standby (undone on failure).
	EnteredStandby bool       `json:"entered_standby,omitempty"`
	Since          time.Time  `json:"since"`
	SignaledAt     *time.Time `json:"signaled_at,omitempty"` // nil = no SIGTERM sent yet
	PausedAt       *time.Time `json:"paused_at,omitempty"`
}

// The states of a SessionPause.
const (
	PauseStopping = "stopping" // SIGTERM sent (or about to be), exit not yet seen
	PausePaused   = "paused"
)

// EventSessionPauseChanged is published when a session enters or leaves a
// pause: {session_id, cmux_workspace_id, paused}. Tagged with the session's
// scope.
const EventSessionPauseChanged EventType = "session.pause_changed"

// EventSessionPauseFailed is published when pigtaild gave up a pause (the
// SIGTERM failed, or the process outlived it): {session_id,
// cmux_workspace_id, reason}. Tagged with the session's scope.
const EventSessionPauseFailed EventType = "session.pause_failed"
