//go:build linux

package powersrc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// New returns the Linux power source, reading the mains adapter's `online`
// flag under /sys/class/power_supply. Best-effort: battery mode is primarily
// a macOS-laptop feature.
func New() Source { return linuxSource{} }

type linuxSource struct{}

func (linuxSource) OnBattery() (bool, error) {
	matches, err := filepath.Glob("/sys/class/power_supply/A*/online")
	if err != nil {
		return false, fmt.Errorf("glob power_supply: %w", err)
	}
	if len(matches) == 0 {
		return false, fmt.Errorf("no AC power_supply adapter found")
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return false, fmt.Errorf("read %s: %w", matches[0], err)
	}
	return parseACOnline(string(b))
}

// parseACOnline maps an AC adapter "online" flag to on-battery: "1" = mains
// (false), "0" = battery (true). Anything else is an error.
func parseACOnline(content string) (bool, error) {
	n, err := strconv.Atoi(strings.TrimSpace(content))
	if err != nil {
		return false, fmt.Errorf("online: %w", err)
	}
	return n == 0, nil
}
