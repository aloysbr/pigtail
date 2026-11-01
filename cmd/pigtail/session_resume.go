package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/spf13/cobra"
)

// `pigtail session resume` (sub_x000000000000036, task 10): relaunch a
// session pigtaild paused, in its own tab — Enter at the launcher's ↻, or
// the resume line typed at the shell's prompt — then verify it like
// `session move`. Nothing is typed into a tab that shows anything else.

// Seams: tests never touch a real cmux, claude or pid.
var (
	resumeReadScreen = cmux.ReadScreenBounded
	resumeSendEnter  = cmux.SendEnterBounded
	resumeSendText   = cmux.SendText
	resumeShellReady = shellReadyOn
	resumeConfirm    = func(c relaunchCheck) (int, error) { return confirmRelaunch(c) }
	resumeBefore     = claudePIDs
	resumeWrapper    = func() string { return resolveWrapper(io.Discard) }
)

// The launcher's ↻ prompts (its shell function): Enter resumes the session, or
// starts a new one when the transcript is missing.
const (
	launcherResumePrompt = "↻ Entrée = relancer"
	launcherNewPrompt    = "↻ Entrée = nouvelle session"
)

type resumeRoute int

const (
	resumeLauncher resumeRoute = iota + 1 // Enter at the launcher's ↻
	resumeShell                           // the resume line at the shell's prompt
)

func sessionResumeCmd() *cobra.Command {
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "resume <subject id|short title|label|session id>",
		Annotations: scopedWrite(),
		Short:       "Relaunch a session pigtaild paused, in its own cmux tab",
		Long: `Relaunch a Claude session that pigtaild paused when it sat idle past the
threshold (-idle-session-pause-seconds): Enter at the launcher's ↻ prompt,
or the resume line typed at the tab's shell prompt. The relaunch is verified
(a new claude resuming the session, in that tab, under its cell). Nothing is
typed into a tab that shows anything else; the line to resume by hand is
printed instead. The session stays in Standby until your prompt.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			ps, err := c.Peers(context.Background(), true)
			if err != nil {
				return err
			}
			caller, err := callerScope()
			if err != nil {
				return err
			}
			return runResume(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], ps, caller, crossScope)
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// runResume resolves target among the paused sessions of ps and resumes it.
func runResume(out, warn io.Writer, target string, ps []pigtail.PeerSession, caller string, crossScope bool) error {
	var paused []pigtail.PeerSession
	for _, p := range ps {
		if p.PausedAt != nil {
			paused = append(paused, p)
		}
	}
	p, _, err := peers.ResolveLevel(paused, target)
	if err != nil {
		var unresolved *peers.ErrUnresolved
		if !errors.As(err, &unresolved) {
			return err // ambiguous among paused sessions: the resolver's own answer
		}
		if q, _, e := peers.ResolveLevel(ps, target); e == nil {
			return fmt.Errorf("session %s is not paused (%s) — nothing done", resumeLabel(q), peerState(q, true))
		}
		return fmt.Errorf("no paused session matches %q", target)
	}
	label := resumeLabel(p)
	if scope.Canonical(p.Scope) != scope.Canonical(caller) && !crossScope {
		return fmt.Errorf("session %s is of scope %s — --cross-scope to resume it", label, p.Scope)
	}

	byHand := resumeByHand(p)
	screen, err := resumeReadScreen(p.CmuxSurfaceID)
	if err != nil {
		return fmt.Errorf("the tab of %s is gone or unreadable: %w — resume by hand: %s", label, err, byHand)
	}
	route, _, err := routeResume(screen, func() (bool, string) { return resumeShellReady(p.CmuxSurfaceID) })
	if err != nil {
		return fmt.Errorf("%s: %w — nothing typed; resume by hand: %s", label, err, byHand)
	}

	before, err := resumeBefore()
	if err != nil {
		return fmt.Errorf("list Claude processes: %w — nothing typed", err)
	}
	wrapper := resumeWrapper()
	line := cmux.ResumeLine(wrapper, p.ConfigDir, p.SessionID)
	switch route {
	case resumeLauncher:
		if err := resumeSendEnter(p.CmuxSurfaceID); err != nil {
			return fmt.Errorf("press Enter at the launcher of %s: %w — resume by hand: %s", label, err, byHand)
		}
	case resumeShell:
		// The line is sent whole by SendText (no 200-rune cap, unlike
		// SendLineBounded): with NIGHTLY paths it runs past 200 runes.
		for _, text := range []string{"\x15", line} {
			if err := resumeSendText(p.CmuxSurfaceID, text); err != nil {
				return fmt.Errorf("type the resume line in the tab of %s: %w — resume by hand: cd -- %s && %s", label, err, shQuote(p.Cwd), line)
			}
		}
		if err := resumeSendEnter(p.CmuxSurfaceID); err != nil {
			return fmt.Errorf("press Enter after the resume line in the tab of %s: %w — resume by hand: cd -- %s && %s", label, err, shQuote(p.Cwd), line)
		}
	}
	pid, err := resumeConfirm(relaunchCheck{
		SessionID: p.SessionID,
		Dst:       claudeprofile.Cell{ID: p.CellID, ConfigDir: p.ConfigDir},
		Before:    before,
		Surface:   p.CmuxSurfaceID,
	})
	if err != nil {
		fmt.Fprintf(warn, "✗ %s: %v\n  resume by hand: cd -- %s && %s\n", label, err, shQuote(p.Cwd), line)
		return err
	}
	fmt.Fprintf(out, "✓ %s resumed (pid %d) — still in Standby until your prompt\n", label, pid)
	return nil
}

// routeResume reads the tab: the launcher's ↻ resume prompt as its last
// line → Enter; its « new session » prompt (transcript missing) → refused;
// else the shell at an empty prompt → the resume line; anything else is
// refused with the tab's last lines. lines is that tail, for messages.
func routeResume(screen string, shellReady func() (bool, string)) (resumeRoute, string, error) {
	var tail []string
	for _, l := range strings.Split(screen, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			tail = append(tail, t)
		}
	}
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	lines := cmux.OneLine(strings.Join(tail, " / "), 160)
	last := ""
	if len(tail) > 0 {
		last = tail[len(tail)-1]
	}
	switch {
	case strings.HasPrefix(last, launcherResumePrompt):
		return resumeLauncher, lines, nil
	case strings.HasPrefix(last, launcherNewPrompt):
		return 0, lines, errors.New("the launcher would start a new session (transcript missing)")
	}
	if ok, why := shellReady(); !ok {
		return 0, lines, fmt.Errorf("the tab is not at its prompt (%s): %s", why, lines)
	}
	return resumeShell, lines, nil
}

// resumeByHand is the line the user can type himself: through the launcher
// when the cell is known, else the plain resume line.
func resumeByHand(p pigtail.PeerSession) string {
	if p.CellID != "" {
		return fmt.Sprintf("cd -- %s && c %s --resume %s", shQuote(p.Cwd), p.CellID, p.SessionID)
	}
	return fmt.Sprintf("cd -- %s && %s", shQuote(p.Cwd), cmux.ResumeLine(resumeWrapper(), p.ConfigDir, p.SessionID))
}

func resumeLabel(p pigtail.PeerSession) string {
	if p.Label != "" {
		return p.Label
	}
	return peers.Label(p)
}
