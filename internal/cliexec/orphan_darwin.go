package cliexec

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// readPPID reads pid's parent from the kernel's process table. EIO and ESRCH
// (and a record for another pid) mean the process is gone.
func readPPID(pid int) (ppid int, alive bool, err error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EIO) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if int(k.Proc.P_pid) != pid {
		return 0, false, nil
	}
	return int(k.Eproc.Ppid), true, nil
}
