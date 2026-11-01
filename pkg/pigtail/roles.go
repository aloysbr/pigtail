package pigtail

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// Role binds a name to a subject that must act on a cadence (role
// supervision, L1). Scope is the SUBJECT's scope, never the caller's.
type Role struct {
	Scope     string        `json:"scope"`
	Name      string        `json:"name"`
	SubjectID string        `json:"subject_id"`
	Cadence   time.Duration `json:"-"`
	Grace     time.Duration `json:"-"`
	Stall     time.Duration `json:"-"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// roleJSON is the wire shape of a Role: durations as milliseconds.
type roleJSON struct {
	Scope     string    `json:"scope"`
	Name      string    `json:"name"`
	SubjectID string    `json:"subject_id"`
	CadenceMS int64     `json:"cadence_ms"`
	GraceMS   int64     `json:"grace_ms"`
	StallMS   int64     `json:"stall_ms"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MarshalJSON writes the durations as cadence_ms, grace_ms, stall_ms.
func (r Role) MarshalJSON() ([]byte, error) {
	return json.Marshal(roleJSON{r.Scope, r.Name, r.SubjectID, r.Cadence.Milliseconds(), r.Grace.Milliseconds(),
		r.Stall.Milliseconds(), r.CreatedAt, r.UpdatedAt})
}

// UnmarshalJSON reads what MarshalJSON writes.
func (r *Role) UnmarshalJSON(b []byte) error {
	var j roleJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*r = Role{j.Scope, j.Name, j.SubjectID, time.Duration(j.CadenceMS) * time.Millisecond,
		time.Duration(j.GraceMS) * time.Millisecond, time.Duration(j.StallMS) * time.Millisecond, j.CreatedAt, j.UpdatedAt}
	return nil
}

// RoleStatus is the last verdict of one role: the sweep's snapshot, served by
// GET /roles/status. Role is a field, not an embedding: Role's MarshalJSON
// would otherwise be promoted and swallow the other fields.
type RoleStatus struct {
	Role     Role       `json:"role"`
	State    string     `json:"state"`
	Reason   string     `json:"reason,omitempty"`
	Deadline *time.Time `json:"deadline,omitempty"`
	Episode  *time.Time `json:"episode,omitempty"`
	JudgedAt time.Time  `json:"judged_at"`
	AlertID  string     `json:"alert_id,omitempty"`
	Held     bool       `json:"held,omitempty"`
}

// Role defaults, applied when a request leaves a duration at 0.
const (
	DefaultRoleGrace = 10 * time.Minute
	DefaultRoleStall = 20 * time.Minute
)

// RoleRequest is the body of POST /roles (upsert). The role's scope is the
// subject's, never the caller's.
type RoleRequest struct {
	Name      string `json:"name"`
	SubjectID string `json:"subject_id"`
	CadenceMS int64  `json:"cadence_ms"`
	GraceMS   int64  `json:"grace_ms,omitempty"`
	StallMS   int64  `json:"stall_ms,omitempty"`
}

// RolesResponse is the body of GET /roles. Roles is never null.
type RolesResponse struct {
	Roles []Role `json:"roles"`
}

// RolesStatusResponse is the body of GET /roles/status. Watch is false when
// pigtaild runs with -role-watch=false: no role is then judged, and the
// statuses say so; an empty Roles with Watch true means no role declared.
type RolesStatusResponse struct {
	Watch bool         `json:"watch"`
	Roles []RoleStatus `json:"roles"`
}

// UpsertRole declares or updates a role (POST /roles).
func (c *Client) UpsertRole(ctx context.Context, req RoleRequest) (*Role, error) {
	var out Role
	if _, err := c.do(ctx, "POST", "/roles", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Roles lists the declared roles the caller reads (GET /roles).
func (c *Client) Roles(ctx context.Context) ([]Role, error) {
	var out RolesResponse
	if _, err := c.do(ctx, "GET", "/roles", nil, &out); err != nil {
		return nil, err
	}
	return out.Roles, nil
}

// DeleteRole removes a role and closes its open episodes (DELETE /roles/{name}).
func (c *Client) DeleteRole(ctx context.Context, name string) error {
	_, err := c.do(ctx, "DELETE", "/roles/"+url.PathEscape(name), nil, nil)
	return err
}

// RolesStatus reads the last verdict of each role (GET /roles/status).
func (c *Client) RolesStatus(ctx context.Context) (*RolesStatusResponse, error) {
	var out RolesStatusResponse
	if _, err := c.do(ctx, "GET", "/roles/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
