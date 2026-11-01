package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// Account rotation (sub_x000000000000008): a launch without --cell runs on the
// account claudeusage.Pick chooses. This file adapts the cell table to Pick;
// every error comes back to the caller, which falls back to the base
// behaviour and says so (warning: rotation: …) — never a silent fallback.

// The rotation switch and the pick itself live in internal/launchmargin (the
// shared launch margin guard, sub_x000000000000011); these aliases keep the
// package main names.
const envRotation = launchmargin.EnvRotation

var errRotationOff = launchmargin.ErrRotationOff

func rotationOff(getenv func(string) string) bool { return launchmargin.RotationOff(getenv) }

// pickCell picks the cell a launch in scope sc runs on (seam: tests replace
// it). limited: accountUuid → limited until.
var pickCell = func(tbl *claudeprofile.Table, sc string, limited map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
	return pickIn(tbl, sc, limited, os.Getenv, os.ReadFile, time.Now())
}

func pickIn(tbl *claudeprofile.Table, sc string, limited map[string]time.Time,
	getenv func(string) string, readFile func(string) ([]byte, error), now time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
	return launchmargin.PickIn(tbl, sc, limited, getenv, readFile, now)
}

// rotationWarnings renders the "rotation: …" lines of a pick: its own
// warnings (stale, no reading, every account over the margin) or, when it
// failed, the reason and the base behaviour used instead (fallback). A
// stale pick also cites the account's failed background refresh, with the
// line pigtail usage prints (backgroundFailureLine), the cell named only
// when the pick's scope sees it (D19: a pick in another scope than the
// caller's is the master's, which sees more, so the pick's scope never
// shows a cell the caller may not see).
func rotationWarnings(ch claudeusage.Choice, err error, fallback string) []string {
	if err != nil {
		msg := "rotation: " + err.Error()
		if fallback != "" {
			msg += " — " + fallback
		}
		return []string{msg}
	}
	out := make([]string, 0, len(ch.Warnings))
	for _, w := range ch.Warnings {
		out = append(out, "rotation: "+w)
	}
	if ch.Group != claudeusage.GroupStale || len(out) == 0 {
		return out
	}
	st, berr := rotationBackground()
	if berr != nil {
		return append(out, "rotation: background refresh state unreadable — "+berr.Error())
	}
	sc, perr := scope.Parse(ch.Cell.Scope)
	if perr != nil {
		sc = "" // sees no cell: the account alone
	}
	for _, res := range backgroundFailures(st) {
		if res.AccountUUID == ch.AccountUUID {
			out[0] += "; " + backgroundFailureLine(res, st, sc, rotationNow())
			break
		}
	}
	return out
}

// rotationBackground reads the daemon's last background refresh (seam);
// rotationNow is the clock its "ago" is measured on.
var (
	rotationBackground = func() (*claudeusage.BackgroundState, error) {
		return readBackgroundRefresh(backgroundStatePath(filepath.Dir(spawnreg.DefaultPath())))
	}
	rotationNow = time.Now
)

func backgroundStatePath(stateDir string) string {
	return filepath.Join(stateDir, claudeusage.BackgroundStateName)
}

// readBackgroundRefresh reads the daemon's usage-refresh.json: absent → nil,
// nil (no sweep yet, nothing to say); unreadable → the error, to be said.
func readBackgroundRefresh(path string) (*claudeusage.BackgroundState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st claudeusage.BackgroundState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &st, nil
}

// backgroundFailures: the failed results of the last pass that produced
// results (nil state: none).
func backgroundFailures(st *claudeusage.BackgroundState) []claudeusage.BackgroundResult {
	if st == nil {
		return nil
	}
	var out []claudeusage.BackgroundResult
	for _, r := range st.Results {
		if r.Status == claudeusage.RefreshFailed {
			out = append(out, r)
		}
	}
	return out
}

// backgroundAge: how long ago the pass that produced the results ran.
func backgroundAge(st *claudeusage.BackgroundState, now time.Time) time.Duration {
	at := st.At
	if st.ResultsAt != nil {
		at = *st.ResultsAt
	}
	return max(now.Sub(at), 0)
}

// backgroundFailureLine is the one rendering of a background refresh failure,
// for pigtail usage and the rotation's stale warning: "background refresh
// failed: <l> (cell <c>) — <err>, <age> ago", " (cell <c>)" only when caller
// sees the cell (D19), else the account alone.
func backgroundFailureLine(res claudeusage.BackgroundResult, st *claudeusage.BackgroundState, caller scope.Scope, now time.Time) string {
	cell := ""
	if res.Cell != "" && claudeusage.CellVisible(caller, res.CellScope) {
		cell = " (cell " + res.Cell + ")"
	}
	return fmt.Sprintf("background refresh failed: %s%s — %s, %s ago", res.Label, cell, res.Error, claudeusage.FormatAge(backgroundAge(st, now)))
}
