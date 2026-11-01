package pigtail

import (
	"context"
	"time"
)

// RuntimeClaimView is GET /runtime/claim: the lease on the installed runtime
// as the daemon reads it from its file — never the holder's label nor the
// reason (D23: the CLI reads those from the file itself). Unreadable carries
// the cause when the file exists but cannot be read.
type RuntimeClaimView struct {
	Dir        string    `json:"dir"`
	Present    bool      `json:"present"`
	Unreadable string    `json:"unreadable,omitempty"`
	ID         string    `json:"id,omitempty"`
	Session    string    `json:"session,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	SHA        string    `json:"sha,omitempty"`
	Dirty      bool      `json:"dirty,omitempty"`
	PR         int       `json:"pr,omitempty"`
	Since      time.Time `json:"since,omitempty"`
	Liveness   string    `json:"liveness,omitempty"`
	// ExpiresAt is the lease's deadline (4 h from its holder's last claim).
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// RuntimeClaimRequest is POST /runtime/claim. Session "" = the user outside
// Claude Code.
type RuntimeClaimRequest struct {
	Session    string `json:"session,omitempty"`
	Label      string `json:"label,omitempty"`
	LabelScope string `json:"label_scope,omitempty"`
	PID        int    `json:"pid,omitempty"`
	ConfigDir  string `json:"config_dir,omitempty"`
	Branch     string `json:"branch,omitempty"`
	SHA        string `json:"sha,omitempty"`
	Dirty      bool   `json:"dirty,omitempty"`
	PR         int    `json:"pr,omitempty"`
	Reason     string `json:"reason"`
	TakeOver   bool   `json:"take_over,omitempty"`
}

// RuntimeReleaseRequest is POST /runtime/release.
type RuntimeReleaseRequest struct {
	Session string `json:"session,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

// RuntimeReleaseResponse: Released is the lifted lease's id, nil when nothing was held.
type RuntimeReleaseResponse struct {
	Released *string `json:"released"`
}

// RuntimeClaim calls GET /runtime/claim.
func (c *Client) RuntimeClaim(ctx context.Context) (RuntimeClaimView, error) {
	var out RuntimeClaimView
	_, err := c.do(ctx, "GET", "/runtime/claim", nil, &out)
	return out, err
}

// ClaimRuntime calls POST /runtime/claim; a lease held by another session is
// a 409 CONFLICT *APIError whose details carry id, session, since, liveness.
func (c *Client) ClaimRuntime(ctx context.Context, req RuntimeClaimRequest) (RuntimeClaimView, error) {
	var out RuntimeClaimView
	_, err := c.do(ctx, "POST", "/runtime/claim", req, &out)
	return out, err
}

// ReleaseRuntime calls POST /runtime/release: the released id, "" when
// nothing was held.
func (c *Client) ReleaseRuntime(ctx context.Context, session string, force bool) (string, error) {
	var out RuntimeReleaseResponse
	if _, err := c.do(ctx, "POST", "/runtime/release", RuntimeReleaseRequest{Session: session, Force: force}, &out); err != nil {
		return "", err
	}
	if out.Released == nil {
		return "", nil
	}
	return *out.Released, nil
}
