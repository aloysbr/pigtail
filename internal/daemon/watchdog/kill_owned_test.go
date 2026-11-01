package watchdog

import (
	"syscall"
	"testing"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The escalation signals the port's listeners. Only the worktree's own pids
// may be signalled; another worktree's listener that holds (or took over)
// the port never is.
func TestDefaultKillerEscalatesOnOwnedPidsOnly(t *testing.T) {
	oldOwned, oldSignal, oldPgid := ownedListeners, signal, getpgid
	t.Cleanup(func() { ownedListeners, signal, getpgid = oldOwned, oldSignal, oldPgid })
	var signalled []int
	signal = func(pid int, _ syscall.Signal) error { signalled = append(signalled, pid); return nil }
	getpgid = func(pid int) (int, error) { return pid, nil }
	target := Target{Workspace: pigtail.Workspace{ID: "ws_k", Path: t.TempDir()}, Port: 39911}

	// Owned pid 9 next to a foreign 4242: only 9's group is signalled, then
	// the port reads free.
	calls := 0
	ownedListeners = func(int, string) (devserver.Ownership, error) {
		calls++
		if calls == 1 {
			return devserver.Ownership{PIDs: []int{9, 4242}, Owned: []int{9}, Foreign: map[int]string{4242: "/elsewhere"}}, nil
		}
		return devserver.Ownership{}, nil
	}
	if err := DefaultKiller(target); err != nil {
		t.Fatal(err)
	}
	if len(signalled) != 1 || signalled[0] != -9 {
		t.Fatalf("signalled %v, want only the owned group -9", signalled)
	}

	// Held by a foreigner only: nothing is signalled, no error.
	signalled = nil
	ownedListeners = func(int, string) (devserver.Ownership, error) {
		return devserver.Ownership{PIDs: []int{4242}, Foreign: map[int]string{4242: "/elsewhere"}}, nil
	}
	if err := DefaultKiller(target); err != nil {
		t.Fatal(err)
	}
	if len(signalled) != 0 {
		t.Fatalf("signalled %v: a foreign listener", signalled)
	}
}
