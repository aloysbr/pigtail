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

// `pigtail alerts` reads and acknowledges pigtail's own alerts (subject
// « abandon respawn visible », D3/D5): a respawn that gave up or failed,
// derived by the daemon from the journal (GET /alerts).

func alertsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "Read and acknowledge pigtail's own alerts (a respawn that gave up or failed, a role that missed its deadline, a nearly full disk)",
	}
	cmd.AddCommand(alertsListCmd(), alertsAckCmd())
	return cmd
}

func alertsListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: scopedRead(),
		Short:       "List the open alerts of your scope",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res, err := c.Alerts(cx)
			if err != nil {
				return err
			}
			alerts := res.Alerts
			if alerts == nil {
				alerts = []pigtail.AlertView{}
			}
			if asJSON {
				b, err := json.MarshalIndent(alerts, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(b))
				return nil
			}
			if len(alerts) == 0 {
				fmt.Println("no open alert")
				return nil
			}
			for _, a := range alerts {
				subject := "-"
				switch {
				case a.SubjectTitle != "":
					subject = a.SubjectTitle
				case a.SubjectID != "":
					subject = a.SubjectID
				}
				// A machine alert (disk) has no scope: a dash holds the column.
				sc := a.Scope
				if sc == "" {
					sc = "-"
				}
				fmt.Printf("%s  %s  %s  %s  %s  %s  %s\n", a.ID, a.Kind, sc, a.State, subject, alertAge(a.AgeSeconds), a.Detail)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the open alerts as a JSON array ([] when none)")
	return cmd
}

// alertAge is an alert's age in its largest unit: 45s, 12m, 3h, 2d.
func alertAge(sec int) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", max(0, sec))
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%dh", sec/3600)
	default:
		return fmt.Sprintf("%dd", sec/86400)
	}
}

func alertsAckCmd() *cobra.Command {
	var crossScope, byHand bool
	cmd := &cobra.Command{
		Use:         "ack <run>",
		Annotations: scopedWrite(),
		Short:       "Acknowledge an alert by its run id",
		Long: `Run it from the session the pigtail notice woke, or from a session of the
alert's subject: the daemon checks it, and the alert leaves Pigtail at its
next read. Outside a session, or with --by-hand, it is acknowledged by hand.
An alert of a --cross-scope respawn takes --cross-scope (the notice says so).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			run := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var err error
			sid := os.Getenv("CLAUDE_CODE_SESSION_ID")
			if byHand {
				sid = ""
			}
			var res *pigtail.AlertResolution
			if sid != "" {
				// Acting as the session: the scope the registry holds for it (sessionScope).
				c := sessionClient(os.Getenv)
				if crossScope {
					c = c.WithCrossScopeWrite()
				}
				res, err = c.AckAlert(ctx, run, sid)
			} else {
				// By hand: the caller's scope, --scope / PIGTAIL_SCOPE honoured out of a cell,
				// the same reading as `alerts list` (sessionScope would fall back to the default).
				c, cerr := newScopedClient()
				if cerr != nil {
					return cerr
				}
				if crossScope {
					c = c.WithCrossScopeWrite()
				}
				res, err = c.AckAlert(ctx, run, "")
			}
			if err != nil {
				msg := fmt.Sprintf("ack alert %s: %s", run, alertErrMessage(err))
				var ae *pigtail.APIError
				if errors.As(err, &ae) && ae.Code == pigtail.ErrConflict {
					msg += "\n  (from outside the session the notice woke and the alert's subject: pigtail alerts ack " + run + " --by-hand)"
				}
				return errors.New(msg)
			}
			if res.Resolved {
				fmt.Printf("✓ alert %s resolved: %s\n", run, alertResolvedBy(res.Summary, run))
			} else {
				fmt.Printf("alert %s already resolved: %s\n", run, alertResolvedBy(res.Summary, run))
			}
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	cmd.Flags().BoolVar(&byHand, "by-hand", false, "acknowledge by hand, not as this session (outside the session the notice woke and the alert's subject)")
	return cmd
}

// alertErrMessage is the daemon's message of an API error, the error itself otherwise.
func alertErrMessage(err error) string {
	var ae *pigtail.APIError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

// alertResolvedBy is who resolved the alert, read from the resolution summary
// "respawn alert <run> resolved: <who>"; the summary whole if it has another shape.
func alertResolvedBy(summary, run string) string {
	if by, ok := strings.CutPrefix(summary, "respawn alert "+run+" resolved: "); ok {
		return by
	}
	if by, ok := strings.CutPrefix(summary, "role alert "+run+" resolved: "); ok {
		return by
	}
	return summary
}
