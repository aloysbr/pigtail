package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Seams — tests replace them; the real ones touch cmux, ps and pigtaild.
var (
	ccRun = func(o cmuxcheck.Options) cmuxcheck.Report {
		d := cmuxcheck.DefaultDeps()
		return cmuxcheck.Run(d, cmuxcheck.DefaultRepairer(d), o)
	}
	ccInfo = machineInfo
	// ccMaintenanceOwner (D4 second source): the session that set an active
	// maintenance — read from the daemon's maintenance dir (GET /maintenance
	// gives the dir, never the owner; maintenance.Load reads the file). ""
	// when none or unreadable.
	ccMaintenanceOwner = func(c *pigtail.Client) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		v, err := c.Maintenance(ctx)
		if err != nil || v == nil || !v.On || v.Dir == "" {
			return ""
		}
		st, ok, err := maintenance.Load(v.Dir)
		if err != nil || !ok || !st.On {
			return ""
		}
		return st.Owner
	}
)

func cmuxCheckCmd() *cobra.Command {
	var fix, fixPilot, unattended, asJSON, last bool
	var surfaces []string
	var ctrlC string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "After a cmux relaunch, compare the Claude sessions before and now; --fix repairs",
		Long: `Compare the Claude sessions cmux had before its last relaunch (or crash)
with the ones running now.

Without --fix nothing is typed anywhere: the check lists what is missing.
--fix repairs what is safe, one session at a time, never a busy session, a
readable draft, the focused pane, or the session running the check: it clears the
"cmux restore claude" line, relaunches a session cmux resumed (its width
may stay frozen) with its account and permission mode, starts a session cmux did
not resume, and sends "continue" to a turn the relaunch cut. A session
whose account or permission mode differs from cmux's binding but that started
10 minutes or more after cmux's launch is taken as changed on purpose (an info
line, no repair); a relaunch, quit or start whose conversation has no
transcript under the binding's account is never typed (blocked, with the reason). A session
stopped on a Claude Code dialog (folder trust…) is reported, never answered.
A session whose frozen-width screen is unreadable is quit by Ctrl+C then
started again (--ctrl-c: rescue, the default, only when --surface names it;
unread in any --fix); an unreadable draft is lost.

--fix-pilot repairs the pilot's session only — the one named by
"pigtail cmux pilot", else the active maintenance's owner — and reports the
rest; with no pilot found it is a report. pigtaild runs it after a cmux
relaunch (its default mode), and only its passes type the pilot the message
asking it to run "pigtail cmux check --last" (once per cmux launch).

The input probe writes only a terminal status query (ESC [ 5 n) to the session's own tty: never a keystroke, never text. A session whose input
is dead (keystrokes never reach its pty) is resumed in a new tab only when
--surface names it: its claude is stopped by SIGTERM (never a forced kill),
the new tab opens without focus, and the dead tab closes once the new claude
is seen; if it does not stop, nothing opens and the line says to stop it
first.

Every run writes its report to last.json (--last prints it); with the cmux
sidebar on, pigtaild shows a pill on each workspace left in defect.

Exit 0: nothing to fix or left (rows only reported, such as background work
lost, exit 0). 1: a defect to fix or left remains. 2: could not look.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// Peers are a scoped relay read (scope.go): the daemon filters them by
		// the caller's scope. D7 wants every scope — the master cell, or
		// outside any cell `--scope all` (what pigtaild runs); any other cell
		// sees its own scope only, and the report says so (peersScopeNote).
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			dir := cmuxcheck.StateDir()
			sc, err := callerScope()
			if err != nil {
				return withExitCode(2, fmt.Errorf("cmux check: %w", err))
			}
			// D18 (É29; D23 of the repo): what this cell may print — an
			// allow-list: a row of a scope it cannot read keeps its short
			// surface id and class only, another pass's info and notes are
			// dropped. A missing cell table resolves no scope: every row
			// masked outside the master.
			cells, _ := claudeprofile.Load(setup.ConfigDir())
			show := func(r cmuxcheck.Report) cmuxcheck.Report { return r.Redacted(scope.Scope(sc), cells.ScopeOf) }
			if last {
				r, err := cmuxcheck.LoadLast(dir)
				if errors.Is(err, fs.ErrNotExist) {
					fmt.Fprintln(out, "no previous check")
					return nil
				}
				if err != nil {
					return err
				}
				if asJSON { // --last --json: the same encoding as a fresh run
					enc := json.NewEncoder(out)
					enc.SetIndent("", "  ")
					_ = enc.Encode(show(r))
					return checkExit(r)
				}
				fmt.Fprintf(out, "last check at %s\n", r.At.Local().Format(time.DateTime))
				show(r).Write(out)
				return checkExit(r)
			}
			quit, err := cmuxcheck.ParseQuitPolicy(ctrlC)
			if err != nil {
				return withExitCode(2, fmt.Errorf("cmux check: %w", err))
			}
			if fix && fixPilot {
				return withExitCode(2, errors.New("cmux check: --fix and --fix-pilot: pick one"))
			}
			typing := fix || fixPilot
			release, err := cmuxcheck.Lock(dir)
			if err != nil {
				return withExitCode(2, fmt.Errorf("cmux check: %w", err))
			}
			defer release()
			// T-f: a pass that types marks itself running — pigtaild's relay
			// holds its idle wake meanwhile; an inventory never does.
			var heartbeat func()
			if typing {
				touch, unmark, err := cmuxcheck.MarkRunning(dir)
				if err != nil {
					return withExitCode(2, fmt.Errorf("cmux check: %w", err))
				}
				defer unmark()
				heartbeat = touch
			}
			client, err := newScopedClient()
			if err != nil {
				return withExitCode(2, fmt.Errorf("cmux check: %w", err))
			}
			partial := "" // "" = the peers of every scope (D7)
			if !scope.SeesAll(scope.Scope(sc)) {
				partial = sc
			}
			o := cmuxcheck.Options{Fix: typing, PilotOnly: fixPilot, Unattended: unattended,
				MaintenanceOwner: ccMaintenanceOwner(client), Surfaces: surfaces, Self: os.Getenv(cmux.EnvSurfaceID),
				Dir: cmuxcheck.CmuxDir(), Sentinel: cmuxcheck.DefaultSentinel(), Events: cmuxcheck.DefaultEvents(),
				Ping: cmux.Ping, PeersScope: partial, Quit: quit, Heartbeat: heartbeat, InStandby: ccInStandby(os.Getenv),
				Probe: !unattended,
				Peers: func() ([]pigtail.PeerSession, error) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					return client.Peers(ctx, true)
				}}
			// D4: the one-shot pilot marker, read first by resolvePilot.
			pilotNote, msgNote := "", ""
			settingsNote := ccResumeSetting(&o)
			if m, err := cmuxcheck.LoadPilot(dir); err == nil {
				o.Marker = &m
			} else if !errors.Is(err, fs.ErrNotExist) {
				pilotNote = "pilot marker unreadable: " + err.Error()
			}
			// T-i, revue PR B I2: the pilot message once per launch — its own
			// state, which no other pass rewrites (last.json is).
			if st, err := cmuxcheck.LoadPilotMsg(dir); err == nil {
				o.PilotMsg = &st
			} else if !errors.Is(err, fs.ErrNotExist) {
				msgNote = "pilot message state unreadable: " + err.Error()
			}
			r := ccRun(o)
			for _, n := range []string{pilotNote, msgNote, settingsNote} {
				if n != "" {
					r.Notes = append(r.Notes, n)
				}
			}
			if r.Fatal == "" && r.Pilot.MessageTyped && (o.PilotMsg == nil || !o.PilotMsg.For(r.Launch)) {
				st := cmuxcheck.PilotMsgState{LaunchPID: r.Launch.PID, SurfaceID: r.Pilot.SurfaceID, At: r.At}
				if err := cmuxcheck.SavePilotMsg(dir, st); err != nil {
					r.Notes = append(r.Notes, "pilot message state not written: "+err.Error())
				}
			}
			if r.Pilot.Source == cmuxcheck.PilotFromMarker {
				if err := cmuxcheck.MarkPilotUsed(dir, r.Launch.PID); err != nil {
					r.Notes = append(r.Notes, "pilot marker not marked used: "+err.Error())
				}
			}
			r.Partial = cmuxcheck.PartialPass(o, r) // É25: SaveLast keeps the other surfaces
			r.PassScope = partial                   // D18: whose info and notes these are
			if r.Fatal == "" {
				info, machine := ccInfo(client, partial)
				r.Info = append(r.Info, info...)
				r.Machine = append(r.Machine, machine...)
			}
			// last.json is also what the sidebar's pigtail-chk pill reads
			// (D13, Task 9): pigtail posts no cmux notification. It keeps
			// every field — its reader, pigtaild, is master (D23, D18).
			if err := cmuxcheck.SaveLast(dir, r); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "cmux check: last.json not written: "+err.Error())
			}
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				_ = enc.Encode(show(r))
			} else {
				show(r).Write(out)
			}
			return checkExit(r)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "repair what is safe (default: inventory only, nothing typed)")
	cmd.Flags().BoolVar(&fixPilot, "fix-pilot", false, "repair the pilot's session only (pigtail cmux pilot, else the active maintenance's owner); report the rest")
	cmd.Flags().BoolVar(&unattended, "unattended", false, "set by pigtaild: --surface names no surface for Ctrl+C (D8)")
	_ = cmd.Flags().MarkHidden("unattended")
	cmd.Flags().StringSliceVar(&surfaces, "surface", nil, "only this cmux surface UUID (repeatable)")
	cmd.Flags().StringVar(&ctrlC, "ctrl-c", string(cmuxcheck.QuitRescue), "when an idle claude whose screen is unreadable may be quit by Ctrl+C: rescue (only when --surface names it) or unread (any --fix)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the report as JSON")
	cmd.Flags().BoolVar(&last, "last", false, "print the last check's report (written by every run)")
	return cmd
}

// multiAccountInfo: one line per live session found on several accounts. D23
// (É28): the line names the session id, never its title. It goes to Info,
// which Redacted shows only to a reader of the pass's own scope (D18). find
// counts the accounts holding a session.
// ccInStandby reads the hooks' Standby marker (D16, § Prédicat Standby): a
// file read, no route, like cmuxcheck.Running. An unreadable marker is in
// Standby, with its error.
func ccInStandby(getenv func(string) string) func(string) (bool, error) {
	dir := standbyMarkerDir(getenv)
	return func(sid string) (bool, error) { return markerInStandby(dir, sid) }
}

func multiAccountInfo(ps []pigtail.PeerSession, cfgs []string, find func(id string, cfgs []string) int) []string {
	var info []string
	if len(cfgs) < 2 {
		return nil
	}
	for _, p := range ps {
		if p.Alive == nil || !*p.Alive {
			continue
		}
		if n := find(p.SessionID, cfgs); n > 1 {
			info = append(info, fmt.Sprintf("session %s exists on %d accounts", p.SessionID, n))
		}
	}
	return info
}

// machineInfo: facts that need no repair (h). info names sessions — a
// session on two accounts, the dead sessions of the scopes the peers cover —
// and Redacted shows it to a reader of the pass's own scope only; machine
// names none — pigtaild unreachable, dev servers, sidebar warnings (cmux
// state and settings, cmuxsync/runner.go, settings.go) — and every reader
// keeps it (D18). Each failure is one line. c is the command's scoped
// client; partial is the only scope its peers cover ("" = every scope),
// named on the dead-session line so a count never reads as machine-wide
// when it is not.
func machineInfo(c *pigtail.Client, partial string) (info, machine []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ps, err := c.Peers(ctx, true)
	if err != nil {
		return nil, []string{"pigtaild unreachable: no dead-session, server or sidebar info (" + err.Error() + ")"}
	}
	dead := 0
	var cfgs []string
	if tbl, err := claudeprofile.Load(setup.ConfigDir()); err == nil {
		cfgs = tbl.ConfigDirs()
	}
	for _, p := range ps {
		if p.Alive != nil && !*p.Alive && p.EndedAt == nil {
			dead++
		}
	}
	info = append(info, multiAccountInfo(ps, cfgs, func(id string, dirs []string) int {
		all, err := claudesession.FindAll(id, dirs)
		if err != nil {
			return 0
		}
		return len(all)
	})...)
	if dead > 0 {
		where := ""
		if partial != "" {
			where = " of scope " + partial + " only"
		}
		info = append(info, fmt.Sprintf("%d dead sessions%s (pigtail session ls)", dead, where))
	}
	if sv, err := c.Servers(ctx); err == nil {
		machine = append(machine, fmt.Sprintf("%d dev servers running (pigtail servers)", len(sv.Servers)))
	}
	if st, err := c.CmuxSidebar(ctx); err == nil {
		for _, w := range st.Warnings {
			machine = append(machine, "sidebar: "+w)
		}
	}
	return info, machine
}

// checkExit carries the report's exit code to main (exitcode.go); the
// report itself is already printed. Its message shares the rows as the
// report line does (D17): reported-only rows never make it exit 1.
func checkExit(r cmuxcheck.Report) error {
	switch r.ExitCode() {
	case 0:
		return nil
	case 1:
		fix, left, reported := r.Split()
		return withExitCode(1, fmt.Errorf("cmux check: %d to fix · %d left · %d reported only", fix, left, reported))
	}
	return withExitCode(2, errors.New(r.Fatal))
}

// ccResumeSetting (D1, sub_x000000000000037) sets o.CmuxResumeOff from cmux's
// resume setting. An unreadable settings file keeps today's behaviour (the
// frozen-width inference) and returns the note saying so — never "off".
func ccResumeSetting(o *cmuxcheck.Options) string {
	on, known := cmux.AgentAutoResume(cmux.SettingsPath())
	o.CmuxResumeOff = known && !on
	if !known {
		return "cmux resume setting unreadable (settings.json or defaults): " + cmux.AgentAutoResumeKey + " taken as on (frozen-width inference kept)"
	}
	return ""
}
