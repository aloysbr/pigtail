package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	suggestTextMax     = 4096
	suggestRepoTimeout = 300 * time.Millisecond
	suggestTimeout     = 2 * time.Second
	msgNoSuggestRoute  = "this pigtail daemon has no subject suggest (restart it after make install)"
)

// cutText cuts s to at most max bytes on a rune boundary.
func cutText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// cleanDir makes dir absolute and resolves its symlinks; a failure keeps what
// it has.
func cleanDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

// suggestWorkspace is the workspace subject suggest sends: --workspace
// resolved (a failure is an error: an explicit gesture), else
// $CMUX_WORKSPACE_ID normalised (absent or invalid → none, never an error:
// that is the hooks' path).
func suggestWorkspace(flag string) (string, error) {
	if strings.TrimSpace(flag) != "" {
		return resolveCmuxWorkspaceArg(flag)
	}
	ws, err := pigtail.NormalizeCmuxWorkspaceID(os.Getenv(cmux.EnvWorkspaceID))
	if err != nil {
		return "", nil
	}
	return ws, nil
}

func subjectSuggestCmd() *cobra.Command {
	var (
		text, cwd, workspace string
		limit                int
		asJSON               bool
	)
	cmd := &cobra.Command{
		Use:         "suggest",
		Annotations: scopedRead(),
		Short:       "Rank the subjects this pane's request may belong to",
		Long: `Rank the subjects a request may belong to, from three signals: a PR the
subject's own log names (strong), the pane's linked worktree or branch
(strong), and the words of the subject's title (weak).

--text - reads the request from stdin (cut at 4096 bytes). The pane's
directory (--cwd, default the current one) gives its branch, default branch
and repository; the cmux workspace (--workspace, default $CMUX_WORKSPACE_ID)
adds its off-topic reason, if any.

--json prints one object: candidates (never null), auto (the id of the one
strong, writable candidate, else ""), elsewhere (counts of candidates in a
scope this cell may not read).`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if text == "-" {
				b, err := io.ReadAll(io.LimitReader(os.Stdin, suggestTextMax))
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				text = string(b)
			}
			text = cutText(text, suggestTextMax)
			if cwd == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				cwd = wd
			}
			cwd = cleanDir(cwd)
			ws, err := suggestWorkspace(workspace)
			if err != nil {
				return err
			}
			gctx, cancel := context.WithTimeout(context.Background(), suggestRepoTimeout)
			pane := git.ReadPaneRepo(gctx, cwd)
			cancel()

			c, err := newScopedClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), suggestTimeout)
			defer cancel()
			res, err := c.SuggestSubjects(ctx, pigtail.SuggestRequest{
				Text: text, Cwd: cwd, Branch: pane.Branch, DefaultBranch: pane.DefaultBranch, Repo: pane.Repo,
				WorkspaceID: ws, Limit: limit,
			})
			if err != nil {
				if pigtail.IsNoSuggestRoute(err) {
					return errors.New(msgNoSuggestRoute)
				}
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			for _, cand := range res.Candidates {
				title := cand.ShortTitle
				if title == "" {
					title = cand.Title
				}
				fmt.Printf("%s\t%s\t%s\t%s\t%s\n", cand.Strength, cand.Signal, cand.SubjectID, title, cand.Evidence)
			}
			if res.Elsewhere.Strong+res.Elsewhere.Weak > 0 {
				fmt.Printf("elsewhere: %d strong, %d weak\n", res.Elsewhere.Strong, res.Elsewhere.Weak)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&text, "text", "", `the request ("-" reads stdin)`)
	cmd.Flags().StringVar(&cwd, "cwd", "", "the pane's directory (default: the current one)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "cmux workspace UUID or workspace:N ref (default: $CMUX_WORKSPACE_ID)")
	cmd.Flags().IntVar(&limit, "limit", 3, "candidates to return, 1..10")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output one JSON object")
	return cmd
}
