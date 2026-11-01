package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/relaunchfile"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// spawnWatchRun is a pending launch handed to the detached watcher: the tab
// created, what the launched entry will carry, and when to give up.
type spawnWatchRun struct {
	ID           string    `json:"id"`
	SubjectID    string    `json:"subject_id,omitempty"`
	WorkspaceID  string    `json:"workspace_id"`
	WorkspaceRef string    `json:"workspace_ref,omitempty"`
	Cell         string    `json:"cell,omitempty"`
	ConfigDir    string    `json:"config_dir"`
	Cwd          string    `json:"cwd"`
	RelayFrom    string    `json:"relay_from,omitempty"`
	Prompt       string    `json:"prompt,omitempty"`
	Mode         string    `json:"permission_mode,omitempty"`
	Before       []int     `json:"before,omitempty"`   // Claude PIDs listed before the create
	Deadline     time.Time `json:"deadline"`           // TS of the launch-ref + spawnConfirmTimeout
	LeaseID      string    `json:"lease_id,omitempty"` // the session budget lease to report launched at the outcome
}

var (
	// spawnConfirmTimeout bounds the confirmation of a launch, from its
	// launch-ref: the watcher's deadline and the --relay wait.
	spawnConfirmTimeout = spawnreg.ConfirmTimeout
	// spawnBaseBudget is runSpawn's ctx budget without --new-worktree or --relay.
	spawnBaseBudget = 2 * time.Minute
	// spawnWatchArgv is the watcher's argv after the executable; tests point it at a helper.
	spawnWatchArgv = func(_, runPath string) []string { return []string{"session", "spawn", "--watch", runPath} }
	// spawnArmWatch hands a pending launch to the detached watcher.
	spawnArmWatch func(reg *spawnreg.Registry, run spawnWatchRun, stderr io.Writer) (int, error)
	// runSpawnWatcher is what `session spawn --watch <run>` runs.
	runSpawnWatcher = realSpawnWatcher
	// spawnArmTimeout bounds the handshake (the armed record).
	spawnArmTimeout = 5 * time.Second
)

func init() { spawnArmWatch = armSpawnWatch }

// spawnWatchDeadline is when a launch stops being awaited: its launch-ref's
// TS + spawnConfirmTimeout — never the reserve's TS, never now.
func spawnWatchDeadline(refTS time.Time) time.Time { return refTS.Add(spawnConfirmTimeout) }

func spawnWatchDir() string {
	return filepath.Join(filepath.Dir(spawnreg.DefaultPath()), "spawn-watch")
}
func spawnWatchRunPath(id string) string { return filepath.Join(spawnWatchDir(), id+".json") }
func spawnWatchLogPath(id string) string { return filepath.Join(spawnWatchDir(), id+".log") }

func writeSpawnWatchRun(path string, run spawnWatchRun) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return relaunchfile.WriteAtomic(path, append(b, '\n'))
}

func readSpawnWatchRun(path string) (spawnWatchRun, error) {
	var run spawnWatchRun
	b, err := os.ReadFile(path)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal(b, &run); err != nil {
		return run, fmt.Errorf("run file %s: %w", path, err)
	}
	if run.ID == "" || run.WorkspaceID == "" || run.Deadline.IsZero() {
		return run, fmt.Errorf("run file %s is incomplete (id, workspace, deadline)", path)
	}
	return run, nil
}

// armSpawnWatch writes the run file (0600), starts the watcher detached, and
// waits up to spawnArmTimeout for its armed record (the respawn pattern,
// armRespawn). A failure leaves the launch pending: it expires on its own.
func armSpawnWatch(reg *spawnreg.Registry, run spawnWatchRun, _ io.Writer) (int, error) {
	runPath, logPath := spawnWatchRunPath(run.ID), spawnWatchLogPath(run.ID)
	if err := writeSpawnWatchRun(runPath, run); err != nil {
		return 0, fmt.Errorf("write the watch file: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate this pigtail binary: %w", err)
	}
	pid, d, err := startDetachedProcess(self, spawnWatchArgv(self, runPath), nil, logPath)
	if err != nil {
		return 0, fmt.Errorf("start the watcher: %w", err)
	}
	deadline := time.Now().Add(spawnArmTimeout)
	for {
		if es, err := reg.Entries(); err == nil {
			for _, e := range es {
				if e.Kind == spawnreg.KindLaunchArmed && e.ID == run.ID {
					return pid, nil
				}
			}
		}
		select {
		case <-d.done:
			return 0, fmt.Errorf("the watcher exited before arming (log %s)", logPath)
		default:
		}
		if time.Now().After(deadline) {
			d.kill()
			return 0, fmt.Errorf("the watcher did not arm within %s (log %s)", spawnArmTimeout, logPath)
		}
		time.Sleep(respawnArmPoll)
	}
}

// realSpawnWatcher is the detached `session spawn --watch <run>`.
func realSpawnWatcher(c *pigtail.Client, runPath string) error {
	pinWorkerLocale()
	signal.Ignore(syscall.SIGHUP)
	run, err := readSpawnWatchRun(runPath)
	if err != nil {
		return err
	}
	return watchSpawn(c, spawnreg.Open(spawnreg.DefaultPath()), run)
}

// watchSpawn arms (the handshake), then waits for the launch's Claude until
// run.Deadline: seen right → the launched entry, same ID; else a failed
// launch-result. Both repeat the launch's identity.
func watchSpawn(c *pigtail.Client, reg *spawnreg.Registry, run spawnWatchRun) error {
	ident := spawnreg.Entry{ID: run.ID, SubjectID: run.SubjectID, WorkspaceID: run.WorkspaceID, WorkspaceRef: run.WorkspaceRef}
	armed := ident
	armed.TS, armed.Kind, armed.Result, armed.WorkerPID = time.Now().UTC(), spawnreg.KindLaunchArmed, spawnreg.ResultArmed, os.Getpid()
	if err := reg.Append(armed); err != nil {
		return err
	}
	before := map[int]bool{}
	for _, p := range run.Before {
		before[p] = true
	}
	var pid int
	var verr error
	if wait := time.Until(run.Deadline); wait > 0 {
		pid, verr = observeSpawn(run.WorkspaceID, run.ConfigDir, run.Cwd, before, wait)
	} else {
		verr = fmt.Errorf("%w within %s of the tab's birth: no Claude process appeared in it", errSpawnNotSeen, spawnConfirmTimeout)
	}
	e := ident
	e.TS = time.Now().UTC()
	if verr != nil {
		e.Kind, e.Result, e.Detail = spawnreg.KindLaunchResult, spawnreg.ResultFailed, verr.Error()
		err := reg.Append(e)
		reportWatchedLease(c, run)
		return err
	}
	e.Result, e.PID, e.Cell, e.Cwd = spawnreg.ResultLaunched, pid, run.Cell, run.Cwd
	e.Prompt, e.Mode, e.RelayFrom = run.Prompt, run.Mode, run.RelayFrom
	err := reg.Append(e)
	reportWatchedLease(c, run)
	return err
}

// reportWatchedLease reports the session budget lease of a watched launch
// once its outcome entry is written (the tab exists; the next sample counts
// whatever runs in it). No LeaseID, no call; a failed report changes nothing.
func reportWatchedLease(c *pigtail.Client, run spawnWatchRun) {
	if run.LeaseID != "" {
		spawnLeaseReport(c, run.LeaseID, true)
	}
}
