package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sdkCmd is `pigtail sdk`: a read of the SDK sessions (docs/wire-protocol.md
// § SDK sessions). Answering, replying and ending are the web front's alone
// in lot 1: no write command here.
func sdkCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sdk", Short: "SDK sessions (Claude Agent SDK): list and show"}
	cmd.AddCommand(sdkLsCmd(), sdkShowCmd())
	return cmd
}

func sdkLsCmd() *cobra.Command {
	var subject string
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "ls",
		Short:       "List the SDK sessions you may read",
		Args:        cobra.NoArgs,
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			l, err := c.SDKSessions(cmd.Context(), subject)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(l)
			}
			if len(l.Sessions) == 0 {
				_, _ = fmt.Fprintln(out, "no SDK session")
				return nil
			}
			for _, s := range l.Sessions {
				live := ""
				if s.Live {
					live = "  live"
				}
				_, _ = fmt.Fprintf(out, "%s  %-8s  %s  turn %d%s\n", s.ID, s.State, s.SubjectID, s.Turn, live)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&subject, "subject", "", "only the sessions of this subject")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func sdkShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "show <id>",
		Short:       "Show one SDK session",
		Args:        cobra.ExactArgs(1),
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			s, err := c.SDKSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(s)
			}
			printSDKSession(out, s)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func printSDKSession(w io.Writer, s pigtail.SDKSession) {
	_, _ = fmt.Fprintf(w, "id        %s\nsubject   %s\nstate     %s\n", s.ID, s.SubjectID, s.State)
	if s.Reason != "" {
		_, _ = fmt.Fprintf(w, "reason    %s\n", s.Reason)
	}
	_, _ = fmt.Fprintf(w, "claude    %s\ncwd       %s\nturn      %d\npending   %d\n", s.ClaudeSessionID, s.Cwd, s.Turn, len(s.Pending))
	if s.ResumeCommand != "" {
		_, _ = fmt.Fprintf(w, "resume    %s\n", s.ResumeCommand)
	}
}

// sdkLiveGuard refuses a TUI launch or relaunch on a subject that has a live
// SDK session (CA3): two writers on one Claude session. A 503 SDK_OFF only
// means the daemon does not serve SDK sessions: a host started earlier can
// still be alive on disk, so the sessions directory is then read locally
// (read-only) and the answer is the same as the daemon's would be. Any other
// error refuses, because liveness is then unknown (fail-safe). errf builds the
// caller's error (spawnErr, respawnErr).
func sdkLiveGuard(ctx context.Context, c *pigtail.Client, subjectID string, errf func(string, ...any) error) error {
	l, err := c.SDKSessions(ctx, subjectID)
	if err != nil {
		var ae *pigtail.APIError
		if errors.As(err, &ae) && ae.Code == pigtail.ErrSDKOff {
			// An unreadable directory or meta.json reads as live, with the reason as the id.
			if id, live := (&sdksession.Registry{Dir: sdksession.DefaultDir()}).LiveForSubject(subjectID); live {
				return errf("subject %s already has a live session (SDK %s, daemon option off) — end it first", subjectID, id)
			}
			return nil
		}
		return retryable(retryShort, errf("subject %s: cannot read its SDK sessions (%v) — liveness unknown, nothing launched (fail-safe)", subjectID, err))
	}
	for _, s := range l.Sessions {
		if s.Live {
			return errf("subject %s already has a live session (SDK %s, %s) — end it from the web front first", subjectID, s.ID, strings.TrimSpace(s.State))
		}
	}
	return nil
}
