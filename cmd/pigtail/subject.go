package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func subjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subject",
		Short: "Manage subjects (durable, prioritised units of work)",
	}
	cmd.AddCommand(subjectCreateCmd(), subjectListCmd(), subjectShowCmd(), subjectNextCmd(),
		subjectLogCmd(), subjectSetCmd(), subjectDepCmd(), subjectAttachCmd(), subjectDetachCmd(), subjectWhichCmd(), subjectOffTopicCmd(), subjectSuggestCmd(), subjectFindCmd(), subjectPickCmd(), subjectOutputCmd())
	return cmd
}

func subjectCreateCmd() *cobra.Command {
	var theme, title, shortTitle, vaultPath string
	var local, crossScope bool
	var source, shortID, boardID, section, boardCard string
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "create --theme <theme_id> --title <title> [--short-title <label>]",
		Annotations: scopedWrite(),
		Short:       "Create a subject (daemon mints id/slug, status=inbox)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}
			if theme == "" {
				return fmt.Errorf("--theme is required (resolve the subject's theme against your vault — e.g. <vault root>/Areas/<Name> for an area — and pass its id; create it first with 'pigtail theme create' if needed)")
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Source inference: explicit --source wins; else --local → local,
			// a --short-id (board card) → board, otherwise default to local.
			src := source
			if src == "" {
				switch {
				case local:
					src = pigtail.SourceLocal
				case shortID != "":
					src = pigtail.SourceBoard
				default:
					src = pigtail.SourceLocal
				}
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			sub, err := c.CreateSubject(cx, pigtail.Subject{
				ThemeID: theme, Title: title, ShortTitle: shortTitle, VaultPath: vaultPath,
				Source: src, ShortID: shortID, BoardID: boardID, Section: section,
				BoardCard: boardCard,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(sub)
			}
			fmt.Printf("✓ created %s  [%s]  %s  %s\n", sub.ID, sub.Status, sub.Scope, sub.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&theme, "theme", "", "theme id (thm_…)")
	cmd.Flags().StringVar(&title, "title", "", "subject title")
	cmd.Flags().StringVar(&shortTitle, "short-title", "",
		fmt.Sprintf("sidebar label, 2-4 words, at most %d characters (default: derived from --title)", pigtail.ShortTitleMax))
	cmd.Flags().StringVar(&vaultPath, "vault-path", "", "vault folder for the subject (optional)")
	cmd.Flags().BoolVar(&local, "local", false, "local subject (no board card)")
	cmd.Flags().StringVar(&source, "source", "", "local|board (default: local, or board if --short-id set)")
	cmd.Flags().StringVar(&shortID, "short-id", "", "board card shortId (ABC-…) when source=board")
	cmd.Flags().StringVar(&boardID, "board-id", "", "board UUID when source=board")
	cmd.Flags().StringVar(&section, "section", "", "board section id when source=board")
	cmd.Flags().StringVar(&boardCard, "board-card", "", "linked board card shortId (ABC-…), any source")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func subjectListCmd() *cobra.Command {
	var theme, status, source string
	var asJSON, all bool
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: scopedRead(),
		Short:       "List subjects ordered by scope, then rank (--scope <scope>|all narrows)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if status != "" {
				if err := pigtail.ValidateSubjectStatus(status); err != nil {
					return err
				}
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			subs, err := c.Subjects(cx, pigtail.SubjectQuery{Theme: theme, Status: status, Source: source, Scope: scopeOverride()})
			if err != nil {
				return err
			}
			// A captured subject was converted into a capture: hidden unless
			// asked for by --status or --all.
			if status == "" && !all {
				kept := subs[:0]
				for _, s := range subs {
					if s.Status != pigtail.StatusCaptured {
						kept = append(kept, s)
					}
				}
				subs = kept
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(subs)
			}
			for _, s := range subs {
				fmt.Printf("%s  [%s]  %s  %s  (impact=%d effort=%d urgency=%d)\n",
					s.ID, s.Status, s.Scope, s.Title, s.Impact, s.Effort, s.Urgency)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&theme, "theme", "", "filter by theme id")
	cmd.Flags().StringVar(&status, "status", "", "filter by status")
	cmd.Flags().StringVar(&source, "source", "", "filter local|board")
	cmd.Flags().BoolVar(&all, "all", false, "include captured subjects (hidden by default)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func subjectShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "show <subject_id>",
		Annotations: scopedRead(),
		Short:       "Show a subject with its phases",
		Args:        cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sub, err := c.Subject(cx, args[0])
			if err != nil {
				return err
			}
			phases, err := c.Phases(cx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				// display_title is the label actually shown: the
				// subject's own, or the one derived from its title.
				out := map[string]any{"phases": phases, "display_title": pigtail.ShortTitle(*sub)}
				// blocked_by sits at the top level; an older daemon
				// without it leaves the key out, never null.
				if sub.BlockedBy != nil {
					out["blocked_by"] = *sub.BlockedBy
					sub.BlockedBy = nil
				}
				// The transitive chains lift the same way; an older daemon
				// without them leaves the keys out, never null.
				if sub.BlockedByAll != nil {
					out["blocked_by_all"] = *sub.BlockedByAll
					sub.BlockedByAll = nil
				}
				if sub.Blocks != nil {
					out["blocks"] = *sub.Blocks
					sub.Blocks = nil
				}
				if sub.BlocksAll != nil {
					out["blocks_all"] = *sub.BlocksAll
					sub.BlocksAll = nil
				}
				out["subject"] = sub
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			fmt.Printf("%s  [%s]  %s\n", sub.ID, sub.Status, sub.Title)
			if sub.ShortTitle != "" {
				fmt.Printf("  short: %s\n", sub.ShortTitle)
			} else {
				fmt.Printf("  short: %s (derived)\n", pigtail.ShortTitle(*sub))
			}
			fmt.Printf("  scope=%s theme=%s rank=%d impact=%d effort=%d urgency=%d\n", sub.Scope, sub.ThemeID, sub.Rank, sub.Impact, sub.Effort, sub.Urgency)
			if len(sub.Deps) > 0 {
				fmt.Printf("  deps: %s\n", strings.Join(sub.Deps, ", "))
			}
			for _, line := range []struct {
				label string
				ids   *[]string
			}{{"blocked by (chain)", sub.BlockedByAll}, {"blocks", sub.Blocks}, {"blocks (chain)", sub.BlocksAll}} {
				if line.ids != nil && len(*line.ids) > 0 {
					fmt.Printf("  %s: %s\n", line.label, strings.Join(*line.ids, ", "))
				}
			}
			if sub.VaultPath != "" {
				fmt.Printf("  vault: %s\n", sub.VaultPath)
			}
			if sub.BoardCard != "" {
				fmt.Printf("  board: %s\n", sub.BoardCard)
			}
			for _, ws := range sub.CmuxWorkspaces {
				fmt.Printf("  cmux: %s\n", ws)
			}
			if sub.OutputsCount != nil && *sub.OutputsCount > 0 {
				last := ""
				if sub.LastOutputTS != nil {
					last = " (last " + time.UnixMilli(*sub.LastOutputTS).Local().Format("2006-01-02 15:04") + ")"
				}
				fmt.Printf("  outputs: %d%s\n", *sub.OutputsCount, last)
			}
			for _, p := range phases {
				fmt.Printf("  phase %s  %s/%s  ws=%s\n", p.ID, p.Kind, p.State, p.WorkspaceID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func subjectNextCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "next",
		Annotations: scopedRead(),
		Short:       "Show the next actionable subject of your scope (top rank, deps satisfied; --scope <scope> from the master cell)",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sub, err := c.NextSubject(cx, scopeOverride())
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(sub)
			}
			fmt.Printf("%s  [%s]  %s  %s\n", sub.ID, sub.Status, sub.Scope, sub.Title)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func subjectLogCmd() *cobra.Command {
	var kind, since string
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         "log <subject_id> [text...]",
		Annotations: scopedWrite(),
		Short:       "Append a log line to a subject, or read its log when no text is given",
		Long: `With text: append one line to the subject's log (--kind, default note).

Without text: read the subject's log, newest first — all of it, or only the
entries since --since (a duration like 24h or 7d, a date like 2026-09-22, or
unix millis). --kind then filters the entries read.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if len(args) == 1 {
				var kindFilter string
				if cmd.Flags().Changed("kind") {
					kindFilter = kind
				}
				return readSubjectLog(cx, c, args[0], since, kindFilter, asJSON)
			}
			if since != "" {
				return fmt.Errorf("--since reads the log: drop the text to read, or drop --since to append")
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			e, err := c.AppendLog(cx, args[0], pigtail.SubjectLogEntry{Kind: kind, Text: strings.Join(args[1:], " ")})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(e)
			}
			fmt.Printf("✓ logged → %s (%s)\n", args[0], kind)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", pigtail.LogNote, "log kind: idea|note|progress|status|phase|delivery|handoff (filters when reading)")
	cmd.Flags().StringVar(&since, "since", "", "read only entries since: 24h, 7d, 2026-09-22 or unix millis")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// readSubjectLog prints a subject's log, newest first, optionally since a
// point in time and of one kind. JSON output is always an array.
func readSubjectLog(ctx context.Context, c *pigtail.Client, subjectID, since, kind string, asJSON bool) error {
	var (
		entries []pigtail.SubjectLogEntry
		err     error
	)
	if since != "" {
		from, perr := parseSince(since, time.Now())
		if perr != nil {
			return perr
		}
		entries, err = c.LogSince(ctx, subjectID, from)
	} else {
		entries, err = c.Log(ctx, subjectID)
	}
	if err != nil {
		return err
	}
	out := make([]pigtail.SubjectLogEntry, 0, len(entries))
	for _, e := range entries {
		if kind == "" || e.Kind == kind {
			out = append(out, e)
		}
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	for _, e := range out {
		fmt.Printf("%s  %-8s  %s\n", e.Timestamp.Local().Format("2006-01-02 15:04"), e.Kind, e.Text)
	}
	return nil
}

// addCrossScopeFlag adds --cross-scope to a write command: a perso caller's
// explicit opt-in to write a work subject or theme (the daemon refuses it
// otherwise). It never widens a work caller.
func addCrossScopeFlag(cmd *cobra.Command, v *bool) {
	cmd.Flags().BoolVar(v, "cross-scope", false, "master caller: allow this write onto another scope's subject or theme")
}

func subjectSetCmd() *cobra.Command {
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "set <subject_id> key=value...",
		Annotations: scopedWrite(),
		Short:       "Set fields on a subject (status, impact, effort, urgency, rank, vault_path, board_card, title, short_title, theme)",
		Args:        cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sub, err := c.Subject(cx, args[0])
			if err != nil {
				return err
			}
			keys := make([]string, 0, len(args)-1)
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("expected key=value, got %q", kv)
				}
				if err := applySubjectField(sub, k, v); err != nil {
					return err
				}
				keys = append(keys, k)
			}
			fields, err := subjectPatch(*sub, keys)
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			out, err := c.PatchSubject(cx, sub.ID, fields)
			if err != nil {
				return err
			}
			fmt.Printf("✓ updated %s [%s]\n", out.ID, out.Status)
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// applySubjectField mutates sub for one key=value pair.
func applySubjectField(sub *pigtail.Subject, key, val string) error {
	switch key {
	case "status":
		if err := pigtail.ValidateSubjectStatus(val); err != nil {
			return err
		}
		sub.Status = val
	case "title":
		sub.Title = val
	case "short_title":
		// Label for narrow surfaces (cmux sidebar), max 32 characters,
		// validated by the daemon. "" = derive it from the title again.
		sub.ShortTitle = val
	case "vault_path":
		sub.VaultPath = val
	case "board_card":
		sub.BoardCard = val
	case "theme", "theme_id":
		// Reassign the subject to another theme. The daemon validates that the
		// target theme exists. DB link only — vault files are not moved.
		sub.ThemeID = val
	case "code_project_id":
		sub.CodeProjectID = val
	case "impact", "effort", "urgency", "rank":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("%s must be an integer: %w", key, err)
		}
		switch key {
		case "impact":
			sub.Impact = n
		case "effort":
			sub.Effort = n
		case "urgency":
			sub.Urgency = n
		case "rank":
			sub.Rank = n
		}
	default:
		return fmt.Errorf("unknown field %q", key)
	}
	return nil
}

func subjectDepCmd() *cobra.Command {
	var on string
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "dep <subject_id> --on <subject_id>",
		Annotations: scopedWrite(),
		Short:       "Declare that a subject depends on another (blocking)",
		Args:        cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if on == "" {
				return fmt.Errorf("--on <subject_id> is required")
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sub, err := c.Subject(cx, args[0])
			if err != nil {
				return err
			}
			for _, d := range sub.Deps {
				if d == on {
					fmt.Printf("already depends on %s\n", on)
					return nil
				}
			}
			sub.Deps = append(sub.Deps, on)
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			if _, err := c.PatchSubject(cx, sub.ID, map[string]any{"deps": sub.Deps}); err != nil {
				return err
			}
			fmt.Printf("✓ %s now depends on %s\n", args[0], on)
			return nil
		},
	}
	cmd.Flags().StringVar(&on, "on", "", "the subject id this one depends on")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}
