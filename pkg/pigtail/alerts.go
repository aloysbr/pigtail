package pigtail

import (
	"context"
	"net/url"
	"time"
)

// The alert kinds. A respawn alert is a `pigtail session respawn` run that
// gave up or failed (subject « abandon respawn visible », D3). A disk alert is
// the machine's free space under the alert threshold: it belongs to no scope,
// subject or session, and every caller reads it.
const (
	AlertKindRespawn = "respawn"
	AlertKindDisk    = "disk"
)

// AlertKindRole is a role alert (role supervision): a declared role missed its
// deadline or stalled. Its ID is the journal id "role-silent:<name>:<ms>".
const AlertKindRole = "role"

// AlertsResponse is the body of GET /alerts (docs/wire-protocol.md § Alerts):
// what pigtail itself raises for the user and nobody has resolved yet, oldest
// first. Alerts is never null.
type AlertsResponse struct {
	Alerts []AlertView `json:"alerts"`
	ReadAt time.Time   `json:"read_at"`
}

// AlertView is one open alert. ID is the run id (for a disk alert, the id of
// its journal line). Scope is empty for a disk alert. SubjectTitle is the
// subject's short title, empty for a caller that does not read the subject's
// scope (D23). Detail is the run's detail as the journal keeps it.
type AlertView struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Scope        string `json:"scope,omitempty"`
	SubjectID    string `json:"subject_id,omitempty"`
	SubjectTitle string `json:"subject_title,omitempty"`
	Detail       string `json:"detail"`
	// FreeBytes and FloorBytes are set on a disk alert only: the free space
	// when it opened and the admission floor then (0 = the gate was off).
	FreeBytes  uint64    `json:"free_bytes,omitempty"`
	FloorBytes uint64    `json:"floor_bytes,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	AgeSeconds int       `json:"age_seconds"`
}

// Alerts reads GET /alerts.
func (c *Client) Alerts(ctx context.Context) (*AlertsResponse, error) {
	var out AlertsResponse
	if _, err := c.do(ctx, "GET", "/alerts", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AlertAckRequest is the body of POST /alerts/{id}/ack. SessionID names the
// session that took the alert in hand (D5); empty = acknowledged by hand.
type AlertAckRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

// AlertResolution answers an ack. Resolved is false when the alert was
// resolved already; Summary is then the first resolution's.
type AlertResolution struct {
	ID       string `json:"id"`
	Resolved bool   `json:"resolved"`
	Summary  string `json:"summary"`
}

// AlertResolveRequest is the body of POST /alerts/resolve: a relaunch of
// SubjectID succeeded (D5); Cause says which (one line, 1..120 bytes).
type AlertResolveRequest struct {
	SubjectID string `json:"subject_id"`
	Cause     string `json:"cause"`
}

// AlertsResolved lists the alerts a resolve closed (never null).
type AlertsResolved struct {
	IDs []string `json:"ids"`
}

// AckAlert acknowledges the alert id (POST /alerts/{id}/ack); sessionID
// names the session that took it in hand, empty = by hand.
func (c *Client) AckAlert(ctx context.Context, id, sessionID string) (*AlertResolution, error) {
	var out AlertResolution
	if _, err := c.do(ctx, "POST", "/alerts/"+url.PathEscape(id)+"/ack", AlertAckRequest{SessionID: sessionID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveSubjectAlerts closes the open alerts of subjectID after a
// successful relaunch (POST /alerts/resolve).
func (c *Client) ResolveSubjectAlerts(ctx context.Context, subjectID, cause string) (*AlertsResolved, error) {
	var out AlertsResolved
	if _, err := c.do(ctx, "POST", "/alerts/resolve", AlertResolveRequest{SubjectID: subjectID, Cause: cause}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
