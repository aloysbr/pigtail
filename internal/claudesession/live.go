package claudesession

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Live is one running Claude Code process and the session it serves.
type Live struct {
	PID       int
	SessionID string
	Cwd       string
	ConfigDir string // the config dir whose sessions/<pid>.json named it
	Args      string // the process command line
}

// pidFile is the subset of <config-dir>/sessions/<pid>.json read here.
type pidFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	// ParkedJobID is set when Claude Code parked the conversation as a
	// background job: the process keeps running, but its file stays frozen on
	// the old sessionId while the conversation goes on in another process
	// under a new id. Such a file no longer hosts the sessionId it names.
	ParkedJobID string `json:"parkedJobId"`
}

// hostsSession reports whether the pid file still serves the session it
// names: false once the conversation was parked as a background job.
func (pf pidFile) hostsSession() bool { return pf.ParkedJobID == "" }

// LiveSessions lists the Claude processes running right now, each joined with
// the session it serves.
//
// The session id cannot come from argv: a `claude` typed bare in a pane
// carries none. Claude Code writes <config-dir>/sessions/<pid>.json for every
// running interactive session, so the id is read from there, in whichever of
// configDirs holds a file for that pid — which also tells which account the
// process runs on. A process with no such file anywhere is skipped: its
// session cannot be named. A file carrying a parkedJobId is skipped too: the
// conversation it names was parked and now runs in another process, which
// reports it under its new id. A file whose content names another pid is not
// trusted. Pid files of processes no longer running are never consulted,
// since only pids from the live listing are looked up.
//
// It fails when the process listing cannot be obtained: "could not look" must
// not read as "nothing is running".
func LiveSessions(configDirs []string) ([]Live, error) {
	out, err := psRun()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	self := os.Getpid()
	var live []Live
	for _, line := range strings.Split(string(out), "\n") {
		m := psLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid == self {
			continue
		}
		args := strings.TrimSpace(m[2])
		if !claudeProcRe.MatchString(args) {
			continue
		}
		for _, cfg := range configDirs {
			pf, ok := readPidFile(cfg, pid)
			if !ok {
				continue
			}
			if !pf.hostsSession() {
				break // parked: the file names a session this process no longer serves
			}
			live = append(live, Live{PID: pid, SessionID: pf.SessionID, Cwd: pf.Cwd, ConfigDir: cfg, Args: args})
			break
		}
	}
	return live, nil
}

// readPidFile reads <cfg>/sessions/<pid>.json, accepting it only when it
// names that pid and a session.
func readPidFile(cfg string, pid int) (pidFile, bool) {
	b, err := os.ReadFile(filepath.Join(cfg, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return pidFile{}, false
	}
	var pf pidFile
	if err := json.Unmarshal(b, &pf); err != nil {
		return pidFile{}, false
	}
	if pf.PID != pid || pf.SessionID == "" {
		return pidFile{}, false
	}
	return pf, true
}
