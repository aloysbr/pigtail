//go:build darwin && cgo

package inputprobe

/*
#include <libproc.h>
#include <sys/proc_info.h>

static int pt_syscalls_unix(int pid, unsigned long long *out) {
	struct proc_taskinfo ti;
	int n = proc_pidinfo(pid, PROC_PIDTASKINFO, 0, &ti, sizeof(ti));
	if (n != (int)sizeof(ti)) {
		return n;
	}
	*out = (unsigned long long)(unsigned int)ti.pti_syscalls_unix;
	return n;
}

static int pt_taskinfo_size(void) { return (int)sizeof(struct proc_taskinfo); }
*/
import "C"

import (
	"errors"
	"fmt"
)

// ErrUnsupported: no syscall counter on this build (the probe then answers
// Unknown, never Dead).
var ErrUnsupported = errors.New("inputprobe: syscall counter unsupported on this build")

// DefaultCounter reads pti_syscalls_unix of pid through libproc
// (proc_pidinfo PROC_PIDTASKINFO). A short read is an error.
func DefaultCounter(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("inputprobe: invalid pid %d", pid)
	}
	var out C.ulonglong
	n := C.pt_syscalls_unix(C.int(pid), &out)
	if n != C.pt_taskinfo_size() {
		return 0, fmt.Errorf("inputprobe: proc_pidinfo(%d) read %d bytes, want %d", pid, int(n), int(C.pt_taskinfo_size()))
	}
	return uint64(out), nil
}
