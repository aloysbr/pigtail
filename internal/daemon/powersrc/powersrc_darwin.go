//go:build darwin

package powersrc

import (
	"fmt"
	"os/exec"
)

// New returns the macOS power source, reading `pmset -g batt`.
func New() Source { return darwinSource{} }

type darwinSource struct{}

func (darwinSource) OnBattery() (bool, error) {
	out, err := exec.Command("pmset", "-g", "batt").Output()
	if err != nil {
		return false, fmt.Errorf("pmset -g batt: %w", err)
	}
	return parsePmsetBatt(string(out))
}
