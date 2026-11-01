//go:build darwin && cgo

package inputprobe

/*
#include <errno.h>
#include <stdlib.h>
#include <libproc.h>
#include <fcntl.h>
#include <sys/proc_info.h>

// pt_stdin: 0 on success (*onvnode says whether fd 0 is a vnode, *rdev its
// vst_rdev), else -errno. The fd list comes first: proc_pidfdinfo on fd 0
// answers EBADF for a pipe or a socket, which is an answer ("not the tty"),
// not an error.
static int pt_stdin(int pid, unsigned int *rdev, int *onvnode) {
	*onvnode = 0;
	*rdev = 0;
	errno = 0;
	int sz = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
	if (sz <= 0) {
		return errno ? -errno : -EIO;
	}
	sz += 16 * (int)sizeof(struct proc_fdinfo);
	struct proc_fdinfo *fds = malloc((size_t)sz);
	if (fds == NULL) {
		return -ENOMEM;
	}
	errno = 0;
	int n = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, sz);
	if (n <= 0) {
		int e = errno ? errno : EIO;
		free(fds);
		return -e;
	}
	int cnt = n / (int)sizeof(struct proc_fdinfo);
	int found = 0;
	for (int i = 0; i < cnt; i++) {
		if (fds[i].proc_fd == 0 && fds[i].proc_fdtype == PROX_FDTYPE_VNODE) {
			found = 1;
		}
	}
	free(fds);
	if (!found) {
		return 0;
	}
	struct vnode_fdinfowithpath vi;
	errno = 0;
	int r = proc_pidfdinfo(pid, 0, PROC_PIDFDVNODEPATHINFO, &vi, (int)sizeof(vi));
	if (r != (int)sizeof(vi)) {
		return errno ? -errno : -EIO;
	}
	*onvnode = 1;
	*rdev = (unsigned int)vi.pvip.vip_vi.vi_stat.vst_rdev;
	return 0;
}

// pt_rdevs: 0 on success, else -errno. Fills out[0..*count) with the vst_rdev
// of every vnode fd of pid opened for reading (at most max; a fd closed
// between the list and the read is skipped).
static int pt_rdevs(int pid, unsigned int *out, int max, int *count) {
	*count = 0;
	errno = 0;
	int sz = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
	if (sz <= 0) {
		return errno ? -errno : -EIO;
	}
	sz += 16 * (int)sizeof(struct proc_fdinfo);
	struct proc_fdinfo *fds = malloc((size_t)sz);
	if (fds == NULL) {
		return -ENOMEM;
	}
	errno = 0;
	int n = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, sz);
	if (n <= 0) {
		int e = errno ? errno : EIO;
		free(fds);
		return -e;
	}
	int cnt = n / (int)sizeof(struct proc_fdinfo);
	for (int i = 0; i < cnt && *count < max; i++) {
		if (fds[i].proc_fdtype != PROX_FDTYPE_VNODE) {
			continue;
		}
		struct vnode_fdinfowithpath vi;
		errno = 0;
		int r = proc_pidfdinfo(pid, fds[i].proc_fd, PROC_PIDFDVNODEPATHINFO, &vi, (int)sizeof(vi));
		if (r != (int)sizeof(vi)) {
			int e = errno ? errno : EIO;
			if (e == EBADF || e == ENOENT) {
				continue;
			}
			free(fds);
			return -e;
		}
		if (vi.pfi.fi_openflags & FREAD) {
			out[(*count)++] = (unsigned int)vi.pvip.vip_vi.vi_stat.vst_rdev;
		}
	}
	free(fds);
	return 0;
}
*/
import "C"

import (
	"fmt"
	"syscall"
)

// stdinTTYImpl reads fd 0 of pid through libproc: onVnode says fd 0 is a
// vnode (not a pipe, a socket, a kqueue, or absent), rdev its device number.
// ESRCH (the process left) is returned as such; so is every other failure.
func stdinTTYImpl(pid int) (uint64, bool, error) {
	var rdev C.uint
	var onv C.int
	if r := C.pt_stdin(C.int(pid), &rdev, &onv); r != 0 {
		if errno := syscall.Errno(-r); errno != 0 {
			return 0, false, fmt.Errorf("inputprobe: fd 0 of pid %d: %w", pid, errno)
		}
		return 0, false, fmt.Errorf("inputprobe: fd 0 of pid %d: unknown error", pid)
	}
	return uint64(rdev), onv != 0, nil
}

// vnodeRdevsImpl lists the device numbers of every vnode fd of pid opened for
// reading (up to 1024), so a tty reached on another fd (/dev/tty: sudo, ssh,
// git prompts) is seen too. Failures are returned as such, ESRCH included.
func vnodeRdevsImpl(pid int) ([]uint64, error) {
	var buf [1024]C.uint
	var cnt C.int
	if r := C.pt_rdevs(C.int(pid), &buf[0], C.int(len(buf)), &cnt); r != 0 {
		if errno := syscall.Errno(-r); errno != 0 {
			return nil, fmt.Errorf("inputprobe: fds of pid %d: %w", pid, errno)
		}
		return nil, fmt.Errorf("inputprobe: fds of pid %d: unknown error", pid)
	}
	out := make([]uint64, int(cnt))
	for i := range out {
		out[i] = uint64(buf[i])
	}
	return out, nil
}
