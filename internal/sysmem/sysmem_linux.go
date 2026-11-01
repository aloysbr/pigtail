//go:build linux

package sysmem

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Available returns MemAvailable from /proc/meminfo.
func Available() (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, err := strconv.ParseUint(f[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return kb * 1024, nil
			}
		}
	}
	return 0, fmt.Errorf("meminfo: MemAvailable not found")
}

// Swapouts returns the cumulative bytes swapped out since boot.
func Swapouts() (uint64, error) {
	b, err := os.ReadFile("/proc/vmstat")
	if err != nil {
		return 0, err
	}
	pageSize := os.Getpagesize()
	if pageSize <= 0 {
		return 0, fmt.Errorf("vmstat: page size %d", pageSize)
	}
	return ParseProcVMStatSwapouts(string(b), uint64(pageSize)) //nolint:gosec // G115: positive, checked just above
}

// Pressure maps PSI onto the darwin pressure scale (see ParsePSIMemory).
func Pressure() (int, error) {
	b, err := os.ReadFile("/proc/pressure/memory")
	if err != nil {
		return 0, err
	}
	return ParsePSIMemory(string(b))
}

// Procs returns every visible process's VmRSS (as MemBytes) and parent, from
// /proc. VmSwap is deliberately left out: swapped pages hold no RAM, and Linux
// is a CI platform here, not the one the eviction and admission thresholds
// were tuned on.
func Procs() (map[int]Proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	procs := map[int]Proc{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, rss, err := readProcMem(pid)
		if err != nil {
			continue
		}
		procs[pid] = Proc{PID: pid, PPID: ppid, MemBytes: rss}
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("/proc: no parseable processes")
	}
	return procs, nil
}

// readProcMem reads ppid from /proc/<pid>/stat (field 4 after comm) and
// VmRSS from /proc/<pid>/status.
func readProcMem(pid int) (ppid int, rssBytes uint64, err error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("bad stat")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, 0, fmt.Errorf("short stat")
	}
	ppid, err = strconv.Atoi(f[1])
	if err != nil {
		return 0, 0, err
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			ff := strings.Fields(line)
			if len(ff) >= 2 {
				kb, err := strconv.ParseUint(ff[1], 10, 64)
				if err != nil {
					return 0, 0, err
				}
				return ppid, kb * 1024, nil
			}
		}
	}
	return ppid, 0, nil // kernel threads have no VmRSS
}

// ReadMemory takes one reading. Only Available is mandatory (see the darwin
// twin): no PSI or no swap counter leaves that field at zero.
func ReadMemory() (Memory, error) {
	var m Memory
	var err error
	if m.Available, err = Available(); err != nil {
		return Memory{}, err
	}
	m.Swapouts, _ = Swapouts()
	m.Pressure, _ = Pressure()
	m.Total, _ = memTotal()
	return m, nil
}

// memTotal returns MemTotal from /proc/meminfo.
func memTotal() (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, err := strconv.ParseUint(f[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return kb * 1024, nil
			}
		}
	}
	return 0, fmt.Errorf("meminfo: MemTotal not found")
}
