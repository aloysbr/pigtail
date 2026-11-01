//go:build darwin

package powersrc

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// ReadLevel reads the battery source and charge from `pmset -g batt`. It is
// bounded at 10 s; a timeout is an error, which the lid guard reads as
// "unknown" (never an off for the battery; the deadline still applies).
func ReadLevel() (Level, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "batt").Output()
	if err != nil { // incl. timeout: "unknown", never turns sleep control off (the deadline still applies)
		return Level{}, fmt.Errorf("pmset -g batt: %w", err)
	}
	return parsePmsetBattLevel(string(out))
}
