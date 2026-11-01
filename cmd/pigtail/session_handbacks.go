package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func sessionHandbacksCmd() *cobra.Command {
	var (
		since  string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:         "handbacks",
		Annotations: scopedRead(),
		Short:       "Count the turns handed back without a declared wait, by the session's last skill",
		Long: `Count, per skill, the turns Claude Code ended (hook Stop) while the session
had declared no wait in that turn and no StopFailure came (D12). A turn is
credited to the last skill the session invoked ("(none)" before any). Only
interactive sessions count; subagents are ignored.

Read-only, filtered by the caller's scope. --json is the contract the weekly
review reads: {since, skills: [{skill, count, last_at}]}.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			from, err := parseSince(since, time.Now())
			if err != nil {
				return err
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			r, err := c.SessionHandbacks(cx, from)
			if err != nil {
				return err
			}
			return renderHandbacks(os.Stdout, *r, asJSON)
		},
	}
	cmd.Flags().StringVar(&since, "since", "7d", "window: a duration (24h, 7d), a date (2026-09-22) or unix millis")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the daemon's answer as JSON")
	return cmd
}

// renderHandbacks prints r as JSON unchanged, or as a SKILL COUNT LAST table
// sorted by count, highest first.
func renderHandbacks(w io.Writer, r pigtail.SessionHandbacksResponse, asJSON bool) error {
	if asJSON {
		if r.Skills == nil {
			r.Skills = []pigtail.SkillHandbacks{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	rows := append([]pigtail.SkillHandbacks(nil), r.Skills...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Skill < rows[j].Skill
	})
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SKILL\tCOUNT\tLAST")
	for _, s := range rows {
		name := s.Skill
		if name == "" {
			name = "(none)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\n", name, s.Count, s.LastAt.Local().Format("02/01 15:04"))
	}
	return tw.Flush()
}
