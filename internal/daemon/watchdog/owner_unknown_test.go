package watchdog

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"syscall"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The escalation never signals a listener whose owner is unknown, and its
// log says the owner is unknown — not another worktree.
func TestDefaultKillerOwnerUnknown(t *testing.T) {
	oldOwned, oldSignal, oldPgid := ownedListeners, signal, getpgid
	t.Cleanup(func() { ownedListeners, signal, getpgid = oldOwned, oldSignal, oldPgid })
	var signalled []int
	signal = func(pid int, _ syscall.Signal) error { signalled = append(signalled, pid); return nil }
	getpgid = func(pid int) (int, error) { return pid, nil }
	ownedListeners = func(int, string) (devserver.Ownership, error) {
		return devserver.Ownership{PIDs: []int{9}, ProbeErr: errors.New("lsof cwd: EMFILE")}, nil
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(nil) })

	if err := DefaultKiller(Target{Workspace: pigtail.Workspace{ID: "ws_k", Path: t.TempDir()}, Port: 39911}); err != nil {
		t.Fatal(err)
	}
	if len(signalled) != 0 {
		t.Fatalf("signalled %v on an unknown owner", signalled)
	}
	got := buf.String()
	if strings.Contains(got, "another worktree") || !strings.Contains(got, "owner unknown (cwd probe failed: lsof cwd: EMFILE; pid 9)") {
		t.Fatalf("log %q misattributes an unknown owner", got)
	}
}

// One failed cwd probe is retried: the runaway is still collected.
func TestCollectTargetsRetriesCwdProbeOnce(t *testing.T) {
	own := t.TempDir()
	st := &fakeStore{workspaces: []pigtail.Workspace{{ID: "ws1", Path: own, Ports: map[string]int{"dev": 3030}}}}
	calls := 0
	w := New(events.New(), st,
		WithConfigResolver(func(pigtail.Workspace) *pigtail.WatchdogConfig { return pigtail.WatchdogDefaults() }),
		WithPortPIDs(func(int) (bool, []int, error) { return true, []int{101}, nil }),
		WithCwds(func([]int) (map[int]string, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("lsof cwd: EMFILE")
			}
			return map[int]string{101: own}, nil
		}),
	)
	if got := w.collectTargets(); len(got) != 1 || len(got[0].PIDs) != 1 || got[0].PIDs[0] != 101 {
		t.Fatalf("targets %+v, want the owned pid 101 after the retry", got)
	}
}
