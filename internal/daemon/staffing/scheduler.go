package staffing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sessionlaunch"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	// RetryAfterFailure is how long a subject whose launch failed or was
	// refused is not taken again.
	RetryAfterFailure = 15 * time.Minute
	// LaunchGrace is how long a freshly launched session counts as working
	// whatever its native status says: between the launch record and its first
	// busy, a tour must not see 0 places and start a second subject.
	LaunchGrace = 3 * time.Minute
	// QueueShown cuts each scope's queue in State.
	QueueShown = 5
	// ObserveWindow is the age past which a launch made by another origin is
	// no longer journaled observed_launch.
	ObserveWindow = time.Hour
	// defaultEvery is the tick: the end of a session has no event.
	defaultEvery = 60 * time.Second
)

// Store is what a tour reads from the database.
type Store interface {
	RankedSubjects(sc scope.Scope) ([]store.RankedSubject, error)
	ListPeers(includeEnded bool) ([]pigtail.PeerSession, error) // live (Alive) and, for the join, ended included
	ListSessionWaits() (map[string][]pigtail.SessionWait, error)
	ListPauses() ([]pigtail.SessionPause, error)
	ListOpenWaitRecords(f store.WaitRecordFilter) ([]store.WaitRecord, error)
}

// Budget is the scheduler's place in the session budget (#255), satisfied by
// *sessbudget.Controller. Only the scheduler asks it: the launcher never does.
type Budget struct {
	Check        func() (enabled, admitted bool, reason string) // records nothing
	Admit        func(scope string) (lease string, admitted bool, reason string)
	Release      func(lease string)
	MarkLaunched func(lease string)
}

// Deps are the scheduler's seams. A nil gate is open except the budget: no
// Check reads as a budget switched off (closed).
type Deps struct {
	Store        Store
	Launch       func(ctx context.Context, sub pigtail.Subject) (pigtail.SessionLaunch, error)
	Launches     func() ([]pigtail.SessionLaunch, error)
	Burst        func() (sessionlaunch.Burst, error)
	Budget       Budget
	Machine      func() (ok bool, reason string) // CPU and memory queues
	Maintenance  func() (on bool, reason string, err error)
	Margin       func(scope string) (ok bool, reason string)
	TokenBudget  func(scope string) (ok bool, reason string) // nil = open, like Margin
	Scopes       func() []string
	Alive        func(pigtail.PeerSession) bool                         // default peers.Alive
	Native       func(cfg string, pid int) (claudesession.Native, bool) // default claudesession.ReadNative
	Publish      func(pigtail.Event)
	SettingsPath string
	LogPath      string
	Every        time.Duration // default 60 s
	Now          func() time.Time
	Logf         func(string, ...any)
	AfterTour    func(pigtail.StaffingDecision) // tests only; nil in production
}

func (d *Deps) defaults() {
	if d.Alive == nil {
		d.Alive = peers.Alive
	}
	if d.Native == nil {
		d.Native = claudesession.ReadNative
	}
	if d.Publish == nil {
		d.Publish = func(pigtail.Event) {}
	}
	if d.Every <= 0 {
		d.Every = defaultEvery
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	if d.Scopes == nil {
		d.Scopes = func() []string { return nil }
	}
	if d.Launches == nil {
		d.Launches = func() ([]pigtail.SessionLaunch, error) { return nil, nil }
	}
}

// startedLaunch is a launch the scheduler started, still open.
type startedLaunch struct {
	subjectID, shortTitle, scope string
}

// Scheduler is the staffing loop.
type Scheduler struct {
	d    Deps
	log  *Log
	kick chan struct{}

	tourMu sync.Mutex // one tour at a time

	mu          sync.Mutex
	settings    Settings
	settingsErr string
	lastTour    *pigtail.StaffingDecision // the last idle/would_launch line written
	tourIsLast  bool                      // lastTour is the last line of the file
	last        *pigtail.StaffingDecision // the last tour's decision
	checkedAt   *time.Time
	own         map[string]bool          // launch ids the scheduler started
	started     map[string]startedLaunch // of them, those still open
	observed    map[string]bool          // launch ids of other origins already journaled
	refusedAt   map[string]time.Time     // subject → its last launch_refused / launch_failed line
	lastScope   string
}

// New builds the scheduler, reading back its settings and its journal.
func New(d Deps) *Scheduler {
	d.defaults()
	s := &Scheduler{d: d, log: OpenLog(d.LogPath), kick: make(chan struct{}, 1),
		own: map[string]bool{}, started: map[string]startedLaunch{}, observed: map[string]bool{}, refusedAt: map[string]time.Time{}}
	st, err := LoadSettings(d.SettingsPath)
	s.settings = st
	if err != nil {
		s.settingsErr = err.Error()
		d.Logf("staffing: settings: %v (running %s)", err, st.Mode)
	}
	if err := s.log.Rotate(LogMaxBytes); err != nil {
		d.Logf("staffing: rotate the decision journal: %v", err)
	}
	ds, err := s.log.Read(time.Time{}, 0)
	if err != nil {
		d.Logf("staffing: read the decision journal: %v", err)
	}
	for i := range ds {
		s.replay(ds[i], i == len(ds)-1)
	}
	return s
}

// isTourLine: the lines a tour rewrites when it repeats.
func isTourLine(outcome string) bool {
	return outcome == pigtail.StaffingOutcomeIdle || outcome == pigtail.StaffingOutcomeWouldLaunch
}

// replay folds one journal line into the memory New rebuilds.
func (s *Scheduler) replay(d pigtail.StaffingDecision, fileLast bool) {
	switch d.Outcome {
	case pigtail.StaffingOutcomeIdle, pigtail.StaffingOutcomeWouldLaunch:
		dd := d
		s.lastTour, s.last = &dd, &dd
		s.tourIsLast = fileLast
	case pigtail.StaffingOutcomeLaunchStarted:
		dd := d
		s.last = &dd
		s.own[d.LaunchID] = true
		s.started[d.LaunchID] = startedLaunch{d.SubjectID, d.ShortTitle, d.Scope}
		if d.Scope != "" {
			s.lastScope = d.Scope
		}
	case pigtail.StaffingOutcomeLaunchRefused:
		dd := d
		s.last = &dd
		s.noteRefused(d.SubjectID, d.TS)
	case pigtail.StaffingOutcomeLaunched:
		delete(s.started, d.LaunchID)
	case pigtail.StaffingOutcomeLaunchFailed:
		delete(s.started, d.LaunchID)
		s.noteRefused(d.SubjectID, d.TS)
	case pigtail.StaffingOutcomeObservedLaunch:
		s.observed[d.LaunchID] = true
	}
	if d.Outcome != "" && !isTourLine(d.Outcome) && fileLast {
		s.tourIsLast = false
	}
}

func (s *Scheduler) noteRefused(sub string, at time.Time) {
	if sub == "" {
		return
	}
	if at.After(s.refusedAt[sub]) {
		s.refusedAt[sub] = at
	}
}

// Kick asks for a tour; it never blocks.
func (s *Scheduler) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run tours at start, then at each Kick and each Every, until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.d.Every)
	defer t.Stop()
	for {
		s.Tour(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-t.C:
		}
	}
}

// reading is one reading of everything a tour needs.
type reading struct {
	in         Inputs
	subjects   map[string]store.RankedSubject
	launches   []pigtail.SessionLaunch
	eligibleIn map[string]bool
}

// read reads the world; Margin is asked for each scope with an eligible subject,
// TokenBudget for those whose margin passes.
func (s *Scheduler) read(now time.Time) (reading, error) {
	d := &s.d
	w := reading{subjects: map[string]store.RankedSubject{}, eligibleIn: map[string]bool{}}
	all, err := d.Store.ListPeers(true)
	if err != nil {
		return w, fmt.Errorf("list peers: %w", err)
	}
	var live []pigtail.PeerSession
	for _, p := range all {
		if d.Alive(p) {
			// The stored status is frozen at SessionStart when the sidebar loop
			// is off: a place reads the native file, like the session budget.
			p.Status = ""
			if !phantomPeer(p) {
				if n, ok := d.Native(p.ConfigDir, p.PID); ok && n.SessionID == p.SessionID {
					p.Status = n.Status
				}
			}
			live = append(live, p)
		}
	}
	waits, err := d.Store.ListSessionWaits()
	if err != nil {
		return w, fmt.Errorf("list session waits: %w", err)
	}
	pauses, err := d.Store.ListPauses()
	if err != nil {
		return w, fmt.Errorf("list pauses: %w", err)
	}
	recs, err := d.Store.ListOpenWaitRecords(store.WaitRecordFilter{Scope: scope.Any, IncludeSnoozed: true, Now: now})
	if err != nil {
		return w, fmt.Errorf("list open waits: %w", err)
	}
	launches, err := d.Launches()
	if err != nil {
		return w, fmt.Errorf("list launches: %w", err)
	}
	openWaits := map[string]string{}
	for _, r := range recs { // oldest first
		if r.SubjectID == "" || !waittype.Answerable(waittype.Type(r.Type)) {
			continue
		}
		if _, ok := openWaits[r.SubjectID]; !ok {
			openWaits[r.SubjectID] = r.Type
		}
	}
	queues := map[string][]store.RankedSubject{}
	for _, sc := range d.Scopes() {
		q, err := d.Store.RankedSubjects(scope.Scope(sc))
		if err != nil {
			return w, fmt.Errorf("rank subjects of a scope: %w", err)
		}
		queues[sc] = q
		for _, r := range q {
			w.subjects[r.ID] = r
			if r.Rank > 0 && readyStatuses[r.Status] {
				w.eligibleIn[sc] = true
			}
		}
	}
	margin := map[string]string{}
	if d.Margin != nil {
		for sc := range w.eligibleIn {
			if ok, reason := d.Margin(sc); !ok {
				if reason == "" {
					reason = "over margin"
				}
				margin[sc] = reason
			}
		}
	}
	tokens := map[string]string{}
	if d.TokenBudget != nil {
		for sc := range w.eligibleIn {
			if margin[sc] != "" {
				continue
			}
			if ok, reason := d.TokenBudget(sc); !ok {
				if reason == "" {
					reason = "over weekly budget"
				}
				tokens[sc] = reason
			}
		}
	}

	s.mu.Lock()
	cooldown := map[string]time.Time{}
	for sub, at := range s.refusedAt {
		cooldown[sub] = at.Add(RetryAfterFailure)
	}
	lastScope := s.lastScope
	s.mu.Unlock()
	for _, l := range launches {
		if l.State != pigtail.LaunchFailed || l.SubjectID == "" {
			continue
		}
		at := l.UpdatedAt
		if at.IsZero() {
			at = l.StartedAt
		}
		if until := at.Add(RetryAfterFailure); until.After(cooldown[l.SubjectID]) {
			cooldown[l.SubjectID] = until
		}
	}

	w.launches = launches
	w.in = Inputs{Now: now, Peers: live, AllPeers: all, Waits: waits, Pauses: pauses, Launches: launches,
		OpenWaits: openWaits, Queues: queues, Cooldown: cooldown, Margin: margin, Tokens: tokens, LastScope: lastScope}
	return w, nil
}

// Tour runs one tour and returns its decision; false when no tour ran (off).
func (s *Scheduler) Tour(ctx context.Context) (pigtail.StaffingDecision, bool) {
	s.tourMu.Lock()
	defer s.tourMu.Unlock()
	s.mu.Lock()
	set := s.settings
	s.mu.Unlock()
	if set.Mode == pigtail.StaffingOff {
		return pigtail.StaffingDecision{}, false
	}
	now := s.d.Now()
	dec := pigtail.StaffingDecision{TS: now, LastAt: now, Mode: set.Mode, Ceiling: set.Ceiling, Outcome: pigtail.StaffingOutcomeIdle}

	w, err := s.read(now)
	if err != nil {
		dec.Gate, dec.Reason = pigtail.StaffingGateUnreadable, err.Error()
		return s.finish(dec), true
	}
	s.journalLaunches(w, now, set)

	plan := Compute(w.in)
	dec.PlacesUsed, dec.Skipped = len(plan.Places), plan.Skipped
	switch {
	case dec.PlacesUsed >= set.Ceiling:
		dec.Gate = pigtail.StaffingGateNoFreePlace
		return s.finish(dec), true
	case plan.Pick == nil:
		dec.Gate = plan.Gate
		switch plan.Gate {
		case pigtail.StaffingGateOverMargin:
			dec.Reason = skipReason(plan.Skipped, pigtail.StaffingSkipOverMargin)
		case pigtail.StaffingGateOverBudget:
			dec.Reason = skipReason(plan.Skipped, pigtail.StaffingSkipOverBudget)
		}
		return s.finish(dec), true
	}
	pick := *plan.Pick
	dec.SubjectID, dec.ShortTitle, dec.Scope = pick.SubjectID, pick.ShortTitle, pick.Scope
	if gate, reason := s.gates(); gate != "" {
		dec.Gate, dec.Reason = gate, reason
		return s.finish(dec), true
	}
	if set.Mode == pigtail.StaffingAdvise {
		dec.Outcome = pigtail.StaffingOutcomeWouldLaunch
		dec.Reason = fmt.Sprintf("would launch %s (%s)", pick.SubjectID, pick.ShortTitle)
		return s.finish(dec), true
	}

	// launch: only the scheduler asks the budget for a place.
	lease, admitted, reason := "", false, "session budget not wired"
	if s.d.Budget.Admit != nil {
		lease, admitted, reason = s.d.Budget.Admit(pick.Scope)
	}
	if !admitted {
		dec.Gate, dec.Reason = pigtail.StaffingGateSessionBudgetFull, reason
		return s.finish(dec), true
	}
	got, err := s.d.Launch(ctx, w.subjects[pick.SubjectID].Subject)
	if err == nil && got.Deduplicated {
		err = fmt.Errorf("launch %s already in flight on the subject", got.LaunchID)
	}
	if err != nil {
		if s.d.Budget.Release != nil {
			s.d.Budget.Release(lease)
		}
		dec.Outcome, dec.Cause = pigtail.StaffingOutcomeLaunchRefused, launchCause(err)
		s.mu.Lock()
		s.noteRefused(pick.SubjectID, now)
		s.mu.Unlock()
		return s.finish(dec), true
	}
	if s.d.Budget.MarkLaunched != nil {
		s.d.Budget.MarkLaunched(lease)
	}
	dec.Outcome, dec.LaunchID = pigtail.StaffingOutcomeLaunchStarted, got.LaunchID
	dec.Reason = fmt.Sprintf("launched %s (%s)", pick.SubjectID, pick.ShortTitle)
	s.mu.Lock()
	s.own[got.LaunchID] = true
	s.started[got.LaunchID] = startedLaunch{pick.SubjectID, pick.ShortTitle, pick.Scope}
	s.lastScope = pick.Scope
	s.mu.Unlock()
	return s.finish(dec), true
}

// gates returns the first closed gate, in order: maintenance, session budget,
// machine, burst. Fail-safe: a gate it cannot read is closed.
func (s *Scheduler) gates() (gate, reason string) {
	d := &s.d
	if d.Maintenance != nil {
		on, why, err := d.Maintenance()
		if err != nil {
			return pigtail.StaffingGateMaintenance, "maintenance unreadable: " + err.Error()
		}
		if on {
			return pigtail.StaffingGateMaintenance, why
		}
	}
	if d.Budget.Check == nil {
		return pigtail.StaffingGateSessionBudgetOff, "session budget not wired"
	}
	enabled, admitted, why := d.Budget.Check()
	if !enabled {
		return pigtail.StaffingGateSessionBudgetOff, "session budget off: pigtail setup --session-budget"
	}
	if !admitted {
		return pigtail.StaffingGateSessionBudgetFull, why
	}
	if d.Machine != nil {
		if ok, why := d.Machine(); !ok {
			return pigtail.StaffingGateMachineBusy, why
		}
	}
	if d.Burst != nil {
		b, err := d.Burst()
		if err != nil {
			return pigtail.StaffingGateUnreadable, "burst unreadable: " + err.Error()
		}
		if b.Full() {
			return pigtail.StaffingGateBurst, fmt.Sprintf("%d launches in %s, frees at %s",
				b.Recent, sessionlaunch.BurstWindow, b.FreesAt().Local().Format("15:04"))
		}
	}
	return "", ""
}

// skipReason is the text of the first candidate skipped for skip.
func skipReason(skipped []pigtail.StaffingCandidate, skip string) string {
	for _, c := range skipped {
		if c.Skip == skip {
			return c.Detail
		}
	}
	return ""
}

// launchCause is the message of a launcher error, with its contract code.
func launchCause(err error) string {
	var r *launch.Refusal
	if errors.As(err, &r) {
		return string(r.Code) + ": " + r.Msg
	}
	var om *launchmargin.OverMarginError
	if errors.As(err, &om) {
		return om.APIMessage()
	}
	return err.Error() // the budget refusals' Error() is their APIMessage
}

// journalLaunches writes the outcomes of the scheduler's launches that
// closed, then the launches of other origins seen for the first time.
func (s *Scheduler) journalLaunches(w reading, now time.Time, set Settings) {
	var out []pigtail.StaffingDecision
	s.mu.Lock()
	for _, l := range w.launches {
		if st, open := s.started[l.LaunchID]; open {
			if l.State == pigtail.LaunchPending {
				continue
			}
			d := pigtail.StaffingDecision{TS: now, LastAt: now, Mode: set.Mode, Ceiling: set.Ceiling,
				Outcome: pigtail.StaffingOutcomeLaunched, SubjectID: st.subjectID, ShortTitle: st.shortTitle, Scope: st.scope, LaunchID: l.LaunchID}
			if l.State == pigtail.LaunchFailed {
				d.Outcome, d.Cause = pigtail.StaffingOutcomeLaunchFailed, l.Cause
				s.noteRefused(st.subjectID, now)
			}
			delete(s.started, l.LaunchID)
			out = append(out, d)
			continue
		}
		if s.own[l.LaunchID] || s.observed[l.LaunchID] || now.Sub(l.StartedAt) > ObserveWindow {
			continue
		}
		s.observed[l.LaunchID] = true
		d := pigtail.StaffingDecision{TS: now, LastAt: now, Mode: set.Mode, Ceiling: set.Ceiling,
			Outcome: pigtail.StaffingOutcomeObservedLaunch, SubjectID: l.SubjectID, LaunchID: l.LaunchID,
			Origin: l.Origin, Reason: string(l.State)}
		if sub, ok := w.subjects[l.SubjectID]; ok {
			d.ShortTitle, d.Scope = pigtail.ShortTitle(sub.Subject), sub.Scope
		}
		out = append(out, d)
	}
	s.mu.Unlock()
	for _, d := range out {
		s.write(d)
	}
}

// sameTour: two tour lines the journal folds into one.
func sameTour(a, b pigtail.StaffingDecision) bool {
	if a.Mode != b.Mode || a.Outcome != b.Outcome || a.Gate != b.Gate || a.SubjectID != b.SubjectID ||
		a.PlacesUsed != b.PlacesUsed || a.Ceiling != b.Ceiling || len(a.Skipped) != len(b.Skipped) {
		return false
	}
	for i := range a.Skipped {
		x, y := a.Skipped[i], b.Skipped[i]
		if x.SubjectID != y.SubjectID || x.Skip != y.Skip || x.Detail != y.Detail {
			return false
		}
	}
	return true
}

// finish journals the tour's decision, records it and returns it as written.
func (s *Scheduler) finish(dec pigtail.StaffingDecision) pigtail.StaffingDecision {
	dec = s.write(dec)
	s.mu.Lock()
	dd := dec
	s.last = &dd
	at := dec.LastAt
	s.checkedAt = &at
	s.mu.Unlock()
	if s.d.AfterTour != nil {
		s.d.AfterTour(dec)
	}
	return dec
}

// write journals one line: a tour line identical to the last one, still the
// file's last line, is folded into it (Repeats+1, LastAt, no event); every
// other line is appended and published.
func (s *Scheduler) write(dec pigtail.StaffingDecision) pigtail.StaffingDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isTourLine(dec.Outcome) && s.tourIsLast && s.lastTour != nil && sameTour(*s.lastTour, dec) {
		folded := *s.lastTour
		folded.Repeats++
		folded.LastAt = dec.TS
		err := s.log.ReplaceLast(folded)
		if err == nil {
			s.lastTour = &folded
			return folded
		}
		s.d.Logf("staffing: rewrite the last journal line: %v (appending)", err)
	}
	if err := s.log.Append(dec); err != nil {
		s.d.Logf("staffing: journal a decision: %v", err)
	}
	if isTourLine(dec.Outcome) {
		dd := dec
		s.lastTour, s.tourIsLast = &dd, true
	} else {
		s.tourIsLast = false
	}
	s.d.Publish(pigtail.Event{Type: pigtail.EventStaffingDecision, Timestamp: dec.TS, Data: decisionData(dec)})
	return dec
}

func decisionData(d pigtail.StaffingDecision) map[string]interface{} {
	b, err := json.Marshal(d)
	if err != nil {
		return map[string]interface{}{"outcome": d.Outcome}
	}
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	return m
}

// launcherFor: who launches the next subject (D5: the Pilot reads it).
func launcherFor(m pigtail.StaffingMode) string {
	if m == pigtail.StaffingLaunch {
		return pigtail.StaffingLauncherScheduler
	}
	return pigtail.StaffingLauncherPilot
}

// State reads the settings, the places, the queues (cut to QueueShown) and
// the last decision. The world is read again, apart from the tour.
func (s *Scheduler) State() (pigtail.StaffingState, error) {
	s.mu.Lock()
	st := pigtail.StaffingState{Mode: s.settings.Mode, Ceiling: s.settings.Ceiling, Launcher: launcherFor(s.settings.Mode),
		SettingsError: s.settingsErr, Places: []pigtail.StaffingPlace{}, Queues: map[string][]pigtail.StaffingCandidate{}}
	if s.last != nil {
		l := *s.last
		st.Last = &l
	}
	if s.checkedAt != nil {
		c := *s.checkedAt
		st.CheckedAt = &c
	}
	s.mu.Unlock()
	w, err := s.read(s.d.Now())
	if err != nil {
		return st, err
	}
	plan := Compute(w.in)
	st.Places, st.PlacesUsed = plan.Places, len(plan.Places)
	scopes := make([]string, 0, len(plan.Queues))
	for sc := range plan.Queues {
		scopes = append(scopes, sc)
	}
	sort.Strings(scopes)
	for _, sc := range scopes {
		q := plan.Queues[sc]
		if len(q) > QueueShown {
			q = q[:QueueShown]
		}
		st.Queues[sc] = q
	}
	return st, nil
}

// Update validates and writes new settings, journals settings_changed and
// asks for a tour. Nothing is written when the settings are refused.
func (s *Scheduler) Update(in pigtail.StaffingSettings) (pigtail.StaffingState, error) {
	s.mu.Lock()
	prev := s.settings
	next := prev
	if in.Mode != "" {
		next.Mode = in.Mode
	}
	if in.Ceiling != nil {
		next.Ceiling = *in.Ceiling
	}
	next.UpdatedAt = s.d.Now()
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return pigtail.StaffingState{}, err
	}
	if err := SaveSettings(s.d.SettingsPath, next); err != nil {
		s.mu.Unlock()
		return pigtail.StaffingState{}, err
	}
	s.settings, s.settingsErr = next, ""
	s.mu.Unlock()
	s.write(pigtail.StaffingDecision{TS: next.UpdatedAt, LastAt: next.UpdatedAt, Mode: next.Mode, Ceiling: next.Ceiling,
		Outcome: pigtail.StaffingOutcomeSettingsChanged,
		Reason:  fmt.Sprintf("mode %s → %s, ceiling %d → %d", prev.Mode, next.Mode, prev.Ceiling, next.Ceiling)})
	s.Kick()
	return s.State()
}

// Decisions reads the journal: the lines at or after since, oldest first,
// limit > 0 keeping the last ones.
func (s *Scheduler) Decisions(since time.Time, limit int) ([]pigtail.StaffingDecision, error) {
	return s.log.Read(since, limit)
}
