//go:build darwin

package clamshell

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Closed runs `ioreg -r -k AppleClamshellState -d 1`, bounded at 10 s. An
// error (a timeout included) reads as "lid unknown": no heat off.
func Closed() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-r", "-k", Key, "-d", "1").Output()
	if err != nil {
		return false, fmt.Errorf("ioreg -r -k %s: %w", Key, err)
	}
	return parseIoreg(string(out))
}
