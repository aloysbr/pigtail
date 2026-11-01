package staffing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sessionlaunch"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// world is the fake store, launcher, clock and gates of a scheduler test.
type world struct {
	t   *testing.T
	dir string

	mu       sync.Mutex
	now      time.Time
	queues   map[string][]store.RankedSubject
	peers    []pigtail.PeerSession
	waits    map[string][]pigtail.SessionWait
	pauses   []pigtail.SessionPause
	records  []store.WaitRecord
	launches []pigtail.SessionLaunch
	peersErr error

	calls         []string // subjects Launch was called on
	launchErr     error
	launchPidless bool // the launched session registers without a pid
	launchListed  bool // the launch is added to the registry (pending)
	seq           int
	nativeStatus  map[int]string // fake native status by pid; absent = busy
	nativeGone    map[int]bool   // native file unreadable

	maintOn   bool
	maintErr  error
	budgetOn  bool
	checkOK   bool
	admitOK   bool
	released  []string
	marked    []string
	machine   string
	burst     int
	margin    map[string]string
	tokens    map[string]string
	tokenAsks int
	events    []pigtail.Event
}

func newWorld(t *testing.T) *world {
	return &world{t: t, dir: t.TempDir(), now: t0, queues: map[string][]store.RankedSubject{},
		waits: map[string][]pigtail.SessionWait{}, budgetOn: true, checkOK: true, admitOK: true, margin: map[string]string{}}
}

func (w *world) RankedSubjects(sc scope.Scope) ([]store.RankedSubject, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]store.RankedSubject(nil), w.queues[string(sc)]...), nil
}

func (w *world) ListPeers(includeEnded bool) ([]pigtail.PeerSession, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.peersErr != nil {
		return nil, w.peersErr
	}
	var out []pigtail.PeerSession
	for _, p := range w.peers {
		if includeEnded || p.EndedAt == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func (w *world) ListSessionWaits() (map[string][]pigtail.SessionWait, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string][]pigtail.SessionWait{}
	for k, v := range w.waits {
		out[k] = v
	}
	return out, nil
}

func (w *world) ListPauses() ([]pigtail.SessionPause, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]pigtail.SessionPause(nil), w.pauses...), nil
}

func (w *world) ListOpenWaitRecords(f store.WaitRecordFilter) ([]store.WaitRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f.Scope != scope.Any || !f.IncludeSnoozed {
		w.t.Errorf("open waits read with %+v, want every scope, snoozed included", f)
	}
	return append([]store.WaitRecord(nil), w.records...), nil
}

func (w *world) add(sc string, subs ...store.RankedSubject) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range subs {
		w.queues[sc] = append(w.queues[sc], inScope(s, sc))
	}
	// The real store answers in rank order, unset ranks last.
	q := w.queues[sc]
	sort.SliceStable(q, func(i, j int) bool {
		ri, rj := q[i].Rank, q[j].Rank
		if (ri == 0) != (rj == 0) {
			return rj == 0
		}
		return ri < rj
	})
}

// setNative sets the native status of the session running at pid.
func (w *world) setNative(pid int, status string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.nativeStatus == nil {
		w.nativeStatus = map[int]string{}
	}
	w.nativeStatus[pid] = status
}

func (w *world) native(_ string, pid int) (claudesession.Native, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.nativeGone[pid] {
		return claudesession.Native{}, false
	}
	for _, p := range w.peers {
		if p.PID == pid {
			st := "busy"
			if v, ok := w.nativeStatus[pid]; ok {
				st = v
			}
			return claudesession.Native{PID: pid, SessionID: p.SessionID, Status: st}, true
		}
	}
	return claudesession.Native{}, false
}

func (w *world) addPeer(p pigtail.PeerSession) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.peers = append(w.peers, p)
}

func (w *world) advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = w.now.Add(d)
}

func (w *world) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

func (w *world) deps() Deps {
	return Deps{
		Store: w,
		Launch: func(_ context.Context, sub pigtail.Subject) (pigtail.SessionLaunch, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.calls = append(w.calls, sub.ID)
			if w.launchErr != nil {
				return pigtail.SessionLaunch{}, w.launchErr
			}
			w.seq++
			id := fmt.Sprintf("ln_%d", w.seq)
			p := livePeer("ps_"+id, sub.ID)
			if w.launchPidless {
				p.PID = 0
			}
			w.peers = append(w.peers, p)
			l := pigtail.SessionLaunch{LaunchID: id, SubjectID: sub.ID, State: pigtail.LaunchPending, Origin: launch.Origin, StartedAt: w.now, UpdatedAt: w.now}
			if w.launchListed {
				w.launches = append(w.launches, l)
			}
			return l, nil
		},
		Launches: func() ([]pigtail.SessionLaunch, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return append([]pigtail.SessionLaunch(nil), w.launches...), nil
		},
		Burst: func() (sessionlaunch.Burst, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return sessionlaunch.Burst{Recent: w.burst}, nil
		},
		Budget: Budget{
			Check: func() (bool, bool, string) {
				w.mu.Lock()
				defer w.mu.Unlock()
				return w.budgetOn, w.checkOK, "budget full (20/20)"
			},
			Admit: func(string) (string, bool, string) {
				w.mu.Lock()
				defer w.mu.Unlock()
				return "lease_1", w.admitOK, "budget full (race)"
			},
			Release: func(l string) {
				w.mu.Lock()
				defer w.mu.Unlock()
				w.released = append(w.released, l)
			},
			MarkLaunched: func(l string) {
				w.mu.Lock()
				defer w.mu.Unlock()
				w.marked = append(w.marked, l)
			},
		},
		Machine: func() (bool, string) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.machine == "", w.machine
		},
		Maintenance: func() (bool, string, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.maintOn, "staffing test", w.maintErr
		},
		Margin: func(sc string) (bool, string) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.margin[sc] == "", w.margin[sc]
		},
		TokenBudget: func(sc string) (bool, string) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.tokenAsks++
			return w.tokens[sc] == "", w.tokens[sc]
		},
		Scopes: func() []string { return []string{tPerso, tWork} },
		Alive:  func(p pigtail.PeerSession) bool { return p.EndedAt == nil },
		Native: w.native,
		Publish: func(e pigtail.Event) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.events = append(w.events, e)
		},
		SettingsPath: filepath.Join(w.dir, "staffing.json"),
		LogPath:      filepath.Join(w.dir, "staffing-decisions.jsonl"),
		Every:        time.Hour,
		Now: func() time.Time {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.now
		},
		Logf: w.t.Logf,
	}
}

// sched writes the settings, then builds a scheduler on them.
func (w *world) sched(mode pigtail.StaffingMode, ceiling int) *Scheduler {
	w.t.Helper()
	if err := SaveSettings(filepath.Join(w.dir, "staffing.json"), Settings{Mode: mode, Ceiling: ceiling}); err != nil {
		w.t.Fatal(err)
	}
	return New(w.deps())
}

func tour(t *testing.T, s *Scheduler) pigtail.StaffingDecision {
	t.Helper()
	d, ok := s.Tour(context.Background())
	if !ok {
		t.Fatal("Tour: no tour")
	}
	return d
}

func lines(t *testing.T, s *Scheduler) []pigtail.StaffingDecision {
	t.Helper()
	ds, err := s.Decisions(time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func countOutcome(ds []pigtail.StaffingDecision, outcome string) int {
	n := 0
	for _, d := range ds {
		if d.Outcome == outcome {
			n++
		}
	}
	return n
}

func skippedAs(d pigtail.StaffingDecision, id string) string {
	for _, c := range d.Skipped {
		if c.SubjectID == id {
			return c.Skip
		}
	}
	return ""
}

func TestSchedulerDefaultIsAdvise(t *testing.T) {
	w := newWorld(t)
	s := New(w.deps())
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != pigtail.StaffingAdvise || st.Ceiling != 3 || st.Launcher != pigtail.StaffingLauncherPilot {
		t.Fatalf("State = %+v, want advise/3/pilot", st)
	}
}

func TestTourAdviseNeverLaunches(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s1", 1, pigtail.StatusBuilding))
	s := w.sched(pigtail.StaffingAdvise, 3)
	d := tour(t, s)
	if w.callCount() != 0 {
		t.Fatalf("Launch called %d times in advise", w.callCount())
	}
	if d.Outcome != pigtail.StaffingOutcomeWouldLaunch || !strings.Contains(d.Reason, "would launch") || !strings.Contains(d.Reason, "s1") {
		t.Fatalf("decision = %+v", d)
	}
}

func TestTourLaunchCapsAtCeiling(t *testing.T) {
	w := newWorld(t)
	for i := 1; i <= 5; i++ {
		w.add(tPerso, ranked(fmt.Sprintf("s%d", i), i, pigtail.StatusBuilding))
	}
	s := w.sched(pigtail.StaffingLaunch, 3)
	var last pigtail.StaffingDecision
	for i := 0; i < 6; i++ {
		last = tour(t, s)
		if i == 3 && (last.Outcome != pigtail.StaffingOutcomeIdle || last.Gate != pigtail.StaffingGateNoFreePlace) {
			t.Fatalf("4th tour = %+v, want idle no_free_place", last)
		}
	}
	if w.callCount() != 3 {
		t.Fatalf("Launch called %d times, want 3", w.callCount())
	}
	if last.Gate != pigtail.StaffingGateNoFreePlace {
		t.Fatalf("last tour = %+v", last)
	}
}

func TestTourCeilingLoweredLaunchesNothing(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s1", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding), ranked("s3", 3, pigtail.StatusBuilding))
	w.addPeer(livePeer("p1", "s1"))
	w.addPeer(livePeer("p2", "s2"))
	s := w.sched(pigtail.StaffingLaunch, 3)
	two := 2
	if _, err := s.Update(pigtail.StaffingSettings{Ceiling: &two}); err != nil {
		t.Fatal(err)
	}
	d := tour(t, s)
	if d.Outcome != pigtail.StaffingOutcomeIdle || d.Gate != pigtail.StaffingGateNoFreePlace || d.PlacesUsed != 2 || d.Ceiling != 2 {
		t.Fatalf("decision = %+v, want idle no_free_place 2/2", d)
	}
	if w.callCount() != 0 {
		t.Fatalf("Launch called %d times", w.callCount())
	}
}

func TestTourDoneFreesPlaceAndLaunchesNext(t *testing.T) {
	w := newWorld(t)
	for i := 1; i <= 4; i++ {
		w.add(tPerso, ranked(fmt.Sprintf("s%d", i), i, pigtail.StatusBuilding))
	}
	s := w.sched(pigtail.StaffingLaunch, 3)
	for i := 0; i < 4; i++ {
		tour(t, s)
	}
	if w.callCount() != 3 {
		t.Fatalf("Launch called %d times, want 3", w.callCount())
	}
	w.mu.Lock()
	w.waits["ps_ln_1"] = []pigtail.SessionWait{{Type: "done", Source: "declared", Since: w.now}}
	w.mu.Unlock()
	d := tour(t, s)
	if d.Outcome != pigtail.StaffingOutcomeLaunchStarted || d.SubjectID != "s4" {
		t.Fatalf("decision = %+v, want launch_started s4", d)
	}
}

func TestTourFailedLaunchCooldown(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.launches = []pigtail.SessionLaunch{{LaunchID: "ln_f", SubjectID: "s", State: pigtail.LaunchFailed, Origin: "api",
		StartedAt: t0.Add(-2 * time.Minute), UpdatedAt: t0.Add(-time.Minute)}}
	s := w.sched(pigtail.StaffingAdvise, 3)
	d := tour(t, s)
	if d.Outcome != pigtail.StaffingOutcomeIdle || skippedAs(d, "s") != pigtail.StaffingSkipCooldown || d.PlacesUsed != 0 {
		t.Fatalf("decision = %+v, want s in cooldown, no place", d)
	}
	w.advance(16 * time.Minute)
	if d := tour(t, s); d.Outcome != pigtail.StaffingOutcomeWouldLaunch || d.SubjectID != "s" {
		t.Fatalf("after 16 min = %+v, want would_launch s", d)
	}
}

func TestTourRefusalCooldownNoLoop(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.launchErr = &launch.Refusal{Code: pigtail.ErrSessionLive, Status: http.StatusConflict, Msg: "a live session holds s"}
	s := w.sched(pigtail.StaffingLaunch, 3)
	d := tour(t, s)
	if d.Outcome != pigtail.StaffingOutcomeLaunchRefused || !strings.Contains(d.Cause, "a live session holds s") {
		t.Fatalf("decision = %+v, want launch_refused", d)
	}
	for i := 0; i < 10; i++ {
		w.advance(time.Minute)
		tour(t, s)
	}
	if w.callCount() != 1 {
		t.Fatalf("Launch called %d times, want 1", w.callCount())
	}
	if n := countOutcome(lines(t, s), pigtail.StaffingOutcomeLaunchRefused); n != 1 {
		t.Fatalf("%d launch_refused lines, want 1", n)
	}
}

func TestTourGatesClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*world)
		gate string
	}{
		{"maintenance", func(w *world) { w.maintOn = true }, pigtail.StaffingGateMaintenance},
		{"maintenance unreadable", func(w *world) { w.maintErr = errors.New("cannot read") }, pigtail.StaffingGateMaintenance},
		{"budget off", func(w *world) { w.budgetOn = false }, pigtail.StaffingGateSessionBudgetOff},
		{"budget full", func(w *world) { w.checkOK = false }, pigtail.StaffingGateSessionBudgetFull},
		{"machine", func(w *world) { w.machine = "cpu: 1 test run(s) waiting for a slot" }, pigtail.StaffingGateMachineBusy},
		{"burst", func(w *world) { w.burst = sessionlaunch.BurstMax }, pigtail.StaffingGateBurst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
			tc.set(w)
			s := w.sched(pigtail.StaffingLaunch, 3)
			d := tour(t, s)
			if d.Outcome != pigtail.StaffingOutcomeIdle || d.Gate != tc.gate || d.SubjectID != "s" {
				t.Fatalf("decision = %+v, want idle %s on s", d, tc.gate)
			}
			if w.callCount() != 0 {
				t.Fatalf("Launch called %d times", w.callCount())
			}
		})
	}
}

func TestTourBudgetFullDoesNotLaunch(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		on, checkOK, admitOK bool
		gate                 string
	}{
		{"admit refuses after check (race)", true, true, false, pigtail.StaffingGateSessionBudgetFull},
		{"check refuses", true, false, true, pigtail.StaffingGateSessionBudgetFull},
		{"budget off", false, true, true, pigtail.StaffingGateSessionBudgetOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
			w.budgetOn, w.checkOK, w.admitOK = tc.on, tc.checkOK, tc.admitOK
			s := w.sched(pigtail.StaffingLaunch, 3)
			d := tour(t, s)
			if d.Outcome != pigtail.StaffingOutcomeIdle || d.Gate != tc.gate || d.SubjectID != "s" {
				t.Fatalf("decision = %+v, want idle %s on s", d, tc.gate)
			}
			if w.callCount() != 0 {
				t.Fatalf("Launch called %d times", w.callCount())
			}
			if n := countOutcome(lines(t, s), pigtail.StaffingOutcomeIdle); n != 1 {
				t.Fatalf("%d idle lines, want 1", n)
			}
		})
	}
}

func TestTourBudgetLeaseReleasedOrMarked(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.launchErr = errors.New("boom")
	s := w.sched(pigtail.StaffingLaunch, 3)
	tour(t, s)
	if len(w.released) != 1 || w.released[0] != "lease_1" || len(w.marked) != 0 {
		t.Fatalf("failed launch: released %v, marked %v", w.released, w.marked)
	}

	w2 := newWorld(t)
	w2.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	s2 := w2.sched(pigtail.StaffingLaunch, 3)
	tour(t, s2)
	if len(w2.marked) != 1 || w2.marked[0] != "lease_1" || len(w2.released) != 0 {
		t.Fatalf("launch: released %v, marked %v", w2.released, w2.marked)
	}
}

func TestTourPausedSessionTakesNoPlace(t *testing.T) {
	ended := t0
	for _, tc := range []struct {
		state string
		alive bool
	}{{pigtail.PausePaused, false}, {pigtail.PauseStopping, true}} {
		t.Run(tc.state, func(t *testing.T) {
			w := newWorld(t)
			w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding))
			w.addPeer(livePeer("p_other", "other"))
			p := livePeer("p_s", "s")
			if !tc.alive {
				p.EndedAt = &ended
			}
			w.addPeer(p)
			w.pauses = []pigtail.SessionPause{{SessionID: "p_s", State: tc.state}}
			s := w.sched(pigtail.StaffingAdvise, 3)
			d := tour(t, s)
			if skippedAs(d, "s") != pigtail.StaffingSkipPaused || d.SubjectID != "s2" || d.PlacesUsed != 1 {
				t.Fatalf("decision = %+v, want s paused, s2 picked, 1 place", d)
			}
		})
	}
}

func TestTourPhantomPeerJournaledPhantom(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding))
	w.addPeer(pigtail.PeerSession{SessionID: "ph", SubjectID: "s", Scope: tPerso})
	s := w.sched(pigtail.StaffingLaunch, 3)
	d := tour(t, s)
	if d.PlacesUsed != 1 || skippedAs(d, "s") != pigtail.StaffingSkipPhantom {
		t.Fatalf("decision = %+v, want 1 place and s phantom", d)
	}
	ds := lines(t, s)
	if len(ds) != 1 || skippedAs(ds[0], "s") != pigtail.StaffingSkipPhantom {
		t.Fatalf("written = %+v", ds)
	}
	for _, c := range w.calls {
		if c == "s" {
			t.Fatal("Launch called on the phantom's subject")
		}
	}
}

func TestTourConfirmedPidlessSessionStillCounted(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s1", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding))
	w.launchPidless, w.launchListed = true, true
	s := w.sched(pigtail.StaffingLaunch, 1)
	if d := tour(t, s); d.Outcome != pigtail.StaffingOutcomeLaunchStarted {
		t.Fatalf("first tour = %+v", d)
	}
	w.mu.Lock()
	w.launches[0].State = pigtail.LaunchLaunched
	w.mu.Unlock()
	for i := 0; i < 3; i++ {
		d := tour(t, s)
		if d.Gate != pigtail.StaffingGateNoFreePlace || d.PlacesUsed != 1 {
			t.Fatalf("tour %d = %+v, want no_free_place 1/1", i, d)
		}
	}
	if w.callCount() != 1 {
		t.Fatalf("Launch called %d times, want 1", w.callCount())
	}
}

func TestTourMachineQueuesCloseGate(t *testing.T) {
	for _, reason := range []string{"cpu: 1 test run(s) waiting for a slot", "memory admission: 1 command(s) queued"} {
		w := newWorld(t)
		w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
		w.machine = reason
		s := w.sched(pigtail.StaffingLaunch, 3)
		d := tour(t, s)
		if d.Gate != pigtail.StaffingGateMachineBusy || d.Reason != reason || w.callCount() != 0 {
			t.Fatalf("decision = %+v, calls %d", d, w.callCount())
		}
	}
}

func TestTourOverMarginLogsReason(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.margin[tPerso] = "every perso cell is over its margin"
	s := w.sched(pigtail.StaffingLaunch, 3)
	d := tour(t, s)
	if d.Outcome != pigtail.StaffingOutcomeIdle || d.Gate != pigtail.StaffingGateOverMargin {
		t.Fatalf("decision = %+v, want idle over_margin", d)
	}
	if len(d.Skipped) != 1 || d.Skipped[0].Skip != pigtail.StaffingSkipOverMargin || d.Skipped[0].Detail != w.margin[tPerso] {
		t.Fatalf("Skipped = %+v", d.Skipped)
	}
}

func TestTourUnreadableWorldLaunchesNothing(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.peersErr = errors.New("db locked")
	s := w.sched(pigtail.StaffingLaunch, 3)
	d := tour(t, s)
	if d.Outcome != pigtail.StaffingOutcomeIdle || d.Gate != pigtail.StaffingGateUnreadable || w.callCount() != 0 {
		t.Fatalf("decision = %+v, calls %d", d, w.callCount())
	}
}

func TestTourOffDoesNothing(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	s := w.sched(pigtail.StaffingOff, 3)
	if _, ok := s.Tour(context.Background()); ok {
		t.Fatal("Tour in off: a tour ran")
	}
	if n := len(lines(t, s)); n != 0 {
		t.Fatalf("%d lines written in off", n)
	}
}

func TestTourRepeatIncrementsRepeats(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 2, pigtail.StatusBuilding))
	s := w.sched(pigtail.StaffingAdvise, 3)
	for i := 0; i < 3; i++ {
		tour(t, s)
		if i < 2 {
			w.advance(time.Minute)
		}
	}
	ds := lines(t, s)
	if len(ds) != 1 || ds[0].Repeats != 2 || !ds[0].LastAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("lines = %+v, want one with repeats 2, last_at +2m", ds)
	}
	w.mu.Lock()
	evs := len(w.events)
	w.mu.Unlock()
	if evs != 1 {
		t.Fatalf("%d events, want 1 (a repeat publishes nothing)", evs)
	}
	w.add(tPerso, ranked("better", 1, pigtail.StatusBuilding))
	tour(t, s)
	if n := len(lines(t, s)); n != 2 {
		t.Fatalf("queue changed: %d lines, want 2", n)
	}
	// An observed launch between two identical tours: a new line, no rewrite.
	w.mu.Lock()
	w.launches = []pigtail.SessionLaunch{{LaunchID: "ln_o", SubjectID: "elsewhere", State: pigtail.LaunchFailed, Origin: "api", StartedAt: w.now, UpdatedAt: w.now}}
	w.mu.Unlock()
	tour(t, s)
	ds = lines(t, s)
	if len(ds) != 4 || ds[2].Outcome != pigtail.StaffingOutcomeObservedLaunch || ds[3].Repeats != 0 {
		t.Fatalf("lines = %+v, want idle, idle, observed_launch, a new tour line", ds)
	}
}

func TestTourRepeatsSurviveRestart(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	s := w.sched(pigtail.StaffingAdvise, 3)
	for i := 0; i < 3; i++ {
		tour(t, s)
	}
	s = New(w.deps())
	tour(t, s)
	ds := lines(t, s)
	if len(ds) != 1 || ds[0].Repeats != 3 {
		t.Fatalf("lines = %+v, want one with repeats 3", ds)
	}
}

func TestTourJournalsLaunchesOfOtherOrigins(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.launches = []pigtail.SessionLaunch{
		{LaunchID: "ln_api", SubjectID: "x", State: pigtail.LaunchPending, Origin: "api", StartedAt: t0.Add(-time.Minute), UpdatedAt: t0},
		{LaunchID: "ln_cli", SubjectID: "y", State: pigtail.LaunchLaunched, Origin: "cli", StartedAt: t0.Add(-5 * time.Minute), UpdatedAt: t0},
		{LaunchID: "ln_old", SubjectID: "z", State: pigtail.LaunchLaunched, Origin: "api", StartedAt: t0.Add(-2 * time.Hour), UpdatedAt: t0.Add(-2 * time.Hour)},
	}
	s := w.sched(pigtail.StaffingAdvise, 3)
	for i := 0; i < 3; i++ {
		tour(t, s)
	}
	s = New(w.deps())
	tour(t, s)
	var seen []string
	for _, d := range lines(t, s) {
		if d.Outcome == pigtail.StaffingOutcomeObservedLaunch {
			seen = append(seen, d.LaunchID+"/"+d.Origin+"/"+d.SubjectID)
		}
	}
	if strings.Join(seen, " ") != "ln_api/api/x ln_cli/cli/y" {
		t.Fatalf("observed = %v", seen)
	}

	// A launch the scheduler started is not observed.
	w2 := newWorld(t)
	w2.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w2.launchListed = true
	s2 := w2.sched(pigtail.StaffingLaunch, 3)
	tour(t, s2)
	tour(t, s2)
	if n := countOutcome(lines(t, s2), pigtail.StaffingOutcomeObservedLaunch); n != 0 {
		t.Fatalf("own launch observed %d times", n)
	}

	// In off, nothing.
	w3 := newWorld(t)
	w3.launches = w.launches
	s3 := w3.sched(pigtail.StaffingOff, 3)
	s3.Tour(context.Background())
	if n := len(lines(t, s3)); n != 0 {
		t.Fatalf("off: %d lines", n)
	}
}

func TestOpenWaitsUseAnswerableTypes(t *testing.T) {
	for typ, waits := range map[string]bool{"permission": false, "arbitrage": true, "question": true, "review": true, "blocked": true} {
		w := newWorld(t)
		w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
		w.records = []store.WaitRecord{{Wait: pigtail.Wait{ID: "w1", SubjectID: "s", Type: typ, State: "open", OpenedAt: t0}, Scope: tPerso}}
		s := w.sched(pigtail.StaffingAdvise, 3)
		d := tour(t, s)
		got := skippedAs(d, "s") == pigtail.StaffingSkipWaitsHuman
		if got != waits {
			t.Errorf("%s: waits_human = %v, want %v (decision %+v)", typ, got, waits, d)
		}
	}
}

func TestStateLauncherFollowsMode(t *testing.T) {
	for mode, want := range map[pigtail.StaffingMode]string{
		pigtail.StaffingLaunch: pigtail.StaffingLauncherScheduler,
		pigtail.StaffingAdvise: pigtail.StaffingLauncherPilot,
		pigtail.StaffingOff:    pigtail.StaffingLauncherPilot,
	} {
		w := newWorld(t)
		st, err := w.sched(mode, 3).State()
		if err != nil || st.Launcher != want {
			t.Errorf("%s: Launcher = %q, %v; want %q", mode, st.Launcher, err, want)
		}
	}
}

func TestTourLogsLaunchOutcome(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("a", 1, pigtail.StatusBuilding))
	w.launchListed = true
	s := w.sched(pigtail.StaffingLaunch, 3)
	if d := tour(t, s); d.Outcome != pigtail.StaffingOutcomeLaunchStarted || d.LaunchID != "ln_1" {
		t.Fatalf("tour 1 = %+v", d)
	}
	w.add(tPerso, ranked("b", 2, pigtail.StatusBuilding))
	w.mu.Lock()
	w.launches[0].State = pigtail.LaunchLaunched
	w.mu.Unlock()
	if d := tour(t, s); d.Outcome != pigtail.StaffingOutcomeLaunchStarted || d.LaunchID != "ln_2" {
		t.Fatalf("tour 2 = %+v", d)
	}
	w.mu.Lock()
	w.launches[1].State, w.launches[1].Cause = pigtail.LaunchFailed, "never registered"
	w.mu.Unlock()
	tour(t, s)
	tour(t, s)
	s = New(w.deps())
	tour(t, s)
	ds := lines(t, s)
	if countOutcome(ds, pigtail.StaffingOutcomeLaunched) != 1 || countOutcome(ds, pigtail.StaffingOutcomeLaunchFailed) != 1 {
		t.Fatalf("lines = %+v, want one launched and one launch_failed", ds)
	}
	for _, d := range ds {
		if d.Outcome == pigtail.StaffingOutcomeLaunchFailed && (d.LaunchID != "ln_2" || d.Cause != "never registered") {
			t.Fatalf("launch_failed = %+v", d)
		}
	}
}

func TestSchedulerRestartKeepsSettingsAndLog(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("a", 1, pigtail.StatusBuilding))
	w.launchErr = errors.New("cmux unreachable")
	s := New(w.deps())
	two := 2
	if _, err := s.Update(pigtail.StaffingSettings{Mode: pigtail.StaffingLaunch, Ceiling: &two}); err != nil {
		t.Fatal(err)
	}
	tour(t, s)
	w.advance(time.Minute)
	tour(t, s)
	s = New(w.deps())
	st, err := s.State()
	if err != nil || st.Mode != pigtail.StaffingLaunch || st.Ceiling != 2 {
		t.Fatalf("State = %+v, %v", st, err)
	}
	if n := len(lines(t, s)); n < 3 {
		t.Fatalf("%d lines after restart, want settings_changed, launch_refused and a tour", n)
	}
	w.advance(time.Minute)
	d := tour(t, s)
	if w.callCount() != 1 || skippedAs(d, "a") != pigtail.StaffingSkipCooldown {
		t.Fatalf("after restart: calls %d, decision %+v; want the refusal's cooldown kept", w.callCount(), d)
	}
}

func TestRunKickTriggersTour(t *testing.T) {
	w := newWorld(t)
	d := w.deps()
	toured := make(chan struct{}, 4)
	d.AfterTour = func(pigtail.StaffingDecision) { toured <- struct{}{} }
	s := New(d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-toured: // the tour at start
	case <-time.After(time.Second):
		t.Fatal("no tour at start")
	}
	s.Kick()
	select {
	case <-toured:
	case <-time.After(time.Second):
		t.Fatal("Kick: no tour within 1 s")
	}
}

func TestUpdateRejectsInvalid(t *testing.T) {
	w := newWorld(t)
	s := New(w.deps())
	bad := 21
	for _, in := range []pigtail.StaffingSettings{{Mode: "foo"}, {Ceiling: &bad}} {
		if _, err := s.Update(in); err == nil {
			t.Errorf("Update(%+v): no error", in)
		}
	}
	if _, err := os.Stat(filepath.Join(w.dir, "staffing.json")); !os.IsNotExist(err) {
		t.Fatalf("settings written: %v", err)
	}
	if n := len(lines(t, s)); n != 0 {
		t.Fatalf("%d lines written", n)
	}
}

func TestTourOverBudgetLogsReasonAndLaunchesNothing(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.tokens = map[string]string{tPerso: "every account of scope perso is out of its weekly token budget; first release at Mon 15:04"}
	s := w.sched(pigtail.StaffingLaunch, 3)
	d := tour(t, s)
	if d.Outcome != pigtail.StaffingOutcomeIdle || d.Gate != pigtail.StaffingGateOverBudget || d.Reason != w.tokens[tPerso] {
		t.Fatalf("decision = %+v, want idle over_budget", d)
	}
	if len(d.Skipped) != 1 || d.Skipped[0].Skip != pigtail.StaffingSkipOverBudget {
		t.Fatalf("Skipped = %+v", d.Skipped)
	}
	if w.callCount() != 0 {
		t.Fatalf("%d launches", w.callCount())
	}
	got := lines(t, s) // read back from the disk (acceptance 2)
	if len(got) == 0 || got[len(got)-1].Gate != pigtail.StaffingGateOverBudget || got[len(got)-1].Reason != w.tokens[tPerso] {
		t.Fatalf("journal = %+v", got)
	}
}

func TestTourUnmeasuredBudgetClosesGate(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.tokens = map[string]string{tPerso: (&launchmargin.UnmeasuredBudgetError{Why: "reading older than 30m"}).APIMessage()}
	s := w.sched(pigtail.StaffingLaunch, 3)
	d := tour(t, s)
	if d.Gate != pigtail.StaffingGateOverBudget || !strings.HasPrefix(d.Reason, "unmeasured: ") || w.callCount() != 0 {
		t.Fatalf("decision = %+v, calls %d", d, w.callCount())
	}
}

// An unmeasured scope on an aging reading is one journal line, not one a Tour.
func TestTourStaleReadingFoldsIntoOneLine(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	s := w.sched(pigtail.StaffingLaunch, 3)
	fetched := t0.Add(-31 * time.Minute)
	for i := 0; i < 2; i++ {
		at := t0.Add(time.Duration(i) * time.Minute)
		v := claudeusage.BudgetFor(&claudeusage.Reading{FetchedAt: fetched}, 70, at)
		w.tokens = map[string]string{tPerso: (&launchmargin.UnmeasuredBudgetError{Why: v.Why}).APIMessage()}
		if d := tour(t, s); d.Gate != pigtail.StaffingGateOverBudget {
			t.Fatalf("decision = %+v", d)
		}
		w.advance(time.Minute)
	}
	if ds := lines(t, s); len(ds) != 1 || ds[0].Repeats != 1 {
		t.Fatalf("lines = %+v, want one with repeats 1", ds)
	}
}

func TestTourTokenBudgetNotAskedWhenMarginClosed(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
	w.margin[tPerso] = "every perso cell is over its margin"
	w.tokens = map[string]string{tPerso: "must not be read"}
	s := w.sched(pigtail.StaffingLaunch, 3)
	if d := tour(t, s); d.Gate != pigtail.StaffingGateOverMargin {
		t.Fatalf("decision = %+v", d)
	}
	if w.tokenAsks != 0 {
		t.Fatalf("TokenBudget asked %d times with the margin closed", w.tokenAsks)
	}
}

// The race between the gate and the launch: the launcher's own refusal is
// journaled with the same text.
func TestTourLaunchRefusedOverBudgetCause(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&launchmargin.OverBudgetError{Scope: tPerso}, "every account of scope " + tPerso + " is out of its weekly token budget; reset time unknown"},
		{&launchmargin.UnmeasuredBudgetError{Why: "reading older than 30m"}, "unmeasured: reading older than 30m"},
	} {
		w := newWorld(t)
		w.add(tPerso, ranked("s", 1, pigtail.StatusBuilding))
		w.launchErr = tc.err
		s := w.sched(pigtail.StaffingLaunch, 3)
		d := tour(t, s)
		if d.Outcome != pigtail.StaffingOutcomeLaunchRefused || d.Cause != tc.want {
			t.Fatalf("decision = %+v, want launch_refused %q", d, tc.want)
		}
	}
}

// The stored status is frozen at SessionStart when the sidebar loop is off:
// the turn reads the native file. (The stored-busy + native-idle leg is the
// red one; stored-idle + native-busy guards a stored-status implementation.)
func TestTourReadsNativeStatusNotStored(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s1", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding))
	a := livePeer("pa", "s1")
	a.Status = "idle" // stored idle, native busy
	w.addPeer(a)
	s := w.sched(pigtail.StaffingAdvise, 3)
	if d := tour(t, s); d.PlacesUsed != 1 {
		t.Fatalf("stored idle + native busy: %+v, want 1 place", d)
	}

	w2 := newWorld(t)
	w2.add(tPerso, ranked("s1", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding))
	b := livePeer("pb", "s1") // stored busy, native idle
	w2.addPeer(b)
	w2.setNative(b.PID, "idle")
	s2 := w2.sched(pigtail.StaffingAdvise, 3)
	if d := tour(t, s2); d.PlacesUsed != 0 {
		t.Fatalf("stored busy + native idle: %+v, want 0 place", d)
	}

	// A native file of another session (pid reused) does not count as idle.
	w3 := newWorld(t)
	w3.add(tPerso, ranked("s1", 1, pigtail.StatusBuilding))
	c := livePeer("pc", "s1")
	w3.addPeer(c)
	w3.nativeGone = map[int]bool{c.PID: true}
	s3 := w3.sched(pigtail.StaffingAdvise, 3)
	if d := tour(t, s3); d.PlacesUsed != 1 {
		t.Fatalf("unreadable native: %+v, want 1 place (fail-safe)", d)
	}
}

// Ceiling 1, a launched session whose native status is idle at once: the
// grace (not the fail-safe) keeps its place, no second Launch.
func TestTourLaunchedIdleSessionHeldByGrace(t *testing.T) {
	w := newWorld(t)
	w.add(tPerso, ranked("s1", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding))
	w.launchListed = true
	s := w.sched(pigtail.StaffingLaunch, 1)
	if d := tour(t, s); d.Outcome != pigtail.StaffingOutcomeLaunchStarted {
		t.Fatalf("first tour = %+v", d)
	}
	w.mu.Lock()
	w.launches[0].State = pigtail.LaunchLaunched
	pid := w.peers[0].PID
	w.mu.Unlock()
	w.setNative(pid, "idle")
	for i := 0; i < 3; i++ {
		d := tour(t, s)
		if d.Gate != pigtail.StaffingGateNoFreePlace || d.PlacesUsed != 1 {
			t.Fatalf("tour %d = %+v, want no_free_place 1/1", i, d)
		}
	}
	if w.callCount() != 1 {
		t.Fatalf("Launch called %d times, want 1", w.callCount())
	}
	// Past the grace the idle session frees its place, its subject stays held.
	w.advance(LaunchGrace + time.Minute)
	d := tour(t, s)
	if d.PlacesUsed != 0 {
		t.Fatalf("after grace = %+v, want 0 places", d)
	}
	if w.callCount() != 2 || w.calls[1] != "s2" {
		t.Fatalf("calls = %v, want a second Launch on s2 only", w.calls)
	}
}
