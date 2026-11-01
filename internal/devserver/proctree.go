package devserver

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// procTreeTimeout bounds a `ps` call. A var for tests.
var procTreeTimeout = 5 * time.Second

// ProcessParents snapshots the process table as pid → ppid.
//
// The parser is dedicated on purpose: internal/sysmem has ParsePSRss and
// SumTree, but they are darwin-only and shaped for RSS sums, while this must
// build and run on the Linux CI too. `ps -axo pid=,ppid=` is the same on
// darwin and Linux procps. An output with no parseable line is an error,
// never an empty tree.
func ProcessParents() (map[int]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), procTreeTimeout)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	parents := parseParents(raw)
	if len(parents) == 0 {
		return nil, fmt.Errorf("ps: no process listed")
	}
	return parents, nil
}

// parseParents reads `pid ppid` lines, skipping anything else.
func parseParents(raw []byte) map[int]int {
	parents := map[int]int{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		parents[pid] = ppid
	}
	return parents
}

// Descendants lists every process below roots in the pid → ppid table,
// sorted, roots excluded. pid <= 1 is never a root nor a descendant, and a
// cycle in the table cannot loop.
func Descendants(parents map[int]int, roots []int) []int {
	children := map[int][]int{}
	for pid, ppid := range parents {
		if pid > 1 && ppid > 1 {
			children[ppid] = append(children[ppid], pid)
		}
	}
	isRoot := map[int]bool{}
	seen := map[int]bool{}
	var queue []int
	for _, r := range roots {
		if r > 1 {
			isRoot[r] = true
			seen[r] = true
			queue = append(queue, r)
		}
	}
	var found []int
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if seen[c] {
				continue
			}
			seen[c] = true
			found = append(found, c)
			queue = append(queue, c)
		}
	}
	sort.Ints(found)
	return found
}

// processParentsFn is the seam over ProcessParents.
var processParentsFn = ProcessParents

// withDescendants returns roots followed by their descendants. The snapshot
// is taken BEFORE the kill: once a parent dies its children are reparented to
// launchd and the link is lost.
func withDescendants(roots []int) []int {
	parents, err := processParentsFn()
	if err != nil {
		fmt.Fprintf(out, "  · could not list child processes (%v); stopping the listener only\n", err)
		return roots
	}
	return append(append([]int(nil), roots...), Descendants(parents, roots)...)
}
