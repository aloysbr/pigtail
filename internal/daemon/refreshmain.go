package daemon

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/mainfresh"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The main worktree of a project is the checkout people and agents READ: it is
// where "what does main look like?" gets answered, and it is the only worktree
// nobody owns, so nothing ever pulls it. PR #96 taught the *create* path to
// fast-forward it (git.go, under `!intent.NoFetch`), which fixed the half of
// the problem that shows up when a worktree is branched from a stale main.
//
// The other half stayed open for two months: that refresh fires at `pigtail
// new` and nowhere else. Between two creations — hours, sometimes days — the
// main checkout only ever falls further behind, and every read of it in that
// window answers from the past. Measured 2026-09-07 on the globex
// workspace: main was **23 commits behind origin/main with a perfectly clean
// tree** — all four safety conditions of the fast-forward met, and nothing to
// trigger it. An agent sent to read it reported code that had been replaced
// days earlier.
//
// So this is the read-side trigger, on the model of the existing sweeps
// (rammon, runBranchReconcile): a ticker over the registered projects that
// re-runs the very same guarded gesture. It adds no new judgement of its own —
// the four conditions live in the backend, once, and this pass cannot loosen
// them. What it adds is a clock.
//
// Why a sweep rather than "refresh when a command points an agent at main":
// there is no such command. Agents are pointed at the main directory by a path
// in a prompt, by a `cd`, by a grep — none of which reach pigtail. A trigger
// that only fires on the paths pigtail can see would have exactly the shape of
// the bug being fixed: correct, and never invoked at the moment that matters.

// defaultMainRefreshInterval is the sweep cadence when the flag leaves it
// unset. Ten minutes: the session journal has main moving in hours, and the
// pass costs one `git fetch` plus a couple of plumbing calls per project — the
// same order as the branch reconciliation next door, an order of magnitude
// under the RAM sweep.
const defaultMainRefreshInterval = 10 * time.Minute

// mainRefreshReminder is how long an unchanged abstention stays silent before
// it is logged again.
//
// The dedup below (log a refusal when its *reason* changes) is what keeps
// daemon.log readable at 144 sweeps a day per project. Its cost, measured on
// the friction this whole effort is about: a main checkout parked on a feature
// branch for a week is named once, on day one, and never again — so the answer
// to "why is main still behind?" is a line that scrolled past six days ago,
// under thousands of others. A refusal that persists is not the same event
// repeating; it is one condition *lasting*, and duration is the part worth
// re-stating. Once a day is the cadence where a reader meets the line while
// the situation is still true, and cannot mistake it for noise: 1 line a day
// against the 144 the naive version would write.
const mainRefreshReminder = 24 * time.Hour

// runMainRefresh keeps each project's main worktree level with its base.
func (s *Server) runMainRefresh(ctx context.Context) {
	if s.cfg.MainRefreshDisabled {
		// Say so once. A feature that is off and a feature that is broken look
		// identical in a log that mentions neither, and this one was invisible
		// for two months precisely because nothing ever said what it did.
		log.Printf("daemon: main-worktree refresh disabled (-refresh-main=false); main checkouts now only move at `pigtail new`")
		return
	}
	refresher, ok := s.backend.(backend.MainRefresher)
	if !ok {
		return // a backend with no shared checkout to keep fresh
	}

	interval := defaultMainRefreshInterval
	if s.cfg.MainRefreshIntervalSeconds > 0 {
		interval = time.Duration(s.cfg.MainRefreshIntervalSeconds) * time.Second
	}
	log.Printf("daemon: main-worktree refresh armed, every %s (fast-forward only, never over a dirty or repurposed checkout)", interval)

	pass := &mainRefreshPass{
		refresher: refresher,
		registry:  s.mainFresh,
		memo:      map[string]mainRefreshMemo{},
		now:       time.Now,
		logf:      log.Printf,
	}

	// Sweep once immediately: a daemon that just restarted is exactly when the
	// checkouts are most likely to have drifted, and waiting a full interval to
	// find out is the delay this whole pass exists to remove.
	s.refreshMainOnce(ctx, pass)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshMainOnce(ctx, pass)
		}
	}
}

func (s *Server) refreshMainOnce(ctx context.Context, pass *mainRefreshPass) {
	if s.store == nil {
		return
	}
	projects, err := s.store.ListProjects()
	if err != nil {
		log.Printf("daemon: main-worktree refresh could not list projects: %v", err)
		return
	}
	pass.run(ctx, projects)
}

// mainRefreshMemo is what the sweep remembers about one project's last
// abstention, so it can tell a *new* refusal from one that is simply lasting.
type mainRefreshMemo struct {
	reason string    // the abstention as last logged
	since  time.Time // when this reason was first seen
	logged time.Time // when it was last written to the log
}

// mainRefreshPass runs the sweep over the projects and reports what happened.
//
// Free of the Server and the store so the rules below are testable as rules:
// what gets logged, what gets deduplicated, what gets repeated, and what gets
// published for the read-only /servers view. The registry and the logger may
// both be nil.
type mainRefreshPass struct {
	refresher backend.MainRefresher
	registry  *mainfresh.Registry
	memo      map[string]mainRefreshMemo
	now       func() time.Time
	logf      func(format string, args ...interface{})
}

// run performs one pass. Returns how many main worktrees were actually moved.
func (p *mainRefreshPass) run(ctx context.Context, projects []pigtail.Project) int {
	if p == nil || p.refresher == nil {
		return 0
	}
	if p.memo == nil {
		p.memo = map[string]mainRefreshMemo{}
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	updated := 0
	for i := range projects {
		proj := projects[i]
		res, err := p.refresher.RefreshMain(ctx, &proj)
		if err != nil {
			p.log("daemon: main-worktree refresh failed for %s: %v", proj.Name, err)
			continue
		}
		// Publish before deciding anything about logging: `pigtail servers`
		// answers "is my main fresh?" from this, and that question is asked on
		// every verdict — including the boring ones the log deliberately omits.
		p.registry.Record(&proj, res, now())

		switch res.Action {
		case backend.MainRefreshUpdated:
			updated++
			delete(p.memo, proj.ID)
			// Always logged, every time: this is the pass doing the thing it
			// exists for, on a directory other sessions are reading. A move
			// nobody can date is a move nobody can trust.
			p.log("daemon: main worktree of %s fast-forwarded onto %s%s (%s → %s): %s",
				proj.Name, res.Base, behindSuffix(res.Behind), res.From, res.To, res.Path)

		case backend.MainRefreshUpToDate:
			// The steady state, reached every ten minutes forever: no line.
			// Clearing the memo is what makes the *next* abstention log again,
			// so a checkout that goes dirty, is cleaned, then goes dirty again
			// says so both times.
			delete(p.memo, proj.ID)

		case backend.MainRefreshSkipped:
			p.logAbstention(proj, res, now())
		}
	}
	return updated
}

// logAbstention applies the two rules an abstention obeys.
//
//  1. Log it when the reason CHANGES. Repeating one refusal 144 times a day
//     per project is its own way of being invisible — a file nobody finishes
//     reading answers no question at all.
//  2. Log it again once a day while it LASTS, with how long it has lasted.
//     Rule 1 alone means a main checkout parked on a feature branch for a week
//     is mentioned once, on day one: the answer to "why is main still behind?"
//     exists, it has simply scrolled out of reach. A daily line keeps it
//     within a `tail` of the question, and its duration is the part that says
//     "this is not a blip".
func (p *mainRefreshPass) logAbstention(proj pigtail.Project, res backend.MainRefresh, now time.Time) {
	prev, known := p.memo[proj.ID]
	switch {
	case !known || prev.reason != res.Reason:
		p.memo[proj.ID] = mainRefreshMemo{reason: res.Reason, since: now, logged: now}
		p.log("daemon: main worktree of %s left untouched%s — %s: %s",
			proj.Name, behindSuffix(res.Behind), res.Reason, res.Path)

	case now.Sub(prev.logged) >= mainRefreshReminder:
		prev.logged = now
		p.memo[proj.ID] = prev
		p.log("daemon: main worktree of %s STILL left untouched after %s%s — %s: %s",
			proj.Name, roundedAge(now.Sub(prev.since)), behindSuffix(res.Behind), res.Reason, res.Path)
	}
}

func (p *mainRefreshPass) log(format string, args ...interface{}) {
	if p.logf == nil {
		return
	}
	p.logf(format, args...)
}

// behindSuffix names the drift in a log line, and says nothing when there is
// no known drift (MainRefresh.Behind is 0 both when level and when it could
// not be measured — inventing a number for the second case would be worse
// than the silence).
func behindSuffix(behind int) string {
	switch {
	case behind <= 0:
		return ""
	case behind == 1:
		return " (1 commit behind)"
	default:
		return fmt.Sprintf(" (%d commits behind)", behind)
	}
}

// roundedAge renders how long a refusal has lasted, in hours — the reminder
// fires daily, so minutes would be false precision.
func roundedAge(d time.Duration) time.Duration { return d.Round(time.Hour) }
