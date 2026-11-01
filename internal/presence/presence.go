// Package presence reads the per-worktree presence markers that
// the user's session-presence hook writes, to tell which worktree a
// Claude session last worked in when its launch cwd says nothing (sessions
// start at the client root and only enter a worktree in their Bash shells).
//
// Contract, owned by the presence hook script (`mark`):
//
//	$TMPDIR/claude-wt-<key8>-<session_id>[.<agent_id>]
//	line 1: absolute worktree path; line 2 (optional): name=<addressable name>
//	mtime:  the session's last git command / write in that worktree
//
// pigtail only reads them: no TTL (the age is shown instead), no write, no
// delete. The name on line 2 is ignored — it is frozen when written and goes
// stale on --resume; the live name comes from sessions/<pid>.json.
package presence

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Prefix is the marker file-name prefix.
const Prefix = "claude-wt-"

// Marker is where and when a session last worked. Gone: that directory no
// longer exists — the marker is still reported, so a consumer learns the
// session left its previous worktree, and must hide it.
type Marker struct {
	Path string
	At   time.Time
	Gone bool
}

// Report is the outcome of one Scan.
//
// Malformed counts claude-wt-* files that do not follow the contract;
// NewestMalformed/NewestValid date the newest of each, so a caller can tell
// leftover junk (older than the newest valid marker) from a format change in
// the presence hook script (the hook writing files that no longer parse). Gone
// counts sessions whose newest marker names a deleted directory.
type Report struct {
	Markers         map[string]Marker // by session id (subagent suffix folded)
	Malformed       int
	Gone            int
	NewestValid     time.Time
	NewestMalformed time.Time
}

var nameRe = regexp.MustCompile(`^claude-wt-[0-9a-f]{8}-([A-Za-z0-9-]+)(\.[A-Za-z0-9_-]+)?$`)

// Dir is where the hooks write: ${TMPDIR:-/tmp}, exactly as the hook does.
func Dir(getenv func(string) string) string {
	if d := getenv("TMPDIR"); d != "" {
		return d
	}
	return "/tmp"
}

// Scan keeps, per session, the newest marker (by mtime) and reads only that
// file's first line — $TMPDIR holds tens of thousands of entries, a few
// hundred of them markers. Exception: a subagent marker (<sid>.<agent>)
// whose worktree no longer exists is skipped for the next newest. A missing dir is an empty report; any other read
// error is returned.
func Scan(dir string) (Report, error) {
	r := Report{Markers: map[string]Marker{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return r, nil
		}
		return r, err
	}
	type cand struct {
		file     string
		at       time.Time
		subagent bool
	}
	bySession := map[string][]cand{}
	malformed := func(at time.Time) {
		r.Malformed++
		if at.After(r.NewestMalformed) {
			r.NewestMalformed = at
		}
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, Prefix) || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // vanished between ReadDir and Info
		}
		m := nameRe.FindStringSubmatch(name)
		if m == nil {
			malformed(info.ModTime())
			continue
		}
		bySession[m[1]] = append(bySession[m[1]], cand{file: name, at: info.ModTime(), subagent: m[2] != ""})
	}
	for sid, cs := range bySession {
		sort.Slice(cs, func(i, j int) bool { return cs[i].at.After(cs[j].at) })
		// Newest first. Usually one read: only a subagent marker whose worktree
		// is gone sends us to the next one — a subagent in an isolated worktree
		// cleaned up since must not mask its parent session's worktree. A gone
		// marker of the session itself is kept (flagged): it means the session
		// left its previous worktree.
		for _, c := range cs {
			path, ok := firstLine(filepath.Join(dir, c.file))
			if !ok || !filepath.IsAbs(path) {
				malformed(c.at)
				break
			}
			mk := Marker{Path: filepath.Clean(path), At: c.at}
			if st, err := os.Stat(mk.Path); err != nil || !st.IsDir() {
				if c.subagent {
					continue
				}
				mk.Gone = true
				r.Gone++
			}
			r.Markers[sid] = mk
			if c.at.After(r.NewestValid) {
				r.NewestValid = c.at
			}
			break
		}
	}
	return r, nil
}

func firstLine(p string) (string, bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 4096)
	if !sc.Scan() {
		return "", false
	}
	return strings.TrimSpace(sc.Text()), true
}
