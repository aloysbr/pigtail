//go:build darwin

package admission

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// platformShape reads performance cores (hw.perflevel0.physicalcpu on Apple
// silicon, all physical cores on an Intel mac) and physical memory.
func platformShape() (cores int, mem uint64) {
	if n, err := unix.SysctlUint32("hw.perflevel0.physicalcpu"); err == nil && n > 0 {
		cores = int(n)
	} else if n, err := unix.SysctlUint32("hw.physicalcpu"); err == nil && n > 0 {
		cores = int(n)
	} else {
		cores = runtime.NumCPU()
	}
	mem, _ = unix.SysctlUint64("hw.memsize")
	return cores, mem
}
