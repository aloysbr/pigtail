package inputprobe

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// psTimeout bounds every ps the probe runs.
const psTimeout = 3 * time.Second

// ProcRow is one line of ListProcs: a process, its group, its terminal's
// foreground group, its state and its tty (the ps column, "ttys037" or "??").
type ProcRow struct {
	PID, PGID, TPGID int
	Stat, TTY        string
}

// listProcs is a seam for tests: `ps -axo pid=,pgid=,tpgid=,stat=,tty=`,
// bounded at 3 s.
var listProcs = func() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "ps", "-axo", "pid=,pgid=,tpgid=,stat=,tty=").Output()
}

// ListProcs lists every process (one ps for all the claudes of a check).
func ListProcs() ([]byte, error) { return listProcs() }

// ParseProcs reads ListProcs' output; a line that does not parse is dropped.
func ParseProcs(out []byte) []ProcRow {
	rows, _ := parseProcs(out)
	return rows
}

// ParseProcsStrict is ParseProcs, but a non-empty line that does not parse is
// an error: a dropped row could be a foreground member reading the tty.
func ParseProcsStrict(out []byte) ([]ProcRow, error) {
	rows, bad := parseProcs(out)
	if bad != "" {
		return nil, fmt.Errorf("inputprobe: unparseable process row %q", bad)
	}
	return rows, nil
}

// parseProcs returns the rows and the first non-empty line that did not parse.
func parseProcs(out []byte) (rows []ProcRow, bad string) {
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		pid, e1 := 0, error(nil)
		pgid, e2 := 0, error(nil)
		tpgid, e3 := 0, error(nil)
		if len(f) == 5 {
			pid, e1 = strconv.Atoi(f[0])
			pgid, e2 = strconv.Atoi(f[1])
			tpgid, e3 = strconv.Atoi(f[2])
		}
		if len(f) != 5 || e1 != nil || e2 != nil || e3 != nil {
			if bad == "" {
				bad = strings.TrimSpace(line)
			}
			continue
		}
		rows = append(rows, ProcRow{PID: pid, PGID: pgid, TPGID: tpgid, Stat: f[3], TTY: strings.TrimPrefix(f[4], "/dev/")})
	}
	return rows, bad
}

// TTYPath is the device path of a ps tty column, "" for none ("??", "-").
func TTYPath(col string) string {
	col = strings.TrimPrefix(strings.TrimSpace(col), "/dev/")
	if col == "" || col == "??" || col == "-" {
		return ""
	}
	return "/dev/" + col
}

// psTTY is a seam for tests: the row of one pid, `ps -o stat=,pgid=,tpgid=,tty=`.
var psTTY = func(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "stat=,pgid=,tpgid=,tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", fmt.Errorf("ps -p %d: %w", pid, err)
	}
	return string(out), nil
}
