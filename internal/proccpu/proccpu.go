// Package proccpu samples per-process CPU accounting and sums it over a
// process subtree. Shared by the runaway watchdog (which kills processes
// pegging the CPU) and the RAM monitor (which uses the same signal, inverted,
// to tell an idle dev server from a busy one).
package proccpu

import (
	"fmt"
	"strconv"
	"strings"
)

// ProcStat is one process's CPU accounting at a point in time.
type ProcStat struct {
	PID        int
	PPID       int
	CPUSeconds float64 // accumulated user+system CPU time
}

// Sampler captures a snapshot of every visible process. Implemented per
// platform (ps on darwin, /proc on linux) and faked in tests.
type Sampler interface {
	Sample() (map[int]ProcStat, error)
}

// ParsePSTime converts a ps TIME field ("[[dd-]hh:]mm:ss[.ff]") to seconds.
func ParsePSTime(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time")
	}
	var days float64
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			return 0, fmt.Errorf("parse days in %q: %w", s, err)
		}
		days = d
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("unexpected time format %q", s)
	}
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, fmt.Errorf("parse component %q in %q: %w", p, s, err)
		}
		total = total*60 + v
	}
	return days*86400 + total, nil
}

// SubtreeCPU sums CPUSeconds for root and every process reachable from it
// through PPID links. Missing root yields 0.
func SubtreeCPU(procs map[int]ProcStat, root int) float64 {
	children := map[int][]int{}
	for pid, st := range procs {
		children[st.PPID] = append(children[st.PPID], pid)
	}
	var total float64
	stack := []int{root}
	seen := map[int]bool{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if st, ok := procs[pid]; ok {
			total += st.CPUSeconds
		}
		stack = append(stack, children[pid]...)
	}
	return total
}
