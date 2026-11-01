package pigtail

import (
	"context"
	"net/url"
	"time"
)

// CreateFocus calls POST /focus: a focus of the caller's scope (D6).
func (c *Client) CreateFocus(ctx context.Context, name string, endsAt *time.Time, members []FocusMember) (*Focus, error) {
	if members == nil {
		members = []FocusMember{}
	}
	body := struct {
		Name    string        `json:"name"`
		EndsAt  *time.Time    `json:"ends_at,omitempty"`
		Members []FocusMember `json:"members"`
	}{name, endsAt, members}
	var out Focus
	if _, err := c.do(ctx, "POST", "/focus", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListFocus calls GET /focus[?all=1]: the focus the caller may read, oldest
// first, and the daemon's note on the folders ("" when they run).
func (c *Client) ListFocus(ctx context.Context, all bool) ([]Focus, string, error) {
	path := "/focus"
	if all {
		path += "?all=1"
	}
	var out struct {
		Focus []Focus `json:"focus"`
		Note  string  `json:"note"`
	}
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, "", err
	}
	return out.Focus, out.Note, nil
}

// AddFocusMember calls POST /focus/{id}/members.
func (c *Client) AddFocusMember(ctx context.Context, focusID string, m FocusMember) (*Focus, error) {
	var out Focus
	body := struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{m.Kind, m.ID}
	if _, err := c.do(ctx, "POST", "/focus/"+url.PathEscape(focusID)+"/members", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveFocusMember calls DELETE /focus/{id}/members/{kind}/{member}.
func (c *Client) RemoveFocusMember(ctx context.Context, focusID, kind, id string) (*Focus, error) {
	var out Focus
	path := "/focus/" + url.PathEscape(focusID) + "/members/" + url.PathEscape(kind) + "/" + url.PathEscape(id)
	if _, err := c.do(ctx, "DELETE", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EndFocus calls POST /focus/{id}/end: the focus ends by hand.
func (c *Client) EndFocus(ctx context.Context, focusID string) (*Focus, error) {
	var out Focus
	if _, err := c.do(ctx, "POST", "/focus/"+url.PathEscape(focusID)+"/end", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
