package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/relaunchfile"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// TestWorkerSurvivesParentDeath: a "parent" process (this test binary in
// helper mode, standing for the Claude Bash tool) starts the worker through the
// real startDetachedWorker, then the test SIGKILLs the parent's whole process
// group — what Claude Code's exit does to its tool processes. The worker (a
// helper that waits 1 s then writes a marker) must still finish, and must sit
// in another session than the parent.
func TestWorkerSurvivesParentDeath(t *testing.T) {
	t.Parallel()
	if os.Getenv("PIGTAIL_HELPER") != "" {
		t.Skip()
	}
	dir := t.TempDir()
	parent := exec.Command(os.Args[0], "-test.run=^TestHelperParent$")
	parent.Env = append(os.Environ(), "PIGTAIL_HELPER=parent", "HELPER_DIR="+dir)
	parent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "worker.pid")
	waitFor(t, 5*time.Second, func() bool { return fileExists(pidFile) })
	wpid := atoiFile(t, pidFile)
	// Read both sessions while both processes live (the worker sleeps 1 s).
	psid, err := unix.Getsid(parent.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	wsid, err := unix.Getsid(wpid)
	if err != nil {
		t.Fatal(err)
	}
	if wsid != wpid || wsid == psid {
		t.Fatalf("worker session %d (pid %d), parent session %d: want a session of its own", wsid, wpid, psid)
	}
	if pg, _ := syscall.Getpgid(wpid); pg == parent.Process.Pid {
		t.Fatalf("worker in the parent's process group %d", pg)
	}
	_ = syscall.Kill(-parent.Process.Pid, syscall.SIGKILL)
	_ = parent.Wait()
	waitFor(t, 5*time.Second, func() bool { return fileExists(filepath.Join(dir, "worker.done")) })
}

func TestHelperParent(t *testing.T) {
	t.Parallel()
	switch os.Getenv("PIGTAIL_HELPER") {
	case "parent":
		dir := os.Getenv("HELPER_DIR")
		pid, err := startDetachedWorkerCmd(os.Args[0], []string{"-test.run=^TestHelperParent$"},
			[]string{"PIGTAIL_HELPER=worker", "HELPER_DIR=" + dir}, filepath.Join(dir, "w.log"))
		if err != nil {
			os.Exit(3)
		}
		// atomic: the test never reads a half-written pid
		_ = relaunchfile.WriteAtomic(filepath.Join(dir, "worker.pid"), []byte(strconv.Itoa(pid)))
		time.Sleep(time.Minute) // killed by the test
	case "worker":
		time.Sleep(time.Second)
		_ = os.WriteFile(filepath.Join(os.Getenv("HELPER_DIR"), "worker.done"), nil, 0o600)
		os.Exit(0)
	}
}

// TestHelperArm is the worker armRespawn starts in the arm tests: "arm" reads
// the run file back with readRespawnRun, echoes it, then appends the armed
// record; "mute" never arms.
func TestHelperArm(t *testing.T) {
	t.Parallel()
	mode := os.Getenv("PIGTAIL_HELPER")
	if mode != "arm" && mode != "mute" {
		return
	}
	runPath := flag.Arg(0)
	_ = relaunchfile.WriteAtomic(runPath+".pid", []byte(strconv.Itoa(os.Getpid())))
	if mode == "mute" {
		time.Sleep(time.Minute) // killed by armRespawn
		os.Exit(0)
	}
	run, err := readRespawnRun(runPath)
	if err != nil {
		os.Exit(4)
	}
	b, _ := json.Marshal(run)
	_ = relaunchfile.WriteAtomic(runPath+".echo", b)
	_ = spawnreg.Open(spawnreg.DefaultPath()).Append(spawnreg.Entry{Kind: spawnreg.KindRespawn, ID: run.ID, SubjectID: run.SubjectID,
		Cell: run.Cell, SurfaceID: run.SurfaceID, SurfaceRef: run.SurfaceRef, Result: respawnArmed, WorkerPID: os.Getpid()})
	os.Exit(0)
}

func helperWorker(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("PIGTAIL_HELPER", mode)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	saved := respawnWorkerArgv
	t.Cleanup(func() { respawnWorkerArgv = saved })
	respawnWorkerArgv = func(_, runPath string) []string { return []string{"-test.run=^TestHelperArm$", runPath} }
}

func sampleRun() respawnRun {
	return respawnRun{ID: newRespawnID(time.Now()), SubjectID: "sub_p", SubjectTitle: "perso one", Cell: "p", ConfigDir: "/cfg/p",
		Cwd: "/w/é", ClaudePID: 4242, SessionID: "sess-1", SurfaceID: "S1", SurfaceRef: "surface:7", WorkspaceID: spawnCallerWS,
		RelaunchFile: "/state/k.sid", Relaunch: relaunchLauncher, Mode: "plan", LaunchMode: "auto", LaunchOrigin: "cell default", CurrentMode: "plan",
		Prompt: "/example-skill resume sub_é", LaunchProto: 2, Bound: true, CrossScope: true, IdleTimeout: 17 * time.Minute, ForegroundPID: 77}
}

func TestArmSucceeds(t *testing.T) {
	helperWorker(t, "arm")
	run := sampleRun()
	reg := spawnreg.Open(spawnreg.DefaultPath())
	pid, err := armRespawn(reg, run, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	runPath := respawnRunPath(run.ID)
	fi, err := os.Stat(runPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("run file %v mode %v, want 0600", err, fi)
	}
	var echo respawnRun
	b, err := os.ReadFile(runPath + ".echo")
	if err != nil || json.Unmarshal(b, &echo) != nil {
		t.Fatalf("echo: %v %s", err, b)
	}
	if !reflect.DeepEqual(echo, run) {
		t.Fatalf("run file round trip:\n got %+v\nwant %+v", echo, run)
	}
	es, _ := reg.Entries()
	if len(es) != 1 || es[0].Result != respawnArmed || es[0].ID != run.ID || es[0].WorkerPID != pid {
		t.Fatalf("registry = %+v, worker pid %d", es, pid)
	}
}

func TestArmTimesOutWithoutHandshake(t *testing.T) {
	helperWorker(t, "mute")
	saved := respawnArmTimeout
	t.Cleanup(func() { respawnArmTimeout = saved })
	respawnArmTimeout = 1500 * time.Millisecond
	run := sampleRun()
	_, err := armRespawn(spawnreg.Open(spawnreg.DefaultPath()), run, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "nothing armed") || !strings.Contains(err.Error(), respawnLogPath(run.ID)) {
		t.Fatalf("err = %v", err)
	}
	pidFile := respawnRunPath(run.ID) + ".pid"
	waitFor(t, 2*time.Second, func() bool { return fileExists(pidFile) })
	if wpid := atoiFile(t, pidFile); claudesession.PidAlive(wpid) {
		t.Fatalf("the mute worker %d still runs", wpid)
	}
}
