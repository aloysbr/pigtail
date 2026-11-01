//go:build darwin

package sysmem

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// footprintErrors logs unexpected footprint errors (A4) to the daemon's
// stderr — ~/.config/pigtail/daemon.log under launchd — once per errno.
var footprintErrors = &oncePerErrno{logf: log.Printf}

const (
	// procInfoCallPIDRusage is PROC_INFO_CALL_PIDRUSAGE (xnu
	// bsd/sys/proc_info_private.h, not in the public SDK): libproc's
	// proc_pid_rusage(pid, flavor, buf) is
	// __proc_info(PROC_INFO_CALL_PIDRUSAGE, pid, flavor, 0, buf, 0).
	// Called directly so the binary needs no cgo.
	procInfoCallPIDRusage = 9
	// rusageInfoFlavorV4 is RUSAGE_INFO_V4 (<sys/resource.h>).
	rusageInfoFlavorV4 = 4
	// sysProcInfo is SYS_proc_info, syscall 336 on both darwin arches (xnu
	// bsd/kern/syscalls.master; same value as x/sys's zsysnum_darwin_amd64.go
	// and zsysnum_darwin_arm64.go). Declared here because unix.SYS_PROC_INFO
	// sits in a block x/sys marks Deprecated ("use libSystem wrappers") — and
	// x/sys has no wrapper for proc_pid_rusage to use instead.
	sysProcInfo = 336
)

// rusageInfoV4 mirrors struct rusage_info_v4 (<sys/resource.h>): a 16-byte
// uuid then 35 uint64. Only the fields up to ri_phys_footprint are named;
// TestRusageInfoV4Layout pins size and offset.
type rusageInfoV4 struct {
	UUID           [16]byte
	UserTime       uint64
	SystemTime     uint64
	PkgIdleWkups   uint64
	InterruptWkups uint64
	Pageins        uint64
	WiredSize      uint64
	ResidentSize   uint64
	PhysFootprint  uint64
	_              [27]uint64
}

// rusageV4 returns pid's whole rusage_info_v4. Errors are the raw errno:
// ESRCH for a process that is gone, EPERM for another user's process.
func rusageV4(pid int) (rusageInfoV4, error) {
	var ri rusageInfoV4
	_, _, errno := unix.Syscall6(sysProcInfo,
		procInfoCallPIDRusage, uintptr(pid), rusageInfoFlavorV4, 0,
		uintptr(unsafe.Pointer(&ri)), 0) //nolint:gosec // G103: the kernel fills a struct we own, sized and pinned by TestRusageInfoV4Layout
	if errno != 0 {
		return rusageInfoV4{}, errno
	}
	return ri, nil
}

// physFootprint returns pid's physical footprint: what top's MEM column and
// footprint(1) show — private dirty memory, compressed pages included. It is
// what the process costs the machine, which ps RSS (no compressed pages,
// shared pages counted) is not. Errors are the raw errno (see rusageV4).
func physFootprint(pid int) (uint64, error) {
	ri, err := rusageV4(pid)
	if err != nil {
		return 0, err
	}
	return ri.PhysFootprint, nil
}

// selfCheckPSTimeout bounds the ps fork of the startup self-check (C6): the
// check runs before the daemon serves, and must never hold its start hostage
// to a stuck fork — memory pressure is exactly when ps is slowest. Past it,
// the reading is "unverified". A var, with selfCheckPSCommand, for the test.
var selfCheckPSTimeout = 2 * time.Second

// selfCheckPSCommand builds the ps that reads one pid's RSS in KiB.
var selfCheckPSCommand = func(ctx context.Context, pid int) *exec.Cmd {
	return exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid))
}

// ReadSelfFootprint reads this process's memory both ways — the private
// proc_info call (footprint and resident size) and ps RSS — for the startup
// self-check (decision D7). TestRusageInfoV4Layout pins our side of the
// struct at compile time; only a run against the live kernel can see its
// side change, which is what this reading is for.
func ReadSelfFootprint() SelfFootprint {
	pid := os.Getpid()
	var r SelfFootprint
	if ri, err := rusageV4(pid); err != nil {
		r.Err = err
	} else {
		r.Footprint, r.Resident = ri.PhysFootprint, ri.ResidentSize
	}
	ctx, cancel := context.WithTimeout(context.Background(), selfCheckPSTimeout)
	defer cancel()
	out, err := selfCheckPSCommand(ctx, pid).Output()
	if ctx.Err() != nil {
		r.RSSErr = fmt.Errorf("ps: timed out after %s", selfCheckPSTimeout)
		return r
	}
	if err != nil {
		r.RSSErr = fmt.Errorf("ps: %w", err)
		return r
	}
	kb, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		r.RSSErr = fmt.Errorf("ps: parse %q: %w", strings.TrimSpace(string(out)), err)
		return r
	}
	r.RSS = kb * 1024
	return r
}

// SelfCheckLine is the line pigtaild logs at start on darwin.
func SelfCheckLine() (string, bool) {
	return FormatSelfCheck(ReadSelfFootprint()), true
}
