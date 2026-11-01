package pigtail

import (
	"context"
	"net/url"
	"time"
)

// ListWaits calls GET /waits: the open waits the caller's scope may read,
// oldest first; all adds the snoozed ones.
func (c *Client) ListWaits(ctx context.Context, all bool) ([]Wait, error) {
	path := "/waits"
	if all {
		path += "?all=1"
	}
	var out []Wait
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetWait calls GET /waits/{id}: one wait, open or closed, with its answer.
// A wait of a scope the caller may not read is an unknown id.
func (c *Client) GetWait(ctx context.Context, id string) (*Wait, error) {
	var out Wait
	if _, err := c.do(ctx, "GET", "/waits/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AnswerWait calls POST /waits/{id}/answer: the user's answer to an open wait,
// delivered to its session. The daemon refuses it from an agent session.
func (c *Client) AnswerWait(ctx context.Context, id string, req WaitAnswerRequest) (*WaitAnswerResponse, error) {
	var out WaitAnswerResponse
	if _, err := c.do(ctx, "POST", "/waits/"+url.PathEscape(id)+"/answer", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SnoozeWait calls POST /waits/{id}/snooze: the open wait leaves the list
// until until (a time in the future); the session is not touched.
func (c *Client) SnoozeWait(ctx context.Context, id string, until time.Time) (*Wait, error) {
	var out Wait
	if _, err := c.do(ctx, "POST", "/waits/"+url.PathEscape(id)+"/snooze", WaitSnoozeRequest{Until: until}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
