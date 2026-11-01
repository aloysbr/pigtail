//go:build darwin

package thermal

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Read runs `notifyutil -g Key`, bounded at 10 s. An error (a timeout
// included) never turns the Travel Mode off: the lid guard logs it.
func Read() (State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/notifyutil", "-g", Key).Output()
	if err != nil {
		return 0, fmt.Errorf("notifyutil -g %s: %w", Key, err)
	}
	return parseNotifyutil(string(out))
}
