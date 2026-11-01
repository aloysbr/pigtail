package spawnreg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendEntriesRoundTrip(t *testing.T) {
	r := Open(filepath.Join(t.TempDir(), "pigtail", "spawns.jsonl"))
	if es, err := r.Entries(); err != nil || len(es) != 0 {
		t.Fatalf("missing file = %v %v, want empty", es, err)
	}
	e := Entry{TS: time.UnixMilli(1000).UTC(), SubjectID: "sub_a", WorkspaceID: "AAA", WorkspaceRef: "workspace:4", Cell: "jp", Cwd: "/w", Prompt: "/example-skill reprends le sujet sub_a", Mode: "plan", Result: "launched"}
	if err := r.Append(e); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(Entry{SubjectID: "sub_b", WorkspaceID: "BBB", Result: "unverified"}); err != nil {
		t.Fatal(err)
	}
	es, err := r.Entries()
	if err != nil || len(es) != 2 || es[0] != e || es[1].WorkspaceID != "BBB" {
		t.Fatalf("entries = %+v %v", es, err)
	}
}

// ·A: relay_from round-trips, and a plain spawn line carries no key.
func TestRelayFromRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spawns.jsonl")
	r := Open(path)
	if err := r.Append(Entry{WorkspaceID: "W1", RelayFrom: "W0"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(Entry{WorkspaceID: "W2"}); err != nil {
		t.Fatal(err)
	}
	es, err := r.Entries()
	if err != nil || len(es) != 2 || es[0].RelayFrom != "W0" || es[1].RelayFrom != "" {
		t.Fatalf("entries = %+v %v", es, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"relay_from":"W0"`) || strings.Contains(lines[1], "relay_from") {
		t.Fatalf("raw lines = %q", lines)
	}
}

func TestLive(t *testing.T) {
	es := []Entry{
		{SubjectID: "sub_a", WorkspaceID: "AAA"},
		{SubjectID: "sub_b", WorkspaceID: "BBB"},
		{SubjectID: "sub_c", WorkspaceID: "AAA"}, // same workspace re-recorded: latest wins
	}
	got := Live(es, map[string]bool{"AAA": true})
	if len(got) != 1 || got[0].SubjectID != "sub_c" {
		t.Fatalf("live = %+v", got)
	}
	if got := Live(es, nil); len(got) != 0 {
		t.Fatalf("nothing live = %+v", got)
	}
}

func TestLockIsExclusive(t *testing.T) {
	r := Open(filepath.Join(t.TempDir(), "spawns.jsonl"))
	unlock, err := r.Lock()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u2, err := Open(r.path).Lock()
		if err == nil {
			u2()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second Lock returned while the first was held")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("second Lock never returned after unlock")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/s")
	if p := DefaultPath(); p != "/s/pigtail/spawns.jsonl" {
		t.Fatalf("DefaultPath = %q", p)
	}
}

func TestLiveIgnoresRespawn(t *testing.T) {
	es := []Entry{
		{SubjectID: "sub_a", WorkspaceID: "AAA"},
		{SubjectID: "sub_a", WorkspaceID: "AAA", Kind: "respawn", ID: "r1", Result: "respawned"},
		{SubjectID: "sub_b", WorkspaceID: "BBB", Kind: "respawn", ID: "r2", Result: "respawned"},
	}
	got := Live(es, map[string]bool{"AAA": true, "BBB": true})
	if len(got) != 1 || got[0].WorkspaceID != "AAA" || got[0].Kind != "" {
		t.Fatalf("live = %+v, want the spawn of AAA only", got)
	}
}

func TestRespawnsLatestPerID(t *testing.T) {
	es := []Entry{
		{Kind: "respawn", ID: "r1", SubjectID: "sub_a", Result: "armed"},
		{Kind: "respawn", ID: "r1", SubjectID: "sub_a", Result: "respawned", PID: 42},
		{Kind: "respawn", ID: "r2", SubjectID: "sub_b", Result: "failed", Detail: "not seen"},
		{SubjectID: "sub_a", WorkspaceID: "AAA"},
	}
	got := Respawns(es, "sub_a")
	if len(got) != 1 || got[0].Result != "respawned" || got[0].PID != 42 {
		t.Fatalf("respawns = %+v", got)
	}
	if n := len(Respawns(es, "")); n != 2 {
		t.Fatalf("all respawns = %d, want 2", n)
	}
}

// TestLaunchesSince: the burst detector's count — spawn
// records only (empty kind: neither respawn nor any later kind), strictly
// inside the window; a zero ts never counts, a future one does.
func TestLaunchesSince(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	since := now.Add(-10 * time.Minute)
	es := []Entry{
		{TS: now.Add(-11 * time.Minute), WorkspaceID: "OLD", Result: "launched"},
		{TS: since, WorkspaceID: "EDGE", Result: "launched"},
		{TS: now.Add(-9 * time.Minute), WorkspaceID: "A", Result: "launched"},
		{TS: now.Add(-time.Minute), WorkspaceID: "B", Result: "unverified"},
		{TS: now.Add(-time.Minute), WorkspaceID: "C", Result: "launched", RelayFrom: "X"},
		{TS: now.Add(-2 * time.Minute), WorkspaceID: "R", Kind: KindRespawn, ID: "r1", Result: "respawned"},
		{TS: now.Add(-2 * time.Minute), WorkspaceID: "K", Kind: "future", Result: "launched"}, // any non-empty kind is not a spawn
		{WorkspaceID: "ZERO", Result: "launched"},
		{TS: now.Add(time.Minute), WorkspaceID: "FUT", Result: "launched"},
	}
	var got []string
	for _, e := range Launches(es, since, now, PendingOpts{Alive: func(int) bool { return true }}) {
		got = append(got, e.WorkspaceID)
	}
	if g, want := strings.Join(got, ","), "A,B,C,FUT"; g != want {
		t.Fatalf("Launches = %s, want %s", g, want)
	}
}

// Launch margin: a spawn line carries no margin key.
func TestEntryMarginOmittedWhenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spawns.jsonl")
	r := Open(path)
	if err := r.Append(Entry{WorkspaceID: "W1", Result: "launched"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(Entry{Kind: KindMargin, Cell: "mp", Margin: "forced mp", Result: "forced"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], `"margin"`) || !strings.Contains(lines[1], `"kind":"margin"`) || !strings.Contains(lines[1], `"margin":"forced mp"`) {
		t.Fatalf("raw lines = %q", lines)
	}
}

// A margin entry is never a launch: the burst cap never counts it.
func TestMarginEntriesNotCountedByLaunches(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	es := []Entry{
		{TS: now.Add(-time.Minute), WorkspaceID: "A", Result: "launched"},
		{TS: now.Add(-time.Minute), Kind: KindMargin, Cell: "mp", Margin: "refused mp frees unknown", Result: "refused"},
		{TS: now.Add(-time.Minute), Kind: KindMargin, SubjectID: "sub_a", Cell: "mp", Margin: "switched mp->jp", Result: "switched"},
	}
	if got := Launches(es, now.Add(-10*time.Minute), now, PendingOpts{Alive: func(int) bool { return true }}); len(got) != 1 || got[0].WorkspaceID != "A" {
		t.Fatalf("launches = %+v, want A only", got)
	}
}

// A margin entry is never a live workspace, even with a workspace id.
func TestLiveSkipsMarginEntries(t *testing.T) {
	es := []Entry{
		{SubjectID: "sub_a", WorkspaceID: "AAA", Result: "launched"},
		{SubjectID: "sub_b", WorkspaceID: "BBB", Kind: KindMargin, Margin: "forced mp", Result: "forced"},
		{SubjectID: "sub_c", WorkspaceID: "AAA", Kind: KindMargin, Margin: "forced mp", Result: "forced"},
	}
	got := Live(es, map[string]bool{"AAA": true, "BBB": true})
	if len(got) != 1 || got[0].SubjectID != "sub_a" || got[0].Kind != "" {
		t.Fatalf("live = %+v, want the spawn of AAA only", got)
	}
}

// --- Launch states ---

var (
	t1Now    = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	t1Window = 6 * time.Minute
	t1Cap    = 16 * time.Minute
)

func aliveAll(int) bool  { return true }
func aliveNone(int) bool { return false }

func t1Opts(alive func(int) bool) PendingOpts {
	return PendingOpts{Window: t1Window, Cap: t1Cap, Alive: alive}
}

func reserveAt(id, sub string, age time.Duration) Entry {
	return Entry{TS: t1Now.Add(-age), Kind: KindLaunchReserve, ID: id, SubjectID: sub, Result: ResultPending, OwnerPID: 4242}
}

func refAt(id, sub, ws, ref string, age time.Duration) Entry {
	return Entry{TS: t1Now.Add(-age), Kind: KindLaunchRef, ID: id, SubjectID: sub, WorkspaceID: ws, WorkspaceRef: ref, Result: ResultPending}
}

func launchedAt(id, sub, ws, ref string, age time.Duration) Entry {
	return Entry{TS: t1Now.Add(-age), ID: id, SubjectID: sub, WorkspaceID: ws, WorkspaceRef: ref, Result: ResultLaunched}
}

func failedAt(id, sub, ws, ref string, age time.Duration) Entry {
	return Entry{TS: t1Now.Add(-age), Kind: KindLaunchResult, ID: id, SubjectID: sub, WorkspaceID: ws, WorkspaceRef: ref, Result: ResultFailed, Detail: "x"}
}

func TestLaunchIDCarriedReserveToLaunched(t *testing.T) {
	id := NewLaunchID()
	if !strings.HasPrefix(id, "ln_") || len(id) <= 3 || NewLaunchID() == id {
		t.Fatalf("NewLaunchID = %q", id)
	}
	es := []Entry{reserveAt(id, "sub_a", 2*time.Minute), refAt(id, "sub_a", "W", "workspace:3", time.Minute), launchedAt(id, "sub_a", "W", "workspace:3", 30*time.Second)}
	for _, e := range es {
		if e.ID != id {
			t.Fatalf("record %+v lost the id", e)
		}
	}
	got, ok := Outcome(es, id, t1Now, t1Opts(aliveAll))
	if !ok || got.Result != ResultLaunched || got.Kind != "" || got.ID != id {
		t.Fatalf("Outcome = %+v %v, want launched", got, ok)
	}
}

func TestPendingReadsLatestStatePerLaunchID(t *testing.T) {
	o := t1Opts(aliveAll)
	if _, ok := Pending([]Entry{reserveAt("ln_1", "s", time.Minute)}, "s", t1Now, o); !ok {
		t.Fatal("fresh reserve, owner alive: want pending")
	}
	if _, ok := Pending([]Entry{reserveAt("ln_1", "s", time.Minute), launchedAt("ln_1", "s", "W", "workspace:1", 0)}, "s", t1Now, o); ok {
		t.Fatal("launched: want not pending")
	}
	if _, ok := Pending([]Entry{reserveAt("ln_1", "s", 8*time.Minute), refAt("ln_1", "s", "W", "workspace:1", 7*time.Minute)}, "s", t1Now, o); ok {
		t.Fatal("launch-ref 7 min old, window 6 min: want not pending")
	}
	if _, ok := Pending([]Entry{reserveAt("ln_1", "s", 20*time.Minute)}, "s", t1Now, PendingOpts{Window: t1Window, Cap: 30 * time.Minute, Alive: aliveAll}); !ok {
		t.Fatal("reserve 20 min old without launch-ref, under the cap: the window does not run yet, want pending")
	}
	armed := Entry{TS: t1Now, Kind: KindLaunchArmed, ID: "ln_1", SubjectID: "s", WorkspaceID: "W", WorkspaceRef: "workspace:1", Result: ResultArmed}
	if _, ok := Pending([]Entry{reserveAt("ln_1", "s", 2*time.Minute), refAt("ln_1", "s", "W", "workspace:1", time.Minute), armed}, "s", t1Now, o); !ok {
		t.Fatal("armed: want pending")
	}
	if _, ok := Pending([]Entry{reserveAt("ln_1", "s", time.Minute)}, "other", t1Now, o); ok {
		t.Fatal("another subject: want not pending")
	}
}

func TestPendingNamesTabFromLaunchRef(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", 2*time.Minute), refAt("ln_1", "s", "W7", "workspace:7", time.Minute)}
	got, ok := Pending(es, "s", t1Now, t1Opts(aliveAll))
	if !ok || got.WorkspaceRef != "workspace:7" || got.WorkspaceID != "W7" || got.ID != "ln_1" || got.Kind != KindLaunchReserve {
		t.Fatalf("Pending = %+v %v", got, ok)
	}
}

func TestPendingReserveOwnerDead(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", time.Minute)}
	if _, ok := Pending(es, "s", t1Now, t1Opts(aliveNone)); ok {
		t.Fatal("owner dead before the tab: want not pending")
	}
	got, ok := Outcome(es, "ln_1", t1Now, t1Opts(aliveNone))
	if !ok || got.Result != ResultFailed || !strings.Contains(got.Detail, "owner exited before creating the tab") {
		t.Fatalf("Outcome = %+v %v", got, ok)
	}
	if len(es) != 1 {
		t.Fatal("Outcome wrote something")
	}
}

func TestPendingReservePastCap(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", t1Cap+time.Minute)}
	if _, ok := Pending(es, "s", t1Now, t1Opts(aliveAll)); ok {
		t.Fatal("reserve past the cap: want not pending")
	}
	got, ok := Outcome(es, "ln_1", t1Now, t1Opts(aliveAll))
	if !ok || got.Result != ResultFailed || !strings.Contains(got.Detail, "reservation cap passed") {
		t.Fatalf("Outcome = %+v %v", got, ok)
	}
	if _, ok := Outcome(es, "ln_unknown", t1Now, t1Opts(aliveAll)); ok {
		t.Fatal("unknown id: want no outcome")
	}
	open := []Entry{reserveAt("ln_2", "s", time.Minute)}
	if _, ok := Outcome(open, "ln_2", t1Now, t1Opts(aliveAll)); ok {
		t.Fatal("open launch: want no outcome yet")
	}
}

func TestPendingExcludesRespawn(t *testing.T) {
	es := []Entry{{TS: t1Now, Kind: KindRespawn, ID: "r1", SubjectID: "s", WorkspaceID: "W", Result: ResultArmed}}
	if _, ok := Pending(es, "s", t1Now, t1Opts(aliveAll)); ok {
		t.Fatal("a respawn's armed record is not a launch in progress")
	}
}

func TestEndedSessionAfterLaunchedNotRefused(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", 3*time.Minute), refAt("ln_1", "s", "W", "workspace:1", 2*time.Minute), launchedAt("ln_1", "s", "W", "workspace:1", time.Minute)}
	if _, ok := Pending(es, "s", t1Now, t1Opts(aliveAll)); ok {
		t.Fatal("launched then ended, inside the window: want not pending")
	}
}

func TestLiveIgnoresWorkspacelessReserve(t *testing.T) {
	es := []Entry{
		reserveAt("ln_1", "s", 4*time.Minute),
		refAt("ln_1", "s", "W", "workspace:1", 3*time.Minute),
		{TS: t1Now, Kind: KindLaunchArmed, ID: "ln_1", SubjectID: "s", WorkspaceID: "W", Result: ResultArmed},
		failedAt("ln_2", "s", "W", "workspace:1", time.Minute),
		launchedAt("ln_3", "s", "W", "workspace:1", 0),
	}
	got := Live(es, map[string]bool{"": true, "W": true})
	if len(got) != 1 || got[0].ID != "ln_3" || got[0].Result != ResultLaunched {
		t.Fatalf("Live = %+v, want the launched entry only", got)
	}
	if got := Live([]Entry{reserveAt("ln_1", "s", 0), {SubjectID: "x"}}, map[string]bool{"": true}); len(got) != 0 {
		t.Fatalf("workspaceless entries are never live: %+v", got)
	}
}

func launchIDs(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID+"/"+e.WorkspaceID)
	}
	return out
}

func TestBurstCountsEachLaunchOnce(t *testing.T) {
	since := t1Now.Add(-10 * time.Minute)
	es := []Entry{
		reserveAt("ln_1", "s", 3*time.Minute), refAt("ln_1", "s", "W", "workspace:1", 2*time.Minute), launchedAt("ln_1", "s", "W", "workspace:1", time.Minute),
		{TS: t1Now.Add(-time.Minute), WorkspaceID: "LEGACY", Result: "launched"},
	}
	if got := Launches(es, since, t1Now, t1Opts(aliveAll)); len(got) != 2 {
		t.Fatalf("Launches = %v, want one per launch (2)", launchIDs(got))
	}
}

func TestLaunchesCountsSubjectlessReserve(t *testing.T) {
	var es []Entry
	for i := 0; i < 9; i++ {
		es = append(es, Entry{TS: t1Now.Add(-time.Minute), WorkspaceID: "L" + string(rune('A'+i)), Result: ResultLaunched})
	}
	es = append(es, reserveAt("ln_x", "", 10*time.Second))
	if got := Launches(es, t1Now.Add(-10*time.Minute), t1Now, t1Opts(aliveAll)); len(got) != 10 {
		t.Fatalf("Launches = %d, want 10 (a subjectless reserve counts)", len(got))
	}
}

func TestPendingSkipsSubjectless(t *testing.T) {
	if _, ok := Pending([]Entry{reserveAt("ln_x", "", time.Second)}, "", t1Now, t1Opts(aliveAll)); ok {
		t.Fatal("no subject: no duplicate guard")
	}
}

func TestLaunchesDropsDeadOwnerReserve(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", time.Minute)}
	since := t1Now.Add(-10 * time.Minute)
	if got := Launches(es, since, t1Now, t1Opts(aliveNone)); len(got) != 0 {
		t.Fatalf("dead owner, no tab: Launches = %v, want 0", launchIDs(got))
	}
	if got := Launches(es, since, t1Now, t1Opts(aliveAll)); len(got) != 1 {
		t.Fatalf("live owner: Launches = %v, want 1", launchIDs(got))
	}
}

func TestLaunchesDropsCapPassedReserve(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", 5*time.Minute)}
	o := PendingOpts{Window: t1Window, Cap: 4 * time.Minute, Alive: aliveAll}
	if got := Launches(es, t1Now.Add(-10*time.Minute), t1Now, o); len(got) != 0 {
		t.Fatalf("reserve past the cap: Launches = %v, want 0", launchIDs(got))
	}
}

func TestBurstCountsWatcherFailedWithTab(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", 8*time.Minute), refAt("ln_1", "s", "W", "workspace:1", 8*time.Minute), failedAt("ln_1", "s", "W", "workspace:1", 2*time.Minute)}
	since := t1Now.Add(-10 * time.Minute)
	if got := Launches(es, since, t1Now, t1Opts(aliveNone)); len(got) != 1 {
		t.Fatalf("watcher failed with a tab: Launches = %v, want 1", launchIDs(got))
	}
	if last := es[len(es)-1]; last.SubjectID != "s" || last.WorkspaceID != "W" {
		t.Fatalf("last record lost its identity: %+v", last)
	}
	if got := Launches(es, t1Now.Add(-7*time.Minute), t1Now, t1Opts(aliveNone)); len(got) != 0 {
		t.Fatalf("reserve outside the burst window: Launches = %v, want 0", launchIDs(got))
	}
	tabless := []Entry{es[0], es[1], failedAt("ln_1", "s", "", "", 2*time.Minute)}
	if got := Launches(tabless, since, t1Now, t1Opts(aliveNone)); len(got) != 0 {
		t.Fatalf("failed without a WorkspaceID: Launches = %v, want 0", launchIDs(got))
	}
}

func TestLaunchesCountsLaunchRefWithDeadOwner(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", 2*time.Minute), refAt("ln_1", "s", "W", "workspace:1", time.Minute)}
	if got := Launches(es, t1Now.Add(-10*time.Minute), t1Now, t1Opts(aliveNone)); len(got) != 1 {
		t.Fatalf("tab born, CLI gone: Launches = %v, want 1", launchIDs(got))
	}
}

func TestBurstSkipsTablessFailure(t *testing.T) {
	since := t1Now.Add(-10 * time.Minute)
	tabless := []Entry{reserveAt("ln_1", "s", 2*time.Minute), failedAt("ln_1", "s", "", "", time.Minute)}
	if got := Launches(tabless, since, t1Now, t1Opts(aliveAll)); len(got) != 0 {
		t.Fatalf("failed before any tab: Launches = %v, want 0", launchIDs(got))
	}
	withTab := []Entry{reserveAt("ln_1", "s", 2*time.Minute), refAt("ln_1", "s", "W", "workspace:1", time.Minute), failedAt("ln_1", "s", "W", "workspace:1", 0)}
	if got := Launches(withTab, since, t1Now, t1Opts(aliveAll)); len(got) != 1 {
		t.Fatalf("failed with a tab: Launches = %v, want 1", launchIDs(got))
	}
}

func TestPendingExcludesCallerWorkspace(t *testing.T) {
	es := []Entry{reserveAt("ln_1", "s", 2*time.Minute), refAt("ln_1", "s", "U", "workspace:1", time.Minute)}
	o := t1Opts(aliveAll)
	o.ExcludeWorkspaceID = "U"
	if _, ok := Pending(es, "s", t1Now, o); ok {
		t.Fatal("the caller's own tab: want not pending")
	}
	for _, ex := range []string{"V", ""} {
		o.ExcludeWorkspaceID = ex
		if _, ok := Pending(es, "s", t1Now, o); !ok {
			t.Fatalf("exclude %q: want pending", ex)
		}
	}
}

// A margin row is never a launch for the launch readers (Pending, Outcome,
// Launches, Live), even carrying a launch id, a subject and a workspace — and
// one sharing a real launch's id neither closes nor ends that launch.
func TestLaunchReadersIgnoreMarginRows(t *testing.T) {
	margin := func(id string) Entry {
		return Entry{TS: t1Now.Add(-time.Minute), Kind: KindMargin, ID: id, SubjectID: "s", WorkspaceID: "W",
			Cell: "mp", Margin: "refused perso frees unknown", Result: ResultPending}
	}
	o := t1Opts(aliveAll)
	alone := []Entry{margin("ln_m")}
	if e, ok := Pending(alone, "s", t1Now, o); ok {
		t.Fatalf("a margin row is pending: %+v", e)
	}
	if e, ok := Outcome(alone, "ln_m", t1Now, o); ok {
		t.Fatalf("a margin row has a launch outcome: %+v", e)
	}
	if got := Launches(alone, t1Now.Add(-10*time.Minute), t1Now, o); len(got) != 0 {
		t.Fatalf("a margin row counts in the burst: %+v", got)
	}
	if got := Live(alone, map[string]bool{"W": true}); len(got) != 0 {
		t.Fatalf("a margin row is live: %+v", got)
	}
	// A real reserve, then a margin row with the same id: still in progress.
	mixed := []Entry{reserveAt("ln_a", "s", time.Minute), margin("ln_a")}
	if e, ok := Pending(mixed, "s", t1Now, o); !ok || e.Kind != KindLaunchReserve {
		t.Fatalf("Pending = %+v %v, want the reserve in progress", e, ok)
	}
	if e, ok := Outcome(mixed, "ln_a", t1Now, o); ok {
		t.Fatalf("a margin row ended the launch: %+v", e)
	}
	if got := Launches(mixed, t1Now.Add(-10*time.Minute), t1Now, o); len(got) != 1 || got[0].Kind != KindLaunchReserve {
		t.Fatalf("Launches = %+v, want the reserve only", got)
	}
}
