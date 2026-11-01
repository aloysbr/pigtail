package cmuxsync

import (
	"strings"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The « Paused » pill (sub_x000000000000036, T-g): pigtaild stopped the
// idle session of this workspace; its tab offers to relaunch it. A key of
// its own, not an attention state: a paused session is not alive and waits
// for nothing.
const (
	pausedLabel    = "Paused"       // English label, rule of 2026-10-01
	pausedIcon     = "pause.circle" // SF Symbol
	pausedColor    = "#5E5CE6"      // systemIndigo: not the grey of Off-topic
	pausedPriority = 75             // under maintenance (80), above inbox (70)
)

// pausedWorkspaces is the cmux workspaces (upper case) of the rows in the
// paused state; a stopping row (SIGTERM not yet seen through) shows nothing.
func pausedWorkspaces(ps []pigtail.SessionPause) map[string]bool {
	m := map[string]bool{}
	for _, p := range ps {
		if p.State != pigtail.PausePaused || p.CmuxWorkspaceID == "" {
			continue
		}
		m[strings.ToUpper(p.CmuxWorkspaceID)] = true
	}
	return m
}

func pausedStatus() cmux.StatusEntry {
	return cmux.StatusEntry{Key: KeyPaused, Value: pausedLabel, Icon: pausedIcon, Color: pausedColor, Priority: pausedPriority}
}

// pausedSet reads Deps.Paused for this pass, filtered to the driven windows.
// A read error keeps the last successful map (the pill does not blink on a
// locked database), logged once per distinct error. nil Deps.Paused = none.
func (r *Runner) pausedSet() map[string]bool {
	if r.deps.Paused == nil {
		return nil
	}
	ps, err := r.deps.Paused()
	if err != nil {
		if msg := err.Error(); msg != r.lastPausedErr {
			r.lastPausedErr = msg
			r.deps.Logf("cmuxsync: list pauses: %v", err)
		}
	} else {
		r.lastPausedErr = ""
		r.lastPaused = pausedWorkspaces(ps)
	}
	out := map[string]bool{}
	for ws := range r.lastPaused {
		if r.allowed[ws] {
			out[ws] = true
		}
	}
	return out
}

// closeMissingPause is closeMissingStandby's proof for a paused row (final
// review I1): a tab closed by hand never resumes nor registers another
// session, so its row would stay forever. Its workspace absent from two
// successful listings in a row ends it through Deps.PauseGone. The count is
// kept in memory (a restart only delays the purge); a stopping row is left
// to the relay's follow-up; an unreadable table purges nothing.
func (r *Runner) closeMissingPause(live map[string]bool) {
	if r.deps.PauseGone == nil || r.deps.Paused == nil {
		return
	}
	ps, err := r.deps.Paused()
	if err != nil {
		return // pausedSet logs it
	}
	next := map[string]int{}
	for _, p := range ps {
		if p.State != pigtail.PausePaused || p.CmuxWorkspaceID == "" {
			continue
		}
		ws := strings.ToUpper(p.CmuxWorkspaceID)
		n, forget := missedTwice(live[ws], r.pauseMissing[p.SessionID])
		if !forget {
			if n > 0 {
				next[p.SessionID] = n
			}
			continue
		}
		if err := r.deps.PauseGone(p.SessionID, ws); err != nil {
			r.deps.Logf("cmuxsync: pause of %s (workspace %s closed): %v", p.SessionID, ws, err)
			next[p.SessionID] = n
		}
	}
	r.pauseMissing = next
}
