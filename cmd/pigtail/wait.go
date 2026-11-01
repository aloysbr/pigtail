package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail wait` reads the waits of the Claude sessions (sub_x000000000000026):
// what each one waits for from the user, with its typed content. GET /waits and
// GET /waits/{id}, read for the caller's scope (newScopedClient).

// waitLong is the help of the `pigtail wait` group.
const waitLong = `Lists, shows, answers and snoozes what the Claude sessions wait for from you: the waits they
declared (pigtail session wait, with --details for a typed content) and the
dialogs Claude Code opened (a permission, a question), marked "(dialog)" —
those are answered in their terminal only. Answering and snoozing run from your
terminal, never from an agent session. A wait bound to a subject outlives its
session ("(ended)"); its answer goes to the subject's live session, or is kept
for the next one.

Outside a Claude cell (a plain terminal) pigtail reads the default scope only;
the full view is ` + "`--scope all`" + ` or ` + "`PIGTAIL_SCOPE=all`" + `, and ` + "`--cross-scope`" + ` writes outside
that scope.`

func waitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "List, show and answer what the Claude sessions wait for from you",
		Long:  waitLong,
	}
	cmd.AddCommand(waitLsCmd(), waitShowCmd(), waitAnswerCmd(), waitSnoozeCmd())
	return cmd
}

func waitLsCmd() *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:         "ls",
		Annotations: scopedRead(),
		Short:       "List the open waits, oldest first",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return runWaitLs(ctx, cmd.OutOrStdout(), c, all, asJSON, time.Now())
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include the snoozed waits")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the waits as a JSON array ([] when none)")
	return cmd
}

func waitShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "show <id>",
		Annotations: scopedRead(),
		Short:       "Show one wait, open or closed: its content and its answer",
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return runWaitShow(ctx, cmd.OutOrStdout(), c, args[0], asJSON, time.Now())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the wait as JSON")
	return cmd
}

func waitAnswerCmd() *cobra.Command {
	var (
		req        pigtail.WaitAnswerRequest
		crossScope bool
	)
	cmd := &cobra.Command{
		Use:         "answer <id>",
		Annotations: scopedWrite(),
		Short:       "Answer an open wait: the session reads it as your answer, and wakes",
		Long: `Answers an open wait with one form, as its type takes it:

  arbitrage           --choice <id> (or --choice reco), or --text
  question            --choice <id> when it has options, or --text
  review with items   --item n=ok|ko[:note] for each item (a ko needs a note)
  review, blocked     --seen, or --text

--reason adds a note to any answer. The answer reaches the session as yours,
wakes it and ends its Standby. Run it from your terminal: pigtaild refuses it
from an agent session. A Claude Code dialog (permission, question) is
answered in its terminal only.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("CLAUDE_CODE_SESSION_ID") != "" {
				return errFromSession("answer")
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return runWaitAnswer(ctx, cmd.OutOrStdout(), os.Getenv, c, args[0], req)
		},
	}
	cmd.Flags().StringVar(&req.Choice, "choice", "", "the option id, or reco for the recommended one (arbitrage, question)")
	cmd.Flags().StringVar(&req.Text, "text", "", "a free answer")
	cmd.Flags().StringArrayVar(&req.Items, "item", nil, "n=ok|ko[:note] for review item n, once per item")
	cmd.Flags().BoolVar(&req.Seen, "seen", false, "seen / done (a review without items, a blocked wait)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "a note added to the answer")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func waitSnoozeCmd() *cobra.Command {
	var (
		until      string
		crossScope bool
	)
	cmd := &cobra.Command{
		Use:         "snooze <id> --until <HH:MM | RFC 3339 | duration>",
		Annotations: scopedWrite(),
		Short:       "Hide an open wait until a time; the session is not touched",
		Long: `Hides an open wait from "wait ls" until a time; "wait ls --all" still lists
it. --until takes a clock time (HH:MM, local; a time already past today means
tomorrow), an RFC 3339 instant, or a duration (30m, 2h). The session is not
told and stays as it is; the wait comes back by itself when the time passes.
Run it from your terminal: pigtaild refuses it from an agent session.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("CLAUDE_CODE_SESSION_ID") != "" {
				return errFromSession("snooze")
			}
			at, err := parseSnoozeUntil(until, time.Now())
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
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			w, err := c.SnoozeWait(ctx, args[0], at)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "snoozed %s until %s\n", w.ID, at.Local().Format(time.DateTime))
			return nil
		},
	}
	cmd.Flags().StringVar(&until, "until", "", "when the wait comes back: HH:MM, an RFC 3339 instant, or a duration (30m, 2h)")
	_ = cmd.MarkFlagRequired("until")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// parseSnoozeUntil reads --until: HH:MM in now's zone (already past → the
// next day), an RFC 3339 instant, or a positive duration. The result is
// always after now.
func parseSnoozeUntil(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("--until is required: HH:MM, an RFC 3339 instant or a duration (30m, 2h)")
	}
	if t, err := time.Parse("15:04", s); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("--until %s is in the past", s)
		}
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--until %s must be positive", s)
		}
		return now.Add(d), nil
	}
	return time.Time{}, fmt.Errorf("--until %q: want HH:MM, an RFC 3339 instant or a duration (30m, 2h)", s)
}

// errFromSession: an agent session never answers nor snoozes a wait for the user
// (D3; a guard against an agent's mistake, the daemon refuses it too).
func errFromSession(sub string) error {
	return fmt.Errorf("pigtail wait %s runs from your terminal, not from an agent session", sub)
}

func runWaitAnswer(ctx context.Context, out io.Writer, getenv func(string) string, c *pigtail.Client, id string, req pigtail.WaitAnswerRequest) error {
	if getenv("CLAUDE_CODE_SESSION_ID") != "" {
		return errFromSession("answer")
	}
	res, err := c.AnswerWait(ctx, id, req)
	if err != nil {
		return err
	}
	if res.Delivery == pigtail.DeliveryKept {
		// D29-C: nobody runs on the subject; its next session gets it.
		fmt.Fprintf(out, "answered %s — %s; it goes to the next session of the subject\n", res.Wait.ID, pigtail.DeliveryKept)
		return nil
	}
	label := firstNonEmptyStr(res.DeliveredToLabel, res.DeliveredTo, res.Wait.SessionLabel, res.Wait.SessionID)
	extra := ""
	if res.StandbyEnded {
		extra = ", standby ended"
	}
	fmt.Fprintf(out, "answered %s → %s (delivery: %s%s)\n", res.Wait.ID, label, res.Delivery, extra)
	return nil
}

func runWaitLs(ctx context.Context, out io.Writer, c *pigtail.Client, all, asJSON bool, now time.Time) error {
	ws, err := c.ListWaits(ctx, all)
	if err != nil {
		return err
	}
	if ws == nil {
		ws = []pigtail.Wait{}
	}
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].OpenedAt.Before(ws[j].OpenedAt) })
	if asJSON {
		return printJSON(out, ws)
	}
	if len(ws) == 0 {
		fmt.Fprintln(out, "no open wait")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tAGE\tTYPE\tSESSION\tSUBJECT\tREASON")
	for _, w := range ws {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", w.ID, waitAge(now, w.OpenedAt), waitTypeLabel(w), waitSession(w),
			orDash(waitSubject(w)), orDash(w.Reason))
	}
	return tw.Flush()
}

func runWaitShow(ctx context.Context, out io.Writer, c *pigtail.Client, id string, asJSON bool, now time.Time) error {
	w, err := c.GetWait(ctx, id)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(out, w)
	}
	fmt.Fprintf(out, "%s  %s  %s\n", w.ID, waitTypeLabel(*w), w.State)
	fmt.Fprintf(out, "session: %s\n", waitSession(*w))
	if s := waitSubject(*w); s != "" {
		fmt.Fprintf(out, "subject: %s\n", s)
	}
	if w.Reason != "" {
		fmt.Fprintf(out, "reason:  %s\n", w.Reason)
	}
	fmt.Fprintf(out, "opened:  %s (%s ago)\n", w.OpenedAt.Local().Format(time.DateTime), waitAge(now, w.OpenedAt))
	if w.SnoozedUntil != nil && w.SnoozedUntil.After(now) {
		fmt.Fprintf(out, "snoozed: until %s\n", w.SnoozedUntil.Local().Format(time.DateTime))
	}
	if w.ClosedAt != nil {
		fmt.Fprintf(out, "closed:  %s (%s)\n", w.ClosedAt.Local().Format(time.DateTime), w.State)
	}
	if d := w.Details; d != nil {
		printWaitDetails(out, d)
	}
	if a := w.Answer; a != nil {
		printWaitAnswer(out, a)
		switch {
		case w.DeliveredTo != "":
			fmt.Fprintf(out, "delivered to: %s\n", firstNonEmptyStr(w.DeliveredToLabel, w.DeliveredTo))
		case w.SubjectID != "" && w.State == pigtail.WaitStateAnswered:
			fmt.Fprintf(out, "answer %s\n", pigtail.DeliveryKept)
		}
	}
	if w.Source == pigtail.WaitSourceAuto {
		fmt.Fprintln(out, "\nA Claude Code dialog: answer it in the terminal.")
	}
	return nil
}

// printWaitDetails prints the typed content: what each type carries.
func printWaitDetails(out io.Writer, d *pigtail.WaitDetails) {
	fmt.Fprintln(out)
	if d.Title != "" {
		fmt.Fprintln(out, d.Title)
	}
	if d.Summary != "" {
		fmt.Fprintln(out, d.Summary)
	}
	if d.Step != "" {
		fmt.Fprintf(out, "step: %s\n", d.Step)
	}
	if d.URL != "" {
		fmt.Fprintf(out, "url:  %s\n", d.URL)
	}
	printShots(out, "", d.Shots)
	if len(d.Options) > 0 {
		legend := "options:"
		for _, o := range d.Options {
			if o.Reco {
				legend = "options (* = reco):"
				break
			}
		}
		fmt.Fprintln(out, legend)
		for _, o := range d.Options {
			mark := " "
			if o.Reco {
				mark = "*"
			}
			fmt.Fprintf(out, "  %s %s  %s\n", mark, o.ID, o.Label)
			if o.Detail != "" {
				fmt.Fprintf(out, "      %s\n", o.Detail)
			}
			if o.URL != "" {
				fmt.Fprintf(out, "      %s\n", o.URL)
			}
		}
	}
	if d.Why != "" {
		fmt.Fprintf(out, "why: %s\n", d.Why)
	}
	if d.Other != "" {
		fmt.Fprintf(out, "other: %s\n", d.Other)
	}
	if d.Lead != "" {
		fmt.Fprintf(out, "lead: %s\n", d.Lead)
	}
	if len(d.Items) > 0 {
		fmt.Fprintln(out, "items:")
		for i, it := range d.Items {
			fmt.Fprintf(out, "  %d. %s\n", i+1, it.Label)
			if it.Detail != "" {
				fmt.Fprintf(out, "     %s\n", it.Detail)
			}
			if it.URL != "" {
				fmt.Fprintf(out, "     %s\n", it.URL)
			}
			printShots(out, "     ", it.Shots)
		}
	}
	if d.Cmd != "" {
		fmt.Fprintf(out, "cmd:   %s\n", d.Cmd)
	}
	if d.Where != "" {
		fmt.Fprintf(out, "where: %s\n", d.Where)
	}
}

func printShots(out io.Writer, indent string, shots []pigtail.WaitShot) {
	for _, s := range shots {
		if s.Caption != "" {
			fmt.Fprintf(out, "%sshot: %s — %s\n", indent, s.Src, s.Caption)
			continue
		}
		fmt.Fprintf(out, "%sshot: %s\n", indent, s.Src)
	}
}

func printWaitAnswer(out io.Writer, a *pigtail.WaitAnswer) {
	fmt.Fprintf(out, "\nanswer (%s):\n", a.At.Local().Format(time.DateTime))
	if a.Choice != "" {
		fmt.Fprintf(out, "  choice: %s\n", a.Choice)
	}
	if a.Seen {
		fmt.Fprintln(out, "  seen")
	}
	for _, v := range a.Items {
		verdict := "ko"
		if v.OK {
			verdict = "ok"
		}
		if v.Note != "" {
			fmt.Fprintf(out, "  %d. %s — %s\n", v.N, verdict, v.Note)
			continue
		}
		fmt.Fprintf(out, "  %d. %s\n", v.N, verdict)
	}
	if a.Text != "" {
		fmt.Fprintf(out, "  %s\n", a.Text)
	}
	if a.Reason != "" {
		fmt.Fprintf(out, "  reason: %s\n", a.Reason)
	}
}

// waitTypeLabel is the wait's type, "(dialog)" added for a Claude Code
// dialog (source auto): it is answered in its terminal only.
func waitTypeLabel(w pigtail.Wait) string {
	if w.Source == pigtail.WaitSourceAuto {
		return w.Type + " (dialog)"
	}
	return w.Type
}

// waitSession is the session's label as the daemon gave it (D23 applied),
// else its id; "(ended)" added when the session is gone (D29-A: its
// subject's wait outlives it).
func waitSession(w pigtail.Wait) string {
	label := firstNonEmptyStr(w.SessionLabel, w.SessionID)
	if w.SessionEnded {
		label += " (ended)"
	}
	return label
}

// waitSubject is the subject's short title, else its id ("" when none or
// not readable from this scope).
func waitSubject(w pigtail.Wait) string {
	if w.SubjectShortTitle != "" {
		return w.SubjectShortTitle
	}
	return w.SubjectID
}

func waitAge(now, at time.Time) string {
	return alertAge(int(now.Sub(at) / time.Second))
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func printJSON(out io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

// readDetails reads --details: a file, or stdin for "-". The daemon validates
// the content; an unreadable source is an error naming it, before any call.
func readDetails(src string, stdin io.Reader) ([]byte, error) {
	if src == "-" {
		if stdin == nil {
			stdin = os.Stdin
		}
		b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			return nil, fmt.Errorf("--details -: read stdin: %w", err)
		}
		return b, nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("--details: %w", err) // the *PathError names the file
	}
	return b, nil
}
