package pigtail

import "context"

// WebLoginCode is the answer of POST /web/login-code (Unix socket
// only): a single-use code and the daemon's HTTP address, from which
// `pigtail web` builds http://<HTTPAddr>/web/login?code=<Code>.
type WebLoginCode struct {
	Code     string `json:"code"`
	HTTPAddr string `json:"http_addr"`
}

// WebLoginCode asks POST /web/login-code. The daemon answers only on
// the Unix socket (403 over HTTP) and 409 when its HTTP listener is off.
func (c *Client) WebLoginCode(ctx context.Context) (*WebLoginCode, error) {
	var out WebLoginCode
	if _, err := c.do(ctx, "POST", "/web/login-code", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
