// Package thermal reads the macOS thermal state for the Travel Mode heat
// guard (internal/lid), without root and without cgo: it runs
// `/usr/bin/notifyutil -g com.apple.system.thermalpressurelevel`, as
// powersrc runs `pmset -g batt`.
//
// The value is the kernel's thermal pressure level (SDK
// libkern/OSThermalNotification.h, kOSThermalNotificationPressureLevelName):
// Nominal=0, Moderate=1, Heavy=2, Trapping=3, Sleeping=4. It maps onto
// ProcessInfo.thermalState (nominal, fair, serious, critical) as
// 0→nominal, 1→fair, 2→serious, 3|4→critical.
//
// Trap (measured 2026-10-24): `notifyutil -g <unknown key>` also answers 0,
// exit 0 — a wrong name reads "nominal" in silence. The key is therefore a
// constant, the parse is strict (the exact key echoed, an integer 0..4) and a
// darwin test reads the real value.
package thermal

import (
	"fmt"
	"strconv"
	"strings"
)

// Key is the notify(3) name of the thermal pressure level.
const Key = "com.apple.system.thermalpressurelevel"

// State is the thermal state, in ProcessInfo.thermalState's terms.
type State int

// The four thermal states, coolest first.
const (
	Nominal State = iota
	Fair
	Serious
	Critical
)

// String is the state's lower-case name: "nominal" | "fair" | "serious" | "critical".
func (s State) String() string {
	switch s {
	case Nominal:
		return "nominal"
	case Fair:
		return "fair"
	case Serious:
		return "serious"
	case Critical:
		return "critical"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// parseNotifyutil reads `notifyutil -g Key` output: exactly "<Key> <0..4>".
func parseNotifyutil(out string) (State, error) {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 2 || f[0] != Key {
		return 0, fmt.Errorf("notifyutil: unexpected output %q", strings.TrimSpace(out))
	}
	n, err := strconv.Atoi(f[1])
	if err != nil || n < 0 || n > 4 {
		return 0, fmt.Errorf("notifyutil: unexpected level %q", f[1])
	}
	switch n {
	case 0:
		return Nominal, nil
	case 1:
		return Fair, nil
	case 2:
		return Serious, nil
	default: // 3 trapping, 4 sleeping
		return Critical, nil
	}
}
