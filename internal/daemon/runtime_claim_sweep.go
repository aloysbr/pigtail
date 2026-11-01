package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runtimeClaimSweepEvery is how often the sweep looks at the lease.
const runtimeClaimSweepEvery = time.Minute

// runtimeClaimSweep lifts the runtime claim at its 4 h deadline and journals
// each expiry once. It is the only journal writer: a lift made by a route or
// by the CLI leaves runtime-claim-expired.json, and the journal id
// (runtime-claim-expired:<rtc id>) makes the write idempotent.
type runtimeClaimSweep struct {
	dir     string
	add     func(*pigtail.JournalEntry) (bool, error)
	logf    func(string, ...any)
	human   string // the user's name (settings.yaml human.name); "" = the default
	lastErr string
}

// once lifts an expired lease, then journals the marker if its lease is not
// the one currently held.
func (w *runtimeClaimSweep) once(now time.Time) {
	if exp, lifted, err := runtimeclaim.Expire(w.dir, now); err != nil {
		if msg := err.Error(); msg != w.lastErr {
			w.lastErr = msg
			w.logf("runtime claim sweep: %v", err)
		}
	} else {
		w.lastErr = ""
		if lifted {
			w.logf("runtime claim %s expired at its deadline, lifted", exp.Lease.ID)
		}
	}
	exp, ok := runtimeclaim.ReadExpiry(w.dir)
	if !ok {
		return
	}
	if cur, held, err := runtimeclaim.Load(w.dir); err == nil && held && cur.ID == exp.Lease.ID {
		return
	}
	e := expiryJournalEntry(exp, w.human)
	if err := e.Validate(); err != nil {
		w.logf("runtime claim sweep: journal entry refused: %v", err)
		return
	}
	created, err := w.add(&e)
	if err != nil {
		w.logf("runtime claim sweep: journal: %v", err)
		return
	}
	if created {
		w.logf("runtime claim %s expiry journalled", exp.Lease.ID)
	}
}

// expiryJournalEntry: the row names the lease and its holder's session, never
// its label, reason nor branch (D23).
func expiryJournalEntry(exp runtimeclaim.Expiry, human string) pigtail.JournalEntry {
	sc := scope.Master()
	if parsed, err := scope.Parse(exp.Lease.LabelScope); err == nil {
		sc = parsed
	}
	if human == "" {
		human = usersettings.DefaultHumanName
	}
	holder := human + " outside Claude Code"
	if s := exp.Lease.Session; s != "" {
		if len(s) > 8 {
			s = s[:8]
		}
		holder = "session " + s
	}
	summary := fmt.Sprintf("runtime claim %s expired at its 4 h deadline %s — held by %s since %s",
		exp.Lease.ID, exp.At.Local().Format("2006-01-02 15:04"), holder, exp.Lease.Since.Local().Format("15:04"))
	if exp.Lease.PR > 0 {
		summary += fmt.Sprintf(", PR #%d", exp.Lease.PR)
	}
	return pigtail.JournalEntry{
		ID:        "runtime-claim-expired:" + exp.Lease.ID,
		Timestamp: exp.At.UTC(),
		Kind:      "runtime-claim-expired",
		Scope:     string(sc),
		Summary:   summary,
		Source:    "pigtail:runtime/claim#" + exp.Lease.ID,
	}
}

// runRuntimeClaimSweep runs the sweep at start, then every minute, until ctx
// ends. It needs the store (the journal); without one it returns.
func (s *Server) runRuntimeClaimSweep(ctx context.Context) {
	if s.store == nil {
		return
	}
	w := &runtimeClaimSweep{dir: daemonMaintenanceDir(s.cfg, os.Getenv), add: s.store.AddJournal, logf: log.Printf, human: s.userSettings().Human.Name}
	w.once(time.Now())
	t := time.NewTicker(runtimeClaimSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.once(time.Now())
		}
	}
}
