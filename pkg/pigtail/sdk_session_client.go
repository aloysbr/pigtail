package pigtail

import (
	"context"
	"net/url"
)

// SDKSessions lists the SDK sessions the caller may read, of one subject when
// subjectID is not empty. An APIError with Code ErrSDKOff means the daemon runs
// without the SDK sessions.
func (c *Client) SDKSessions(ctx context.Context, subjectID string) (SDKSessionList, error) {
	path := "/sdk-sessions"
	if subjectID != "" {
		path += "?subject=" + url.QueryEscape(subjectID)
	}
	var out SDKSessionList
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return SDKSessionList{}, err
	}
	return out, nil
}

// SDKSession reads one SDK session.
func (c *Client) SDKSession(ctx context.Context, id string) (SDKSession, error) {
	var out SDKSession
	if _, err := c.do(ctx, "GET", "/sdk-sessions/"+url.PathEscape(id), nil, &out); err != nil {
		return SDKSession{}, err
	}
	return out, nil
}
