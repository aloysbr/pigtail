package daemon

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// dbHome returns a throwaway database path. attempt() writes
// offsite-state.json beside it, so a shared literal like "/tmp/pigtail.db"
// would have these tests scribbling on each other and on a real machine.
func dbHome(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "pigtail.db")
}

// The offsite copy is the one part of the backup chain that talks to something
// pigtail does not control. Three behaviours follow from that, and each is a
// deliberate choice rather than an implementation detail.

// A hung network mount blocks a write for as long as it likes, and a
// context does not interrupt a read already inside the kernel. Running the copy
// off the backup loop is what keeps a hung NAS from stopping the *local*
// backups too — the ones that still work.
func TestOffsiteNeverBlocksTheBackupLoop(t *testing.T) {
	db, dest := dbHome(t), filepath.Join(t.TempDir(), "not-mounted")
	release := make(chan struct{})
	o := &offsiteBackupper{
		copy: func(string, string, int) error {
			<-release // stands in for a write to a hung mount
			return nil
		},
	}

	done := make(chan struct{})
	go func() {
		o.run(db, dest)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run blocked on the copy; a hung mount would stop local backups too")
	}
	close(release)
	waitFor(t, func() bool { o.mu.Lock(); defer o.mu.Unlock(); return !o.inFlight })
}

// One goroutine may be stuck on a hung mount forever. A second must not join
// it: an hourly ticker against an unreachable NAS would otherwise leak a
// blocked goroutine per hour, indefinitely.
func TestOffsiteSkipsACycleWhileOneIsStillInFlight(t *testing.T) {
	db, dest := dbHome(t), filepath.Join(t.TempDir(), "not-mounted")
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	o := &offsiteBackupper{
		copy: func(string, string, int) error {
			mu.Lock()
			started++
			mu.Unlock()
			<-release
			return nil
		},
	}

	o.run(db, dest)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return started == 1 })

	for i := 0; i < 5; i++ {
		o.run(db, dest)
	}
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	got := started
	mu.Unlock()
	if got != 1 {
		t.Fatalf("%d copies started while one was in flight, want 1", got)
	}
	close(release)
	// Let the in-flight copy finish before t.Cleanup removes the directory it
	// writes its state into — otherwise the test tears the ground out from
	// under its own goroutine.
	waitFor(t, func() bool { o.mu.Lock(); defer o.mu.Unlock(); return !o.inFlight })
}

// A laptop away from its NAS is the normal case, not an incident. Logging it
// every hour produces a log nobody reads — the same failure the deaths section
// avoids by staying absent when there is nothing to say. Only the transitions
// are worth a line: into failing, into alarm, and back to working. Each of
// those three fires once per crossing, not once per attempt spent on the
// far side of it.
func TestOffsiteLogsTransitionsOnly(t *testing.T) {
	db, dest := dbHome(t), filepath.Join(t.TempDir(), "not-mounted")
	var mu sync.Mutex
	var lines []string
	fail := true
	o := &offsiteBackupper{
		copy: func(string, string, int) error {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return errors.New("offsite destination unreachable")
			}
			return nil
		},
		logf: func(format string, args ...interface{}) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, format)
		},
	}

	countContaining := func(word string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), word) {
				n++
			}
		}
		return n
	}

	for i := 0; i < 4; i++ {
		o.runSync(db, dest)
	}
	// The 3rd of these 4 failures crosses the streak into alarm: two distinct
	// transitions on record ("failing" on the 1st, "alarm" on the 3rd), never
	// re-emitted by the 2nd or 4th attempt that stays on the same side of
	// each threshold.
	if got := countContaining("failing"); got != 1 {
		t.Fatalf("four consecutive failures produced %d 'failing' lines, want 1", got)
	}
	if got := countContaining("alarm"); got != 1 {
		t.Fatalf("four consecutive failures produced %d 'alarm' lines, want 1", got)
	}
	mu.Lock()
	linesBeforeRecovery := len(lines)
	fail = false
	mu.Unlock()
	if linesBeforeRecovery != 2 {
		t.Fatalf("four consecutive failures produced %d log lines total, want 2 (one per transition)", linesBeforeRecovery)
	}

	for i := 0; i < 3; i++ {
		o.runSync(db, dest)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 3 {
		t.Fatalf("recovery produced %d total lines, want 3 (one per transition): %v", len(lines), lines)
	}
	if !strings.Contains(strings.ToLower(lines[2]), "again") &&
		!strings.Contains(strings.ToLower(lines[2]), "resumed") &&
		!strings.Contains(strings.ToLower(lines[2]), "ok") {
		t.Errorf("the recovery line does not read as a recovery: %q", lines[2])
	}
}

// The persistent replacement for offsiteBackupper.failing: a run of failed
// attempts must accumulate in the state file, not just in memory, because the
// whole point is that a daemon restart mid-outage must not erase it.
func TestOffsiteAttemptIncrementsConsecutiveFailures(t *testing.T) {
	db, dest := dbHome(t), filepath.Join(t.TempDir(), "not-mounted")
	o := &offsiteBackupper{
		copy: func(string, string, int) error { return errors.New("offsite destination unreachable") },
		logf: func(string, ...interface{}) {},
	}

	o.runSync(db, dest)
	if got := store.ReadOffsiteState(db).ConsecutiveFailures; got != 1 {
		t.Fatalf("ConsecutiveFailures after 1 failure = %d, want 1", got)
	}

	o.runSync(db, dest)
	o.runSync(db, dest)
	if got := store.ReadOffsiteState(db).ConsecutiveFailures; got != 3 {
		t.Fatalf("ConsecutiveFailures after 3 failures = %d, want 3", got)
	}
}

// A single success must clear the streak, not merely stop growing it: the
// next failure after a recovery is a new incident, not a continuation.
func TestOffsiteAttemptResetsConsecutiveFailuresOnSuccess(t *testing.T) {
	db, dest := dbHome(t), filepath.Join(t.TempDir(), "not-mounted")
	fail := true
	o := &offsiteBackupper{
		copy: func(string, string, int) error {
			if fail {
				return errors.New("offsite destination unreachable")
			}
			return nil
		},
		logf: func(string, ...interface{}) {},
	}

	for i := 0; i < 3; i++ {
		o.runSync(db, dest)
	}
	if got := store.ReadOffsiteState(db).ConsecutiveFailures; got != 3 {
		t.Fatalf("ConsecutiveFailures before recovery = %d, want 3", got)
	}

	fail = false
	o.runSync(db, dest)
	if got := store.ReadOffsiteState(db).ConsecutiveFailures; got != 0 {
		t.Fatalf("ConsecutiveFailures after a success = %d, want 0", got)
	}
}

// The defect this task exists to fix: without carrying the count through
// declare(), a daemon restart mid-outage — same destination, nothing else
// changed — would silently drop the streak back to zero and erase the alarm.
func TestOffsiteDeclareCarriesConsecutiveFailuresForwardOnSameDestination(t *testing.T) {
	db, dest := dbHome(t), filepath.Join(t.TempDir(), "dest")
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:             dest,
		ConsecutiveFailures: 3,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{}
	o.declare(db, dest, "")

	got := store.ReadOffsiteState(db)
	if got.ConsecutiveFailures != 3 {
		t.Fatalf("ConsecutiveFailures after declare on the same destination = %d, want 3 (restart erased the streak)",
			got.ConsecutiveFailures)
	}
}

// A changed destination is a different backup relationship: its failure
// history belongs to it, not to whatever the previous destination reported.
func TestOffsiteDeclareResetsConsecutiveFailuresOnANewDestination(t *testing.T) {
	db := dbHome(t)
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:             "/old/place",
		ConsecutiveFailures: 5,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{}
	o.declare(db, "/new/place", "")

	got := store.ReadOffsiteState(db)
	if got.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures on a new destination = %d, want 0 (inherited the old destination's streak)",
			got.ConsecutiveFailures)
	}
}

// declare() is the only writer of declared_at: the daemon calls it once at
// startup with whatever destination is configured, and a fresh destination —
// nothing persisted yet — has no declared_at to carry forward, so this is
// where the clock starts.
func TestOffsiteDeclareSetsDeclaredAtWhenAbsent(t *testing.T) {
	db, dest := dbHome(t), t.TempDir()

	o := &offsiteBackupper{}
	o.declare(db, dest, "")

	got := store.ReadOffsiteState(db)
	if got.DeclaredAt.IsZero() {
		t.Fatal("DeclaredAt is zero after declare() on a fresh destination, want it set to now")
	}
	if age := time.Since(got.DeclaredAt); age < 0 || age > 5*time.Second {
		t.Errorf("DeclaredAt = %v ago, want roughly now", age)
	}
}

// The other half of the substitute-age arbitrage: once declared_at is on
// record, a restart must not reset the clock, or a destination declared 25
// hours ago would read as freshly declared on every daemon restart and never
// reach the 26h alarm.
func TestOffsiteDeclareCarriesDeclaredAtForwardOnSameDestination(t *testing.T) {
	db, dest := dbHome(t), t.TempDir()
	declaredAt := time.Now().Add(-25 * time.Hour)
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:    dest,
		DeclaredAt: declaredAt,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{}
	o.declare(db, dest, "")

	got := store.ReadOffsiteState(db)
	if !got.DeclaredAt.Equal(declaredAt) {
		t.Errorf("DeclaredAt = %v, want %v unchanged (declare on the same destination must carry it forward)",
			got.DeclaredAt, declaredAt)
	}
}

// A changed destination is a different backup relationship — symmetric with
// how declare() already resets ConsecutiveFailures on a new DestDir — so its
// declared_at starts fresh too, rather than inheriting how long ago the old
// destination was declared.
func TestOffsiteDeclareResetsDeclaredAtOnANewDestination(t *testing.T) {
	db := dbHome(t)
	oldDeclaredAt := time.Now().Add(-25 * time.Hour)
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:    "/old/place",
		DeclaredAt: oldDeclaredAt,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{}
	o.declare(db, "/new/place", "")

	got := store.ReadOffsiteState(db)
	if got.DeclaredAt.Equal(oldDeclaredAt) {
		t.Fatal("DeclaredAt carried the old destination's value onto a new destination")
	}
	if age := time.Since(got.DeclaredAt); age < 0 || age > 5*time.Second {
		t.Errorf("DeclaredAt = %v ago on a new destination, want roughly now", age)
	}
}

// The file a previous version of the daemon wrote has no declared_at at all
// (the field did not exist yet). declare() on the same destination must not
// leave it zero forever — that would permanently disable the escalation this
// task adds for anyone upgrading — it must backfill it to now, same as a
// genuinely fresh destination.
//
// Mutation: an unconditional `s.DeclaredAt = now` (dropping the IsZero
// guard) also makes this pass, which is why
// TestOffsiteDeclareCarriesDeclaredAtForwardOnSameDestination above exists
// alongside it: only together do they pin "now if absent, carried forward
// otherwise".
func TestOffsiteDeclareBackfillsDeclaredAtOnAPreExistingStateFileWithoutIt(t *testing.T) {
	db, dest := dbHome(t), t.TempDir()
	if err := store.WriteOffsiteState(db, store.OffsiteState{
		DestDir:             dest,
		ConsecutiveFailures: 2,
	}); err != nil {
		t.Fatal(err)
	}

	o := &offsiteBackupper{}
	o.declare(db, dest, "")

	got := store.ReadOffsiteState(db)
	if got.DeclaredAt.IsZero() {
		t.Fatal("DeclaredAt is still zero after declare() backfilled a pre-existing state file, want it set to now")
	}
	if age := time.Since(got.DeclaredAt); age < 0 || age > 5*time.Second {
		t.Errorf("DeclaredAt = %v ago, want roughly now", age)
	}
	if got.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2 preserved (declare on the same destination is a backfill, not a reset)",
			got.ConsecutiveFailures)
	}
}

// The alarm this feature builds used to live only in offsite-state.json,
// reachable solely by someone thinking to run `pigtail servers`. On
// 2026-09-06 the daemon logged the first failure and then nothing else for
// the rest of the outage, including the moment the streak crossed into
// alarm. A dedicated log line at that crossing — once, not re-emitted on
// every subsequent failed attempt — is what would have put the escalation in
// front of someone tailing the log instead of only the unremarkable first
// symptom.
func TestOffsiteLogsOnceWhenSeverityReachesAlarm(t *testing.T) {
	db, dest := dbHome(t), filepath.Join(t.TempDir(), "not-mounted")
	var mu sync.Mutex
	var lines []string
	o := &offsiteBackupper{
		copy: func(string, string, int) error { return errors.New("offsite destination unreachable") },
		logf: func(format string, args ...interface{}) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf(format, args...))
		},
	}

	// Matched case-sensitively against the exact log phrase, not a loose
	// case-insensitive "alarm": t.TempDir() embeds the test's own name in the
	// path it returns, and this test's name itself contains "...ReachesAlarm...",
	// which a case-insensitive substring check over lines that mention dest
	// would otherwise match by accident.
	countAlarm := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, l := range lines {
			if strings.Contains(l, "reached alarm") {
				n++
			}
		}
		return n
	}

	o.runSync(db, dest) // 1st failure
	o.runSync(db, dest) // 2nd failure
	if got := countAlarm(); got != 0 {
		t.Fatalf("alarm log lines after 2 failures = %d, want 0", got)
	}

	o.runSync(db, dest) // 3rd failure crosses the streak threshold into alarm
	if got := countAlarm(); got != 1 {
		t.Fatalf("alarm log lines after 3 failures = %d, want 1", got)
	}

	o.runSync(db, dest) // 4th and 5th: still alarm, must not re-log
	o.runSync(db, dest)
	if got := countAlarm(); got != 1 {
		t.Fatalf("alarm log lines after 5 failures = %d, want 1 (no re-emission while still alarm)", got)
	}
}

// The defect fixed alongside the log line above: the state written *before*
// a copy runs carries the previous attempt's failure count forward without
// yet knowing whether this attempt will fail too. Before the fix, starting a
// 4th attempt after 3 consecutive failures classified as alarm severity while
// the rendered detail still read "copy in progress" — an alarm-severity row
// with a reassuring sentence. A copy in flight must not be counted as one
// more failure just because the previous ones were.
func TestOffsiteInProgressAfterAStreakDoesNotReadAsAlarm(t *testing.T) {
	db, dest := dbHome(t), filepath.Join(t.TempDir(), "not-mounted")
	release := make(chan struct{})
	attemptsStarted := 0
	var mu sync.Mutex
	o := &offsiteBackupper{
		copy: func(string, string, int) error {
			mu.Lock()
			attemptsStarted++
			n := attemptsStarted
			mu.Unlock()
			if n <= 3 {
				return errors.New("offsite destination unreachable")
			}
			<-release // the 4th attempt: hang here, mid-flight
			return nil
		},
		logf: func(string, ...interface{}) {},
	}

	for i := 0; i < 3; i++ {
		o.runSync(db, dest)
	}
	if got := store.ReadOffsiteState(db).ConsecutiveFailures; got != 3 {
		t.Fatalf("ConsecutiveFailures before the 4th attempt = %d, want 3", got)
	}

	done := make(chan struct{})
	go func() {
		o.runSync(db, dest)
		close(done)
	}()
	defer func() {
		close(release)
		<-done
	}()

	waitFor(t, func() bool { return store.ReadOffsiteState(db).Attempting() })

	state := store.ReadOffsiteState(db)
	health := store.ClassifyOffsite(state, time.Now())
	if health.Status != store.OffsiteInProgress {
		t.Fatalf("Status while the 4th attempt is in flight = %q, want in_progress", health.Status)
	}
	if health.Severity == store.SeverityAlarm {
		t.Fatalf("Severity while merely in progress = alarm, want at most warn (a carried-forward "+
			"failure streak must not turn a normal retry into a false alarm); detail: %q",
			store.RenderOffsiteDetail(health))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
