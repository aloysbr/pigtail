package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// gateTimeout bounds the gate's one call: a daemon that accepts then hangs
// lets the launch through after it (fail-open).
const gateTimeout = 2 * time.Second

// sessionGateCmd is `pigtail session gate --kind human`: the launcher's gate
// (plan « Contrat pour le lanceur »). Internal plumbing, hidden from help.
// the user's launches are always admitted: the command only prints the
// over-budget line when the daemon flags the launch, and exits 0 always.
func sessionGateCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:    "gate",
		Hidden: true,
		// It writes no theme nor subject: a kind human ask records nothing.
		Annotations: scopedRead(),
		Short:       "Launcher gate: warn when a launch goes over the session budget (never blocks)",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if kind != "human" {
				return fmt.Errorf("--kind: only human is supported")
			}
			if gateBypassed(os.Getenv) {
				return nil
			}
			c, err := newScopedClient()
			if err != nil {
				return nil // fail-open
			}
			runSessionGate(cmd.ErrOrStderr(), c)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "human", "who launches (human: the user's launch, always admitted)")
	return cmd
}

// gateBypassed: PIGTAIL_SESSION_GATE=0, or PIGTAIL_GATE_PASSED set (an
// admitted spawn, a respawn, a cmux fix or a relay: already gated or exempt).
func gateBypassed(getenv func(string) string) bool {
	return getenv("PIGTAIL_SESSION_GATE") == "0" || getenv("PIGTAIL_GATE_PASSED") != ""
}

// runSessionGate asks the daemon once, as kind human, and prints the
// over-budget line when the answer carries over_budget. Any failure to reach
// the daemon within gateTimeout passes silently.
func runSessionGate(stderr io.Writer, c *pigtail.Client) {
	cx, cancel := context.WithTimeout(context.Background(), gateTimeout)
	defer cancel()
	a, err := c.AdmitSessionNow(cx, pigtail.SessionLeaseRequest{Kind: "human", Origin: "launcher"})
	if err != nil || a.OverBudget == nil || !*a.OverBudget {
		return
	}
	fmt.Fprintf(stderr, "au-delà du budget : %d/%d au travail · %s\n", a.Busy, a.Budget, a.Reason)
}
