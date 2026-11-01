package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// Standby (sub_x000000000000041, D4, D16): the /standby skill parks its own
// session; the next prompt the user types ends it (pigtail hook claude-wait).

const sessionStandbyLong = `Puts this Claude Code session in Standby: pigtail compacts it once it is idle
and out of focus, and the cmux sidebar files its workspace in the folded
« Standby » folder. No automaton types into it or claims its inbox; the next
prompt you type ends the Standby and brings it back to its space. A relaunch
of the same conversation (claude --resume) keeps it.

Run by a standby skill from inside the session (CLAUDE_CODE_SESSION_ID).
It refuses, writing nothing, while background work runs (its end would
relaunch the agent): a cron or wakeup listed by the session's last Stop
(recorded by pigtail hook claude-wait), a task pending in the transcript, a
Bash-tool shell, or a background row on screen — exit 3. When a reading cannot
be made (no Stop recorded for this turn, unreadable transcript or screen,
pigtail hooks off) and nothing was seen, it refuses too — exit 4.
--check runs that verification alone; --clear ends the Standby by hand.`

// The refusal texts (D29, D33, D43) are printed alone on stderr: the
// /standby skill relays that line as it is.
const (
	standbyRefusedFmt    = "Standby refusé — tâche de fond en cours : %s. Relance /standby à sa fin."
	standbyUnreadableFmt = "Standby refusé — je n'arrive pas à lire les tâches de fond (%s). Réessaie /standby dans un instant."
	standbyHooksOffFmt   = "Standby refusé — je n'arrive pas à lire les tâches de fond (hooks pigtail coupés : %s=off). Réactive-les pour utiliser /standby."
)

// errStandbyRefused marks a refusal already printed (exit 3 or 4).
var errStandbyRefused = errors.New("standby refused")

func sessionStandbyCmd() *cobra.Command {
	var clear, check bool
	cmd := &cobra.Command{
		Use:   "standby",
		Short: "Put this Claude session in Standby (the /standby skill runs it)",
		Long:  sessionStandbyLong,
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// Set here, not on the command: a flag error (before RunE) is
			// still cobra's to print; what RunE returns is printed by
			// runSessionStandby, once.
			c.SilenceErrors = true
			return runSessionStandby(c.Context(), c.OutOrStdout(), c.ErrOrStderr(), os.Getenv, clear, check)
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "end this session's Standby")
	cmd.Flags().BoolVar(&check, "check", false, "only check for background work (exit 3: work runs, 4: unreadable)")
	cmd.MarkFlagsMutuallyExclusive("clear", "check")
	return cmd
}

// runSessionStandby prints its own errors on errOut: a refusal as its text
// alone, anything else once as "Error: …".
func runSessionStandby(ctx context.Context, out, errOut io.Writer, getenv func(string) string, clear, check bool) error {
	err := sessionStandby(ctx, out, errOut, getenv, clear, check)
	if err != nil && !errors.Is(err, errStandbyRefused) {
		fmt.Fprintf(errOut, "Error: %v\n", err)
	}
	return err
}

func sessionStandby(ctx context.Context, out, errOut io.Writer, getenv func(string) string, clear, check bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	me, err := resolveSelf(readSelfEnv(getenv), nil)
	if err != nil {
		return err
	}
	c := sessionClient(getenv)
	if clear {
		// A session the daemon does not know is in no Standby: quiet success.
		if err := c.ClearSessionStandby(ctx, me); err != nil && !isNotFound(err) {
			return err
		}
		fmt.Fprintln(out, "standby cleared")
		return nil
	}
	if err := standbyRefusal(errOut, getenv, me); err != nil {
		return err
	}
	if check {
		fmt.Fprintln(out, "standby: no background work")
		return nil
	}
	if err := unpinBeforeStandby(errOut, getenv); err != nil {
		return err
	}
	sb, err := c.SetSessionStandby(ctx, me)
	if isNotFound(err) {
		// Not registered yet (its SessionStart ran while the daemon was
		// down): register it as itself, then retry once.
		registerSession(ctx, relayHookPayload{SessionID: me}, getenv)
		sb, err = c.SetSessionStandby(ctx, me)
	}
	if err != nil {
		return err
	}
	line := "standby: set (the next prompt you type ends it)"
	// Folder arrangement off: the tab never moves into « Standby », say so
	// on the same line (still a success). Unknown state: no warning.
	if sb != nil && sb.FoldersOff != "" {
		line += fmt.Sprintf(" — folder arrangement is off (%s): the tab stays where it is", sb.FoldersOff)
	}
	fmt.Fprintln(out, line)
	return nil
}

// standbyRefusal checks for background work before anything is written
// (D25): work seen → D29, exit 3; nothing seen but a reading failed → D33,
// exit 4; pigtail hooks off → the D43 text, exit 4 (no Stop is ever recorded).
func standbyRefusal(errOut io.Writer, getenv func(string) string, session string) error {
	for _, k := range []string{"PIGTAIL_WAIT", "PIGTAIL_RELAY"} {
		if relayOff(getenv, k) {
			fmt.Fprintf(errOut, standbyHooksOffFmt+"\n", k)
			return withExitCode(4, fmt.Errorf("%w: %s=off", errStandbyRefused, k))
		}
	}
	work, err := standbyBackgroundWork(standbyBgDepsFor(getenv), session)
	if len(work) > 0 {
		fmt.Fprintf(errOut, standbyRefusedFmt+"\n", strings.Join(work, ", "))
		return withExitCode(3, fmt.Errorf("%w: background work", errStandbyRefused))
	}
	if err != nil {
		fmt.Fprintf(errOut, standbyUnreadableFmt+"\n", err)
		return withExitCode(4, fmt.Errorf("%w: %w", errStandbyRefused, err))
	}
	return nil
}

// unpinBeforeStandby (D18, D29: the last gesture wins) takes the pin off the
// session's tab before the Standby is written: read first (D31), unpin only a
// pinned tab. A failed read or unpin writes no Standby and says so alone on
// stderr (Copy D23), never announcing a Standby. Outside cmux: nothing to read.
func unpinBeforeStandby(errOut io.Writer, getenv func(string) string) error {
	ws := strings.TrimSpace(getenv(cmux.EnvWorkspaceID))
	if ws == "" {
		return nil
	}
	ops := cmuxPinFor(getenv)
	pinned, err := ops.isPinned(ws)
	if err == nil && pinned {
		err = ops.pin(ws, false)
	}
	if err != nil {
		fmt.Fprintf(errOut, "standby: not set — unpin failed: %v\n", err)
		return withExitCode(1, fmt.Errorf("%w: unpin failed: %w", errStandbyRefused, err))
	}
	return nil
}
