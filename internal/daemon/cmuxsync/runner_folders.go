package cmuxsync

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The folder arrangement in service (Task 6, sub_x000000000000041): the pure
// engine (Arrange, folders.go) is applied one round at a time, only while no
// cmux window is key (D2, focus D11), on every allowed window, each on its own
// (D8 of sub_x000000000000007). Every write goes
// through Deps.Folders (internal/cmux/folders.go), the focus is read again
// before each op, and the first failure ends the round. No session is ever
// closed: only a generated anchor proved bare goes (spec 3, D18 (A)).

// winFolders is one cmux window's arrangement state (D8): each window is
// arranged on its own, with its own folders.
type winFolders struct {
	sig           string
	dirty         bool
	after         time.Time // ArrangeSettle
	slowSeen      time.Time
	createdIDs    map[string]string
	unverified    map[string]string
	createdAnchor map[string]string
	seen          map[string]bool
	unseenRounds  map[string]int
	roundGroups   []cmux.Group
	iconSent      map[string]string
	iconRefused   map[string]bool
	collapseSeen  map[string]bool // last folded state read in this window, per folder
	err           string
	// enter: the workspaces of this window entering their place, not yet
	// seen at the bottom of their band (D16 of sub_x000000000000014): pigtail's
	// adds (FolderNoSort only, D11 (A) of sub_x000000000000030; enterAdd), a
	// band change, an unpin. The arrangement's Enter (enterFor). Per window
	// (D8): another window's round never forgets them.
	enter    map[string]bool
	enterAdd map[string]bool // the enter entries made by an add (the run-under-the-anchor rule)
	// bandSeen is the band last seen for each bound workspace (-1: bound to a
	// closed subject); pinnedSeen whether it was pinned. A change from a known
	// value is an entry.
	bandSeen   map[string]int
	pinnedSeen map[string]bool
	// The hand-drag memory of this window (space_drag.go): each listed
	// workspace's slot and binding identity at the last trusted listing (nil =
	// no listing yet: the next one is the base only), and the slot the engine
	// last wrote for it, pending until a listing shows it. Noted only while
	// the drag is wired (Runner.spaceDragWired).
	spaceBase  map[string]spaceBaseEntry
	spaceWrote map[string]spaceWrite
}

// createUnseenRounds bounds how many rounds a created group may stay absent
// from cmux's lists before the arrangement stops: a list lags at most 1.5 s
// and ArrangeSettle (2 s) separates two re-reads, so a real group shows at the
// first or second; 10 rounds never cut a normal case.
const createUnseenRounds = 10

// winState is win's arrangement state, made on first use.
func (r *Runner) winState(win string) *winFolders {
	if r.wins == nil {
		r.wins = map[string]*winFolders{}
	}
	st := r.wins[win]
	if st == nil {
		st = &winFolders{collapseSeen: map[string]bool{}}
		r.wins[win] = st
	}
	st.createdMaps()
	return st
}

// forgetClosedWindows drops the state of every window no longer listed
// among the allowed ones (D8): a window that comes back starts afresh.
func (r *Runner) forgetClosedWindows() {
	for w := range r.wins {
		if !slices.Contains(r.folderWins, w) {
			delete(r.wins, w)
		}
	}
	r.mu.Lock()
	for w := range r.foldersWait {
		if w != "" && !slices.Contains(r.folderWins, w) {
			delete(r.foldersWait, w) // its round is no longer owed anywhere
		}
	}
	// Apart from foldersWait: unavailable() empties that one and keeps this.
	for w := range r.waitLog {
		if w != "" && !slices.Contains(r.folderWins, w) {
			delete(r.waitLog, w) // its wait is not a wait that applied
		}
	}
	r.mu.Unlock()
}

// arrange runs at the end of a pass, after sync, while Config.Folders is on
// and the listings are trusted. The store is read once for the pass; then
// each allowed window, in listing order, is arranged on its own (D8 of
// sub_x000000000000007) while no cmux window at all is key (focus D11).
func (r *Runner) arrange(now time.Time) {
	r.mu.Lock()
	resume := r.foldersResume
	r.foldersResume = false
	r.mu.Unlock()
	if resume && r.foldersStopped {
		r.foldersStopped = false
		for _, st := range r.wins {
			st.createdIDs, st.unverified, st.seen, st.unseenRounds, st.createdAnchor = nil, nil, nil, nil, nil
			st.dirty = true
		}
		r.forgetSpaceDrags() // tabs moved while stopped are no hand drags
		r.setFoldersView("")
		r.clearWait(now)
	}
	if r.foldersStopped {
		r.noteWait(now, r.folderWins, waitStopped)
		return
	}
	r.loadCollapse()
	if len(r.folderWins) == 0 {
		r.noteWait(now, []string{""}, waitNoWindow)
		return
	}
	r.clearWait(now, "") // a window is listed: the loop's own no-window wait is over
	r.setFoldersView("")
	var ready []string // the windows past their ArrangeSettle
	for _, win := range r.folderWins {
		if !now.Before(r.winState(win).after) {
			ready = append(ready, win)
		} else {
			r.noteWait(now, []string{win}, waitSettle)
		}
	}
	if len(ready) == 0 {
		return
	}
	fv, err := r.readFocus()
	if err != nil {
		r.arrangeFailed(now, ready, "store read", fmt.Errorf("focus: %w", err))
		return
	}
	f, err := r.filing(fv)
	if err != nil {
		r.arrangeFailed(now, ready, "store read", err)
		return
	}
	var owed []string
	for _, win := range ready {
		st := r.wins[win]
		if sig := r.arrangeSignature(win, f, fv, now); sig != st.sig || !r.lastSlow.Equal(st.slowSeen) {
			st.sig, st.slowSeen, st.dirty = sig, r.lastSlow, true
		}
		if st.dirty {
			owed = append(owed, win)
		}
	}
	if len(owed) == 0 {
		return // a wait in progress stays: every slow pass owes a round, which clears it
	}
	if !r.focusAt.IsZero() && now.Sub(r.focusAt) < r.cfg.FocusPoll {
		r.noteWait(now, owed, waitFocusPoll)
		return
	}
	r.focusAt = now
	key, selected := r.cmuxKey()
	if key {
		r.noteWait(now, owed, waitKey) // an unknown window state reads as key too
		return                         // D2, focus D11: while any cmux window has the focus, nothing moves
	}
	wrote := false
	for i, win := range owed {
		if r.foldersStopped {
			r.noteWait(now, r.folderWins, waitStopped)
			return
		}
		var stop string
		wrote, stop = r.arrangeWindow(now, win, r.wins[win], f, fv, selected[win], wrote)
		switch {
		case r.foldersStopped:
			r.noteWait(now, r.folderWins, waitStopped)
			return
		case stop == waitKey:
			r.noteWait(now, owed[i:], waitKey) // the windows left owed wait on the same focus
			return
		case stop != "":
			return
		}
	}
}

// arrangeWindow applies one round to win. wrote says whether an op of this
// pass was already applied (in another window): every op but the pass's first
// waits OpInterval. stop, when not empty, ends the pass and says why: a window
// turned key (waitKey), a write failed (waitWriteError), the folders stopped
// (waitStopped) or the selection moved onto an add (D21 B, "selected").
func (r *Runner) arrangeWindow(now time.Time, win string, st *winFolders, f Filing, fv focusView, selected string, wrote bool) (bool, string) {
	in, err := r.arrangeInput(win, st, f, fv, selected, now)
	if err != nil {
		r.arrangeFailed(now, []string{win}, "round read", err)
		return wrote, ""
	}
	if r.foldersStopped {
		return wrote, waitStopped
	}
	ops := Arrange(in)
	if len(ops) == 0 {
		st.dirty = false
		st.err = ""
		if deferredSelected(in) {
			r.noteWait(now, []string{win}, waitSelectedCollapsed) // filed once no longer selected
		} else {
			r.clearWait(now, win)
		}
		return wrote, ""
	}
	macTargets := map[string]bool{} // workspaces this round files in « Maintenance »
	for ws, f := range in.Target {
		if f == space.MacMaintenance {
			macTargets[ws] = true
		}
	}
	wroteHere := false
	defer func() {
		if wroteHere {
			st.after = r.deps.Now().Add(r.cfg.ArrangeSettle)
		}
	}()
	for i, op := range ops {
		if wrote {
			r.deps.Sleep(r.cfg.OpInterval)
		}
		// D2: the focus is read after the pause, right before the write, for
		// every window (focus D11).
		key, sel := r.cmuxKey()
		if key {
			return wrote, waitKey // the focus came back: the pass ends here, re-read next time
		}
		// The Mac waits are read again after the pause too, never cached across
		// it: a wait served meanwhile is no longer filed (the next pass redoes
		// the round on the real state).
		if wrote && len(macTargets) > 0 && touchesMaintenance(op, macTargets) {
			fresh, err := r.macNeedWorkspaces()
			if err != nil || slices.ContainsFunc(slices.Collect(maps.Keys(macTargets)), func(ws string) bool { return !fresh[ws] }) {
				return wrote, "mac-need-changed"
			}
		}
		if sel[win] != selected && addsWorkspace(ops[i:], sel[win]) {
			return wrote, "selected" // D21 B: never add the newly selected workspace
		}
		wrote, wroteHere = true, true
		if err := r.applyFolderOp(win, op); err != nil {
			r.noteWait(now, []string{win}, waitWriteError)
			r.countError(now) // every failure counts; the log says each distinct one once
			if msg := fmt.Sprintf("%s: %v", op.Kind, err); msg != st.err {
				st.err = msg
				r.deps.Logf("cmuxsync: folders: %s", msg)
			}
			return wrote, waitWriteError
		}
		if r.foldersStopped {
			return wrote, waitStopped
		}
	}
	st.err = "" // a whole round applied: the next failure is news again
	return wrote, ""
}

// touchesMaintenance: op creates « Maintenance » or adds one of its targets.
func touchesMaintenance(op FolderOp, targets map[string]bool) bool {
	switch op.Kind {
	case FolderCreate:
		return op.Name == space.MacMaintenance
	case FolderAdd:
		return targets[op.Workspace]
	}
	return false
}

func addsWorkspace(ops []FolderOp, ws string) bool {
	if ws == "" {
		return false
	}
	for _, op := range ops {
		if op.Kind == FolderAdd && op.Workspace == ws {
			return true
		}
	}
	return false
}

// arrangeFailed skips the round of wins: no folder call, each stays dirty
// and retries after ArrangeSettle; counted once, logged once per distinct
// error (a store read shared by the windows is one line, not one per window).
func (r *Runner) arrangeFailed(now time.Time, wins []string, what string, err error) {
	r.noteWait(now, wins, waitReadError)
	r.countError(now)
	msg := what + ": " + err.Error()
	news := false
	for _, w := range wins {
		st := r.winState(w)
		st.dirty = true
		st.after = now.Add(r.cfg.ArrangeSettle)
		if st.err != msg {
			st.err, news = msg, true
		}
	}
	if news {
		r.deps.Logf("cmuxsync: folders: round skipped, %s", msg)
	}
}

// The reasons a folder round waits (D3 of sub_x000000000000019,
// pigtail.CmuxFoldersWait). settle and focus-poll follow from a wait in
// progress: they name a wait only when they open it, never replace a cause.
const (
	waitKey               = "key"
	waitSettle            = "settle"
	waitFocusPoll         = "focus-poll"
	waitSelectedCollapsed = "selected-collapsed"
	waitListingDown       = "listing-down"
	waitReadError         = "read-error"
	waitWriteError        = "write-error"
	waitStopped           = "stopped"
	waitNoWindow          = "no-window"
)

// waitTrace is the start of a window's owed round for the daemon log
// (FoldersWaitLog); logged is the last cause written. Unlike foldersWait it
// survives unavailable(): a wait cut by pauses of 35-60 s must still reach the
// threshold.
type waitTrace struct {
	since  time.Time
	logged string
}

func waitWindow(w string) string {
	if w == "" {
		return "loop"
	}
	return w
}

// noteWait records why the owed round of each of wins waits (D32 of
// sub_x000000000000007: each window its own wait; "" is the loop's own,
// no-window): the first reason of a window's wait sets its Since; a cause
// (any reason but settle and focus-poll) replaces its reason, never Since; a
// derived gate replaces nothing. A wait past FoldersWaitLog logs once per
// cause. Under mu; the log lines are written after it.
func (r *Runner) noteWait(now time.Time, wins []string, reason string) {
	var lines []string
	r.mu.Lock()
	for _, w := range wins {
		if r.foldersWait == nil {
			r.foldersWait = map[string]*pigtail.CmuxFoldersWait{}
		}
		cur := r.foldersWait[w]
		switch {
		case cur == nil:
			r.foldersWait[w] = &pigtail.CmuxFoldersWait{Window: w, Since: now.UTC(), Reason: reason}
		case reason != waitSettle && reason != waitFocusPoll:
			cur.Reason = reason
		}
		if r.waitLog == nil {
			r.waitLog = map[string]*waitTrace{}
		}
		tr := r.waitLog[w]
		if tr == nil {
			tr = &waitTrace{since: now}
			r.waitLog[w] = tr
		}
		// The effective cause, not the argument: under a key cmux, key and
		// focus-poll alternate from one pass to the next.
		if cause := r.foldersWait[w].Reason; now.Sub(tr.since) >= r.cfg.FoldersWaitLog && cause != tr.logged {
			tr.logged = cause
			lines = append(lines, fmt.Sprintf("cmuxsync: folders: round owed in %s since %s, waiting: %s",
				waitWindow(w), tr.since.UTC().Format("15:04:05"), cause))
		}
	}
	r.mu.Unlock()
	for _, l := range lines {
		r.deps.Logf("%s", l)
	}
}

// clearWait: nothing is owed any more in wins; none named = in any window. A
// wait that had passed FoldersWaitLog logs that it applied.
func (r *Runner) clearWait(now time.Time, wins ...string) {
	var lines []string
	r.mu.Lock()
	if len(wins) == 0 {
		wins = make([]string, 0, len(r.waitLog))
		for w := range r.waitLog {
			wins = append(wins, w)
		}
		slices.Sort(wins)
		r.foldersWait = nil
	}
	for _, w := range wins {
		delete(r.foldersWait, w)
		if tr := r.waitLog[w]; tr != nil {
			if d := now.Sub(tr.since); d >= r.cfg.FoldersWaitLog {
				lines = append(lines, fmt.Sprintf("cmuxsync: folders: round in %s applied after %s (owed since %s)",
					waitWindow(w), d.Round(time.Second), tr.since.UTC().Format("15:04:05")))
			}
			delete(r.waitLog, w)
		}
	}
	r.mu.Unlock()
	for _, l := range lines {
		r.deps.Logf("%s", l)
	}
}

// dirtyWins are the allowed windows whose round is owed.
func (r *Runner) dirtyWins() []string {
	var out []string
	for _, w := range r.folderWins {
		if st := r.wins[w]; st != nil && st.dirty {
			out = append(out, w)
		}
	}
	return out
}

// waitList is State's FoldersWait: the loop's own wait first, then each
// allowed window's in listing order (foldWins). Under mu.
func (r *Runner) waitList() []pigtail.CmuxFoldersWait {
	var out []pigtail.CmuxFoldersWait
	for _, w := range append([]string{""}, r.foldWins...) {
		if fw := r.foldersWait[w]; fw != nil {
			out = append(out, *fw)
		}
	}
	return out
}

// setFoldersView writes what State reads of the arrangement, under mu.
func (r *Runner) setFoldersView(note string) {
	r.mu.Lock()
	changed := r.foldersNote != note
	r.foldersNote = note
	r.mu.Unlock()
	if changed && note != "" {
		r.deps.Logf("cmuxsync: folders: %s", note)
	}
}

// stopFolders stops every folder write until SetEnabled(true) or a restart;
// nothing is undone.
func (r *Runner) stopFolders(note string) {
	r.foldersStopped = true
	r.setFoldersView(note)
}

func (r *Runner) loadCollapse() {
	if r.collapseLoaded {
		return
	}
	r.collapseLoaded = true
	r.collapsedMem = map[string]bool{}
	if r.deps.LoadCollapse == nil {
		return
	}
	m, err := r.deps.LoadCollapse()
	if err != nil {
		r.deps.Logf("cmuxsync: folders: folded state unreadable, folders born expanded: %v", err)
		return
	}
	for k, v := range m {
		r.collapsedMem[k] = v
	}
}

// filing reads what classifies each workspace of the allowed windows: its
// binding (and the folder it wants: its focus, else its theme's space, D1),
// its off-topic declaration (and its space, explicit or inferred from the
// subject its reason cites: D1 of sub_x000000000000019; a focus never takes
// it, a focus files bindings only) and its Standby. A read error is returned,
// never read as « no binding », « no declaration », « no space » or « not in
// Standby ». Young is filled by arrangeInput.
func (r *Runner) filing(fv focusView) (Filing, error) {
	bws, err := r.boundWorkspaces()
	if err != nil {
		return Filing{}, err
	}
	bound := make(map[string]string, len(bws))
	band := map[string]int{}
	for ws, b := range bws {
		bound[ws] = fv.folderOf(b)
		if n, ok := BandOf(b.Status); ok {
			band[ws] = n
		}
	}
	ots, err := r.deps.FolderReads.ListOffTopic()
	if err != nil {
		return Filing{}, fmt.Errorf("list off-topic: %w", err)
	}
	lookup := roundLookup(r.deps.FolderReads, r.knownSpace())
	off, offScope := map[string]string{}, map[string]string{}
	for _, o := range ots {
		ws := strings.ToUpper(o.CmuxWorkspaceID)
		if !r.allowed[ws] {
			continue
		}
		offScope[ws] = o.Scope
		sp, _, err := space.OffTopicSpace(o, lookup)
		if err != nil {
			return Filing{}, fmt.Errorf("off-topic space %s: %w", ws, err)
		}
		if slices.Contains(fv.names, sp) {
			sp = "" // a focus files bindings only: a space named like one is no folder here
		}
		off[ws] = sp
	}
	sb, sbSessions, err := r.standbyWorkspaces()
	if err != nil {
		return Filing{}, err
	}
	mn, err := r.macNeedWorkspaces()
	if err != nil {
		return Filing{}, err
	}
	return Filing{Bound: bound, BoundBy: bws, Band: band, OffTopic: off, OffTopicScope: offScope, Standby: sb, MacNeed: mn,
		Young: map[string]bool{}, standbySessions: sbSessions, dragged: map[string]string{}}, nil
}

// roundLookup reads the subjects and themes an off-topic inference needs,
// each once per round.
func roundLookup(fr folderReads, known func(string) bool) space.Lookup {
	subs := map[string]*pigtail.Subject{}
	themes := map[string]*pigtail.Theme{}
	return space.Lookup{
		Subject: func(id string) (*pigtail.Subject, error) {
			if s, ok := subs[id]; ok {
				return s, nil
			}
			s, err := fr.GetSubject(id)
			if err != nil {
				return nil, err
			}
			subs[id] = s
			return s, nil
		},
		Theme: func(id string) (*pigtail.Theme, error) {
			if t, ok := themes[id]; ok {
				return t, nil
			}
			t, err := fr.GetTheme(id)
			if err != nil {
				return nil, err
			}
			themes[id] = t
			return t, nil
		},
		NotFound:   func(err error) bool { return errors.Is(err, store.ErrNotFound) },
		KnownSpace: func(name, _ string) bool { return known(name) },
	}
}

// standbyWorkspaces reads the Standby table and turns its session ids into
// workspaces (D30), with the sessions in Standby of each (D29 lifts them).
// nil Deps.Standby = none.
func (r *Runner) standbyWorkspaces() (map[string]bool, map[string][]string, error) {
	if r.deps.Standby == nil {
		return map[string]bool{}, nil, nil
	}
	sb, err := r.deps.Standby()
	if err != nil {
		return nil, nil, fmt.Errorf("list standby: %w", err)
	}
	if len(sb) == 0 {
		return map[string]bool{}, nil, nil
	}
	ps, err := r.deps.FolderReads.ListPeers(true)
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}
	out := standbyWorkspaces(ps, sb, r.sessionAlive)
	for ws := range out {
		if !r.allowed[ws] {
			delete(out, ws)
		}
	}
	sessions := map[string][]string{}
	for _, p := range ps {
		if ws := strings.ToUpper(strings.TrimSpace(p.CmuxWorkspaceID)); out[ws] && sb[p.SessionID] {
			sessions[ws] = append(sessions[ws], p.SessionID)
		}
	}
	return out, sessions, nil
}

// liftPinnedStandby is D29 of sub_x000000000000014: a tab in Standby that the
// listing shows pinned (a pin by hand: the CLI unpins before it sets a
// Standby) has the Standby of its sessions ended through Deps.StandbyPinned;
// once all are, it leaves f.Standby for this pass and is filed like any
// pinned tab. A failure is logged, the pass goes on, the tab stays left alone
// until the next pass. ws is the window's listing of this very round, never
// the slow pass's cache: the CLI unpins then sets a Standby, and a cached pin
// would lift the Standby it has just written.
func (r *Runner) liftPinnedStandby(f *Filing, listed []cmux.WorkspaceInfo) {
	if r.deps.StandbyPinned == nil {
		return
	}
	for _, w := range listed {
		ws := strings.ToUpper(w.ID)
		if !w.Pinned || !f.Standby[ws] {
			continue
		}
		lifted := true
		for _, s := range f.standbySessions[ws] {
			if err := r.deps.StandbyPinned(s, ws); err != nil {
				lifted = false
				r.deps.Logf("cmuxsync: folders: standby of %s (workspace %s pinned) not cleared: %v", s, ws, err)
			}
		}
		if lifted {
			delete(f.Standby, ws)
		}
	}
}

// MacNeedSessions is the sessions among waits that hold a Mac wait whose
// reason announced a duration (waittype.MacNeed, contract C4 of the
// maintenance window). A Mac wait without one (no reason, or a command name)
// is not in it.
func MacNeedSessions(waits map[string][]pigtail.SessionWait) map[string]bool {
	out := map[string]bool{}
	for session, ws := range waits {
		for _, w := range ws {
			if w.Source != pigtail.WaitSourceHeld || waittype.Type(w.Type) != waittype.Mac {
				continue
			}
			if _, ok := waittype.MacNeed(w.Reason); ok {
				out[session] = true
			}
		}
	}
	return out
}

// macNeedWorkspaces reads Deps.MacNeed and turns its session ids into the
// workspaces of the allowed windows that have a *live* such session
// (r.sessionAlive, the loop's one judgment: a finished or ghost session files
// nothing). Read at every pass, never cached across a pause. nil Deps.MacNeed
// = none; a read error is returned, never read as « no wait ».
func (r *Runner) macNeedWorkspaces() (map[string]bool, error) {
	if r.deps.MacNeed == nil {
		return map[string]bool{}, nil
	}
	need, err := r.deps.MacNeed()
	if err != nil {
		return nil, fmt.Errorf("list mac waits: %w", err)
	}
	if len(need) == 0 {
		return map[string]bool{}, nil
	}
	ps, err := r.deps.FolderReads.ListPeers(true)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := map[string]bool{}
	for _, p := range ps {
		ws := strings.ToUpper(strings.TrimSpace(p.CmuxWorkspaceID))
		if ws != "" && need[p.SessionID] && r.allowed[ws] && r.sessionAlive(p) {
			out[ws] = true
		}
	}
	return out, nil
}

// standbyWorkspaces: a workspace is in Standby if and only if one of its
// sessions is (alive or finished: during a relaunch the tab stays in
// « Standby », D24) and every one of its live sessions is (D30: a split with
// a live session outside Standby stays in its space). Live is the loop's own
// judgment (Runner.sessionAlive, the one the Dead pill reads), never the bare
// absence of ended_at: `cmux sessions list` keeps a session whose process
// died without SessionEnd, so its row stays unended forever. A finished or
// dead session outside Standby counts for nothing. Keys are upper case.
func standbyWorkspaces(peers []pigtail.PeerSession, standby map[string]bool, live func(pigtail.PeerSession) bool) map[string]bool {
	in, liveOut := map[string]bool{}, map[string]bool{}
	for _, p := range peers {
		ws := strings.ToUpper(strings.TrimSpace(p.CmuxWorkspaceID))
		if ws == "" {
			continue
		}
		switch {
		case standby[p.SessionID]:
			in[ws] = true
		case live(p):
			liveOut[ws] = true
		}
	}
	out := map[string]bool{}
	for ws := range in {
		if !liveOut[ws] {
			out[ws] = true
		}
	}
	return out
}

// boundWorkspaces is what each bound workspace of the allowed windows files
// by: its subject, its theme and the theme's space ("" when it has none). A
// read error is returned, never read as « no theme » or « no subject ».
func (r *Runner) boundWorkspaces() (map[string]boundWS, error) {
	bs, err := r.deps.FolderReads.ListAllCmuxBindings()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	themes := map[string]string{}
	known := r.knownSpace()
	out := map[string]boundWS{}
	for _, b := range bs {
		ws := strings.ToUpper(b.CmuxWorkspaceID)
		if !r.allowed[ws] {
			continue
		}
		bw, err := boundOf(r.deps.FolderReads, b.SubjectID, themes, known)
		if err != nil {
			return nil, err
		}
		out[ws] = bw
	}
	return out, nil
}

// boundOf is what a tab bound to subjectID files by: the subject, its theme,
// the theme's space, the subject's scope and its effective space (its own
// when known names it, else its theme's: effectiveSpace). themes caches
// theme id → space across calls (nil: no cache). A read error is returned,
// never read as « no theme ».
func boundOf(reads folderReads, subjectID string, themes map[string]string, known func(string) bool) (boundWS, error) {
	sub, err := reads.GetSubject(subjectID)
	if err != nil {
		return boundWS{}, fmt.Errorf("subject %s: %w", subjectID, err)
	}
	sp, ok := themes[sub.ThemeID]
	if !ok {
		th, err := reads.GetTheme(sub.ThemeID)
		if err != nil {
			return boundWS{}, fmt.Errorf("theme %s: %w", sub.ThemeID, err)
		}
		sp = th.Space
		if themes != nil {
			themes[sub.ThemeID] = sp
		}
	}
	return boundWS{Subject: subjectID, Theme: sub.ThemeID, Space: effectiveSpace(sub, sp, known), ThemeSpace: sp, Scope: sub.Scope, Status: sub.Status}, nil
}

// knownSpace tells whether name is a space folder of the table: in
// FolderOrder, none of « Inbox », « Maintenance » and « Standby ».
func (r *Runner) knownSpace() func(string) bool { return knownSpaceIn(r.deps.FolderOrder()) }

// knownSpaceIn is knownSpace over a given folder order (FolderOrder's), so
// SubjectFolder judges a subject's own space by the loop's own rule.
func knownSpaceIn(order []string) func(string) bool {
	return func(name string) bool {
		return name != space.NoSubject && name != space.MacMaintenance && name != space.Standby && slices.Contains(order, name)
	}
}

// effectiveSpace is a subject's space (D3, D5 of sub_x000000000000033): its
// own when it names a known space, else its theme's.
func effectiveSpace(sub *pigtail.Subject, themeSpace string, known func(string) bool) string {
	if sub.Space != "" && known(sub.Space) {
		return sub.Space
	}
	return themeSpace
}

// readFocus reads Deps.Focus once and builds the pass's view (D1). A focus
// whose name is a folder of FolderOrder (a space, « Inbox », « Standby ») is
// dropped, logged once per name. A read error is returned, never read as
// « no focus ».
func (r *Runner) readFocus() (focusView, error) {
	var fs []pigtail.Focus
	if r.deps.Focus != nil {
		var err error
		if fs, err = r.deps.Focus(); err != nil {
			return focusView{}, err
		}
	}
	fv, dropped := newFocusView(fs, reservedFolderNames(r.deps.FolderOrder()))
	for _, n := range dropped {
		if r.focusDropped == nil {
			r.focusDropped = map[string]bool{}
		}
		if !r.focusDropped[n] {
			r.focusDropped[n] = true
			r.deps.Logf("cmuxsync: folders: focus %s is named like a folder of the spaces table: not filed", strconv.Quote(n))
		}
	}
	return fv, nil
}

// folderOrder is the pass's folder order: the open focus, oldest first (D4,
// focus D18), then FolderOrder (D6).
func (r *Runner) folderOrder(fv focusView) []string { return fv.passFolders(r.deps.FolderOrder()) }

// arrangeSignature: what makes a round owed in win, read without calling
// cmux; only win's workspaces count.
func (r *Runner) arrangeSignature(win string, f Filing, fv focusView, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "focus|%s\n", strings.Join(fv.names, "\x00"))
	for _, w := range r.workspaces {
		if w.WindowID != win {
			continue
		}
		var class string
		switch sp, bound := f.Bound[w.ID]; {
		case f.Standby[w.ID]:
			class = "s"
		case f.MacNeed[w.ID]:
			class = "m"
		case bound:
			class = "b:" + sp
		case hasOffTopic(f, w.ID):
			class = "o:" + f.OffTopic[w.ID]
		case r.young(w.ID, now): // the end of a grace changes the class: a round is owed
			class = "y"
		default:
			class = "x"
		}
		rank := 0 // sort off (D5-A): the gravity moves nothing, so it owes no round
		if !r.cfg.FolderNoSort {
			rank = r.rankOf(w.ID)
		}
		band := "-" // a status change owes a round (D2)
		if n, ok := f.Band[w.ID]; ok {
			band = strconv.Itoa(n)
		}
		fmt.Fprintf(&b, "%s|%s|%d|%t|%s\n", w.ID, class, rank, w.Pinned, band)
	}
	return b.String()
}

// rankOf is a workspace's gravity: its pill's wait type, no pill last.
func (r *Runner) rankOf(ws string) int {
	d, ok := r.lastDesired[ws]
	if !ok || d.Attention == AttentionNone {
		return len(waittype.Order) + 1
	}
	return waittype.Rank(d.WaitType)
}

// cmuxKey reads whether cmux has the focus and each window's selection (D2,
// focus D11): key when any listed window is, allowed or not; an error or an
// allowed window (folderWins) missing from the listing reads as key: nothing
// moves.
func (r *Runner) cmuxKey() (key bool, selected map[string]string) {
	wins, err := r.deps.Sidebar.ListWindows()
	if err != nil {
		return true, nil
	}
	selected = make(map[string]string, len(wins))
	for _, w := range wins {
		if w.Key {
			key = true
		}
		selected[w.ID] = strings.ToUpper(w.SelectedWorkspaceID)
	}
	for _, w := range r.folderWins {
		if _, ok := selected[w]; !ok {
			return true, nil
		}
	}
	return key, selected
}

// arrangeInput re-reads the window (workspaces, groups) and builds its round.
func (r *Runner) arrangeInput(win string, st *winFolders, f Filing, fv focusView, selected string, now time.Time) (ArrangeInput, error) {
	listStart := r.deps.Now() // before the listing: the drag guard reads from it (3b)
	ws, err := r.deps.Sidebar.ListWorkspaces(win)
	if err != nil {
		return ArrangeInput{}, err
	}
	groups, err := r.deps.Folders.ListGroups(win)
	if err != nil {
		return ArrangeInput{}, err
	}
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].Index < ws[j].Index })
	r.liftPinnedStandby(&f, ws) // on this fresh listing (D29)
	st.roundGroups = groups
	for _, gr := range groups { // a generated anchor has no grace (D18): old once its group is dissolved
		if gr.AnchorGenerated {
			r.markOld(gr.AnchorID)
		}
	}
	st.createdMaps()
	r.verifyCreated(st, groups)
	if r.foldersStopped {
		return ArrangeInput{}, nil
	}
	// A created folder's anchor has no grace either; once it is gone from the
	// listing (dissolved, then closed by the user) it is forgotten, never
	// marked again: markOld would hand the birth clock an id it just dropped.
	listed := make(map[string]bool, len(ws))
	for _, w := range ws {
		listed[strings.ToUpper(w.ID)] = true
	}
	for name, a := range st.createdAnchor {
		switch {
		case listed[strings.ToUpper(a)]:
			r.markOld(a)
		case st.unverified[name] == "": // verified: a created anchor not yet listed is waited for
			delete(st.createdAnchor, name)
		}
	}
	folders := r.folderOrder(fv)
	r.trackCollapse(st, groups, folders)

	hosted, err := r.hostedSet()
	if err != nil {
		return ArrangeInput{}, err
	}
	anchorHosts, closable := map[string]bool{}, map[string]bool{}
	var tree map[string][]string
	for _, gr := range groups {
		if !gr.AnchorGenerated {
			continue
		}
		if hosted[gr.AnchorID] {
			anchorHosts[gr.AnchorID] = true
			continue
		}
		if len(gr.Members) != 1 {
			continue
		}
		if tree == nil {
			if tree, err = r.deps.Sidebar.ListSurfaces(win); err != nil {
				return ArrangeInput{}, err
			}
		}
		ok, err := r.provedBare(gr.AnchorID, hosted, tree)
		if err != nil {
			return ArrangeInput{}, err
		}
		closable[gr.AnchorID] = ok
	}

	order := make([]string, 0, len(ws))
	pinned := map[string]bool{}
	for _, w := range ws {
		order = append(order, w.ID)
		if w.Pinned {
			pinned[w.ID] = true
		}
	}
	r.seeBirths(order, now)
	hold := map[string]bool{}
	for _, id := range order {
		if r.young(id, now) {
			f.Young[id] = true
			hold[id] = true
		}
	}
	icons := map[string]string{}
	if r.deps.FolderIcon != nil {
		for _, f := range folders {
			icons[f] = r.deps.FolderIcon(f)
		}
	}
	collapse := r.standbyCollapse()
	for _, n := range fv.names {
		icons[n] = space.FocusIcon // D5, focus D19
		collapse[n] = false        // focus D19: born expanded, whatever the memory says
	}
	if r.deps.MacNeed != nil { // maintenance window: born expanded, the folder is a live state
		icons[space.MacMaintenance] = space.MacMaintenanceIcon
		collapse[space.MacMaintenance] = false
	}
	r.dropRefusedIcons(st, groups, icons)
	rank := map[string]int{}
	for _, id := range order {
		rank[id] = r.rankOf(id)
	}
	// A hand drag into a space folder writes the space before filing (lot 3 of
	// sub_x000000000000033): the tab is then filed where it was dropped.
	skip := r.applySpaceDrags(st, dragRead{order: order, groups: groups, pinned: pinned, anchorHosts: anchorHosts, folders: folders, listStart: listStart}, f, fv)
	target := folderTargets(order, groups, pinned, anchorHosts, st.pendingAnchors(), f, folders)
	for ws := range skip {
		delete(target, ws) // its write failed: neither filed back nor moved this round
	}
	banded, bound, seen := map[string]bool{}, map[string]bool{}, map[string]int{}
	for _, n := range r.deps.FolderOrder() { // D14: the area folders and « Inbox », never « Standby » nor a focus
		if n != space.Standby && n != space.MacMaintenance { // « Maintenance » has no bands either
			banded[n] = true
		}
	}
	for ws := range f.Bound {
		bound[ws] = true
		seen[ws] = -1
		if n, ok := f.Band[ws]; ok {
			seen[ws] = n
		}
	}
	return ArrangeInput{
		Folders:                    folders,
		Order:                      order,
		Groups:                     groups,
		Target:                     target,
		Last:                       offTopicLast(target, f),
		Icons:                      icons,
		Rank:                       rank,
		Unsorted:                   map[string]bool{space.Standby: true},
		Collapse:                   collapse,
		Pinned:                     pinned,
		Closable:                   closable,
		GroupMoves:                 r.cfg.GroupMoves,
		AddNeedsRemove:             r.cfg.AddNeedsRemove,
		AnchorHosts:                anchorHosts,
		CloseEmptyGeneratedAnchors: CloseEmptyGeneratedAnchors,
		Selected:                   selected,
		Hold:                       hold,
		NoSort:                     r.cfg.FolderNoSort,
		Enter:                      st.enterFor(order, groups, pinned, seen, banded, !r.cfg.FolderNoSort),
		Band:                       f.Band,
		Banded:                     banded,
		Bound:                      bound,
	}, nil
}

// enterFor is ArrangeInput.Enter (D16 of sub_x000000000000014; D11 (A) of
// sub_x000000000000030). band holds each bound workspace's band (-1: bound,
// no band); banded the folders in bands. It sets an entry when the band seen
// changes from a known value (a first band seen — a new tab, a tab just bound
// — sets nothing: Review Focus 5) and when a tab seen pinned is no longer
// pinned, both only in a banded folder (D14: a focus keeps its order);
// pigtail's adds are set by applyFolderOp. Sorted, it keeps nothing: Enter
// is read by the unsorted order only. It forgets an entry gone
// from the window or pinned (D13), seen at the bottom of its band (of its
// folder when it has none: no other non-entering member of the same kind
// below it), or — an add only — seen anywhere but in the run right under its
// group's anchor (the user moved it by hand, his order wins: D5-A). An entry in
// no group yet is kept (a listing may lag its add).
func (st *winFolders) enterFor(order []string, groups []cmux.Group, pinned map[string]bool, band map[string]int, banded map[string]bool, sorted bool) map[string]bool {
	if sorted { // the sort never reads Enter: an entry would never be consumed
		st.enter, st.enterAdd = nil, nil
		return nil
	}
	if st.bandSeen == nil {
		st.bandSeen, st.pinnedSeen = map[string]int{}, map[string]bool{}
	}
	listed := make(map[string]bool, len(order))
	for _, ws := range order {
		listed[ws] = true
	}
	groupOf := map[string]*cmux.Group{}
	for i := range groups {
		gr := &groups[i]
		for _, m := range gr.Members {
			groupOf[m] = gr
		}
	}
	// D14: only a banded folder (an area folder, « Inbox ») reorders on a band
	// change or an unpin; a focus keeps its order.
	bandedHere := func(ws string) bool {
		gr := groupOf[ws]
		return gr != nil && banded[gr.Name]
	}
	mark := func(ws string) {
		if st.enter == nil {
			st.enter = map[string]bool{}
		}
		st.enter[ws] = true
	}
	for _, ws := range order {
		b, isBound := band[ws]
		if prev, ok := st.bandSeen[ws]; ok && isBound && prev != b && bandedHere(ws) {
			mark(ws)
		}
		if isBound {
			st.bandSeen[ws] = b
		} else {
			delete(st.bandSeen, ws)
		}
		if st.pinnedSeen[ws] && !pinned[ws] && bandedHere(ws) {
			mark(ws)
		}
		st.pinnedSeen[ws] = pinned[ws]
	}
	for ws := range st.bandSeen {
		if !listed[ws] {
			delete(st.bandSeen, ws)
		}
	}
	for ws := range st.pinnedSeen {
		if !listed[ws] {
			delete(st.pinnedSeen, ws)
		}
	}
	if len(st.enter) == 0 {
		return nil
	}
	at := make(map[string]int, len(order))
	for i, ws := range order {
		at[ws] = i
	}
	// sameKind: m competes with ws for the bottom of its place — the whole
	// folder when it has no bands or ws has no subject, its band otherwise
	sameKind := func(ws, m string, gr *cmux.Group) bool {
		bw, wb := band[ws]
		if !banded[gr.Name] || !wb {
			return true
		}
		bm, mb := band[m]
		return mb && bm == bw
	}
	// afterAnchor: an added entry is in the run right under its group's
	// anchor (pinned members and other entries only between them)
	afterAnchor := func(ws string, gr *cmux.Group) bool {
		a, ok := at[gr.AnchorID]
		if !ok {
			return false
		}
		for k := a + 1; k < len(order); k++ {
			m := order[k]
			if m == ws {
				return true
			}
			if groupOf[m] != gr || !st.enter[m] && !pinned[m] {
				return false
			}
		}
		return false
	}
	var done []string // judged against the whole set, then forgotten
	for ws := range st.enter {
		i, ok := at[ws]
		if !ok || pinned[ws] {
			done = append(done, ws)
			continue
		}
		gr := groupOf[ws]
		if gr == nil {
			continue
		}
		bottom := true
		for _, m := range gr.Members {
			if j, ok := at[m]; ok && j > i && m != gr.AnchorID && !st.enter[m] && !pinned[m] && sameKind(ws, m, gr) {
				bottom = false
				break
			}
		}
		if bottom || st.enterAdd[ws] && !afterAnchor(ws, gr) {
			done = append(done, ws)
		}
	}
	for _, ws := range done {
		delete(st.enter, ws)
		delete(st.enterAdd, ws)
	}
	if len(st.enter) == 0 {
		return nil
	}
	return maps.Clone(st.enter)
}

// standbyCollapse is the folded memory with « Standby » forced: it is born
// collapsed whatever the memory says (spec 7); collapsedMem itself is left as
// the user set it.
func (r *Runner) standbyCollapse() map[string]bool {
	out := make(map[string]bool, len(r.collapsedMem)+1)
	for k, v := range r.collapsedMem {
		out[k] = v
	}
	out[space.Standby] = true
	return out
}

// dropRefusedIcons stops asking an icon cmux was already sent for that very
// group and does not show: it accepts an unknown SF Symbol and stores none
// (cmux probe P11), so asking again every round would never converge. Asked
// once per group; logged once.
func (r *Runner) dropRefusedIcons(st *winFolders, groups []cmux.Group, icons map[string]string) {
	listed := map[string]bool{}
	for _, gr := range groups {
		listed[gr.ID] = true
		want := icons[gr.Name]
		if sent, ok := st.iconSent[gr.ID]; ok && want != "" && sent == want && gr.Icon != want {
			icons[gr.Name] = ""
			if !st.iconRefused[gr.ID] {
				st.iconRefused[gr.ID] = true
				r.deps.Logf("cmuxsync: folders: cmux shows no icon %q on folder %q: not asked again", want, gr.Name)
			}
		}
	}
	for id := range st.iconSent {
		if !listed[id] {
			delete(st.iconSent, id)
			delete(st.iconRefused, id)
		}
	}
}

// Filing classifies the workspaces for folderTargets (D31, D36); keys are
// workspace ids in upper case.
type Filing struct {
	Bound    map[string]string  // bound workspace → its folder (focus, else subject's own space, else theme's space; D1, D3), "" without one
	BoundBy  map[string]boundWS // bound workspace → what that folder was read from (subject, theme, effective space)
	OffTopic map[string]string  // declared off-topic → its resolved space, "" without one (D1 of sub_x000000000000019)
	Standby  map[string]bool    // every live session in Standby (standbyWorkspaces, D30)
	MacNeed  map[string]bool    // one live session holds a Mac wait with a duration (macNeedWorkspaces, maintenance window)
	Band     map[string]int     // bound workspace → its subject's band (BandOf); absent = closed subject (D15)
	Young    map[string]bool    // inside the birth grace (never a generated anchor)
	// OffTopicScope: declared off-topic → the scope it was declared in
	OffTopicScope map[string]string
	// standbySessions: workspace in Standby → its sessions in Standby (D29)
	standbySessions map[string][]string
	// dragged: subjects whose space a hand drag wrote this pass → the folder
	// it went to (the first listed wins, space_drag.go); shared by every
	// window of the pass
	dragged map[string]string
}

func hasOffTopic(f Filing, ws string) bool { _, ok := f.OffTopic[ws]; return ok }

// spaceFolder is the folder a space (or a focus) files a workspace in (bound
// or off-topic): that folder when it is one of the pass's folders, else
// « Inbox ».
func spaceFolder(sp string, folders []string) string {
	if sp != "" && sp != space.Standby && sp != space.Prio && sp != space.MacMaintenance && slices.Contains(folders, sp) {
		return sp
	}
	return space.NoSubject
}

// folderTargets files each workspace: a bound one in its folder when that
// folder exists — its subject's focus, else its theme's focus, else its
// theme's space (D1, resolved in Filing.Bound) —, « Inbox » otherwise
// (qualified, no grace); a declared off-topic one in its resolved space when
// that space is a folder, « Inbox » otherwise, at once (D1 of
// sub_x000000000000019, amends D36); a young one nowhere
// (it stays where it was born, D31) unless it was born in « Standby », which
// it leaves at once for « Inbox »; every other, its grace over, in
// « Inbox » (D7). A workspace in Standby is filed in « Standby » before
// any other rule, then one with a live session waiting for the Mac with an
// announced duration in « Maintenance » (maintenance window) — Standby >
// Maintenance > bound > off-topic > young > expired —, each when its folder
// is in the pass. A pinned bound workspace is filed like any other (it keeps
// its pin); a pinned unbound one never is. Never filed: a pinned workspace, a pinned group's members
// (D13), a generated anchor unless it hosts a session.
//
// pending holds the anchors of groups created and not listed yet: cmux lists
// them as plain workspaces for a moment, never filed.
func folderTargets(order []string, groups []cmux.Group, pinned, anchorHosts, pending map[string]bool, f Filing, folders []string) map[string]string {
	skip := map[string]bool{}
	for a := range pending {
		skip[a] = true
	}
	for _, gr := range groups {
		if gr.Pinned {
			for _, m := range gr.Members {
				skip[m] = true
			}
			skip[gr.AnchorID] = true
		}
		if gr.AnchorGenerated && !anchorHosts[gr.AnchorID] {
			skip[gr.AnchorID] = true
		}
	}
	inStandbyFolder, inMaintFolder := map[string]bool{}, map[string]bool{}
	for _, gr := range groups {
		in := map[string]map[string]bool{space.Standby: inStandbyFolder, space.MacMaintenance: inMaintFolder}[gr.Name]
		if in == nil {
			continue
		}
		for _, m := range gr.Members {
			if m != gr.AnchorID {
				in[m] = true
			}
		}
	}
	hasStandby := slices.Contains(folders, space.Standby)
	hasMaint := slices.Contains(folders, space.MacMaintenance)
	out := map[string]string{}
	for _, ws := range order {
		if skip[ws] || pinned[ws] && !pinnedFiled(ws, f) {
			continue
		}
		switch sp, bound := f.Bound[ws]; {
		case f.Standby[ws] && hasStandby:
			out[ws] = space.Standby
		case f.MacNeed[ws] && hasMaint:
			out[ws] = space.MacMaintenance
		case bound:
			out[ws] = spaceFolder(sp, folders)
		case hasOffTopic(f, ws):
			out[ws] = spaceFolder(f.OffTopic[ws], folders)
		case f.Young[ws] && inStandbyFolder[ws]:
			out[ws] = space.NoSubject // born in « Standby »: only Standby sessions live there
		case f.Young[ws] && inMaintFolder[ws]:
			out[ws] = space.NoSubject // born in « Maintenance »: only workspaces waiting with a duration live there
		case f.Young[ws]:
			// stays where it was born
		default:
			out[ws] = space.NoSubject
		}
	}
	return out
}

// pinnedFiled: a pinned workspace is filed when bound to a subject (D3 of
// sub_x000000000000014); unbound, it is left alone (D13). A pinned one whose
// session is in Standby stays left alone here: the runner lifts its Standby
// first (D29), then files it.
func pinnedFiled(ws string, f Filing) bool {
	_, bound := f.Bound[ws]
	return bound && !f.Standby[ws]
}

// offTopicLast holds the off-topic workspaces filed in their space (D1 of
// sub_x000000000000019): their target is their resolved space, not « Inbox »,
// nor Standby.
func offTopicLast(target map[string]string, f Filing) map[string]bool {
	out := map[string]bool{}
	for ws, tg := range target {
		if _, bound := f.Bound[ws]; bound || f.Standby[ws] || f.MacNeed[ws] {
			continue
		}
		if sp, ok := f.OffTopic[ws]; ok && sp != "" && sp != space.NoSubject && tg == sp {
			out[ws] = true
		}
	}
	return out
}

// trackCollapse records the folded state of each own folder present in win
// (spec 7); a deleted folder keeps its entry. The memory follows a change
// read in this window only (st.collapseSeen): two windows that fold one
// folder differently never rewrite it at every pass. Saved when it changed.
func (r *Runner) trackCollapse(st *winFolders, groups []cmux.Group, folders []string) {
	changed := false
	done := map[string]bool{}
	for _, gr := range groups {
		if gr.Pinned || !gr.AnchorGenerated || !slices.Contains(folders, gr.Name) || done[gr.Name] {
			continue
		}
		done[gr.Name] = true
		prev, ok := st.collapseSeen[gr.Name]
		st.collapseSeen[gr.Name] = gr.Collapsed
		if ok && prev == gr.Collapsed {
			continue // unchanged in this window
		}
		if v, ok := r.collapsedMem[gr.Name]; !ok || v != gr.Collapsed {
			r.collapsedMem[gr.Name] = gr.Collapsed
			changed = true
		}
	}
	if changed && r.deps.SaveCollapse != nil {
		if err := r.deps.SaveCollapse(r.collapsedMem); err != nil {
			r.deps.Logf("cmuxsync: folders: save folded state: %v", err)
		}
	}
}

// hostedSet is every cmux workspace a registry row (alive or not) or a
// subject link names. The registry's cmux workspace is read, never its
// pigtail worktree. An error is returned, never « hosted » nor « not hosted ».
func (r *Runner) hostedSet() (map[string]bool, error) {
	ps, err := r.deps.FolderReads.ListPeers(true)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	bs, err := r.deps.FolderReads.ListAllCmuxBindings()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	out := map[string]bool{}
	for _, p := range ps {
		if id := strings.ToUpper(strings.TrimSpace(p.CmuxWorkspaceID)); id != "" {
			out[id] = true
		}
	}
	for _, b := range bs {
		out[strings.ToUpper(b.CmuxWorkspaceID)] = true
	}
	return out, nil
}

// anchorHostsSession reports whether a session or a link names the anchor.
func (r *Runner) anchorHostsSession(anchor string) (bool, error) {
	hosted, err := r.hostedSet()
	if err != nil {
		return false, err
	}
	return hosted[strings.ToUpper(anchor)], nil
}

// closable reports an anchor proved bare, read now (spec 3): no registry
// row, no link, exactly one surface, its shell at its prompt or the surface
// never rendered (D22). Any doubt or error is false.
func (r *Runner) closable(win, anchor string) bool {
	hosted, err := r.hostedSet()
	if err != nil {
		return false
	}
	tree, err := r.deps.Sidebar.ListSurfaces(win)
	if err != nil {
		return false
	}
	ok, err := r.provedBare(anchor, hosted, tree)
	return err == nil && ok
}

func (r *Runner) provedBare(anchor string, hosted map[string]bool, tree map[string][]string) (bool, error) {
	anchor = strings.ToUpper(anchor)
	if hosted[anchor] || r.deps.ShellAtPrompt == nil {
		return false, nil
	}
	var surfaces []string
	for id, s := range tree {
		if strings.ToUpper(id) == anchor {
			surfaces = s
		}
	}
	if len(surfaces) != 1 {
		return false, nil
	}
	// D22: a surface cmux never rendered has no shell to prove; it counts as
	// bare. Read here, so again right before the close (closable): one
	// started since the round's read falls back to the shell proof, and an
	// unreadable state is no proof.
	if r.deps.SurfaceNeverRendered != nil {
		if never, err := r.deps.SurfaceNeverRendered(surfaces[0]); err == nil && never {
			return true, nil
		}
	}
	ok, err := r.deps.ShellAtPrompt(surfaces[0])
	if err != nil {
		return false, nil // an unreadable pane is not bare; the round goes on
	}
	return ok, nil
}

func (st *winFolders) anchorOf(groupID string) string {
	for _, gr := range st.roundGroups {
		if gr.ID == groupID {
			return gr.AnchorID
		}
	}
	return ""
}

// verifyCreated checks the groups created earlier against the round's list
// (a2): present and wrong stops; present and right is done; absent is never
// conclusive, until createUnseenRounds rounds.
func (r *Runner) verifyCreated(st *winFolders, groups []cmux.Group) {
	st.createdMaps()
	for name, id := range st.unverified {
		var got *cmux.Group
		for i := range groups {
			if groups[i].ID == id {
				got = &groups[i]
			}
		}
		if got == nil {
			st.unseenRounds[name]++
			if st.unseenRounds[name] >= createUnseenRounds {
				r.stopFolders("folder created but never listed by cmux: folders stopped")
				return
			}
			continue
		}
		st.seen[name] = true
		delete(st.unseenRounds, name)
		if got.Name != name {
			r.stopFolders(fmt.Sprintf("folder %s read back differs from %s: folders stopped", strconv.Quote(got.Name), strconv.Quote(name)))
			return
		}
		if !got.AnchorGenerated {
			r.stopFolders("created group has no generated anchor: folders stopped")
			return
		}
		delete(st.unverified, name)
	}
}

// mayCreate bounds the creates of a folder name: a group created for it
// still listed but never recognised stops; one created and not listed yet
// is waited for, never created twice.
func (st *winFolders) mayCreate(name string) (create, stop bool) {
	id := st.createdIDs[name]
	if id == "" {
		return true, false
	}
	for _, gr := range st.roundGroups {
		if gr.ID == id {
			return false, true
		}
	}
	if st.unverified[name] == id && !st.seen[name] {
		return false, false
	}
	st.forgetName(name)
	return true, false
}

// pendingAnchors are the anchors of the groups created and not verified yet.
func (st *winFolders) pendingAnchors() map[string]bool {
	out := map[string]bool{}
	for name := range st.unverified {
		if a := st.createdAnchor[name]; a != "" {
			out[strings.ToUpper(a)] = true
		}
	}
	return out
}

// createdMaps makes the create bookkeeping (reset to nil by a resume).
func (st *winFolders) createdMaps() {
	if st.createdIDs == nil {
		st.createdIDs, st.unverified = map[string]string{}, map[string]string{}
		st.seen, st.unseenRounds = map[string]bool{}, map[string]int{}
		st.createdAnchor = map[string]string{}
	}
	if st.iconSent == nil {
		st.iconSent, st.iconRefused = map[string]string{}, map[string]bool{}
	}
	if st.collapseSeen == nil {
		st.collapseSeen = map[string]bool{}
	}
}

// forgetCreated forgets the folder whose created group is groupID.
func (st *winFolders) forgetCreated(groupID string) {
	for name, id := range st.createdIDs {
		if id == groupID {
			st.forgetName(name)
		}
	}
}

func (st *winFolders) forgetName(name string) {
	delete(st.createdIDs, name)
	delete(st.unverified, name)
	delete(st.seen, name)
	delete(st.unseenRounds, name)
	delete(st.createdAnchor, name)
}

// applyFolderOp applies one op to win, with win's own bookkeeping (D8).
func (r *Runner) applyFolderOp(win string, op FolderOp) (err error) {
	st := r.winState(win)
	defer func() { // the engine's own slot write, never read as a hand drag (space_drag.go)
		if err == nil && r.spaceDragWired() {
			st.noteSpaceWrite(op, r.deps.Now())
		}
	}()
	f := r.deps.Folders
	switch op.Kind {
	case FolderCreate:
		create, stop := st.mayCreate(op.Name)
		if stop { // previous group of this name still listed, never recognised
			r.stopFolders(fmt.Sprintf("second create of %s while the first group is still unrecognised: folders stopped", strconv.Quote(op.Name)))
			return nil
		}
		if !create { // created, not listed yet: wait for verifyCreated, never a second create
			return nil
		}
		g, err := f.CreateGroup(win, op.Name)
		if err != nil {
			return err
		}
		st.createdIDs[op.Name] = g.ID
		st.unverified[op.Name] = g.ID // (a2): verified next round, from the list
		st.createdAnchor[op.Name] = g.AnchorID
		delete(st.seen, op.Name) // a new ID has not been seen yet
		delete(st.unseenRounds, op.Name)
		if g.Name != "" && g.Name != op.Name { // (a1): only the create output itself, never a list read now
			r.stopFolders(fmt.Sprintf("folder %s read back differs from %s: folders stopped", strconv.Quote(g.Name), strconv.Quote(op.Name)))
			return nil // nothing undone, no icon, no collapse
		}
		// D6: cmux creates at the top (cmux probe P11); the folder goes to its
		// fixed place now, not rounds later at the reorder, which waits for
		// cmux not to be key. Same op, so the same pre-write focus check.
		ref := op.Ref
		if op.RefFolder != "" {
			ref = st.createdIDs[op.RefFolder]
		}
		// A refused move never costs the birth icon and collapse: nothing
		// re-collapses an existing folder (only the D12 toggle does). Its
		// error is still returned, so the round counts and logs it.
		var moveErr error
		if ref != "" && ref != g.ID {
			moveErr = f.MoveGroup(win, g.ID, ref, op.Before)
		}
		if op.Icon != "" {
			err = f.SetIcon(win, g.ID, op.Icon)
			st.iconSent[g.ID] = op.Icon
		}
		if err == nil && op.Collapse {
			err = f.Collapse(win, g.ID)
		}
		if moveErr != nil {
			return moveErr
		}
		return err
	case FolderAdd:
		if err := f.AddToGroup(win, op.Group, op.Workspace); err != nil {
			return err
		}
		if r.cfg.FolderNoSort { // D11 (A): cmux may leave it right after the anchor (P19)
			if st.enter == nil {
				st.enter, st.enterAdd = map[string]bool{}, map[string]bool{}
			}
			if st.enterAdd == nil {
				st.enterAdd = map[string]bool{}
			}
			st.enter[op.Workspace], st.enterAdd[op.Workspace] = true, true
		}
		return nil
	case FolderRemove: // P2 (b) only; the workspace stays open
		return f.RemoveFromGroup(win, op.Workspace)
	case FolderSetIcon:
		st.iconSent[op.Group] = op.Icon
		return f.SetIcon(win, op.Group, op.Icon)
	case FolderRename: // lot 6: the former no-subject folder, in place (P20)
		return f.RenameGroup(win, op.Group, op.Name)
	case FolderDissolve:
		// spec 3: re-proved now; an anchor the user opened since the read stays
		return f.Ungroup(win, op.Group, op.RemoveAnchor && r.closable(win, st.anchorOf(op.Group)))
	case FolderDelete:
		st.forgetCreated(op.Group) // a deleted folder is simply recreated if needed
		return f.Ungroup(win, op.Group, r.closable(win, st.anchorOf(op.Group)))
	case FolderMove:
		return f.MoveGroup(win, op.Group, op.Ref, op.Before)
	case FolderReorder:
		return f.Reorder(win, op.Order)
	case FolderPlace: // NoSort only (D39): one workspace, within its own folder
		return f.PlaceWorkspace(win, op.Workspace, op.Ref, op.Before)
	case FolderCollapse: // the D12 toggle only (ToggleFolders)
		return f.Collapse(win, op.Group)
	case FolderExpand:
		return f.Expand(win, op.Group)
	}
	return fmt.Errorf("cmuxsync: unknown folder op %q", op.Kind)
}

// forgetSpaceDrags drops every window's drag memory (spaceBase, spaceWrote):
// the next trusted listing is the new base, so a tab moved while the folders
// were stopped or the loop was off is no hand drag.
func (r *Runner) forgetSpaceDrags() {
	for _, st := range r.wins {
		st.spaceBase, st.spaceWrote = nil, nil
	}
}
