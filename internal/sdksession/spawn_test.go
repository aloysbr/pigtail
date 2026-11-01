package sdksession

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

var dirtyEnv = []string{
	"PATH=/usr/bin:/x/cmux-cli-shims/bin:relative:/opt/bin", "HOME=/home/ada", "LANG=fr_FR.UTF-8",
	"CMUX_WORKSPACE_ID=w", "CMUX_CLAUDE_HOOKS_DISABLED=1", "ANTHROPIC_API_KEY=k", "CLAUDE_CONFIG_DIR=/other", "CLAUDECODE=1",
	"CLAUDE_CODE_ENTRYPOINT=cli", "PIGTAIL_SESSION=s", "PIGTAIL_SOCKET=/old.sock", "CMUX_CLAUDE_WRAPPER_SHIM_ROOT=/x/cmux-cli-shims/bin",
}

func TestEnvDropsInheritedAndAdds(t *testing.T) {
	got := envMap(Env(dirtyEnv, SpawnConfig{DefaultSocket: true, SocketPath: "/def.sock"}, "/cell", sid))
	for k := range got {
		if strings.HasPrefix(k, "CMUX_") || strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "CLAUDE") ||
			(strings.HasPrefix(k, "PIGTAIL_") && k != "PIGTAIL_SDK_SESSION") {
			if k != "CLAUDE_CONFIG_DIR" {
				t.Errorf("inherited %s survived", k)
			}
		}
	}
	if got["CLAUDE_CONFIG_DIR"] != "/cell" || got["PIGTAIL_SDK_SESSION"] != sid {
		t.Errorf("config dir / marker wrong: %v", got)
	}
	if _, ok := got["CMUX_CLAUDE_HOOKS_DISABLED"]; ok {
		t.Error("CMUX_CLAUDE_HOOKS_DISABLED must not be set (CA10)")
	}
	if got["PATH"] != "/usr/bin:/opt/bin" {
		t.Errorf("PATH = %q", got["PATH"])
	}
	if got["HOME"] != "/home/ada" || got["LANG"] != "fr_FR.UTF-8" {
		t.Errorf("other vars lost: %v", got)
	}
	if _, ok := got["PIGTAIL_SOCKET"]; ok {
		t.Error("default socket: PIGTAIL_SOCKET must be absent")
	}
}

func TestEnvSocketOnlyWhenNotDefault(t *testing.T) {
	got := envMap(Env(dirtyEnv, SpawnConfig{DefaultSocket: false, SocketPath: "/custom.sock"}, "/cell", sid))
	if got["PIGTAIL_SOCKET"] != "/custom.sock" {
		t.Errorf("PIGTAIL_SOCKET = %q", got["PIGTAIL_SOCKET"])
	}
	got = envMap(Env(dirtyEnv, SpawnConfig{DefaultSocket: true, SocketPath: "/custom.sock"}, "/cell", sid))
	if _, ok := got["PIGTAIL_SOCKET"]; ok {
		t.Error("PIGTAIL_SOCKET set for the default socket")
	}
}

func spawnMeta() Meta {
	return Meta{ID: sid, SubjectID: sub, Cwd: "/tmp/work", ConfigDir: "/tmp/cell", Mode: "default", Prompt: "p", SessionUUID: uuid}
}

func TestSpawnWritesMetaThenHostPID(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	r := f.registry(dir)
	cfg := SpawnConfig{Node: "/usr/bin/node", HostDir: "/opt/sdkhost", Claude: "/bin/claude", DefaultSocket: true}
	var seen *exec.Cmd
	var args []string
	var metaAtStart Meta
	var started int
	s, err := r.Spawn(cfg, spawnMeta(), func(c *exec.Cmd) error {
		seen = c
		args = append([]string(nil), c.Args...)
		if err := readJSON(filepath.Join(dir, sid, "meta.json"), &metaAtStart); err != nil {
			t.Errorf("meta.json must exist before start: %v", err)
		}
		// Run something harmless in place of the host, to get a real pid.
		c.Path, c.Args = "/usr/bin/true", []string{"true"}
		err := c.Start()
		if err == nil {
			started = c.Process.Pid
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if metaAtStart.Version != 1 || metaAtStart.HostPID != 0 || metaAtStart.Claude != "/bin/claude" || metaAtStart.CreatedAt.IsZero() {
		t.Errorf("meta at start = %+v", metaAtStart)
	}
	var after Meta
	if err := readJSON(filepath.Join(dir, sid, "meta.json"), &after); err != nil {
		t.Fatal(err)
	}
	if after.HostPID != started || started == 0 {
		t.Errorf("host_pid = %d, started pid %d", after.HostPID, started)
	}
	if s.ID != sid || s.State != "starting" || !s.Live {
		t.Errorf("session = %+v", s)
	}
	if seen.SysProcAttr == nil || !seen.SysProcAttr.Setsid {
		t.Error("host must run in its own session (Setsid)")
	}
	want := []string{"/usr/bin/node", "--experimental-strip-types", "--no-warnings", "/opt/sdkhost/main.ts", filepath.Join(dir, sid)}
	if !equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	if seen.Stdin != nil {
		t.Error("stdin must be nil (/dev/null)")
	}
	if seen.Stdout == nil || seen.Stderr == nil {
		t.Error("stdout/stderr must go to host.out / host.err")
	}
	if e := envMap(seen.Env); e["PIGTAIL_SDK_SESSION"] != sid || e["CLAUDE_CONFIG_DIR"] != "/tmp/cell" {
		t.Errorf("env = %v", e)
	}
	for _, n := range []string{"host.out", "host.err"} {
		if _, err := os.Stat(filepath.Join(dir, sid, n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSpawnStartFailureLeavesFailed(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	r := f.registry(dir)
	s, err := r.Spawn(SpawnConfig{Node: "/nope/node", HostDir: "/h"}, spawnMeta(), func(*exec.Cmd) error { return errors.New("fork refused") })
	if err == nil {
		t.Fatal("want the start error")
	}
	if s.State != "failed" || s.Live || !strings.Contains(s.Reason, "fork refused") {
		t.Errorf("session = %+v", s)
	}
	got, _ := r.Get(sid)
	if got.State != "failed" || !strings.Contains(got.Reason, "fork refused") {
		t.Errorf("Get after failure = %+v", got)
	}
}

// B6: the real host and a fake claude, to prove what claude's environment holds.
func TestFakeClaudeSeesCleanEnv(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	hostDir, _ := filepath.Abs(filepath.Join("..", "..", "clients", "sdkhost"))
	if _, err := os.Stat(filepath.Join(hostDir, "node_modules")); err != nil {
		t.Skip("clients/sdkhost/node_modules missing (make sdkhost)")
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, "claude-env.txt")
	fake := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\nenv > '" + out + "'\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"CLAUDE_X_SENTINEL", "CMUX_X_SENTINEL", "ANTHROPIC_X_SENTINEL", "PIGTAIL_X_SENTINEL"} {
		t.Setenv(k, "leak")
	}
	cell := filepath.Join(tmp, "cell")
	if err := os.MkdirAll(cell, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "state")
	r := &Registry{Dir: dir}
	m := spawnMeta()
	m.Cwd, m.ConfigDir = tmp, cell
	cfg := SpawnConfig{Node: node, HostDir: hostDir, Claude: fake, DefaultSocket: true}
	if _, err := r.Spawn(cfg, m, func(c *exec.Cmd) error { return c.Start() }); err != nil {
		t.Fatal(err)
	}
	// The fake does not speak the protocol and exits at once: the host's query
	// iterator ends. Measured: that reads as `ended` (claude left by itself), not
	// `failed` as the plan expected; either way the host stops.
	deadline := time.Now().Add(40 * time.Second)
	var s Session
	for time.Now().Before(deadline) {
		s, _ = r.Get(sid)
		if s.State == "failed" || s.State == "ended" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s.State != "failed" && s.State != "ended" {
		t.Fatalf("host state = %q, want failed or ended (fake claude does not speak the protocol)", s.State)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the fake claude never ran: %v", err)
	}
	got := envMap(strings.Split(string(raw), "\n"))
	if got["PIGTAIL_SDK_SESSION"] != sid || got["CLAUDE_CONFIG_DIR"] != cell || got["PATH"] == "" || got["HOME"] == "" {
		t.Errorf("claude env lacks the marker / cell / PATH / HOME: %v", got)
	}
	// Set by the SDK itself, not inherited: CLAUDE_CODE_ENTRYPOINT=sdk-ts
	// (spikes/sdk/RESULTS.md) and the others seen in the env of a fake claude
	// run by SDK 0.3.312 (measured 2026-10-29, T8).
	tolerated := map[string]bool{"CLAUDE_CONFIG_DIR": true, "CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_SDK_READS_SESSION_STATE": true}
	for k := range got {
		switch {
		case strings.HasSuffix(k, "_X_SENTINEL"):
			t.Errorf("sentinel %s leaked to claude", k)
		case strings.HasPrefix(k, "CLAUDE") && !tolerated[k] && !strings.HasPrefix(k, "CLAUDE_AGENT_SDK_"):
			t.Errorf("unexpected CLAUDE* variable in claude's env: %s=%s", k, got[k])
		case strings.HasPrefix(k, "CMUX_"), strings.HasPrefix(k, "ANTHROPIC_"):
			t.Errorf("%s leaked to claude", k)
		case strings.HasPrefix(k, "PIGTAIL_") && k != "PIGTAIL_SDK_SESSION":
			t.Errorf("%s leaked to claude", k)
		}
	}
	if _, ok := got["CMUX_CLAUDE_HOOKS_DISABLED"]; ok {
		t.Error("CMUX_CLAUDE_HOOKS_DISABLED leaked")
	}
}

func TestReserveCountsAsLiveBeforeStart(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	r := f.registry(dir)
	rv, err := r.Reserve(SpawnConfig{Node: "/n", HostDir: "/h", Claude: "/bin/claude", DefaultSocket: true}, spawnMeta())
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := r.LiveForSubject(sub); !ok || id != sid {
		t.Fatalf("a reservation must be live before the host starts: %q %v", id, ok)
	}
	s, err := rv.Start(func(c *exec.Cmd) error { return nil })
	if err != nil || s.State != "starting" {
		t.Fatalf("start: %+v %v", s, err)
	}
}

func TestDialWorksNDJSON(t *testing.T) {
	dir := testutil.MkdirTemp(t, "sdkd-")
	r := &Registry{Dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", r.SockPath(sid))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
		_, _ = c.Write([]byte(`{"ok":true}` + "\n"))
	}()
	reply, err := r.Dial(sid, map[string]any{"op": "end"})
	if err != nil || !reply.OK {
		t.Fatalf("reply = %+v, %v", reply, err)
	}
	if line := <-got; strings.TrimSpace(line) != `{"op":"end"}` {
		t.Fatalf("host got %q", line)
	}
	if _, err := r.Dial("sdk_nohost", map[string]any{"op": "end"}); err == nil {
		t.Fatal("dialing a missing socket must fail")
	}
}
