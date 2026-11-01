package claudesession

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// psTreeRun lists every process as "pid ppid command"; a seam for tests.
var psTreeRun = defaultPsTree

func defaultPsTree() ([]byte, error) {
	return exec.Command("ps", "-axwwo", "pid=,ppid=,command=").Output()
}

// bashSnapshotMarker is in the command line of every shell Claude Code's Bash
// tool runs: `/bin/zsh -c source <cfg>/shell-snapshots/snapshot-zsh-….sh …`.
const bashSnapshotMarker = "/shell-snapshots/snapshot-"

// BashToolShells lists the pids of Bash-tool shells whose parent is pid
// (command contains "/shell-snapshots/snapshot-"), from one `ps -axo
// pid=,ppid=,command=` listing. A listing failure is an error, never "none".
func BashToolShells(pid int) ([]int, error) {
	out, err := psTreeRun()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	var shells []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		p, err1 := strconv.Atoi(f[0])
		pp, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || pp != pid {
			continue
		}
		if strings.Contains(strings.Join(f[2:], " "), bashSnapshotMarker) {
			shells = append(shells, p)
		}
	}
	return shells, nil
}

// psAgeRun lists every process as "pid ppid etime command", full width
// (-ww: the Bash tool's command sits at the end of a long line); a seam for
// tests.
var psAgeRun = defaultPsAge

func defaultPsAge() ([]byte, error) {
	return exec.Command("ps", "-axww", "-o", "pid=,ppid=,etime=,command=").Output()
}

// ShellInfo is one Bash-tool shell of a Claude process: its pid, how long it
// has run, and the command the agent gave it (the eval'd text when the line
// has the tool's shape, else the whole command line).
type ShellInfo struct {
	PID     int
	Age     time.Duration
	Command string
}

// BashToolShellInfo is BashToolShells with each shell's age and command,
// from one `ps -axww -o pid=,ppid=,etime=,command=` listing. A listing
// failure is an error, never "none"; an unreadable etime reads as age 0.
func BashToolShellInfo(pid int) ([]ShellInfo, error) {
	out, err := psAgeRun()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	var shells []ShellInfo
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		p, err1 := strconv.Atoi(f[0])
		pp, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || pp != pid {
			continue
		}
		cmd := strings.Join(f[3:], " ")
		if !strings.Contains(cmd, bashSnapshotMarker) {
			continue
		}
		age, _ := parseEtime(f[2])
		shells = append(shells, ShellInfo{PID: p, Age: age, Command: bashToolCommand(cmd)})
	}
	return shells, nil
}

// parseEtime reads ps's elapsed time, [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	days := 0
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days, s = d, s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var hms [3]int
	off := 3 - len(parts)
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		hms[off+i] = v
	}
	return time.Duration(days)*24*time.Hour + time.Duration(hms[0])*time.Hour +
		time.Duration(hms[1])*time.Minute + time.Duration(hms[2])*time.Second, true
}

// bashToolCommand extracts the agent's command from a Bash-tool shell line:
// the one shell word after `eval `, unquoted as the shell would. A ' inside
// the command is written one of two ways:
//
//	'"'"'   Claude Code (measured 2026-10-13)
//	'\''    other quoters
//
// Both are concatenated quoted segments, so a small word reader undoes
// both — single-quoted text verbatim, double-quoted text with \" \\ \$ \`
// undone, a backslash outside quotes escaping one byte; an unquoted blank
// ends the word. A line cut inside a quote keeps what it has;
// a line without `eval '` is returned whole.
func bashToolCommand(cmdline string) string {
	i := strings.Index(cmdline, "eval '")
	if i < 0 {
		return cmdline
	}
	s := cmdline[i+len("eval "):]
	var b strings.Builder
	for j := 0; j < len(s); {
		switch c := s[j]; {
		case c == '\'':
			k := strings.IndexByte(s[j+1:], '\'')
			if k < 0 { // cut line: the rest is the command
				b.WriteString(s[j+1:])
				return b.String()
			}
			b.WriteString(s[j+1 : j+1+k])
			j += k + 2
		case c == '"':
			j++
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' && j+1 < len(s) && strings.IndexByte("\"\\$`", s[j+1]) >= 0 {
					j++
				}
				b.WriteByte(s[j])
				j++
			}
			j++ // the closing quote, or past the end of a cut line
		case c == '\\' && j+1 < len(s):
			b.WriteByte(s[j+1])
			j += 2
		case c == ' ' || c == '\t':
			return b.String()
		default:
			b.WriteByte(c)
			j++
		}
	}
	return b.String()
}
