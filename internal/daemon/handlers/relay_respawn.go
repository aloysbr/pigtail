package handlers

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RespawnNotifier tells the session of a respawn run that gave up or failed
// (subject « abandon respawn visible », D1/D2). The relay implements it; nil
// in Services means no notice (the handler logs it and says so).
type RespawnNotifier interface {
	NotifyRespawnEnd(caller scope.Scope, cross bool, wsID string, req pigtail.CmuxRespawnRequest) pigtail.CmuxRespawnNotice
}

const (
	respawnJournalKindPrefix = "respawn-"
	// RespawnWaitReasonPrefix starts the reason of the wait a final respawn
	// poses, so a reader of the waits can tell it from a declared one.
	RespawnWaitReasonPrefix = "respawn "
	respawnSummaryMax       = 280 // journal CHECK, runes
	respawnNoOne            = " — no live session to tell"
)

// withheldDetail replaces a detail that looks like a secret.
func withheldDetail(name string) string {
	return "(detail withheld: it looks like a secret, " + name + ")"
}

// NotifyRespawnEnd handles a final respawn state carrying its run: one
// journal line in the subject's scope (its id is the run's idempotence key),
// then — the target session alive and acting in the caller's scope — for a
// failed run a blocked wait (source auto: it never holds the idle wake; an
// abandoned run poses none: it overwrote the session's own Permission /
// Question and repainted the pill red, D2b 2026-10-23) and an info message
// from pigtail that wakes it. Each failure is logged and reported;
// nothing is retried. cross is the caller's --cross-scope (the worker's
// client sends it when the respawn was armed with it).
func (rl *Relay) NotifyRespawnEnd(caller scope.Scope, cross bool, wsID string, req pigtail.CmuxRespawnRequest) pigtail.CmuxRespawnNotice {
	now := rl.cfg.Now()
	detail := req.Detail
	if name := relay.SecretShape(detail); name != "" {
		detail = withheldDetail(name)
	}
	var out pigtail.CmuxRespawnNotice
	p, why := rl.respawnTarget(caller, req.SessionID)
	told := ""
	if p != nil {
		told = p.SessionID // the session the notice goes to: the one the ack recognises (Task 8)
	}

	sum := "respawn " + req.State + " " + req.RunID + ": " + detail
	tail := ""
	if p == nil {
		tail = respawnNoOne
	}
	lineScope, scopeErr := rl.respawnLineScope(caller, cross, req.SubjectID)
	e := &pigtail.JournalEntry{ID: "respawn:" + req.RunID + ":" + req.State, Timestamp: now,
		Kind: respawnJournalKindPrefix + req.State, Scope: string(lineScope), SubjectID: req.SubjectID,
		Summary: clipRunes(sum, respawnSummaryMax-utf8.RuneCountInString(tail)) + tail, Source: respawnSource(req.RunID, told)}
	created, err := false, scopeErr
	if err == nil {
		created, err = rl.svc.Store.AddJournal(e)
	}
	switch {
	case err != nil:
		out.Journal = "failed: " + oneLineErr(err)
		rl.svc.logf("respawn %s %s: journal line not written: %v", req.RunID, req.State, err)
	case !created:
		rl.svc.logf("respawn %s %s: already told (journal line present)", req.RunID, req.State)
		d := pigtail.CmuxRespawnNoticeDuplicate
		return pigtail.CmuxRespawnNotice{Journal: d, Wait: d, Notice: d}
	default:
		out.Journal = pigtail.CmuxRespawnNoticeOK
	}
	if p == nil {
		out.Wait, out.Notice = "skipped: "+why, "skipped: "+why
		rl.svc.logf("respawn %s %s: nobody told — %s", req.RunID, req.State, why)
		return out
	}

	if req.State == pigtail.CmuxRespawnFailed {
		reason := clipBytes(RespawnWaitReasonPrefix+req.State+": "+detail, waittype.MaxReasonBytes)
		wait := pigtail.SessionWait{Type: string(waittype.Blocked), Reason: reason, Source: pigtail.WaitSourceAuto, Since: now}
		if err := rl.recordWait(*p, wait, nil); err != nil {
			out.Wait = "failed: " + oneLineErr(err)
			rl.svc.logf("respawn %s: wait not posed on %s: %v", req.RunID, wakeShort(p.SessionID), err)
		} else {
			out.Wait = pigtail.CmuxRespawnNoticeOK
		}
	} else {
		// An abandoned run poses no wait (D2b): it overwrote the session's
		// own Permission / Question and repainted the pill red.
		out.Wait = pigtail.CmuxRespawnNoticeNone
	}

	subject := req.SubjectID
	if subject == "" {
		subject = "none"
	}
	ack := "pigtail alerts ack " + req.RunID
	if lineScope != caller {
		ack += " --cross-scope" // the alert sits in the subject's scope (Task 8 asks for it)
	}
	body := clipBytes(fmt.Sprintf("Your respawn %s %s (subject %s): %s. Nobody else tells %s — tell them now, then run `%s`. Record: `pigtail session respawn --status`.",
		req.RunID, req.State, subject, detail, rl.svc.humanName(), ack), relay.MaxBodyBytes)
	m := pigtail.Message{ID: genID(pigtail.PrefixMessage), ThreadID: genID(pigtail.PrefixThread),
		FromSession: pigtail.SystemSender, ToSession: p.SessionID, FromLabel: "pigtail", ToLabel: rl.labelOf(p),
		SubjectID: req.SubjectID, Kind: pigtail.MessageKindInfo, Body: body, State: pigtail.MessageStatePending,
		CreatedAt: now, ExpiresAt: now.Add(relay.DefaultTTL)}
	if err := rl.svc.Store.CreateMessage(&m); err != nil {
		out.Notice = "failed: " + oneLineErr(err)
		rl.svc.logf("respawn %s: message to %s not created: %v", req.RunID, wakeShort(p.SessionID), err)
		return out
	}
	rl.refreshMarker(p.SessionID)
	rl.publish(pigtail.EventMessageCreated, m)
	rl.wake(p.SessionID)
	rl.svc.logf("respawn %s %s: %s told (message %s)", req.RunID, req.State, wakeShort(p.SessionID), m.ID)
	out.Notice = pigtail.CmuxRespawnNoticeOK
	return out
}

// respawnSource is the source of a respawn's journal line: the run, and the
// session the notice told when there is one — the session that may ack the
// alert without being on its subject (Task 8, D5).
func respawnSource(run, told string) string {
	if told == "" {
		return "pigtail:respawn/" + run
	}
	return "pigtail:respawn/" + run + "?session=" + told
}

// respawnLineScope is the scope of a respawn's journal line: its subject's
// (Store.SubjectScope, as POST /journal demands), the caller's without a
// subject or for one the store does not know (logged). An error when the
// caller may not write that scope with its cross flag: the line is refused.
func (rl *Relay) respawnLineScope(caller scope.Scope, cross bool, subjectID string) (scope.Scope, error) {
	if subjectID == "" {
		return caller, nil
	}
	sc, err := rl.svc.Store.SubjectScope(subjectID)
	if err != nil {
		rl.svc.logf("respawn: subject %s unreadable (%v) — journal line in the caller's scope", subjectID, err)
		return caller, nil
	}
	if err := scope.CanWrite(caller, sc, cross); err != nil {
		return sc, fmt.Errorf("scope of subject %s not writable by this caller: %w", subjectID, err)
	}
	return sc, nil
}

// respawnTarget is the live session a final respawn tells, or why none:
// unknown to the caller's scope (actingAs: the same answer as unknown), or
// gone.
func (rl *Relay) respawnTarget(caller scope.Scope, session string) (*pigtail.PeerSession, string) {
	if session == "" {
		return nil, "no session in the request"
	}
	p, ok := rl.actingAs(caller, session)
	if !ok {
		return nil, "session " + wakeShort(session) + " unknown to this scope"
	}
	if !rl.cfg.Alive(*p) {
		return nil, "session " + wakeShort(session) + " is gone"
	}
	return p, ""
}

// clipRunes cuts s to at most n runes, "…" marking a cut.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func oneLineErr(err error) string {
	return clipBytes(strings.Join(strings.Fields(err.Error()), " "), 160)
}

// clipBytes cuts s to at most n bytes on a rune boundary, "…" marking a cut.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ell = "…"
	cut := n - len(ell)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ell
}
