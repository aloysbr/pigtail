package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/presence"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// relayPeers refreshes the relay's registry and returns it by session id, for
// `session ls`'s extra columns. nil when the daemon cannot be reached: the
// listing then falls back to its narrow form rather than failing — `session
// ls` must keep working without a daemon, as it did before the relay. c
// speaks for the caller's scope (newScopedClient): the daemon strips the
// subjects that scope may not read. warn receives the presence-marker
// warnings (see warnPresence).
func relayPeers(ctx context.Context, c *pigtail.Client, warn io.Writer) map[string]pigtail.PeerSession {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	self := readSelfEnv(os.Getenv)
	ps, rep := snapshot(self, warn)
	posted := peers.LiveOnly(ps, peers.Alive, self.SessionID)
	got, err := c.PostPeers(ctx, pigtail.PeersRequest{Peers: posted})
	if err != nil {
		return nil
	}
	// The echo comes from the store; a daemon without migration 0012 drops
	// last_worktree. The local scan is at least as fresh: lay it over.
	overlayMarkers(got, rep.Markers)
	out := make(map[string]pigtail.PeerSession, len(got))
	for _, p := range got {
		out[p.SessionID] = p
	}
	// A paused session's peer is ended and no snapshot posts it: its row
	// comes from the full list, so its tab reads paused (sub_x000000000000036).
	if all, err := c.Peers(ctx, true); err == nil {
		mergePausedPeers(out, all)
	}
	markUnposted(out, ps, posted)
	return out
}

// markUnposted adds to byID, as dead, each local row the snapshot did not post
// (its claude exited), so `session ls` keeps showing its tab as dead. A row
// byID already holds (echoed, or paused) is kept.
func markUnposted(byID map[string]pigtail.PeerSession, local, posted []pigtail.PeerSession) {
	sent := make(map[string]bool, len(posted))
	for _, p := range posted {
		sent[p.SessionID] = true
	}
	dead := false
	for _, p := range local {
		if sent[p.SessionID] {
			continue
		}
		if _, ok := byID[p.SessionID]; ok {
			continue
		}
		p.Alive = &dead
		byID[p.SessionID] = p
	}
}

// mergePausedPeers adds to byID the paused peers it does not hold yet; a
// session the snapshot echoed is never replaced.
func mergePausedPeers(byID map[string]pigtail.PeerSession, all []pigtail.PeerSession) {
	for _, p := range all {
		if _, ok := byID[p.SessionID]; ok || p.PausedAt == nil {
			continue
		}
		byID[p.SessionID] = p
	}
}

// overlayMarkers sets a row's last worktree from the local scan when the row
// has none or an older one. A newer stored value (another session's snapshot
// raced ours) is kept.
func overlayMarkers(got []pigtail.PeerSession, ms map[string]presence.Marker) {
	for i := range got {
		m, ok := ms[got[i].SessionID]
		if !ok {
			continue
		}
		if got[i].LastWorktreeAt == nil || m.At.After(*got[i].LastWorktreeAt) {
			at := m.At.UTC()
			got[i].LastWorktree, got[i].LastWorktreeAt = m.Path, &at
		}
	}
}

// dirExists is the real check behind the WORKTREE fallback.
func dirExists(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }

// worktreeCell is the WORKTREE column: the pigtail worktree containing the
// cwd, else the last worktree from the presence markers with its age (no
// TTL: the age is the staleness signal), else "-". A last worktree deleted
// since is not shown.
func worktreeCell(p pigtail.PeerSession, now time.Time, exists func(string) bool) string {
	if p.WorkspaceName != "" {
		return p.WorkspaceName
	}
	if p.LastWorktree != "" && p.LastWorktreeAt != nil && exists(p.LastWorktree) {
		return filepath.Base(p.LastWorktree) + " (" + peers.Age(now.Sub(*p.LastWorktreeAt)) + ")"
	}
	return "-"
}

// peerState is the STATE column: paused (pigtaild stopped it idle, `session
// resume` relaunches it), dead/ended, then Claude's own status, then cmux's
// lifecycle.
func peerState(p pigtail.PeerSession, known bool) string {
	switch {
	case !known:
		return "-"
	case p.PausedAt != nil:
		return "paused"
	case p.EndedAt != nil:
		return "ended"
	case p.Alive != nil && !*p.Alive:
		return "dead"
	case p.Status != "":
		return p.Status
	case p.Lifecycle != "":
		return p.Lifecycle
	}
	return "?"
}

// firstNonEmptyStr returns the first non-empty string.
func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printSessionsWide is printSessions plus the relay's columns. LABEL is the
// name `pigtail msg` shows for a session and accepts as its address — unique
// even for two sessions of one cmux workspace. Sessions the
// relay knows but no cmux pane shows (a claude started outside cmux) follow,
// with PANE "-": they can be messaged too.
func printSessionsWide(w io.Writer, rows []sessionRow, byID map[string]pigtail.PeerSession, now time.Time, exists func(string) bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PANE\tCELL\tSESSION\tLABEL\tSUBJECT\tWORKTREE\tCMUX TITLE\tNAME\tSTATE\tINBOX\tCWD")
	seen := map[string]bool{}
	line := func(pane, cell, sid, cwd string, p pigtail.PeerSession, known bool) {
		inbox := "-"
		if known {
			inbox = strconv.Itoa(p.Inbox)
		}
		label := "-"
		if known {
			label = firstNonEmptyStr(p.Label, peers.Label(p)) // the daemon's: live title
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", pane, dash(cell), sid, label,
			dash(p.SubjectShortTitle), worktreeCell(p, now, exists), dash(cmux.OneLine(p.CmuxTitle, 32)), dash(p.Name),
			peerState(p, known), inbox, cwd)
	}
	for _, r := range rows {
		p, ok := byID[r.SessionID]
		seen[r.SessionID] = true
		line(r.SurfaceRef, r.CellID, r.SessionID, r.Cwd, p, ok)
	}
	var extra []pigtail.PeerSession
	for id, p := range byID {
		// A paused session has no live claude (alive:false) but keeps its tab:
		// it is listed, STATE paused, for `session resume`.
		if !seen[id] && (p.Alive == nil || *p.Alive || p.PausedAt != nil) {
			extra = append(extra, p)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].SessionID < extra[j].SessionID })
	for _, p := range extra {
		line("-", p.CellID, p.SessionID, p.Cwd, p, true)
	}
	if len(rows) == 0 && len(extra) == 0 {
		fmt.Fprintln(w, "no Claude session found")
		return
	}
	tw.Flush()
}
