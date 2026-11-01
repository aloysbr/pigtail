package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waitdetail"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// POST /waits/{id}/answer (sub_x000000000000026, Task 9): the user answers a
// wait from his terminal. The answer goes to the session as a relay message
// from pigtail.HumanSender (relay.Frame frames it apart), in the same
// transaction that closes the wait; then the session's Standby is lifted (D1,
// the one exception to D16) and it is woken. Every refusal writes nothing.

// refusedInTerminal is the one refusal of a Claude Code dialog and of a
// permission wait.
const refusedInTerminal = "answer it in the terminal"

// AnswerWait handles POST /waits/{id}/answer.
func (rl *Relay) AnswerWait(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A guard against an agent's mistake, not a security boundary: the
		// CLI of an agent session sends its session id.
		if r.Header.Get(pigtail.HeaderSession) != "" {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrUnauthorized, "answer from your terminal, not from an agent session", nil)
			return
		}
		id := chi.URLParam(r, "id")
		rec, ok := rl.waitFor(r, id)
		if !ok {
			noSuchWait(w, r, resp)
			return
		}
		c := callerOf(r)
		if err := scope.CanWrite(c.scope, waitScope(rec), c.cross); err != nil {
			scopeError(w, r, resp, err, "no such wait")
			return
		}
		if rec.State != pigtail.WaitStateOpen {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "wait "+id+" is "+rec.State, nil)
			return
		}
		if rec.Source == pigtail.WaitSourceAuto || waittype.Type(rec.Type) == waittype.Permission {
			refuse(w, r, resp, refusedInTerminal)
			return
		}
		var req pigtail.WaitAnswerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		answer, err := waitdetail.ParseAnswer(rec.Wait, req)
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		out, status, code, msg := rl.answerWait(rec, answer)
		if out == nil {
			resp.Error(w, r, status, code, msg, nil)
			return
		}
		out.Wait = rl.waitView(c.scope, &out.rec)
		if out.DeliveredTo != "" {
			out.DeliveredToLabel = out.Wait.DeliveredToLabel // under the caller's scope (D23)
		}
		resp.JSON(w, r, http.StatusOK, out.WaitAnswerResponse)
	}
}

// waitScope is the scope a wait was declared under; none is the master's (D17).
func waitScope(rec *store.WaitRecord) scope.Scope {
	if strings.TrimSpace(rec.Scope) == "" {
		return scope.Master()
	}
	return scope.Canonical(rec.Scope)
}

type answered struct {
	pigtail.WaitAnswerResponse
	rec store.WaitRecord
}

// answerWait writes the answer and delivers it to its recipient
// (subjectRecipient): the live emitter, else the subject's live session of
// the same scope, else nobody — the answer is kept for the subject's next
// session (D29-C). A nil result carries the refusal (status, code, message):
// nothing was written nor published.
func (rl *Relay) answerWait(rec *store.WaitRecord, a pigtail.WaitAnswer) (*answered, int, pigtail.ErrorCode, string) {
	// One clock read for the answer, its message and the Standby lift: the
	// lift gives pending messages back the time the Standby lasted (D28,
	// now − max(since, created_at)), which is zero for a message created at
	// that same now — the answer keeps its relay.MaxTTL.
	now := rl.cfg.Now()
	a.At = now
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, http.StatusInternalServerError, pigtail.ErrInternal, err.Error()
	}
	emitter := pigtail.PeerSession{SessionID: rec.SessionID}
	if got, err := rl.svc.Store.GetPeer(rec.SessionID); err == nil && got != nil {
		emitter = *got
	}
	to, found := rl.subjectRecipient(rec)
	var (
		m       pigtail.Message
		toLabel string
		done    *store.WaitRecord
	)
	if found {
		m, toLabel = rl.answerMessage(rec, *to, a, now)
		done, err = rl.svc.Store.AnswerWaitWithMessage(rec.ID, raw, now, &m)
		if errors.Is(err, store.ErrRecipientGone) {
			if rec.SubjectID == "" {
				// C2: never kept — KeptAnswers reads subject waits only, it
				// would be lost in silence.
				return nil, http.StatusConflict, pigtail.ErrConflict, "wait " + rec.ID + " is closed: its session ended"
			}
			found = false // C3: ended between its choice and the write
		}
	}
	if !found {
		// The trace's entry: the message built, never inserted.
		m, _ = rl.answerMessage(rec, pigtail.PeerSession{}, a, now)
		m.ToSession = ""
		toLabel = "kept for the subject's next session"
		done, err = rl.svc.Store.AnswerWaitWithMessage(rec.ID, raw, now, nil)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, http.StatusNotFound, pigtail.ErrNotFound, "no such wait"
	case errors.Is(err, store.ErrWaitClosed):
		state := "closed"
		if cur, gerr := rl.svc.Store.GetWaitRecord(rec.ID); gerr == nil {
			state = cur.State
		}
		return nil, http.StatusConflict, pigtail.ErrConflict, "wait " + rec.ID + " is " + state
	case errors.Is(err, store.ErrWaitAuto):
		return nil, http.StatusUnprocessableEntity, pigtail.ErrRelayRefused, refusedInTerminal
	case err != nil:
		return nil, http.StatusInternalServerError, pigtail.ErrInternal, err.Error()
	}
	rl.publishWaitAnswered(emitter, done)

	out := &answered{rec: *done}
	if found {
		if rl.afterAnswerWrite != nil {
			rl.afterAnswerWrite(rec.ID)
		}
		out.MessageID = m.ID
		out.DeliveredTo = to.SessionID
		out.Delivery, out.StandbyEnded = rl.deliverAnswer(done, *to, m, now, true)
		if out.Delivery == "" {
			// Ended right after the write: EndPeerClosing kept it again.
			out.Delivery = pigtail.DeliveryNone
			if done.SubjectID != "" {
				out.Delivery = pigtail.DeliveryKept
				out.MessageID, out.DeliveredTo = "", ""
				toLabel = "kept for the subject's next session"
				// The record written above still names the ended recipient:
				// the response shows the one kept again.
				if cur, gerr := rl.svc.Store.GetWaitRecord(rec.ID); gerr == nil {
					out.rec = *cur
				} else {
					rl.svc.logf("relay: answer %s: re-read: %v", rec.ID, gerr)
				}
			}
		}
	} else {
		out.Delivery = pigtail.DeliveryKept
		rl.svc.logf("relay: answer %s kept for subject %s: no live session", rec.ID, rec.SubjectID)
	}
	rl.traceAnswer(m, toLabel, waitScope(done))
	return out, 0, "", ""
}

// traceAnswer writes the answer's line in its subject's log and its pointer
// in the journal. A failure is logged; the answer stays delivered.
func (rl *Relay) traceAnswer(m pigtail.Message, toLabel string, sc scope.Scope) {
	waitID := m.ThreadID
	// Neither the log nor the journal name a subject the wait's scope may not
	// read (D23; POST /journal refuses the same case).
	subjectID := m.SubjectID
	if subjectID != "" && !rl.subjectVisible(sc, subjectID) {
		subjectID = ""
	}
	if subjectID != "" {
		if err := rl.svc.Store.AppendLog(&pigtail.SubjectLogEntry{
			ID: genID(pigtail.PrefixLog), SubjectID: subjectID, Timestamp: m.CreatedAt, Kind: "wait",
			Text: fmt.Sprintf("%s → %s [answer %s]: %s", rl.svc.humanName(), toLabel, waitID, relay.FirstLine(m.Body, 160)),
		}); err != nil {
			rl.svc.logf("relay: answer %s: subject log: %v", waitID, err)
		}
	}
	// The journal's id is the producer's deterministic one (pigtail.JournalEntry):
	// a wait is answered once, so its id names the answer.
	e := pigtail.JournalEntry{
		ID: "wait-answer-" + waitID, Timestamp: m.CreatedAt, Kind: "wait-answer", Scope: string(sc), SubjectID: subjectID,
		Summary: relay.FirstLine(m.Body, pigtail.JournalSummaryMax), Source: "pigtail:wait/" + waitID,
	}
	if err := e.Validate(); err != nil {
		rl.svc.logf("relay: answer %s: journal: %v", waitID, err)
		return
	}
	if _, err := rl.svc.Store.AddJournal(&e); err != nil {
		rl.svc.logf("relay: answer %s: journal: %v", waitID, err)
	}
}
