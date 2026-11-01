package orphans

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Proc is one row of the process table.
type Proc struct {
	PID, PPID, PGID int
	CPU             float64 // %CPU as ps reports it
	Command         string  // the full command line
}

// psTimeout bounds one listing.
const psTimeout = 5 * time.Second

// ListProcs snapshots the process table with ps (same columns on darwin and
// Linux procps). LC_ALL=C keeps pcpu's decimal point whatever the locale. An
// output with no readable line is an error, never an empty table: an empty
// table would read as « every root vanished ».
func ListProcs() ([]Proc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pgid=,pcpu=,command=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(raw)
}

// parsePS reads `pid ppid pgid pcpu command…` lines; the command is the rest
// of the line, spaces included. A line whose first four columns do not parse
// is skipped.
func parsePS(raw []byte) ([]Proc, error) {
	var procs []Proc
	for _, line := range strings.Split(string(raw), "\n") {
		rest := line
		var cols [4]string
		ok := true
		for i := range cols {
			rest = strings.TrimLeft(rest, " \t")
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				ok = false
				break
			}
			cols[i], rest = rest[:end], rest[end:]
		}
		if !ok {
			continue
		}
		command := strings.TrimSpace(rest)
		pid, e1 := strconv.Atoi(cols[0])
		ppid, e2 := strconv.Atoi(cols[1])
		pgid, e3 := strconv.Atoi(cols[2])
		cpu, e4 := strconv.ParseFloat(cols[3], 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || command == "" {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, PGID: pgid, CPU: cpu, Command: command})
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("ps: no process listed")
	}
	return procs, nil
}
