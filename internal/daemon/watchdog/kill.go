package watchdog

import (
	"fmt"
	"log"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// DefaultConfigResolver loads the workspace's project config (walking up
// from its path to pigtail.yaml) and returns its watchdog block. Falls back
// to the defaults when the config cannot be resolved — the watchdog still
// guards workspaces whose project config is briefly unreadable.
func DefaultConfigResolver(ws pigtail.Workspace) *pigtail.WatchdogConfig {
	ctx, err := devserver.ResolveContextFromPath(ws.Path)
	if err != nil || ctx.Config == nil {
		return pigtail.WatchdogDefaults()
	}
	return ctx.Config.Watchdog
}

// DefaultPortPIDs adapts devserver's lsof-based port probe.
func DefaultPortPIDs(port int) (bool, []int, error) {
	return devserver.IsPortBusy(port)
}

// Seams of DefaultKiller's escalation (tests replace them).
var (
	ownedListeners = devserver.OwnedListeners
	signal         = syscall.Kill
	getpgid        = syscall.Getpgid
)

// DefaultKiller terminates a confirmed runaway. Preferred path is the
// regular `pigtail stop` logic for the workspace's worktree (keeps daemon
// state consistent and purges the Turbopack dev cache); if the port is
// still busy afterwards, it escalates to killing the listener's process
// group (dev servers are spawned with Setpgid), then the bare PIDs.
func DefaultKiller(t Target) error {
	ctx, err := devserver.ResolveContextFromPath(t.Workspace.Path)
	if err == nil {
		_ = devserver.Stop(ctx, filepath.Base(t.Workspace.Path), "")
	}

	// Only the worktree's own listeners are escalated on: a pid running
	// elsewhere is another worktree's server (or one that took the port over
	// in the meantime), never ours to kill.
	own, _ := ownedListeners(t.Port, t.Workspace.Path)
	if len(own.PIDs) == 0 {
		return nil
	}
	if own.HeldElsewhere() {
		log.Printf("watchdog: :%d now %s, not killed",
			t.Port, own.Describe(filepath.Dir(t.Workspace.Path)))
		return nil
	}

	// Fallback: SIGTERM → SIGKILL the process group of each survivor.
	signalAll(own.Owned, syscall.SIGTERM)
	time.Sleep(400 * time.Millisecond)
	if own, _ = ownedListeners(t.Port, t.Workspace.Path); len(own.Owned) == 0 {
		return nil
	}
	signalAll(own.Owned, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
	if own, _ = ownedListeners(t.Port, t.Workspace.Path); len(own.Owned) > 0 {
		return fmt.Errorf("port :%d still busy after SIGKILL (pids %v)", t.Port, own.Owned)
	}
	return nil
}

// signalAll signals each pid's process group (dev servers are spawned with
// Setpgid), or the bare pid when its group cannot be read.
func signalAll(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		if pgid, err := getpgid(pid); err == nil {
			_ = signal(-pgid, sig)
		} else {
			_ = signal(pid, sig)
		}
	}
}
