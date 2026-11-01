package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/waittype"
)

// The hooks' and the CLI's Standby predicate (sub_x000000000000041,
// § Prédicat Standby): the marker <socket dir>/standby/<session> the daemon
// keeps as the mirror of its session_standby table (peers_standby.go). A
// marker that cannot be read counts as in Standby, never as free.

// standbyMarkerDir must agree with the daemon's RelayConfig.StandbyMarkerDir:
// the directory of the socket, plus "standby" (same rule as waitMarkerDir).
func standbyMarkerDir(getenv func(string) string) string {
	if d := getenv("PIGTAIL_STANDBY_DIR"); d != "" {
		return d
	}
	if s := socketPath(); s != "" {
		return filepath.Join(filepath.Dir(s), "standby")
	}
	return filepath.Join(setup.ConfigDir(), "standby")
}

// markerInStandby: the marker is there → true; it is not (fs.ErrNotExist) →
// false; any other error → true, with the error. An invalid key is never
// stat'ed: false.
func markerInStandby(dir, session string) (bool, error) {
	if !validMarkerKey(session) {
		return false, nil
	}
	_, err := os.Stat(filepath.Join(dir, session))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return true, err
	}
}

// inStandby is markerInStandby on the session's marker dir.
func inStandby(getenv func(string) string, session string) bool {
	in, _ := markerInStandby(standbyMarkerDir(getenv), session)
	return in
}

var compactPrompt = regexp.MustCompile(`(?i)^\s*/compact(\s|$)`)
var exitPrompt = regexp.MustCompile(`^\s*/exit\s*$`)

// standbyEnds: a prompt the user typed ends the session's Standby (D5); the
// /compact pigtail types (relay_standby.go), the /exit typed before a
// relaunch and a turnless command do not. No automaton types text into a
// standby session (D16: relay_wake.go, cmuxcheck).
//
// The /compact exception is a defensive guard (D41): P16b (internal/cmux/testdata/
// PROBE.md, 2026-10-13) measured that built-in slash commands never reach
// UserPromptSubmit; kept in case a later Claude Code delivers them.
func standbyEnds(p string) bool {
	return !compactPrompt.MatchString(p) && !exitPrompt.MatchString(p) && !waittype.TurnlessPrompt(p)
}
