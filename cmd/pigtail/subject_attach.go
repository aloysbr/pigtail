package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// resolveCmuxRef turns `workspace:N` into a workspace UUID (a var for tests).
var resolveCmuxRef = cmux.ResolveWorkspaceRef

// resolveCmuxWorkspaceArg returns the cmux workspace UUID a link command acts
// on: --workspace (a UUID, or a `workspace:N` ref resolved through cmux), else
// the pane's own $CMUX_WORKSPACE_ID.
func resolveCmuxWorkspaceArg(flag string) (string, error) {
	v := strings.TrimSpace(flag)
	if v == "" {
		v = os.Getenv(cmux.EnvWorkspaceID)
		if v == "" {
			return "", fmt.Errorf("not in a cmux pane ($%s unset): pass --workspace <UUID|workspace:N>", cmux.EnvWorkspaceID)
		}
	}
	if strings.HasPrefix(v, "workspace:") {
		return resolveCmuxRef(v)
	}
	return pigtail.NormalizeCmuxWorkspaceID(v)
}

// Refusals of a link write for a workspace id cmux does not know. One
// constant each, reused by the tests (exact comparison).
const (
	// cmuxStaleEnvMsgFmt: %s = the UUID, %s = the full resume command.
	cmuxStaleEnvMsgFmt = "$CMUX_WORKSPACE_ID (%s) est inconnu de cmux : l'id de workspace de cet environnement est périmé, typiquement après une reprise de session par cmux (redémarrage). Rien n'a été lié. Ouvre un nouvel onglet cmux à la main (⌘N) et reprends-y la session avec `%s` : depuis cet onglet, spawn, respawn et session move refusent aussi."
	// cmuxStaleFlagMsgFmt: %s = the UUID.
	cmuxStaleFlagMsgFmt = "workspace cmux %s inconnu de cmux (fermé, ou id périmé après une reprise cmux). Rien n'a été lié. Lie l'onglet par sa référence : --workspace workspace:N"
)

// resumeCommand is the full command that resumes this session from a bare
// ⌘N tab (a bare `claude` there starts from ~/.claude, not this cell).
func resumeCommand() string {
	sid := os.Getenv("CLAUDE_CODE_SESSION_ID")
	if sid == "" {
		return "claude --resume <id de session>"
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return "CLAUDE_CONFIG_DIR=" + dir + " claude --resume " + sid
	}
	return "claude --resume " + sid
}

// cmuxStaleMessage is the refusal text for wsID.
func cmuxStaleMessage(wsID string, fromEnv bool) string {
	if fromEnv {
		return fmt.Sprintf(cmuxStaleEnvMsgFmt, wsID, resumeCommand())
	}
	return fmt.Sprintf(cmuxStaleFlagMsgFmt, wsID)
}

// checkCmuxWorkspaceKnown refuses a workspace UUID cmux answers it does not
// know (identify: caller null). Any other outcome (cmux absent, slow, in
// error, unexpected output) returns nil: the link is written unchecked.
func checkCmuxWorkspaceKnown(wsID string, fromEnv bool) error {
	known, err := cmux.WorkspaceKnown(wsID)
	if err != nil || known {
		return nil
	}
	return errors.New(cmuxStaleMessage(wsID, fromEnv))
}

// resolveCmuxWorkspaceForLink is resolveCmuxWorkspaceArg for a command that
// WRITES a link: an id that does not come from a `workspace:N` ref (already
// listed by cmux) is checked against cmux first. detach, which and the
// readers keep resolveCmuxWorkspaceArg: unlinking a stale id must stay possible.
func resolveCmuxWorkspaceForLink(flag string) (string, error) {
	id, err := resolveCmuxWorkspaceArg(flag)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(strings.TrimSpace(flag), "workspace:") {
		return id, nil
	}
	if err := checkCmuxWorkspaceKnown(id, strings.TrimSpace(flag) == ""); err != nil {
		return "", err
	}
	return id, nil
}

func subjectAttachCmd() *cobra.Command {
	var (
		workspace, source, findID    string
		replace, retitle, crossScope bool
	)
	cmd := &cobra.Command{
		Use:         "attach <subject_id>",
		Annotations: scopedWrite(),
		Short:       "Bind a cmux workspace (default: this pane's) to a subject",
		Long: `Bind a cmux workspace to a subject, so pigtail knows which subject the
workspace works on (session ls SUBJECT column, cmux sidebar).

A workspace carries one subject: the first link wins. Binding a workspace
already bound to another subject fails (409) unless --replace is given, and
--replace also needs write access to that other subject.

A workspace UUID cmux does not know (a stale $CMUX_WORKSPACE_ID after cmux resumed the session) is refused and nothing is linked; when cmux does not answer, the link is written unchecked.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wsID, err := resolveCmuxWorkspaceForLink(workspace)
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
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			b, err := c.AttachCmuxWorkspace(cx, args[0], pigtail.AttachCmuxWorkspaceRequest{
				CmuxWorkspaceID: wsID, Source: source, Replace: replace, Retitle: retitle, FindID: findID,
			})
			if err != nil {
				return err
			}
			// The echoed find_id is checked before the success line: a daemon that
			// ignores it linked the workspace but recorded no pick.
			if findID != "" && b.FindID != findID {
				return fmt.Errorf("the daemon ignored --find (restart it after make install), then: pigtail subject pick %s %s", findID, args[0])
			}
			fmt.Printf("✓ cmux workspace %s → %s (%s)\n", b.CmuxWorkspaceID, b.SubjectID, b.Source)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "cmux workspace UUID or workspace:N ref (default: $CMUX_WORKSPACE_ID)")
	cmd.Flags().StringVar(&source, "source", pigtail.CmuxSourceAttach, "who writes the link: attach | subject-work | ptnew")
	cmd.Flags().BoolVar(&replace, "replace", false, "move the workspace off the subject it is bound to")
	cmd.Flags().BoolVar(&retitle, "retitle", false, "hand the workspace description back to pigtail (the title always is)")
	cmd.Flags().StringVar(&findID, "find", "", "the subject find (find_id) that led to this subject: records it as the search's pick")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func subjectDetachCmd() *cobra.Command {
	var (
		workspace  string
		crossScope bool
	)
	cmd := &cobra.Command{
		Use:         "detach <subject_id>",
		Annotations: scopedWrite(),
		Short:       "Unbind a cmux workspace (default: this pane's) from a subject",
		Args:        cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
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
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.DetachCmuxWorkspace(cx, args[0], wsID); err != nil {
				return err
			}
			fmt.Printf("✓ cmux workspace %s unbound from %s\n", wsID, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "cmux workspace UUID or workspace:N ref (default: $CMUX_WORKSPACE_ID)")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}
