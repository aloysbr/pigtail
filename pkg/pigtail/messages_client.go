package pigtail

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// PostPeers upserts peer sessions (or marks them ended with req.End).
func (c *Client) PostPeers(ctx context.Context, req PeersRequest) ([]PeerSession, error) {
	var out []PeerSession
	if _, err := c.do(ctx, "POST", "/peers", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Peers lists the live peer sessions (all=true includes ended/dead ones).
func (c *Client) Peers(ctx context.Context, all bool) ([]PeerSession, error) {
	path := "/peers"
	if all {
		path += "?all=1"
	}
	var out []PeerSession
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SendMessage calls POST /messages.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (*SendMessageResponse, error) {
	var out SendMessageResponse
	if _, err := c.do(ctx, "POST", "/messages", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BroadcastMessage calls POST /messages/broadcast.
func (c *Client) BroadcastMessage(ctx context.Context, req BroadcastRequest) (*BroadcastResponse, error) {
	var out BroadcastResponse
	if _, err := c.do(ctx, "POST", "/messages/broadcast", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Messages calls GET /messages with the filter.
func (c *Client) Messages(ctx context.Context, f MessageFilter) ([]Message, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("to", f.To)
	set("from", f.From)
	set("session", f.Session)
	set("state", f.State)
	set("thread", f.Thread)
	set("subject", f.Subject)
	if !f.Since.IsZero() {
		q.Set("since", f.Since.UTC().Format(time.RFC3339))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	path := "/messages"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []Message
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Message calls GET /messages/{id}.
func (c *Client) Message(ctx context.Context, id string) (*Message, error) {
	var out Message
	if _, err := c.do(ctx, "GET", "/messages/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimMessages calls POST /messages/claim.
func (c *Client) ClaimMessages(ctx context.Context, session, via string) ([]Message, error) {
	var out ClaimResponse
	if _, err := c.do(ctx, "POST", "/messages/claim", ClaimRequest{Session: session, Via: via}, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// WaitMessages calls GET /messages/wait: blocks up to timeout for the
// session's next messages, claimed with via=rewake.
func (c *Client) WaitMessages(ctx context.Context, session string, timeout time.Duration) (*ClaimResponse, error) {
	q := url.Values{}
	q.Set("session", session)
	q.Set("timeout", strconv.Itoa(int(timeout/time.Second)))
	var out ClaimResponse
	if _, err := c.do(ctx, "GET", "/messages/wait?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AckMessage calls POST /messages/{id}/ack as the given recipient session.
func (c *Client) AckMessage(ctx context.Context, id, session string) (*Message, error) {
	var out Message
	if _, err := c.do(ctx, "POST", "/messages/"+url.PathEscape(id)+"/ack", AckRequest{Session: session}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
