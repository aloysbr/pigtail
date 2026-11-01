package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// confirmYes reads one line and reports whether it's an affirmative answer.
// A bare newline (user pressed Enter) is affirmative; a closed/empty stream
// (EOF with no input, i.e. non-TTY) is not.
func confirmYes(r io.Reader) bool {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return false // closed/empty stream → not an affirmative
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	default:
		return false
	}
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isNotFound(err error) bool {
	var ae *pigtail.APIError
	return errors.As(err, &ae) && ae.Code == pigtail.ErrNotFound
}

func newCmd() *cobra.Command {
	var projectID, branch, baseRef, subjectID string
	var quiet, force, reuse, forceNew, noFetch bool
	cmd := &cobra.Command{
		Use:   "new [intent]",
		Short: "Create a worktree (and ports) without spawning an agent",
		Long: `Creates a new git worktree and allocates dev/storybook ports, then prints
the absolute worktree path. No agent is spawned — use this for bootstrap.

The first line of output is always the worktree path, so it can be consumed
by a shell:  cd "$(pigtail new --quiet 'my intent')"

Examples:
  pigtail new "redesign the dev palette"
  pigtail new --branch feat/foo --quiet`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// stdout carries the path contract and nothing else; every human
			// message goes to stderr. Both are indirected through cobra so a
			// test can hold the two streams apart — which is the only way to
			// check a contract that is about *which stream* a line lands on.
			stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
			intent := strings.Join(args, " ")
			if intent == "" && branch == "" {
				return fmt.Errorf("provide an intent or --branch")
			}
			if projectID == "" {
				projectID = resolveLocalProjectID()
			}
			c := newClient()
			cx, cancel := context.WithTimeout(context.Background(), devserver.NewWorkspaceTimeout)
			defer cancel()
			if subjectID != "" && !forceNew {
				sc, err := callerScope()
				if err != nil {
					return err
				}
				existing, err := c.WithScope(sc).SubjectWorktree(cx, subjectID)
				if err != nil && !isNotFound(err) {
					return err
				}
				if existing != nil && existing.Path != "" && pathExists(existing.Path) {
					if reuse {
						fmt.Fprintln(stdout, existing.Path)
						bindNewToSubject(cx, c, subjectID, "", quiet, stderr)
						return nil
					}
					if !isInteractive() {
						return fmt.Errorf("subject %s already has worktree %s (%s); pass --reuse to use it or --force-new to create another",
							subjectID, existing.ID, existing.Path)
					}
					fmt.Fprintf(stderr, "subject %s already has worktree %s (%s) — reuse it? [Y/n] ", subjectID, existing.ID, existing.Path)
					if confirmYes(os.Stdin) {
						fmt.Fprintln(stdout, existing.Path)
						bindNewToSubject(cx, c, subjectID, "", quiet, stderr)
						return nil
					}
					// declined → fall through and create a new worktree
				}
			}
			ws, err := c.New(cx, pigtail.NewWorkspaceRequest{
				ProjectID:      projectID,
				Intent:         intent,
				BranchOverride: branch,
				BaseRef:        baseRef,
				NoFetch:        noFetch,
				Force:          force,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, ws.Path)
			if !quiet {
				b := ws.DisplayRef
				parts := make([]string, 0, len(ws.Ports))
				for k, v := range ws.Ports {
					parts = append(parts, fmt.Sprintf("%s=%d", k, v))
				}
				sort.Strings(parts)
				fmt.Fprintf(stdout, "✓ worktree created · branch %s · %s\n", b, strings.Join(parts, " "))
				// The drift this create just closed — or refused to close.
				// stderr, always: the first line of stdout is a path other
				// tools consume verbatim (`cd "$(pigtail new …)"`), and a
				// second stdout line would be read as part of it by anything
				// less careful than a shell command substitution.
				if notice := mainRefreshNotice(ws.MainRefresh); notice != "" {
					fmt.Fprintln(stderr, notice)
				}
			}
			if subjectID != "" {
				bindNewToSubject(cx, c, subjectID, ws.ID, quiet, stderr)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "project_id (defaults to prj_<cwd basename>)")
	cmd.Flags().StringVar(&branch, "branch", "", "branch override (skip slug derivation)")
	cmd.Flags().StringVar(&baseRef, "base", "", "base ref to branch from (default: project base_ref; a remote-tracking base like origin/main is auto-fetched first)")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip the auto-fetch of the base ref's remote before branching")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "print only the worktree path")
	cmd.Flags().BoolVar(&force, "force", false, "override the collision guard")
	cmd.Flags().StringVar(&subjectID, "subject", "", "subject id — reuse this subject's existing worktree if one is linked")
	cmd.Flags().BoolVar(&reuse, "reuse", false, "if the subject already has a worktree, print its path and exit (no prompt)")
	cmd.Flags().BoolVar(&forceNew, "force-new", false, "create a new worktree even if the subject already has one")
	return cmd
}

// bindNewToSubject links what pigtail new --subject just gave to the subject
// (D6 of sub_x000000000000027), as session spawn does: after a creation
// (wsID set) a phase implement/active on the new workspace, then — with a
// valid $CMUX_WORKSPACE_ID only — the cmux workspace, source ptnew. It never
// fails the command: every failure is a warning on stderr; --quiet cuts only
// the success line. stdout is untouched (its first line is the path).
func bindNewToSubject(ctx context.Context, c *pigtail.Client, subjectID, wsID string, quiet bool, stderr io.Writer) {
	warn := func(err error) {
		fmt.Fprintf(stderr, "warning: subject link not set (%s) — pigtail subject attach %s --source ptnew\n", strings.TrimSpace(err.Error()), subjectID)
	}
	sc, err := callerScope()
	if err != nil {
		warn(err)
		return
	}
	sc2 := c.WithScope(sc)
	if wsID != "" {
		if _, err := sc2.CreatePhase(ctx, subjectID, pigtail.SubjectPhase{Kind: pigtail.PhaseImplement, State: pigtail.PhaseActive, WorkspaceID: wsID}); err != nil {
			warn(err)
		}
	}
	cmuxWS, err := pigtail.NormalizeCmuxWorkspaceID(os.Getenv(cmux.EnvWorkspaceID))
	if err != nil {
		return // not in a cmux pane (or an unreadable id): nothing to link
	}
	if err := checkCmuxWorkspaceKnown(cmuxWS, true); err != nil {
		// Stale id: one warning, no attach proposal (it would be refused too).
		fmt.Fprintf(stderr, "warning: %v\n", err)
		return
	}
	if _, err := sc2.AttachCmuxWorkspace(ctx, subjectID, pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS, Source: pigtail.CmuxSourcePtnew}); err != nil {
		var ae *pigtail.APIError
		if errors.As(err, &ae) && ae.Code == pigtail.ErrConflict {
			fmt.Fprintf(stderr, "warning: cmux workspace already bound to another subject — not replaced (pigtail subject attach %s --replace)\n", subjectID)
			return
		}
		warn(err)
		return
	}
	if !quiet {
		fmt.Fprintf(stderr, "✓ cmux workspace bound to subject %s\n", subjectID)
	}
}
