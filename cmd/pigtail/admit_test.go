package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/admission"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/internal/testrun"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type roomy struct{}

func (roomy) ReadMemory() (sysmem.Memory, error) {
	return sysmem.Memory{Available: 60 << 30, Pressure: 1, Total: 64 << 30}, nil
}
func (roomy) Procs() (map[int]sysmem.Proc, error) { return map[int]sysmem.Proc{}, nil }

// envelopeResponder mimics the daemon's: {"data": …} / {"error": …}.
type envelopeResponder struct{}

func (envelopeResponder) JSON(w http.ResponseWriter, _ *http.Request, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = jsonEncode(w, map[string]any{"data": data})
}

func (envelopeResponder) Error(w http.ResponseWriter, _ *http.Request, status int, code pigtail.ErrorCode, msg string, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	e := map[string]any{"code": code, "message": msg}
	if details != nil {
		e["details"] = details // as the daemon's writeError does
	}
	_ = jsonEncode(w, map[string]any{"error": e})
}

// startAdmissionDaemon serves the real admission handlers on a Unix socket
// and points this process's client at it.
func startAdmissionDaemon(t *testing.T, maxJobs int) *admission.Controller {
	t.Helper()
	return startAdmissionDaemonWith(t, maxJobs)
}

// startAdmissionDaemonWith is startAdmissionDaemon with extra controller
// options (a history, class limits).
func startAdmissionDaemonWith(t *testing.T, maxJobs int, opts ...admission.Option) *admission.Controller {
	t.Helper()
	c := admission.New(events.New(), append([]admission.Option{
		admission.WithConfig(admission.Config{MaxJobs: maxJobs, FloorBytes: 6 << 30, MaxHold: time.Hour}),
		admission.WithSampler(roomy{}),
		admission.WithLogger(func(string, ...any) {})}, opts...)...)
	r := chi.NewRouter()
	handlers.RegisterAdmissionRoutes(r, &handlers.Services{Admission: c}, envelopeResponder{})
	dir := testutil.MkdirTemp(t, "admit-") // short: the 104-byte socket path cap
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r}
	go func() { _ = srv.Serve(ln) }()
	pointClientAt(t, sock)
	t.Cleanup(func() { _ = srv.Close(); _ = os.RemoveAll(dir) })
	return c
}

func pointClientAt(t *testing.T, sock string) {
	t.Helper()
	testutil.SerialOnly(t) // swaps flagHTTP
	prevHTTP, prevSocket := flagHTTP, flagSocket
	flagHTTP, flagSocket = "", sock
	t.Setenv(envAdmit, "")
	t.Setenv(envAdmitLease, "")
	t.Cleanup(func() { flagHTTP, flagSocket = prevHTTP, prevSocket })
}

func TestAdmitGrantedAtOnceIsSilent(t *testing.T) {
	c := startAdmissionDaemon(t, 2)
	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "tsc", maxWait: time.Second, onTimeout: "fail"})
	if abort || lease == nil {
		t.Fatalf("lease=%v abort=%v", lease, abort)
	}
	if stderr.Len() != 0 {
		t.Fatalf("an immediate grant must print nothing, got %q", stderr.String())
	}
	if n := len(c.Status().Leases); n != 1 {
		t.Fatalf("leases = %d", n)
	}
	lease.Release()
}

func TestAdmitTimesOutWithExit75(t *testing.T) {
	c := startAdmissionDaemon(t, 1)
	blocker, err := c.Enqueue(admission.Request{Group: "other", Class: "jest"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release(c.Grant(blocker))

	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "tsc", maxWait: 300 * time.Millisecond, onTimeout: "fail"})
	if !abort || lease != nil {
		t.Fatalf("lease=%v abort=%v, want an abort", lease, abort)
	}
	out := stderr.String()
	for _, want := range []string{"queued", "position 1/1", "ceiling", "gave up", "Exit 75"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q:\n%s", want, out)
		}
	}

	stderr.Reset()
	lease, abort = admit(&stderr, t.TempDir(), admitOptions{class: "tsc", maxWait: 200 * time.Millisecond, onTimeout: "run"})
	if abort || lease != nil || !strings.Contains(stderr.String(), "running anyway") {
		t.Fatalf("--on-timeout=run: lease=%v abort=%v stderr=%q", lease, abort, stderr.String())
	}
}

// A queue held by a class cap gives up naming that cap, never a memory
// shortage the machine does not have.
func TestAdmitTimesOutWithExit75OnAClassLimit(t *testing.T) {
	c := startAdmissionDaemonWith(t, 4, admission.WithConfig(admission.Config{
		MaxJobs: 4, FloorBytes: 6 << 30, MaxHold: time.Hour,
		ClassLimits: map[string]int{"go-test": 1},
	}))
	blocker, err := c.Enqueue(admission.Request{Group: "other", Class: "go-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release(c.Grant(blocker))

	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "go-test", maxWait: 300 * time.Millisecond, onTimeout: "fail"})
	if !abort || lease != nil {
		t.Fatalf("lease=%v abort=%v, want an abort", lease, abort)
	}
	out := stderr.String()
	for _, want := range []string{"gave up after", "another go-test command held the queue", "Exit 75"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "short of memory") {
		t.Errorf("a class cap is not a memory shortage:\n%s", out)
	}
}

func TestAdmitFailsOpen(t *testing.T) {
	pointClientAt(t, filepath.Join(t.TempDir(), "absent.sock"))
	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "tsc", maxWait: time.Second, onTimeout: "fail"})
	if abort || lease != nil || !strings.Contains(stderr.String(), "running unadmitted") {
		t.Fatalf("no daemon: lease=%v abort=%v stderr=%q", lease, abort, stderr.String())
	}

	startAdmissionDaemon(t, 0) // running, admission switched off
	stderr.Reset()
	lease, abort = admit(&stderr, t.TempDir(), admitOptions{class: "tsc", maxWait: time.Second, onTimeout: "fail"})
	if abort || lease != nil || stderr.Len() != 0 {
		t.Fatalf("disabled: lease=%v abort=%v stderr=%q — must run silently", lease, abort, stderr.String())
	}
}

func TestAdmitSkipsWhenNestedOrOptedOut(t *testing.T) {
	c := startAdmissionDaemon(t, 2)
	// The suite itself may run under a generic envelope (pre-push, `pigtail
	// admit -- go test`): an inherited class turns adm-7 into an outer lease
	// that a tsc nests under, which is another case than the one tested here.
	t.Setenv(envAdmitClass, "")
	for _, env := range [][2]string{{envAdmitLease, "adm-7"}, {envAdmit, "0"}} {
		t.Setenv(env[0], env[1])
		lease, abort := admit(&bytes.Buffer{}, t.TempDir(), admitOptions{class: "tsc", maxWait: time.Second, onTimeout: "fail"})
		if abort || lease != nil || len(c.Status().Leases) != 0 {
			t.Fatalf("%s=%s must run without a lease", env[0], env[1])
		}
		t.Setenv(env[0], "")
	}
}

func TestRunAdmittedPropagatesExitCodeAndLease(t *testing.T) {
	c := startAdmissionDaemon(t, 2)
	out := filepath.Join(t.TempDir(), "env")
	code := runAdmitted([]string{"sh", "-c", `echo "$PIGTAIL_ADMIT_LEASE" > "$0"; exit 7`, out},
		admitOptions{maxWait: time.Second, onTimeout: "fail"})
	if code != 7 {
		t.Fatalf("exit code = %d, want the command's own 7", code)
	}
	got, _ := os.ReadFile(out)
	if !strings.HasPrefix(string(got), "adm-") {
		t.Fatalf("the command must see its lease id, got %q", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(c.Status().Leases) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the lease was not released after the command exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, missing := range []string{"/nonexistent/cmd", "pigtail-no-such-command-on-path"} {
		if code := runAdmitted([]string{missing}, admitOptions{maxWait: time.Second, onTimeout: "fail"}); code != 127 {
			t.Fatalf("%s: a missing command must exit 127 like the shell, got %d", missing, code)
		}
	}
}

func TestDescribeQueued(t *testing.T) {
	t.Parallel()
	s := describeQueued("tsc", pigtail.AdmissionUpdate{
		State: pigtail.AdmissionQueued, Position: 2, Queued: 5, Running: 4, Reason: "memory",
		NeedBytes: 3 << 29, EffectiveBytes: 4 << 30, FloorBytes: 6 << 30,
	})
	for _, want := range []string{"tsc (~1.5 GiB)", "position 2/5", "4 heavy", "4.0 GiB free", "6.0 GiB floor"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q lacks %q", s, want)
		}
	}

	s = describeQueued("go-test", pigtail.AdmissionUpdate{
		State: pigtail.AdmissionQueued, Position: 1, Queued: 1, Running: 1, Reason: "class_limit",
	})
	if want := "waiting for a go-test turn"; !strings.Contains(s, want) {
		t.Errorf("%q lacks %q", s, want)
	}
}

func TestDescribeQueuedNamesTheDisk(t *testing.T) {
	t.Parallel()
	s := describeQueued("go-test", pigtail.AdmissionUpdate{
		State: pigtail.AdmissionQueued, Position: 1, Queued: 1, Reason: "disk",
		DiskFreeBytes: 12 << 30, DiskFloorBytes: 20 << 30,
	})
	for _, want := range []string{"disk: 12 GB free < 20 GB floor"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q lacks %q", s, want)
		}
	}
	if got := giveUpCause("go-test", "disk"); !strings.Contains(got, "disk") {
		t.Errorf("giveUpCause(disk) = %q, must name the disk", got)
	}
}

func TestAdmissionGroupFallsBackToGitRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	group, repo := admissionGroup(sub)
	if group != filepath.Base(root) || repo != root {
		t.Fatalf("group=%q repo=%q", group, repo)
	}
}

func jsonEncode(w http.ResponseWriter, v any) error { return json.NewEncoder(w).Encode(v) }

// At an interactive terminal the default wait is unlimited: a human watching
// the queue would only retype the command a 75 sent them back to.
func TestResolveWaitInteractiveRule(t *testing.T) {
	prev := interactive
	t.Cleanup(func() { interactive = prev })

	interactive = func() bool { return true }
	if got := resolveWait(admitOptions{maxWait: 10 * time.Minute}); got != 0 {
		t.Fatalf("interactive, flag unset: got %s, want no limit", got)
	}
	if got := resolveWait(admitOptions{maxWait: 3 * time.Minute, maxWaitSet: true}); got != 3*time.Minute {
		t.Fatalf("an explicit --max-wait must win at a terminal too, got %s", got)
	}

	interactive = func() bool { return false }
	if got := resolveWait(admitOptions{maxWait: 10 * time.Minute}); got != 10*time.Minute {
		t.Fatalf("non-interactive (an agent): got %s, want the default budget", got)
	}
	if got := resolveWait(admitOptions{maxWait: 0, maxWaitSet: true}); got != 0 {
		t.Fatalf("--max-wait=0 must still mean forever, got %s", got)
	}
}

func TestAdmitWaitsWithoutLimitAtATerminal(t *testing.T) {
	c := startAdmissionDaemon(t, 1)
	blocker, err := c.Enqueue(admission.Request{Group: "other", Class: "jest"})
	if err != nil {
		t.Fatal(err)
	}
	prev := interactive
	interactive = func() bool { return true }
	t.Cleanup(func() { interactive = prev })

	type result struct {
		lease *pigtail.AdmissionLease
		abort bool
	}
	done := make(chan result, 1)
	go func() {
		var stderr bytes.Buffer
		l, a := admit(&stderr, t.TempDir(), admitOptions{class: "tsc", maxWait: 200 * time.Millisecond, onTimeout: "fail"})
		done <- result{l, a}
	}()
	// Well past the budget it would have used had it not been interactive.
	select {
	case r := <-done:
		t.Fatalf("gave up despite the terminal: lease=%v abort=%v", r.lease, r.abort)
	case <-time.After(600 * time.Millisecond):
	}
	c.Release(c.Grant(blocker))
	select {
	case r := <-done:
		if r.abort || r.lease == nil {
			t.Fatalf("lease=%v abort=%v", r.lease, r.abort)
		}
		r.lease.Release()
	case <-time.After(3 * time.Second):
		t.Fatal("never admitted after the blocker left")
	}
}

func TestSocketPathPrefersTheFlagThenTheEnvironment(t *testing.T) {
	prev := flagSocket
	t.Cleanup(func() { flagSocket = prev })

	flagSocket = "/from/flag"
	t.Setenv("PIGTAIL_SOCKET", "/from/env")
	if got := socketPath(); got != "/from/flag" {
		t.Fatalf("got %q, want the flag to win", got)
	}
	flagSocket = ""
	if got := socketPath(); got != "/from/env" {
		t.Fatalf("got %q, want the environment", got)
	}
	t.Setenv("PIGTAIL_SOCKET", "")
	if got := socketPath(); got != "" {
		t.Fatalf("got %q, want the client's own default", got)
	}
}

// A command admitted here may itself call pigtail: it must reach the same
// daemon, not the machine's default one.
func TestRunAdmittedPassesTheSocketDown(t *testing.T) {
	startAdmissionDaemon(t, 2)
	out := filepath.Join(t.TempDir(), "sock")
	if code := runAdmitted([]string{"sh", "-c", `echo "$PIGTAIL_SOCKET" > "$0"`, out},
		admitOptions{maxWait: time.Second, onTimeout: "fail"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	got, _ := os.ReadFile(out)
	if strings.TrimSpace(string(got)) != flagSocket {
		t.Fatalf("child saw PIGTAIL_SOCKET=%q, want %q", strings.TrimSpace(string(got)), flagSocket)
	}
}

func TestAdmitClassHelpNamesGoTest(t *testing.T) {
	t.Parallel()
	usage := admitCmd().Flags().Lookup("class").Usage
	if !strings.Contains(usage, "go-test") {
		t.Fatalf("--class help must name go-test, got %q", usage)
	}
}

func TestDescribeQueuedCompress(t *testing.T) {
	t.Parallel()
	s := describeQueued("tsc", pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1, Running: 3, Reason: "compress"})
	if !strings.HasSuffix(s, ", waiting on compressor traffic") {
		t.Fatalf("%q does not name the compressor", s)
	}
}

// startCapturingDaemon is startAdmissionDaemon that also hands back the body
// of every POST /admission/leases it served.
func startCapturingDaemon(t *testing.T, maxJobs int) func() []pigtail.AdmissionRequest {
	t.Helper()
	c := admission.New(events.New(),
		admission.WithConfig(admission.Config{MaxJobs: maxJobs, FloorBytes: 6 << 30, MaxHold: time.Hour}),
		admission.WithSampler(roomy{}),
		admission.WithLogger(func(string, ...any) {}))
	var mu sync.Mutex
	var got []pigtail.AdmissionRequest
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodPost {
				body, _ := io.ReadAll(req.Body)
				req.Body = io.NopCloser(bytes.NewReader(body))
				var ar pigtail.AdmissionRequest
				if json.Unmarshal(body, &ar) == nil {
					mu.Lock()
					got = append(got, ar)
					mu.Unlock()
				}
				// Raw JSON keys, to prove omitempty.
				captureRaw(body)
			}
			next.ServeHTTP(w, req)
		})
	})
	handlers.RegisterAdmissionRoutes(r, &handlers.Services{Admission: c}, envelopeResponder{})
	dir := testutil.MkdirTemp(t, "admit-")
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r}
	go func() { _ = srv.Serve(ln) }()
	pointClientAt(t, sock)
	t.Cleanup(func() { _ = srv.Close(); _ = os.RemoveAll(dir) })
	return func() []pigtail.AdmissionRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]pigtail.AdmissionRequest(nil), got...)
	}
}

var (
	rawMu     sync.Mutex
	rawBodies []string
)

func captureRaw(b []byte) { rawMu.Lock(); rawBodies = append(rawBodies, string(b)); rawMu.Unlock() }

func TestAdmitRequestCarriesIdentity(t *testing.T) {
	testutil.SerialOnly(t) // swaps rawBodies
	served := startCapturingDaemon(t, 4)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-123")
	t.Setenv("CMUX_WORKSPACE_ID", "AAAA-BBBB")
	cwd := t.TempDir()
	lease, abort := admit(&bytes.Buffer{}, cwd, admitOptions{class: "tsc", maxWait: time.Second, onTimeout: "fail"})
	if abort || lease == nil {
		t.Fatalf("lease=%v abort=%v", lease, abort)
	}
	defer lease.Release()
	reqs := served()
	if len(reqs) != 1 {
		t.Fatalf("requests = %+v", reqs)
	}
	if r := reqs[0]; r.SessionID != "sess-123" || r.CmuxWorkspaceID != "AAAA-BBBB" || r.Cwd != cwd {
		t.Fatalf("request = %+v, want the session, the cmux workspace and the cwd", r)
	}
}

func TestAdmitRequestWithoutEnv(t *testing.T) {
	startCapturingDaemon(t, 4)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CMUX_WORKSPACE_ID", "")
	rawMu.Lock()
	rawBodies = nil
	rawMu.Unlock()
	lease, abort := admit(&bytes.Buffer{}, t.TempDir(), admitOptions{class: "tsc", maxWait: time.Second, onTimeout: "fail"})
	if abort || lease == nil {
		t.Fatalf("lease=%v abort=%v", lease, abort)
	}
	defer lease.Release()
	rawMu.Lock()
	defer rawMu.Unlock()
	if len(rawBodies) != 1 {
		t.Fatalf("bodies = %q", rawBodies)
	}
	for _, k := range []string{"session_id", "cmux_workspace_id"} {
		if strings.Contains(rawBodies[0], k) {
			t.Fatalf("empty %s must be omitted: %s", k, rawBodies[0])
		}
	}
}

// pigtail test goes through the same admit() with the plan's cwd, which is
// not the process's working directory.
func TestPigtailTestLeaseCarriesIdentity(t *testing.T) {
	served := startCapturingDaemon(t, 4)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-t")
	planCwd := t.TempDir()
	if wd, _ := os.Getwd(); wd == planCwd {
		t.Fatal("setup: the plan's cwd must differ from the process's")
	}
	lease, _ := admit(&bytes.Buffer{}, planCwd, admitOptions{class: "vitest", label: "pigtail test x", maxWait: time.Second, onTimeout: "fail"})
	if lease == nil {
		t.Fatal("no lease")
	}
	defer lease.Release()
	if reqs := served(); len(reqs) != 1 || reqs[0].Cwd != planCwd || reqs[0].SessionID != "sess-t" {
		t.Fatalf("requests = %+v", reqs)
	}
}

// historyExits reads the daemon's history until n lines carry an exit code
// (the line is written at the disconnect, asynchronously) and returns them.
func historyExits(t *testing.T, hist *admission.History, n int) []int {
	t.Helper()
	var got []int
	waitFor(t, 5*time.Second, func() bool {
		recs, err := hist.Read(time.Time{})
		if err != nil {
			return false
		}
		got = got[:0]
		for _, r := range recs {
			if r.Outcome == pigtail.AdmissionOutcomeReleased && r.ExitCode != nil {
				got = append(got, *r.ExitCode)
			}
		}
		return len(got) >= n
	})
	return got
}

func newHistoryDaemon(t *testing.T) *admission.History {
	t.Helper()
	hist := admission.NewHistory(filepath.Join(t.TempDir(), "admission-history.jsonl"))
	startAdmissionDaemonWith(t, 2, admission.WithHistory(hist))
	return hist
}

// The exit code reaches the history, 126 and 127 included.
func TestRunAdmittedReportsTheExitCode(t *testing.T) {
	hist := newHistoryDaemon(t)
	opts := admitOptions{maxWait: time.Second, onTimeout: "fail"}

	if code := runAdmitted([]string{"sh", "-c", "exit 7"}, opts); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if code := runAdmitted([]string{"/nonexistent/cmd"}, opts); code != 127 {
		t.Fatalf("missing command: %d, want 127", code)
	}
	plain := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runAdmitted([]string{plain}, opts); code != 126 {
		t.Fatalf("non-executable file: %d, want 126", code)
	}

	got := historyExits(t, hist, 3)
	want := []int{7, 127, 126}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("history exit codes = %v, want %v", got, want)
	}
}

// TestHelperPigtailTest is the `pigtail test` the exit-code test below runs
// as a subprocess (runTests ends in os.Exit on a red run).
func TestHelperPigtailTest(t *testing.T) {
	if os.Getenv("PIGTAIL_HELPER") != "pigtail-test" {
		return
	}
	if err := os.Chdir(os.Getenv("HELPER_DIR")); err != nil {
		os.Exit(3)
	}
	if err := runTests(testrun.RunnerVitest, nil, nil); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// pigtail test reports the runner's exit code to the daemon, on the normal
// path (a red run) and on the error path (the runner cannot start).
func TestPigtailTestReportsTheExitCode(t *testing.T) {
	if os.Getenv("PIGTAIL_HELPER") != "" {
		t.Skip()
	}
	hist := newHistoryDaemon(t)
	sock := socketPath()

	run := func(dir string, env ...string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPigtailTest$")
		cmd.Env = append(os.Environ(), "PIGTAIL_HELPER=pigtail-test", "HELPER_DIR="+dir, "PIGTAIL_SOCKET="+sock,
			envAdmit+"=", envAdmitLease+"=")
		cmd.Env = append(cmd.Env, env...)
		_ = cmd.Run() // the exit status is asserted through the history
	}

	// A fake vitest that fails with 4.
	red := t.TempDir()
	bin := filepath.Join(red, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "vitest"), []byte("#!/bin/sh\nexit 4\n"), 0o700); err != nil { //nolint:gosec // a test script
		t.Fatal(err)
	}
	run(red)
	if got := historyExits(t, hist, 1); got[0] != 4 {
		t.Fatalf("red run: history exit codes = %v, want [4]", got)
	}

	// No local vitest and no npx on PATH: the runner cannot start (127).
	run(t.TempDir(), "PATH="+t.TempDir())
	got := historyExits(t, hist, 2)
	if len(got) != 2 || got[1] != 127 {
		t.Fatalf("unstartable runner: history exit codes = %v, want [4 127]", got)
	}
}

func TestAdmitClassHelpNamesBuild(t *testing.T) {
	if usage := admitCmd().Flags().Lookup("class").Usage; !strings.Contains(usage, ", build,") {
		t.Fatalf("--class help must name build, got %q", usage)
	}
}

func TestAdmitClassHelpNamesVitestBrowser(t *testing.T) {
	if usage := admitCmd().Flags().Lookup("class").Usage; !strings.Contains(usage, "vitest-browser") {
		t.Fatalf("--class help must name vitest-browser, got %q", usage)
	}
}

func TestDescribeQueuedCompressCalm(t *testing.T) {
	s := describeQueued("build", pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1, Running: 1, Reason: "compress_calm"})
	if !strings.HasSuffix(s, ", waiting for sustained compressor calm (heavy class: a long wait does not lift it)") {
		t.Fatalf("%q does not name the calm rule", s)
	}
}

func TestDescribeQueuedClassLimit(t *testing.T) {
	s := describeQueued("go-test", pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 2, Running: 1, Reason: "class_limit"})
	if !strings.HasSuffix(s, ", waiting for a go-test turn") {
		t.Fatalf("%q does not name the class limit", s)
	}
}

// A heavy command started under a generic envelope (`pigtail admit -- git
// push` running the pre-push hook) must still queue for its own class, and
// name the envelope's lease so the daemon does not make it wait on its parent.
func TestAdmitNestedUnderGenericTakesItsOwnLease(t *testing.T) {
	// MaxJobs 1: the child only gets in if the daemon sets the parent aside.
	served := startCapturingDaemon(t, 1)
	cwd := t.TempDir()
	outer, abort := admit(&bytes.Buffer{}, cwd, admitOptions{class: "generic", maxWait: time.Second, onTimeout: "fail"})
	if abort || outer == nil {
		t.Fatalf("outer lease=%v abort=%v", outer, abort)
	}
	defer outer.Release()
	t.Setenv(envAdmitLease, outer.Grant.LeaseID)
	t.Setenv(envAdmitClass, "generic")

	inner, abort := admit(&bytes.Buffer{}, cwd, admitOptions{class: "go-test", maxWait: time.Second, onTimeout: "fail"})
	if abort || inner == nil {
		t.Fatalf("a go-test under a generic envelope must take its own lease, got lease=%v abort=%v", inner, abort)
	}
	defer inner.Release()
	reqs := served()
	if len(reqs) != 2 || reqs[1].Class != "go-test" || reqs[1].ParentLease != outer.Grant.LeaseID {
		t.Fatalf("requests = %+v, want the second to be go-test under %s", reqs, outer.Grant.LeaseID)
	}
}

func TestAdmitNestedUnderSameOrSpecificClassSkips(t *testing.T) {
	cases := []struct{ name, lease, outer, inner string }{
		{"go-test under go-test", "adm-1", "go-test", "go-test"},
		{"go-test under tsc", "adm-1", "tsc", "go-test"},
		{"go-test under an unknown class", "adm-1", "", "go-test"},
		{"generic under generic", "adm-1", "generic", "generic"},
		{"go-test under an unadmitted envelope", "none", "generic", "go-test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			served := startCapturingDaemon(t, 4)
			t.Setenv(envAdmitLease, c.lease)
			t.Setenv(envAdmitClass, c.outer)
			lease, abort := admit(&bytes.Buffer{}, t.TempDir(), admitOptions{class: c.inner, maxWait: time.Second, onTimeout: "fail"})
			if abort || lease != nil || len(served()) != 0 {
				t.Fatalf("lease=%v abort=%v requests=%v: must run without a lease", lease, abort, served())
			}
		})
	}
}

func TestRunAdmittedExportsItsClass(t *testing.T) {
	startAdmissionDaemon(t, 2)
	read := func(opts admitOptions, argv ...string) string {
		out := filepath.Join(t.TempDir(), "env")
		argv = append(argv, out)
		opts.maxWait, opts.onTimeout = time.Second, "fail"
		if code := runAdmitted(argv, opts); code != 0 {
			t.Fatalf("exit %d", code)
		}
		b, _ := os.ReadFile(out)
		return strings.TrimSpace(string(b))
	}
	sh := []string{"sh", "-c", `echo "$PIGTAIL_ADMIT_CLASS" > "$0"`}
	if got := read(admitOptions{}, sh...); got != "generic" {
		t.Fatalf("an inferred class must be exported as generic, got %q", got)
	}
	if got := read(admitOptions{class: "tsc"}, sh...); got != "tsc" {
		t.Fatalf("--class tsc must be exported, got %q", got)
	}
}

func TestAdmittedEnvCarriesLeaseAndClass(t *testing.T) {
	if got := strings.Join(admittedEnv(nil, "go-test"), " "); got != "PIGTAIL_ADMIT_LEASE=none PIGTAIL_ADMIT_CLASS=go-test" {
		t.Fatalf("got %q", got)
	}
	l := &pigtail.AdmissionLease{Grant: pigtail.AdmissionUpdate{LeaseID: "adm-3"}}
	if got := strings.Join(admittedEnv(l, "vitest"), " "); got != "PIGTAIL_ADMIT_LEASE=adm-3 PIGTAIL_ADMIT_CLASS=vitest" {
		t.Fatalf("got %q", got)
	}
}

// A generic admit nested under a generic envelope skips, and must leave the
// envelope's lease and class in place: rewriting them to "none" would make
// the grandchild read "covered" as "not admitted" and run without a cap.
func TestNestedSkipKeepsInheritedAdmission(t *testing.T) {
	t.Setenv(envAdmitLease, "adm-7")
	t.Setenv(envAdmitClass, "generic")
	if got := childAdmitEnv(nil, "generic"); len(got) != 0 {
		t.Fatalf("a nested skip must inherit the envelope's variables, got %v", got)
	}
	// An unadmitted run (fail-open) still says so.
	t.Setenv(envAdmitLease, "")
	if got := strings.Join(childAdmitEnv(nil, "generic"), " "); got != "PIGTAIL_ADMIT_LEASE=none PIGTAIL_ADMIT_CLASS=generic" {
		t.Fatalf("got %q", got)
	}
	// PIGTAIL_ADMIT=0 keeps none even under an envelope.
	t.Setenv(envAdmitLease, "adm-7")
	t.Setenv(envAdmit, "0")
	if got := strings.Join(childAdmitEnv(nil, "generic"), " "); got != "PIGTAIL_ADMIT_LEASE=none PIGTAIL_ADMIT_CLASS=generic" {
		t.Fatalf("got %q", got)
	}
}

func TestRunAdmittedNestedGenericKeepsEnvelopeLease(t *testing.T) {
	startAdmissionDaemon(t, 2)
	t.Setenv(envAdmitLease, "adm-7")
	t.Setenv(envAdmitClass, "generic")
	out := filepath.Join(t.TempDir(), "env")
	opts := admitOptions{maxWait: time.Second, onTimeout: "fail"}
	argv := []string{"sh", "-c", `echo "$PIGTAIL_ADMIT_LEASE $PIGTAIL_ADMIT_CLASS" > "$0"`, out}
	if code := runAdmitted(argv, opts); code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(b)); got != "adm-7 generic" {
		t.Fatalf("the command saw %q, want the envelope's \"adm-7 generic\"", got)
	}
}
