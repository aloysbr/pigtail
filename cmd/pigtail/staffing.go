package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail staffing` (sub_x000000000000004): the staffing scheduler's state,
// its decision log, and its two settings (mode, ceiling). The settings are
// global: only the master cell changes them, with --cross-scope, and launch
// is turned on from a terminal outside any Claude session only.

const (
	staffingQueueShown = 5  // ready-queue lines shown per scope
	staffingTitleMax   = 60 // a title longer than this is cut, with …
	staffingTimeout    = 30 * time.Second
)

func staffingCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "staffing",
		Short:       "Show the staffing scheduler: mode, places, ready queue, last decision",
		Args:        cobra.NoArgs,
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), staffingTimeout)
			defer cancel()
			st, err := c.Staffing(ctx)
			if err != nil {
				return fmt.Errorf("staffing: %w", err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), st)
			}
			printStaffingState(cmd.OutOrStdout(), st)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the state as JSON")
	cmd.AddCommand(staffingModeCmd(), staffingCeilingCmd(), staffingLogCmd())
	return cmd
}

// launchOnRefusal is the CLI's refusal of mode launch from a Claude session:
// the exact command to type in a bare terminal.
func launchOnRefusal() error {
	return errors.New("staffing mode launch: only " + humanName() + " turns launch on — run it from a terminal outside any Claude session: pigtail --scope " +
		string(scope.Master()) + " staffing mode launch --cross-scope")
}

func staffingModeCmd() *cobra.Command {
	var crossScope, asJSON bool
	cmd := &cobra.Command{
		Use:         "mode <off|advise|launch>",
		Short:       "Set the staffing mode (master cell, --cross-scope; launch only from a terminal outside Claude)",
		Args:        cobra.ExactArgs(1),
		ValidArgs:   []string{string(pigtail.StaffingOff), string(pigtail.StaffingAdvise), string(pigtail.StaffingLaunch)},
		Annotations: scopedWrite(),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := pigtail.StaffingMode(args[0])
			switch mode {
			case pigtail.StaffingOff, pigtail.StaffingAdvise, pigtail.StaffingLaunch:
			default:
				return fmt.Errorf("staffing mode: %q: want off, advise or launch", args[0])
			}
			if mode == pigtail.StaffingLaunch {
				// Fail closed: a process tree that cannot be read counts as
				// a Claude session.
				if _, found, err := spawnCallerClaude(); found || err != nil {
					return launchOnRefusal()
				}
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			ctx, cancel := context.WithTimeout(context.Background(), staffingTimeout)
			defer cancel()
			st, err := c.SetStaffing(ctx, pigtail.StaffingSettings{Mode: mode})
			if err != nil {
				return fmt.Errorf("staffing mode: %w", err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), st)
			}
			printStaffingState(cmd.OutOrStdout(), st)
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the new state as JSON")
	return cmd
}

func staffingCeilingCmd() *cobra.Command {
	var crossScope, asJSON bool
	cmd := &cobra.Command{
		Use:         "ceiling <n>",
		Short:       "Set how many places the scheduler may fill (master cell, --cross-scope)",
		Args:        cobra.ExactArgs(1),
		Annotations: scopedWrite(),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("staffing ceiling: %q: want an integer", args[0])
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			ctx, cancel := context.WithTimeout(context.Background(), staffingTimeout)
			defer cancel()
			st, err := c.SetStaffing(ctx, pigtail.StaffingSettings{Ceiling: &n})
			if err != nil {
				return fmt.Errorf("staffing ceiling: %w", err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), st)
			}
			printStaffingState(cmd.OutOrStdout(), st)
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the new state as JSON")
	return cmd
}

func staffingLogCmd() *cobra.Command {
	var (
		asJSON bool
		since  time.Duration
		limit  int
	)
	cmd := &cobra.Command{
		Use:         "log",
		Short:       "Show the staffing scheduler's decisions",
		Args:        cobra.NoArgs,
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if since <= 0 || limit < 1 {
				return errors.New("staffing log: --since and --limit must be positive")
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), staffingTimeout)
			defer cancel()
			ds, err := c.StaffingDecisions(ctx, time.Now().Add(-since), limit)
			if err != nil {
				return fmt.Errorf("staffing log: %w", err)
			}
			if asJSON {
				if ds == nil {
					ds = []pigtail.StaffingDecision{}
				}
				return printJSON(cmd.OutOrStdout(), ds)
			}
			printStaffingLog(cmd.OutOrStdout(), ds)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the decisions as JSON")
	cmd.Flags().DurationVar(&since, "since", 24*time.Hour, "how far back to read")
	cmd.Flags().IntVar(&limit, "limit", 50, "at most this many decisions")
	return cmd
}

// printStaffingState prints the state as plain text, no colour.
func printStaffingState(w io.Writer, st pigtail.StaffingState) {
	if st.Mode == pigtail.StaffingOff {
		fmt.Fprintln(w, "staffing: off — nothing is staffed (pigtail staffing mode advise|launch)")
		if st.SettingsError != "" {
			fmt.Fprintln(w, "settings error: "+st.SettingsError)
		}
		return
	}
	fmt.Fprintf(w, "staffing: %s · %d/%d places · launcher: %s\n", st.Mode, st.PlacesUsed, st.Ceiling, st.Launcher)
	if st.SettingsError != "" {
		fmt.Fprintln(w, "settings error: "+st.SettingsError)
	}
	if len(st.Places) == 0 {
		fmt.Fprintln(w, "places: (none)")
	} else {
		fmt.Fprintln(w, "places:")
		for _, p := range st.Places {
			fmt.Fprintln(w, "  "+staffingPlaceLine(p))
		}
	}
	if st.PlacesUsed >= st.Ceiling {
		fmt.Fprintf(w, "no free place (%d/%d)\n", st.PlacesUsed, st.Ceiling)
	}
	if len(st.Queues) == 0 {
		fmt.Fprintln(w, "(no ready subject)")
	}
	scopes := make([]string, 0, len(st.Queues))
	for k := range st.Queues {
		scopes = append(scopes, k)
	}
	sort.Strings(scopes)
	for _, k := range scopes {
		fmt.Fprintf(w, "ready queue — %s:\n", k)
		q := st.Queues[k]
		if len(q) == 0 {
			fmt.Fprintln(w, "  (no ready subject)")
		}
		for i, c := range q {
			if i == staffingQueueShown {
				fmt.Fprintf(w, "  … %d more\n", len(q)-i)
				break
			}
			fmt.Fprintf(w, "  %d. %s %s — %s\n", c.Rank, c.SubjectID, staffingTitle(c.ShortTitle), staffingSkipText(c))
		}
	}
	if st.Last != nil {
		fmt.Fprintf(w, "last decision (%s): %s\n", st.Last.TS.Local().Format("15:04"), staffingSummary(*st.Last))
	}
}

func staffingPlaceLine(p pigtail.StaffingPlace) string {
	s := p.SubjectID + " " + staffingTitle(p.ShortTitle)
	if p.Label != "" {
		s = p.Label + ": " + s
	}
	switch {
	case p.Pending:
		s += " · launch pending"
	case p.Wait != "":
		s += " · waits for " + humanName() + " (" + p.Wait + ")"
	default:
		s += " · working"
	}
	if p.Phantom {
		s += " · phantom"
	}
	return s
}

func staffingTitle(t string) string {
	r := []rune(t)
	if len(r) <= staffingTitleMax {
		return t
	}
	return string(r[:staffingTitleMax-1]) + "…"
}

// staffingSkipText is why a candidate is not taken, readable.
func staffingSkipText(c pigtail.StaffingCandidate) string {
	switch c.Skip {
	case "":
		return "ready"
	case pigtail.StaffingSkipHeld:
		return "held by a live session"
	case pigtail.StaffingSkipPhantom:
		return "phantom session"
	case pigtail.StaffingSkipPaused:
		return "session paused"
	case pigtail.StaffingSkipPending:
		return "launch pending"
	case pigtail.StaffingSkipWaitsHuman:
		return "waits for " + humanName() + " (" + c.Detail + ")"
	case pigtail.StaffingSkipDeps:
		return "blocked by " + c.Detail
	case pigtail.StaffingSkipCooldown:
		return "retry after " + c.Detail
	case pigtail.StaffingSkipOverMargin:
		return "accounts over margin"
	case pigtail.StaffingSkipOverBudget:
		switch {
		case c.Detail == "": // masked for a non-master caller: the cause is not known here
			return "accounts over budget or not measured"
		case strings.HasPrefix(c.Detail, "unmeasured: "):
			return "accounts not measured"
		}
		return "accounts over budget"
	}
	if c.Detail != "" {
		return c.Skip + " (" + c.Detail + ")"
	}
	return c.Skip
}

func staffingOutcomeText(d pigtail.StaffingDecision) string {
	s := strings.ReplaceAll(d.Outcome, "_", " ")
	if d.Outcome == pigtail.StaffingOutcomeObservedLaunch && d.Origin != "" {
		s += " (" + d.Origin + ")"
	}
	return s
}

// staffingSummary is one decision in a few words: outcome, subject, gate.
func staffingSummary(d pigtail.StaffingDecision) string {
	s := staffingOutcomeText(d)
	if d.SubjectID != "" {
		s += " " + d.SubjectID
	}
	if d.Gate != "" {
		s += " - " + d.Gate
	}
	return s
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printStaffingLog prints one line per decision:
// time outcome subject gate reason (×n until hh:mm).
func printStaffingLog(w io.Writer, ds []pigtail.StaffingDecision) {
	if len(ds) == 0 {
		fmt.Fprintln(w, "(no decision)")
		return
	}
	for _, d := range ds {
		line := d.TS.Local().Format("15:04:05") + " " + staffingOutcomeText(d) + " " +
			dashIfEmpty(d.SubjectID) + " " + dashIfEmpty(d.Gate)
		if d.Reason != "" {
			line += " " + d.Reason
		}
		if d.Repeats > 0 {
			line += fmt.Sprintf(" (×%d until %s)", d.Repeats+1, d.LastAt.Local().Format("15:04"))
		}
		fmt.Fprintln(w, line)
	}
}
