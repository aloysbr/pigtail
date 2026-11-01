package pigtail

import (
	"context"
	"net/url"
	"time"
)

// Standby (sub_x000000000000041, D4, D16): a session parked by /standby. It
// wrote its handoff, pigtail compacts it, the cmux sidebar files it in the
// folded « Standby » folder, and only a prompt the user types ends it.

// EventSessionStandbyChanged is published when a session enters or leaves
// Standby: {session_id, cmux_workspace_id, standby}. Tagged with the
// session's scope.
const EventSessionStandbyChanged EventType = "session.standby_changed"

// EventSessionStandbyCompactFailed is published when the /compact pigtail
// types in a standby session is still not proven after its last send:
// {session_id, cmux_workspace_id, tries, reason}. Tagged with the session's
// scope.
const EventSessionStandbyCompactFailed EventType = "session.standby_compact_failed"

// SessionStandby is a session's Standby row (migration 0023).
type SessionStandby struct {
	SessionID string    `json:"session_id"`
	Since     time.Time `json:"since"`
	// CompactSentAt: when pigtail last typed /compact (nil = not yet).
	CompactSentAt *time.Time `json:"compact_sent_at,omitempty"`
	// CompactStatusAt: the native statusUpdatedAt read right before that send.
	CompactStatusAt int64 `json:"compact_status_at,omitempty"`
	// CompactPID: the claude pid the /compact was sent to (proof on it only).
	CompactPID   int `json:"compact_pid,omitempty"`
	CompactTries int `json:"compact_tries,omitempty"`
	// CompactedAt: the session left idle after the send (the proof).
	CompactedAt *time.Time `json:"compacted_at,omitempty"`
	// CompactFailedAt: given up after CompactTries sends without proof.
	CompactFailedAt *time.Time `json:"compact_failed_at,omitempty"`
	// MissingTicks: successful cmux listings in a row without its workspace.
	MissingTicks int `json:"missing_ticks,omitempty"`
	// FoldersOff, on the PUT answer only (not stored): why the cmux folder
	// arrangement will not file the tab in « Standby » —
	// "pigtaild --cmux-folders=false" or "cmux sidebar off". "" = it will, or
	// the daemon cannot tell (no sidebar loop, an older daemon).
	FoldersOff string `json:"folders_off,omitempty"`
}

// StandbyWorkspace is a Standby row with the cmux workspace its session
// registered on (upper case; "" when it never registered one) and its count of
// successful cmux listings in a row without that workspace.
type StandbyWorkspace struct {
	SessionID       string `json:"session_id"`
	CmuxWorkspaceID string `json:"cmux_workspace_id,omitempty"`
	MissingTicks    int    `json:"missing_ticks,omitempty"`
}

// SetSessionStandby calls PUT /peers/{session}/standby: the session enters
// Standby. The caller acts as the session (sessionClient).
func (c *Client) SetSessionStandby(ctx context.Context, session string) (*SessionStandby, error) {
	var out SessionStandby
	if _, err := c.do(ctx, "PUT", "/peers/"+url.PathEscape(session)+"/standby", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearSessionStandby calls DELETE /peers/{session}/standby: the session
// leaves Standby. Clearing what is not there is a success.
func (c *Client) ClearSessionStandby(ctx context.Context, session string) error {
	_, err := c.do(ctx, "DELETE", "/peers/"+url.PathEscape(session)+"/standby", nil, nil)
	return err
}
