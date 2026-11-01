package cmuxcheck

import (
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/aloysbr/pigtail/internal/cmux"
)

var shellCommRe = regexp.MustCompile(`^-?(?:/bin/)?(zsh|bash|fish|sh)$`)

// psAll lists every process with its parent, its group, its terminal and
// that terminal's foreground group (D22); a seam for tests (Deps.PS).
var psAll = func() (string, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,tpgid=,tty=,comm=").Output()
	return string(out), err
}

type psProc struct {
	pid, ppid, pgid, tpgid int
	tty, comm              string
}

func (p psProc) shell() bool    { return p.pid == p.pgid && shellCommRe.MatchString(p.comm) }
func (p psProc) login() bool    { return path.Base(strings.TrimPrefix(p.comm, "-")) == "login" }
func (p psProc) atPrompt() bool { return p.tpgid > 0 && p.tpgid == p.pgid }

func parsePS(out string) []psProc {
	var ps []psProc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		var n [4]int
		ok := true
		for i := range n {
			v, err := strconv.Atoi(f[i])
			if err != nil {
				ok = false
				break
			}
			n[i] = v
		}
		if !ok {
			continue
		}
		ps = append(ps, psProc{pid: n[0], ppid: n[1], pgid: n[2], tpgid: n[3],
			tty: strings.TrimPrefix(f[4], "/dev/"), comm: strings.Join(f[5:], " ")})
	}
	return ps
}

// shellIdle reports whether the shell of a surface sits at its prompt: its
// terminal's foreground group is its own (knowledge cmux-and-sessions,
// "Sibling surfaces"). The shell is found by shellOfSurface (D22).
func shellIdle(d Deps, surfaceID string, cmuxPID int) (bool, error) {
	out, err := d.PS()
	if err != nil {
		return false, fmt.Errorf("process listing: %w", err)
	}
	return shellIdleIn(d, parsePS(out), surfaceID, cmuxPID)
}

func shellIdleIn(d Deps, ps []psProc, surfaceID string, cmuxPID int) (bool, error) {
	sh, err := shellOfSurface(ps, surfaceID, cmuxPID, d.Environ, d.SurfaceTTYs)
	if err != nil {
		return false, err
	}
	return sh.atPrompt(), nil
}

func (p psProc) cmuxApp() bool {
	return path.Base(p.comm) == "cmux" && strings.Contains(p.comm, ".app/Contents/MacOS/")
}

// runningCmuxPID is the pid of the running cmux, for the shell lookup. The
// sentinel is trusted only when its pid is a live cmux process: a short-lived
// second instance can overwrite it with a pid that is gone (2026-10-23). Else
// the process table decides: exactly one cmux app process, or 0 (unknown) —
// never a guess among several.
func runningCmuxPID(ps []psProc, sentinelPID int) int {
	found := 0
	for _, p := range ps {
		if !p.cmuxApp() {
			continue
		}
		if p.pid == sentinelPID {
			return sentinelPID
		}
		if found != 0 {
			found = -1
		} else if found == 0 {
			found = p.pid
		}
	}
	if found < 0 {
		return 0
	}
	return found
}

// ShellAtPrompt reports whether a surface's shell sits at its prompt, with
// the real seams and the running cmux's pid (its sentinel): the D22 proof the
// folder arrangement of the sidebar loop needs before closing a generated
// anchor (spec 3, sub_x000000000000041). An error is returned as is, never
// read as « at its prompt ».
func ShellAtPrompt(surfaceID string) (bool, error) {
	return shellAtPrompt(DefaultDeps(), DefaultSentinel(), surfaceID)
}

func shellAtPrompt(d Deps, sentinel, surfaceID string) (bool, error) {
	pid, err := ReadSentinel(sentinel)
	if err != nil {
		pid = 0 // unknown: the process table may still name the running cmux
	}
	out, err := d.PS()
	if err != nil {
		return false, fmt.Errorf("process listing: %w", err)
	}
	ps := parsePS(out)
	return shellIdleIn(d, ps, surfaceID, runningCmuxPID(ps, pid))
}

// shellOfSurface ties a surface to its pane's shell (D22).
//
//  1. The shell's own environment, when readable, names the surface
//     (CMUX_SURFACE_ID): that shell, as before (Linux, non-login shells).
//  2. Else — cmux execs every pane shell through /usr/bin/login, whose
//     environment macOS does not show (KERN_PROCARGS2 nenv 0, 41/41 shells
//     measured 2026-10-09) — the surface's tty, from cmux tree only, and
//     only when no other surface claims it (F9: cmux duplicates ttys).
//  3. On that tty, exactly one process-group-leading shell whose parent is a
//     login whose parent is the running cmux (cmuxPID): a stale shell
//     (ppid 1) or one of an older cmux never qualifies.
//  4. Refused when a process under that login has a readable environment
//     naming another surface; such a process is never required.
//
// Anything else is an error: "pane state unreadable", never a guess.
func shellOfSurface(ps []psProc, surfaceID string, cmuxPID int, environ func(int) (string, error), ttys func() (map[string]string, error)) (psProc, error) {
	var found []psProc
	for _, p := range ps {
		if !p.shell() {
			continue
		}
		env, err := environ(p.pid)
		if err != nil {
			continue // gone since the listing
		}
		if s, _ := cmux.EnvValue(env, cmux.EnvSurfaceID); s == "" || !strings.EqualFold(s, surfaceID) {
			continue
		}
		found = append(found, p)
	}
	if len(found) > 1 { // a login shell is the pane's own; keep those
		var login []psProc
		for _, sh := range found {
			if strings.HasPrefix(sh.comm, "-") {
				login = append(login, sh)
			}
		}
		found = login
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return shellByTTY(ps, surfaceID, cmuxPID, environ, ttys)
	}
	return psProc{}, fmt.Errorf("%d shells claim surface %s: which one is the pane's would be a guess", len(found), surfaceID)
}

// shellByTTY is steps 2–4 of shellOfSurface.
func shellByTTY(ps []psProc, surfaceID string, cmuxPID int, environ func(int) (string, error), ttys func() (map[string]string, error)) (psProc, error) {
	if cmuxPID <= 0 {
		return psProc{}, fmt.Errorf("no shell of surface %s found by its environment, and the running cmux pid is unknown", surfaceID)
	}
	all, err := ttys()
	if err != nil {
		return psProc{}, fmt.Errorf("no shell of surface %s found by its environment; %w", surfaceID, err)
	}
	tty := all[strings.ToUpper(surfaceID)]
	if tty == "" {
		return psProc{}, fmt.Errorf("no shell of surface %s found by its environment, and cmux tree gives it no tty", surfaceID)
	}
	others := 0
	for s, t := range all {
		if t == tty && !strings.EqualFold(s, surfaceID) {
			others++
		}
	}
	if others > 0 {
		return psProc{}, fmt.Errorf("cmux tree gives tty %s to surface %s and to %d other surface(s) (F9): which pane it is would be a guess", tty, surfaceID, others)
	}
	byPID := map[int]psProc{}
	for _, p := range ps {
		byPID[p.pid] = p
	}
	var found []psProc
	for _, p := range ps {
		if p.tty != tty || !p.shell() {
			continue
		}
		if l, ok := byPID[p.ppid]; ok && l.login() && l.ppid == cmuxPID {
			found = append(found, p)
		}
	}
	switch {
	case len(found) == 0:
		return psProc{}, fmt.Errorf("no shell on %s (surface %s) under a login of the running cmux (pid %d)", tty, surfaceID, cmuxPID)
	case len(found) > 1:
		return psProc{}, fmt.Errorf("%d shells on %s under a login of the running cmux: which one is the pane's would be a guess", len(found), tty)
	}
	sh := found[0]
	login := sh.ppid
	for _, p := range ps {
		if !under(byPID, p.pid, login) {
			continue
		}
		env, err := environ(p.pid)
		if err != nil {
			continue
		}
		if s, ok := cmux.EnvValue(env, cmux.EnvSurfaceID); ok && s != "" && !strings.EqualFold(s, surfaceID) {
			return psProc{}, fmt.Errorf("the shell on %s runs process %d of surface %s: tty and environment disagree", tty, p.pid, s)
		}
	}
	return sh, nil
}

// under reports whether pid descends from (or is) root, following ppid.
func under(byPID map[int]psProc, pid, root int) bool {
	for i := 0; i < 64 && pid > 1; i++ {
		if pid == root {
			return true
		}
		p, ok := byPID[pid]
		if !ok {
			return false
		}
		pid = p.ppid
	}
	return false
}
