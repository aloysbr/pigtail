package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sessionCostCmd is `pigtail session cost` (D6): what a Claude session costs,
// by type (repo and status), over a window of the sampler's records (D7).
func sessionCostCmd() *cobra.Command {
	var (
		since  string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:         "cost",
		Annotations: scopedRead(),
		Short:       "What the sessions cost, by repo and status: CPU, mean and peak memory",
		Long: `Aggregate the daemon's session samples (session-samples.jsonl, one every 30 s)
over a window: per type — a repo and a status (au travail, inactive, en attente)
— the number of sessions, the mean CPU, the mean and the peak memory.

Read-only, filtered by the caller's scope. --json prints
{window, samples, types:[{repo, status, sessions, cpu_avg_percent,
memory_avg_gb, memory_peak_gb}]}; status is the native busy|idle|waiting.`,
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
			res, err := c.SessionSamples(cx, from)
			if err != nil {
				return err
			}
			return renderSessionCost(os.Stdout, sessbudget.Aggregate(since, res.Samples), asJSON)
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "window: a duration (24h, 7d), a date (2026-09-22) or unix millis")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}

// costStatusLabel is the D10 label of a native status.
func costStatusLabel(status string) string {
	switch status {
	case "busy":
		return "au travail"
	case "waiting":
		return "en attente"
	default:
		return "inactive"
	}
}

// frComma writes v with n decimals and a decimal comma.
func frComma(v float64, n int) string {
	return strings.Replace(fmt.Sprintf("%.*f", n, v), ".", ",", 1)
}

// renderSessionCost prints the report (D7), or its JSON.
func renderSessionCost(w io.Writer, r pigtail.SessionCost, asJSON bool) error {
	return renderSessionCostAt(w, r, asJSON, time.Now())
}

// sinceLabel renders a --since value in French for the header.
// A duration reads as « 7 j », « 24 h » or « 90 min »; a date or unix millis
// as « depuis le 22/09 » (the year added when it is not the current one).
func sinceLabel(v string, now time.Time) string {
	v = strings.TrimSpace(v)
	from, err := parseSince(v, now)
	if err != nil {
		return v
	}
	if strings.HasSuffix(v, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil {
			return fmt.Sprintf("%d j", n)
		}
	} else if d, err := time.ParseDuration(v); err == nil {
		if d%time.Hour == 0 {
			return fmt.Sprintf("%d h", int64(d/time.Hour))
		}
		return fmt.Sprintf("%d min", int64(d/time.Minute))
	}
	from = from.In(now.Location())
	if from.Year() != now.Year() {
		return "depuis le " + from.Format("02/01/2006")
	}
	return "depuis le " + from.Format("02/01")
}

func renderSessionCostAt(w io.Writer, r pigtail.SessionCost, asJSON bool, now time.Time) error {
	if r.Types == nil {
		r.Types = []pigtail.SessionCostType{}
	}
	if asJSON {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	unit := "échantillons"
	if r.Samples <= 1 {
		unit = "échantillon"
	}
	fmt.Fprintf(w, "Coût des sessions — %s (%d %s)\n", sinceLabel(r.Window, now), r.Samples, unit)
	if len(r.Types) == 0 {
		if r.Samples == 0 {
			fmt.Fprintln(w, "aucun échantillon — mesure éteinte ou historique vide")
		} else {
			fmt.Fprintln(w, "aucune session sur la fenêtre")
		}
	}
	for _, t := range r.Types {
		repo := t.Repo
		if repo == "" {
			repo = "(sans dépôt)"
		}
		noun := "sessions"
		if t.Sessions == 1 {
			noun = "session"
		}
		fmt.Fprintf(w, "%s · %s · %d %s · CPU moy %s %% · mémoire moy %s Go · pic %s Go\n",
			repo, costStatusLabel(t.Status), t.Sessions, noun,
			frComma(t.CPUAvgPct, 0), frComma(t.MemoryAvgGB, 1), frComma(t.MemoryPeakGB, 1))
	}
	return nil
}
