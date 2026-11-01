package cliexec

import (
	"os"
	"os/signal"
	"syscall"
)

// Seams: tests script the process table and the signal disposition.
var (
	getppid    = os.Getppid
	getpgrp    = syscall.Getpgrp
	hupIgnored = func() bool { return signal.Ignored(syscall.SIGHUP) }
	// ppidReader reads a process's parent. alive=false means the process is
	// gone; a non-nil err is any other failure to read.
	ppidReader = readPPID
)

// ArmOrphanWatch arms the orphan probe and returns it, or nil when it must
// not be armed. Call it at start-up, before anything waits and before any
// signal.Notify (a Notify lifts an ignored disposition, and the "SIGHUP is
// ignored" test below would then read false).
//
// Armed only if, right now, neither this process's parent nor the parent of
// the leader of its process group is launchd (pid 1), and SIGHUP is not
// ignored: under `nohup` a detached run is wanted, never stopped. The leader
// is the anchor: the shell of an agent's Bash tool is a session leader that
// never execs, so when its claude dies the shell goes to launchd but stays
// this process's parent — the leader's own parent is what changes.
//
// The returned probe is true once the parent is launchd, or the leader is
// gone or reparented to launchd. A failure to read the leader is not an
// orphan.
func ArmOrphanWatch() func() bool {
	if hupIgnored() || getppid() == 1 {
		return nil
	}
	leader := getpgrp()
	lppid, alive, err := ppidReader(leader)
	if err != nil || !alive || lppid == 1 {
		return nil
	}
	return func() bool {
		if getppid() == 1 {
			return true
		}
		p, alive, err := ppidReader(leader)
		if err != nil {
			return false
		}
		return !alive || p == 1
	}
}
