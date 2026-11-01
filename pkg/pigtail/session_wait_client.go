package pigtail

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// SetSessionWait calls POST /peers/{session}/wait: record what the session
// waits for. The caller acts as the session (its scope must be the
// session's), so build the client with the session's scope.
func (c *Client) SetSessionWait(ctx context.Context, session string, req SessionWaitRequest) (*PeerSession, error) {
	var out PeerSession
	if _, err := c.do(ctx, "POST", "/peers/"+url.PathEscape(session)+"/wait", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearSessionWait calls DELETE /peers/{session}/wait?source=…: source is
// auto, declared, held, or "" for auto and declared (a held wait is spared)
// and all for the three.
func (c *Client) ClearSessionWait(ctx context.Context, session, source string) (*PeerSession, error) {
	path := "/peers/" + url.PathEscape(session) + "/wait"
	if source != "" {
		path += "?source=" + url.QueryEscape(source)
	}
	var out PeerSession
	if _, err := c.do(ctx, "DELETE", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearSessionWaitKeepingSubject calls DELETE /peers/{session}/wait?keep=subject:
// the clear of the done command's prompt — the turn's waits go, the wait of its
// subject the session emitted stays open for the subject's next session.
func (c *Client) ClearSessionWaitKeepingSubject(ctx context.Context, session string) (*PeerSession, error) {
	var out PeerSession
	if _, err := c.do(ctx, "DELETE", "/peers/"+url.PathEscape(session)+"/wait?keep=subject", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearHeldWait calls DELETE /peers/{session}/wait?source=held&holder_pid=N:
// clears the session's held wait only if pid still holds it.
func (c *Client) ClearHeldWait(ctx context.Context, session string, pid int) (*PeerSession, error) {
	path := "/peers/" + url.PathEscape(session) + "/wait?source=held&holder_pid=" + strconv.Itoa(pid)
	var out PeerSession
	if _, err := c.do(ctx, "DELETE", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetSessionSkill calls POST /peers/{session}/skill: skill becomes the
// session's last one (D11).
func (c *Client) SetSessionSkill(ctx context.Context, session, skill string) error {
	_, err := c.do(ctx, "POST", "/peers/"+url.PathEscape(session)+"/skill", SessionSkillRequest{Skill: skill}, nil)
	return err
}

// PostSessionTurn calls POST /peers/{session}/turn: a turn end (D12).
func (c *Client) PostSessionTurn(ctx context.Context, session string, req SessionTurnRequest) error {
	_, err := c.do(ctx, "POST", "/peers/"+url.PathEscape(session)+"/turn", req, nil)
	return err
}

// SessionHandbacks calls GET /session-handbacks?since=…: the turns handed
// back without a declared wait since then, by skill. A zero since lets the
// daemon pick its default (7 days).
func (c *Client) SessionHandbacks(ctx context.Context, since time.Time) (*SessionHandbacksResponse, error) {
	path := "/session-handbacks"
	if !since.IsZero() {
		path += "?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
	}
	var out SessionHandbacksResponse
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
