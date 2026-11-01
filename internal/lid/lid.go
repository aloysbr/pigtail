// Package lid keeps a Mac awake with its lid closed: `pmset -a disablesleep
// 1|0` run through `sudo -n`, allowed by the scoped NOPASSWD rule `pigtail lid
// setup` installs (see sudoers.go), with a deadline, a battery guard and a
// lid-closed heat guard run by pigtaild (manager.go). The thermal and lid
// readers are not root and live in internal/daemon/thermal and
// internal/daemon/clamshell; this package only receives them as functions.
//
// This is the only place pigtaild runs a root command. Tests use Runner
// fakes, never the real pmset or sudo.
package lid

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultHours is the timer of `pigtail lid on` with no hours given.
	DefaultHours = 8
	// MaxHours bounds a timer (D4).
	MaxHours = 24
	// MinBattery is the charge (percent, on battery) at or below which the
	// guard restores normal sleep.
	MinBattery = 25
	// SeriousTicks is how many consecutive "serious" thermal readings with
	// the lid closed turn sleep control off (D2·t2): 10 min at TickEvery.
	// "critical" with the lid closed turns it off at once; with the lid open,
	// heat never does.
	SeriousTicks = 10
	// TickEvery is the deadline, battery and heat check period.
	TickEvery = time.Minute
	// SudoersPath is the rule `pigtail lid setup` installs.
	SudoersPath = "/etc/sudoers.d/pigtail-lid"
	stateFile   = "lid-state.json"

	execTimeout = 10 * time.Second
)

// ErrNotSetUp is returned when sudo refuses pmset without a password: the
// sudoers rule is missing.
var ErrNotSetUp = errors.New("sleep control is not set up — run `pigtail lid setup` once (asks for your admin password)")

// ErrHours rejects a timer outside 1..MaxHours.
var ErrHours = errors.New("hours must be an integer from 1 to 24")

// Runner runs the three pmset commands the manager needs. ExecRunner is the
// real one; tests use fakes.
type Runner interface {
	ReadPM() (string, error)        // /usr/bin/pmset -g
	SetSleepDisabled(on bool) error // /usr/bin/sudo -n /usr/bin/pmset -a disablesleep 1|0
	ProbeSet(current bool) error    // /usr/bin/sudo -n -k /usr/bin/pmset -a disablesleep <current> (D7 probe)
}

// ExecRunner runs the real commands, each bounded at 10 s.
type ExecRunner struct{}

// SetArgs is the exact sudo argv for disablesleep on|off — the only commands
// the sudoers rule allows.
func SetArgs(on bool) []string {
	return []string{"-n", "/usr/bin/pmset", "-a", "disablesleep", strconv.Itoa(b01(on))}
}

// ProbeArgs is the sudo argv of the D7 probe: a no-op write of the current
// value. current must be "0" or "1", else nil.
func ProbeArgs(current string) []string {
	if current != "0" && current != "1" {
		return nil
	}
	return []string{"-n", "-k", "/usr/bin/pmset", "-a", "disablesleep", current}
}

// ReadPM returns the output of `pmset -g`.
func (ExecRunner) ReadPM() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/pmset", "-g").Output()
	return string(out), err
}

// SetSleepDisabled runs `sudo -n pmset -a disablesleep 1|0`.
func (ExecRunner) SetSleepDisabled(on bool) error {
	return runSudo(SetArgs(on))
}

// ProbeSet is the D7 probe: a no-op write of the value the caller just read,
// -k so a cached sudo timestamp cannot pass for the NOPASSWD rule.
func (ExecRunner) ProbeSet(current bool) error {
	return runSudo(ProbeArgs(strconv.Itoa(b01(current))))
}

func runSudo(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", args...)
	cmd.Stderr = &stderr // stdin nil: no prompt is possible with -n
	if err := cmd.Run(); err != nil {
		return classifySudo(stderr.String(), err)
	}
	return nil
}

// ParseSleepDisabled reads the SleepDisabled line of `pmset -g`; absent means
// false.
func ParseSleepDisabled(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "SleepDisabled" {
			return f[1] == "1"
		}
	}
	return false
}

func classifySudo(stderr string, err error) error {
	for _, s := range []string{"a password is required", "is not allowed to execute", "no tty present"} {
		if strings.Contains(stderr, s) {
			return ErrNotSetUp
		}
	}
	return fmt.Errorf("sudo pmset: %s: %w", strings.TrimSpace(stderr), err)
}

func b01(v bool) int {
	if v {
		return 1
	}
	return 0
}
