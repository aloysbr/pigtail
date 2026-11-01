package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// captureCmd is `pigtail capture`: a capture (backlog sense) is a lightweight
// idea filed under a project (theme), not yet a subject — never a screenshot.
func captureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture an idea under a project (theme) — a backlog item, not a screenshot",
		Long: "A capture is a lightweight idea filed under a project (a pigtail theme), not yet a\n" +
			"subject: it costs no worktree and no phase. Promote one or several captures into a\n" +
			"subject when the work starts. A capture is NOT a screenshot.",
	}
	cmd.AddCommand(captureAddCmd(), captureListCmd(), capturePromoteCmd(), captureDeleteCmd())
	return cmd
}

func captureAddCmd() *cobra.Command {
	var theme, text, from string
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         `add --theme <thm> --text "<text>" [--from-subject <sub>]`,
		Annotations: scopedWrite(),
		Short:       "Capture an idea under a project (--from-subject: the subject it replaces becomes captured)",
		Args:        cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if theme == "" || text == "" {
				return fmt.Errorf("--theme and --text are required")
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
			cp, err := c.AddCapture(cx, theme, text, from)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(cp)
			}
			fmt.Printf("✓ captured %s  %s  %s\n", cp.ID, cp.ThemeID, cp.Text)
			return nil
		},
	}
	cmd.Flags().StringVar(&theme, "theme", "", "project (theme) id, thm_…")
	cmd.Flags().StringVar(&text, "text", "", "the idea, one line")
	cmd.Flags().StringVar(&from, "from-subject", "", "a subject of the same project this capture replaces (it becomes captured)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func captureListCmd() *cobra.Command {
	var theme string
	var open, asJSON bool
	cmd := &cobra.Command{
		Use:         "list [--theme <thm>] [--open]",
		Annotations: scopedRead(),
		Short:       "List captures, newest first (--open: not promoted yet)",
		Args:        cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			caps, err := c.Captures(cx, pigtail.CaptureQuery{Theme: theme, Open: open, Scope: scopeOverride()})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(caps)
			}
			for _, cp := range caps {
				state := "open"
				if cp.PromotedSubjectID != "" {
					state = "→ " + cp.PromotedSubjectID
				}
				fmt.Printf("%s  [%s]  %s  %s\n", cp.ID, state, cp.ThemeID, cp.Text)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&theme, "theme", "", "filter by project (theme) id")
	cmd.Flags().BoolVar(&open, "open", false, "only captures not promoted yet")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func capturePromoteCmd() *cobra.Command {
	var title, shortTitle string
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         `promote <cap_id>... --title "<title>" [--short-title "<label>"]`,
		Annotations: scopedWrite(),
		Short:       "Promote open captures of one project into a subject (status=inbox)",
		Args:        cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
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
			res, err := c.PromoteCaptures(cx, args, title, shortTitle)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			fmt.Printf("✓ promoted %d capture(s) into %s  [%s]  %s\n", len(res.Captures), res.Subject.ID, res.Subject.Status, res.Subject.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "subject title")
	cmd.Flags().StringVar(&shortTitle, "short-title", "", "sidebar label (default: derived from --title)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func captureDeleteCmd() *cobra.Command {
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         "delete <cap_id>",
		Annotations: scopedWrite(),
		Short:       "Delete a capture (undo); its subject gets its previous status back if still captured",
		Args:        cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res, err := c.DeleteCapture(cx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			fmt.Printf("✓ deleted %s\n", res.Capture.ID)
			if res.Subject != nil {
				fmt.Printf("  %s restored → %s\n", res.Subject.ID, res.Subject.Status)
			}
			if res.Note != "" {
				fmt.Printf("  %s\n", res.Note)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}
