// Package peers derives the registry of Claude Code sessions the
// session-message relay can address, and resolves a human address to one of
// them.
//
// The registry is derived, never declared: it joins cmux's session list
// (`cmux sessions list --agent claude --json`), Claude's own
// <config-dir>/sessions/<pid>.json, the cell table (account + scope) and the
// cmux workspace titles. Everything here is a pure function over those inputs
// so it can be tested on recorded JSON; the exec calls live in internal/cmux
// and the CLI.
package peers

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/presence"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// CmuxSession is the subset of one `cmux sessions list --json` entry read here.
type CmuxSession struct {
	SessionID              string `json:"session_id"`
	WorkspaceID            string `json:"workspace_id"`
	SurfaceID              string `json:"surface_id"`
	Cwd                    string `json:"cwd"`
	SessionDir             string `json:"session_dir"` // the config dir (profile)
	PID                    int    `json:"pid"`
	Lifecycle              string `json:"agent_lifecycle"`
	Agent                  string `json:"agent"`
	ActiveForSurface       bool   `json:"active_for_surface"`        // cmux: this session is the surface's active one
	ActiveSurfaceSessionID string `json:"active_surface_session_id"` // the session cmux sees active on SurfaceID
}

// ParseCmuxSessions decodes `cmux sessions list --agent claude --json`.
func ParseCmuxSessions(b []byte) ([]CmuxSession, error) {
	var env struct {
		Sessions []CmuxSession `json:"sessions"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("decode cmux sessions: %w", err)
	}
	out := env.Sessions[:0]
	for _, s := range env.Sessions {
		if s.SessionID == "" || (s.Agent != "" && s.Agent != "claude") {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

var workspaceLineRe = regexp.MustCompile(`^\s*\*?\s*workspace:\d+\s+([0-9A-Fa-f-]{36})\s+(.*?)\s*$`)

// ParseWorkspaceTitles decodes `cmux workspace list --id-format both` into
// workspace uuid → title, with the trailing "[selected]" marker removed.
func ParseWorkspaceTitles(out string) map[string]string {
	titles := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		m := workspaceLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		title := strings.TrimSpace(strings.TrimSuffix(m[2], "[selected]"))
		titles[strings.ToUpper(m[1])] = title
	}
	return titles
}

// CleanTitle drops the status glyph cmux prefixes a workspace title with
// ("✳ ", "◑ ") so it reads and matches as text.
func CleanTitle(t string) string {
	return pigtail.CleanCmuxTitle(t)
}

// Inputs gathers what Build joins. NativeByPID and NativeBySession default to
// the real sessions/<pid>.json readers.
type Inputs struct {
	Cmux   []CmuxSession
	Titles map[string]string
	Cells  *claudeprofile.Table
	// Live lists running claude processes (claudesession.LiveSessions), to
	// include sessions cmux does not host. May be nil.
	Live            []claudesession.Live
	NativeByPID     func(cfg string, pid int) (claudesession.Native, bool)
	NativeBySession func(cfg, sessionID string) (claudesession.Native, bool)
	Now             time.Time
}

// Build joins the inputs into peer rows, one per session id. cmux rows win on
// cmux ids; the native file wins on pid, name, status and cwd.
func Build(in Inputs) []pigtail.PeerSession {
	if in.NativeByPID == nil {
		in.NativeByPID = claudesession.ReadNative
	}
	if in.NativeBySession == nil {
		in.NativeBySession = claudesession.FindNative
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	byID := map[string]*pigtail.PeerSession{}
	var order []string
	get := func(id string) *pigtail.PeerSession {
		if p, ok := byID[id]; ok {
			return p
		}
		p := &pigtail.PeerSession{SessionID: id, Source: "snapshot", UpdatedAt: in.Now}
		byID[id] = p
		order = append(order, id)
		return p
	}
	for _, c := range in.Cmux {
		p := get(c.SessionID)
		p.ConfigDir = firstNonEmpty(c.SessionDir, p.ConfigDir)
		p.Cwd = firstNonEmpty(c.Cwd, p.Cwd)
		p.CmuxWorkspaceID = c.WorkspaceID
		p.CmuxSurfaceID = c.SurfaceID
		p.Lifecycle = c.Lifecycle
		p.PID = c.PID
		if t, ok := in.Titles[strings.ToUpper(c.WorkspaceID)]; ok {
			p.CmuxTitle = pigtail.ShownTitle(t, "", false)
		}
	}
	for _, l := range in.Live {
		p := get(l.SessionID)
		p.ConfigDir = firstNonEmpty(p.ConfigDir, l.ConfigDir)
		p.Cwd = firstNonEmpty(p.Cwd, l.Cwd)
		if p.PID == 0 {
			p.PID = l.PID
		}
	}
	for _, id := range order {
		p := byID[id]
		var n claudesession.Native
		ok := false
		if p.ConfigDir != "" && p.PID > 0 {
			n, ok = in.NativeByPID(p.ConfigDir, p.PID)
			ok = ok && n.SessionID == p.SessionID
		}
		if !ok && p.ConfigDir != "" {
			n, ok = in.NativeBySession(p.ConfigDir, p.SessionID)
		}
		if ok {
			p.PID = n.PID
			p.Name = n.Name
			p.Status = n.Status
			p.Cwd = firstNonEmpty(n.Cwd, p.Cwd)
		}
		if c, ok := in.Cells.ByConfigDir(p.ConfigDir); ok {
			p.CellID, p.Scope = c.ID, c.Scope
		}
	}
	out := make([]pigtail.PeerSession, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

// maxPrimary bounds the readable half of a label. A cmux title is the usual
// long one; it is truncated, never the handle that follows it.
const maxPrimary = 32

// DisplayName is the session's whole name (pigtail.SessionDisplayName): its
// subject's short title when its cmux workspace is bound, else the title cmux
// shows; for a session cmux does not show, its subject's short title,
// worktree, native name or id prefix.
func DisplayName(p pigtail.PeerSession) string {
	if n := cmuxName(p); n != "" {
		return n
	}
	return firstNonEmpty(p.SubjectShortTitle, p.WorkspaceName, p.Name, shortID(p.SessionID))
}

// cmuxName is the name a session's cmux workspace gives it, "" outside cmux
// (no title known): the part of DisplayName the "name" address level reads —
// the fallbacks (worktree, native name, id prefix) keep their own levels.
func cmuxName(p pigtail.PeerSession) string {
	return pigtail.SessionDisplayName(pigtail.DisplayNameInput{
		Shown: p.CmuxTitle, ShortTitle: p.SubjectShortTitle, Bound: p.CmuxBound,
	})
}

// Label is the human name of a peer, unique among live sessions and itself an
// address: "<primary> · <handle> (<cell>)", e.g.
// "Relais de messages · globex-d4 (mt)".
//
//   - primary: the session's name (DisplayName, D3/D5) — the subject's short
//     title of a bound cmux workspace, else the title cmux shows, else the
//     subject's short title, worktree, native name or a session-id prefix —
//     truncated to maxPrimary runes. The subject slug is no longer shown,
//     but stays an address (ResolveLevel "subject", matchLabel).
//   - handle: Claude's native session name (sessions/<pid>.json "name"), else
//     an 8-char session-id prefix. Always present unless it would repeat the
//     primary: two sessions in one cmux workspace (or one worktree) share
//     their primary, and a label that depends on who else is live would
//     change under a message already sent. Both handle forms resolve alone.
//   - cell, when known.
func Label(p pigtail.PeerSession) string {
	name := DisplayName(p)
	handle := handleOf(p)
	l := truncate(name, maxPrimary)
	if handle != "" && !strings.EqualFold(handle, name) {
		l += " · " + handle
	}
	if p.CellID != "" {
		l += " (" + p.CellID + ")"
	}
	return l
}

// handleOf is the disambiguating half of a label.
func handleOf(p pigtail.PeerSession) string {
	return firstNonEmpty(p.Name, shortID(p.SessionID))
}

// labelRe splits an address written in label form: "<primary> · <handle>
// (<cell>)", each of the handle and the cell optional.
var labelRe = regexp.MustCompile(`^(.*?)(?:\s*·\s*([^()·]+?))?(?:\s*\(([A-Za-z0-9_-]+)\))?\s*$`)

// matchLabel reports whether addr designates p in label form: the exact
// label, or a hand-written "<primary> · <handle>", "<primary> (<cell>)" or
// both, where primary may be truncated ("…") or any part of the title.
// Without a handle or a cell it matches nothing: plain words go to the
// other levels.
func matchLabel(p pigtail.PeerSession, addr string) bool {
	if strings.EqualFold(addr, Label(p)) {
		return true
	}
	m := labelRe.FindStringSubmatch(addr)
	if m == nil || (m[2] == "" && m[3] == "") {
		return false
	}
	primary := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), "…"))
	handle, cell := strings.TrimSpace(m[2]), m[3]
	if cell != "" && !strings.EqualFold(cell, p.CellID) {
		return false
	}
	if handle != "" && !strings.EqualFold(handle, p.Name) &&
		(len(handle) < 4 || !strings.HasPrefix(p.SessionID, strings.ToLower(handle))) {
		return false
	}
	if primary == "" {
		return handle != ""
	}
	lp := strings.ToLower(primary)
	if hasFoldPrefix(p.SubjectSlug, lp) || hasFoldPrefix(p.WorkspaceName, lp) ||
		strings.EqualFold(primary, p.Name) || strings.HasPrefix(p.SessionID, lp) ||
		(p.CmuxTitle != "" && strings.Contains(strings.ToLower(p.CmuxTitle), lp)) ||
		hasFoldPrefix(p.SubjectShortTitle, lp) || hasFoldPrefix(DisplayName(p), strings.TrimSuffix(lp, "…")) {
		return true
	}
	// A label written before the name changed (edited short title, the slug
	// era): a strong handle still designates the session.
	return handle != "" && (strings.EqualFold(handle, p.Name) ||
		(len(handle) >= 8 && strings.HasPrefix(p.SessionID, strings.ToLower(handle))))
}

// ErrAmbiguous is returned by Resolve when an address matches several peers.
type ErrAmbiguous struct {
	Address    string
	Level      string
	Candidates []pigtail.PeerSession
}

func (e *ErrAmbiguous) Error() string {
	names := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		names = append(names, Label(c)+" ["+shortID(c.SessionID)+"]")
	}
	sort.Strings(names)
	return fmt.Sprintf("address %q matches %d sessions by %s: %s — address one by its label, its session name or a session-id prefix", e.Address, len(e.Candidates), e.Level, strings.Join(names, ", "))
}

// ErrUnresolved is returned by Resolve when nothing matches.
type ErrUnresolved struct{ Address string }

func (e *ErrUnresolved) Error() string {
	return fmt.Sprintf("no live session matches %q (see `pigtail session ls`)", e.Address)
}

// LevelLastWorktree is the addressing level fed by presence markers.
const LevelLastWorktree = "last worktree"

// AttachMarkers sets each peer's last worktree from the presence markers
// (presence.Scan), by session id — gone ones included, so the store learns a
// session left its previous worktree; consumers hide a deleted path. Peers
// without a marker are left untouched.
func AttachMarkers(ps []pigtail.PeerSession, ms map[string]presence.Marker) {
	for i := range ps {
		if m, ok := ms[ps[i].SessionID]; ok {
			at := m.At.UTC()
			ps[i].LastWorktree, ps[i].LastWorktreeAt = m.Path, &at
		}
	}
}

// Age renders how old a marker is: "<1m", "12m", "3h", "2d".
func Age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// Resolve is ResolveLevel without the level.
func Resolve(peers []pigtail.PeerSession, addr string) (pigtail.PeerSession, error) {
	p, _, err := ResolveLevel(peers, addr)
	return p, err
}

// ResolveLevel finds the one peer an address designates, and the level that
// matched. Levels are tried in order, and the first level with any match
// decides: exactly one match resolves, several fail as ambiguous (never
// "pick the first").
//
//  1. full session id
//  2. label form (see Label, matchLabel): "<primary> · <handle> (<cell>)"
//  3. subject slug or sub_ id
//     3b. session name (the name cmux gives it — DisplayName without its
//     fallbacks —, exact, or the subject's short title)
//  4. pigtail worktree name
//  5. last worktree — basename of the newest presence marker, exact, only
//     for a session without a cwd-derived worktree
//  6. Claude's native session name
//  7. cmux workspace title (case-insensitive substring)
//  8. session-id prefix (≥ 4 chars)
//
// The native name comes before the title: it is exact and per session,
// where a title is a substring shared by every session of a workspace.
func ResolveLevel(peers []pigtail.PeerSession, addr string) (pigtail.PeerSession, string, error) {
	a := strings.TrimSpace(addr)
	if a == "" {
		return pigtail.PeerSession{}, "", &ErrUnresolved{Address: addr}
	}
	la := strings.ToLower(a)
	levels := []struct {
		name  string
		match func(p pigtail.PeerSession) bool
	}{
		{"session id", func(p pigtail.PeerSession) bool { return p.SessionID == a }},
		{"label", func(p pigtail.PeerSession) bool { return matchLabel(p, a) }},
		{"subject", func(p pigtail.PeerSession) bool {
			return (p.SubjectID != "" && p.SubjectID == a) || (p.SubjectSlug != "" && strings.EqualFold(p.SubjectSlug, a))
		}},
		{"name", func(p pigtail.PeerSession) bool {
			n := cmuxName(p)
			return (n != "" && strings.EqualFold(n, a)) ||
				(p.SubjectShortTitle != "" && strings.EqualFold(p.SubjectShortTitle, a))
		}},
		{"worktree", func(p pigtail.PeerSession) bool {
			return p.WorkspaceName != "" && strings.EqualFold(p.WorkspaceName, a)
		}},
		{LevelLastWorktree, func(p pigtail.PeerSession) bool {
			return p.WorkspaceName == "" && p.LastWorktree != "" &&
				strings.EqualFold(filepath.Base(p.LastWorktree), a)
		}},
		{"session name", func(p pigtail.PeerSession) bool { return p.Name != "" && strings.EqualFold(p.Name, a) }},
		{"cmux title", func(p pigtail.PeerSession) bool {
			return p.CmuxTitle != "" && strings.Contains(strings.ToLower(p.CmuxTitle), la)
		}},
		{"session-id prefix", func(p pigtail.PeerSession) bool { return len(a) >= 4 && strings.HasPrefix(p.SessionID, la) }},
	}
	for _, lv := range levels {
		var hits []pigtail.PeerSession
		for _, p := range peers {
			if lv.match(p) {
				hits = append(hits, p)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], lv.name, nil
		default:
			return pigtail.PeerSession{}, "", &ErrAmbiguous{Address: a, Level: lv.name, Candidates: hits}
		}
	}
	return pigtail.PeerSession{}, "", &ErrUnresolved{Address: a}
}

// Alive reports whether a peer's claude process is still running and still
// serving that session. A peer with no pid or config dir cannot be checked
// and is reported alive unless it was marked ended — the relay must not drop
// a message for want of a pid a SessionStart hook could not know yet.
func Alive(p pigtail.PeerSession) bool {
	if p.EndedAt != nil {
		return false
	}
	if p.PID <= 0 || p.ConfigDir == "" {
		return true
	}
	return claudesession.SessionAlive(p.ConfigDir, p.PID, p.SessionID)
}

// LiveOnly keeps the rows alive(p) accepts, plus the row whose session id is
// keep (the caller: its native file may not exist yet). Order is kept.
func LiveOnly(ps []pigtail.PeerSession, alive func(pigtail.PeerSession) bool, keep string) []pigtail.PeerSession {
	out := make([]pigtail.PeerSession, 0, len(ps))
	for _, p := range ps {
		if alive(p) || (keep != "" && p.SessionID == keep) {
			out = append(out, p)
		}
	}
	return out
}

// hasFoldPrefix reports whether s starts with the lower-case prefix lp — a
// label truncates its primary, so a pasted slug may be a prefix of the real one.
func hasFoldPrefix(s, lp string) bool {
	return s != "" && strings.HasPrefix(strings.ToLower(s), lp)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRightFunc(string(r[:n-1]), unicode.IsSpace) + "…"
}
