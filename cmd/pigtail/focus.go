package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// pigtail focus (sub_x000000000000007): temporary sidebar folders. Visible
// texts are focus D24: any change goes back to the user.

const focusStampLayout = "2006-01-02 15:04"

func focusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "focus",
		Short: "Create, fill and end temporary cmux sidebar folders (focus)",
		Long: `A focus gathers the sessions of some themes and subjects in a temporary
cmux sidebar folder, on top of the sidebar, until it ends (by hand or at
--until); its sessions then go back to their space. A focus belongs to the
cell's scope and holds that scope's themes and subjects only.`,
	}
	cmd.AddCommand(focusCreateCmd(), focusLsCmd(), focusAddCmd(), focusRmCmd(), focusEndCmd())
	return cmd
}

// focusMembers turns --theme / --subject ids into members, themes first.
func focusMembers(themes, subjects []string) []pigtail.FocusMember {
	out := make([]pigtail.FocusMember, 0, len(themes)+len(subjects))
	for _, id := range themes {
		out = append(out, pigtail.FocusMember{Kind: pigtail.FocusMemberTheme, ID: id})
	}
	for _, id := range subjects {
		out = append(out, pigtail.FocusMember{Kind: pigtail.FocusMemberSubject, ID: id})
	}
	return out
}

func addFocusMemberFlags(cmd *cobra.Command, themes, subjects *[]string) {
	cmd.Flags().StringArrayVar(themes, "theme", nil, "theme id (thm_…), repeatable")
	cmd.Flags().StringArrayVar(subjects, "subject", nil, "subject id (sub_…), repeatable")
}

// parseUntil reads --until (focus D16, D25) in now's location: a Go duration
// or Nd, a date (until the end of that day: the local midnight after it), a
// local date and time, or RFC 3339.
func parseUntil(v string, now time.Time) (time.Time, error) {
	loc := now.Location()
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(d), nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && strings.HasSuffix(v, "d") && n > 0 {
		return now.Add(time.Duration(n) * 24 * time.Hour), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		// Never midnight + 24h: wrong on a day the clock changes.
		return time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc), nil
	}
	if t, err := time.ParseInLocation(focusStampLayout, v, loc); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--until %q: want a duration (3h, 2d), a date (2026-10-16), a date and time (2026-10-16 18:00) or RFC 3339", v)
}

// resolveFocus turns <focus> into an id: an fcs_ id as is, else the name of
// an open focus the cell may read.
func resolveFocus(ctx context.Context, c *pigtail.Client, arg string) (string, error) {
	if strings.HasPrefix(arg, pigtail.PrefixFocus) {
		return arg, nil
	}
	fs, _, err := c.ListFocus(ctx, false)
	if err != nil {
		return "", err
	}
	for _, f := range fs {
		if f.Name == arg {
			return f.ID, nil
		}
	}
	return "", fmt.Errorf("no open focus named %q", arg)
}

func focusStamp(t time.Time) string { return t.Local().Format(focusStampLayout) }

func focusEnds(f *pigtail.Focus) string {
	if f.EndsAt == nil {
		return "manually"
	}
	return focusStamp(*f.EndsAt)
}

func focusMemberIDs(f *pigtail.Focus) string {
	ids := make([]string, 0, len(f.Members))
	for _, m := range f.Members {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ", ")
}

func focusEndWord(reason string) string {
	if reason == pigtail.FocusEndManual {
		return "by hand"
	}
	return reason
}

// printFocusList writes `focus ls` (focus D24).
func printFocusList(w io.Writer, fs []pigtail.Focus, note string) {
	for i := range fs {
		f := &fs[i]
		if f.EndedAt != nil {
			fmt.Fprintf(w, "%s  %s  ended (%s) %s\n", f.Name, f.ID, focusEndWord(f.EndReason), focusStamp(*f.EndedAt))
		} else {
			themes, subjects := 0, 0
			for _, m := range f.Members {
				if m.Kind == pigtail.FocusMemberTheme {
					themes++
				} else {
					subjects++
				}
			}
			fmt.Fprintf(w, "%s  %s  ends %s  %d theme(s), %d subject(s)\n", f.Name, f.ID, focusEnds(f), themes, subjects)
		}
		for _, m := range f.Members {
			if m.Missing {
				fmt.Fprintf(w, "  %s %s (not found)\n", m.Kind, m.ID)
			}
		}
	}
	if note != "" {
		fmt.Fprintln(w, note)
	}
}

func focusCreateCmd() *cobra.Command {
	var (
		themes, subjects []string
		until            string
		crossScope       bool
	)
	cmd := &cobra.Command{
		Use:         "create <name>",
		Annotations: scopedWrite(),
		Short:       "Create a focus of this cell's scope",
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var endsAt *time.Time
			if until != "" {
				t, err := parseUntil(until, time.Now())
				if err != nil {
					return err
				}
				endsAt = &t
			}
			// --cross-scope is ignored: a focus takes the cell's scope (D6);
			// declared for TestScopedCommandsDeclareAccess.
			_ = crossScope
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f, err := c.CreateFocus(cx, args[0], endsAt, focusMembers(themes, subjects))
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "✓ created %s  %s  %s\n", f.ID, f.Name, f.Scope)
			fmt.Fprintf(w, "  ends: %s\n", focusEnds(f))
			fmt.Fprintf(w, "  members: %s\n", focusMemberIDs(f))
			fmt.Fprintln(w, "  folder: shows at the next pass while cmux is not focused, once a member has an open session")
			if f.Note != "" {
				fmt.Fprintf(w, "  note: %s\n", f.Note)
			}
			return nil
		},
	}
	addFocusMemberFlags(cmd, &themes, &subjects)
	cmd.Flags().StringVar(&until, "until", "", "end: a duration (3h, 2d), a date (2026-10-16, until the end of that day), a date and time (2026-10-16 18:00) or RFC 3339")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func focusLsCmd() *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:         "ls",
		Annotations: scopedRead(),
		Short:       "List the open focus (--all: ended ones too)",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fs, note, err := c.ListFocus(cx, all)
			if err != nil {
				return err
			}
			if asJSON {
				if fs == nil {
					fs = []pigtail.Focus{}
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Focus []pigtail.Focus `json:"focus"`
					Note  string          `json:"note,omitempty"`
				}{fs, note})
			}
			printFocusList(cmd.OutOrStdout(), fs, note)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include ended focus")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// focusMembersWrite is add/rm: one call per member, then the focus as it reads.
func focusMembersWrite(cmd *cobra.Command, c *pigtail.Client, arg string, members []pigtail.FocusMember, remove bool) error {
	if len(members) == 0 {
		return fmt.Errorf("give at least one --theme or --subject")
	}
	cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := resolveFocus(cx, c, arg)
	if err != nil {
		return err
	}
	var f *pigtail.Focus
	for _, m := range members {
		if remove {
			f, err = c.RemoveFocusMember(cx, id, m.Kind, m.ID)
		} else {
			f, err = c.AddFocusMember(cx, id, m)
		}
		if err != nil {
			return err
		}
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "✓ updated %s  %s\n", f.ID, f.Name)
	fmt.Fprintf(w, "  members: %s\n", focusMemberIDs(f))
	return nil
}

func focusAddCmd() *cobra.Command {
	var (
		themes, subjects []string
		crossScope       bool
	)
	cmd := &cobra.Command{
		Use:         "add <focus>",
		Annotations: scopedWrite(),
		Short:       "Add themes or subjects to an open focus (id or name)",
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope { // the master writing another scope's focus
				c = c.WithCrossScopeWrite()
			}
			return focusMembersWrite(cmd, c, args[0], focusMembers(themes, subjects), false)
		},
	}
	addFocusMemberFlags(cmd, &themes, &subjects)
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func focusRmCmd() *cobra.Command {
	var (
		themes, subjects []string
		crossScope       bool
	)
	cmd := &cobra.Command{
		Use:         "rm <focus>",
		Annotations: scopedWrite(),
		Short:       "Remove themes or subjects from an open focus (id or name)",
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			return focusMembersWrite(cmd, c, args[0], focusMembers(themes, subjects), true)
		},
	}
	addFocusMemberFlags(cmd, &themes, &subjects)
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func focusEndCmd() *cobra.Command {
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "end <focus>",
		Annotations: scopedWrite(),
		Short:       "End an open focus now (id or name): its sessions go back to their space",
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			id, err := resolveFocus(cx, c, args[0])
			if err != nil {
				return err
			}
			f, err := c.EndFocus(cx, id)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ ended %s  %s (by hand)\n", f.ID, f.Name)
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}
