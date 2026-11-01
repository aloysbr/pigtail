package devserver

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// errStub is a sentinel spawn failure used by the cmux fallback tests.
var errStub = errors.New("stub spawn failure")

// installSeams snapshots the package seams and returns a restore func meant to
// be deferred. It stubs sleepFn to a no-op and shrinks the sidecar timing
// windows so the port-poll loops don't busy-spin against their real wall-clock
// deadlines; individual tests override portBusy/spawnSidecar/runBackgroundFn/out.
func installSeams(t *testing.T) func() {
	t.Helper()
	origSpawn, origPort, origRun, origSleep, origOut, origKill := spawnSidecar, portBusy, runBackgroundFn, sleepFn, out, killPIDsFn
	origBoot, origSettle, origInterval, origGrace := sidecarBootTimeout, sidecarSettleWindow, sidecarSettleInterval, sidecarFreeGrace
	sleepFn = func(time.Duration) {}
	sidecarBootTimeout = 20 * time.Millisecond
	sidecarSettleWindow = 20 * time.Millisecond
	sidecarSettleInterval = time.Millisecond
	sidecarFreeGrace = 20 * time.Millisecond
	return func() {
		spawnSidecar, portBusy, runBackgroundFn, sleepFn, out, killPIDsFn = origSpawn, origPort, origRun, origSleep, origOut, origKill
		sidecarBootTimeout, sidecarSettleWindow, sidecarSettleInterval, sidecarFreeGrace = origBoot, origSettle, origInterval, origGrace
	}
}

// probe is one scripted result for the portBusy seam. err stands in for an
// lsof failure: the port state is undecidable.
type probe struct {
	busy bool
	pids []int
	err  error
}

// scriptPortBusy installs a portBusy seam that returns the given probes in
// order (repeating the last one once exhausted) and reports the call count via
// the returned pointer, so tests can assert the loop stopped at the right step.
func scriptPortBusy(probes []probe) *int {
	calls := 0
	portBusy = func(int) (bool, []int, error) {
		i := calls
		calls++
		if i >= len(probes) {
			i = len(probes) - 1
		}
		return probes[i].busy, probes[i].pids, probes[i].err
	}
	return &calls
}

func testConfig() *pigtail.ProjectConfig {
	return &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{
			"dev":       {Command: "npm run dev", PortKind: "dev"},
			"storybook": {Command: "npm run storybook:dev", PortKind: "storybook"},
			// web is the monorepo alias: a second name on the dev port.
			"web": {Command: "npm run dev", PortKind: "dev"},
		},
		Ports: map[string]pigtail.PortSpec{
			"dev":       {Range: [2]int{3000, 3099}, Env: "PORT"},
			"storybook": {Range: [2]int{6006, 6099}, Env: "STORYBOOK_PORT"},
		},
	}
}

func TestServersToStop(t *testing.T) {
	cfg := testConfig()

	all, err := serversToStop(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("empty filter should return all servers, got %d", len(all))
	}

	// Stopping a name kills its port, so every name on that port is stopped.
	dev, err := serversToStop(cfg, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dev["web"]; len(dev) != 2 || !ok {
		t.Errorf("dev filter should expand to its port twin web, got %v", dev)
	}

	one, err := serversToStop(cfg, "storybook")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 {
		t.Fatalf("named filter should return 1 server, got %d", len(one))
	}
	if _, ok := one["storybook"]; !ok {
		t.Error("filtered map should contain storybook")
	}

	if _, err := serversToStop(cfg, "nope"); err == nil {
		t.Error("unknown server should error")
	}
}

// TestNamesSharingPort: the names one process answers to, found by the port
// variable their port kind resolves to.
func TestNamesSharingPort(t *testing.T) {
	cfg := testConfig()
	cases := []struct {
		name string
		want []string
	}{
		{"dev", []string{"dev", "web"}},
		{"web", []string{"dev", "web"}},
		{"storybook", []string{"storybook"}},
		{"nope", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NamesSharingPort(cfg, tc.name); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NamesSharingPort(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// asProbed turns a server set into the probed form shouldPurgeNextCache
// consumes. Port state is irrelevant to the purge decision — only the port
// kind of the servers actually being acted on is.
func asProbed(servers map[string]pigtail.ServerSpec) []probedServer {
	probed := make([]probedServer, 0, len(servers))
	for name, spec := range servers {
		probed = append(probed, probedServer{name: name, spec: spec})
	}
	return probed
}

func TestShouldPurgeNextCache(t *testing.T) {
	cfg := testConfig()
	cases := []struct {
		serverName string
		want       bool
	}{
		{"", true},           // stops everything, dev included
		{"dev", true},        // PortKind "dev"
		{"storybook", false}, // PortKind "storybook" — no purge
	}
	for _, c := range cases {
		servers, err := serversToStop(cfg, c.serverName)
		if err != nil {
			t.Fatal(err)
		}
		if got := shouldPurgeNextCache(asProbed(servers)); got != c.want {
			t.Errorf("shouldPurgeNextCache(%q) = %v, want %v", c.serverName, got, c.want)
		}
	}
	// A Next.js server with a custom name must still trigger the purge.
	custom := map[string]pigtail.ServerSpec{
		"web": {Command: "npm run dev", PortKind: "dev"},
	}
	if !shouldPurgeNextCache(asProbed(custom)) {
		t.Error("custom-named server with PortKind dev should purge")
	}
	// A server set with no dev-kind server never purges.
	noDev := map[string]pigtail.ServerSpec{
		"storybook": {Command: "npm run storybook:dev", PortKind: "storybook"},
	}
	if shouldPurgeNextCache(asProbed(noDev)) {
		t.Error("stopping a set with no dev-kind server should not purge")
	}
	// An empty set (e.g. every server in a worktree scoped out) never purges.
	if shouldPurgeNextCache(nil) {
		t.Error("an empty server set should not purge")
	}
}

func TestWaitPortStable(t *testing.T) {
	t.Run("stays up over the whole window", func(t *testing.T) {
		defer installSeams(t)()
		scriptPortBusy([]probe{{busy: true}}) // always up
		if !waitPortStable(5, 15*time.Millisecond, time.Millisecond) {
			t.Error("a port up across the whole settle window should be stable")
		}
	})

	t.Run("drop mid-window returns false and stops probing", func(t *testing.T) {
		defer installSeams(t)()
		// A large settle window ensures the deadline is never the terminating
		// condition — the scripted drop is what must stop the loop.
		calls := scriptPortBusy([]probe{{busy: true}, {busy: true}, {busy: false}})
		if waitPortStable(5, 10*time.Second, time.Millisecond) {
			t.Error("a mid-window drop should make the port unstable")
		}
		if *calls != 3 {
			t.Errorf("should stop at the first drop (3 probes), got %d", *calls)
		}
	})
}

// TestWaitPortFree: waiting for a dying sidecar to release its port never
// kills. Past the grace, whatever holds the port bound it during the wait — a
// restarted sidecar or a port twin — and is reported, not reclaimed.
func TestWaitPortFree(t *testing.T) {
	cases := []struct {
		name        string
		probes      []probe
		grace       time.Duration
		wantFree    bool
		wantHolders []int
		wantProbes  int // 0 = don't check
	}{
		{
			name:       "frees within grace",
			probes:     []probe{{busy: true, pids: []int{7}}, {busy: true, pids: []int{7}}, {busy: false}},
			grace:      10 * time.Second,
			wantFree:   true,
			wantProbes: 3,
		},
		{
			name:        "still held after grace reports holders",
			probes:      []probe{{busy: true, pids: []int{7, 8}}},
			grace:       0,
			wantFree:    false,
			wantHolders: []int{7, 8},
		},
		{
			name:       "lsof error then free",
			probes:     []probe{{err: errStub}, {busy: false}},
			grace:      10 * time.Second,
			wantFree:   true,
			wantProbes: 2,
		},
		{
			name:     "lsof error until the deadline is not free",
			probes:   []probe{{err: errStub}},
			grace:    0,
			wantFree: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer installSeams(t)()
			var kills [][]int
			killPIDsFn = func(pids []int) { kills = append(kills, pids) }
			calls := scriptPortBusy(tc.probes)

			free, holders := waitPortFree(5, tc.grace, time.Millisecond)

			if free != tc.wantFree {
				t.Errorf("free = %v, want %v", free, tc.wantFree)
			}
			if !reflect.DeepEqual(holders, tc.wantHolders) {
				t.Errorf("holders = %v, want %v", holders, tc.wantHolders)
			}
			if len(kills) != 0 {
				t.Errorf("waitPortFree killed %v — it must never kill", kills)
			}
			if tc.wantProbes != 0 && *calls != tc.wantProbes {
				t.Errorf("probes = %d, want %d", *calls, tc.wantProbes)
			}
		})
	}
}

// TestReadPortHandwrittenForms is the regression test for the worst of the
// three .env.local readers. ReadPort used to match with
// HasPrefix(line, envVar+"="), so `export PORT=…` and `PORT = 3000` were not
// assignments at all and `PORT=3000 # dev` broke its Atoi. Every one of those
// failures ends the same way: no port, the caller falls back to 3000, and two
// worktrees fight over one port with no error anywhere — exactly what
// `pigtail dev` exists to prevent.
//
// envLocalOwner already accepted all of these; the point of the shared parser
// is that the port reader now does too.
func TestReadPortHandwrittenForms(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"plain", "PORT=3042\n", 3042},
		{"export prefix", "export PORT=3042\n", 3042},
		{"spaces around the equals", "PORT = 3042\n", 3042},
		{"trailing comment", "PORT=3042 # dev server\n", 3042},
		{"export, quoted, commented", "export PORT=\"3042\"  # dev\n", 3042},
		{"indented", "\tPORT=3042\n", 3042},
		{"no trailing newline", "PORT=3042", 3042},
		{"commented line does not win", "# PORT=3000\nPORT=3042\n", 3042},
		{"surrounded by other variables", "FOO=bar\nPORT=3042\nSTORYBOOK_PORT=6042\n", 3042},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeEnvLocalFile(t, dir, tc.content)
			got, err := ReadPort(dir, "PORT")
			if err != nil {
				t.Fatalf("ReadPort: %v\n--- .env.local ---\n%s", err, tc.content)
			}
			if got != tc.want {
				t.Fatalf("ReadPort = %d, want %d\n--- .env.local ---\n%s", got, tc.want, tc.content)
			}
		})
	}
}

// TestReadPortTakesTheLastAssignment pins the deliberate behaviour change of
// this lot: ReadPort took the FIRST match, envLocalOwner the LAST. A shell
// sources the file top to bottom, so the last assignment is the one the dev
// server actually runs under — the port pigtail reports must be that one.
func TestReadPortTakesTheLastAssignment(t *testing.T) {
	dir := t.TempDir()
	writeEnvLocalFile(t, dir, "PORT=3000\nPORT=3042\n")
	got, err := ReadPort(dir, "PORT")
	if err != nil {
		t.Fatal(err)
	}
	if got != 3042 {
		t.Fatalf("ReadPort = %d, want 3042 (last assignment wins, like the shell)", got)
	}
}

func TestReadPortErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := ReadPort(filepath.Join(t.TempDir(), "nope"), "PORT"); err == nil {
			t.Fatal("a missing .env.local must be an error, not a silent zero")
		}
	})
	t.Run("variable absent", func(t *testing.T) {
		dir := t.TempDir()
		writeEnvLocalFile(t, dir, "FOO=bar\n")
		_, err := ReadPort(dir, "PORT")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("want a not-found error, got %v", err)
		}
	})
	t.Run("non-numeric value", func(t *testing.T) {
		dir := t.TempDir()
		writeEnvLocalFile(t, dir, "PORT=lolwat\n")
		_, err := ReadPort(dir, "PORT")
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("want an invalid-value error, got %v", err)
		}
	})
	t.Run("empty value", func(t *testing.T) {
		// `PORT=` is an assignment the shell honours (to the empty string), so
		// it is a broken port rather than an absent one — reporting it as
		// invalid is what keeps it from being read as "no PORT here".
		dir := t.TempDir()
		writeEnvLocalFile(t, dir, "PORT=\n")
		_, err := ReadPort(dir, "PORT")
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("want an invalid-value error, got %v", err)
		}
	})
}

// TestStartWarnsOnADuplicatedPortAssignment: a .env.local assigning the port
// twice is resolved silently by Lookup (last wins, like the shell). These
// files are real — the pre-fix mergeEnvLocal produced them by appending a port
// line instead of rewriting the one it could not recognise — and a user
// staring at a server on an unexpected port has no way to know their file says
// two different things. Starting a server is the moment to say so.
func TestStartWarnsOnADuplicatedPortAssignment(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	container := t.TempDir()
	const wtName = "wt"
	wtDir := filepath.Join(container, wtName)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := freeTestPort(t)
	port := freeTestPort(t)
	// The shape the old merge left behind: the inherited line, then the one
	// appended below it. The last is the one that counts.
	writeEnvLocalFile(t, wtDir, fmt.Sprintf("export PORT=%d\nPORT=%d\n", stale, port))

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {Command: "true", PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := Start(ctx, wtName, "dev", true); err != nil {
		t.Fatal(err)
	}

	report := buf.String()
	if !strings.Contains(report, "assigned 2 times") {
		t.Errorf("starting a server from a doubly-assigned .env.local should say so, got: %q", report)
	}
	// The warning has to name the port actually being used, or it just adds
	// confusion to the confusion.
	if !strings.Contains(report, fmt.Sprintf("%d", port)) {
		t.Errorf("the warning should name the port in use (:%d), got: %q", port, report)
	}
	if strings.Count(report, "assigned 2 times") != 1 {
		t.Errorf("the warning should appear exactly once, got: %q", report)
	}
}

// TestStartIsQuietOnASinglePortAssignment: the warning must not become
// background noise on the file every worktree has.
func TestStartIsQuietOnASinglePortAssignment(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	container := t.TempDir()
	const wtName = "wt"
	wtDir := filepath.Join(container, wtName)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEnvLocalFile(t, wtDir, fmt.Sprintf("PORT=%d\n", freeTestPort(t)))

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {Command: "true", PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := Start(ctx, wtName, "dev", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "assigned") {
		t.Errorf("a single assignment must not warn, got: %q", buf.String())
	}
}

// TestReadPortDoesNotWarn keeps the warning off the sweep paths. ReadPort is
// called by probeServers (every `stop --idle`, which runs INSIDE the daemon)
// and by ListRunning (GET /servers, polled by the menu bar), for every
// worktree of every project. A warning there would repeat forever, in a
// process with no user attached to read it.
func TestReadPortDoesNotWarn(t *testing.T) {
	dir := t.TempDir()
	writeEnvLocalFile(t, dir, "PORT=3000\nPORT=3042\n")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	got, err := ReadPort(dir, "PORT")
	if err != nil {
		t.Fatal(err)
	}
	if got != 3042 {
		t.Fatalf("ReadPort = %d, want 3042", got)
	}
	if buf.Len() != 0 {
		t.Errorf("ReadPort must stay silent — it runs on the daemon's sweeps, got: %q", buf.String())
	}
}

// cmuxFixture builds a minimal Context whose worktree has a .env.local so
// StartInCmux resolves a real port through ReadPort without hitting a project
// on disk. Returns the context, worktree name and the port written.
func cmuxFixture(t *testing.T) (*Context, string, int) {
	t.Helper()
	container := t.TempDir()
	const wtName = "feat-x"
	const port = 3042
	wtDir := filepath.Join(container, wtName)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".env.local"), []byte("PORT=3042\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Context{ContainerDir: container, Config: testConfig()}, wtName, port
}

func TestStartInCmux(t *testing.T) {
	cases := []struct {
		name         string
		spawnErr     error
		probes       []probe // portBusy timeline (repeats last)
		wantFallback bool
		wantOut      []string // substrings the printed output must contain
		notOut       []string // substrings it must NOT contain
	}{
		{
			// probe[0] is the "already running" guard (free), then up for the
			// boot check and the whole settle window.
			name:         "spawn ok and port stable goes live",
			probes:       []probe{{busy: false}, {busy: true}},
			wantFallback: false,
			wantOut:      []string{"in a cmux sidecar", "● dev live on :3042"},
			notOut:       []string{"falling back"},
		},
		{
			name:         "port never comes up falls back",
			probes:       []probe{{busy: false}}, // guard free, then never up → boot timeout
			wantFallback: true,
			wantOut:      []string{"never came up on :3042", "falling back to background daemon"},
			notOut:       []string{"● dev live"},
		},
		{
			// guard free, boot ok, then the port drops during the settle window.
			name:         "port comes up then dies falls back",
			probes:       []probe{{busy: false}, {busy: true}, {busy: false}},
			wantFallback: true,
			wantOut:      []string{"came up then died", "falling back to background daemon"},
			notOut:       []string{"● dev live"},
		},
		{
			// guard free, boot ok, settle drop — then, while the fallback waits
			// for the port to free, something re-binds it (the sidecar's own
			// restart, or a port twin). It is a fresh listener: never killed,
			// never doubled by a second copy.
			name:         "re-bound during grace: no fallback, no kill, warns",
			probes:       []probe{{busy: false}, {busy: true}, {busy: false}, {busy: true, pids: []int{4242}}},
			wantFallback: false,
			wantOut:      []string{"came up then died", ":3042 re-bound by pids [4242]", "not starting a second copy", "pigtail stop --server dev"},
			notOut:       []string{"● dev live"},
		},
		{
			// An lsof failure during the settle window is not a drop: the
			// sidecar is not declared dead on a probe that did not happen.
			name:         "lsof error during settle goes live",
			probes:       []probe{{busy: false}, {busy: true}, {err: errStub}},
			wantFallback: false,
			wantOut:      []string{"● dev live on :3042"},
			notOut:       []string{"falling back"},
		},
		{
			name:         "spawn error falls back",
			spawnErr:     errStub,
			probes:       []probe{{busy: false}}, // port free so waitPortFree returns fast
			wantFallback: true,
			wantOut:      []string{"cmux sidecar unavailable", "falling back to background daemon"},
			notOut:       []string{"● dev live"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer installSeams(t)()
			ctx, wtName, _ := cmuxFixture(t)

			var buf bytes.Buffer
			out = &buf
			spawnSidecar = func(string, string, string) error { return c.spawnErr }
			scriptPortBusy(c.probes)
			var kills [][]int
			killPIDsFn = func(pids []int) { kills = append(kills, pids) }
			fallbackCalls := 0
			runBackgroundFn = func(string, string, string, int) error {
				fallbackCalls++
				return nil
			}

			if err := StartInCmux(ctx, wtName, "dev"); err != nil {
				t.Fatalf("StartInCmux returned error: %v", err)
			}

			if len(kills) != 0 {
				t.Errorf("StartInCmux killed %v — no path may kill a listener", kills)
			}
			if c.wantFallback && fallbackCalls != 1 {
				t.Errorf("expected the background daemon to be invoked once, got %d", fallbackCalls)
			}
			if !c.wantFallback && fallbackCalls != 0 {
				t.Errorf("expected no fallback, but the daemon was invoked %d time(s)", fallbackCalls)
			}
			got := buf.String()
			for _, sub := range c.wantOut {
				if !strings.Contains(got, sub) {
					t.Errorf("output should contain %q; got:\n%s", sub, got)
				}
			}
			for _, sub := range c.notOut {
				if strings.Contains(got, sub) {
					t.Errorf("output should NOT contain %q; got:\n%s", sub, got)
				}
			}
		})
	}
}

func TestWrapCommand(t *testing.T) {
	got := wrapCommand("/wt/feat-x", "npm run dev")

	// .env.local is referenced by absolute, single-quoted path so an
	// interactive zsh sidecar sources the file from disk (not $PATH).
	if !strings.Contains(got, "'/wt/feat-x/.env.local'") {
		t.Errorf("wrapCommand should source the absolute .env.local path, got %q", got)
	}
	// A bare `. .env.local` (no slash) is the zsh $PATH-lookup bug — must be gone.
	if strings.Contains(got, ". .env.local") {
		t.Errorf("wrapCommand must not source a bare .env.local name, got %q", got)
	}
	// Guarded so a missing .env.local skips instead of aborting the command.
	if !strings.Contains(got, "[ -f '/wt/feat-x/.env.local' ]") {
		t.Errorf("wrapCommand should guard the source with a file test, got %q", got)
	}
	// The server command still runs after the env is (optionally) sourced.
	if !strings.HasSuffix(got, "npm run dev") {
		t.Errorf("wrapCommand should end with the server command, got %q", got)
	}
}

func TestWrapCommandQuotesSpaces(t *testing.T) {
	got := wrapCommand("/wt/my feature", "npm run dev")
	if !strings.Contains(got, "'/wt/my feature/.env.local'") {
		t.Errorf("wrapCommand should single-quote a path with spaces, got %q", got)
	}
}
