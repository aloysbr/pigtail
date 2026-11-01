package main

import (
	"bufio"
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

// The system journal (docs/wire-protocol.md § Journal): one pointer line per
// event, written by deterministic producers, read across subjects.

func journalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "System journal: one pointer line per event (never the content)",
	}
	cmd.AddCommand(journalAddCmd(), journalListCmd(), journalImportCmd())
	return cmd
}

// parseJournalTS reads --ts: empty (the daemon's now), RFC 3339, or unix ms.
func parseJournalTS(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil && len(v) >= 10 {
		return time.UnixMilli(ms).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("--ts %q: want RFC 3339 or unix millis", v)
	}
	return t, nil
}

func journalAddCmd() *cobra.Command {
	var e pigtail.JournalEntry
	var ts string
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         "add",
		Annotations: scopedWrite(),
		Short:       "Add one journal row (idempotent: an id already present is a no-op)",
		Long: `Add one row to the system journal. The id is the producer's deterministic
id: re-adding it in the same scope changes nothing and still exits 0.

The row's scope is the subject's (--subject), else --entry-scope, else this
session's scope. A master caller writing another scope's row needs --cross-scope.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			when, err := parseJournalTS(ts)
			if err != nil {
				return err
			}
			e.Timestamp = when
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res, err := c.AddJournal(cx, e)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			if res.Created {
				fmt.Printf("✓ journal ← %s (%s, %s)\n", res.Entry.ID, res.Entry.Kind, res.Entry.Scope)
			} else {
				fmt.Printf("= journal: %s already present (%s)\n", res.Entry.ID, res.Entry.Scope)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&e.ID, "id", "", "the producer's deterministic id (required)")
	f.StringVar(&e.Kind, "kind", "", "event kind, lowercase token: session-end, gate-fired, meeting, delivery… (required)")
	f.StringVar(&e.Summary, "summary", "", "one line, at most 280 characters (required)")
	f.StringVar(&e.Source, "source", "", "link to the content: https:, vault:, file:, pigtail: (required)")
	f.StringVar(&e.SubjectID, "subject", "", "pigtail subject id (sets the row's scope)")
	f.StringVar(&e.Scope, "entry-scope", "", "a scope name or alias of the scope table, when there is no subject (default: this session's scope)")
	f.StringVar(&ts, "ts", "", "event time, RFC 3339 or unix millis (default: now)")
	f.BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	for _, name := range []string{"id", "kind", "summary", "source"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func journalListCmd() *cobra.Command {
	var since, until, kind, subject string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: scopedRead(),
		Short:       "Read the journal across subjects, newest first",
		Long: `Read the journal across subjects, newest first. --since/--until take a
duration back from now (24h, 7d), a date (2026-10-03, local midnight) or unix
millis; --since is inclusive, --until exclusive. The persistent --scope is the
view (a non-master session only ever sees its own scope's rows).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := pigtail.JournalQuery{Scope: scopeOverride(), Kind: kind, SubjectID: subject, Limit: limit}
			now := time.Now()
			var err error
			if since != "" {
				if q.Since, err = parseSince(since, now); err != nil {
					return err
				}
			}
			if until != "" {
				if q.Until, err = parseSince(until, now); err != nil {
					return fmt.Errorf("--until: %w", err)
				}
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rows, err := c.Journal(cx, q)
			if err != nil {
				return err
			}
			if asJSON {
				if rows == nil {
					rows = []pigtail.JournalEntry{}
				}
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			for _, e := range rows {
				subj := e.SubjectID
				if subj == "" {
					subj = "-"
				}
				fmt.Printf("%s  %s  %s  %s  %s  → %s\n", e.Timestamp.Local().Format("2006-01-02 15:04"),
					e.Scope, e.Kind, subj, e.Summary, e.Source)
			}
			effective := limit
			if effective <= 0 {
				effective = pigtail.JournalListDefault
			}
			if len(rows) == effective {
				fmt.Fprintf(os.Stderr, "(limit %d reached — narrow with --since/--until or raise --limit, max %d)\n",
					effective, pigtail.JournalListMax)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "rows at or after: 24h, 7d, 2026-10-03 or unix millis")
	cmd.Flags().StringVar(&until, "until", "", "rows strictly before: same forms as --since")
	cmd.Flags().StringVar(&kind, "kind", "", "only this kind")
	cmd.Flags().StringVar(&subject, "subject", "", "only this subject")
	cmd.Flags().IntVar(&limit, "limit", 0, fmt.Sprintf("at most N rows (default %d, max %d)", pigtail.JournalListDefault, pigtail.JournalListMax))
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON (always an array)")
	return cmd
}

func journalImportCmd() *cobra.Command {
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "import",
		Annotations: scopedWrite(),
		Short:       "Add journal rows from NDJSON on stdin (one JournalEntry per line)",
		Long: `Read one JSON journal entry per line on stdin and add each, as journal add
would. Blank lines are skipped. Prints one stderr line per refused row and a
final count; exits 1 when any row was refused. Idempotent: re-importing the
same lines creates nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			var created, present, refused int
			sc := bufio.NewScanner(cmd.InOrStdin())
			sc.Buffer(make([]byte, 64*1024), 1<<20)
			for n := 1; sc.Scan(); n++ {
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				var e pigtail.JournalEntry
				if err := json.Unmarshal([]byte(line), &e); err != nil {
					refused++
					fmt.Fprintf(os.Stderr, "journal import: line %d: invalid JSON: %v\n", n, err)
					continue
				}
				cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				res, err := c.AddJournal(cx, e)
				cancel()
				switch {
				case err != nil:
					refused++
					fmt.Fprintf(os.Stderr, "journal import: line %d (%s): %v\n", n, e.ID, err)
				case res.Created:
					created++
				default:
					present++
				}
			}
			if err := sc.Err(); err != nil {
				return fmt.Errorf("journal import: reading stdin: %w", err)
			}
			fmt.Printf("journal import: %d created, %d already present, %d refused\n", created, present, refused)
			if refused > 0 {
				return fmt.Errorf("%d row(s) refused", refused)
			}
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}
