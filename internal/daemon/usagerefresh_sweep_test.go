package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/usagehistory"
)

var sweepNow = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

// sweepFS is an in-memory set of .claude.json files the fake launcher bumps.
type sweepFS struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (f *sweepFS) read(p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}

func (f *sweepFS) set(dir string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[filepath.Join(dir, claudeusage.FileName)] = b
}

// cellFile is a logged-in cell's .claude.json read age before sweepNow.
func cellFile(uuid string, age time.Duration) []byte {
	return []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationRateLimitTier":"default_claude_max_20x","userRateLimitTier":null},
"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":%q,"utilization":{"five_hour":{"utilization":10,"resets_at":null},"seven_day":{"utilization":10,"resets_at":null}}}}`,
		uuid, sweepNow.Add(-age).UnixMilli(), uuid))
}

func sweepCell(id, sc, letter string, rank int) claudeusage.CellInput {
	r := rank
	return claudeusage.CellInput{ID: id, ConfigDir: "/cells/" + id, Scope: sc, Account: letter, Rank: &r}
}

const (
	uuidSweepM = "11111111-1111-4111-8111-111111111111"
	uuidSweepJ = "33333333-3333-4333-8333-333333333333"
)

type sweepRig struct {
	pass     *usageRefreshSweep
	fs       *sweepFS
	launched []string
	logs     []string
	mu       sync.Mutex
	clock    time.Time
	fail     map[string]error // config dir → launch error
}

// newSweepRig: m has two cells (mp perso, stale 3 h; mt work, fresh 10 min),
// j one (jp perso, stale 45 min). Only j is due.
func newSweepRig(t *testing.T) *sweepRig {
	t.Helper()
	r := &sweepRig{fs: &sweepFS{files: map[string][]byte{}}, clock: sweepNow, fail: map[string]error{}}
	cells := []claudeusage.CellInput{
		sweepCell("jp", "perso", "j", 2),
		sweepCell("mp", "perso", "m", 1),
		sweepCell("mt", "gx", "m", 0),
	}
	r.fs.set("/cells/mp", cellFile(uuidSweepM, 3*time.Hour))
	r.fs.set("/cells/mt", cellFile(uuidSweepM, 10*time.Minute))
	r.fs.set("/cells/jp", cellFile(uuidSweepJ, 45*time.Minute))
	uuidOf := map[string]string{"/cells/mp": uuidSweepM, "/cells/mt": uuidSweepM, "/cells/jp": uuidSweepJ}
	r.pass = &usageRefreshSweep{
		loadCells: func() ([]claudeusage.CellInput, error) { return cells, nil },
		readFile:  r.fs.read,
		now:       func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.clock },
		launch: func(_ context.Context, dir string) error {
			r.mu.Lock()
			r.launched = append(r.launched, filepath.Base(dir))
			err := r.fail[dir]
			now := r.clock
			r.mu.Unlock()
			if err != nil {
				return err
			}
			r.fs.set(dir, cellFile(uuidOf[dir], sweepNow.Sub(now))) // fetched at "now"
			return nil
		},
		timeout:  time.Minute,
		stateDir: t.TempDir(),
		lock:     claudeusage.TryRefreshLock,
		logf: func(format string, args ...any) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.logs = append(r.logs, fmt.Sprintf(format, args...))
		},
	}
	return r
}

func (r *sweepRig) state(t *testing.T) claudeusage.BackgroundState {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.pass.stateDir, claudeusage.BackgroundStateName))
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	var st claudeusage.BackgroundState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("state %s: %v", b, err)
	}
	return st
}

// Only the accounts whose reading (newest of all their cells) is older than
// 30 min are launched: j; m is fresh through mt although its target mp is not.
func TestUsageRefreshSweepOnlyStale(t *testing.T) {
	r := newSweepRig(t)
	r.pass.run(context.Background())
	if got := strings.Join(r.launched, ","); got != "jp" {
		t.Fatalf("launched %q, want jp only", got)
	}
}

// Never two claude -p /usage at once: two passes started together run one
// after the other, and inside a pass the accounts follow each other.
func TestUsageRefreshSweepSequential(t *testing.T) {
	r := newSweepRig(t)
	r.fs.set("/cells/mt", cellFile(uuidSweepM, 2*time.Hour)) // m due too
	var inFlight, peak int32
	inner := r.pass.launch
	r.pass.launch = func(ctx context.Context, dir string) error {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		defer atomic.AddInt32(&inFlight, -1)
		// put the cache back as stale so the second pass launches too
		err := inner(ctx, dir)
		r.fs.set(dir, cellFile(map[string]string{"/cells/mt": uuidSweepM, "/cells/mp": uuidSweepM, "/cells/jp": uuidSweepJ}[dir], 2*time.Hour))
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.pass.run(context.Background()) }()
	}
	wg.Wait()
	if peak != 1 {
		t.Fatalf("peak concurrent launches = %d, want 1", peak)
	}
	if len(r.launched) != 4 {
		t.Fatalf("launched %v, want 2 accounts × 2 passes", r.launched)
	}
}

// Another process holds the refresh lock (pigtail usage --refresh): the pass
// launches nothing, writes skipped=locked keeping the previous results, and
// logs it once.
func TestUsageRefreshSweepSkipsWhenLocked(t *testing.T) {
	r := newSweepRig(t)
	r.fail["/cells/jp"] = errors.New("claude exited 1")
	r.pass.run(context.Background()) // a first pass with a failure to keep
	r.launched = nil
	unlock, held, err := claudeusage.TryRefreshLock(r.pass.stateDir)
	if err != nil || held {
		t.Fatalf("lock: held=%v err=%v", held, err)
	}
	defer unlock()
	r.pass.run(context.Background())
	r.pass.run(context.Background())
	if len(r.launched) != 0 {
		t.Fatalf("launched %v while locked", r.launched)
	}
	st := r.state(t)
	if st.Skipped != "locked" || len(st.Results) != 1 || st.Results[0].Status != claudeusage.RefreshFailed || st.ResultsAt == nil || !st.ResultsAt.Equal(sweepNow) {
		t.Fatalf("state = %+v, want skipped=locked with the previous failure kept", st)
	}
	n := 0
	for _, l := range r.logs {
		if strings.Contains(l, "another refresh is running") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("locked logged %d times, want once:\n%s", n, strings.Join(r.logs, "\n"))
	}
}

// The pass's results land in usage-refresh.json, atomically, master view.
func TestUsageRefreshSweepWritesState(t *testing.T) {
	r := newSweepRig(t)
	r.clock = sweepNow.Add(time.Minute)
	r.pass.run(context.Background())
	st := r.state(t)
	if !st.At.Equal(sweepNow.Add(time.Minute)) || st.ResultsAt == nil || !st.ResultsAt.Equal(st.At) || st.Skipped != "" || st.Error != "" {
		t.Fatalf("state = %+v", st)
	}
	if len(st.Results) != 1 {
		t.Fatalf("results = %+v, want j only", st.Results)
	}
	x := st.Results[0]
	if x.Label != "j" || x.AccountUUID != uuidSweepJ || x.Cell != "jp" || x.CellScope != "perso" || x.Status != claudeusage.RefreshOK || x.FetchedAfter == nil || !x.FetchedAfter.Equal(sweepNow.Add(time.Minute)) {
		t.Fatalf("result = %+v", x)
	}
	if ents, _ := os.ReadDir(r.pass.stateDir); len(ents) != 2 { // the state and the lock, no temp left
		t.Fatalf("state dir holds %v", ents)
	}
}

// A failure is logged when it appears, not at every pass; once a day while
// it lasts; again as soon as the error changes.
func TestUsageRefreshSweepLogsFailureOnce(t *testing.T) {
	r := newSweepRig(t)
	r.fail["/cells/jp"] = errors.New("claude not found on PATH")
	count := func() int {
		n := 0
		for _, l := range r.logs {
			if strings.Contains(l, "background usage refresh failed: j (cell jp)") {
				n++
			}
		}
		return n
	}
	r.pass.run(context.Background())
	r.clock = r.clock.Add(10 * time.Minute)
	r.pass.run(context.Background())
	if count() != 1 {
		t.Fatalf("after 2 passes: %d lines, want 1:\n%s", count(), strings.Join(r.logs, "\n"))
	}
	r.clock = r.clock.Add(25 * time.Hour)
	r.pass.run(context.Background())
	if count() != 2 || !strings.Contains(r.logs[len(r.logs)-1], "STILL") {
		t.Fatalf("daily reminder missing:\n%s", strings.Join(r.logs, "\n"))
	}
	r.fail["/cells/jp"] = errors.New("claude exited 1")
	r.pass.run(context.Background())
	if count() != 3 {
		t.Fatalf("changed error not logged:\n%s", strings.Join(r.logs, "\n"))
	}
}

// No cell table: said once, nothing launched, the state says why.
func TestUsageRefreshSweepNoTable(t *testing.T) {
	r := newSweepRig(t)
	r.pass.loadCells = func() ([]claudeusage.CellInput, error) {
		return nil, errors.New("read claude-cells.yaml: no such file (regenerate it)")
	}
	r.pass.run(context.Background())
	r.pass.run(context.Background())
	if len(r.launched) != 0 {
		t.Fatalf("launched %v without a table", r.launched)
	}
	if len(r.logs) != 1 || !strings.Contains(r.logs[0], "claude-cells.yaml") {
		t.Fatalf("logs = %q, want one line naming the table", r.logs)
	}
	if st := r.state(t); !strings.Contains(st.Error, "claude-cells.yaml") {
		t.Fatalf("state = %+v", st)
	}
}

// -usage-refresh=false: said once, the sweep never starts.
func TestUsageRefreshSweepDisabled(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	s := &Server{cfg: Config{UsageRefreshDisabled: true}}
	done := make(chan struct{})
	go func() { s.runUsageRefresh(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled sweep did not return")
	}
	if !strings.Contains(buf.String(), "usage refresh disabled") {
		t.Fatalf("log = %q", buf.String())
	}
}

// withHistory gives the rig a usage history in its own temp dir.
func (r *sweepRig) withHistory(t *testing.T) *usagehistory.History {
	t.Helper()
	h, err := usagehistory.Open(filepath.Join(t.TempDir(), usagehistory.FileName), sweepNow)
	if err != nil {
		t.Fatal(err)
	}
	r.pass.history = h
	return h
}

func samplesOf(ss []claudeusage.Sample, uuid string) []claudeusage.Sample {
	var out []claudeusage.Sample
	for _, s := range ss {
		if s.AccountUUID == uuid {
			out = append(out, s)
		}
	}
	return out
}

// Every pass records one sample per account with a reading, At = the pass,
// whether or not the account was refreshed.
func TestSweepRecordsOneSamplePerAccountPerPass(t *testing.T) {
	r := newSweepRig(t)
	h := r.withHistory(t)
	r.pass.run(context.Background())
	r.clock = sweepNow.Add(10 * time.Minute)
	r.pass.run(context.Background())
	all := h.Since(time.Time{})
	if len(all) != 4 {
		t.Fatalf("samples = %+v, want 2 accounts × 2 passes", all)
	}
	for _, uuid := range []string{uuidSweepM, uuidSweepJ} {
		got := samplesOf(all, uuid)
		if len(got) != 2 || !got[0].At.Equal(sweepNow) || !got[1].At.Equal(sweepNow.Add(10*time.Minute)) {
			t.Fatalf("%s samples = %+v, want At at each pass", uuid[:8], got)
		}
	}
}

// The sample is taken after the refresh: j's carries the reading the fake
// launcher just wrote.
func TestSweepRecordsAfterRefresh(t *testing.T) {
	r := newSweepRig(t)
	h := r.withHistory(t)
	r.clock = sweepNow.Add(time.Minute)
	r.pass.run(context.Background())
	j := samplesOf(h.Since(time.Time{}), uuidSweepJ)
	if len(j) != 1 || !j[0].FetchedAt.Equal(sweepNow.Add(time.Minute)) {
		t.Fatalf("j samples = %+v, want fetched_at = the refresh", j)
	}
	if len(r.launched) != 1 {
		t.Fatalf("launched %v", r.launched)
	}
}

// The lock held by another refresh skips the launches, not the record.
func TestSweepRecordsWhenLockHeld(t *testing.T) {
	r := newSweepRig(t)
	h := r.withHistory(t)
	unlock, held, err := claudeusage.TryRefreshLock(r.pass.stateDir)
	if err != nil || held {
		t.Fatalf("lock: held=%v err=%v", held, err)
	}
	defer unlock()
	r.pass.run(context.Background())
	if len(r.launched) != 0 {
		t.Fatalf("launched %v while locked", r.launched)
	}
	if got := h.Since(time.Time{}); len(got) != 2 {
		t.Fatalf("samples = %+v, want m and j", got)
	}
}

// No cell table: nothing to record.
func TestSweepRecordsNothingWithoutTable(t *testing.T) {
	r := newSweepRig(t)
	h := r.withHistory(t)
	r.pass.loadCells = func() ([]claudeusage.CellInput, error) { return nil, errors.New("no table") }
	r.pass.run(context.Background())
	if got := h.Since(time.Time{}); len(got) != 0 {
		t.Fatalf("samples = %+v without a table", got)
	}
}

// A history that cannot be written is logged once, not at every pass.
func TestSweepHistoryFailureLoggedOnce(t *testing.T) {
	r := newSweepRig(t)
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	h, err := usagehistory.Open(filepath.Join(blocker, usagehistory.FileName), sweepNow)
	if err != nil {
		t.Fatal(err)
	}
	// A file where the history's dir should be: every append fails.
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.pass.history = h
	r.pass.run(context.Background())
	r.clock = sweepNow.Add(10 * time.Minute)
	r.pass.run(context.Background())
	n := 0
	for _, l := range r.logs {
		if strings.HasPrefix(l, "daemon: usage history: ") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("history failure logged %d times, want once:\n%s", n, strings.Join(r.logs, "\n"))
	}
}

// No state dir: the pass returns before the lock and writes nothing at all
// (not even in the working directory).
func TestSweepRunEmptyStateDirWritesNothing(t *testing.T) {
	r := newSweepRig(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	r.pass.stateDir = ""
	r.pass.lock = func(string) (func(), bool, error) {
		t.Fatal("lock taken without a state dir")
		return func() {}, false, nil
	}
	r.pass.run(context.Background())
	if len(r.launched) != 0 {
		t.Fatalf("launched %v without a state dir", r.launched)
	}
	if ents, _ := os.ReadDir(cwd); len(ents) != 0 {
		t.Fatalf("working dir holds %v", ents)
	}
}

func TestSweepStaleAfter(t *testing.T) {
	for _, tc := range []struct{ interval, want time.Duration }{
		{10 * time.Minute, 20 * time.Minute},
		{0, 30 * time.Minute},
		{claudeusage.PickFreshFor, time.Minute},
		{time.Hour, time.Minute},
	} {
		if got := sweepStaleAfter(tc.interval); got != tc.want {
			t.Errorf("sweepStaleAfter(%s) = %s, want %s", tc.interval, got, tc.want)
		}
	}
}

// The sweep relaunches early enough for no reading to pass PickFreshFor between
// two passes; what remains is the length of one relaunch (D7, the safe side).
func TestUsageRefreshSweepKeepsReadingsFresh(t *testing.T) {
	t.Run("the threshold is the sweep's, not the freshness", func(t *testing.T) {
		r := newSweepRig(t)
		r.pass.staleAfter = sweepStaleAfter(10 * time.Minute)
		r.fs.set("/cells/jp", cellFile(uuidSweepJ, 25*time.Minute))
		r.fs.set("/cells/mp", cellFile(uuidSweepM, 15*time.Minute))
		r.fs.set("/cells/mt", cellFile(uuidSweepM, 15*time.Minute))
		r.pass.run(context.Background())
		if got := strings.Join(r.launched, ","); got != "jp" {
			t.Fatalf("launched %q, want jp only", got)
		}
	})

	const relaunch = 30 * time.Second // one claude -p /usage, simulated
	for _, initial := range []time.Duration{29 * time.Minute, 20 * time.Minute} {
		t.Run(fmt.Sprintf("four passes from a reading of %s", initial), func(t *testing.T) {
			r := newSweepRig(t)
			r.pass.staleAfter = sweepStaleAfter(10 * time.Minute)
			r.pass.loadCells = func() ([]claudeusage.CellInput, error) {
				return []claudeusage.CellInput{sweepCell("jp", "perso", "j", 2)}, nil
			}
			fetched := sweepNow.Add(-initial)
			r.fs.set("/cells/jp", cellFile(uuidSweepJ, initial))
			var widest time.Duration
			r.pass.launch = func(_ context.Context, dir string) error {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.launched = append(r.launched, filepath.Base(dir))
				r.clock = r.clock.Add(relaunch) // the reading ages while claude runs
				widest = max(widest, r.clock.Sub(fetched))
				fetched = r.clock
				r.fs.set(dir, cellFile(uuidSweepJ, sweepNow.Sub(r.clock)))
				return nil
			}
			for i := 0; i < 4; i++ {
				at := sweepNow.Add(time.Duration(i) * 10 * time.Minute)
				r.mu.Lock()
				r.clock = at
				age := at.Sub(fetched)
				r.mu.Unlock()
				if age > claudeusage.PickFreshFor {
					t.Fatalf("pass %d: the reading is %s old, past %s", i, age, claudeusage.PickFreshFor)
				}
				r.pass.run(context.Background())
			}
			if len(r.launched) == 0 {
				t.Fatal("nothing relaunched")
			}
			if widest > claudeusage.PickFreshFor+relaunch {
				t.Fatalf("a reading reached %s during a relaunch, past %s + one relaunch", widest, claudeusage.PickFreshFor)
			}
		})
	}
}

// The daemon's sweep runs on the threshold, not on the zero value's PickFreshFor.
func TestArmedUsageRefreshSweepUsesTheThreshold(t *testing.T) {
	s := &Server{cfg: Config{UsageStateDir: t.TempDir()}}
	if got := s.armedUsageRefreshSweep(defaultUsageRefreshInterval).staleAfter; got != 20*time.Minute {
		t.Fatalf("staleAfter = %s, want 20m", got)
	}
}
