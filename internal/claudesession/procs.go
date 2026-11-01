package claudesession

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Proc is one running process whose command line carries a claude token.
type Proc struct {
	PID  int
	Args string
	// Ancestors: the pids of its parents, nearest first, read from the same
	// listing (a parent missing from the listing is kept as the last one).
	// cmux 0.65 runs `cmux hooks claude …` children under each claude, whose
	// command line carries the same token: the chain tells them apart.
	Ancestors []int
}

// maxAncestors bounds the walk up the process tree.
const maxAncestors = 64

// psTreeLineRe splits one `ps -axwwo pid=,ppid=,command=` line.
var psTreeLineRe = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(.*)$`)

// ClaudeProcesses lists the Claude processes running right now, from one
// `ps` listing (pid, ppid, command) and the same claudeProcRe filter as
// Running and LiveSessions, pid files not consulted: a caller comparing two
// listings needs every Claude process, including one whose
// sessions/<pid>.json is not written yet.
//
// It fails when the listing cannot be obtained: "could not look" must not
// read as "nothing is running".
func ClaudeProcesses() ([]Proc, error) {
	out, err := psTreeRun()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	return ParseClaudeProcs(out, os.Getpid()), nil
}

// ParseClaudeProcs is ClaudeProcesses after the listing: the claude
// processes of out (`pid ppid command` lines), without pid self, each with
// its ancestors. The chain is built from every process of the listing, a
// non-claude intermediate included; it stops at pid 1 or 0, at a pid already
// seen (a loop) and after maxAncestors hops.
func ParseClaudeProcs(out []byte, self int) []Proc {
	parent := map[int]int{}
	type row struct {
		pid  int
		args string
	}
	var rows []row
	for _, line := range strings.Split(string(out), "\n") {
		m := psTreeLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err1 := strconv.Atoi(m[1])
		ppid, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil {
			continue
		}
		parent[pid] = ppid
		rows = append(rows, row{pid, strings.TrimSpace(m[3])})
	}
	var procs []Proc
	for _, r := range rows {
		if r.pid == self || !claudeProcRe.MatchString(r.args) {
			continue
		}
		p := Proc{PID: r.pid, Args: r.args}
		seen := map[int]bool{r.pid: true}
		for cur := r.pid; len(p.Ancestors) < maxAncestors; {
			pp, ok := parent[cur]
			if !ok || pp <= 1 || seen[pp] {
				break
			}
			seen[pp] = true
			p.Ancestors = append(p.Ancestors, pp)
			cur = pp
		}
		procs = append(procs, p)
	}
	return procs
}
