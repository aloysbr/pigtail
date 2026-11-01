//go:build darwin

package sysmem

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// Available returns the bytes the system could reclaim right now (see
// ParseVMStatAvailable).
func Available() (uint64, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("vm_stat: %w", err)
	}
	return ParseVMStatAvailable(string(out))
}

// Swapouts returns the cumulative bytes swapped out since boot.
func Swapouts() (uint64, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("vm_stat: %w", err)
	}
	return ParseVMStatSwapouts(string(out))
}

// Pressure returns the kernel's own memory-pressure verdict
// (kern.memorystatus_vm_pressure_level: 1 normal, 2 warn, 4 critical). A
// sysctl, not a fork: cheap enough to read on every decision.
func Pressure() (int, error) {
	v, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return 0, fmt.Errorf("sysctl kern.memorystatus_vm_pressure_level: %w", err)
	}
	return int(v), nil
}

// Procs returns every visible process's memory and parent: the tree from one
// ps, each process's memory from its physical footprint (see physFootprint;
// applyFootprints for what happens when one cannot be read). Footprints for
// ~1300 processes cost ~5 ms, a tenth of the ps fork.
func Procs() (map[int]Proc, error) {
	// LC_ALL=C: pcpu with a decimal point whatever the user's locale.
	cmd := exec.Command("ps", "-axo", "pid=,ppid=,rss=,pcpu=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	procs, err := ParsePSRss(string(out))
	if err != nil {
		return nil, err
	}
	applyFootprints(procs, physFootprint, footprintErrors.report)
	return procs, nil
}

// ReadMemory takes one reading: a single vm_stat for the page counters, two
// sysctls for pressure and total. Only Available is mandatory — a pressure or
// swap counter that cannot be read leaves its field at zero, which the
// admission gates read as "no signal", never as "all clear to refuse".
func ReadMemory() (Memory, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return Memory{}, fmt.Errorf("vm_stat: %w", err)
	}
	var m Memory
	if m.Available, err = ParseVMStatAvailable(string(out)); err != nil {
		return Memory{}, err
	}
	m.Swapouts, _ = ParseVMStatSwapouts(string(out))
	if c, err := ParseVMStatCompressor(string(out)); err == nil {
		m.Compressions, m.Decompressions, m.CompressorBytes, m.UnusedBytes =
			c.Compressions, c.Decompressions, c.OccupiedBytes, c.UnusedBytes
	}
	m.Pressure, _ = Pressure()
	if total, err := unix.SysctlUint64("hw.memsize"); err == nil {
		m.Total = total
	}
	return m, nil
}
