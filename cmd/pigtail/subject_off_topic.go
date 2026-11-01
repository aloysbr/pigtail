package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func subjectOffTopicCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "off-topic",
		Short: "Declare, lift or list cmux workspaces that work on no subject, by choice",
	}
	cmd.AddCommand(offTopicDeclareCmd(), offTopicLiftCmd(), offTopicListCmd(), offTopicSetSpaceCmd())
	return cmd
}

func offTopicDeclareCmd() *cobra.Command {
	var (
		workspace, reason, spaceName string
		crossScope                   bool
	)
	cmd := &cobra.Command{
		Use:         "declare",
		Annotations: scopedWrite(),
		Short:       "Declare this pane's workspace (or --workspace) off-topic",
		Long: `Declares a cmux workspace off-topic: the session there answers a question,
a micro-fix or an administrative request, and works on no subject. The declaration
is dropped when the workspace closes, when the declaring session ends (the
SessionEnd hook runs lift --if-session) or a subject is bound to it. A bound
workspace is refused (409): detach it first. A first declaration must resolve
a space (--space, "Inbox" included, or a sub_… cited in --reason): otherwise it
is refused (400). Dragging the tab into a space folder by hand rewrites that
space afterwards.`,
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
			if crossScope { // no effect here: the row takes the caller's scope; declared for TestScopedCommandsDeclareAccess
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			spaceName = strings.TrimSpace(spaceName)
			o, err := c.DeclareOffTopic(cx, wsID, pigtail.DeclareOffTopicRequest{SessionID: offTopicCallerSession(), Reason: reason, Space: spaceName})
			if err != nil {
				return err
			}
			// A masked answer (another scope's row) or a --space the daemon left
			// unchanged (Q5) names no folder (D7, sub_x000000000000019).
			if o.Scope == "" || (spaceName != "" && o.ResolvedSpace != spaceName) {
				fmt.Printf("✓ cmux workspace %s already declared off-topic elsewhere: folder unchanged\n", o.CmuxWorkspaceID)
				return nil
			}
			fmt.Printf("✓ cmux workspace %s declared off-topic (since %s) → folder %s\n", o.CmuxWorkspaceID, o.DeclaredAt.Local().Format("15:04"), offTopicFolder(*o))
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "cmux workspace UUID or workspace:N ref (default: $CMUX_WORKSPACE_ID)")
	cmd.Flags().StringVar(&reason, "reason", "", "why, one line (200 characters at most)")
	cmd.Flags().StringVar(&spaceName, "space", "", "cmux sidebar folder for this off-topic session, one of pigtail theme spaces; required unless a sub_… cited in --reason (not \"<id> refusé\") resolves one; \""+space.NoSubject+"\" files it in "+space.NoSubject)
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// offTopicFolder is the folder the daemon resolved for a declaration (D1), the
// no-subject folder when none.
func offTopicFolder(o pigtail.WorkspaceOffTopic) string {
	if o.ResolvedSpace == "" {
		return space.NoSubject
	}
	return o.ResolvedSpace
}

func offTopicLiftCmd() *cobra.Command {
	var (
		workspace, ifSession string
		crossScope           bool
	)
	cmd := &cobra.Command{
		Use:         "lift",
		Annotations: scopedWrite(),
		Short:       "Lift the off-topic declaration of this pane's workspace (or --workspace)",
		Args:        cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			wsID, err := resolveCmuxWorkspaceArg(workspace)
			if err != nil {
				return err
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope { // a perso (master) cell lifting a work declaration: without it, CanWrite refuses and the route answers 404
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c.LiftOffTopic(cx, wsID, ifSession); err != nil {
				if isNotFound(err) {
					return fmt.Errorf("no off-topic declaration on cmux workspace %s", wsID)
				}
				return err
			}
			fmt.Printf("✓ cmux workspace %s: off-topic lifted\n", wsID)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "cmux workspace UUID or workspace:N ref (default: $CMUX_WORKSPACE_ID)")
	cmd.Flags().StringVar(&ifSession, "if-session", "", "lift only if this Claude session declared it (SessionEnd hook); else exit 1")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func offTopicListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "ls",
		Annotations: scopedRead(),
		Short:       "List the off-topic workspaces this cell may read",
		Args:        cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			rows, err := c.ListOffTopic(cx)
			if err != nil {
				return err
			}
			if asJSON {
				if rows == nil {
					rows = []pigtail.WorkspaceOffTopic{}
				}
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Println("no off-topic workspace")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WORKSPACE\tSINCE\tSESSION\tFOLDER\tREASON")
			for _, o := range rows {
				sid := o.SessionID
				if sid == "" {
					sid = "-"
				}
				folder := offTopicFolder(o)
				if o.ResolvedSpace != "" && o.Space == "" {
					folder += " (inferred)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", o.CmuxWorkspaceID, o.DeclaredAt.Local().Format("2006-01-02 15:04"), sid, folder, o.Reason)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON array")
	return cmd
}

// offTopicCallerSession is the calling Claude session's id, "" when pigtail cannot tell (never an error).
func offTopicCallerSession() string {
	caller, found, err := spawnCallerClaude()
	if err != nil || !found {
		return ""
	}
	sid, ok := spawnCallerSession(claudeConfigDir(os.Getenv), caller.PID)
	if !ok {
		return ""
	}
	return sid
}

func offTopicSetSpaceCmd() *cobra.Command {
	var (
		workspace, spaceName string
		crossScope           bool
	)
	cmd := &cobra.Command{
		Use:         "set-space",
		Annotations: scopedWrite(),
		Short:       "Master only: rewrite the space (sidebar folder) of an off-topic workspace, nothing else",
		Long: `Rewrites only the space of an existing off-topic declaration; the session, the
reason, the scope and the declaration time stay. --space '' lets the daemon
infer the folder again. Reserved to the master cell; a row of another scope needs
--cross-scope. Dragging the tab into a space folder by hand rewrites the space
the same way.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("space") {
				return fmt.Errorf("--space is required (use --space '' to let the daemon infer it)")
			}
			wsID, err := resolveCmuxWorkspaceArg(workspace)
			if err != nil {
				return err
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			o, err := c.SetOffTopicSpace(cx, wsID, strings.TrimSpace(spaceName))
			if err != nil {
				return err
			}
			fmt.Printf("✓ cmux workspace %s: space set → folder %s\n", o.CmuxWorkspaceID, offTopicFolder(*o))
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "cmux workspace UUID or workspace:N ref (default: $CMUX_WORKSPACE_ID)")
	cmd.Flags().StringVar(&spaceName, "space", "", "the new space, one of pigtail theme spaces ('' = let the daemon infer it)")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}
