package handlers

import (
	"errors"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/waitdetail"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The wait of a subject (D28, D29 of sub_x000000000000026, Task 17). A
// declared wait of an answerable type bound to a subject belongs to that
// subject in its scope: it outlives its emitter (D29-A), and its answer goes
// to the emitter while it lives, else to the subject's live session of the
// same scope, else it is kept (D29-C) and goes to the subject's next session
// — at that session's registration (no wake, C4) or at the relay's tick.

// emitterGone tells whether session p is gone: ended, or its process dead (a
// crash without SessionEnd). Alive is the only criterion: Source and
// UpdatedAt are rewritten by the snapshot and by UpsertPeer.
func (rl *Relay) emitterGone(p pigtail.PeerSession) bool { return !rl.cfg.Alive(p) }

// subjectRecipient picks the session the answer to rec goes to: a wait with
// no subject answers its emitter, always (C2); else the emitter when it is
// not gone, else the most recent session not gone with the same effective
// subject in the wait's scope; none → nil, false.
func (rl *Relay) subjectRecipient(rec *store.WaitRecord) (*pigtail.PeerSession, bool) {
	emitter := pigtail.PeerSession{SessionID: rec.SessionID}
	if got, err := rl.svc.Store.GetPeer(rec.SessionID); err == nil && got != nil {
		emitter = *got
	}
	if rec.SubjectID == "" || !rl.emitterGone(emitter) {
		return &emitter, true
	}
	ps, err := rl.svc.Store.ListPeers(false) // most recent first
	if err != nil {
		rl.svc.logf("relay: wait %s: recipient: %v", rec.ID, err)
		return nil, false
	}
	sc := waitScope(rec)
	for i := range ps {
		p := ps[i]
		if p.SessionID == rec.SessionID || p.SubjectID != rec.SubjectID || sessionScope(p) != sc || rl.emitterGone(p) {
			continue
		}
		return &p, true
	}
	return nil, false
}

// answerMessage is the relay message carrying rec's answer a to session to:
// from pigtail.HumanSender, the label to's own scope reads (D23), its body
// the rendering of the stored value (D28 (b)).
func (rl *Relay) answerMessage(rec *store.WaitRecord, to pigtail.PeerSession, a pigtail.WaitAnswer, now time.Time) (pigtail.Message, string) {
	own := []pigtail.PeerSession{to}
	rl.redactPeers(sessionScope(to), own)
	toLabel := rl.labelOf(&own[0])
	return pigtail.Message{
		ID:          genID(pigtail.PrefixMessage),
		ThreadID:    rec.ID,
		FromSession: pigtail.HumanSender,
		ToSession:   to.SessionID,
		FromLabel:   rl.svc.humanName(),
		ToLabel:     toLabel,
		// The subject is the one frozen at declaration — never the session's
		// current one, which may be bound later or belong to another scope.
		SubjectID: rec.SubjectID,
		Kind:      pigtail.MessageKindInfo,
		Body:      waitdetail.RenderAnswer(rec.Wait, a, rl.svc.humanName()),
		State:     pigtail.MessageStatePending,
		CreatedAt: now,
		ExpiresAt: now.Add(relay.MaxTTL),
	}, toLabel
}

// deliverAnswer tells the message m, written, carrying rec's answer to
// session to. Re-read first: to ended between the write and here → nothing
// (EndPeerClosing kept the answer again, the tick hands it on) and "".
// Otherwise: the Standby D1 lifted on to, its marker, message.created and
// wait.delivered; with wakeNow only, the delivery computed and the wake.
func (rl *Relay) deliverAnswer(rec *store.WaitRecord, to pigtail.PeerSession, m pigtail.Message, now time.Time, wakeNow bool) (delivery string, standbyEnded bool) {
	if got, err := rl.svc.Store.GetPeer(to.SessionID); err == nil && got != nil {
		to = *got
	}
	if to.EndedAt != nil {
		return "", false
	}
	// D1: lift the Standby before the wake — a claude-inbox-wait watcher
	// held in the long-poll (WaitMessages) re-reads inStandby when woken.
	var held string
	if in, why := rl.inStandby(to.SessionID); in {
		if why == standbyReason {
			if gone, err := rl.svc.Store.ClearStandby(to.SessionID, now); err != nil {
				held = "standby not lifted: " + err.Error()
			} else {
				rl.refreshStandbyMarker(to.SessionID)
				if gone {
					rl.publishStandby(to, false)
					rl.svc.logf("relay: %s standby ended: answer %s", to.SessionID, rec.ID)
					standbyEnded = true
				}
			}
		} else {
			held = why
		}
	}
	rl.refreshMarker(to.SessionID)
	rl.publish(pigtail.EventMessageCreated, m)
	rl.publishWaitEvent(pigtail.EventWaitDelivered, rec.ID, to.SessionID, waitScope(rec), pigtail.WaitStateAnswered)
	if held != "" {
		// Still in Standby: nothing claims nor wakes it before the user's prompt.
		rl.svc.logf("relay: %s answer %s held: %s", to.SessionID, rec.ID, held)
		return pigtail.DeliveryHeld + ": " + held, standbyEnded
	}
	if !wakeNow {
		return pigtail.DeliveryNone, standbyEnded
	}
	delivery = rl.delivery(to.SessionID, to.Status) // before wake: wake changes nothing it reads
	if delivery == pigtail.DeliveryNone || delivery == pigtail.DeliveryUnknown {
		if ls := rl.sendListing(); ls != nil && rl.sendWake(to.SessionID, ls) {
			delivery = pigtail.DeliveryWake
		}
	}
	rl.wake(to.SessionID)
	return delivery, standbyEnded
}

// deliverKeptAnswers hands each kept answer to its subject's session now
// running — only that session when only is set (its registration, wakeNow
// false: C4), every one at the tick (only "", wakeNow true). Delivered once:
// a lost race (ErrWaitDelivered) is the other path's win; any other error is
// logged and retried at the next tick. No subject trace: the answer wrote it.
func (rl *Relay) deliverKeptAnswers(only string, wakeNow bool) {
	kept, err := rl.svc.Store.KeptAnswers()
	if err != nil {
		rl.svc.logf("relay: kept answers: %v", err)
		return
	}
	for i := range kept {
		rec := &kept[i]
		if rec.Answer == nil {
			continue
		}
		to, ok := rl.subjectRecipient(rec)
		if !ok || (only != "" && to.SessionID != only) {
			continue
		}
		now := rl.cfg.Now()
		m, _ := rl.answerMessage(rec, *to, *rec.Answer, now)
		switch err := rl.svc.Store.DeliverKeptAnswer(rec.ID, &m, now); {
		case errors.Is(err, store.ErrWaitDelivered):
			continue
		case err != nil:
			rl.svc.logf("relay: kept answer %s to %s: %v", rec.ID, to.SessionID, err)
			continue
		}
		rl.deliverAnswer(rec, *to, m, now, wakeNow)
		var kept time.Duration // since the answer (its closed_at)
		if rec.ClosedAt != nil {
			kept = now.Sub(*rec.ClosedAt).Round(time.Second)
		}
		rl.svc.logf("relay: answer %s delivered to %s (kept %s)", rec.ID, to.SessionID, kept)
	}
}

// goneRecipientConfirm is how long a recipient must read gone, across two
// ticks, before its unread answers are kept again — the sidebar loop's
// WaitGoneConfirm: one unreadable native file must not move an answer.
const goneRecipientConfirm = 2 * time.Second

// rekeepGoneRecipients keeps again the unread answers to a subject's wait
// of each recipient gone (!Alive: a crash without SessionEnd), confirmed
// goneRecipientConfirm apart (D29-C). Its declared row went with the answer,
// so the sidebar loop never judges it; this pass runs in the relay's tick,
// before deliverKeptAnswers hands the answer to the subject's next session,
// and calls no cmux. A store error is logged and retried at the next tick.
func (rl *Relay) rekeepGoneRecipients() {
	ids, err := rl.svc.Store.UnreadAnswerRecipients()
	if err != nil {
		rl.svc.logf("relay: unread answer recipients: %v", err)
		return
	}
	now := rl.cfg.Now()
	rl.goneMu.Lock()
	defer rl.goneMu.Unlock()
	seen := make(map[string]time.Time, len(ids))
	for _, id := range ids {
		p, err := rl.svc.Store.GetPeer(id)
		if err != nil || p == nil || !rl.emitterGone(*p) {
			continue // unknown or alive: nothing to judge
		}
		first, ok := rl.goneSince[id]
		if !ok {
			seen[id] = now
			continue
		}
		if now.Sub(first) < goneRecipientConfirm {
			seen[id] = first
			continue
		}
		reheld, err := rl.svc.Store.RekeepUnreadAnswers(id)
		if err != nil {
			rl.svc.logf("relay: keep again the answers of %s: %v", id, err)
			seen[id] = first
			continue
		}
		if len(reheld) > 0 {
			rl.refreshMarker(id) // its expired answers left the inbox
			rl.svc.logf("relay: %s gone: %d unread answer(s) kept again", id, len(reheld))
		}
	}
	rl.goneSince = seen
}
