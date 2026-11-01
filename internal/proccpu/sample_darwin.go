//go:build darwin

package proccpu

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// NewSampler returns the darwin sampler: one `ps` call captures every
// process's accumulated CPU time (macOS has no /proc).
func NewSampler() Sampler { return psSampler{} }

// psSampler shells out to ps. Fixed argv — no shell interpolation.
type psSampler struct{}

func (psSampler) Sample() (map[int]ProcStat, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,time=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePSOutput(string(out))
}

// parsePSOutput decodes `ps -axo pid=,ppid=,time=` lines. Unparseable lines
// are skipped (zombies can render odd TIME values).
func parsePSOutput(out string) (map[int]ProcStat, error) {
	procs := map[int]ProcStat{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		secs, err := ParsePSTime(fields[2])
		if err != nil {
			continue
		}
		procs[pid] = ProcStat{PID: pid, PPID: ppid, CPUSeconds: secs}
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("ps returned no parseable processes")
	}
	return procs, nil
}
