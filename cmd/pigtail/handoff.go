package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func handoffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Write or show a subject handoff (a log entry of kind=handoff)",
	}
	cmd.AddCommand(handoffWriteCmd(), handoffShowCmd())
	return cmd
}

func handoffWriteCmd() *cobra.Command {
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "write <subject_id> [text...]",
		Annotations: scopedWrite(),
		Short:       "Append a handoff to a subject (reads stdin when no text given)",
		Args:        cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			text := strings.Join(args[1:], " ")
			if text == "" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				text = strings.TrimSpace(string(b))
			}
			if text == "" {
				return fmt.Errorf("handoff text is empty (pass args or pipe via stdin)")
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			if _, err := c.AppendLog(cx, args[0], pigtail.SubjectLogEntry{Kind: pigtail.LogHandoff, Text: text}); err != nil {
				return err
			}
			fmt.Printf("✓ handoff written → %s\n", args[0])
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// parseHandoffAutoID reads the session and the turn out of the id of a
// handoff-auto line, "handoff-auto:<session>:<turn>".
func parseHandoffAutoID(id string) (session, turn string, ok bool) {
	rest, found := strings.CutPrefix(id, pigtail.LogHandoffAuto+":")
	if !found {
		return "", "", false
	}
	session, turn, found = strings.Cut(rest, ":")
	if !found || session == "" || turn == "" {
		return "", "", false
	}
	return session, turn, true
}

// handoffShowJSON is the shape of `handoff show --json`.
type handoffShowJSON struct {
	Handoff *struct {
		ID   string    `json:"id"`
		TS   time.Time `json:"ts"`
		Text string    `json:"text"`
	} `json:"handoff"`
	Auto *struct {
		ID      string    `json:"id"`
		TS      time.Time `json:"ts"`
		Session string    `json:"session"`
		Turn    string    `json:"turn"`
		Text    string    `json:"text"`
	} `json:"auto"`
}

// writeHandoffShow prints the latest manual handoff first, then the latest
// automatic one when it is newer (an older one is hidden: the manual wins).
// logs are newest-first.
func writeHandoffShow(w io.Writer, logs []pigtail.SubjectLogEntry, asJSON bool) error {
	var manual, auto *pigtail.SubjectLogEntry
	for i := range logs {
		switch {
		case manual == nil && logs[i].Kind == pigtail.LogHandoff:
			manual = &logs[i]
		case auto == nil && logs[i].Kind == pigtail.LogHandoffAuto:
			auto = &logs[i]
		}
	}
	if auto != nil && manual != nil && !auto.Timestamp.After(manual.Timestamp) {
		auto = nil
	}
	if asJSON {
		var out handoffShowJSON
		if manual != nil {
			out.Handoff = &struct {
				ID   string    `json:"id"`
				TS   time.Time `json:"ts"`
				Text string    `json:"text"`
			}{manual.ID, manual.Timestamp, manual.Text}
		}
		if auto != nil {
			session, turn, _ := parseHandoffAutoID(auto.ID)
			out.Auto = &struct {
				ID      string    `json:"id"`
				TS      time.Time `json:"ts"`
				Session string    `json:"session"`
				Turn    string    `json:"turn"`
				Text    string    `json:"text"`
			}{auto.ID, auto.Timestamp, session, turn, auto.Text}
		}
		return json.NewEncoder(w).Encode(out)
	}
	switch {
	case manual == nil && auto == nil:
		_, err := fmt.Fprintln(w, "no handoff yet")
		return err
	case manual != nil:
		if _, err := fmt.Fprintf(w, "# handoff %s — %s\n\n%s\n", manual.ID, manual.Timestamp.Format(time.RFC3339), manual.Text); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintln(w, "no manual handoff yet"); err != nil {
			return err
		}
	}
	if auto != nil {
		session, _, _ := parseHandoffAutoID(auto.ID)
		_, err := fmt.Fprintf(w, "\n# handoff automatique %s — session %s — %s\n\n%s\n", auto.ID, session, auto.Timestamp.Format(time.RFC3339), auto.Text)
		return err
	}
	return nil
}

func handoffShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "show <subject_id>",
		Annotations: scopedRead(),
		Short:       "Show the latest handoff for a subject",
		Args:        cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			logs, err := c.Log(cx, args[0])
			if err != nil {
				return err
			}
			return writeHandoffShow(os.Stdout, logs, asJSON) // logs are newest-first
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {handoff, auto} as JSON")
	return cmd
}
