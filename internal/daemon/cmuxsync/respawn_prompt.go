package cmuxsync

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// promptCursor is where the loop stopped reading one session's transcript
// for one abandoned mark (loop-owned, like busySeen: no lock).
type promptCursor struct {
	since    time.Time // the mark's since: a new mark resets the cursor
	cfg      string    // the config dir the path was resolved in
	path     string    // the transcript; "" = not resolved
	off      int64     // -1 = read the tail
	lookedAt uint64    // slowSeq+1 of the last lookup (0 = never)
	logged   bool      // a read error was logged for this cursor
}

var (
	commandNameRe = regexp.MustCompile(`(?s)<command-name>(.*?)</command-name>`)
	commandArgsRe = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
)

// realPrompt reports whether a typed prompt is the user's own real turn: not
// pigtaild's wake line or /compact, not a turnless command. A slash command
// is logged as <command-name>/open</command-name> tags, so it is rebuilt
// before the turnless check.
func realPrompt(p claudesession.Prompt) bool {
	text := p.Text
	// Only a text that opens with the command tags is a logged slash command:
	// a pasted <command-name> excerpt in the middle of a prompt is a real one.
	head := strings.TrimSpace(text)
	isCommand := strings.HasPrefix(head, "<command-message>") || strings.HasPrefix(head, "<command-name>")
	if m := commandNameRe.FindStringSubmatch(text); isCommand && m != nil {
		text = strings.TrimSpace(m[1])
		if a := commandArgsRe.FindStringSubmatch(p.Text); a != nil {
			text += " " + strings.TrimSpace(a[1])
		}
	}
	text = strings.TrimSpace(text)
	if text == relay.WakeLine || text == relay.CompactLine || waittype.TurnlessPrompt(text) {
		return false
	}
	return text != "" || p.Image
}

// respawnPrompted reads the transcripts of the live sessions on the surface
// of every abandoned mark and returns, by workspace, the since of the mark a
// real prompt stamped after it cleared. A failed or waiting mark is never
// looked at (D4): no cursor, no lookup, no read.
func (r *Runner) respawnPrompted(ss []Session) map[string]time.Time {
	r.mu.Lock()
	marks := map[string]respawnMark{}
	for ws, m := range r.respawn {
		if m.state == pigtail.CmuxRespawnAbandoned {
			marks[ws] = m
		}
	}
	r.mu.Unlock()

	read := r.deps.ReadPrompts
	if read == nil {
		read = claudesession.PromptsAfter
	}
	find := r.findTranscript()
	if r.prompts == nil {
		r.prompts = map[string]promptCursor{}
	}
	out := map[string]time.Time{}
	keep := map[string]bool{}
	for ws, m := range marks {
		for _, s := range ss {
			if !s.Alive || s.WorkspaceID != ws || s.SurfaceID != m.surface || s.ConfigDir == "" {
				continue
			}
			key := ws + "|" + s.SessionID
			keep[key] = true
			c, ok := r.prompts[key]
			if !ok || !c.since.Equal(m.since) || c.cfg != s.ConfigDir {
				c = promptCursor{since: m.since, cfg: s.ConfigDir, off: -1}
			}
			if c.path == "" && c.lookedAt != r.slowSeq+1 {
				c.lookedAt = r.slowSeq + 1
				if p, found := find(s.SessionID, s.ConfigDir); found {
					c.path = p
				}
			}
			if c.path != "" {
				ps, next, err := read(c.path, c.off, m.since)
				switch {
				case errors.Is(err, claudesession.ErrNoTranscript):
					c.path = "" // looked up again at the next slow pass
					c.off = -1  // and read from its tail, like a new cursor
				case err != nil:
					if !c.logged {
						c.logged = true
						r.deps.Logf("cmuxsync: respawn transcript of %s: %v", s.SessionID, err)
					}
				}
				if !errors.Is(err, claudesession.ErrNoTranscript) {
					c.off = next
				}
				for _, p := range ps {
					if realPrompt(p) {
						out[ws] = m.since
						break
					}
				}
			}
			r.prompts[key] = c
		}
	}
	for key := range r.prompts {
		if !keep[key] {
			delete(r.prompts, key)
		}
	}
	return out
}
