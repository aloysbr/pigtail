package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The session launcher under launchd (sub_x000000000000015): pigtaild started
// by launchd has HOME and PATH, nothing of a cmux pane nor of a Claude cell.
// That is enough: the cmux CLI resolves by PATH, the cell table and the
// launch_dir from HOME, and the tab it opens is the one a full environment
// would open. cmux is a fake script; the real ps is never run.

// fakeCmuxScript answers the launcher's three calls: one window, its tabs
// (none before new-workspace, one after), and new-workspace, whose argv (NUL
// separated) and process env it records.
const fakeCmuxScript = `#!/bin/sh
d="$FAKE_CMUX_DIR"
case "$1" in
list-windows) printf '[{"id":"aaaaaaaa-0000-4000-8000-000000000001","index":0}]' ;;
workspace)
  if [ -f "$d/tab" ]; then
    printf '{"workspaces":[{"id":"bbbbbbbb-0000-4000-8000-000000000001","ref":"workspace:1","index":0,"title":"t"}]}'
  else
    printf '{"workspaces":[]}'
  fi ;;
new-workspace)
  for a in "$@"; do printf '%s\0' "$a"; done > "$d/argv"
  env > "$d/env"
  : > "$d/tab"
  printf 'OK workspace:1\n' ;;
*) echo "fake cmux: unexpected $1" >&2; exit 1 ;;
esac
`

// TestHelperLaunchEnv is the child of TestLaunchdEnvIsEnough: it strips the
// environment as run() does, then launches a session through the daemon's
// own router, on the real cell table, launch_dir and cmux CLI resolution.
func TestHelperLaunchEnv(t *testing.T) {
	if os.Getenv("PIGTAILD_LAUNCH_HELPER") != "1" {
		t.Skip("helper process of TestLaunchdEnvIsEnough")
	}
	stripCmuxEnv()
	if _, err := exec.LookPath("ps"); err != nil {
		t.Fatalf("ps is not on the launchd PATH: %v", err) // looked up, never run
	}
	cells, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil {
		t.Fatalf("cell table: %v", err)
	}
	// The builtin declares no launch_dir: the builtin's one scope, with one.
	launchTable, err := scope.ParseTable([]byte("default: "+string(scope.Builtin().Master())+"\nscopes:\n  "+
		string(scope.Builtin().Master())+": {sees_all: true, launch_dir: ~/Developer/work}\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Use(launchTable)()
	master := scope.Master()
	if _, ok := cells.Cells["mp"]; !ok {
		t.Fatalf("cell table read without its cell: %+v", cells)
	}
	dir, ok := scope.Active().LaunchDir(master)
	home, _ := os.UserHomeDir()
	if !ok || !strings.HasPrefix(dir, home+string(os.PathSeparator)) {
		t.Fatalf("launch_dir = %q, %v; want under HOME %s", dir, ok, home)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_l", Name: "L", Kind: "ongoing", Scope: string(master), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_l", ThemeID: "thm_l", Title: "Launch", ShortTitle: "Launch", Slug: "l", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	work := os.Getenv("PIGTAILD_LAUNCH_WORK")
	srv, err := daemon.New(daemon.Config{SocketPath: os.Getenv("PIGTAILD_LAUNCH_SOCK"), LaunchOverrides: func(d *launch.Deps) {
		d.Registry = spawnreg.Open(filepath.Join(work, "spawns.jsonl"))
		d.Procs = func() ([]claudesession.Proc, error) { return nil, nil } // never the real ps
		d.Environ = func(int) (string, error) { return "", nil }
	}}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/subjects/sub_l/launch", bytes.NewReader(nil))
	req.RemoteAddr = ""
	req.Header.Set(pigtail.HeaderScope, string(master))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST launch = %d %s", rec.Code, rec.Body)
	}
	var env struct {
		Data pigtail.SessionLaunch `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data.Cell != "mp" || env.Data.Cwd != dir {
		t.Fatalf("launch = %s (%v)", rec.Body, err)
	}
}

// launchRun runs the helper under env (plus its own plumbing: a work dir,
// the daemon socket sock) and returns
// the argv and the process env the fake cmux recorded.
func launchRun(t *testing.T, sock string, env []string) (argv, procEnv []string) {
	t.Helper()
	work := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLaunchEnv$", "-test.v")
	cmd.Env = append(append([]string{}, env...),
		"PIGTAILD_LAUNCH_HELPER=1", "PIGTAILD_LAUNCH_WORK="+work, "PIGTAILD_LAUNCH_SOCK="+sock, "FAKE_CMUX_DIR="+work)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestHelperLaunchEnv") {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	a, err := os.ReadFile(filepath.Join(work, "argv"))
	if err != nil {
		t.Fatalf("the fake cmux never got new-workspace: %v\n%s", err, out)
	}
	e, err := os.ReadFile(filepath.Join(work, "env"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(a), "\x00"), "\x00"), strings.Split(strings.TrimSpace(string(e)), "\n")
}

func TestLaunchdEnvIsEnough(t *testing.T) {
	if testing.Short() {
		t.Skip("starts helper processes")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	bindir := filepath.Join(dir, "bin")
	cellDir := filepath.Join(home, "cells", "mp")
	cfgDir := filepath.Join(home, ".config", "pigtail")
	for _, d := range []string{bindir, cellDir, cfgDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(bindir, "cmux")
	if err := os.WriteFile(bin, []byte(fakeCmuxScript), 0o755); err != nil {
		t.Fatal(err)
	}
	table := "cells:\n  mp:\n    config_dir: ~/cells/mp\n    scope: " + string(scope.Master()) + "\n    account: m\n    rank: 0\n"
	if err := os.WriteFile(filepath.Join(cfgDir, claudeprofile.Filename), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "p.sock") // one daemon socket for both runs
	path := "PATH=" + bindir + ":/usr/bin:/bin"

	launchd := []string{"HOME=" + home, path}
	argvL, envL := launchRun(t, sock, launchd)

	full := append(append([]string{}, launchd...),
		"CMUX_SURFACE_ID=80914656-BF88-4780-9982-CA1665755494",
		"CMUX_WORKSPACE_ID=B89B4597-2397-45F6-81D9-95F3B0FA83F8",
		"CMUX_SOCKET_PATH="+filepath.Join(dir, "callers-cmux.sock"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(dir, "the-daemons-own-cell"),
		"PIGTAILD_CANARY=leak")
	argvF, envF := launchRun(t, sock, full)

	if strings.Join(argvL, "\x00") != strings.Join(argvF, "\x00") {
		t.Fatalf("the tab differs with a full environment:\nlaunchd %q\nfull    %q", argvL, argvF)
	}
	t.Logf("new-workspace argv: %q", argvL)
	if len(argvL) == 0 || argvL[0] != "new-workspace" {
		t.Fatalf("argv = %q", argvL)
	}
	var envArgs []string
	for i := 0; i+1 < len(argvL); i++ {
		if argvL[i] == "--env" {
			envArgs = append(envArgs, argvL[i+1])
		}
	}
	joined := strings.Join(argvL, " ")
	if !containsKV(envArgs, "CLAUDE_CONFIG_DIR="+cellDir) || !strings.Contains(joined, "Work on pigtail subject sub_l: Launch.") {
		t.Fatalf("argv lacks the cell or the prompt: %q", argvL)
	}
	if strings.Contains(joined, "the-daemons-own-cell") || strings.Contains(joined, "PIGTAILD_CANARY") || strings.Contains(joined, "callers-cmux.sock") {
		t.Fatalf("a daemon variable reached the tab: %q", argvL)
	}
	for _, kv := range envArgs {
		if strings.HasPrefix(kv, "CMUX_") && !strings.HasPrefix(kv, "CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV") {
			t.Fatalf("argv carries %q", kv)
		}
	}
	for name, pe := range map[string][]string{"launchd": envL, "full": envF} {
		if !containsKV(pe, "FAKE_CMUX_DIR=") {
			t.Fatalf("%s: the env probe read nothing useful: %q", name, pe)
		}
		for _, kv := range pe {
			if strings.HasPrefix(kv, "CMUX_") {
				t.Errorf("%s: cmux ran with %q in its environment", name, kv)
			}
		}
	}
}

func containsKV(kvs []string, prefix string) bool {
	for _, kv := range kvs {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}
