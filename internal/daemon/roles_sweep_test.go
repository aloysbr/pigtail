package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var rsT0 = time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)

func rsTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func rsArm(at, due time.Time) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"%s","toolUseResult":{"scheduledFor":%d}}`, rsTS(at), due.UnixMilli())
}
func rsUser(at time.Time) string { return fmt.Sprintf(`{"type":"user","timestamp":"%s"}`, rsTS(at)) }
func rsAsst(at time.Time) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"%s"}`, rsTS(at))
}

// sweepRoleRig drives a rolesSweep on a real in-memory store and real
// transcripts with a fake clock: every write to a transcript is Chtimes'd to
// the fake time, so ActivityMtime, stuck and the reader memo follow it.
type sweepRoleRig struct {
	t      *testing.T
	st     *store.Store
	cfg    string
	clock  time.Time
	start  time.Time
	sw     *rolesSweep
	col    *collector
	logs   []string
	logsMu sync.Mutex
	native map[string]claudesession.Native
	alive  map[string]bool
	files  map[string]string
	ambig  map[string]bool
	pids   map[int]string
}

func newSweepRoleRig(t *testing.T, start time.Time) *sweepRoleRig {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &sweepRoleRig{t: t, st: st, cfg: t.TempDir(), clock: start, start: start,
		native: map[string]claudesession.Native{}, alive: map[string]bool{}, files: map[string]string{},
		ambig: map[string]bool{}, pids: map[int]string{}}
	r.newSweep()
	return r
}

// newSweep replaces the sweep and its loop reader by fresh ones on the same
// store: a daemon restart.
func (r *sweepRoleRig) newSweep() {
	r.col = &collector{
		loops: claudesession.NewLoopReader(),
		peers: r.st.PeersOfSubject,
		readNative: func(cfg string, pid int) (claudesession.Native, bool) {
			n, ok := r.native[r.pids[pid]]
			return n, ok
		},
		alive: func(cfg string, pid int, sid string) bool { return r.alive[sid] },
		find: func(sid string, cfgs []string) (claudesession.Transcript, bool, error) {
			if r.ambig[sid] {
				return claudesession.Transcript{}, false, fmt.Errorf("ambiguous %s", sid)
			}
			if r.files[sid] == "" {
				return claudesession.Transcript{}, false, nil
			}
			return claudesession.Transcript{ID: sid, ConfigDir: r.cfg, Slug: "slug"}, true, nil
		},
	}
	r.sw = newRolesSweep(r.st, r.col, time.Minute)
	r.sw.now = func() time.Time { return r.clock }
	r.sw.mono = func() time.Duration { return r.clock.Sub(r.start) }
	r.sw.logf = func(f string, a ...any) {
		r.logsMu.Lock()
		r.logs = append(r.logs, fmt.Sprintf(f, a...))
		r.logsMu.Unlock()
	}
}

func (r *sweepRoleRig) role(sc, name, subject string, created time.Time) pigtail.Role {
	ro := pigtail.Role{Scope: sc, Name: name, SubjectID: subject, Cadence: 30 * time.Minute, CreatedAt: created, UpdatedAt: created}
	if err := r.st.UpsertRole(&ro); err != nil {
		r.t.Fatal(err)
	}
	return ro
}

// session registers a peer of the subject; live sets its pid alive and idle.
func (r *sweepRoleRig) session(sid, subject string, pid int, live bool) {
	r.t.Helper()
	p := &pigtail.PeerSession{SessionID: sid, ConfigDir: r.cfg, PID: pid, Scope: "perso", SubjectID: subject, SubjectSet: true, UpdatedAt: r.clock}
	if err := r.st.UpsertPeer(p); err != nil {
		r.t.Fatal(err)
	}
	r.pids[pid] = sid
	r.setLive(sid, pid, subject, live)
}

func (r *sweepRoleRig) setLive(sid string, pid int, subject string, live bool) {
	r.alive[sid] = live
	if live {
		p := &pigtail.PeerSession{SessionID: sid, ConfigDir: r.cfg, PID: pid, Scope: "perso", SubjectID: subject, SubjectSet: true, UpdatedAt: r.clock}
		if err := r.st.UpsertPeer(p); err != nil {
			r.t.Fatal(err)
		}
		r.native[sid] = claudesession.Native{Status: "idle", StatusUpdatedAt: r.clock.UnixMilli()}
	} else if err := r.st.EndPeer(sid, r.clock); err != nil {
		r.t.Fatal(err)
	}
}

func (r *sweepRoleRig) status(sid, status string) {
	r.native[sid] = claudesession.Native{Status: status, StatusUpdatedAt: r.clock.UnixMilli()}
}

// write appends lines to the session's transcript and sets its mtime to at.
func (r *sweepRoleRig) write(sid string, at time.Time, lines ...string) {
	r.t.Helper()
	p := r.files[sid]
	if p == "" {
		dir := filepath.Join(r.cfg, "projects", "slug")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			r.t.Fatal(err)
		}
		p = filepath.Join(dir, sid+".jsonl")
		r.files[sid] = p
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		r.t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Chtimes(p, at, at); err != nil {
		r.t.Fatal(err)
	}
}

func (r *sweepRoleRig) tick(d time.Duration) {
	r.clock = r.clock.Add(d)
	r.sw.pass()
}

// run ticks every minute for d: a longer step reads as a sleep gap.
func (r *sweepRoleRig) run(d time.Duration) {
	for ; d >= time.Minute; d -= time.Minute {
		r.tick(time.Minute)
	}
}

func (r *sweepRoleRig) rows(kind string) []pigtail.JournalEntry {
	r.t.Helper()
	es, err := r.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: kind, Limit: 500})
	if err != nil {
		r.t.Fatal(err)
	}
	return es
}

func (r *sweepRoleRig) logCount(sub string) int {
	r.logsMu.Lock()
	defer r.logsMu.Unlock()
	n := 0
	for _, l := range r.logs {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// silentRig: role pilote (scope sc) whose wakeup, armed at t0, was due at t0+30m.
func silentRig(t *testing.T, sc string) *sweepRoleRig {
	r := newSweepRoleRig(t, rsT0)
	r.role(sc, "pilote", "sub-pilote", rsT0.Add(-time.Hour))
	r.session("sid-pilote", "sub-pilote", 100, true)
	r.write("sid-pilote", rsT0, rsArm(rsT0, rsT0.Add(30*time.Minute)))
	return r
}

func TestSweepRaisesOncePerEpisode(t *testing.T) {
	for _, sc := range []string{"perso", "globex"} { // a non-master scope too (V2)
		t.Run(sc, func(t *testing.T) {
			r := silentRig(t, sc)
			r.clock = rsT0.Add(10 * time.Minute)
			r.tick(31 * time.Minute) // 41m: past due + grace
			r.tick(time.Minute)
			r.tick(time.Minute)
			rows := r.rows("role-alert")
			if len(rows) != 1 {
				t.Fatalf("alert rows = %d: %+v", len(rows), rows)
			}
			e := rows[0]
			if want := roles.AlertID("pilote", rsT0.Add(30*time.Minute)); e.ID != want {
				t.Fatalf("id = %s, want %s", e.ID, want)
			}
			if e.Scope != sc || e.SubjectID != "sub-pilote" || !strings.Contains(e.Source, "state=silent") {
				t.Fatalf("row = %+v", e)
			}
		})
	}
}

func TestSweepHumanTurnDoesNotRecoverWakeupRole(t *testing.T) {
	r := silentRig(t, "perso")
	r.tick(41 * time.Minute)
	r.write("sid-pilote", r.clock, rsUser(r.clock), rsAsst(r.clock.Add(time.Second)))
	r.tick(time.Minute)
	r.tick(time.Minute)
	if n := len(r.rows("role-recovered")); n != 0 {
		t.Fatalf("recovered rows = %d", n)
	}
	if got := r.sw.Snapshot()[0].State; got != "silent" {
		t.Fatalf("state = %s", got)
	}
}

func TestSweepRecoveredOnRearm(t *testing.T) {
	r := silentRig(t, "perso")
	r.tick(41 * time.Minute)
	r.write("sid-pilote", r.clock, rsArm(r.clock, r.clock.Add(30*time.Minute)))
	r.tick(time.Minute)
	rec := r.rows("role-recovered")
	if len(rec) != 1 || rec[0].ID != roles.RecoveredID("pilote", rsT0.Add(30*time.Minute)) {
		t.Fatalf("recovered = %+v", rec)
	}
	open, err := r.st.OpenRoleAlerts(scope.Any)
	if err != nil || len(open) != 0 {
		t.Fatalf("open = %v, %v", open, err)
	}
}

func TestSweepNewEpisodeAfterRecovery(t *testing.T) {
	r := silentRig(t, "perso")
	r.tick(41 * time.Minute)
	r.write("sid-pilote", r.clock, rsArm(r.clock, r.clock.Add(30*time.Minute)))
	r.tick(time.Minute)
	r.run(45 * time.Minute) // second missed deadline + grace
	al := r.rows("role-alert")
	if len(al) != 2 || al[0].ID == al[1].ID {
		t.Fatalf("alerts = %+v", al)
	}
}

func TestSweepStateShiftKeepsOneAlert(t *testing.T) {
	r := silentRig(t, "perso")
	r.tick(41 * time.Minute) // silent
	r.setLive("sid-pilote", 100, "sub-pilote", false)
	r.tick(time.Minute) // absent
	r.setLive("sid-pilote", 100, "sub-pilote", true)
	r.status("sid-pilote", "busy")
	if err := os.Chtimes(r.files["sid-pilote"], r.clock.Add(-21*time.Minute), r.clock.Add(-21*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r.tick(time.Minute) // stuck
	if got := r.sw.Snapshot()[0].State; got != "stuck" {
		t.Fatalf("state = %s", got)
	}
	if n := len(r.rows("role-alert")); n != 1 {
		t.Fatalf("alert rows = %d", n)
	}
}

func TestSweepStandbyStillAlerts(t *testing.T) {
	r := silentRig(t, "perso")
	if err := r.st.EnterStandby("sid-pilote", rsT0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	r.tick(41 * time.Minute)
	if n := len(r.rows("role-alert")); n != 1 {
		t.Fatalf("alert rows = %d", n)
	}
}

func TestSweepHoldsAfterSleepGap(t *testing.T) {
	r := silentRig(t, "perso")
	r.tick(time.Minute)
	r.run(19 * time.Minute)  // 20m, a pass per minute
	r.tick(25 * time.Minute) // 45m: 25 min wall gap = a sleep; deadline + grace long past
	r.run(5 * time.Minute)   // 50m: held until 45m + 10m
	if n := len(r.rows("role-alert")) + len(r.rows("role-recovered")); n != 0 {
		t.Fatalf("rows during hold = %d", n)
	}
	if !r.sw.Snapshot()[0].Held {
		t.Fatal("snapshot not held")
	}
	r.run(6 * time.Minute) // 56m
	if n := len(r.rows("role-alert")); n != 1 {
		t.Fatalf("alert rows after hold = %d", n)
	}
}

func TestSweepFirstPassNotHeld(t *testing.T) {
	r := silentRig(t, "perso")
	r.clock = rsT0.Add(41 * time.Minute)
	r.sw.pass()
	if n := len(r.rows("role-alert")); n != 1 {
		t.Fatalf("alert rows = %d", n)
	}
	if r.sw.Snapshot()[0].Held {
		t.Fatal("first pass held")
	}
}

func TestSweepRestartNoDuplicateAlert(t *testing.T) {
	r := silentRig(t, "perso")
	r.tick(41 * time.Minute)
	r.newSweep()
	r.tick(time.Minute)
	r.tick(time.Minute)
	if n := len(r.rows("role-alert")); n != 1 {
		t.Fatalf("alert rows = %d", n)
	}
}

func TestSweepReadErrorIsUnknownLoggedOnce(t *testing.T) {
	r := silentRig(t, "perso")
	r.ambig["sid-pilote"] = true
	r.tick(41 * time.Minute)
	r.tick(time.Minute)
	if n := len(r.rows("role-alert")) + len(r.rows("role-recovered")); n != 0 {
		t.Fatalf("rows = %d", n)
	}
	if got := r.sw.Snapshot()[0]; got.State != "unknown" || !strings.Contains(got.Reason, "ambiguous") {
		t.Fatalf("status = %+v", got)
	}
	if n := r.logCount("unknown"); n != 1 {
		t.Fatalf("unknown log lines = %d: %v", n, r.logs)
	}
}

func TestSweepPanicIsUnknown(t *testing.T) {
	r := silentRig(t, "perso") // role pilote
	r.role("perso", "a", "sub-a", rsT0.Add(-time.Hour))
	real := r.sw.collect
	r.sw.collect = func(ro pigtail.Role) roles.Observation {
		if ro.Name == "a" {
			panic("boom")
		}
		return real(ro)
	}
	r.tick(41 * time.Minute)
	r.tick(time.Minute)
	r.tick(time.Minute)
	var a, p pigtail.RoleStatus
	for _, s := range r.sw.Snapshot() {
		if s.Role.Name == "a" {
			a = s
		} else {
			p = s
		}
	}
	if a.State != "unknown" || !strings.HasPrefix(a.Reason, "panic: boom") {
		t.Fatalf("a = %+v", a)
	}
	if p.State != "silent" {
		t.Fatalf("pilote = %+v", p)
	}
	al := r.rows("role-alert")
	if len(al) != 1 || !strings.Contains(al[0].ID, "pilote") {
		t.Fatalf("alerts = %+v", al)
	}
	if n := r.logCount("a panicked"); n != 1 {
		t.Fatalf("panic log lines = %d: %v", n, r.logs)
	}
}

func TestSweepClosedKeyNotReraised(t *testing.T) {
	// A plain re-upsert (created_at kept) of a role whose episode was closed
	// by hand: the same key is never raised again, logged once.
	r := silentRig(t, "perso")
	r.tick(41 * time.Minute)
	key := rsT0.Add(30 * time.Minute)
	closeEpisode := func() {
		for _, e := range []pigtail.JournalEntry{
			{ID: "alert-resolved:" + roles.AlertID("pilote", key), Kind: "alert-resolved", Scope: "perso", Summary: "role removed", Source: "pigtail:roles/pilote", Timestamp: r.clock},
			{ID: roles.RecoveredID("pilote", key), Kind: "role-recovered", Scope: "perso", Summary: "role removed", Source: "pigtail:roles/pilote", Timestamp: r.clock},
		} {
			e := e
			if _, err := r.st.AddJournal(&e); err != nil {
				t.Fatal(err)
			}
		}
	}
	closeEpisode()
	r.tick(time.Minute)
	r.tick(time.Minute)
	if n := len(r.rows("role-alert")); n != 1 {
		t.Fatalf("alert rows after plain re-upsert = %d", n)
	}
	if n := r.logCount("already closed"); n != 1 {
		t.Fatalf("already-closed log lines = %d: %v", n, r.logs)
	}
	// rm + add: a new creation time, a new key, exactly one new row.
	if err := r.st.DeleteRole("perso", "pilote"); err != nil {
		t.Fatal(err)
	}
	r.role("perso", "pilote", "sub-pilote", r.clock)
	r.tick(time.Minute)
	r.tick(time.Minute)
	al := r.rows("role-alert")
	if len(al) != 2 {
		t.Fatalf("alert rows after rm+add = %d", len(al))
	}
	var created bool
	for _, a := range al {
		created = created || a.ID == roles.AlertID("pilote", r.clock.Add(-2*time.Minute))
	}
	if !created {
		t.Fatalf("no row keyed at CreatedAt: %+v", al)
	}
	// A later new missed deadline gives a new row once the old one recovers.
	r.write("sid-pilote", r.clock, rsArm(r.clock, r.clock.Add(30*time.Minute)))
	r.tick(time.Minute)
	r.run(45 * time.Minute)
	if n := len(r.rows("role-alert")); n != 3 {
		t.Fatalf("alert rows after a new deadline = %d", n)
	}
}

func TestSweepAckedEpisodeNotReraised(t *testing.T) {
	r := silentRig(t, "perso")
	r.tick(41 * time.Minute)
	id := roles.AlertID("pilote", rsT0.Add(30*time.Minute))
	e := &pigtail.JournalEntry{ID: "alert-resolved:" + id, Kind: "alert-resolved", Scope: "perso", Summary: "acked", Source: "pigtail:alerts", Timestamp: r.clock}
	if _, err := r.st.AddJournal(e); err != nil {
		t.Fatal(err)
	}
	r.setLive("sid-pilote", 100, "sub-pilote", false) // silent -> absent
	r.tick(time.Minute)
	r.tick(time.Minute)
	if n := len(r.rows("role-alert")); n != 1 {
		t.Fatalf("alert rows = %d", n)
	}
}

func TestSweepBeatsAtPassStart(t *testing.T) {
	r := silentRig(t, "perso")
	r.clock = rsT0.Add(time.Hour) // mono far past the stale threshold
	if _, stale := r.sw.Stale(); !stale {
		t.Fatal("never-beaten sweep one hour in should be stale")
	}
	in, release := make(chan struct{}), make(chan struct{})
	r.sw.collect = func(pigtail.Role) roles.Observation {
		close(in)
		<-release
		return roles.Observation{}
	}
	done := make(chan struct{})
	go func() { r.sw.pass(); close(done) }()
	<-in
	if msg, stale := r.sw.Stale(); stale {
		t.Fatalf("stale during a pass that began: %s", msg)
	}
	close(release)
	<-done
}

func TestSweepStaleAfterThreeIntervalsPlus30s(t *testing.T) {
	r := silentRig(t, "perso")
	var m time.Duration
	r.sw.mono = func() time.Duration { return m }
	r.sw.lastBeat.Store(0)
	m = 3*time.Minute + 29*time.Second
	if msg, stale := r.sw.Stale(); stale {
		t.Fatalf("stale at 3m29s: %s", msg)
	}
	m = 3*time.Minute + 31*time.Second
	msg, stale := r.sw.Stale()
	if !stale || !strings.Contains(msg, "3m31s") || !strings.HasPrefix(msg, "loop roles: last tick") {
		t.Fatalf("at 3m31s: %q, %v", msg, stale)
	}
}

func TestSweepNeverBeatCountsFromStart(t *testing.T) {
	r := silentRig(t, "perso")
	var m time.Duration
	r.sw.mono = func() time.Duration { return m }
	m = time.Minute
	if _, stale := r.sw.Stale(); stale {
		t.Fatal("stale one minute after construction")
	}
	m = 4 * time.Minute
	if _, stale := r.sw.Stale(); !stale {
		t.Fatal("not stale 4 minutes after construction with no beat")
	}
}

// A role removed between the pass's read of the list and its alert write must
// not leave an orphan alert: the sweep re-reads the role after writing and
// closes the episode when it is gone (DELETE /roles shares nothing with it).
func TestSweepAlertForRoleRemovedMidPassIsClosed(t *testing.T) {
	r := newSweepRoleRig(t, time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	ro := r.role("perso", "pilote", "sub-pilote", r.clock.Add(-3*time.Hour))
	r.sw.beforeAlert = func() {
		// the DELETE /roles handler, landing between the list and the write
		if err := handlers.CloseRoleEpisodes(r.st, ro, r.clock); err != nil {
			t.Error(err)
		}
		if err := r.st.DeleteRole("perso", "pilote"); err != nil {
			t.Error(err)
		}
	}
	r.tick(time.Minute)
	if n := len(r.rows("role-alert")); n != 1 {
		t.Fatalf("alert rows = %d, want the one written mid-pass", n)
	}
	open, err := r.st.UnrecoveredRoleAlerts("perso")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("orphan alert left open: %+v", open)
	}
}
