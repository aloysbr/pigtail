//go:build linux

package proccpu

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// clkTck is the kernel clock tick rate used by /proc/<pid>/stat (USER_HZ).
// 100 on every mainstream Linux; reading it via sysconf would need cgo or
// x/sys plumbing for a constant that has not changed in decades.
const clkTck = 100.0

// NewSampler returns the linux sampler: /proc/<pid>/stat for every
// numeric entry in /proc.
func NewSampler() Sampler { return procSampler{} }

type procSampler struct{}

func (procSampler) Sample() (map[int]ProcStat, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	procs := map[int]ProcStat{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := readProcStat(pid)
		if err != nil {
			continue // process exited between ReadDir and read
		}
		procs[pid] = st
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("/proc yielded no parseable processes")
	}
	return procs, nil
}

// readProcStat parses /proc/<pid>/stat: ppid is field 4, utime/stime are
// fields 14/15 (1-based, after the parenthesised comm which may contain
// spaces).
func readProcStat(pid int) (ProcStat, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcStat{}, err
	}
	s := string(b)
	// comm is parenthesised and may contain spaces/parens: cut after the
	// LAST ')' then split the remainder.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ProcStat{}, fmt.Errorf("malformed stat for pid %d", pid)
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] = state (field 3), fields[1] = ppid (4),
	// fields[11] = utime (14), fields[12] = stime (15).
	if len(fields) < 13 {
		return ProcStat{}, fmt.Errorf("short stat for pid %d", pid)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return ProcStat{}, err
	}
	utime, err := strconv.ParseFloat(fields[11], 64)
	if err != nil {
		return ProcStat{}, err
	}
	stime, err := strconv.ParseFloat(fields[12], 64)
	if err != nil {
		return ProcStat{}, err
	}
	return ProcStat{PID: pid, PPID: ppid, CPUSeconds: (utime + stime) / clkTck}, nil
}
