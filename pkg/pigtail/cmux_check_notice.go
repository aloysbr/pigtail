package pigtail

import (
	"context"
	"time"
)

// CmuxCheckNotice is GET /cmux/check/notice (D2, sub_x000000000000037): the
// end-of-pass line of the running cmux launch, which the Pigtail app posts as a
// notification. Line is "" when nothing is left for the user, or when the notice
// belongs to another launch than the running cmux.
type CmuxCheckNotice struct {
	LaunchPID int       `json:"launch_pid"`
	At        time.Time `json:"at"`
	Line      string    `json:"line"`
}

// CmuxCheckNotice reads GET /cmux/check/notice (master scope only).
func (c *Client) CmuxCheckNotice(ctx context.Context) (*CmuxCheckNotice, error) {
	var out CmuxCheckNotice
	if _, err := c.do(ctx, "GET", "/cmux/check/notice", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
