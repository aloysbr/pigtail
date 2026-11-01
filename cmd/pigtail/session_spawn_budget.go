package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// spawnBudgetTimeout bounds each call a spawn makes to the session budget
// (the ask, the give-back, the launched report): a daemon that hangs never
// holds a spawn (fail-open).
const spawnBudgetTimeout = 1500 * time.Millisecond

// spawnNeed says whether a spawn asks the session budget for a place.
type spawnNeed int

const (
	spawnNeedNone spawnNeed = iota // no ask: dry-run, a real relay, the gate off
	spawnNeedAsk                   // the one admitting ask, non-blocking
)

// spawnBudgetNeed is the single source of when a spawn asks for a place; no
// other line restates it. relayFrom is p.RelayFrom (the relay planSpawn
// resolved, "" when none or when it fell back to RelayNothing: a fallback is
// a plain spawn and asks).
func spawnBudgetNeed(dry bool, relayFrom string, getenv func(string) string) spawnNeed {
	switch {
	case dry, relayFrom != "", getenv("PIGTAIL_SESSION_GATE") == "0":
		return spawnNeedNone
	}
	return spawnNeedAsk
}

// spawnGateReason is the PIGTAIL_GATE_PASSED value of a spawn's launch line:
// relay for a real relay (never gated), spawn otherwise (already admitted).
func spawnGateReason(relayFrom string) string {
	if relayFrom != "" {
		return "relay"
	}
	return "spawn"
}

var (
	// spawnBudgetAsk is the one « admit now? » call (POST /session-budget/leases,
	// kind agent, no_wait); tests replace it.
	spawnBudgetAsk = realSpawnBudgetAsk
	// spawnLeaseReport gives an admitted lease back (launched false) or reports
	// that its spawn launched (true). Errors are ignored: PendingLeaseMax bounds
	// the lease. The client is the command's own (passed explicitly, never a
	// package global: two CLI callers may run in one process); the detached
	// watcher gets the one RunE built from its environment (the spawn's cell scope).
	spawnLeaseReport = realSpawnLeaseReport
)

func realSpawnBudgetAsk(ctx context.Context, c *pigtail.Client) (pigtail.SessionLeaseAnswer, error) {
	cx, cancel := context.WithTimeout(ctx, spawnBudgetTimeout)
	defer cancel()
	return c.AdmitSessionNow(cx, pigtail.SessionLeaseRequest{Kind: "agent", Origin: "spawn", NoWait: true})
}

// realSpawnLeaseReport reports through c, built by the command's RunE
// (newScopedClient is called there only, TestScopedCommandsDeclareAccess).
// nil c: nothing is reported.
func realSpawnLeaseReport(c *pigtail.Client, leaseID string, launched bool) {
	if c == nil {
		return
	}
	cx, cancel := context.WithTimeout(context.Background(), spawnBudgetTimeout)
	defer cancel()
	_ = c.ReleaseSessionLease(cx, leaseID, launched)
}

// askSpawnBudget makes the spawn's one ask. Admitted, it returns the lease id
// ("" when the daemon is off, absent or hangs: fail-open, nothing to give
// back). Refused, it returns the exit-3 error, after printing the --json
// object when asJSON.
func askSpawnBudget(ctx context.Context, c *pigtail.Client, stdout io.Writer, asJSON bool) (string, error) {
	a, err := spawnBudgetAsk(ctx, c)
	if err != nil || a.Admitted {
		return a.LeaseID, nil
	}
	if asJSON {
		obj := map[string]any{"refused": true, "busy": a.Busy, "budget": a.Budget, "reason": a.Reason}
		if b, mErr := json.Marshal(obj); mErr == nil {
			_, _ = fmt.Fprintln(stdout, string(b))
		}
	}
	return "", withExitCode(3, spawnErr("refusé · %s (%d/%d sessions occupées) — réessaie plus tard", a.Reason, a.Busy, a.Budget))
}

// leaseEnd is how an admitted lease ends.
type leaseEnd int

const (
	leaseNone    leaseEnd = iota // nothing to send
	leaseRelease                 // give the place back
	leaseReport                  // report launched: the place is kept until the next sample
	leaseWatcher                 // the detached watcher reports
)

// spawnLeaseEnd decides how an admitted lease ends, once WithReservation has
// returned err and the spawn's result r. Nothing is reported before Claude
// exists or its wait is over.
func spawnLeaseEnd(leaseID string, err error, r spawnResult) leaseEnd {
	switch {
	case leaseID == "":
		return leaseNone
	case r.State == "pending":
		// Handed off to the detached watcher (a failed write of the launch-ref
		// bind still returns an error with the launch pending: the watcher wins).
		if r.ArmWarning == "" {
			return leaseWatcher
		}
		return leaseNone // not armed: the lease runs to PendingLeaseMax
	case err != nil && !r.TabCreated:
		return leaseRelease
	}
	return leaseReport // the tab exists: the next sample counts whatever runs in it
}

// endSpawnLease sends what spawnLeaseEnd says.
func endSpawnLease(c *pigtail.Client, leaseID string, err error, r spawnResult) {
	switch spawnLeaseEnd(leaseID, err, r) {
	case leaseRelease:
		spawnLeaseReport(c, leaseID, false)
	case leaseReport:
		spawnLeaseReport(c, leaseID, true)
	}
}
