package pigtail

import (
	"context"
	"time"
)

// LidCallTimeout bounds every lid client call (CLI on/off/status, the setup
// seams). The daemon's worst wait is 70 s: a Tick in progress (<= 20 s), the
// setup probe (<= 30 s) and the operation itself (<= 20 s).
const LidCallTimeout = 90 * time.Second

// Lid returns the real sleep state and pigtail's timer (GET /lid).
func (c *Client) Lid(ctx context.Context) (LidResponse, error) {
	var out LidResponse
	if _, err := c.do(ctx, "GET", "/lid", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// SetLid turns sleep control on for hours (0 = the daemon's default), or off
// (POST /lid). On while already on re-arms the timer.
func (c *Client) SetLid(ctx context.Context, on bool, hours int) (LidResponse, error) {
	var out LidResponse
	if _, err := c.do(ctx, "POST", "/lid", LidRequest{On: on, Hours: hours}, &out); err != nil {
		return out, err
	}
	return out, nil
}

// LidProbe asks pigtaild to check that the sudoers rule works for its own
// process (POST /lid/probe): a no-op write of the current value, under the
// manager lock. The caller passes LidCallTimeout.
func (c *Client) LidProbe(ctx context.Context) (LidProbeResponse, error) {
	var out LidProbeResponse
	if _, err := c.do(ctx, "POST", "/lid/probe", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}
