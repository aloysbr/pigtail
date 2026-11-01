package devserver

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ProcessCwds returns each pid's current working directory, in one lsof call.
// A pid absent from the map has no known cwd (gone, or another user's).
//
//	-a       AND the selections (pids AND cwd descriptor), not OR
//	-w       no warnings on stderr
//	-d cwd   the cwd descriptor only
//	-Fpn     field output: p=pid, n=path
//
// Why the cwd: a port only says *something* listens. Whether the listener is
// this worktree's server is answered by where the process runs — on
// 2026-10-05 another worktree's server on :3013 was listed, and would have
// been evicted, under the worktree the store gave 3013 to.
func ProcessCwds(pids []int) (map[int]string, error) {
	if len(pids) == 0 {
		return map[int]string{}, nil
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cwdProbeTimeout)
	defer cancel()
	cmd := lsofCwdCommand(ctx, "-a", "-w", "-p", strings.Join(ids, ","), "-d", "cwd", "-Fpn")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("lsof cwd: timed out after %v", cwdProbeTimeout)
	}
	return cwdsResult(out, err)
}

// The cwd probe's command and bound. Vars for tests: a stuck lsof (a hung
// NFS mount, a loaded machine) must fail the probe, never hang a stop.
var (
	lsofCwdCommand = func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "lsof", args...)
	}
	cwdProbeTimeout = 5 * time.Second
)

// cwdsResult reads one invocation. Exit 1 is lsof's "some pid matched
// nothing": what it printed is still an answer. Anything else is a probe that
// did not happen, which a caller must never read as "foreign".
func cwdsResult(out []byte, err error) (map[int]string, error) {
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return nil, fmt.Errorf("lsof cwd: %w", err)
		}
	}
	return parseCwds(out), nil
}

func parseCwds(out []byte) map[int]string {
	got := map[int]string{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			n, err := strconv.Atoi(line[1:])
			if err != nil {
				pid = 0
				continue
			}
			pid = n
		case 'n':
			if pid != 0 {
				got[pid] = line[1:]
			}
		}
	}
	return got
}

// Within reports whether path is dir or below it. Both are resolved through
// symlinks first (lsof reports /private/var for /var on macOS), the longest
// existing ancestor of each when the full path does not exist.
func Within(path, dir string) bool {
	p, d := resolve(path), resolve(dir)
	if p == d {
		return true
	}
	return strings.HasPrefix(p, d+string(filepath.Separator))
}

// resolve resolves the longest existing ancestor of p through symlinks and
// re-appends the rest: a worktree path that does not exist (yet, or any more)
// still compares in the same spelling as one that does — /var/x/gone and
// /private/var/x/gone match on macOS.
func resolve(p string) string {
	p = filepath.Clean(p)
	var tail []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return filepath.Clean(r)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p // nothing resolvable: compare as written
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// SplitByCwd separates the pids running inside dir (owned) from the others
// (foreign, pid → cwd, "" when the cwd is unknown). Owned is sorted.
func SplitByCwd(pids []int, cwds map[int]string, dir string) (owned []int, foreign map[int]string) {
	foreign = map[int]string{}
	for _, p := range pids {
		if c, ok := cwds[p]; ok && Within(c, dir) {
			owned = append(owned, p)
			continue
		}
		foreign[p] = cwds[p]
	}
	sort.Ints(owned)
	return owned, foreign
}

// HolderWorktree names the worktree a foreign cwd belongs to: the first
// directory under containerDir, when the cwd is inside it; else the cwd.
func HolderWorktree(cwd, containerDir string) string {
	if cwd == "" || containerDir == "" || !Within(cwd, containerDir) {
		return cwd
	}
	rel, err := filepath.Rel(resolve(containerDir), resolve(cwd))
	if err != nil || rel == "." {
		return cwd
	}
	return filepath.Join(containerDir, strings.SplitN(rel, string(filepath.Separator), 2)[0])
}
