package devserver

import (
	"fmt"
	"sort"
	"time"
)

// cwdRetryPause separates the two attempts of a cwd probe. A var for tests.
var cwdRetryPause = 50 * time.Millisecond

// Ownership is the one classification of a busy port's listeners for a
// worktree: its own (Owned), another worktree's (Foreign, with each pid's
// cwd), or unknown — the cwd probe failed twice and nothing can be
// attributed. Every surface that kills or names a holder reads it from here.
//
// Unknown is never killed (decided 2026-10-06), and never reported as
// another worktree: the message says the owner could not be determined.
type Ownership struct {
	PIDs     []int          // every listener, sorted
	Owned    []int          // listeners running inside the worktree, sorted
	Foreign  map[int]string // the others: pid → cwd ("" when lsof had none); empty when Unknown
	ProbeErr error          // non-nil: the cwd probe failed, the owner is unknown
}

// Unknown reports a failed cwd probe: no pid is attributed.
func (o Ownership) Unknown() bool { return o.ProbeErr != nil }

// HeldElsewhere: the port is held and no listener is known to be the
// worktree's own — a foreign holder, or an unknown owner.
func (o Ownership) HeldElsewhere() bool { return len(o.PIDs) > 0 && len(o.Owned) == 0 }

// HolderPath names the foreign holder's worktree (the first foreign pid with
// a known cwd), "" when unknown.
func (o Ownership) HolderPath(containerDir string) string {
	if o.Unknown() {
		return ""
	}
	for _, p := range o.foreignPIDs() {
		if c := o.Foreign[p]; c != "" {
			return HolderWorktree(c, containerDir)
		}
	}
	return ""
}

// ForeignPIDs lists the listeners that are not the worktree's own, sorted:
// the foreign ones, or every listener when the owner is unknown.
func (o Ownership) ForeignPIDs() []int {
	if o.Unknown() {
		return append([]int(nil), o.PIDs...)
	}
	return o.foreignPIDs()
}

func (o Ownership) foreignPIDs() []int {
	pids := make([]int, 0, len(o.Foreign))
	for p := range o.Foreign {
		pids = append(pids, p)
	}
	sort.Ints(pids)
	return pids
}

// Describe says who holds the port, for a message:
//
//	"held by another worktree (<worktree>, pid 1,2)"
//	"held by another worktree (unknown holder, pid 7)"   — lsof had no cwd for 7
//	"owner unknown (cwd probe failed: <err>; pid 1,2)"   — the probe itself failed
func (o Ownership) Describe(containerDir string) string {
	if o.Unknown() {
		return fmt.Sprintf("owner unknown (cwd probe failed: %v; pid %s)", o.ProbeErr, joinPIDs(o.PIDs))
	}
	holder := o.HolderPath(containerDir)
	if holder == "" {
		holder = "unknown holder"
	}
	return fmt.Sprintf("held by another worktree (%s, pid %s)", holder, joinPIDs(o.foreignPIDs()))
}

// ProbeCwds runs a cwd probe, retried once on error: lsof fails now and then
// under load (EMFILE, a timeout), and one failure must not turn a worktree's
// own server into an unknown one.
func ProbeCwds(cwdsOf func([]int) (map[int]string, error), pids []int) (map[int]string, error) {
	cwds, err := cwdsOf(pids)
	if err == nil {
		return cwds, nil
	}
	time.Sleep(cwdRetryPause)
	return cwdsOf(pids)
}

// ClassifyListeners attributes pids (a busy port's listeners) to wtDir,
// probing their cwds with cwdsOf (retried once).
func ClassifyListeners(pids []int, wtDir string, cwdsOf func([]int) (map[int]string, error)) Ownership {
	o := Ownership{PIDs: append([]int(nil), pids...)}
	sort.Ints(o.PIDs)
	if len(pids) == 0 {
		return o
	}
	cwds, err := ProbeCwds(cwdsOf, o.PIDs)
	if err != nil {
		o.ProbeErr = err
		o.Foreign = map[int]string{}
		return o
	}
	o.Owned, o.Foreign = SplitByCwd(o.PIDs, cwds, wtDir)
	return o
}

// OwnedListeners probes port and classifies its listeners for wtDir. Every
// stop path kills Owned only: a listener running elsewhere is another
// worktree's server, whatever .env.local says, and one whose cwd could not be
// probed is not killed either. err is the port probe's own failure.
func OwnedListeners(port int, wtDir string) (Ownership, error) {
	busy, pids, err := portBusy(port)
	if err != nil || !busy {
		return Ownership{}, err
	}
	return ClassifyListeners(pids, wtDir, processCwds), nil
}
