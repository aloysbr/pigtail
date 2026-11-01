package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// subjectWhichCmd answers "which subject does this pane work on?" — wrap-check,
// session hooks ask it.
func subjectWhichCmd() *cobra.Command {
	var workspace string
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "which",
		Annotations: scopedRead(),
		Short:       "Print the subject bound to a cmux workspace (default: this pane's)",
		Long: `Prints the subject bound to a cmux workspace — this pane's by default —
as "<id>\t<short title>", or one JSON object with --json. Exits 1 with
"no subject bound to cmux workspace <id>" when none the caller may read is
(suffixed "(bound to a subject this scope cannot read)" when one it may not
read is; --json then gives "state":"bound-elsewhere").`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			wsID, err := resolveCmuxWorkspaceArg(workspace)
			if err != nil {
				return err
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			l, err := c.CmuxWorkspace(cx, wsID)
			if err != nil {
				if !isNotFound(err) {
					return err
				}
				var st map[string]string
				if pigtail.IsCmuxBoundElsewhere(err) { // D25: the lookup already says a link exists, never whose
					st = map[string]string{"state": pigtail.WorkspaceStateBoundElsewhere, "workspace_id": wsID}
				} else {
					st = whichState(cx, c, wsID)
				}
				if asJSON {
					_ = json.NewEncoder(os.Stdout).Encode(st)
				}
				return fmt.Errorf("no subject bound to cmux workspace %s%s", wsID, whichSuffix(st))
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{
					"subject_id": l.Subject.ID, "short_title": l.Subject.ShortTitle,
					"scope": l.Subject.Scope, "workspace_id": wsID, "state": pigtail.WorkspaceStateBound,
				})
			}
			fmt.Printf("%s\t%s\n", l.Subject.ID, l.Subject.ShortTitle)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "cmux workspace UUID or workspace:N ref (default: $CMUX_WORKSPACE_ID)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object")
	return cmd
}

// whichState is the state of a workspace lookup found unbound, as `which --json` prints it.
func whichState(ctx context.Context, c *pigtail.Client, wsID string) map[string]string {
	st, err := c.CmuxWorkspaceState(ctx, wsID)
	if err != nil { // an old daemon's chi 404 body ends with a newline: keep the message on one line
		return map[string]string{"state": pigtail.WorkspaceStateUnknown, "workspace_id": wsID, "error": strings.Join(strings.Fields(err.Error()), " ")}
	}
	out := map[string]string{"state": st.State, "workspace_id": wsID}
	if st.OffTopic != nil {
		out["declared_at"] = st.OffTopic.DeclaredAt.Format(time.RFC3339)
		if st.OffTopic.Reason != "" {
			out["reason"] = st.OffTopic.Reason
		}
	}
	return out
}

// whichSuffix keeps "no subject bound to cmux workspace <id>" as the prefix every caller greps.
func whichSuffix(st map[string]string) string {
	switch st["state"] {
	case pigtail.WorkspaceStateOffTopic:
		return " (declared off-topic)"
	case pigtail.WorkspaceStateBoundElsewhere:
		return " (bound to a subject this scope cannot read)"
	case pigtail.WorkspaceStateUnknown:
		return " (state unknown: " + st["error"] + ")"
	}
	return ""
}
