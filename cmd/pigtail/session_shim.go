package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
)

// Sessions cmux cannot see.
//
// Everything `session ls/move` knows comes from cmux resume bindings, and
// cmux only records a binding for a claude launched through its wrapper. In a
// pane whose cmux-cli-shims/<SURFACE_ID>/ directory is empty, `claude` (or any
// alias of it) resolves past the missing shim to the bare binary: the session
// runs fine, but it has no binding, so the inventory skips it without a word.
// This file finds those sessions from the other side — running processes —
// so `session ls` can name them and say how to bring them back under cmux.

// The seams below follow the pattern of the move operations in session.go:
// injected so tests control them, real implementations in production.
var (
	claudeWrapper  = cmux.ClaudeWrapper
	processSurface = cmux.ProcessSurface
	liveSessions   = claudesession.LiveSessions
)

// resolveWrapper returns the cmux-claude-wrapper path moveOne launches
// through — both for the live relaunch and for the resume binding cmux replays
// on restore. Going through the wrapper by absolute path is all the pane's
// shim would do, and it keeps working when the shim is empty. Without a
// wrapper it returns "" (plain `claude`) and says on warn, once, what that
// costs.
func resolveWrapper(warn io.Writer) string {
	wrapper, err := claudeWrapper()
	if err != nil {
		fmt.Fprintf(warn, "   ! %v — relaunching and rebinding with plain `claude`: in a pane whose cmux shim is empty, cmux will not track the session\n", err)
		return ""
	}
	return wrapper
}

// unboundSession is a running Claude session cmux has no binding for.
type unboundSession struct {
	SessionID string
	SurfaceID string
	CellID    string
	ConfigDir string
	Cwd       string
	PID       int
	// ShimEmpty is the usual cause: the surface's claude shim is missing
	// or empty. False means the session was launched outside the wrapper
	// some other way (an absolute path to the binary, for instance).
	ShimEmpty bool
}

// unboundSessions picks, among the running sessions, those cmux cannot see: in
// a cmux surface, launched without the wrapper's managed --settings, and not
// already named by one of the resume bindings the inventory read (rows).
//
// A process whose environment cannot be read is skipped: it may have exited
// between the two listings, and this only feeds a report, never an action.
func unboundSessions(rows []sessionRow, live []claudesession.Live, surfaceOf func(int) (cmux.Surface, error), tbl *claudeprofile.Table) []unboundSession {
	bound := make(map[string]bool, len(rows))
	for _, r := range rows {
		bound[r.SessionID] = true
	}
	var out []unboundSession
	for _, l := range live {
		if bound[l.SessionID] || strings.Contains(l.Args, cmux.ManagedSettingsMarker) {
			continue
		}
		s, err := surfaceOf(l.PID)
		if err != nil || s.ID == "" {
			continue
		}
		cell, _ := tbl.ByConfigDir(l.ConfigDir)
		out = append(out, unboundSession{
			SessionID: l.SessionID,
			SurfaceID: s.ID,
			CellID:    cell.ID,
			ConfigDir: l.ConfigDir,
			Cwd:       l.Cwd,
			PID:       l.PID,
			ShimEmpty: !cmux.ShimUsable(s.ShimRoot),
		})
	}
	return out
}

// printUnbound warns about sessions cmux cannot see, each with the line that
// relaunches it through the wrapper on the account it already runs on — after
// which cmux records its binding and `session ls/move` see it again. Nothing
// to report prints nothing.
func printUnbound(w io.Writer, sessions []unboundSession, wrapper string) {
	if len(sessions) == 0 {
		return
	}
	fmt.Fprintf(w, "warning: %d Claude session(s) run in a cmux pane without the cmux wrapper — cmux has no binding for them, so session ls/move cannot see them:\n", len(sessions))
	for _, s := range sessions {
		cause := "launched outside cmux-claude-wrapper"
		if s.ShimEmpty {
			cause = "the pane's cmux claude shim is empty"
		}
		cell := s.CellID
		if cell == "" {
			cell = "?"
		}
		fmt.Fprintf(w, "  %s  pane %s  cell %s  pid %d  %s  (%s)\n", s.SessionID, s.SurfaceID, cell, s.PID, s.Cwd, cause)
		fmt.Fprintf(w, "    fix, from that pane: /exit, then  %s\n", cmux.ResumeLine(wrapper, s.ConfigDir, s.SessionID))
	}
}

// reportUnbound runs the detection against the live system and prints its
// result on warn. A failure to list processes is itself reported: silence
// would read as "every session is visible".
func reportUnbound(warn io.Writer, rows []sessionRow, tbl *claudeprofile.Table) {
	live, err := liveSessions(tbl.ConfigDirs())
	if err != nil {
		fmt.Fprintf(warn, "warning: could not check for sessions cmux cannot see: %v\n", err)
		return
	}
	unbound := unboundSessions(rows, live, processSurface, tbl)
	if len(unbound) == 0 {
		return
	}
	wrapper, err := claudeWrapper()
	if err != nil {
		wrapper = "" // the fix line falls back to plain `claude`, which is all there is
	}
	printUnbound(warn, unbound, wrapper)
}
