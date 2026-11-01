package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const respawnPrompt = "/example-skill resume sub_p"

// armCalls records each run handed to the stubbed respawnArm.
var armCalls []respawnRun

// installRespawnFake is spawn's fake world plus what respawn reads: the
// caller's surface, launcher proto 2, its relaunch file and directory. The
// stubbed arm records the call and returns worker pid 999.
func installRespawnFake(t *testing.T) *spawnFake {
	t.Helper()
	testutil.SerialOnly(t) // swaps respawnCurrentSurfaceRef
	f := installSpawnFake(t)
	tmp := t.TempDir()
	wt := filepath.Join(tmp, "wt")
	if err := os.Mkdir(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	f.envs[f.caller.PID] = "CLAUDE_CONFIG_DIR=" + os.Getenv("CLAUDE_CONFIG_DIR") + " CMUX_WORKSPACE_ID=" + spawnCallerWS +
		" CMUX_SURFACE_ID=S1 CLAUDE_RELAUNCH_FILE=" + filepath.Join(tmp, "k.sid") + " CLAUDE_LAUNCH_PROTO=2 PWD=" + wt
	savedRef, savedArm := respawnCurrentSurfaceRef, respawnArm
	t.Cleanup(func() { respawnCurrentSurfaceRef, respawnArm, armCalls = savedRef, savedArm, nil })
	armCalls = nil
	respawnCurrentSurfaceRef = func() (string, error) { return "surface:7", nil }
	respawnArm = func(_ *spawnreg.Registry, run respawnRun, _ io.Writer) (int, error) {
		armCalls = append(armCalls, run)
		return 999, nil
	}
	return f
}

func runRespawnCLI(t *testing.T, s *stubDaemon, args ...string) (string, string, error) {
	t.Helper()
	var o, e bytes.Buffer
	var err error
	withStubDaemon(t, s, func() {
		cmd := sessionCmd()
		silenceUsageOnRunE(cmd)
		cmd.SetOut(&o)
		cmd.SetErr(&e)
		cmd.SetArgs(append([]string{"respawn"}, args...))
		err = cmd.Execute()
	})
	return o.String(), e.String(), err
}

func TestRespawnDryRunTouchesNothing(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nothing armed", "sub_p", "cell      p", "surface   surface:7", "launcher  proto 2", "idle      10m0s",
		"mode      auto (launch auto, cell default; current auto)", "prompt    " + respawnPrompt} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run lacks %q:\n%s", want, out)
		}
	}
	if f.consumed != 0 || len(armCalls) != 0 {
		t.Fatal("dry-run consumed the stamp or armed")
	}
	if es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries(); len(es) != 0 {
		t.Fatal("dry-run wrote the registry")
	}
}

func TestRespawnRefusesOldLauncher(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	f.envs[f.caller.PID] = withoutVar(f.envs[f.caller.PID], "CLAUDE_LAUNCH_PROTO")
	_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt)
	if err == nil || !strings.Contains(err.Error(), "speaks proto none (fresh relaunch needs ≥ 2)") {
		t.Fatalf("err = %v", err)
	}
	if f.consumed != 0 || len(armCalls) != 0 {
		t.Fatal("refusal touched something")
	}
}

// a Claude that c did not start is relaunched through its tab's shell.
func TestRespawnNotStartedByCRelaunchesByShell(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	env := withoutVar(f.envs[f.caller.PID], "CLAUDE_LAUNCH_PROTO")
	f.envs[f.caller.PID] = withoutVar(env, "CLAUDE_RELAUNCH_FILE")
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run")
	if err != nil || !strings.Contains(out, "launcher  none (not started by c) — relaunch typed into this tab's shell: c p") {
		t.Fatalf("dry-run: %v\n%s", err, out)
	}
	out, _, err = runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run", "--json")
	var v map[string]any
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v["relaunch"] != "shell" || v["launch_proto"] != float64(0) {
		t.Fatalf("dry-run json: %v %q", err, out)
	}
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt); err != nil {
		t.Fatal(err)
	}
	if len(armCalls) != 1 || armCalls[0].Relaunch != relaunchShell || armCalls[0].RelaunchFile != "" {
		t.Fatalf("armed = %+v", armCalls)
	}
}

func TestReadRespawnRunShellNeedsNoRelaunchFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "r.json")
	run := respawnRun{ID: "r1", ClaudePID: 42, SurfaceID: "S1", Relaunch: relaunchShell}
	if err := writeRespawnRun(p, run); err != nil {
		t.Fatal(err)
	}
	if _, err := readRespawnRun(p); err != nil {
		t.Fatalf("shell run refused: %v", err)
	}
	old := respawnRun{ID: "r2", ClaudePID: 42, SurfaceID: "S1", RelaunchFile: "/x/k.sid"} // a run file written before
	_ = writeRespawnRun(p, old)
	got, err := readRespawnRun(p)
	if err != nil || got.Relaunch != relaunchLauncher {
		t.Fatalf("old run file: %+v %v", got, err)
	}
	_ = writeRespawnRun(p, respawnRun{ID: "r3", ClaudePID: 42, SurfaceID: "S1"})
	if _, err := readRespawnRun(p); err == nil {
		t.Fatal("a launcher run without relaunch file must stay refused")
	}
}

func TestShellPathOK(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"/tmp/a'b.zsh", "/tmp/où.zsh", "/" + strings.Repeat("a", 200) + ".zsh"} {
		if shellPathOK(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := shellPathOK("/Users/x/.local/state/pigtail/respawn/20261013T120000.000-ab12.relaunch.zsh"); err != nil {
		t.Fatal(err)
	}
}

// D-requirement: the per-subject check never counts the session being replaced,
// but still refuses a live session of the subject in another workspace.
func TestRespawnSubjectGuardExcludesCaller(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installRespawnFake(t)
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnCallerWS, SubjectID: "sub_p", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.addProc(f.caller.PID, f.caller.Args, f.envs[f.caller.PID]) // the caller is live in its bound workspace
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run")
	if err != nil {
		t.Fatalf("the replaced session was counted: %v", err)
	}
	if !strings.Contains(out, "workspace already bound to sub_p") {
		t.Errorf("bound workspace not shown:\n%s", out)
	}
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnOtherWS, SubjectID: "sub_p", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run"); err == nil || !strings.Contains(err.Error(), "already has a live session") {
		t.Fatalf("another live session of the subject = %v", err)
	}
}

// A margin row is no launch: carrying the WorkspaceID of a live workspace it
// never makes that workspace a live session of the subject.
func TestRespawnCandidatesIgnoreMarginEntryWithWorkspace(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: spawnreg.KindMargin, SubjectID: "sub_p", WorkspaceID: spawnOtherWS, Cell: "p", Margin: "forced p", Result: "forced"})
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run"); err != nil {
		t.Fatalf("a margin entry naming a live workspace blocked the respawn: %v", err)
	}
}

// TestRespawnIgnoresTheBurst: no burst check — a respawn replaces, net zero.
func TestRespawnIgnoresTheBurst(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	for i, ws := range []string{"A1", "A2", "A3", "A4", "A5", "A6", "A7", "A8", "A9", "A10"} {
		_ = reg.Append(spawnreg.Entry{TS: time.Now().UTC(), SubjectID: "sub_other", WorkspaceID: ws, Result: "launched"})
		f.addProc(7000+i, "/x/bin/claude", "CMUX_WORKSPACE_ID="+ws)
	}
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run"); err != nil {
		t.Fatalf("10 recent launches blocked a respawn: %v", err)
	}
}

func TestRespawnRefusals(t *testing.T) {
	cases := []struct {
		name    string
		cell    string // "t" or "p" (default p)
		setup   func(t *testing.T, f *spawnFake, st bindingStore)
		args    []string
		wantErr string
	}{
		{"invalid prompt", "", nil, []string{"--subject", "sub_p", "--prompt", "a\nb"}, "control character"},
		{"prompt dash", "", nil, []string{"--subject", "sub_p", "--prompt", "-p x"}, "starts with '-'"},
		{"idle timeout too short", "", nil, []string{"--subject", "sub_p", "--prompt", respawnPrompt, "--idle-timeout", "10s"}, "--idle-timeout 10s is out of range"},
		{"idle timeout too long", "", nil, []string{"--subject", "sub_p", "--prompt", respawnPrompt, "--idle-timeout", "3h"}, "out of range"},
		{"not in cmux", "", func(t *testing.T, _ *spawnFake, _ bindingStore) { t.Setenv(cmux.EnvWorkspaceID, "") },
			nil, "not in a cmux pane"},
		{"no Claude above", "", func(_ *testing.T, f *spawnFake, _ bindingStore) { f.caller = claudesession.Proc{} },
			nil, "no Claude session above this pigtail — respawn only runs"},
		{"caller env unreadable", "", func(_ *testing.T, f *spawnFake, _ bindingStore) { f.badEnv[4242] = true },
			nil, "cannot read the environment of the calling Claude"},
		{"config dir mismatch", "", func(t *testing.T, _ *spawnFake, _ bindingStore) {
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "work"))
		}, nil, "the cell is the session's"},
		{"no cell", "", func(t *testing.T, f *spawnFake, _ bindingStore) {
			d := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", d)
			f.envs[4242] = withVar(f.envs[4242], "CLAUDE_CONFIG_DIR", d)
		}, nil, "no Claude cell owns"},
		{"subject not found", "", nil, []string{"--subject", "sub_nope", "--prompt", respawnPrompt}, "subject sub_nope not found"},
		{"other scope", "", nil, []string{"--subject", "sub_t", "--prompt", respawnPrompt}, "--cross-scope"},
		{"no permission record", "", func(_ *testing.T, f *spawnFake, _ bindingStore) { f.stamp = "" },
			nil, "current permission mode unknown"},
		{"bypass", "", func(_ *testing.T, f *spawnFake, _ bindingStore) { f.stamp = "bypassPermissions" },
			nil, "bypassPermissions now"},
		{"launcher proto 1", "", func(_ *testing.T, f *spawnFake, _ bindingStore) {
			f.envs[4242] = withVar(f.envs[4242], "CLAUDE_LAUNCH_PROTO", "1")
		}, nil, "speaks proto 1 (fresh relaunch needs ≥ 2)"},
		{"no relaunch file", "", func(_ *testing.T, f *spawnFake, _ bindingStore) {
			f.envs[4242] = withoutVar(f.envs[4242], "CLAUDE_RELAUNCH_FILE")
		}, nil, "without CLAUDE_RELAUNCH_FILE"},
		{"no surface", "", func(_ *testing.T, f *spawnFake, _ bindingStore) {
			f.envs[4242] = withoutVar(f.envs[4242], "CMUX_SURFACE_ID")
		}, nil, "carries no CMUX_SURFACE_ID"},
		{"surface ref unreadable", "", func(_ *testing.T, _ *spawnFake, _ bindingStore) {
			respawnCurrentSurfaceRef = func() (string, error) { return "", errors.New("cmux identify: boom") }
		}, nil, "locate this tab in cmux"},
		{"no PWD", "", func(_ *testing.T, f *spawnFake, _ bindingStore) { f.envs[4242] = withoutVar(f.envs[4242], "PWD") },
			nil, "carries no PWD"},
		{"cwd gone", "", func(_ *testing.T, f *spawnFake, _ bindingStore) {
			f.envs[4242] = withVar(f.envs[4242], "PWD", "/nonexistent/wt")
		}, nil, "is gone"},
		{"bound to another subject", "", func(t *testing.T, _ *spawnFake, st bindingStore) {
			if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnCallerWS, SubjectID: "sub_t", Source: "ptnew", BoundAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
		}, nil, "workspace bound to sub_t"},
		{"unreadable env on a real call", "", func(_ *testing.T, f *spawnFake, _ bindingStore) {
			f.addProc(8000, "/x/bin/claude", "")
			f.badEnv[8000] = true
		}, nil, "1 Claude process(es) have an unreadable environment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withCells(t, func(work, perso string) string {
				if c.cell == "t" {
					return work
				}
				return perso
			})
			s, st := realSubjectDaemon(t)
			f := installRespawnFake(t)
			if c.setup != nil {
				c.setup(t, f, st)
			}
			args := c.args
			if args == nil {
				args = []string{"--subject", "sub_p", "--prompt", respawnPrompt}
			}
			_, _, err := runRespawnCLI(t, s, args...)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "session respawn: ") {
				t.Fatalf("error not prefixed: %v", err)
			}
			if !strings.Contains(err.Error(), " — ") {
				t.Fatalf("refusal names no next step (\" — <what to do>\"): %v", err)
			}
			if strings.Contains(err.Error(), "— spawn only") || strings.Contains(err.Error(), "pigtail session spawn …") {
				t.Fatalf("a respawn refusal names spawn: %v", err)
			}
			if f.consumed != 0 || len(armCalls) != 0 {
				t.Fatalf("a refusal consumed (%d) or armed (%d)", f.consumed, len(armCalls))
			}
			retry := map[string]bool{"surface ref unreadable": true, "unreadable env on a real call": true}
			want := 1
			if retry[c.name] {
				want = exitTempFail
			}
			if got := exitCode(err); got != want {
				t.Fatalf("exit %d, want %d: %v", got, want, err)
			}
		})
	}
}

// a technical refusal says so under --json; a rule refusal does not.
func TestRespawnRefusalJSONRetryable(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	f := installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "r0", SubjectID: "sub_p", Cell: "p", SurfaceID: "S1", SurfaceRef: "surface:7", Result: "armed", WorkerPID: os.Getpid()})
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--json")
	if exitCode(err) != exitTempFail {
		t.Fatalf("already armed: exit %d (%v)", exitCode(err), err)
	}
	var v map[string]any
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil || v["retryable"] != true || v["retry_after_s"] != float64(60) {
		t.Fatalf("already armed json = %q %v", out, jerr)
	}
	_ = f
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnCallerWS, SubjectID: "sub_t", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, _, err = runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--json", "--dry-run")
	v = nil
	if exitCode(err) != 1 || json.Unmarshal([]byte(out), &v) != nil || v["retryable"] != false {
		t.Fatalf("bound elsewhere: exit %d json %q", exitCode(err), out)
	}
}

// a registry lock that cannot be taken is a technical refusal, and
// --json still prints one JSON object (it returned before spawnJSONError).
func TestSessionLockRefusalJSON(t *testing.T) {
	for _, cmd := range []string{"respawn", "spawn"} {
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		installRespawnFake(t)
		// a directory where the lock file goes: OpenFile(O_RDWR) fails (EISDIR)
		if err := os.MkdirAll(spawnreg.DefaultPath()+".lock", 0o700); err != nil {
			t.Fatal(err)
		}
		run, args := runRespawnCLI, []string{"--subject", "sub_p", "--prompt", respawnPrompt, "--json"}
		if cmd == "spawn" {
			run, args = runSpawnCLI, []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--json"}
		}
		out, _, err := run(t, s, args...)
		if exitCode(err) != exitTempFail {
			t.Fatalf("%s: lock refusal exits %d (%v)", cmd, exitCode(err), err)
		}
		var v map[string]any
		if jerr := json.Unmarshal([]byte(out), &v); jerr != nil || v["retryable"] != true || v["error"] != err.Error() {
			t.Fatalf("%s: lock refusal stdout = %q (%v)", cmd, out, jerr)
		}
	}
}

// bindingStore is what the refusal table needs from the real store.
type bindingStore interface {
	CreateCmuxBinding(*pigtail.SubjectCmuxBinding) error
}

// TestRespawnWorkCellInPersoBoundWorkspace: the documented limit — a work
// cell reads a perso-bound workspace as unbound, so its dry-run passes (the
// worker's attach then gets a conflict, recorded as a warning).
func TestRespawnWorkCellInPersoBoundWorkspace(t *testing.T) {
	withCells(t, func(work, _ string) string { return work })
	s, st := realSubjectDaemon(t)
	installRespawnFake(t)
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnCallerWS, SubjectID: "sub_p", Source: "ptnew", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_t", "--prompt", "/example-skill resume sub_t", "--dry-run")
	if err != nil || !strings.Contains(out, "cell      t") {
		t.Fatalf("work cell, perso-bound workspace: %v\n%s", err, out)
	}
}

func TestRespawnAlreadyArmed(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "r0", SubjectID: "sub_p", Cell: "p", SurfaceID: "S1", SurfaceRef: "surface:7", Result: "armed", WorkerPID: os.Getpid()})
	_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt)
	if err == nil || !strings.Contains(err.Error(), "a respawn is already armed for surface surface:7 (run r0)") {
		t.Fatalf("err = %v", err)
	}
	if f.consumed != 0 || len(armCalls) != 0 {
		t.Fatal("a refused second respawn consumed or armed")
	}
	// The same record with a dead worker does not block.
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "r0", SubjectID: "sub_p", Cell: "p", SurfaceID: "S1", SurfaceRef: "surface:7", Result: "armed", WorkerPID: 999999})
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run"); err != nil {
		t.Fatalf("a dead worker's armed record blocked: %v", err)
	}
}

func TestRespawnStatusJSONKeys(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "r1", SubjectID: "sub_p", Cell: "p", WorkspaceID: spawnCallerWS, SurfaceRef: "surface:7", SurfaceID: "S1", Result: "armed", WorkerPID: 999999})
	out, _, err := runRespawnCLI(t, s, "--status", "--subject", "sub_p", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("status json = %q %v", out, err)
	}
	for _, k := range []string{"id", "ts", "subject_id", "workspace_id", "surface_ref", "surface_id", "cell", "permission_mode", "prompt", "result", "worker_pid", "worker_alive", "old_session_id", "pid", "new_session_id", "detail"} {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("status json lacks %q", k)
		}
	}
	if len(rows[0]) != 16 {
		t.Errorf("status json has %d keys, want exactly the 16 pinned: %v", len(rows[0]), rows[0])
	}
	if rows[0]["worker_alive"] != false {
		t.Errorf("worker 999999 reported alive")
	}
}

func TestRespawnStatusScope(t *testing.T) {
	for _, c := range []struct {
		cell, seen, hidden string
	}{{"p", "rp", "rt"}, {"t", "rt", "rp"}} {
		withCells(t, func(work, perso string) string {
			if c.cell == "t" {
				return work
			}
			return perso
		})
		s, _ := realSubjectDaemon(t)
		installRespawnFake(t)
		reg := spawnreg.Open(spawnreg.DefaultPath())
		_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rp", SubjectID: "sub_p", Cell: "p", Result: "respawned"})
		_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rt", SubjectID: "sub_t", Cell: "t", Result: "respawned"})
		_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rnocell", SubjectID: "sub_p", Result: "respawned"})
		_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rx", SubjectID: "sub_p", Cell: "zz", Result: "respawned"})
		out, _, err := runRespawnCLI(t, s, "--status", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0]["id"] != c.seen {
			t.Fatalf("cell %s sees %v, want only %s (never %s, an empty or unknown cell)", c.cell, rows, c.seen, c.hidden)
		}
		// --subject of the other scope answers `[]` on stdout like an unknown subject; stderr counts its runs.
		other := map[string]string{"p": "sub_t", "t": "sub_p"}[c.cell]
		if out, _, err := runRespawnCLI(t, s, "--status", "--subject", other, "--json"); err != nil || strings.TrimSpace(out) != "[]" {
			t.Fatalf("cell %s, --subject %s = %q %v, want []", c.cell, other, out, err)
		}
	}
}

func TestRespawnStatusEmptyJSON(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	out, _, err := runRespawnCLI(t, s, "--status", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty status = %q %v, want []", out, err)
	}
}

// TestRespawnStatusWithoutPrompt: --status is the availability probe — it
// needs no calling Claude, no permission record, no subject.
func TestRespawnStatusWithoutPrompt(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	f.caller = claudesession.Proc{}
	f.stamp = ""
	if _, _, err := runRespawnCLI(t, s, "--status"); err != nil {
		t.Fatalf("--status alone = %v, want exit 0", err)
	}
}

func TestRespawnStatusTextWorkerGone(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rdead", SubjectID: "sub_p", Cell: "p", SurfaceRef: "surface:7", Result: "armed", WorkerPID: 999999})
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rlive", SubjectID: "sub_p", Cell: "p", SurfaceRef: "surface:8", Result: "armed", WorkerPID: os.Getpid()})
	out, _, err := runRespawnCLI(t, s, "--status")
	if err != nil {
		t.Fatal(err)
	}
	var dead, live string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "rdead") {
			dead = l
		}
		if strings.HasPrefix(l, "rlive") {
			live = l
		}
	}
	if !strings.Contains(dead, "armed (worker gone)") {
		t.Errorf("dead worker row = %q", dead)
	}
	if !strings.Contains(live, "armed") || strings.Contains(live, "worker gone") {
		t.Errorf("live worker row = %q", live)
	}
}

func TestRespawnArmedOutput(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installRespawnFake(t)
		f.stamp = "plan"
		args := []string{"--subject", "sub_p", "--prompt", respawnPrompt}
		if asJSON {
			args = append(args, "--json")
		}
		out, _, err := runRespawnCLI(t, s, args...)
		if err != nil {
			t.Fatalf("json=%v: %v", asJSON, err)
		}
		if len(armCalls) != 1 || f.consumed != 1 {
			t.Fatalf("json=%v: armed %d times, consumed %d", asJSON, len(armCalls), f.consumed)
		}
		run := armCalls[0]
		if run.ID == "" || run.SurfaceID != "S1" || run.SurfaceRef != "surface:7" || run.WorkspaceID != spawnCallerWS ||
			run.Mode != "plan" || run.SessionID != "sess-caller" || run.ClaudePID != 4242 || run.Cell != "p" ||
			run.IdleTimeout != respawnIdleDefault || run.ForegroundPID != os.Getpid() || run.LaunchProto != 2 || !strings.HasSuffix(run.RelaunchFile, "k.sid") {
			t.Fatalf("json=%v: run = %+v", asJSON, run)
		}
		if !asJSON {
			if !strings.HasPrefix(out, "respawn armed (run ") || !strings.Contains(out, "surface:7") || !strings.Contains(out, "mode plan") || !strings.Contains(out, respawnPrompt) {
				t.Fatalf("armed line = %q", out)
			}
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("armed json = %q %v", out, err)
		}
		if v["armed"] != true || v["id"] != run.ID || v["worker_pid"] != float64(999) || v["surface_ref"] != "surface:7" ||
			v["permission_mode"] != "plan" || v["prompt"] != respawnPrompt || !strings.HasSuffix(v["log"].(string), run.ID+".log") {
			t.Fatalf("armed json = %v", v)
		}
	}
}

func TestRespawnRealCallNeedsSubjectAndPrompt(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	_, _, err := runRespawnCLI(t, s)
	if err == nil || !strings.Contains(err.Error(), `required flag(s) "subject", "prompt" not set`) {
		t.Fatalf("err = %v", err)
	}
	_, _, err = runRespawnCLI(t, s, "--subject", "sub_p")
	if err == nil || !strings.Contains(err.Error(), `required flag(s) "prompt" not set`) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--", "--dry-run"); err == nil {
		t.Fatal("args after -- were accepted")
	}
}

func TestRespawnJSONDryRun(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	f.addProc(8000, "/x/bin/claude", "")
	f.badEnv[8000] = true
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, k := range []string{"dry_run", "surface_ref", "permission_mode", "launch_proto", "prompt", "warnings"} {
		if _, ok := v[k]; !ok {
			t.Errorf("dry-run json lacks %q", k)
		}
	}
	if v["dry_run"] != true || v["surface_ref"] != "surface:7" || v["launch_proto"] != float64(2) || v["relaunch"] != "launcher" || len(v["warnings"].([]any)) != 2 ||
		!strings.HasPrefix(v["warnings"].([]any)[1].(string), "rotation: no candidate") { // no .claude.json here: the rotation says so
		t.Fatalf("dry-run json = %v", v)
	}
	if f.consumed != 0 || len(armCalls) != 0 {
		t.Fatal("dry-run --json consumed or armed")
	}
}

// TestRespawnArmsUnderLock: the real call reaches the arm seam once, with the
// stamp consumed, while the registry lock is held.
func TestRespawnArmsUnderLock(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	var consumedAtArm int
	var blocked bool
	respawnArm = func(_ *spawnreg.Registry, run respawnRun, _ io.Writer) (int, error) {
		armCalls = append(armCalls, run)
		consumedAtArm = f.consumed
		got := make(chan struct{})
		go func() {
			unlock, err := spawnreg.Open(spawnreg.DefaultPath()).Lock()
			if err == nil {
				unlock()
			}
			close(got)
		}()
		select {
		case <-got:
		case <-time.After(200 * time.Millisecond):
			blocked = true
		}
		return 999, nil
	}
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt); err != nil {
		t.Fatal(err)
	}
	if len(armCalls) != 1 || consumedAtArm != 1 || !blocked {
		t.Fatalf("arm calls %d, consumed at arm %d, lock held %v", len(armCalls), consumedAtArm, blocked)
	}
}

// (defense in depth behind the caller guard): pflag keeps a flag's last
// value, so `--dry-run --dry-run=false` would run a real respawn. A bool flag
// given twice or with a value other than its bare form, and a repeated
// string flag, are refused before anything is touched.
func TestRespawnRefusesAmbiguousFlags(t *testing.T) {
	cases := [][]string{
		{"--dry-run", "--dry-run=false"},
		{"--dry-run=false"},
		{"--dry-run=0"},
		{"--dry-run", "--dry-run"},
		{"--status", "--status=false"},
		{"--status=f"},
		{"--json", "--json=false"},
		{"--cross-scope=false"},
		{"--cross-scope", "--cross-scope"},
		{"--subject", "sub_t"},                      // --subject repeated (sub_p comes first)
		{"--prompt", "/example-skill resume sub_t"}, // --prompt repeated
		{"--idle-timeout", "1m", "--idle-timeout", "2m"},
	}
	for _, extra := range cases {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			s, _ := realSubjectDaemon(t)
			f := installRespawnFake(t)
			args := append([]string{"--subject", "sub_p", "--prompt", respawnPrompt}, extra...)
			_, _, err := runRespawnCLI(t, s, args...)
			if err == nil {
				t.Fatalf("%v accepted", extra)
			}
			if !strings.Contains(err.Error(), "given more than once") && !strings.Contains(err.Error(), "takes no value") {
				t.Fatalf("err = %v, want a refusal naming the repeated or valued flag", err)
			}
			if f.consumed != 0 || len(armCalls) != 0 {
				t.Fatalf("an ambiguous call consumed (%d) or armed (%d)", f.consumed, len(armCalls))
			}
		})
	}
}

// the launcher's `read -r` strips edge whitespace from the prompt, so the
// verify would fail after the old session is closed: refuse it up front.
func TestRespawnRefusesEdgeWhitespacePrompt(t *testing.T) {
	for _, p := range []string{respawnPrompt + " ", " " + respawnPrompt, respawnPrompt + " "} {
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installRespawnFake(t)
		_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", p, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "leading or trailing whitespace") || !strings.Contains(err.Error(), " — ") {
			t.Fatalf("prompt %q: err = %v", p, err)
		}
		if f.consumed != 0 || len(armCalls) != 0 {
			t.Fatal("refusal touched something")
		}
	}
}

// a respawn record names a session that was replaced, not a session that
// holds the subject: the per-subject guard never counts it.
func TestRespawnSubjectGuardIgnoresRespawnRecords(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: spawnreg.KindRespawn, ID: "rold", SubjectID: "sub_p", Cell: "p", WorkspaceID: spawnOtherWS, Result: "respawned"})
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run"); err != nil {
		t.Fatalf("a respawn record counted as a live session of the subject: %v", err)
	}
	// The same workspace as a spawn record does count.
	_ = reg.Append(spawnreg.Entry{SubjectID: "sub_p", WorkspaceID: spawnOtherWS, Result: "launched"})
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run"); err == nil || !strings.Contains(err.Error(), "already has a live session") {
		t.Fatalf("a spawn record of the subject = %v", err)
	}
}

// the help names the sidebar pill as the channel — no
// cmux notification is posted any more (#166).
func TestRespawnHelpNamesTheSidebarNotANotification(t *testing.T) {
	long := sessionRespawnCmd().Long
	if strings.Contains(strings.ToLower(long), "notification") {
		t.Errorf("help still names a notification:\n%s", long)
	}
	for _, want := range []string{"Respawn · <reason>", "Respawn · abandoned", "Blocked · respawn failed", "next prompt", "busy after an idle read"} {
		if !strings.Contains(long, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if strings.Contains(long, "Blocked · respawn abandoned") {
		t.Error("help still names the red abandoned pill")
	}
}

// A cell still written work and a globex cell are one scope: each sees
// the other's respawn entries, never the perso one.
// --status keeps scope isolation and says how many runs it hid (stderr
// only: stdout stays a pure listing / JSON).
func TestRespawnStatusSaysHiddenRuns(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rp", SubjectID: "sub_p", Cell: "p", Result: "respawned"})
	_, errOut, err := runRespawnCLI(t, s, "--status", "--json")
	if err != nil || errOut != "" {
		t.Fatalf("nothing hidden: stderr %q err %v", errOut, err)
	}
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rt", SubjectID: "sub_t", Cell: "t", Result: "respawned"})
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rt2", SubjectID: "sub_t", Cell: "t", Result: "failed"})
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rnocell", SubjectID: "sub_p", Result: "respawned"})
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rx", SubjectID: "sub_p", Cell: "zz", Result: "respawned"})
	out, errOut, err := runRespawnCLI(t, s, "--status", "--json")
	if err != nil || errOut != "2 respawn runs of another scope hidden\n" {
		t.Fatalf("stderr = %q err %v", errOut, err)
	}
	var rows []map[string]any
	if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil || len(rows) != 1 {
		t.Fatalf("stdout no longer pure JSON: %q %v", out, jerr)
	}
	_, errOut, _ = runRespawnCLI(t, s, "--status", "--subject", "sub_t")
	if errOut != "2 respawn runs of another scope hidden\n" {
		t.Fatalf("--subject of the other scope: stderr %q", errOut)
	}
	_, errOut, _ = runRespawnCLI(t, s, "--status", "--subject", "sub_p")
	if errOut != "" {
		t.Fatalf("own subject: stderr %q", errOut)
	}
}

func TestRespawnStatusWorkAndGlobexCells(t *testing.T) {
	scopes := map[string]string{"t": "globex", "s": "globex", "p": "perso"}
	for _, cell := range []string{"t", "s"} {
		withCellScopes(t, scopes, cell)
		s, _ := realSubjectDaemon(t)
		installRespawnFake(t)
		reg := spawnreg.Open(spawnreg.DefaultPath())
		_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rt", SubjectID: "sub_t", Cell: "t", Result: "respawned"})
		_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rs", SubjectID: "sub_t", Cell: "s", Result: "respawned"})
		_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rp", SubjectID: "sub_p", Cell: "p", Result: "respawned"})
		out, errOut, err := runRespawnCLI(t, s, "--status", "--json")
		if err != nil {
			t.Fatal(err)
		}
		if errOut != "1 respawn runs of another scope hidden\n" {
			t.Fatalf("cell %s: stderr = %q (work and globex are one scope; only rp is hidden)", cell, errOut)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range rows {
			ids = append(ids, r["id"].(string))
		}
		sort.Strings(ids)
		if strings.Join(ids, ",") != "rs,rt" {
			t.Fatalf("cell %s sees %v, want rs,rt", cell, ids)
		}
	}
}

// Account rotation. The caller runs on ip;
// m's 7-day window renews first in perso; work account t renews sooner still
// (never picked for a perso caller).
func rotationRespawnCells(t *testing.T, readings bool) map[string]string {
	t.Helper()
	testutil.SerialOnly(t) // swaps respawnPreTrust
	dirs := withCellRows(t, map[string]cellRow{
		"ip": {Scope: "perso", Account: "i", Rank: 0},
		"mp": {Scope: "perso", Account: "m", Rank: 1},
		"tt": {Scope: "globex", Account: "t", Rank: 2},
	}, "ip")
	if readings {
		writeUsage(t, dirs["ip"], "22222222-2222-4222-8222-222222222222", 40, 5*24*time.Hour)
		writeUsage(t, dirs["mp"], rotUUIDM, 40, 24*time.Hour)
		writeUsage(t, dirs["tt"], "44444444-4444-4444-8444-444444444444", 10, time.Hour)
	}
	saved := respawnPreTrust
	t.Cleanup(func() { respawnPreTrust = saved })
	respawnPreTrust = func(_ context.Context, _ *pigtail.Client, dir, cwd string) claudeconfig.Result {
		preTrustDirs = append(preTrustDirs, dir)
		return claudeconfig.Result{Outcome: claudeconfig.Written, Key: cwd}
	}
	preTrustDirs = nil
	return dirs
}

var preTrustDirs []string

func setProto(f *spawnFake, proto string) {
	env := f.envs[f.caller.PID]
	if proto == "" {
		env = withoutVar(withoutVar(env, "CLAUDE_LAUNCH_PROTO"), "CLAUDE_RELAUNCH_FILE")
	} else {
		env = withVar(env, "CLAUDE_LAUNCH_PROTO", proto)
	}
	f.envs[f.caller.PID] = env
}

func TestRespawnRotationArmsFreshCell(t *testing.T) {
	dirs := rotationRespawnCells(t, true)
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	_, stderr, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if len(armCalls) != 1 || armCalls[0].Cell != "ip" || armCalls[0].ConfigDir != dirs["ip"] ||
		armCalls[0].FreshCell != "mp" || armCalls[0].FreshConfigDir != dirs["mp"] {
		t.Fatalf("armed = %+v", armCalls)
	}
	if len(preTrustDirs) != 1 || preTrustDirs[0] != dirs["mp"] {
		t.Fatalf("pre-trust = %v, want mp's dir", preTrustDirs)
	}
	if strings.Contains(stderr, "rotation") {
		t.Fatalf("stderr %q, want no rotation warning", stderr)
	}
}

func TestRespawnRotationKeepsScope(t *testing.T) {
	rotationRespawnCells(t, true)
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_t", "--cross-scope", "--prompt", "/example-skill resume sub_t", "--dry-run", "--json")
	var v map[string]any
	if err != nil || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("dry-run: %v %s", err, out)
	}
	if v["fresh_cell"] != "mp" {
		t.Fatalf("fresh_cell = %v, want mp (the caller's scope, never tt)", v["fresh_cell"])
	}
}

func TestRespawnRotationUnavailableKeepsCaller(t *testing.T) {
	rotationRespawnCells(t, false)
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	_, stderr, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if len(armCalls) != 1 || armCalls[0].FreshCell != "" || len(preTrustDirs) != 0 {
		t.Fatalf("armed = %+v pre-trust %v, want the caller's cell", armCalls, preTrustDirs)
	}
	if !strings.Contains(stderr, "warning: rotation: no candidate") || !strings.Contains(stderr, "fresh session stays on ip") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRespawnRotationOldLauncherKeepsCaller(t *testing.T) {
	cases := []struct {
		proto, fresh string
		warn         bool
	}{{"2", "", true}, {"3", "mp", false}, {"", "mp", false}}
	for _, c := range cases {
		t.Run("proto="+c.proto, func(t *testing.T) {
			rotationRespawnCells(t, true)
			s, _ := realSubjectDaemon(t)
			f := installRespawnFake(t)
			setProto(f, c.proto)
			out, stderr, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--json")
			if err != nil {
				t.Fatal(err)
			}
			if len(armCalls) != 1 || armCalls[0].FreshCell != c.fresh {
				t.Fatalf("armed = %+v, want fresh_cell %q", armCalls, c.fresh)
			}
			w := "warning: rotation: this tab's launcher speaks proto 2 (a fresh session on another cell needs ≥ 3: re-source it) — fresh session stays on ip"
			if got := strings.Contains(stderr, w); got != c.warn {
				t.Fatalf("stderr %q: warning shown %v, want %v", stderr, got, c.warn)
			}
			var v map[string]any
			_ = json.Unmarshal([]byte(out), &v)
			ws, _ := v["warnings"].([]any)
			if (len(ws) == 1) != c.warn {
				t.Fatalf("json warnings = %v", v["warnings"])
			}
		})
	}
}

func TestRotationOffRespawn(t *testing.T) {
	rotationRespawnCells(t, true)
	t.Setenv("PIGTAIL_ROTATION", "off")
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	_, stderr, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if len(armCalls) != 1 || armCalls[0].FreshCell != "" || armCalls[0].FreshConfigDir != "" || len(preTrustDirs) != 0 {
		t.Fatalf("armed = %+v, want the base run", armCalls)
	}
	if strings.Count(stderr, "rotation: off") != 1 {
		t.Fatalf("stderr = %q, want one rotation: off line", stderr)
	}
}

func TestRespawnDryRunFreshCell(t *testing.T) {
	dirs := rotationRespawnCells(t, true)
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run")
	if err != nil || !strings.Contains(out, "cell      ip ("+dirs["ip"]+")") || !strings.Contains(out, "fresh     cell mp ("+dirs["mp"]+") — rotation: account m") {
		t.Fatalf("dry-run: %v\n%s", err, out)
	}
	if len(preTrustDirs) != 0 {
		t.Fatalf("dry-run pre-trusted %v", preTrustDirs)
	}
	out, _, err = runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run", "--json")
	var v map[string]any
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v["cell"] != "ip" || v["config_dir"] != dirs["ip"] ||
		v["fresh_cell"] != "mp" || v["fresh_config_dir"] != dirs["mp"] {
		t.Fatalf("dry-run json: %v %v", err, v)
	}
	setProto(f, "")
	out, _, err = runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run")
	if err != nil || !strings.Contains(out, "relaunch typed into this tab's shell: c mp, or claude on "+dirs["mp"]+" without c\n") {
		t.Fatalf("shell dry-run: %v\n%s", err, out)
	}
}

func TestRespawnArmedNamesFreshCell(t *testing.T) {
	arm := func(args ...string) (string, error) {
		rotationRespawnCells(t, true)
		s, _ := realSubjectDaemon(t)
		f := installRespawnFake(t)
		setProto(f, "3")
		out, _, err := runRespawnCLI(t, s, append([]string{"--subject", "sub_p", "--prompt", respawnPrompt}, args...)...)
		return out, err
	}
	out, err := arm()
	if err != nil || !strings.Contains(out, "— cell mp, mode") {
		t.Fatalf("armed: %v %q", err, out)
	}
	out, err = arm("--json")
	var v map[string]any
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v["fresh_cell"] != "mp" || v["cell"] != "ip" {
		t.Fatalf("armed json: %v %v", err, v)
	}
}

const modelProto3Warning = "warning: model: this tab's launcher speaks proto 3 (a fresh session on a chosen model needs ≥ 4: re-source it) — fresh session on the cell's default model"

func TestRespawnModelUnknownRefused(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--model", "sonet")
	want := `--model "sonet" is not a model alias — pass sonnet, opus, haiku or fable (or leave it out for the cell's default); nothing done`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v", err)
	}
	if len(armCalls) != 0 {
		t.Fatal("a refused model armed")
	}
}

func TestRespawnModelGivenTwice(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--model", "sonnet", "--model", "opus")
	if err == nil || !strings.Contains(err.Error(), "--model given more than once") {
		t.Fatalf("err = %v", err)
	}
	if len(armCalls) != 0 {
		t.Fatal("armed")
	}
}

func TestRespawnModelProto(t *testing.T) {
	for _, tc := range []struct {
		proto, wantModel string
		warn             bool
	}{{"4", "sonnet", false}, {"3", "", true}, {"", "sonnet", false}} {
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installRespawnFake(t)
		setProto(f, tc.proto)
		out, errOut, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--model", "sonnet", "--json")
		if err != nil {
			t.Fatalf("proto %q: %v", tc.proto, err)
		}
		if len(armCalls) != 1 || armCalls[0].Model != tc.wantModel {
			t.Fatalf("proto %q: armCalls = %+v", tc.proto, armCalls)
		}
		if got := strings.Contains(errOut, modelProto3Warning); got != tc.warn {
			t.Errorf("proto %q: warning on stderr = %v, want %v (%q)", tc.proto, got, tc.warn, errOut)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("proto %q: %q %v", tc.proto, out, err)
		}
		if v["model"] != tc.wantModel {
			t.Errorf("proto %q: json model = %v, want %q", tc.proto, v["model"], tc.wantModel)
		}
		ws, _ := v["warnings"].([]any)
		found := false
		for _, w := range ws {
			if w == strings.TrimPrefix(modelProto3Warning, "warning: ") {
				found = true
			}
		}
		if found != tc.warn {
			t.Errorf("proto %q: json warnings = %v, want warning %v", tc.proto, ws, tc.warn)
		}
	}
	// No --model on proto 3: no model line at all.
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	_, errOut, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt)
	if err != nil || strings.Contains(errOut, "warning: model:") {
		t.Fatalf("err = %v, stderr = %q", err, errOut)
	}
}

func TestRespawnModelAbsent(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "4")
	out, errOut, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(armCalls) != 1 || armCalls[0].Model != "" || strings.Contains(errOut, "warning: model:") {
		t.Fatalf("armCalls = %+v, stderr = %q", armCalls, errOut)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if m, ok := v["model"]; !ok || m != "" {
		t.Fatalf("json model = %v (present %v)", m, ok)
	}
}

func TestRespawnDryRunNamesModel(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "4")
	out, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run", "--model", "sonnet")
	if err != nil || !strings.Contains(out, "  model     sonnet — passed as --model\n") {
		t.Fatalf("with model: %v\n%s", err, out)
	}
	out, _, err = runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run")
	if err != nil || !strings.Contains(out, "  model     cell default (no --model)\n") {
		t.Fatalf("without model: %v\n%s", err, out)
	}
	out, _, err = runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run", "--json", "--model", "sonnet")
	var v map[string]any
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v["model"] != "sonnet" {
		t.Fatalf("json: %v %q", err, out)
	}
}

func TestRespawnArmedOutputModel(t *testing.T) {
	for _, tc := range []struct{ proto, model, want string }{
		{"4", "", "model cell default"}, {"4", "sonnet", "model sonnet"}, {"3", "sonnet", "model cell default"},
	} {
		withCells(t, func(_, perso string) string { return perso })
		s, _ := realSubjectDaemon(t)
		f := installRespawnFake(t)
		setProto(f, tc.proto)
		args := []string{"--subject", "sub_p", "--prompt", respawnPrompt}
		if tc.model != "" {
			args = append(args, "--model", tc.model)
		}
		out, _, err := runRespawnCLI(t, s, args...)
		if err != nil || !strings.Contains(out, ", "+tc.want+", prompt: ") {
			t.Fatalf("%+v: %v\n%s", tc, err, out)
		}
	}
}

func TestRespawnStatusTextModel(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rmod", SubjectID: "sub_p", Cell: "p", SurfaceRef: "surface:7", Result: "respawned", Model: "sonnet"})
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "rplain", SubjectID: "sub_p", Cell: "p", SurfaceRef: "surface:8", Result: "respawned"})
	out, _, err := runRespawnCLI(t, s, "--status")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "rmod"):
			if !strings.Contains(l, "p (sonnet)") {
				t.Errorf("model row = %q", l)
			}
		case strings.HasPrefix(l, "rplain"):
			if strings.Contains(l, "(") {
				t.Errorf("plain row = %q", l)
			}
		}
	}
}

func TestRespawnStatusJSONModel(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installRespawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{Kind: "respawn", ID: "r1", SubjectID: "sub_p", Cell: "p", WorkspaceID: spawnCallerWS, SurfaceRef: "surface:7", SurfaceID: "S1", Result: "respawned", Model: "sonnet"})
	out, _, err := runRespawnCLI(t, s, "--status", "--subject", "sub_p", "--json")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
		t.Fatalf("status json = %q %v", out, err)
	}
	if rows[0]["model"] != "sonnet" || len(rows[0]) != 17 {
		t.Fatalf("row = %v", rows[0])
	}
}

// Launch margin guard on respawn: one GuardScope on
// the caller's scope; refused (exit 3) before anything is armed.
const rotUUIDI = "22222222-2222-4222-8222-222222222222"

func saturateRespawn(t *testing.T, dirs map[string]string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		switch id {
		case "ip":
			writeUsage(t, dirs["ip"], rotUUIDI, 97, 5*24*time.Hour)
		case "mp":
			writeUsage(t, dirs["mp"], rotUUIDM, 96, 24*time.Hour)
		}
	}
}

func TestRespawnRefusedWhenAllOver(t *testing.T) {
	dirs := rotationRespawnCells(t, true)
	saturateRespawn(t, dirs, "ip", "mp")
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt)
	if exitCode(err) != 3 || len(armCalls) != 0 || len(preTrustDirs) != 0 || f.consumed != 0 {
		t.Fatalf("exit %d (%v) armed %v pre-trust %v consumed %d, want 3 and nothing touched", exitCode(err), err, armCalls, preTrustDirs, f.consumed)
	}
	if !strings.Contains(err.Error(), "respawn refused: every account of scope perso is over the margin") || !strings.Contains(err.Error(), "first release: ") || !strings.Contains(err.Error(), "--over-margin") {
		t.Fatalf("refusal = %q", err)
	}
}

func TestRespawnOverMarginFlagForces(t *testing.T) {
	dirs := rotationRespawnCells(t, true)
	saturateRespawn(t, dirs, "ip", "mp")
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	_, stderr, err := runRespawnCLI(t, s, "--subject", "sub_p", "--over-margin", "--prompt", respawnPrompt)
	if err != nil || len(armCalls) != 1 {
		t.Fatalf("--over-margin = %v armed %+v", err, armCalls)
	}
	if !strings.Contains(stderr, "warning: launch margin: --over-margin") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRespawnCallsSingleGuard(t *testing.T) {
	rotationRespawnCells(t, true)
	guards, _ := countGuard(t)
	saved := pickCell
	t.Cleanup(func() { pickCell = saved })
	pickCell = func(*claudeprofile.Table, string, map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
		t.Fatal("respawn must not pick outside the guard")
		return claudeprofile.Cell{}, claudeusage.Choice{}, nil
	}
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt); err != nil {
		t.Fatal(err)
	}
	if *guards != 1 {
		t.Fatalf("guards = %d, want 1", *guards)
	}
}

func TestRespawnSwitchesToFreeAccount(t *testing.T) {
	dirs := rotationRespawnCells(t, true)
	saturateRespawn(t, dirs, "ip")
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt); err != nil {
		t.Fatal(err)
	}
	if len(armCalls) != 1 || armCalls[0].FreshCell != "mp" {
		t.Fatalf("armed = %+v, want a fresh session on mp", armCalls)
	}
}

func TestRespawnOldLauncherSaturatedCallerRefused(t *testing.T) {
	dirs := rotationRespawnCells(t, true)
	saturateRespawn(t, dirs, "ip")
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "2")
	_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt)
	if exitCode(err) != 3 || len(armCalls) != 0 || f.consumed != 0 {
		t.Fatalf("exit %d (%v) armed %v, want 3 and nothing armed", exitCode(err), err, armCalls)
	}
	if !strings.Contains(err.Error(), "launcher too old to switch account") {
		t.Fatalf("refusal = %q", err)
	}
}

// --over-margin with an old launcher keeps the caller's cell. New
// behaviour: the flag does not exist on base (unknown flag), so this is red
// there and green on the branch.
func TestRespawnOldLauncherSaturatedCallerOverMargin(t *testing.T) {
	dirs := rotationRespawnCells(t, true)
	saturateRespawn(t, dirs, "ip")
	s, _ := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "2")
	_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--over-margin", "--prompt", respawnPrompt)
	if err != nil || len(armCalls) != 1 || armCalls[0].FreshCell != "" {
		t.Fatalf("--over-margin = %v armed %+v, want the caller's cell", err, armCalls)
	}
}

func TestRespawnRefusalWritesRefusedEntry(t *testing.T) {
	dirs := rotationRespawnCells(t, true)
	saturateRespawn(t, dirs, "ip", "mp")
	s, st := realSubjectDaemon(t)
	f := installRespawnFake(t)
	setProto(f, "3")
	if _, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt); exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	m := marginEntries(t)
	if len(m) != 1 || m[0].Result != "refused" || m[0].Cell != "mp" || m[0].SubjectID != "sub_p" || !strings.HasPrefix(m[0].Margin, "refused perso frees ") {
		t.Fatalf("margin entries = %+v", m)
	}
	if rows := marginJournalRows(t, st); len(rows) != 1 {
		t.Fatalf("journal = %+v", rows)
	}
}

func TestRespawnOldLauncherRefusalWritesRefusedEntry(t *testing.T) {
	for _, c := range []struct {
		force   bool
		margin  string
		summary string
	}{
		{false, "refused ip launcher-proto", "launch margin: respawn refused on cell ip — launcher too old to switch account"},
		{true, "forced ip", "launch margin: --over-margin on cell ip"},
	} {
		t.Run(c.margin, func(t *testing.T) {
			dirs := rotationRespawnCells(t, true)
			saturateRespawn(t, dirs, "ip")
			s, st := realSubjectDaemon(t)
			f := installRespawnFake(t)
			setProto(f, "2")
			args := []string{"--subject", "sub_p", "--prompt", respawnPrompt}
			if c.force {
				args = append(args, "--over-margin")
			}
			_, stderr, err := runRespawnCLI(t, s, args...)
			m := marginEntries(t)
			if len(m) != 1 || m[0].Margin != c.margin {
				t.Fatalf("margin entries = %+v, want %q", m, c.margin)
			}
			rows := marginJournalRows(t, st)
			if len(rows) != 1 || rows[0].Summary != c.summary {
				t.Fatalf("journal = %+v, want summary %q", rows, c.summary)
			}
			// only the caller's cell id is named. The table also holds mp
			// (same scope, account m) and tt (scope globex, account t).
			texts := []string{m[0].Margin, rows[0].Summary}
			if err != nil {
				texts = append(texts, err.Error())
			}
			for _, l := range strings.Split(stderr, "\n") {
				if strings.Contains(l, "launch margin") || strings.Contains(l, "respawn refused") {
					texts = append(texts, l)
				}
			}
			for _, txt := range texts {
				for _, other := range []string{"mp", "tt", "globex"} {
					if strings.Contains(txt, other) {
						t.Fatalf("text %q names %q, a cell, account or scope other than the caller's", txt, other)
					}
				}
			}
		})
	}
}

// A dry run only plans: no margin entry, no journal row, nothing armed.
func TestRespawnDryRunWritesNoMarginEntry(t *testing.T) {
	for _, c := range []struct {
		name  string
		proto string
		args  []string
		sat   []string
	}{
		{"refused-whole-scope", "3", nil, []string{"ip", "mp"}},
		{"forced", "3", []string{"--over-margin"}, []string{"ip", "mp"}},
		{"switch", "3", nil, []string{"ip"}},
		{"old-launcher-refused", "2", nil, []string{"ip"}},
		{"old-launcher-forced", "2", []string{"--over-margin"}, []string{"ip"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dirs := rotationRespawnCells(t, true)
			saturateRespawn(t, dirs, c.sat...)
			s, st := realSubjectDaemon(t)
			f := installRespawnFake(t)
			setProto(f, c.proto)
			args := append([]string{"--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run"}, c.args...)
			_, _, _ = runRespawnCLI(t, s, args...)
			if m, rows := marginEntries(t), marginJournalRows(t, st); len(m) != 0 || len(rows) != 0 || len(armCalls) != 0 {
				t.Fatalf("dry run traced: entries %+v journal %+v armed %v", m, rows, armCalls)
			}
		})
	}
}
