package devserver

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestOwnedByMe(t *testing.T) {
	cases := []struct {
		name  string
		owner string
		me    string
		want  bool
	}{
		{"orphan is claimable", "", "ws-a", true},
		{"my own server", "ws-a", "ws-a", true},
		{"someone else's server", "ws-b", "ws-a", false},
		{"unknown owner of an orphan", "", "", true},
		{"no identity of my own, owned server", "ws-b", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownedByMe(tc.owner, tc.me); got != tc.want {
				t.Fatalf("ownedByMe(%q, %q) = %v, want %v", tc.owner, tc.me, got, tc.want)
			}
		})
	}
}

func TestOwnerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := writeOwner(dir, "dev", "ws-a"); err != nil {
		t.Fatal(err)
	}
	if got := ReadOwner(dir, "dev"); got != "ws-a" {
		t.Fatalf("ReadOwner = %q, want %q", got, "ws-a")
	}
	clearOwner(dir, "dev")
	if got := ReadOwner(dir, "dev"); got != "" {
		t.Fatalf("after clear: ReadOwner = %q, want empty", got)
	}
}

// TestWriteOwnerEmptyClearsTheMarker: a session with no identity starting a
// server must assert that truth, not stay silent. Writing nothing was the
// second half of the fossilised-marker bug — a plain terminal (no
// CMUX_WORKSPACE_ID, no PIGTAIL_OWNER) could never correct a marker left
// behind by one of the kill paths that does not clear it, so the marker
// outlived every server that followed it on that worktree.
func TestWriteOwnerEmptyClearsTheMarker(t *testing.T) {
	dir := t.TempDir()

	// No marker to begin with: an empty owner still creates none.
	if err := writeOwner(dir, "dev", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(OwnerPath(dir, "dev")); !os.IsNotExist(err) {
		t.Fatal("an empty owner must not create a file")
	}

	// A marker left behind by an earlier session must be dropped, not kept.
	if err := writeOwner(dir, "dev", "ws-b"); err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(dir, "dev", ""); err != nil {
		t.Fatal(err)
	}
	if got := ReadOwner(dir, "dev"); got != "" {
		t.Fatalf("starting under no identity must clear a stale marker, got %q", got)
	}
	// Sibling servers keep their own markers — the file is per server.
	if err := writeOwner(dir, "storybook", "ws-b"); err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(dir, "dev", ""); err != nil {
		t.Fatal(err)
	}
	if got := ReadOwner(dir, "storybook"); got != "ws-b" {
		t.Fatalf("clearing dev's marker must not touch storybook's, got %q", got)
	}
}

// TestWriteOwnerIsAtomic is finding 5 of the 2026-07-27 final review:
// os.WriteFile truncates the marker file THEN writes its bytes, so a
// concurrent reader — the daemon's rammon sweep, or another session's
// triageScope — can land inside that window and see "", which ownedByMe
// reads as *unclaimed*, i.e. stoppable. On the pre-fix code (os.WriteFile)
// this reliably reproduces within a few thousand iterations: a writer loop
// re-asserting the same owner races a reader loop, and the reader observes
// an empty marker thousands of times even though an owner was continuously
// recorded throughout. os.Rename over a same-directory temp file is atomic
// within a filesystem, so no reader can ever observe a partial state.
func TestWriteOwnerIsAtomic(t *testing.T) {
	dir := t.TempDir()
	if err := writeOwner(dir, "dev", "ws-a"); err != nil {
		t.Fatal(err)
	}

	const iterations = 20000
	var wg sync.WaitGroup
	var emptyReads int64
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if err := writeOwner(dir, "dev", "ws-a"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if got := ReadOwner(dir, "dev"); got == "" {
				atomic.AddInt64(&emptyReads, 1)
			}
		}
	}()
	wg.Wait()

	if emptyReads > 0 {
		t.Fatalf("ReadOwner observed an empty marker %d times while writeOwner kept reasserting the same owner — writeOwner is not atomic", emptyReads)
	}
}

func TestReadOwnerTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureStateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(OwnerPath(dir, "dev"), []byte("ws-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadOwner(dir, "dev"); got != "ws-a" {
		t.Fatalf("ReadOwner = %q, want %q", got, "ws-a")
	}
}

func TestReadOwnerMissingIsEmpty(t *testing.T) {
	if got := ReadOwner(t.TempDir(), "dev"); got != "" {
		t.Fatalf("missing owner file: got %q, want empty", got)
	}
}

func TestOwnerIDPrefersCmuxWorkspace(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "ws-cmux")
	t.Setenv("PIGTAIL_OWNER", "ws-env")
	if got := OwnerID(t.TempDir()); got != "ws-cmux" {
		t.Fatalf("OwnerID = %q, want ws-cmux", got)
	}
}

func TestOwnerIDFallsBackToPigtailOwner(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-env")
	if got := OwnerID(t.TempDir()); got != "ws-env" {
		t.Fatalf("OwnerID = %q, want ws-env", got)
	}
}

// TestOwnerIDFallsBackToEnvLocal: with neither env var set, but the worktree's
// .env.local carrying PIGTAIL_OWNER (written by `pigtail new`), that value is
// used — the third cran, and the one that gives an agent session (whose shell
// does not survive between invocations, so the fourth cran below is useless
// to it) a stable identity.
func TestOwnerIDFallsBackToEnvLocal(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "")
	dir := t.TempDir()
	writeEnvLocalFile(t, dir, "PORT=3000\nPIGTAIL_OWNER=feat-agent\n")
	if got := OwnerID(dir); got != "feat-agent" {
		t.Fatalf("OwnerID = %q, want feat-agent", got)
	}
}

// TestOwnerIDEnvOverridesEnvLocal: an explicit export stays the escape
// hatch even once the worktree carries its own PIGTAIL_OWNER — it is the
// only way to name a session legibly or to share one deliberately.
func TestOwnerIDEnvOverridesEnvLocal(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-env")
	dir := t.TempDir()
	writeEnvLocalFile(t, dir, "PIGTAIL_OWNER=feat-agent\n")
	if got := OwnerID(dir); got != "ws-env" {
		t.Fatalf("OwnerID = %q, want ws-env (explicit env wins over .env.local)", got)
	}
}

// TestOwnerIDFallsBackToSessionWhenUnset: with neither env var set and no
// .env.local carrying PIGTAIL_OWNER, a plain terminal is still not
// anonymous — it carries its POSIX session id, the fourth and last cran.
// This test replaced TestOwnerIDEmptyWhenUnset, whose premise (no env ⇒ no
// identity) is exactly what this lot closes.
func TestOwnerIDFallsBackToSessionWhenUnset(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "")
	got := OwnerID(t.TempDir())
	if got == "" {
		t.Fatal("a session with no env identity must fall back to its POSIX session id")
	}
	if !strings.HasPrefix(got, sessionPrefix) {
		t.Fatalf("OwnerID = %q, want a %s… identity", got, sessionPrefix)
	}
}

// --- envLocalOwner: the reader powering OwnerID's third cran ---

func TestEnvLocalOwnerReadsAssignment(t *testing.T) {
	dir := t.TempDir()
	writeEnvLocalFile(t, dir, "PORT=3000\nPIGTAIL_OWNER=feat-x\n")
	if got := envLocalOwner(dir); got != "feat-x" {
		t.Fatalf("envLocalOwner = %q, want feat-x", got)
	}
}

func TestEnvLocalOwnerIgnoresCommentsAndTakesLastAssignment(t *testing.T) {
	dir := t.TempDir()
	writeEnvLocalFile(t, dir, "# PIGTAIL_OWNER=old-comment\nPIGTAIL_OWNER=first\nPIGTAIL_OWNER=second\n")
	if got := envLocalOwner(dir); got != "second" {
		t.Fatalf("envLocalOwner = %q, want second (last non-commented assignment wins)", got)
	}
}

func TestEnvLocalOwnerTrimsQuotesAndWhitespace(t *testing.T) {
	dir := t.TempDir()
	writeEnvLocalFile(t, dir, "PIGTAIL_OWNER = \"feat-x\" \n")
	if got := envLocalOwner(dir); got != "feat-x" {
		t.Fatalf("envLocalOwner = %q, want feat-x", got)
	}
}

func TestEnvLocalOwnerMissingFileIsEmpty(t *testing.T) {
	if got := envLocalOwner(filepath.Join(t.TempDir(), "does-not-exist")); got != "" {
		t.Fatalf("envLocalOwner on missing dir = %q, want empty", got)
	}
}

func TestEnvLocalOwnerNoAssignmentIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeEnvLocalFile(t, dir, "PORT=3000\n")
	if got := envLocalOwner(dir); got != "" {
		t.Fatalf("envLocalOwner = %q, want empty when PIGTAIL_OWNER absent", got)
	}
}

// TestEnvLocalOwnerHandwrittenForms covers the shapes a habitual shell user
// produces when hand-editing .env.local, which the README now instructs them to
// do for worktrees predating the session-identity lot. The reference is the
// shell itself: internal/tui/commands.go sources this exact file with
// `set -a && . .env.local`, so what the shell would assign is what OwnerID must
// read back — anything else means a stamped identity silently disagrees with
// the environment the dev server actually runs under.
func TestEnvLocalOwnerHandwrittenForms(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "trailing comment is not part of the value",
			content: "PIGTAIL_OWNER=feat-x # added by hand\n",
			want:    "feat-x",
		},
		{
			name:    "export prefix is an assignment too",
			content: "export PIGTAIL_OWNER=feat-x\n",
			want:    "feat-x",
		},
		{
			name:    "export with quotes and a trailing comment",
			content: "export PIGTAIL_OWNER=\"feat-x\"  # note\n",
			want:    "feat-x",
		},
		{
			name:    "a hash inside quotes is data, not a comment",
			content: "PIGTAIL_OWNER=\"feat#x\"\n",
			want:    "feat#x",
		},
		{
			name:    "an unspaced hash is a literal word to the shell",
			content: "PIGTAIL_OWNER=feat#x\n",
			want:    "feat#x",
		},
		{
			name:    "last assignment wins across both forms",
			content: "export PIGTAIL_OWNER=first\nPIGTAIL_OWNER=second # c\n",
			want:    "second",
		},
		{
			name:    "value that is only a comment is empty",
			content: "PIGTAIL_OWNER= # nothing yet\n",
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeEnvLocalFile(t, dir, tc.content)
			if got := envLocalOwner(dir); got != tc.want {
				t.Fatalf("envLocalOwner = %q, want %q\n--- .env.local ---\n%s", got, tc.want, tc.content)
			}
		})
	}
}

func writeEnvLocalFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShouldStopInScope(t *testing.T) {
	cases := []struct {
		name  string
		scope Scope
		owner string
		me    string
		alive bool
		want  bool
	}{
		{"owned scope stops my server", ScopeOwned, "ws-a", "ws-a", true, true},
		{"owned scope stops an orphan", ScopeOwned, "", "ws-a", true, true},
		{"owned scope spares another live session", ScopeOwned, "ws-b", "ws-a", true, false},
		{"owned scope reclaims a dead session", ScopeOwned, "ws-b", "ws-a", false, true},
		{"everywhere stops another session", ScopeEverywhere, "ws-b", "ws-a", true, true},
		{"everywhere stops an orphan", ScopeEverywhere, "", "ws-a", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldStopInScope(tc.scope, tc.owner, tc.me, tc.alive); got != tc.want {
				t.Fatalf("shouldStopInScope(%v, %q, %q, %v) = %v, want %v",
					tc.scope, tc.owner, tc.me, tc.alive, got, tc.want)
			}
		})
	}
}

// TestHelperListener is not a real test: re-executed as a subprocess by
// TestStopAllScopesToOwner, it listens on the given port until killed. Same
// pattern as internal/daemon/watchdog's TestHelperListener, duplicated here
// (not shared) because os.Args[0] re-execs THIS package's test binary.
func TestHelperListener(t *testing.T) {
	if os.Getenv("DEVSERVER_HELPER") != "1" {
		t.Skip("helper process only")
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("DEVSERVER_PORT"))
	if err != nil {
		os.Exit(1)
	}
	defer l.Close()
	time.Sleep(60 * time.Second)
}

// TestStopAllScopesToOwner exercises StopAll end to end on a fake container:
// three worktrees, each with a real listener occupying its port (so
// IsPortBusy — which shells to lsof — sees a genuine process, exactly like a
// real dev server). One worktree is owned by another session, one is mine,
// one is an orphan. A ScopeOwned StopAll must stop mine and the orphan, and
// must leave the other session's server running and its owner marker intact
// — the actual safety property this task exists to guarantee.
func TestStopAllScopesToOwner(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	container := t.TempDir()

	portMine := freeTestPort(t)
	portOther := freeTestPort(t)
	portOrphan := freeTestPort(t)
	dirMine := mkFakeWorktreeDir(t, container, "wt-mine", portMine)
	dirOther := mkFakeWorktreeDir(t, container, "wt-other", portOther)
	dirOrphan := mkFakeWorktreeDir(t, container, "wt-orphan", portOrphan)

	if err := writeOwner(dirMine, "dev", "ws-a"); err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(dirOther, "dev", "ws-b"); err != nil {
		t.Fatal(err)
	}
	// dirOrphan deliberately gets no owner marker.

	spawnPortListener(t, portMine, dirMine)
	spawnPortListener(t, portOther, dirOther)
	spawnPortListener(t, portOrphan, dirOrphan)
	waitPortBusyForTest(t, portMine)
	waitPortBusyForTest(t, portOther)
	waitPortBusyForTest(t, portOrphan)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeOwned); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mineBusy, _, _ := IsPortBusy(portMine)
		orphanBusy, _, _ := IsPortBusy(portOrphan)
		if !mineBusy && !orphanBusy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("my server (busy=%v) and the orphan (busy=%v) should both be stopped", mineBusy, orphanBusy)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if busy, _, _ := IsPortBusy(portOther); !busy {
		t.Fatal("another session's server must be spared by a ScopeOwned StopAll")
	}
	if got := ReadOwner(dirOther, "dev"); got != "ws-b" {
		t.Fatalf("owner marker of the spared server must remain, got %q", got)
	}
	if got := ReadOwner(dirOrphan, "dev"); got != "" {
		t.Fatalf("stopping an orphan must not fabricate an owner, got %q", got)
	}

	report := buf.String()
	if strings.Contains(report, "kept dev (owned by ws-a)") {
		t.Fatalf("my own server must not be reported as kept, got: %s", report)
	}
	if !strings.Contains(report, "kept dev (owned by ws-b)") {
		t.Fatalf("expected the other session's server to be reported as kept, got: %s", report)
	}
}

// freeTestPort grabs a genuinely free TCP port (bind then immediately close),
// so IsPortBusy's real `lsof` shell-out sees it as free at the moment a test
// needs an "already dead" or "never started" server on a real port number,
// without widening the portBusy seam (that seam is StartInCmux-only).
func freeTestPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// mkFakeWorktreeDir creates a fake worktree directory under container with a
// .env.local pointing PORT at the given port — the on-disk shape StopAll
// scans for (a directory containing .env.local), without needing a real
// project checkout.
func mkFakeWorktreeDir(t *testing.T, container, name string, port int) string {
	t.Helper()
	dir := filepath.Join(container, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := fmt.Sprintf("PORT=%d\n", port)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// spawnPortListener starts a subprocess that binds and holds the given port
// until killed, standing in for a real dev server so IsPortBusy's real lsof
// shell-out sees a genuine process. Same re-exec pattern as
// internal/daemon/watchdog's TestHelperListener; self-cleaning via
// t.Cleanup.
//
// dir is the listener's working directory: a stop only kills a listener
// running inside the worktree (its cwd), so a test standing in for a
// worktree's own server passes that worktree. "" keeps the inherited cwd —
// for raw probes, where ownership plays no part.
func spawnPortListener(t *testing.T, port int, dir string) *exec.Cmd {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=TestHelperListener") //nolint:gosec // re-exec of the test binary
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DEVSERVER_HELPER=1", fmt.Sprintf("DEVSERVER_PORT=%d", port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

// waitPortBusyForTest polls IsPortBusy until the port has a listener or the
// deadline elapses.
func waitPortBusyForTest(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if busy, _, _ := IsPortBusy(port); busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener on :%d never came up", port)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitPortFreeForTest polls IsPortBusy until the port is free or the
// deadline elapses.
func waitPortFreeForTest(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if busy, _, _ := IsPortBusy(port); !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port :%d still busy after stop", port)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestStopAllPurgesCacheForInScopeWorktree is the regression test for a
// review finding on this task: StopAll's scoped path (StopAll → stopScoped →
// stopServers) never called shouldPurgeNextCache/purgeNextDevCache at all,
// for any worktree — including ones the caller legitimately stops. Before
// the fix, this worktree's dev server is stopped but .next/dev/cache (often
// >10 GB) survives; this test fails on that code because the cache
// directory is still there after StopAll returns.
func TestStopAllPurgesCacheForInScopeWorktree(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	container := t.TempDir()
	port := freeTestPort(t)
	dir := mkFakeWorktreeDir(t, container, "wt-mine", port)
	// No owner marker: an orphan, in scope for any session's ScopeOwned stop.

	cacheDir := filepath.Join(dir, ".next", "dev", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	spawnPortListener(t, port, dir)
	waitPortBusyForTest(t, port)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeOwned); err != nil {
		t.Fatal(err)
	}
	waitPortFreeForTest(t, port)

	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf(".next/dev/cache should be purged for a worktree whose dev server StopAll actually stopped, stat err = %v", err)
	}
}

// TestStopAllSparesCacheForAnotherSession is the safety half of the same
// fix: purging must never happen for a worktree a scoped stop leaves
// entirely alone because it belongs to another session. Erasing 10+ GB of a
// neighboring session's cache would be the same kind of cross-session damage
// this whole task exists to stop — the review's explicit concern.
func TestStopAllSparesCacheForAnotherSession(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	container := t.TempDir()
	port := freeTestPort(t)
	dir := mkFakeWorktreeDir(t, container, "wt-other", port)
	if err := writeOwner(dir, "dev", "ws-b"); err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(dir, ".next", "dev", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	spawnPortListener(t, port, dir)
	waitPortBusyForTest(t, port)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeOwned); err != nil {
		t.Fatal(err)
	}

	if busy, _, _ := IsPortBusy(port); !busy {
		t.Fatal("another session's server must be spared")
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Fatalf("another session's .next/dev/cache must be left intact, stat err = %v", err)
	}
}

// twoServerConfig is a project with a dev server and a storybook server, each
// on its own port env var — the shape needed to exercise a worktree whose
// servers fall on DIFFERENT sides of the ownership scope.
func twoServerConfig() *pigtail.ProjectConfig {
	return &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{
			"dev":       {PortKind: "dev"},
			"storybook": {PortKind: "storybook"},
		},
		Ports: map[string]pigtail.PortSpec{
			"dev":       {Env: "PORT"},
			"storybook": {Env: "STORYBOOK_PORT"},
		},
	}
}

// mkTwoServerWorktree creates a fake worktree whose .env.local carries both
// ports, plus a .next/dev/cache to watch. Returns the worktree dir.
func mkTwoServerWorktree(t *testing.T, container, name string, devPort, sbPort int) string {
	t.Helper()
	dir := filepath.Join(container, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := fmt.Sprintf("PORT=%d\nSTORYBOOK_PORT=%d\n", devPort, sbPort)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mkNextDevCache plants a .next/dev/cache directory in a worktree and returns
// its path.
func mkNextDevCache(t *testing.T, wtDir string) string {
	t.Helper()
	cacheDir := filepath.Join(wtDir, ".next", "dev", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cacheDir
}

// TestStopAllSparesCacheOfAMixedScopeWorktree is the discriminating test for
// the .next/dev/cache guard, which the earlier
// TestStopAllSparesCacheForAnotherSession does NOT provide: there, every
// server of the worktree belongs to another session, so StopAll skips the
// whole worktree before stopScoped ever runs — the test passes with or
// without the guard.
//
// The demanding case is a worktree of MIXED scope: its storybook is an orphan
// (mine to stop) while its dev server belongs to another session and is
// running. StopAll must enter the worktree, stop storybook, spare dev — and
// leave .next/dev/cache (often >10 GB) alone, because the dev server that
// writes it is still live in someone else's session. Only
// shouldPurgeNextCache(inScope) produces that: computing the purge from the
// project config, or from the full server set, wipes a neighbour's cache from
// under a running dev server.
func TestStopAllSparesCacheOfAMixedScopeWorktree(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	container := t.TempDir()
	devPort := freeTestPort(t)
	sbPort := freeTestPort(t)
	dir := mkTwoServerWorktree(t, container, "wt-mixed", devPort, sbPort)
	if err := writeOwner(dir, "dev", "ws-b"); err != nil { // another session's
		t.Fatal(err)
	}
	// storybook deliberately gets no marker: an orphan, in scope for me.
	cacheDir := mkNextDevCache(t, dir)

	spawnPortListener(t, devPort, dir)
	spawnPortListener(t, sbPort, dir)
	waitPortBusyForTest(t, devPort)
	waitPortBusyForTest(t, sbPort)

	ctx := &Context{ContainerDir: container, Config: twoServerConfig()}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeOwned); err != nil {
		t.Fatal(err)
	}
	waitPortFreeForTest(t, sbPort)

	if busy, _, _ := IsPortBusy(devPort); !busy {
		t.Fatal("the other session's dev server must be spared")
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Fatalf(".next/dev/cache must survive while another session's dev server is still running, stat err = %v", err)
	}
	report := buf.String()
	if !strings.Contains(report, "kept dev (owned by ws-b)") {
		t.Fatalf("expected dev to be reported as kept, got: %s", report)
	}
	if !strings.Contains(report, "✓ storybook") {
		t.Fatalf("expected storybook to be stopped, got: %s", report)
	}
}

// TestStopAllIgnoresTheMarkerOfAServerThatIsNoLongerRunning is the
// non-regression test for the fossilised owner marker.
//
// The marker is written by two start paths but left behind by six kill paths
// (`stop --idle`, the daemon's RAM/battery/idle-purge/cap evictions, the menu
// bar, `rename --force`, the TUI's off toggle). A session with no identity —
// a plain terminal, outside cmux and without PIGTAIL_OWNER — cannot overwrite
// one on the next start, so the marker used to be permanent, and with it two
// silent failures: `pigtail stop --all` spared, forever, a dev server the
// caller had just started themselves, and that worktree's .next/dev/cache
// (>10 GB) stopped being purged because the worktree contributed nothing to
// the in-scope set.
//
// Here the port is FREE and the marker names another session — exactly the
// state one of those kill paths leaves. Ownership must not protect a server
// that is not running: the marker is cleared, the cache is purged, and
// nothing is reported as kept. On the pre-fix code all three assertions fail.
func TestStopAllIgnoresTheMarkerOfAServerThatIsNoLongerRunning(t *testing.T) {
	container := t.TempDir()
	port := freeTestPort(t) // nothing listening: the daemon already reaped it
	dir := mkFakeWorktreeDir(t, container, "wt-reaped", port)
	if err := writeOwner(dir, "dev", "ws-b"); err != nil {
		t.Fatal(err)
	}
	cacheDir := mkNextDevCache(t, dir)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "") // a plain terminal: no identity of its own

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeOwned); err != nil {
		t.Fatal(err)
	}

	if got := ReadOwner(dir, "dev"); got != "" {
		t.Fatalf("the marker of a server that is no longer running must be cleared, got %q", got)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf(".next/dev/cache must be purged once no dev server is left running, stat err = %v", err)
	}
	if report := buf.String(); strings.Contains(report, "kept dev") {
		t.Fatalf("a server that is not running must not be reported as kept, got: %s", report)
	}
}

// TestStartForegroundOwnerLifecycle covers Constat 1: the foreground path
// blocks for the entire life of the process (unlike background/cmux, where
// "launched" and "still running" are separate moments), so it is the one
// path where writing the marker just before the call and clearing it right
// after is correct — the marker's lifetime must exactly match the process's.
// Before the fix, Start's foreground branch never touched writeOwner/
// clearOwner at all, so the marker would never appear (this test fails on
// the pre-fix code, mid-run, waiting for a marker that is never written).
func TestStartForegroundOwnerLifecycle(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	container := t.TempDir()
	const wtName = "wt"
	wtDir := filepath.Join(container, wtName)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	port := freeTestPort(t)
	if err := os.WriteFile(filepath.Join(wtDir, ".env.local"), []byte(fmt.Sprintf("PORT=%d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {Command: "sleep 0.3", PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	done := make(chan error, 1)
	go func() { done <- Start(ctx, wtName, "dev", true) }()

	// Give the foreground process a moment to actually be running, then
	// assert the marker is present WHILE it blocks — the property the old
	// code never satisfied (no write on this path at all).
	deadline := time.Now().Add(2 * time.Second)
	for ReadOwner(wtDir, "dev") != "ws-a" {
		if time.Now().After(deadline) {
			t.Fatal("owner marker should be recorded while the foreground server is running")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := <-done; err != nil {
		t.Fatalf("Start (foreground) returned error: %v", err)
	}

	if got := ReadOwner(wtDir, "dev"); got != "" {
		t.Fatalf("owner marker should be cleared once the foreground process has exited, got %q", got)
	}
}

// TestStopClearsStaleOwnerWhenPortAlreadyFree covers Constat 2: a server that
// died outside of pigtail (crash, OOM, manual kill) must not keep its owner
// marker forever. Before the fix, stopServers' `if !busy { continue }`
// skipped clearOwner entirely whenever the port was already free, leaving a
// stale marker that a later scoped sweep would misread (wrongly "sparing" a
// server that no longer exists) and that a future process binding the same
// port would inherit as a foreign owner.
func TestStopClearsStaleOwnerWhenPortAlreadyFree(t *testing.T) {
	container := t.TempDir()
	const wtName = "wt"
	wtDir := filepath.Join(container, wtName)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	port := freeTestPort(t) // nothing is listening: the "server" is already dead
	if err := os.WriteFile(filepath.Join(wtDir, ".env.local"), []byte(fmt.Sprintf("PORT=%d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(wtDir, "dev", "ws-a"); err != nil {
		t.Fatal(err)
	}

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {Command: "true", PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := Stop(ctx, wtName, ""); err != nil {
		t.Fatal(err)
	}

	if got := ReadOwner(wtDir, "dev"); got != "" {
		t.Fatalf("a stale owner marker for an already-dead server should be cleared by Stop, got %q", got)
	}
}
