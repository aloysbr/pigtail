package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/usagehistory"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/usagerefresh"
)

// Background refresh of the usage readings (account rotation, D5 of
// sub_x000000000000008). The rotation picks an account from the usage cache
// each cell's .claude.json carries; that cache only moves while a session of
// the account runs. So every 10 minutes the daemon runs claude -p /usage (a
// local Claude Code command: no model call, no token) on each account whose
// newest reading is older than sweepStaleAfter (20 min), one account after the
// other, and writes the pass to <state>/usage-refresh.json, where pigtail usage
// and the rotation's stale warning read its failures. Staffing closes on a
// reading older than claudeusage.PickFreshFor (30 min), so the threshold leaves
// one interval of margin. Its cost: an idle account is read every 30 min instead
// of 40, at worst 10 relaunches an hour over 5 accounts instead of 7.5 (+33 %),
// still one claude at a time and no token.
//
// Never two claude -p /usage at once, across processes: the pass holds the
// same flock as pigtail usage --refresh (claudeusage.TryRefreshLock) for its
// whole Refresh; held by the other, the pass is skipped and says so. This
// sweep runs claude, never cmux.

// defaultUsageRefreshInterval: the sweep's cadence when the flag leaves it 0.
const defaultUsageRefreshInterval = 10 * time.Minute

// usageRefreshTimeout bounds one account's claude -p /usage (the CLI's 60 s).
const usageRefreshTimeout = 60 * time.Second

// sweepStaleAfter is the age from which the sweep relaunches a reading, so that
// no reading passes claudeusage.PickFreshFor between two passes: the freshness
// minus one interval, at least a minute. A residual window remains, on the safe
// side (one idle Staffing tour more): a reading of exactly the threshold waits
// for the next pass (StaleTargets is strict), and it is past PickFreshFor for
// the length of its relaunch (up to 60 s per account, in series).
func sweepStaleAfter(interval time.Duration) time.Duration {
	return max(claudeusage.PickFreshFor-interval, time.Minute)
}

// usageRefreshReminder: a lasting failure is logged again once a day (the
// main-worktree sweep's rule, mainRefreshReminder).
const usageRefreshReminder = 24 * time.Hour

// runUsageRefresh runs the sweep until ctx ends. The first pass comes one
// interval after start, never at once: a daemon restart (or a test starting
// a Server) launches no claude by itself.
func (s *Server) runUsageRefresh(ctx context.Context) {
	if s.cfg.UsageRefreshDisabled {
		log.Printf("daemon: background usage refresh disabled (-usage-refresh=false); readings only move while a session runs or on pigtail usage --refresh")
		return
	}
	interval := defaultUsageRefreshInterval
	if s.cfg.UsageRefreshIntervalSeconds > 0 {
		interval = time.Duration(s.cfg.UsageRefreshIntervalSeconds) * time.Second
	}
	if s.cfg.UsageStateDir == "" {
		log.Printf("daemon: background usage refresh not armed: no usage state dir")
		return
	}
	pass := s.armedUsageRefreshSweep(interval)
	log.Printf("daemon: background usage refresh armed, every %s (claude -p /usage on readings older than %s, one account at a time)",
		interval, claudeusage.FormatAge(pass.staleAfter))
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass.run(ctx)
		}
	}
}

// armedUsageRefreshSweep is the sweep the daemon runs at that interval.
func (s *Server) armedUsageRefreshSweep(interval time.Duration) *usageRefreshSweep {
	pass := newUsageRefreshSweep(s.cfg.UsageStateDir, s.usageHistory)
	pass.staleAfter = sweepStaleAfter(interval)
	return pass
}

// usageRefreshSweep is one sweep's state and seams, free of the Server so
// its rules are testable: what is launched, what is written, what is logged.
type usageRefreshSweep struct {
	mu sync.Mutex // one pass at a time in this process (the flock covers the others)

	loadCells func() ([]claudeusage.CellInput, error)
	readFile  func(string) ([]byte, error)
	now       func() time.Time
	launch    claudeusage.LaunchFunc
	timeout   time.Duration
	stateDir  string                // "" = the pass does nothing
	history   *usagehistory.History // nil = nothing recorded
	lock      func(stateDir string) (unlock func(), held bool, err error)
	logf      func(format string, args ...any)
	// staleAfter: the age from which a reading is relaunched; 0 = PickFreshFor.
	staleAfter time.Duration

	memo map[string]usageRefreshMemo // key → last logged condition
}

type usageRefreshMemo struct {
	reason        string
	since, logged time.Time
}

func newUsageRefreshSweep(stateDir string, history *usagehistory.History) *usageRefreshSweep {
	return &usageRefreshSweep{
		loadCells: func() ([]claudeusage.CellInput, error) {
			tbl, err := claudeprofile.Load(setup.ConfigDir())
			if err != nil {
				return nil, err
			}
			return usageCellInputs(tbl), nil
		},
		readFile: os.ReadFile,
		now:      time.Now,
		launch:   usagerefresh.New(os.Environ()).Launch,
		timeout:  usageRefreshTimeout,
		stateDir: stateDir,
		history:  history,
		lock:     claudeusage.TryRefreshLock,
		logf:     log.Printf,
	}
}

// usageCellInputs is the cell table as claudeusage reads it, in config-dir
// order — the CLI's CellsFrom (cmd/pigtail), which the daemon cannot import.
func usageCellInputs(tbl *claudeprofile.Table) []claudeusage.CellInput {
	dirs := tbl.ConfigDirs()
	out := make([]claudeusage.CellInput, 0, len(dirs))
	for _, dir := range dirs {
		c, _ := tbl.ByConfigDir(dir)
		out = append(out, c.UsageInput())
	}
	return out
}

// run is one pass.
func (p *usageRefreshSweep) run(ctx context.Context) {
	if p.stateDir == "" {
		return // no state dir: no lock, no state, no history (TestSweepRunEmptyStateDirWritesNothing)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.memo == nil {
		p.memo = map[string]usageRefreshMemo{}
	}
	now := p.now().UTC()

	cells, err := p.loadCells()
	if err != nil {
		p.once("table", err.Error(), now, "daemon: background usage refresh: no cell table — %s")
		p.writeState(claudeusage.BackgroundState{At: now, Error: "cell table: " + err.Error()})
		return
	}
	p.clear("table")
	// Recorded whatever comes next (refreshed, skipped on the lock, lock
	// error), after the refresh: the sample is the pass's last word.
	defer p.record(cells, now)

	unlock, held, err := p.lock(p.stateDir)
	switch {
	case err != nil:
		p.once("lock", err.Error(), now, "daemon: background usage refresh not run — %s")
		p.writeState(claudeusage.BackgroundState{At: now, Error: err.Error()})
		return
	case held:
		p.once("lock", "another refresh is running ("+claudeusage.RefreshLockPath(p.stateDir)+")", now, "daemon: background usage refresh skipped — %s")
		p.writeState(claudeusage.BackgroundState{At: now, Skipped: "locked"})
		return
	}
	defer unlock()
	p.clear("lock")

	snaps := claudeusage.ReadTable(cells, p.readFile)
	master := scope.Master()
	staleAfter := p.staleAfter
	if staleAfter == 0 {
		staleAfter = claudeusage.PickFreshFor
	}
	targets := claudeusage.StaleTargets(claudeusage.RefreshTargets(snaps, master),
		claudeusage.Build(snaps, master, now).Accounts, now, staleAfter)
	res := claudeusage.Refresh(ctx, targets, claudeusage.RefreshDeps{
		Launch: p.launch, ReadFile: p.readFile, Now: p.now, Timeout: p.timeout,
	})
	at := now
	st := claudeusage.BackgroundState{At: now, ResultsAt: &at, Results: make([]claudeusage.BackgroundResult, 0, len(res))}
	for i, x := range res {
		tg := targets[i] // Refresh returns one result per target, in order
		st.Results = append(st.Results, claudeusage.BackgroundResult{
			Label: tg.Label, AccountUUID: tg.AccountUUID, Cell: tg.Cell.ID, CellScope: tg.Cell.Scope,
			Status: x.Status, Error: x.Error, FetchedAfter: x.FetchedAfter,
		})
		key := "account:" + tg.AccountUUID
		if x.Status == claudeusage.RefreshFailed {
			p.once(key, x.Error, now, fmt.Sprintf("daemon: background usage refresh failed: %s (cell %s) — %%s", tg.Label, tg.Cell.ID))
		} else {
			p.clear(key)
		}
	}
	p.writeState(st)
}

// record appends one sample per account with a reading to the history, the
// cells read again so a refresh of this pass is in it. A failure is logged
// once per condition, like the sweep's others.
func (p *usageRefreshSweep) record(cells []claudeusage.CellInput, at time.Time) {
	if p.history == nil {
		return
	}
	snaps := claudeusage.ReadTable(cells, p.readFile)
	accts := claudeusage.Build(snaps, scope.Master(), p.now()).Accounts
	if err := p.history.Append(claudeusage.SamplesFrom(accts, at)); err != nil {
		p.once("history", err.Error(), at, "daemon: usage history: %s")
		return
	}
	p.clear("history")
}

// once logs a condition when it appears or changes, then once a day while it
// lasts (format takes the reason as its one %s).
func (p *usageRefreshSweep) once(key, reason string, now time.Time, format string) {
	prev, known := p.memo[key]
	switch {
	case !known || prev.reason != reason:
		p.memo[key] = usageRefreshMemo{reason: reason, since: now, logged: now}
		p.logf(format, reason)
	case now.Sub(prev.logged) >= usageRefreshReminder:
		prev.logged = now
		p.memo[key] = prev
		p.logf("%s", fmt.Sprintf(format, reason)+fmt.Sprintf(" (STILL, for %s)", roundedAge(now.Sub(prev.since))))
	}
}

func (p *usageRefreshSweep) clear(key string) { delete(p.memo, key) }

// writeState writes the pass atomically. A pass that did not run (skipped,
// error) keeps the previous pass's results: their failures stay visible.
func (p *usageRefreshSweep) writeState(st claudeusage.BackgroundState) {
	path := filepath.Join(p.stateDir, claudeusage.BackgroundStateName)
	if st.Results == nil {
		var prev claudeusage.BackgroundState
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &prev) == nil {
			st.Results, st.ResultsAt = prev.Results, prev.ResultsAt
		}
		if st.Results == nil {
			st.Results = []claudeusage.BackgroundResult{}
		}
	}
	if err := writeUsageState(path, st); err != nil {
		p.once("state", err.Error(), st.At, "daemon: background usage refresh: state not written — %s")
		return
	}
	p.clear("state")
}

func writeUsageState(path string, st claudeusage.BackgroundState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".usage-refresh-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
