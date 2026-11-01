package claudesession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// ModeBypass is the permission mode a spawned session must never get.
const ModeBypass = "bypassPermissions"

var (
	permModeRe = regexp.MustCompile(`(?:^|\s)--permission-mode(?:=|\s+)(\S+)`)
	skipPermRe = regexp.MustCompile(`(?:^|\s)--dangerously-skip-permissions(?:\s|$)`)
)

// PermissionMode is the mode a Claude command line was started with: the
// last --permission-mode value, ModeBypass when --dangerously-skip-permissions
// is present (it wins over any mode), "" when the argv names none.
func PermissionMode(args string) string {
	if skipPermRe.MatchString(args) {
		return ModeBypass
	}
	m := permModeRe.FindAllStringSubmatch(args, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// DefaultPermissionMode reads permissions.defaultMode from the cell's
// settings.json. A missing file or key is "" (Claude's own default); an
// unreadable or unparsable file is an error — guessing would be the unsafe side.
func DefaultPermissionMode(configDir string) (string, error) {
	p := filepath.Join(configDir, "settings.json")
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p, err)
	}
	var s struct {
		Permissions struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return "", fmt.Errorf("parse %s: %w", p, err)
	}
	return s.Permissions.DefaultMode, nil
}

// modeRank orders the permission modes from the strictest (plan: nothing
// runs) to the most autonomous one a spawned session may get (auto).
// bypassPermissions is deliberately absent: it is never a valid answer.
var modeRank = map[string]int{"plan": 0, "dontAsk": 1, "default": 2, "acceptEdits": 3, "auto": 4}

// Stricter returns the stricter of two modes; ok=false when either is not a
// known mode (bypassPermissions included).
func Stricter(a, b string) (string, bool) {
	ra, okA := modeRank[a]
	rb, okB := modeRank[b]
	if !okA || !okB {
		return "", false
	}
	if rb < ra {
		return b, true
	}
	return a, true
}

// PidSession is the session id Claude Code recorded for a running pid in
// <configDir>/sessions/<pid>.json.
func PidSession(configDir string, pid int) (string, bool) {
	pf, ok := readPidFile(configDir, pid)
	return pf.SessionID, ok
}
