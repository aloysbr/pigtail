package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	findTimeout    = 5 * time.Second
	msgNoFindRoute = "this pigtail daemon has no subject find (restart it after make install)"
)

func subjectFindCmd() *cobra.Command {
	var (
		limit  int
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:         "find <words…>",
		Annotations: scopedRead(),
		Short:       "Find subjects by a few words of their title, short title or project",
		Long: `Rank the subjects a few words name — title and short title (prefixes and
two-letter acronyms count), the project's name as a bonus, every status
(closed ones a little lower), the last phase/progress/handoff/delivery/decision
line as the tie-break. Every search is recorded with the pane's cmux workspace
($CMUX_WORKSPACE_ID); link the chosen subject with
  pigtail subject attach --find <find_id> <subject_id>
or, without a link, pigtail subject pick <find_id> <subject_id>.

Plain output: one line per candidate (id, status, short title, project, last
activity), then the find id on stderr. --json prints one object: find_id,
query, candidates (never null), total, elsewhere (candidates of a scope this
cell may not read, never named).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if limit < 1 || limit > pigtail.SubjectFindLimitMax {
				return fmt.Errorf("--limit must be 1..%d (got %d)", pigtail.SubjectFindLimitMax, limit)
			}
			ws, err := suggestWorkspace("")
			if err != nil {
				return err
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), findTimeout)
			defer cancel()
			res, err := c.FindSubjects(ctx, pigtail.SubjectFindRequest{Query: strings.Join(args, " "), WorkspaceID: ws, Limit: limit})
			if err != nil {
				if pigtail.IsNoSuggestRoute(err) {
					return errors.New(msgNoFindRoute)
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
				day := "-"
				if cand.LastActivity != nil {
					day = cand.LastActivity.Local().Format("2006-01-02")
				}
				fmt.Printf("%s\t%s\t%s\t%s\t%s\n", cand.SubjectID, cand.Status, title, cand.Theme, day)
			}
			fmt.Fprintln(os.Stderr, findFooter(res))
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", pigtail.SubjectFindLimitDefault, "at most N candidates (1..20)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object")
	return cmd
}

// findFooter is the stderr line of a plain search: its id and how to link.
func findFooter(res *pigtail.SubjectFindResult) string {
	elsewhere := ""
	if res.Elsewhere > 0 {
		elsewhere = fmt.Sprintf(" (%d elsewhere)", res.Elsewhere)
	}
	if len(res.Candidates) == 0 {
		return fmt.Sprintf("find %s: no candidate%s", res.FindID, elsewhere)
	}
	return fmt.Sprintf("find %s: %d candidates%s — link: pigtail subject attach --find %s <subject_id>", res.FindID, res.Total, elsewhere, res.FindID)
}

func subjectPickCmd() *cobra.Command {
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         "pick <find_id> <subject_id>",
		Annotations: scopedWrite(),
		Short:       "Record the subject a search (subject find) led to, without linking a workspace",
		Args:        cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			ctx, cancel := context.WithTimeout(context.Background(), findTimeout)
			defer cancel()
			p, err := c.PickSubjectFind(ctx, args[0], args[1])
			if err != nil {
				if pigtail.IsBareNoRoute(err) {
					return errors.New(msgNoFindRoute)
				}
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(p)
			}
			mark := "✓"
			if !p.Created {
				mark = "="
			}
			place := fmt.Sprintf("rank %d/%d", p.Rank, p.Candidates)
			if p.Rank == 0 {
				place = fmt.Sprintf("not among the %d candidates", p.Candidates)
			}
			fmt.Printf("%s %s → %s (%s, %d searches)\n", mark, p.FindID, p.SubjectID, place, p.Searches)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}
