package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Role supervision, L1 (sub_x000000000000016). Every minute the sweep judges
// each declared role (a subject that must act on a cadence) from its
// subject's transcripts and sessions, and writes one journal alert per missed
// deadline. It reads files and the store; it never runs cmux, git or launchctl.

const (
	defaultRoleSweepInterval = time.Minute
	// Defaults of a role's grace and stall when its row carries none.
	defaultRoleGrace = 10 * time.Minute
	defaultRoleStall = 20 * time.Minute
	// roleBeatSlack: the heartbeat is stale after 3 intervals plus this.
	roleBeatSlack = 30 * time.Second
)

// rolesSweep is the sweep's state and seams, free of the Server so its rules
// are testable.
type rolesSweep struct {
	st       *store.Store
	collect  func(pigtail.Role) roles.Observation
	now      func() time.Time
	mono     func() time.Duration // time since the sweep's construction, monotonic
	logf     func(format string, args ...any)
	interval time.Duration

	lastBeat atomic.Int64 // mono() ns at the start of the last pass

	mu        sync.Mutex // one pass at a time; guards the fields below but snap
	snapMu    sync.Mutex // guards snap, so Snapshot never waits for a pass
	prev      time.Time
	holdUntil time.Time
	snap      []pigtail.RoleStatus
	logged    map[string]bool // dedup of log lines

	beforeAlert func() // test seam: runs right before an alert is written
}

func newRolesSweep(st *store.Store, c *collector, interval time.Duration) *rolesSweep {
	if interval <= 0 {
		interval = defaultRoleSweepInterval
	}
	start := time.Now()
	return &rolesSweep{
		st: st, collect: c.collect, now: time.Now, logf: log.Printf, interval: interval,
		mono:   func() time.Duration { return time.Since(start) },
		logged: map[string]bool{},
	}
}

// newRolesCollector is the real collector on the store and the claude files.
func newRolesCollector(st *store.Store) *collector {
	return &collector{
		loops:      claudesession.NewLoopReader(),
		peers:      st.PeersOfSubject,
		readNative: claudesession.ReadNative,
		alive:      claudesession.SessionAlive,
		find:       claudesession.Find,
	}
}

// logOnce logs a line the first time its key is seen.
func (w *rolesSweep) logOnce(key, format string, args ...any) {
	if w.logged[key] {
		return
	}
	w.logged[key] = true
	w.logf(format, args...)
}

// Stale reports whether the sweep has stopped beating: no pass began for more
// than 3 intervals plus 30 s. Lock-free; a sweep never beaten counts from its
// construction.
func (w *rolesSweep) Stale() (string, bool) {
	age := w.mono() - time.Duration(w.lastBeat.Load())
	if age > 3*w.interval+roleBeatSlack {
		return fmt.Sprintf("loop roles: last tick %s ago", age.Round(time.Second)), true
	}
	return "", false
}

// Snapshot is the verdict of each role at the last pass.
func (w *rolesSweep) Snapshot() []pigtail.RoleStatus {
	w.snapMu.Lock()
	defer w.snapMu.Unlock()
	return append([]pigtail.RoleStatus(nil), w.snap...)
}

func (s *Server) runRoleWatch(ctx context.Context) {
	w := s.roles
	if w == nil {
		log.Printf("daemon: role supervision disabled (-role-watch=false)")
		return
	}
	log.Printf("daemon: role supervision armed, every %s", w.interval)
	w.pass()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.pass()
		}
	}
}

func normalizeRole(r pigtail.Role) pigtail.Role {
	if r.Grace <= 0 {
		r.Grace = defaultRoleGrace
	}
	if r.Stall <= 0 {
		r.Stall = defaultRoleStall
	}
	return r
}

// pass judges every role once.
func (w *rolesSweep) pass() {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() {
		if p := recover(); p != nil {
			w.logOnce(fmt.Sprintf("pass-panic:%v", p), "roles: pass panicked: %v\n%s", p, debug.Stack())
		}
	}()
	w.lastBeat.Store(int64(w.mono()))
	now := w.now()
	rs, err := w.st.ListRoles(scope.Any)
	if err != nil {
		w.logOnce("list:"+err.Error(), "roles: listing roles: %v", err)
		return
	}
	var maxGrace time.Duration
	for _, r := range rs {
		maxGrace = max(maxGrace, normalizeRole(r).Grace)
	}
	if maxGrace == 0 {
		maxGrace = defaultRoleGrace
	}
	// A sleep gap is read on wall clocks: the monotonic clock stops during sleep.
	if !w.prev.IsZero() && now.Round(0).Sub(w.prev.Round(0)) > 3*w.interval {
		w.holdUntil = now.Add(maxGrace)
		w.logf("roles: gap of %s since the last pass (sleep?): writes held for %s",
			now.Round(0).Sub(w.prev.Round(0)).Round(time.Second), maxGrace)
	}
	w.prev = now
	held := now.Before(w.holdUntil)
	snap := make([]pigtail.RoleStatus, 0, len(rs))
	for _, r := range rs {
		snap = append(snap, w.judgeRole(normalizeRole(r), now, held))
	}
	w.snapMu.Lock()
	w.snap = snap
	w.snapMu.Unlock()
}

// judgeRole judges and acts for one role under its own recover.
func (w *rolesSweep) judgeRole(r pigtail.Role, now time.Time, held bool) (st pigtail.RoleStatus) {
	st = pigtail.RoleStatus{Role: r, JudgedAt: now, Held: held}
	v := roles.Unknown("not judged")
	defer func() {
		if p := recover(); p != nil {
			v = roles.Unknown(fmt.Sprintf("panic: %v", p))
			w.logOnce(fmt.Sprintf("panic:%s/%s:%v", r.Scope, r.Name, p), "roles: %s panicked: %v\n%s", r.Name, p, debug.Stack())
		}
		fillStatus(&st, v)
	}()
	v = roles.Judge(r, w.collect(r), now)
	if v.State == roles.StateUnknown {
		w.logOnce(fmt.Sprintf("unknown:%s/%s:%s", r.Scope, r.Name, v.Reason), "roles: %s unknown: %s", r.Name, v.Reason)
	}
	unrec, err := w.st.UnrecoveredRoleAlerts(scope.Scope(r.Scope))
	if err != nil {
		w.logOnce("store:"+err.Error(), "roles: %s: reading alerts: %v", r.Name, err)
		return st
	}
	closed, err := w.st.RecoveredRoleKeys(scope.Scope(r.Scope), r.Name)
	if err != nil {
		w.logOnce("store:"+err.Error(), "roles: %s: reading recovered keys: %v", r.Name, err)
		return st
	}
	var open []roles.Episode
	for _, a := range unrec {
		if a.Name == r.Name {
			open = append(open, roles.Episode{Key: a.Key, Acked: a.Acked})
		}
	}
	act := roles.Decide(open, closed, v, held, now)
	if !act.Reclosed.IsZero() {
		w.logOnce(fmt.Sprintf("reclosed:%s/%s:%d", r.Scope, r.Name, act.Reclosed.UnixMilli()),
			"roles: %s: episode %s already closed — not re-raised", r.Name, act.Reclosed.Local().Format("15:04"))
	}
	if !act.Raise.IsZero() {
		e := &pigtail.JournalEntry{
			ID: roles.AlertID(r.Name, act.Raise), Timestamp: now, Kind: store.RoleAlertKind, Scope: r.Scope,
			SubjectID: r.SubjectID, Summary: fmt.Sprintf("Role %s %s: %s", r.Name, v.State, v.Reason),
			Source: fmt.Sprintf("pigtail:roles/%s?state=%s", r.Name, v.State),
		}
		if w.beforeAlert != nil {
			w.beforeAlert()
		}
		if _, err := w.st.AddJournal(e); err != nil {
			w.logOnce("store:"+err.Error(), "roles: %s: writing alert: %v", r.Name, err)
		}
		st.AlertID = e.ID
		w.closeIfRemoved(r, now)
	}
	for _, k := range act.Recover {
		e := &pigtail.JournalEntry{
			ID: roles.RecoveredID(r.Name, k), Timestamp: now, Kind: store.RoleRecoveredKind, Scope: r.Scope,
			SubjectID: r.SubjectID, Summary: fmt.Sprintf("Role %s recovered: %s", r.Name, v.Reason),
			Source: fmt.Sprintf("pigtail:roles/%s?state=ok", r.Name),
		}
		if _, err := w.st.AddJournal(e); err != nil {
			w.logOnce("store:"+err.Error(), "roles: %s: writing recovery: %v", r.Name, err)
		}
	}
	if st.AlertID == "" && len(open) > 0 {
		sort.Slice(open, func(i, j int) bool { return open[i].Key.Before(open[j].Key) })
		st.AlertID = roles.AlertID(r.Name, open[0].Key)
	}
	return st
}

func fillStatus(st *pigtail.RoleStatus, v roles.Verdict) {
	st.State, st.Reason = string(v.State), v.Reason
	if !v.Deadline.IsZero() {
		d := v.Deadline
		st.Deadline = &d
	}
	if !v.Episode.IsZero() {
		e := v.Episode
		st.Episode = &e
	}
}

// closeIfRemoved closes the episodes of a role that was removed since the pass listed it: DELETE /roles shares no lock with
// the sweep, so an alert written after its closeRoleEpisodes would stay open
// for good. Checked after the write, so either this closes it or the DELETE,
// which runs later, finds it.
func (w *rolesSweep) closeIfRemoved(r pigtail.Role, now time.Time) {
	_, err := w.st.GetRole(scope.Scope(r.Scope), r.Name)
	if err == nil {
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		w.logOnce("store:"+err.Error(), "roles: %s: re-reading the role: %v", r.Name, err)
		return
	}
	if err := handlers.CloseRoleEpisodes(w.st, r, now.UTC()); err != nil {
		w.logOnce("store:"+err.Error(), "roles: %s: closing the episodes of a removed role: %v", r.Name, err)
	}
}
