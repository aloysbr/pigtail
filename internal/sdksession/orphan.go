package sdksession

import (
	"strings"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// claudeWriter classifies one claude pid recorded in host.json once the host is
// dead. writer: the pid is a live claude of this session (or cannot be told
// apart from it) and may still write to the transcript. proven: its identity is
// established, so it may be killed. A live pid that is not a claude process (a
// recycled pid), or whose native file names another cwd or session, is neither.
func (r *Registry) claudeWriter(m Meta, h HostFile, pid int, procs []claudesession.Proc) (writer, proven bool) {
	if !r.alive(pid) {
		return false, false
	}
	var proc *claudesession.Proc
	for i := range procs {
		if procs[i].PID == pid {
			proc = &procs[i]
			break
		}
	}
	if proc == nil {
		return false, false
	}
	if n, ok := r.native(m.ConfigDir, pid); ok {
		if n.Cwd != "" && n.Cwd != m.Cwd {
			return false, false
		}
		if h.ClaudeSessionID != "" && n.SessionID != h.ClaudeSessionID {
			return false, false
		}
		if h.ClaudeSessionID != "" {
			return true, true
		}
	}
	for _, id := range []string{m.SessionUUID, h.ClaudeSessionID} {
		if id != "" && strings.Contains(proc.Args, "--session-id "+id) {
			return true, true
		}
	}
	return true, false
}

// shellQuote leaves a plain word as is and single-quotes anything else.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:@,", r)
		return !safe
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// resumeCommand is the terminal fallback; "" when there is no session id to resume.
func resumeCommand(m Meta, claudeSessionID string) string {
	id := claudeSessionID
	if id == "" {
		id = m.SessionUUID
	}
	if id == "" {
		return ""
	}
	return "cd " + shellQuote(m.Cwd) + " && CLAUDE_CONFIG_DIR=" + shellQuote(m.ConfigDir) + " claude --resume " + shellQuote(id)
}
