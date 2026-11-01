package handlers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/handoffdraft"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The automatic handoff (sub_x000000000000028). The Stop hook writes a draft
// per session at every interactive turn end (internal/handoffdraft); pigtaild
// promotes it into a subject_log line of kind handoff-auto when the session
// ends (endPeer), is confirmed gone (sweepHandoffDrafts) or was gone before the
// daemon started. No model: the text is the last summary the draft kept plus
// what the store knows. A manual handoff is never overwritten or masked.

type promoteReason string

const (
	promoteEnd     promoteReason = "end"
	promoteGone    promoteReason = "gone"
	promoteStartup promoteReason = "startup"

	// handoffAutoMax bounds the text of a handoff-auto line.
	handoffAutoMax = 8000
	// handoffAutoHeader opens every automatic handoff.
	handoffAutoHeader = "[automatique — brouillon mécanique, non validé par la session]"
	// handoffAutoFailedKind is the journal kind of a failed promotion.
	handoffAutoFailedKind = "handoff-auto-failed"
)

var prInWaitReason = regexp.MustCompile(`PR #(\d+)`)

// handoffAutoID is the deterministic id of the line of one turn of one session.
func handoffAutoID(session, turn string) string { return "handoff-auto:" + session + ":" + turn }

func (rl *Relay) appendLogOnceRow(e *pigtail.SubjectLogEntry) (bool, error) {
	if rl.appendLogOnce != nil {
		return rl.appendLogOnce(e)
	}
	return rl.svc.Store.AppendLogOnce(e)
}

// lastWaitOf is the session's last wait, declared first: its session_waits
// rows, else the open wait record the done command or an end left to the subject
// (clearTx keeps those open), else the newest closed record.
func (rl *Relay) lastWaitOf(session string) (*pigtail.SessionWait, error) {
	ws, err := rl.svc.Store.SessionWaits(session)
	if err != nil {
		return nil, err
	}
	if len(ws) > 0 {
		pick := ws[0]
		for _, w := range ws {
			if w.Source == pigtail.WaitSourceDeclared {
				pick = w
				break
			}
		}
		return &pick, nil
	}
	f := store.WaitRecordFilter{SessionID: session, Scope: scope.Any, IncludeSnoozed: true}
	open, err := rl.svc.Store.ListOpenWaitRecords(f)
	if err != nil {
		return nil, err
	}
	if len(open) > 0 {
		return waitOfRecord(open[len(open)-1]), nil
	}
	closed, err := rl.svc.Store.ListRecentClosedWaitRecords(f, 1)
	if err != nil {
		return nil, err
	}
	if len(closed) > 0 {
		return waitOfRecord(closed[0]), nil
	}
	return nil, nil
}

// wrappedSince tells whether the session declared "wrapped" at or after t:
// in its session_waits rows, its open wait records or its recent closed ones.
// An end clears the row and, a wrapped being session-owned, closes its record,
// so the closed list is where an ended session's wrap is read. (A wrapped
// declared over an open subject wait leaves no record at all, D30: that case
// cannot be told after the end.)
func (rl *Relay) wrappedSince(session string, t time.Time) (bool, error) {
	ws, err := rl.svc.Store.SessionWaits(session)
	if err != nil {
		return false, err
	}
	for _, w := range ws {
		if w.Type == string(waittype.Wrapped) && !w.Since.Before(t) {
			return true, nil
		}
	}
	f := store.WaitRecordFilter{SessionID: session, Scope: scope.Any, IncludeSnoozed: true}
	open, err := rl.svc.Store.ListOpenWaitRecords(f)
	if err != nil {
		return false, err
	}
	closed, err := rl.svc.Store.ListRecentClosedWaitRecords(f, store.MaxClosedWaitHistory)
	if err != nil {
		return false, err
	}
	for _, r := range append(open, closed...) {
		if r.Type == string(waittype.Wrapped) && !r.OpenedAt.Before(t) {
			return true, nil
		}
	}
	return false, nil
}

func waitOfRecord(r store.WaitRecord) *pigtail.SessionWait {
	return &pigtail.SessionWait{Type: r.Type, Reason: r.Reason, Source: pigtail.WaitSourceDeclared, Since: r.OpenedAt}
}

// promoteHandoffDraft turns the session's draft into a handoff-auto line, or
// drops it when the rules say it would be noise. Every step that fails keeps
// the draft (retried at the next tick) and the error is returned.
func (rl *Relay) promoteHandoffDraft(p pigtail.PeerSession, why promoteReason, now time.Time) error {
	dir := rl.cfg.DraftDir
	if dir == "" {
		return nil
	}
	d, err := handoffdraft.Read(dir, p.SessionID)
	switch {
	case err != nil && os.IsNotExist(err):
		return rl.noteMissingDraft(p, now)
	case err != nil:
		rl.svc.logf("handoff-auto: %s: unreadable draft, moved aside: %v", p.SessionID, err)
		return handoffdraft.MoveAside(dir, p.SessionID)
	}
	drop := func(reason string) error {
		if err := handoffdraft.MarkHandled(dir, p.SessionID, d.TurnID); err != nil {
			rl.svc.logf("handoff-auto: %s: mark handled: %v", p.SessionID, err)
		}
		_, rerr := handoffdraft.RemoveIfTurn(dir, p.SessionID, d.TurnID)
		rl.svc.logf("handoff-auto: %s turn %s: %s", p.SessionID, d.TurnID, reason)
		return rerr
	}
	if p.SubjectID == "" {
		return drop("no subject bound: draft dropped")
	}
	manual, err := rl.svc.Store.LatestLogOfKind(p.SubjectID, pigtail.LogHandoff)
	if err != nil {
		return rl.promoteFailed(p, d, now, err)
	}
	if manual != nil && !manual.Timestamp.Before(d.TurnAt) {
		return drop("manual handoff is newer than the turn: draft dropped")
	}
	sub, err := rl.svc.Store.GetSubject(p.SubjectID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return drop("subject not found: draft dropped")
	case err != nil:
		return rl.promoteFailed(p, d, now, err)
	case pigtail.IsClosedStatus(sub.Status):
		return drop("subject closed: draft dropped")
	}
	wait, err := rl.lastWaitOf(p.SessionID)
	if err != nil {
		return rl.promoteFailed(p, d, now, err)
	}
	if wrapped, err := rl.wrappedSince(p.SessionID, d.TurnAt); err != nil {
		return rl.promoteFailed(p, d, now, err)
	} else if wrapped {
		return drop("session wrapped: draft dropped")
	}
	// The done command (RelayConfig.DoneCommand) closes the session itself:
	// its turn is not promoted. session_last_skill is never cleared, and a
	// typed slash command sets it at UserPromptExpansion, a few hundred ms
	// before the transcript's first promptId entry (d.TurnAt). It counts when
	// set after the previous turn's end; with no earlier turn, within 5 s
	// before d.TurnAt. No done command set: no turn is dropped for its skill.
	since := d.TurnAt.Add(-wrapSkillSlack)
	if prev, ok, err := rl.svc.Store.LatestTurnEndBefore(p.SessionID, d.TurnAt); err != nil {
		return rl.promoteFailed(p, d, now, err)
	} else if ok {
		since = prev
	}
	if skill, at, ok, err := rl.svc.Store.SessionSkillAt(p.SessionID); err != nil {
		return rl.promoteFailed(p, d, now, err)
	} else if done := strings.TrimPrefix(rl.cfg.DoneCommand, "/"); ok && done != "" && skill == done && at.After(since) {
		return drop("turn ran the done command: draft dropped")
	}
	var notes []string
	laterTurn := ""
	// a later turn has ended after this draft's turn began; a turn with no
	// row (pigtaild was down) makes the previous one the latest, not a later one
	if last, lastAt, ok, err := rl.svc.Store.LatestTurnEnd(p.SessionID); err != nil {
		return rl.promoteFailed(p, d, now, err)
	} else if ok && last != d.TurnID && lastAt.After(d.TurnAt) {
		laterTurn = last
		notes = append(notes, "attention: brouillon d'un tour antérieur (le dernier tour, "+last+", n'a pas de brouillon)")
	}
	phase := ""
	if ph, err := rl.svc.Store.LatestLogOfKind(p.SubjectID, pigtail.LogPhase); err != nil {
		return rl.promoteFailed(p, d, now, err)
	} else if ph != nil {
		phase = ph.Text
	}
	e := &pigtail.SubjectLogEntry{
		ID: handoffAutoID(p.SessionID, d.TurnID), SubjectID: p.SubjectID, Timestamp: now,
		Kind: pigtail.LogHandoffAuto, Text: composeHandoffAuto(d, p, wait, phase, why, notes...),
	}
	if _, err := rl.appendLogOnceRow(e); err != nil {
		return rl.promoteFailed(p, d, now, err)
	}
	if err := handoffdraft.MarkHandled(dir, p.SessionID, d.TurnID); err != nil {
		rl.svc.logf("handoff-auto: %s: mark handled: %v", p.SessionID, err)
	}
	_, err = handoffdraft.RemoveIfTurn(dir, p.SessionID, d.TurnID)
	if laterTurn != "" {
		// the hook failed to write the later turn's draft: say so too
		if nerr := rl.noteMissingDraft(p, now); nerr != nil && err == nil {
			err = nerr
		}
	}
	return err
}

// promoteFailed makes a failed promotion visible: a daemon log line at every
// try and one journal row (first writer wins); the draft stays.
func (rl *Relay) promoteFailed(p pigtail.PeerSession, d handoffdraft.Draft, now time.Time, cause error) error {
	rl.svc.logf("handoff-auto: %s turn %s: promotion failed, draft kept: %v", p.SessionID, d.TurnID, cause)
	rl.journalHandoffFailure(p, handoffAutoID(p.SessionID, d.TurnID)+":failed", cause.Error(), now)
	return cause
}

func (rl *Relay) journalHandoffFailure(p pigtail.PeerSession, id, cause string, now time.Time) {
	cause = strings.Join(strings.Fields(cause), " ")
	summary := fmt.Sprintf("automatic handoff of session %s failed: %s", p.SessionID, cause)
	if r := []rune(summary); len(r) > pigtail.JournalSummaryMax {
		summary = string(r[:pigtail.JournalSummaryMax-1]) + "…"
	}
	e := pigtail.JournalEntry{ID: id, Timestamp: now, Kind: handoffAutoFailedKind, Scope: string(sessionScope(p)),
		SubjectID: p.SubjectID, Summary: summary, Source: "pigtail:session/" + p.SessionID}
	if err := e.Validate(); err != nil {
		rl.svc.logf("handoff-auto: %s: journal row invalid: %v", p.SessionID, err)
		return
	}
	if _, err := rl.addJournalRow(&e); err != nil {
		rl.svc.logf("handoff-auto: %s: journal row: %v", p.SessionID, err)
	}
}

// noteMissingDraft is step 4b: no draft for a session with a subject. If its
// last finished turn was neither promoted nor dropped by the daemon, has no
// handoff line and no manual handoff covers it, the draft was lost on the
// hook side: one journal row says so.
func (rl *Relay) noteMissingDraft(p pigtail.PeerSession, now time.Time) error {
	if p.SubjectID == "" {
		return nil
	}
	dir := rl.cfg.DraftDir
	turn, _, ok, err := rl.svc.Store.LatestTurnEnd(p.SessionID)
	if err != nil || !ok {
		return err
	}
	if turn == handoffdraft.ReadHandled(dir, p.SessionID) {
		return nil
	}
	if exists, err := rl.svc.Store.LogExists(handoffAutoID(p.SessionID, turn)); err != nil || exists {
		return err
	}
	sub, err := rl.svc.Store.GetSubject(p.SubjectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if pigtail.IsClosedStatus(sub.Status) {
		return nil
	}
	// Known limit: turn_start is the one the Stop hook read from the transcript
	// tail, so it can be later than the turn's true start; a manual handoff
	// written early in a long turn may then not count here. Low impact.
	start, _, err := rl.svc.Store.TurnEndStart(p.SessionID, turn)
	if err != nil {
		return err
	}
	if wrapped, err := rl.wrappedSince(p.SessionID, start); err != nil {
		return err
	} else if wrapped {
		return nil
	}
	manual, err := rl.svc.Store.LatestLogOfKind(p.SubjectID, pigtail.LogHandoff)
	if err != nil {
		return err
	}
	if manual != nil && !manual.Timestamp.Before(start) {
		return nil
	}
	rl.svc.logf("handoff-auto: %s turn %s: draft missing", p.SessionID, turn)
	rl.journalHandoffFailure(p, handoffAutoID(p.SessionID, turn)+":nodraft", "draft missing for its last turn", now)
	if err := handoffdraft.MarkHandled(dir, p.SessionID, turn); err != nil {
		rl.svc.logf("handoff-auto: %s: mark handled: %v", p.SessionID, err)
	}
	return nil
}

// composeHandoffAuto builds the text of the line: header, a few facts the
// store knows, then the last summary (or the tail of the last message). At most
// handoffAutoMax bytes, valid UTF-8.
func composeHandoffAuto(d handoffdraft.Draft, p pigtail.PeerSession, wait *pigtail.SessionWait, phase string, why promoteReason, notes ...string) string {
	var b strings.Builder
	b.WriteString(handoffAutoHeader + "\n")
	fmt.Fprintf(&b, "session: %s · fin: %s · dernier tour: %s\n", d.SessionID, why, d.TurnAt.UTC().Format(time.RFC3339))
	for _, n := range notes {
		b.WriteString(oneLine(n, 300) + "\n")
	}
	if d.Branch != "" {
		b.WriteString("branche: " + d.Branch + "\n")
	}
	wt := p.LastWorktree
	if wt == "" {
		wt = d.Cwd
	}
	if wt != "" {
		b.WriteString("worktree: " + wt + "\n")
	}
	if wait != nil {
		if m := prInWaitReason.FindStringSubmatch(wait.Reason); m != nil {
			b.WriteString("PR: #" + m[1] + "\n")
		}
		line := wait.Type
		if wait.Reason != "" {
			line += " — " + wait.Reason
		}
		b.WriteString("attente déclarée: " + oneLine(line, 300) + "\n")
	}
	if phase = oneLine(phase, 300); phase != "" {
		b.WriteString("phase: " + phase + "\n")
	}
	if d.TLDR != "" {
		b.WriteString("--- dernier résumé ---\n" + d.TLDR)
	} else if d.Tail != "" {
		b.WriteString("--- fin du dernier message ---\n" + d.Tail)
	}
	out := b.String()
	if len(out) > handoffAutoMax {
		out = out[:handoffAutoMax]
		for !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
	}
	return out
}

// oneLine collapses whitespace and cuts to at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// draftGoneSeen is the first reading that saw a draft's session gone.
type draftGoneSeen struct {
	at      time.Time
	startup bool // seen by the start-up pass
}

// orphanDraftMaxAge: a draft of a session the store never heard of is purged
// after this long (by the file's mtime).
// wrapSkillSlack: how long before a turn's first transcript entry its skill may
// have been set (measured 58-900 ms on a typed slash command).
const wrapSkillSlack = 5 * time.Second

const orphanDraftMaxAge = 30 * 24 * time.Hour

// sweepHandoffDrafts promotes the drafts of sessions that ended or were
// confirmed gone (two readings goneRecipientConfirm apart, as
// rekeepGoneRecipients: one unreadable native file must not promote a live
// session's draft). startup marks the pass Run makes before its loop: a
// "gone" first seen there is promoted with the reason startup.
func (rl *Relay) sweepHandoffDrafts(now time.Time, startup bool) {
	dir := rl.cfg.DraftDir
	if dir == "" {
		return
	}
	if err := handoffdraft.PurgeStale(dir, orphanDraftMaxAge, now); err != nil {
		rl.svc.logf("handoff-auto: purge old .handled/.bad files: %v", err)
	}
	ids, err := handoffdraft.List(dir)
	if err != nil {
		rl.svc.logf("handoff-auto: list drafts: %v", err)
		return
	}
	rl.goneMu.Lock()
	prev := rl.draftGoneSince
	rl.goneMu.Unlock()
	next := make(map[string]draftGoneSeen, len(ids))
	for _, id := range ids {
		p, err := rl.svc.Store.GetPeer(id)
		switch {
		case errors.Is(err, store.ErrNotFound) || (err == nil && p == nil):
			rl.purgeOrphanDraft(id, now)
			continue
		case err != nil:
			rl.svc.logf("handoff-auto: %s: peer: %v", id, err)
			continue
		}
		if p.EndedAt != nil {
			if err := rl.promoteHandoffDraft(*p, promoteEnd, now); err != nil {
				rl.svc.logf("handoff-auto: %s: promote after end: %v", id, err)
			}
			continue
		}
		if !rl.emitterGone(*p) {
			continue // alive: forget any earlier reading
		}
		first, seen := prev[id]
		if !seen {
			next[id] = draftGoneSeen{at: now, startup: startup}
			continue
		}
		if now.Sub(first.at) < goneRecipientConfirm {
			next[id] = first
			continue
		}
		why := promoteGone
		if first.startup {
			why = promoteStartup
		}
		if err := rl.promoteHandoffDraft(*p, why, now); err != nil {
			rl.svc.logf("handoff-auto: %s: promote: %v", id, err)
			next[id] = first // retried at the next tick
		}
	}
	rl.goneMu.Lock()
	rl.draftGoneSince = next
	rl.goneMu.Unlock()
}

func (rl *Relay) purgeOrphanDraft(id string, now time.Time) {
	fi, err := os.Stat(filepath.Join(rl.cfg.DraftDir, id+".json"))
	if err != nil || now.Sub(fi.ModTime()) <= orphanDraftMaxAge {
		return
	}
	if err := handoffdraft.Remove(rl.cfg.DraftDir, id); err != nil {
		rl.svc.logf("handoff-auto: purge draft of unknown session %s: %v", id, err)
		return
	}
	rl.svc.logf("handoff-auto: draft of unknown session %s purged (older than 30 days)", id)
}
