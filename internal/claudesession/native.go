package claudesession

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Native is the richer view of <config-dir>/sessions/<pid>.json used by the
// session-message relay: on top of what readPidFile checks, the session's
// native name, status and kind (interactive or not).
//
// Status is busy|idle|waiting. WaitingFor, set while waiting, is Claude's
// loose hint of what for ("input needed", "dialog open", "sandbox request",
// "goal proposal"…; spec C10) — a fallback, not a typed wait. StatusUpdatedAt
// is when Status last changed (epoch ms). A partial file without a sessionId
// (e.g. `{"peerToken":…}`) is never returned: readPidFile rejects it.
type Native struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
	StartedAt       int64  `json:"startedAt"` // when this process started or resumed the session (ms)
	Kind            string `json:"kind"`
	ParkedJobID     string `json:"parkedJobId"` // set once the conversation was parked as a background job
	UpdatedAt       int64  `json:"updatedAt"`
	ConfigDir       string `json:"-"`
}

// ReadNative reads <cfg>/sessions/<pid>.json, accepting it only when it names
// that pid and a session — the same trust rule as readPidFile.
func ReadNative(cfg string, pid int) (Native, bool) {
	if _, ok := readPidFile(cfg, pid); !ok {
		return Native{}, false
	}
	b, err := os.ReadFile(filepath.Join(cfg, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return Native{}, false
	}
	var n Native
	if err := json.Unmarshal(b, &n); err != nil {
		return Native{}, false
	}
	n.ConfigDir = cfg
	return n, true
}

// FindNative scans <cfg>/sessions/*.json for the file naming sessionID and
// returns it. Among several (a stale file left by a crashed process next to
// the live one), a file whose pid is alive wins.
func FindNative(cfg, sessionID string) (Native, bool) {
	if sessionID == "" {
		return Native{}, false
	}
	entries, err := os.ReadDir(filepath.Join(cfg, "sessions"))
	if err != nil {
		return Native{}, false
	}
	var fallback Native
	found := false
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		n, ok := ReadNative(cfg, pid)
		if !ok || n.SessionID != sessionID {
			continue
		}
		if PidAlive(pid) {
			return n, true
		}
		fallback, found = n, true
	}
	return fallback, found
}

// PidAlive reports whether a process with this pid exists (kill -0). EPERM
// counts as alive: the process exists, it just is not ours.
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// SessionAlive reports whether the claude process pid is running AND its
// sessions/<pid>.json in cfg still names sessionID. Both halves matter: a pid
// is recycled by the OS, and a claude process that /resumed another session
// rewrites its file. A file carrying a parkedJobId no longer hosts its
// sessionId (the conversation moved to a background-job process under a new
// id), so it reads as not alive. Never call this with a Bash-tool pid — those are
// setsid-detached and die with each command.
func SessionAlive(cfg string, pid int, sessionID string) bool {
	if !PidAlive(pid) {
		return false
	}
	pf, ok := readPidFile(cfg, pid)
	return ok && pf.SessionID == sessionID && pf.hostsSession()
}
