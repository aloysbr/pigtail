package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemonwatch"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// wdRig is a fake pigtaild on a unix socket plus the dirs the watchdog reads.
type wdRig struct {
	t      *testing.T
	dir    string // short: unix socket paths are capped near 100 bytes
	db     string
	mu     sync.Mutex
	health func(w http.ResponseWriter, r *http.Request) // guarded by mu: a slow handler outlives its probe
	posts  []pigtail.JournalEntry
	kicks  []recorded
	deadl  []bool
}

type recorded struct {
	name string
	args []string
}

func newWDRig(t *testing.T) *wdRig {
	t.Helper()
	testutil.SerialOnly(t)
	withCells(t, func(_, perso string) string { return perso })
	dir := testutil.MkdirTemp(t, "wd-")
	r := &wdRig{t: t, dir: dir, db: filepath.Join(dir, "pigtail.db")}
	r.setHealth(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", dir)
	t.Setenv("PIGTAIL_SOCKET", "")
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	prevSock, prevHTTP, prevRunner, prevTimeout := flagSocket, flagHTTP, watchdogRunner, watchdogProbeTimeout
	flagSocket, flagHTTP = "", ""
	watchdogProbeTimeout = 300 * time.Millisecond
	watchdogRunner = func(ctx context.Context, name string, args ...string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, has := ctx.Deadline()
		r.kicks = append(r.kicks, recorded{name, args})
		r.deadl = append(r.deadl, has)
		return nil
	}
	t.Cleanup(func() {
		flagSocket, flagHTTP, watchdogRunner, watchdogProbeTimeout = prevSock, prevHTTP, prevRunner, prevTimeout
	})
	return r
}

func (r *wdRig) serve() {
	r.t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) { r.getHealth()(w, req) })
	mux.HandleFunc("/journal", func(w http.ResponseWriter, req *http.Request) {
		var e pigtail.JournalEntry
		_ = json.NewDecoder(req.Body).Decode(&e)
		if err := e.Validate(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		r.mu.Lock()
		r.posts = append(r.posts, e)
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(pigtail.JournalAddResult{Created: true, Entry: e})
	})
	ln, err := net.Listen("unix", filepath.Join(r.dir, "pigtail.sock"))
	if err != nil {
		r.t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	r.t.Cleanup(srv.Close)
}

func (r *wdRig) run(args ...string) (string, error) {
	r.t.Helper()
	cmd := daemonWatchdogCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	flagSocket = "" // each run derives it from --db, like launchd's
	cmd.SetArgs(append([]string{"--db", r.db}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func (r *wdRig) state() daemonwatch.State {
	r.t.Helper()
	st, err := daemonwatch.LoadState(filepath.Join(r.dir, "state", "pigtail", "daemon-watchdog.json"))
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

func TestWatchdogProbeHealthyWedged(t *testing.T) {
	r := newWDRig(t)
	r.serve()
	sock := filepath.Join(r.dir, "pigtail.sock")
	now := time.Now()
	if res, _ := watchdogProbe(context.Background(), sock, r.db, now); res != daemonwatch.Healthy {
		t.Fatalf("200 → %v", res)
	}
	r.setHealth(func(w http.ResponseWriter, _ *http.Request) { time.Sleep(time.Second) })
	if res, d := watchdogProbe(context.Background(), sock, r.db, now); res != daemonwatch.Wedged || d != "timeout" {
		t.Fatalf("slow → %v %q", res, d)
	}
	r.setHealth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"data":{"ok":false,"failing":["store"]}}`))
	})
	if res, d := watchdogProbe(context.Background(), sock, r.db, now); res != daemonwatch.Wedged || d != "503 failing=[store]" {
		t.Fatalf("503 → %v %q", res, d)
	}
}

func TestWatchdogProbeRefusedLockDeadIsDown(t *testing.T) {
	r := newWDRig(t)
	sock := filepath.Join(r.dir, "pigtail.sock")
	if res, _ := watchdogProbe(context.Background(), sock, r.db, time.Now()); res != daemonwatch.Down {
		t.Fatalf("no lock → %v", res)
	}
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.db+".lock", []byte(strconv.Itoa(c.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, _ := watchdogProbe(context.Background(), sock, r.db, time.Now()); res != daemonwatch.Down {
		t.Fatalf("dead pid → %v", res)
	}
}

func TestWatchdogProbeRefusedLockAliveIsWedged(t *testing.T) {
	r := newWDRig(t)
	sock := filepath.Join(r.dir, "pigtail.sock")
	if err := os.WriteFile(r.db+".lock", []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-11 * time.Minute)
	if err := os.Chtimes(r.db+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	if res, d := watchdogProbe(context.Background(), sock, r.db, now); res != daemonwatch.Wedged || !strings.Contains(d, "refused while pid") {
		t.Fatalf("11 min → %v %q", res, d)
	}
	young := now.Add(-time.Minute)
	if err := os.Chtimes(r.db+".lock", young, young); err != nil {
		t.Fatal(err)
	}
	if res, _ := watchdogProbe(context.Background(), sock, r.db, now); res != daemonwatch.Starting {
		t.Fatalf("1 min → %v", res)
	}
}

func (r *wdRig) wedge() {
	r.setHealth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"data":{"ok":false,"failing":["roles-sweep"]}}`))
	})
}

func TestWatchdogCommandKicksFakeRunner(t *testing.T) {
	r := newWDRig(t)
	r.serve()
	r.wedge()
	for i := 0; i < 2; i++ {
		if _, err := r.run(); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.kicks) != 0 {
		t.Fatalf("kicked after two wedged runs: %v", r.kicks)
	}
	if _, err := r.run(); err != nil {
		t.Fatal(err)
	}
	if len(r.kicks) != 1 || r.kicks[0].name != "launchctl" || r.kicks[0].args[0] != "kickstart" || !r.deadl[0] {
		t.Fatalf("kicks = %v deadline=%v", r.kicks, r.deadl)
	}
	if st := r.state(); len(st.Kicks) != 1 || len(st.Unreported) != 1 {
		t.Fatalf("state = %+v", st)
	}
}

func TestWatchdogSkipsUnderRuntimeClaim(t *testing.T) {
	r := newWDRig(t)
	r.serve()
	r.wedge()
	now := time.Now()
	claim := func(expires time.Time) {
		b, _ := json.Marshal(map[string]any{"id": "rt_1", "reason": "test", "since": now.Add(-time.Hour), "expires_at": expires})
		if err := os.WriteFile(filepath.Join(r.dir, "runtime-claim.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	claim(now.Add(time.Hour))
	for i := 0; i < 3; i++ {
		if _, err := r.run(); err != nil {
			t.Fatal(err)
		}
	}
	if st := r.state(); len(r.kicks) != 0 || !strings.Contains(st.LastSkip, "runtime claim") {
		t.Fatalf("kicks=%v skip=%q", r.kicks, st.LastSkip)
	}
	// An expired lease never holds the watchdog (R3-1).
	claim(now.Add(-time.Minute))
	for i := 0; i < 3; i++ {
		if _, err := r.run(); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.kicks) != 1 {
		t.Fatalf("kicks after an expired claim = %d, want 1", len(r.kicks))
	}
}

func TestWatchdogPostsKickWhenHealthy(t *testing.T) {
	r := newWDRig(t)
	r.serve()
	r.wedge()
	watchdogRunnerErr := strings.Repeat("é", 400) + "\nsecond line"
	watchdogRunner = func(context.Context, string, ...string) error { return errString(watchdogRunnerErr) }
	for i := 0; i < 3; i++ {
		if _, err := r.run(); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.posts) != 0 {
		t.Fatal("posted while wedged")
	}
	r.setHealth(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	if _, err := r.run(); err != nil {
		t.Fatal(err)
	}
	if len(r.posts) != 1 {
		t.Fatalf("posts = %+v", r.posts)
	}
	e := r.posts[0]
	if e.Kind != "daemon-watchdog" || !strings.HasPrefix(e.ID, "daemon-watchdog:kick:") ||
		!strings.HasPrefix(e.Source, "pigtail:daemon-watchdog?kind=kick") || len([]rune(e.Summary)) > pigtail.JournalSummaryMax {
		t.Fatalf("entry = %+v", e)
	}
	if st := r.state(); len(st.Unreported) != 0 {
		t.Fatalf("unreported kept: %+v", st.Unreported)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestWatchdogStatusFlagsStaleRun(t *testing.T) {
	r := newWDRig(t)
	p := filepath.Join(r.dir, "state", "pigtail", "daemon-watchdog.json")
	if err := daemonwatch.SaveState(p, daemonwatch.State{LastRun: time.Now().Add(-5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	out, err := r.run("--status")
	if err == nil || !strings.Contains(out, `"stale": true`) {
		t.Fatalf("5 min: %q %v", out, err)
	}
	if err := daemonwatch.SaveState(p, daemonwatch.State{LastRun: time.Now().Add(-30 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	out, err = r.run("--status")
	if err != nil || !strings.Contains(out, `"stale": false`) {
		t.Fatalf("30 s: %q %v", out, err)
	}
}

func TestDaemonCmdRegistered(t *testing.T) {
	root := newRootCmd()
	c, _, err := root.Find([]string{"daemon", "watchdog"})
	if err != nil || c.Name() != "watchdog" {
		t.Fatalf("find: %v %v", c, err)
	}
}

func (r *wdRig) setHealth(h func(w http.ResponseWriter, r *http.Request)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.health = h
}

func (r *wdRig) getHealth() func(w http.ResponseWriter, r *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.health
}
