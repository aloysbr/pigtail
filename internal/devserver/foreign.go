package devserver

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ForeignListenerError: the worktree's port is held by a process running
// elsewhere. Starting nothing and saying "already running" — the behaviour
// until 2026-10-05 — left the worktree serving another worktree's app.
type ForeignListenerError struct {
	Server, Kind string
	Port         int
	PIDs         []int
	HolderPath   string // "" when the holder's cwd is unknown
	WorktreeDir  string
}

func (e *ForeignListenerError) Error() string {
	holder := "unknown holder"
	if e.HolderPath != "" {
		holder = e.HolderPath
	}
	return fmt.Sprintf("%s: port %d is held by another worktree (%s, pid %s), not by %s — nothing started.\n"+
		"Give this worktree a free port: cd %s && pigtail ports reassign --kind %s",
		e.Server, e.Port, holder, joinPIDs(e.PIDs), filepath.Base(e.WorktreeDir), e.WorktreeDir, e.Kind)
}

func joinPIDs(pids []int) string {
	s := make([]string, len(pids))
	for i, p := range pids {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

// checkOwnListener decides what a busy port means for wtDir and reports it.
// skip=true: nothing to start — the worktree's own server already runs, or
// the owner could not be determined (the cwd probe failed twice: nothing is
// started, nothing is claimed, the message says to retry). A port held from
// another worktree is refused with a ForeignListenerError.
func checkOwnListener(ctx *Context, wtDir, serverName, kind string, port int, pids []int) (skip bool, err error) {
	o := ClassifyListeners(pids, wtDir, processCwds)
	switch {
	case o.Unknown():
		fmt.Fprintf(out, "%s on :%d — %s, nothing started; retry\n", serverName, port, o.Describe(ctx.ContainerDir))
		return true, nil
	case len(o.Owned) > 0:
		fmt.Fprintf(out, "%s already running on :%d (pids: %v)\n", serverName, port, pids)
		return true, nil
	}
	return false, &ForeignListenerError{
		Server: serverName, Kind: kind, Port: port, PIDs: o.ForeignPIDs(),
		HolderPath: o.HolderPath(ctx.ContainerDir), WorktreeDir: wtDir,
	}
}
