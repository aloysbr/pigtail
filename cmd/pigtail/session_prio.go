package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// Prio = cmux's own pin on the session's tab (sub_x000000000000014, D3, D18,
// D19, D25, D28, D31). The truth is the pin itself, read in `workspace list`
// before any write; no table, no cache: a pin by hand is a prio, and the pin
// survives /clear and a fresh session in the same tab because it belongs to
// the tab.

const sessionPrioLong = `Pins this session's cmux tab (cmux's own pin): cmux puts it at the top of its
folder, above the maturity bands, and pigtail never reorders it. The pin belongs
to the tab: /clear or a fresh claude in the same tab keep it. It ends with
--clear, an unpin by hand in cmux, the session entering Standby, or the tab
closing. A session in Standby leaves Standby: the last gesture wins.

Run from inside the session (CMUX_WORKSPACE_ID), by hand or from your own
slash command. --json prints
{"session_id","workspace_id","pinned","changed","warning"}.`

// warnDaemonDown is the D25 warning: the pin is posed, the Standby could not
// be lifted because the daemon did not answer.
const warnDaemonDown = "daemon unreachable: standby not cleared"

var errNotInCmux = errors.New("not in a cmux workspace: nothing pinned")

func sessionPrioCmd() *cobra.Command {
	var clear, asJSON bool
	cmd := &cobra.Command{
		Use:   "prio",
		Short: "Pin this session's tab (prio)",
		Long:  sessionPrioLong,
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// As standby: a flag error stays cobra's to print; what RunE
			// returns is printed by runSessionPrio, once.
			c.SilenceErrors = true
			return runSessionPrio(c.Context(), c.OutOrStdout(), c.ErrOrStderr(), os.Getenv, clear, asJSON)
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "unpin it")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// cmuxPinOps reads and writes the pin of one workspace (D31: read first).
type cmuxPinOps struct {
	isPinned func(ws string) (bool, error)
	pin      func(ws string, on bool) error
}

// pinLister is the part of cmux.SidebarCLI workspacePinned reads.
type pinLister interface {
	ListWindows() ([]cmux.Window, error)
	ListWorkspaces(windowID string) ([]cmux.WorkspaceInfo, error)
}

type pinWriter interface {
	SetPinned(workspaceID string, pinned bool) error
}

func cmuxPinOpsWith(l pinLister, w pinWriter) cmuxPinOps {
	return cmuxPinOps{
		isPinned: func(ws string) (bool, error) { return workspacePinned(l, ws) },
		pin:      w.SetPinned,
	}
}

// cmuxPinFor wires the real cmux, aimed at the caller's CMUX_SOCKET_PATH (a
// live pass on NIGHTLY); every call strips the other CMUX_* variables, so the
// target is the explicit UUID only. Tests replace it.
var cmuxPinFor = func(getenv func(string) string) cmuxPinOps {
	cmux.SetSocketPath(getenv(cmux.EnvSocketPath))
	return cmuxPinOpsWith(cmux.SidebarCLI{}, cmux.FoldersCLI{})
}

// workspacePinned finds ws in every cmux window (`list-windows`, then
// `workspace list --window` on each: the window never comes from the env,
// D31) and returns its `pinned`.
func workspacePinned(l pinLister, ws string) (bool, error) {
	wins, err := l.ListWindows()
	if err != nil {
		return false, err
	}
	for _, w := range wins {
		rows, err := l.ListWorkspaces(w.ID)
		if err != nil {
			return false, err
		}
		for _, r := range rows {
			if strings.EqualFold(r.ID, ws) {
				return r.Pinned, nil
			}
		}
	}
	return false, errNoWorkspace{ws}
}

// errNoWorkspace: ws is in no cmux window (a successful read, not an outage).
type errNoWorkspace struct{ ws string }

func (e errNoWorkspace) Error() string { return fmt.Sprintf("workspace %s not found in cmux", e.ws) }

// prioDeps are sessionPrio's seams: the pin, and the Standby lift (the only
// part that needs the session and the daemon).
type prioDeps struct {
	cmuxPinOps
	clearStandby func(ctx context.Context, session string) error
}

var prioDepsFor = func(getenv func(string) string) prioDeps {
	return prioDeps{
		cmuxPinOps: cmuxPinFor(getenv),
		clearStandby: func(ctx context.Context, session string) error {
			return sessionClient(getenv).ClearSessionStandby(ctx, session)
		},
	}
}

// runSessionPrio prints its own errors on errOut, once, as "Error: …".
func runSessionPrio(ctx context.Context, out, errOut io.Writer, getenv func(string) string, clear, asJSON bool) error {
	return runSessionPrioWith(ctx, out, errOut, getenv, clear, asJSON, prioDepsFor(getenv))
}

func runSessionPrioWith(ctx context.Context, out, errOut io.Writer, getenv func(string) string, clear, asJSON bool, d prioDeps) error {
	err := sessionPrio(ctx, out, getenv, clear, asJSON, d)
	if err != nil {
		fmt.Fprintf(errOut, "Error: %v\n", err)
	}
	return err
}

type prioResult struct {
	SessionID   string `json:"session_id"`
	WorkspaceID string `json:"workspace_id"`
	Pinned      bool   `json:"pinned"`
	Changed     bool   `json:"changed"`
	Warning     string `json:"warning,omitempty"`
}

// sessionPrio pins (or with clear unpins) the tab of CMUX_WORKSPACE_ID. It
// never resolves the session to pin or unpin (an unpin run while a session
// closes must not fail on a session the daemon does not know); only the Standby lift of a pin
// uses CLAUDE_CODE_SESSION_ID, and its failure is a warning, never a failure.
func sessionPrio(ctx context.Context, out io.Writer, getenv func(string) string, clear, asJSON bool, d prioDeps) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ws := strings.TrimSpace(getenv(cmux.EnvWorkspaceID))
	if ws == "" {
		return errNotInCmux
	}
	pinned, err := d.isPinned(ws)
	if err != nil {
		return err
	}
	res := prioResult{SessionID: getenv("CLAUDE_CODE_SESSION_ID"), WorkspaceID: ws}
	if clear {
		if pinned {
			if err := d.pin(ws, false); err != nil {
				return err
			}
			res.Changed = true
		}
		line := "prio: not pinned"
		if res.Changed {
			line = "prio: unpinned"
		}
		return printPrio(out, asJSON, res, line)
	}
	if !pinned {
		if err := d.pin(ws, true); err != nil {
			return err
		}
		res.Changed = true
	}
	res.Pinned = true
	// The last gesture wins (D9 of 19/10): the pin lifts the Standby.
	res.Warning = liftStandby(ctx, d, res.SessionID)
	return printPrio(out, asJSON, res, "prio: pinned")
}

// liftStandby ends the session's Standby after a pin and returns the warning
// to show, "" when lifted or when there was none (404).
func liftStandby(ctx context.Context, d prioDeps, session string) string {
	if session == "" {
		return "standby not cleared: no Claude session id (CLAUDE_CODE_SESSION_ID unset)"
	}
	err := d.clearStandby(ctx, session)
	switch {
	case err == nil, isNotFound(err):
		return ""
	case isDaemonDown(err):
		return warnDaemonDown
	}
	return "standby not cleared: " + err.Error()
}

func printPrio(out io.Writer, asJSON bool, res prioResult, line string) error {
	if asJSON {
		return printPrioJSON(out, res)
	}
	fmt.Fprintln(out, line)
	if res.Warning != "" {
		fmt.Fprintln(out, "warning: "+res.Warning)
	}
	return nil
}

func printPrioJSON(out io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", b)
	return err
}
