package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/mainfresh"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeMainRefresher replays a scripted outcome per call, so the sweep's
// logging rules can be exercised without a git repo.
type fakeMainRefresher struct {
	results []backend.MainRefresh
	err     error
	calls   int
}

func (f *fakeMainRefresher) RefreshMain(_ context.Context, _ *pigtail.Project) (backend.MainRefresh, error) {
	f.calls++
	if f.err != nil {
		return backend.MainRefresh{}, f.err
	}
	res := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	return res, nil
}

type recorder struct{ lines []string }

func (r *recorder) logf(format string, args ...interface{}) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *recorder) joined() string { return strings.Join(r.lines, "\n") }

func oneProject() []pigtail.Project {
	return []pigtail.Project{{ID: "prj_1", Name: "globex", RootPath: "/tmp/ts"}}
}

// testClock is a hand-wound clock: the daily reminder is a rule about elapsed
// time, and a rule about time is only testable if the test owns the clock.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestPass wires a sweep over a scripted refresher with a controllable
// clock and a recording logger.
func newTestPass(fake *fakeMainRefresher, rec *recorder, clk *testClock) *mainRefreshPass {
	return &mainRefreshPass{
		refresher: fake,
		memo:      map[string]mainRefreshMemo{},
		now:       clk.now,
		logf:      rec.logf,
	}
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)}
}

// TestSweepLogsEveryRefresh: a fast-forward of a directory other sessions read
// is logged every single time it happens. Never deduplicated — a move nobody
// can date is a move nobody can trust.
func TestSweepLogsEveryRefresh(t *testing.T) {
	fake := &fakeMainRefresher{results: []backend.MainRefresh{{
		Path: "/tmp/ts/main", Base: "origin/main",
		Action: backend.MainRefreshUpdated, From: "aaaaaaa", To: "bbbbbbb",
	}}}
	rec := &recorder{}
	pass := newTestPass(fake, rec, newTestClock())

	for i := 0; i < 3; i++ {
		if n := pass.run(context.Background(), oneProject()); n != 1 {
			t.Fatalf("sweep %d reported %d refreshes, want 1", i, n)
		}
	}
	if len(rec.lines) != 3 {
		t.Fatalf("got %d log lines for 3 refreshes, want 3:\n%s", len(rec.lines), rec.joined())
	}
	for _, want := range []string{"globex", "origin/main", "aaaaaaa", "bbbbbbb", "/tmp/ts/main"} {
		if !strings.Contains(rec.lines[0], want) {
			t.Errorf("log line %q does not name %q", rec.lines[0], want)
		}
	}
}

// TestSweepLogsAnAbstentionOnceWithItsReason: the refusal must be findable in
// daemon.log — "why is main still behind?" needs an answer there — without
// repeating 144 times a day per project, which is its own way of being
// invisible.
func TestSweepLogsAnAbstentionOnceWithItsReason(t *testing.T) {
	fake := &fakeMainRefresher{results: []backend.MainRefresh{{
		Path: "/tmp/ts/main", Base: "origin/main",
		Action: backend.MainRefreshSkipped,
		Reason: "working tree not pristine (3 uncommitted or untracked entries)",
	}}}
	rec := &recorder{}
	pass := newTestPass(fake, rec, newTestClock())

	for i := 0; i < 5; i++ {
		if n := pass.run(context.Background(), oneProject()); n != 0 {
			t.Fatalf("an abstention counted as %d refreshes", n)
		}
	}
	if len(rec.lines) != 1 {
		t.Fatalf("got %d lines for the same abstention repeated, want 1:\n%s", len(rec.lines), rec.joined())
	}
	if !strings.Contains(rec.lines[0], "not pristine") || !strings.Contains(rec.lines[0], "globex") {
		t.Errorf("line %q must name both the project and the reason", rec.lines[0])
	}
}

// TestSweepLogsAChangedReason: a checkout that goes from dirty to parked on a
// feature branch is a different refusal, and says so.
func TestSweepLogsAChangedReason(t *testing.T) {
	fake := &fakeMainRefresher{results: []backend.MainRefresh{
		{Action: backend.MainRefreshSkipped, Reason: "working tree not pristine (1 uncommitted or untracked entry)"},
		{Action: backend.MainRefreshSkipped, Reason: `checked out on "feat/x", not "main"`},
	}}
	rec := &recorder{}
	pass := newTestPass(fake, rec, newTestClock())

	pass.run(context.Background(), oneProject())
	pass.run(context.Background(), oneProject())

	if len(rec.lines) != 2 {
		t.Fatalf("got %d lines for two different refusals, want 2:\n%s", len(rec.lines), rec.joined())
	}
	if !strings.Contains(rec.lines[1], "feat/x") {
		t.Errorf("second line %q does not carry the new reason", rec.lines[1])
	}
}

// TestSweepReArmsAfterTheObstacleClears: dirty → clean → dirty again must log
// the refusal twice. Remembering a reason forever would silence exactly the
// case worth seeing: a checkout that keeps falling back into an unsafe state.
func TestSweepReArmsAfterTheObstacleClears(t *testing.T) {
	dirty := backend.MainRefresh{Action: backend.MainRefreshSkipped, Reason: "working tree not pristine (1 uncommitted or untracked entry)"}
	fake := &fakeMainRefresher{results: []backend.MainRefresh{
		dirty,
		{Action: backend.MainRefreshUpToDate},
		dirty,
	}}
	rec := &recorder{}
	pass := newTestPass(fake, rec, newTestClock())

	for i := 0; i < 3; i++ {
		pass.run(context.Background(), oneProject())
	}
	if len(rec.lines) != 2 {
		t.Fatalf("got %d lines, want the refusal logged on both occurrences:\n%s", len(rec.lines), rec.joined())
	}
}

// TestSweepStaysSilentWhenThereIsNothingToDo: the steady state is reached
// every interval forever. It must not write a line.
func TestSweepStaysSilentWhenThereIsNothingToDo(t *testing.T) {
	fake := &fakeMainRefresher{results: []backend.MainRefresh{{Action: backend.MainRefreshUpToDate}}}
	rec := &recorder{}
	pass := newTestPass(fake, rec, newTestClock())

	for i := 0; i < 10; i++ {
		pass.run(context.Background(), oneProject())
	}
	if len(rec.lines) != 0 {
		t.Fatalf("an up-to-date checkout logged %d lines:\n%s", len(rec.lines), rec.joined())
	}
}

// TestSweepSurvivesOneFailingProject: a project that cannot answer must not
// stop the sweep from reaching the ones that can.
func TestSweepSurvivesOneFailingProject(t *testing.T) {
	fake := &fakeMainRefresher{err: errors.New("git exploded")}
	rec := &recorder{}
	projects := append(oneProject(), pigtail.Project{ID: "prj_2", Name: "tools"})

	newTestPass(fake, rec, newTestClock()).run(context.Background(), projects)

	if fake.calls != 2 {
		t.Errorf("refresher called %d times, want one call per project", fake.calls)
	}
	if len(rec.lines) != 2 {
		t.Fatalf("got %d lines, want one per failure:\n%s", len(rec.lines), rec.joined())
	}
}

// TestSweepWithoutARefresherIsANoOp: the capability is optional
// (architecture.md §9); a backend that does not implement it must not panic
// the daemon's goroutine.
func TestSweepWithoutARefresherIsANoOp(t *testing.T) {
	pass := &mainRefreshPass{memo: map[string]mainRefreshMemo{}}
	if n := pass.run(context.Background(), oneProject()); n != 0 {
		t.Fatalf("nil refresher reported %d refreshes", n)
	}
}

// --- the daily reminder ----------------------------------------------------

// TestSweepRepeatsAPersistentAbstentionDaily: deduplicating on the reason
// alone means a checkout parked on a feature branch for a week is named once,
// on day one, then never again — the answer to "why is main still behind?"
// exists but has scrolled past thousands of lines. A lasting refusal is one
// condition *enduring*, not the same event repeating, so it is re-stated once
// a day, with how long it has lasted.
func TestSweepRepeatsAPersistentAbstentionDaily(t *testing.T) {
	parked := backend.MainRefresh{
		Path: "/tmp/ts/main", Base: "origin/main", Behind: 23,
		Action: backend.MainRefreshSkipped,
		Reason: `checked out on "feat/x", not "main"`,
	}
	fake := &fakeMainRefresher{results: []backend.MainRefresh{parked}}
	rec := &recorder{}
	clk := newTestClock()
	pass := newTestPass(fake, rec, clk)

	// Four days of sweeps, ten minutes apart: 576 verdicts, all identical.
	for day := 0; day < 4; day++ {
		for tick := 0; tick < 24*6; tick++ {
			pass.run(context.Background(), oneProject())
			clk.advance(10 * time.Minute)
		}
	}

	// One line on day 1, then one per elapsed day — not 576, not 1.
	if len(rec.lines) != 4 {
		t.Fatalf("got %d lines over four days of one unchanged refusal, want 4:\n%s",
			len(rec.lines), rec.joined())
	}
	if strings.Contains(rec.lines[0], "STILL") {
		t.Errorf("the first line must be the plain refusal, got %q", rec.lines[0])
	}
	for _, l := range rec.lines[1:] {
		if !strings.Contains(l, "STILL") {
			t.Errorf("a reminder must say the refusal is still standing, got %q", l)
		}
	}
	// The duration is the point of the reminder: day 2 says 24h, day 4 says 72h.
	if !strings.Contains(rec.lines[1], "24h") {
		t.Errorf("first reminder %q does not carry how long the refusal has lasted", rec.lines[1])
	}
	if !strings.Contains(rec.lines[3], "72h") {
		t.Errorf("third reminder %q does not carry how long the refusal has lasted", rec.lines[3])
	}
	// And the drift it is leaving in place, which is what makes it actionable.
	if !strings.Contains(rec.lines[1], "23 commits behind") {
		t.Errorf("reminder %q does not name the drift being left in place", rec.lines[1])
	}
}

// TestSweepReminderRestartsWhenTheReasonChanges: a refusal that changes shape
// is a new event. It logs immediately, and its own daily clock starts there —
// not on the previous reason's schedule.
func TestSweepReminderRestartsWhenTheReasonChanges(t *testing.T) {
	dirty := backend.MainRefresh{Action: backend.MainRefreshSkipped, Reason: "working tree not pristine (1 uncommitted or untracked entry)"}
	parked := backend.MainRefresh{Action: backend.MainRefreshSkipped, Reason: `checked out on "feat/x", not "main"`}
	fake := &fakeMainRefresher{results: []backend.MainRefresh{dirty, dirty, parked, parked}}
	rec := &recorder{}
	clk := newTestClock()
	pass := newTestPass(fake, rec, clk)

	pass.run(context.Background(), oneProject()) // logs: dirty
	clk.advance(23 * time.Hour)
	pass.run(context.Background(), oneProject()) // silent: same reason, under a day
	pass.run(context.Background(), oneProject()) // logs: new reason
	clk.advance(23 * time.Hour)
	pass.run(context.Background(), oneProject()) // silent: 23h since the NEW reason

	if len(rec.lines) != 2 {
		t.Fatalf("got %d lines, want the two distinct refusals and nothing else:\n%s",
			len(rec.lines), rec.joined())
	}
	if strings.Contains(rec.lines[1], "STILL") {
		t.Errorf("a changed reason is a new event, not a reminder: %q", rec.lines[1])
	}
}

// TestSweepUpdateClearsTheReminder: once the obstacle is gone and the checkout
// moves, the refusal is over. A reminder afterwards would describe a state
// that no longer exists.
func TestSweepUpdateClearsTheReminder(t *testing.T) {
	dirty := backend.MainRefresh{Action: backend.MainRefreshSkipped, Reason: "working tree not pristine (1 uncommitted or untracked entry)"}
	fake := &fakeMainRefresher{results: []backend.MainRefresh{
		dirty,
		{Action: backend.MainRefreshUpdated, Base: "origin/main", From: "aaaaaaa", To: "bbbbbbb", Behind: 3},
		dirty,
	}}
	rec := &recorder{}
	clk := newTestClock()
	pass := newTestPass(fake, rec, clk)

	pass.run(context.Background(), oneProject()) // refusal
	clk.advance(time.Minute)
	pass.run(context.Background(), oneProject()) // the move
	clk.advance(time.Minute)
	pass.run(context.Background(), oneProject()) // refusal again, one minute later

	if len(rec.lines) != 3 {
		t.Fatalf("got %d lines, want refusal + move + refusal:\n%s", len(rec.lines), rec.joined())
	}
	if strings.Contains(rec.lines[2], "STILL") {
		t.Errorf("the refusal after a successful move is a fresh one, not a reminder: %q", rec.lines[2])
	}
}

// TestSweepNamesTheDriftItClosed: "fast-forwarded" alone does not say whether
// anything was actually wrong. The count is what turns the line into evidence
// that the drift this whole pass exists for was real.
func TestSweepNamesTheDriftItClosed(t *testing.T) {
	fake := &fakeMainRefresher{results: []backend.MainRefresh{{
		Path: "/tmp/ts/main", Base: "origin/main", Behind: 23,
		Action: backend.MainRefreshUpdated, From: "aaaaaaa", To: "bbbbbbb",
	}}}
	rec := &recorder{}
	newTestPass(fake, rec, newTestClock()).run(context.Background(), oneProject())

	if !strings.Contains(rec.lines[0], "23 commits behind") {
		t.Fatalf("line %q does not name how far behind the checkout was", rec.lines[0])
	}
}

// --- what the sweep publishes for `pigtail servers` ------------------------

// TestSweepPublishesEveryVerdict: the read-only display answers "is my main
// fresh?" from this registry and never runs git itself, so the sweep must
// publish the boring verdicts too — an up-to-date checkout is exactly the
// answer someone is looking for before pointing an agent at it.
func TestSweepPublishesEveryVerdict(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  backend.MainRefresh
	}{
		{"updated", backend.MainRefresh{Action: backend.MainRefreshUpdated, Base: "origin/main", Behind: 23, From: "aaaaaaa", To: "bbbbbbb"}},
		{"up-to-date", backend.MainRefresh{Action: backend.MainRefreshUpToDate, Base: "origin/main"}},
		{"skipped", backend.MainRefresh{Action: backend.MainRefreshSkipped, Base: "origin/main", Behind: 23, Reason: "working tree not pristine (1 uncommitted or untracked entry)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newTestClock()
			pass := newTestPass(&fakeMainRefresher{results: []backend.MainRefresh{tc.res}}, &recorder{}, clk)
			pass.registry = mainfresh.New()

			pass.run(context.Background(), oneProject())

			got := pass.registry.Snapshot()
			if len(got) != 1 {
				t.Fatalf("registry holds %d entries, want one per project swept", len(got))
			}
			if got[0].Action != tc.res.Action || got[0].Behind != tc.res.Behind {
				t.Errorf("published %+v, want the verdict as measured (%q, behind %d)",
					got[0], tc.res.Action, tc.res.Behind)
			}
			if got[0].ProjectName != "globex" {
				t.Errorf("published project name %q, want the project's own", got[0].ProjectName)
			}
			if !got[0].At.Equal(clk.now()) {
				t.Errorf("published at %v, want the sweep's own clock %v", got[0].At, clk.now())
			}
		})
	}
}

// TestSweepPublishesNothingForAFailedProject: a project git could not answer
// for has no verdict. Leaving a previous one in place would be worse than
// silence only if it were presented as current — and publishing an empty one
// would claim a freshness nobody measured.
func TestSweepPublishesNothingForAFailedProject(t *testing.T) {
	pass := newTestPass(&fakeMainRefresher{err: errors.New("git exploded")}, &recorder{}, newTestClock())
	pass.registry = mainfresh.New()

	pass.run(context.Background(), oneProject())

	if got := pass.registry.Snapshot(); len(got) != 0 {
		t.Fatalf("registry holds %+v after a failed refresh, want nothing", got)
	}
}

// TestSweepWithoutARegistryIsANoOp: the registry is optional wiring; a daemon
// built without one must not panic on every tick.
func TestSweepWithoutARegistryIsANoOp(t *testing.T) {
	pass := newTestPass(&fakeMainRefresher{results: []backend.MainRefresh{{Action: backend.MainRefreshUpToDate}}}, &recorder{}, newTestClock())
	pass.registry = nil
	pass.run(context.Background(), oneProject())
}
