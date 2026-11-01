package claudesession

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// psInfo returns the parent pid and full command line of one process. A seam:
// tests never inspect real processes.
var psInfo = func(pid int) (int, string, error) {
	out, err := exec.Command("ps", "-ww", "-o", "ppid=,args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", fmt.Errorf("ps -p %d: %w", pid, err)
	}
	m := psLineRe.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return 0, "", fmt.Errorf("ps -p %d: unexpected output %q", pid, strings.TrimSpace(string(out)))
	}
	ppid, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", fmt.Errorf("ps -p %d: bad ppid %q", pid, m[1])
	}
	return ppid, strings.TrimSpace(m[2]), nil
}

// isClaudeArgv0 is stricter than claudeProcRe on purpose: the Bash tool's own
// shell carries "claude" inside its -c string, and must never pass for the
// caller.
func isClaudeArgv0(args string) bool {
	f := strings.Fields(args)
	return len(f) > 0 && filepath.Base(f[0]) == "claude"
}

// Ancestors lists the parents of pid (excluded), nearest first, up to (not
// including) init. A ps failure is an error, never a shorter chain.
func Ancestors(pid int) ([]int, error) {
	var out []int
	cur := pid
	for i := 0; i < 64; i++ {
		ppid, _, err := psInfo(cur)
		if err != nil {
			return nil, err
		}
		if ppid <= 1 {
			return out, nil
		}
		out = append(out, ppid)
		cur = ppid
	}
	return nil, fmt.Errorf("process tree deeper than 64 above pid %d", pid)
}

// CallerClaude walks up from pid (excluded) to the nearest Claude process —
// the session that ran this pigtail. ok=false when none is found (a human
// terminal); an error when ps could not answer.
func CallerClaude(pid int) (Proc, bool, error) {
	cur := pid
	for i := 0; i < 64; i++ {
		ppid, _, err := psInfo(cur)
		if err != nil {
			return Proc{}, false, err
		}
		if ppid <= 1 {
			return Proc{}, false, nil
		}
		_, args, err := psInfo(ppid)
		if err != nil {
			return Proc{}, false, err
		}
		if isClaudeArgv0(args) {
			return Proc{PID: ppid, Args: args}, true, nil
		}
		cur = ppid
	}
	return Proc{}, false, fmt.Errorf("process tree deeper than 64 above pid %d", pid)
}
