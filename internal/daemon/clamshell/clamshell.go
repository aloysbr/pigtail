// Package clamshell reads whether the Mac's lid is closed, for the Travel
// Mode heat guard (internal/lid), without root and without cgo: it runs
// `/usr/sbin/ioreg -r -k AppleClamshellState -d 1` and reads the property of
// the IOPMrootDomain node ("Yes" = closed).
//
// Trap (measured 2026-10-24): when the key does not exist (a Mac with no lid,
// or a rename by Apple) ioreg prints nothing and exits 0. The parse therefore
// wants exactly one "AppleClamshellState" line with Yes or No; anything else
// is an error, never "open".
package clamshell

import (
	"fmt"
	"strings"
)

// Key is the IOPMrootDomain property that says whether the lid is closed.
const Key = "AppleClamshellState"

// parseIoreg reads ioreg output: exactly one line `"AppleClamshellState" =
// Yes|No`, any indentation (`|` at -d 4, spaces at -d 1).
func parseIoreg(out string) (closed bool, err error) {
	quoted := `"` + Key + `"`
	var values []string
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "| "))
		rest, ok := strings.CutPrefix(s, quoted)
		if !ok {
			continue
		}
		v, ok := strings.CutPrefix(rest, " = ")
		if !ok {
			continue
		}
		values = append(values, strings.TrimSpace(v))
	}
	switch len(values) {
	case 0:
		return false, fmt.Errorf("ioreg: no %s property (no lid, or the key changed)", Key)
	case 1:
	default:
		return false, fmt.Errorf("ioreg: %d %s lines, want one", len(values), Key)
	}
	switch values[0] {
	case "Yes":
		return true, nil
	case "No":
		return false, nil
	}
	return false, fmt.Errorf("ioreg: %s = %q, want Yes or No", Key, values[0])
}
