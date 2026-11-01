// Package netprobe answers one question for the relay's auto-retry: can the
// Anthropic API be reached at all? A bounded TCP connect, closed at once, no
// byte sent. It is the only network access of the relay.
package netprobe

import (
	"context"
	"net"
	"time"
)

const (
	// APIAddr is the endpoint Claude Code talks to.
	APIAddr = "api.anthropic.com:443"
	// ProbeTimeout bounds one probe.
	ProbeTimeout = 3 * time.Second
)

// Reachable opens a TCP connection to addr within timeout (and ctx) and closes
// it at once. nil = reachable.
func Reachable(ctx context.Context, addr string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}
