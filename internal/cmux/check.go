package cmux

import (
	"fmt"
	"strings"
)

func sendTextArgs(surface, text string) []string { return []string{"send", "--surface", surface, text} }

// SendText types text into a surface as is: control characters (\x15, \r)
// included, nothing appended (pigtail cmux check, F5). Bounded like every
// cmux write (relayCallTimeout): a cmux that takes the call and never
// answers must not hold the check and its lock.
//
// Arguments are checked as the sidebar's are (sidebar.go): the surface is a
// UUID, the text never empty nor readable by cmux as a flag. Control
// characters and a long line stay allowed — they are what the check types.
func SendText(surface, text string) error {
	if err := checkUUID("surface", surface); err != nil {
		return err
	}
	if text == "" || strings.HasPrefix(text, "-") {
		return invalid("text", text)
	}
	if _, err := run(sendTextArgs(surface, text)...); err != nil {
		return fmt.Errorf("%w (surface %s)", err, surface)
	}
	return nil
}

// Ping checks the cmux socket answers (`cmux ping` → PONG), within the
// write bound.
func Ping() error {
	out, err := run("ping")
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "PONG") {
		return fmt.Errorf("cmux ping: no PONG (output: %s)", strings.TrimSpace(string(out)))
	}
	return nil
}

// PaneEnv: the variables cmux exports into a pane, naming that pane. A
// process started from a pane (pigtaild launched by hand) inherits them; the
// daemon's cmuxCheckEnv drops them — one list, kept here.
var PaneEnv = []string{EnvSurfaceID, "CMUX_PANEL_ID", "CMUX_TAB_ID", EnvWorkspaceID}

// IsPaneEnv reports whether kv ("NAME=value") is one of PaneEnv.
func IsPaneEnv(kv string) bool {
	name := strings.SplitN(kv, "=", 2)[0]
	for _, v := range PaneEnv {
		if name == v {
			return true
		}
	}
	return false
}
