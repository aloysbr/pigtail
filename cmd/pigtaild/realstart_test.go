package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/testutil"
)

// The real start, through run() in a child process (N2 of lot 1, plan
// 2026-10-18-install-guard): versionMode and checkArgs at the head of main,
// the flags moved into registerFlags and config() split out of run() must
// keep the start as it was — an invalid spaces.yaml still falls back to the
// built-in table with one ERROR line, through run() and not only through
// loadDaemonSpaceTable (correction of 2026-10-12, sub_x000000000000041).

// TestHelperRunDaemonArgs is the child of startDaemonChild: it starts the real
// run() with the arguments the parent passed as JSON.
func TestHelperRunDaemonArgs(t *testing.T) {
	if os.Getenv("PIGTAILD_HELPER_ARGS") == "" {
		t.Skip("helper process of startDaemonChild")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("PIGTAILD_HELPER_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"pigtaild"}, args...)
	if err := run(); err != nil {
		_, _ = os.Stderr.WriteString("run: " + err.Error() + "\n")
		os.Exit(3)
	}
}

// daemonChild is a pigtaild started by startDaemonChild.
type daemonChild struct {
	cmd      *exec.Cmd
	errPath  string // the child's stderr
	cmuxLog  string // one line per call of the fake cmux
	exited   chan struct{}
	exitErr  error
	tempRoot string
}

func (d *daemonChild) stderr(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(d.errPath)
	return string(b)
}

func (d *daemonChild) cmuxCalls() string {
	b, _ := os.ReadFile(d.cmuxLog)
	return string(b)
}

func (d *daemonChild) running() bool {
	select {
	case <-d.exited:
		return false
	default:
		return true
	}
}

// shortTempDir is a throwaway dir short enough for a Unix socket path (104
// bytes on darwin; t.TempDir() under /var/folders overshoots it).
func shortTempDir(t *testing.T, prefix string) string {
	t.Helper()
	return testutil.MkdirTemp(t, strings.TrimPrefix(prefix, testutil.TempPrefix))
}

// daemonChildArgs puts -tmp-sweep=off first: the child runs the real run()
// with the flags' defaults, and a test daemon must never sweep the real
// $TMPDIR (TestDaemonChildTurnsTmpSweepOff). A later flag of the caller wins.
func daemonChildArgs(args []string) []string {
	if len(args) > 0 && args[0] == "-tmp-sweep=off" {
		return args
	}
	return append([]string{"-tmp-sweep=off"}, args...)
}

// startDaemonChild runs the real run() in a child process with args, a
// throwaway HOME / XDG_CONFIG_HOME (xdg) / XDG_STATE_HOME and a fake cmux that
// notes each of its calls. The caller passes --socket and --db under a
// throwaway dir; the child is killed at the end of the test.
func startDaemonChild(t *testing.T, args []string, xdg string) *daemonChild {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a daemon child process")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	state := filepath.Join(root, "state")
	for _, d := range []string{home, state, xdg} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d := &daemonChild{
		errPath:  filepath.Join(root, "stderr"),
		cmuxLog:  filepath.Join(root, "cmux.calls"),
		exited:   make(chan struct{}),
		tempRoot: root,
	}
	bin := filepath.Join(root, "cmux")
	script := "#!/bin/sh\necho \"$*\" >> '" + d.cmuxLog + "'\nprintf '{\"windows\":[]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(daemonChildArgs(args))
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.Create(d.errPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errFile.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperRunDaemonArgs$")
	d.cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PIGTAILD_HELPER_ARGS=" + string(raw),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + xdg,
		"XDG_STATE_HOME=" + state,
		cmux.EnvBundledCLI + "=" + bin,
	}
	d.cmd.Stdout = io.Discard
	d.cmd.Stderr = errFile
	if err := d.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		d.exitErr = d.cmd.Wait()
		close(d.exited)
	}()
	t.Cleanup(func() {
		_ = d.cmd.Process.Kill()
		<-d.exited
	})
	return d
}

// waitSocket waits up to 20 s for the child to listen on sock.
func (d *daemonChild) waitSocket(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !d.running() {
			t.Fatalf("the daemon exited (%v) before listening:\n%s", d.exitErr, d.stderr(t))
		}
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no socket at %s within 20 s:\n%s", sock, d.stderr(t))
}

// getOverSocket GETs path on the daemon's socket and returns status + body.
func getOverSocket(t *testing.T, sock, path string) (int, []byte) {
	t.Helper()
	hc := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}},
	}
	req, err := http.NewRequest("GET", "http://pigtail"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Pigtail-API", "v1")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// writeInvalidSpaces puts an unparsable spaces.yaml in xdg/pigtail.
func writeInvalidSpaces(t *testing.T, xdg string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", "spaces.yaml"), []byte("spaces: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunStartsWithInvalidSpacesAndServesBuild(t *testing.T) {
	dir := shortTempDir(t, "pt-rs-")
	xdg := filepath.Join(t.TempDir(), "xdg")
	writeInvalidSpaces(t, xdg)
	sock := filepath.Join(dir, "p.sock")
	d := startDaemonChild(t, []string{
		"--socket", sock, "--db", filepath.Join(dir, "p.db"),
		"--http=", "--cmux-check-after-relaunch=off",
	}, xdg)
	d.waitSocket(t, sock)

	code, body := getOverSocket(t, sock, "/version")
	if code != 200 {
		t.Fatalf("GET /version = %d: %s", code, body)
	}
	var v struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("GET /version: %v: %s", err, body)
	}
	if _, ok := v.Data["daemon_commit"]; !ok {
		t.Fatalf("GET /version has no daemon_commit: %s", body)
	}
	if !strings.Contains(d.stderr(t), "ERROR space table") {
		t.Fatalf("no ERROR space table line on stderr:\n%s", d.stderr(t))
	}
	if !d.running() {
		t.Fatalf("the daemon exited (%v):\n%s", d.exitErr, d.stderr(t))
	}
}

// The real start with every flag `pigtail setup` writes (all parsed by run(),
// then overridden — the last flag wins — so nothing reaches the NAS, a gate,
// cmux or the HTTP port).
func TestRunStartsWithSetupPlistArgs(t *testing.T) {
	plistArgs, err := setup.ProgramArguments(fullSetupPlist(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := shortTempDir(t, "pt-rs-")
	xdg := filepath.Join(t.TempDir(), "xdg")
	writeInvalidSpaces(t, xdg)
	sock := filepath.Join(dir, "p.sock")
	args := append(append([]string{}, plistArgs[1:]...),
		"--socket", sock, "--db", filepath.Join(dir, "p.db"),
		"--http=", "--db-offsite-dir=", "--db-offsite-fallback-dest=", "--gate-cmd=",
		"--cmux-sidebar=false", "--cmux-folders=false", "--cmux-check-after-relaunch=off")
	d := startDaemonChild(t, args, xdg)
	d.waitSocket(t, sock)

	if code, body := getOverSocket(t, sock, "/health"); code != 200 {
		t.Fatalf("GET /health = %d: %s", code, body)
	}
	if !strings.Contains(d.stderr(t), "ERROR space table") {
		t.Fatalf("no ERROR space table line on stderr:\n%s", d.stderr(t))
	}
	if calls := d.cmuxCalls(); calls != "" {
		t.Fatalf("the fake cmux was called:\n%s", calls)
	}
	if !d.running() {
		t.Fatalf("the daemon exited (%v):\n%s", d.exitErr, d.stderr(t))
	}
}

// An invalid scopes.yaml stops the daemon before its socket is bound: with a
// neutral single-scope builtin, falling back to it would make every caller
// the master (plan alpha-oss, P1-A1). The child's run() returns the error
// (main turns it into log.Fatal, exit 1).
func TestDaemonRefusesInvalidScopeTable(t *testing.T) {
	dir := shortTempDir(t, "pt-st-")
	xdg := filepath.Join(t.TempDir(), "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(xdg, "pigtail", "scopes.yaml")
	if err := os.WriteFile(path, []byte("default: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "p.sock")
	d := startDaemonChild(t, []string{
		"--socket", sock, "--db", filepath.Join(dir, "p.db"),
		"--http=", "--cmux-check-after-relaunch=off",
	}, xdg)
	select {
	case <-d.exited:
	case <-time.After(20 * time.Second):
		t.Fatalf("the daemon still runs 20 s after an invalid scopes.yaml:\n%s", d.stderr(t))
	}
	if d.exitErr == nil {
		t.Fatalf("the daemon exited 0 on an invalid scopes.yaml:\n%s", d.stderr(t))
	}
	out := d.stderr(t)
	if !strings.Contains(out, "ERROR scope table:") || !strings.Contains(out, "fix "+path+" and restart") {
		t.Fatalf("stderr = %q, want « ERROR scope table: … — fix %s and restart »", out, path)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket %s exists (%v): the daemon bound it before refusing", sock, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "p.db")); !os.IsNotExist(err) {
		t.Fatalf("the database was opened before the refusal (%v)", err)
	}
}
