package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// `pigtail session prio-check` (maintenance window, contract C2): is a tab
// pinned in cmux, which is what Prio means since #244? Read by the window
// runner to serve Prio sessions first. CLI only, no daemon route: the pin is
// cmux's, read with the same workspacePinned `session prio` uses.

const sessionPrioCheckLong = `Tells whether the cmux tab --workspace is pinned (the prio). Two booleans,
nothing else (no folder, no subject): {"prio":bool,"available":bool}.

Exit 0 whenever cmux answered or failed: available:false means cmux could not
be read (then prio is false). An empty --workspace, or a tab in no window, is
prio:false. Neither --session nor --workspace is a usage error (exit 2).
Works without any CMUX_* variable (a detached runner); CMUX_SOCKET_PATH is
honoured when set.`

func sessionPrioCheckCmd() *cobra.Command {
	var session, workspace string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "prio-check",
		Short: "Tell whether a session's cmux tab is pinned (prio)",
		Long:  sessionPrioCheckLong,
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			c.SilenceErrors = true
			return runSessionPrioCheck(c.OutOrStdout(), c.ErrOrStderr(), os.Getenv, session, workspace, asJSON)
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "the session id asked about")
	cmd.Flags().StringVar(&workspace, "workspace", "", "the cmux workspace id of its tab")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the answer as JSON")
	return cmd
}

// prioCheckLister is the cmux read of prio-check; tests replace it. It aims
// at CMUX_SOCKET_PATH only when set (else cmux's default socket).
var prioCheckLister = func(getenv func(string) string) pinLister {
	cmux.SetSocketPath(getenv(cmux.EnvSocketPath))
	return cmux.SidebarCLI{}
}

type prioCheckResult struct {
	Prio      bool `json:"prio"`
	Available bool `json:"available"`
}

// runSessionPrioCheck prints its own errors on errOut, once, as "Error: …".
func runSessionPrioCheck(out, errOut io.Writer, getenv func(string) string, session, workspace string, asJSON bool) error {
	err := sessionPrioCheck(out, getenv, session, workspace, asJSON)
	if err != nil {
		fmt.Fprintf(errOut, "Error: %v\n", err)
	}
	return err
}

func sessionPrioCheck(out io.Writer, getenv func(string) string, session, workspace string, asJSON bool) error {
	session, workspace = strings.TrimSpace(session), strings.TrimSpace(workspace)
	if session == "" && workspace == "" {
		return withExitCode(2, errors.New("prio-check: --session or --workspace is required"))
	}
	res := prioCheckResult{Available: true}
	if workspace != "" {
		pinned, err := workspacePinned(prioCheckLister(getenv), workspace)
		var absent errNoWorkspace
		switch {
		case errors.As(err, &absent):
			// in no window: not pinned
		case err != nil:
			res.Available = false
		default:
			res.Prio = pinned
		}
	}
	if asJSON {
		return printPrioJSON(out, res)
	}
	switch {
	case !res.Available:
		fmt.Fprintln(out, "prio-check: unavailable")
	case res.Prio:
		fmt.Fprintln(out, "prio-check: yes")
	default:
		fmt.Fprintln(out, "prio-check: no")
	}
	return nil
}
