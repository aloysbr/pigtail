package cmuxsync

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// nativeCache re-reads a session's native file (<config-dir>/sessions/<pid>.json)
// only when its mtime or size moved: the loop polls every second, and a stat
// is all a quiet session costs (spec §3.2).
type nativeCache struct {
	read    func(cfg string, pid int) (claudesession.Native, bool)
	entries map[string]nativeEntry
}

type nativeEntry struct {
	mod  time.Time
	size int64
	n    claudesession.Native
	ok   bool
	seen bool // touched during the current sweep
}

func newNativeCache(read func(string, int) (claudesession.Native, bool)) *nativeCache {
	if read == nil {
		read = claudesession.ReadNative
	}
	return &nativeCache{read: read, entries: map[string]nativeEntry{}}
}

func (c *nativeCache) get(cfg string, pid int) (claudesession.Native, bool) {
	if cfg == "" || pid <= 0 {
		return claudesession.Native{}, false
	}
	path := filepath.Join(cfg, "sessions", strconv.Itoa(pid)+".json")
	st, err := os.Stat(path)
	if err != nil {
		delete(c.entries, path)
		return claudesession.Native{}, false
	}
	if e, ok := c.entries[path]; ok && e.mod.Equal(st.ModTime()) && e.size == st.Size() {
		e.seen = true
		c.entries[path] = e
		return e.n, e.ok
	}
	n, ok := c.read(cfg, pid)
	c.entries[path] = nativeEntry{mod: st.ModTime(), size: st.Size(), n: n, ok: ok, seen: true}
	return n, ok
}

// sweep forgets the files no session asked for since the last sweep.
func (c *nativeCache) sweep() {
	for k, e := range c.entries {
		if !e.seen {
			delete(c.entries, k)
			continue
		}
		e.seen = false
		c.entries[k] = e
	}
}

// turnStatus reads Claude Code's native status the way the loop judges it.
// "shell" is Claude's own idle with a background shell still alive: its
// source writes status = (idle && a live local_bash task) ? "shell" : status
// (claude 2.1.293, and 2.1.172 in the same form). A Bash command running
// mid-turn is written "busy", never "shell" — so "shell" always means the
// session waits for its user, and counts as "idle" (the waits, the pill,
// the claude_code row fix). Session.BusySeen (D24) is set by a "busy" the
// loop saw, no longer by a "shell". The earlier reading, "shell = a Bash-tool
// command in the turn" (D7 of the usage subject), was a wrong premise.
func turnStatus(status string) string {
	if status == "shell" {
		return "idle"
	}
	return status
}
