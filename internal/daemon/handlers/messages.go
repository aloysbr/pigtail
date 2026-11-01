package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// maxWait bounds one GET /messages/wait. The Stop-hook watcher loops over
// several waits; a single long request would outlive a daemon restart silently.
const (
	defaultWait = 50 * time.Second
	maxWait     = 5 * time.Minute
)

func refuse(w http.ResponseWriter, r *http.Request, resp Responder, msg string) {
	resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrRelayRefused, msg, nil)
}

// SendMessage handles POST /messages.
//
// Every limit is enforced here, not in the CLI: the CLI is one client among
// others, and a limit a client can skip is a suggestion.
func (rl *Relay) SendMessage(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		from, ok := rl.actingAs(callerOf(r).scope, req.From)
		if !ok { // unknown, or of another scope than the caller: same answer
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"unknown sender session "+strconv.Quote(req.From)+": register it first (POST /peers; `pigtail msg send` does it for you)", nil)
			return
		}
		// Subjects are seen from the sender's scope: a session neither addresses
		// a session by a subject of another scope nor attaches one to its
		// message. A sender with no scope acts as the default scope (D17).
		senderScope := actingScope(*from)
		one := []pigtail.PeerSession{*from}
		rl.redactPeers(senderScope, one)
		fromP := one[0]
		fromP.Label = rl.labelOf(&fromP)

		var (
			to        pigtail.PeerSession
			threadID  string
			matchedBy string // addressing level; empty for a reply
		)
		kind := req.Kind
		if req.InReplyTo != "" {
			orig, err := rl.svc.Store.GetMessage(req.InReplyTo)
			if err != nil {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such message "+req.InReplyTo, nil)
				return
			}
			if orig.FromSession == pigtail.HumanSender {
				refuse(w, r, resp, "answers from "+rl.svc.humanName()+" take no reply: tell them in your next turn")
				return
			}
			if orig.FromSession == pigtail.SystemSender {
				refuse(w, r, resp, "notices from pigtail take no reply")
				return
			}
			if orig.ToSession != fromP.SessionID {
				// Covers the self-reply too: the sender of a message is not its recipient.
				refuse(w, r, resp, "only the recipient of "+orig.ID+" can reply to it")
				return
			}
			if orig.Kind == pigtail.MessageKindNotice {
				refuse(w, r, resp, "a notice takes no reply — acknowledge it instead: pigtail msg ack "+orig.ID)
				return
			}
			tp, err := rl.svc.Store.GetPeer(orig.FromSession)
			if err != nil || !peerVisible(senderScope, *tp) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrAddressUnresolved, "the sender of "+orig.ID+" is no longer known", nil)
				return
			}
			back := []pigtail.PeerSession{*tp}
			rl.redactPeers(senderScope, back)
			to, threadID = back[0], orig.ThreadID
			if kind == "" {
				kind = pigtail.MessageKindReply
			}
			if kind != pigtail.MessageKindReply && kind != pigtail.MessageKindAck {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "a reply has kind reply or ack", nil)
				return
			}
		} else {
			if kind == "" {
				kind = pigtail.MessageKindInfo
			}
			if kind != pigtail.MessageKindInfo && kind != pigtail.MessageKindRequest && kind != pigtail.MessageKindNotice {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "a new message has kind info, request or notice", nil)
				return
			}
			live, err := rl.livePeers()
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			// The sender never addresses itself, even by a title it shares,
			// nor a session its scope may not see (a default-scope sender gets no
			// perso candidate: neither a match nor an ambiguity names one).
			cands := live[:0:0]
			for _, p := range live {
				if p.SessionID != fromP.SessionID && peerVisible(senderScope, p) {
					if p.LastWorktree != "" && !rl.cfg.DirExists(p.LastWorktree) {
						p.LastWorktree, p.LastWorktreeAt = "", nil
					}
					cands = append(cands, p)
				}
			}
			rl.redactPeers(senderScope, cands)
			rl.applyLiveTitles(cands) // the address may be written as the tab shows it
			tp, level, err := peers.ResolveLevel(cands, req.To)
			if err != nil {
				var amb *peers.ErrAmbiguous
				if errors.As(err, &amb) {
					names := make([]string, 0, len(amb.Candidates))
					for _, c := range amb.Candidates {
						c := c
						names = append(names, rl.labelOf(&c)+" ["+c.SessionID+"]")
					}
					resp.Error(w, r, http.StatusConflict, pigtail.ErrAddressAmbiguous, err.Error(), map[string]interface{}{"candidates": names})
					return
				}
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrAddressUnresolved, err.Error(), nil)
				return
			}
			to, threadID, matchedBy = tp, genID(pigtail.PrefixThread), level
		}
		to.Label = rl.labelOf(&to)
		out, fail := rl.deliver(fromP, to, kind, threadID, req.InReplyTo, req.Body, req.TTLSeconds, callerOf(r).cross, rl.sendListing())
		if fail != nil {
			resp.Error(w, r, fail.status, fail.code, fail.msg, nil)
			return
		}
		if kind == pigtail.MessageKindNotice {
			rl.auditSender(fromP, out.Message.CreatedAt, fmt.Sprintf("→ %s [notice %s]: %s", out.To.Label, out.Message.ID, relay.FirstLine(req.Body, 160)))
		}
		out.MatchedBy = matchedBy
		resp.JSON(w, r, http.StatusCreated, *out)
	}
}

// sendFailure is a refusal of deliver: what the route answers.
type sendFailure struct {
	status int
	code   pigtail.ErrorCode
	msg    string
}

func relayRefusal(msg string) *sendFailure {
	return &sendFailure{http.StatusUnprocessableEntity, pigtail.ErrRelayRefused, msg}
}

func internalFailure(err error) *sendFailure {
	return &sendFailure{http.StatusInternalServerError, pigtail.ErrInternal, err.Error()}
}

// deliver checks one message from → to against every relay rule and stores
// it. SendMessage and BroadcastMessage share it: a broadcast passes the same
// limits as a single send, recipient by recipient. to.Label is the label from
// the sender's view; a notice that crosses scopes stores a label built from
// the recipient's own view and the neutral sender (D23), and no subject (D1).
// ls is the send-time cmux listing (sendListing; nil = no wake check).
func (rl *Relay) deliver(fromP, to pigtail.PeerSession, kind, threadID, inReplyTo, body string, ttlSeconds int, crossOptIn bool, ls *cmuxListing) (*pigtail.SendMessageResponse, *sendFailure) {
	now := rl.cfg.Now()
	if to.SessionID == fromP.SessionID {
		return nil, relayRefusal("a session cannot message itself")
	}
	if err := relay.CheckScope(fromP, to, kind, crossOptIn); err != nil {
		return nil, &sendFailure{http.StatusForbidden, pigtail.ErrScopeMismatch, err.Error()}
	}
	if err := relay.CheckBody(body); err != nil {
		return nil, relayRefusal(err.Error())
	}
	pending, err := rl.svc.Store.CountPending(to.SessionID, now)
	if err != nil {
		return nil, internalFailure(err)
	}
	if pending >= relay.MaxPendingPerPeer {
		return nil, relayRefusal(fmt.Sprintf("%s already has %d unread messages — wait for it to catch up, or tell the user", to.Label, pending))
	}
	sent, err := rl.svc.Store.CountSentSince(fromP.SessionID, to.SessionID, now.Add(-relay.RateWindow))
	if err != nil {
		return nil, internalFailure(err)
	}
	if sent >= relay.RateMax {
		return nil, relayRefusal(fmt.Sprintf("rate limit: %d messages to %s in the last %s — stop and tell %s", sent, to.Label, relay.RateWindow, rl.svc.humanName()))
	}
	if n, err := rl.svc.Store.CountThread(threadID); err == nil && n >= relay.ThreadCap {
		return nil, relayRefusal(fmt.Sprintf("thread %s reached %d messages — stop here and escalate to the user", threadID, n))
	}

	fromLabel, toLabel := fromP.Label, to.Label
	subjectID := fromP.SubjectID
	if subjectID == "" {
		subjectID = to.SubjectID
	}
	if kind == pigtail.MessageKindNotice {
		subjectID = ""
		if relay.CrossesScope(fromP, to) {
			fromLabel = relay.NoticeSenderLabel
			own := []pigtail.PeerSession{to}
			rl.redactPeers(sessionScope(to), own)
			toLabel = rl.labelOf(&own[0])
		}
	}
	m := pigtail.Message{
		ID:          genID(pigtail.PrefixMessage),
		ThreadID:    threadID,
		InReplyTo:   inReplyTo,
		FromSession: fromP.SessionID,
		ToSession:   to.SessionID,
		FromLabel:   fromLabel,
		ToLabel:     toLabel,
		SubjectID:   subjectID,
		Kind:        kind,
		Body:        body,
		State:       pigtail.MessageStatePending,
		CreatedAt:   now,
		ExpiresAt:   now.Add(relay.ClampTTL(ttlSeconds)),
	}
	if err := rl.svc.Store.CreateMessage(&m); err != nil {
		return nil, internalFailure(err)
	}
	if kind != pigtail.MessageKindNotice {
		rl.audit(m, fromP, to) // a notice: the caller writes the sender's line alone (auditSender)
	}
	n := rl.refreshMarker(to.SessionID)
	rl.publish(pigtail.EventMessageCreated, m)
	delivery := rl.delivery(to.SessionID, to.Status) // before wake: wake changes nothing it reads
	if (delivery == pigtail.DeliveryNone || delivery == pigtail.DeliveryUnknown) && ls != nil {
		if rl.sendWake(to.SessionID, ls) {
			delivery = pigtail.DeliveryWake // D28: pane verified, pigtail will wake it
		}
	}
	rl.wake(to.SessionID)
	to.Inbox = n
	return &pigtail.SendMessageResponse{Message: m, To: to, Pending: n, Delivery: delivery}, nil
}

// auditSender writes one subject_log line on the sender's side only. A notice
// writes nothing on the recipient's side: it carries no subject (D1), and a
// line there would name the sender to another scope (D23).
func (rl *Relay) auditSender(from pigtail.PeerSession, at time.Time, text string) {
	if from.SubjectID == "" {
		return
	}
	if err := rl.svc.Store.AppendLog(&pigtail.SubjectLogEntry{
		ID: genID(pigtail.PrefixLog), SubjectID: from.SubjectID, Timestamp: at, Kind: "message", Text: text,
	}); err != nil {
		rl.svc.logf("relay: subject log: %v", err)
	}
}

// audit writes one subject_log line on each side that maps to a subject.
// Messages with no subject on either side stay in the messages table only.
func (rl *Relay) audit(m pigtail.Message, from, to pigtail.PeerSession) {
	line := func(arrow, peer string) string {
		return fmt.Sprintf("%s %s [%s %s]: %s", arrow, peer, m.Kind, m.ID, relay.FirstLine(m.Body, 160))
	}
	write := func(subjectID, text string) {
		if err := rl.svc.Store.AppendLog(&pigtail.SubjectLogEntry{
			ID: genID(pigtail.PrefixLog), SubjectID: subjectID, Timestamp: m.CreatedAt, Kind: "message", Text: text,
		}); err != nil {
			rl.svc.logf("relay: subject log: %v", err)
		}
	}
	if from.SubjectID != "" {
		write(from.SubjectID, line("→", to.Label))
	}
	if to.SubjectID != "" && to.SubjectID != from.SubjectID {
		write(to.SubjectID, line("←", from.Label))
	}
}

// ListMessages handles GET /messages?to=&from=&session=&state=&thread=&subject=&since=&limit=.
// since takes RFC 3339 or a Go duration back from now ("24h"). A default-scope caller
// sees default-scope conversations only (relay_scope.go).
func (rl *Relay) ListMessages(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rl.expire()
		q := r.URL.Query()
		f := pigtail.MessageFilter{
			To: q.Get("to"), From: q.Get("from"), Session: q.Get("session"),
			State: q.Get("state"), Thread: q.Get("thread"), Subject: q.Get("subject"),
		}
		if s := q.Get("since"); s != "" {
			if d, err := time.ParseDuration(s); err == nil {
				f.Since = rl.cfg.Now().Add(-d)
			} else if t, err := time.Parse(time.RFC3339, s); err == nil {
				f.Since = t
			} else {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "since: RFC 3339 or a duration like 24h", nil)
				return
			}
		}
		if l := q.Get("limit"); l != "" {
			n, err := strconv.Atoi(l)
			if err != nil || n < 0 {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "limit: a positive integer", nil)
				return
			}
			f.Limit = n
		}
		caller := callerOf(r).scope
		// D19: a filter naming a session caller cannot read answers like an
		// unknown one — the notice branch of the SQL filter skips the sender's
		// scope, so without this ?from=<master session> would return its
		// notices while ?from=<unknown> returns [].
		for _, id := range []string{f.From, f.To, f.Session} {
			if id != "" && !rl.sessionReadable(caller, id) {
				resp.JSON(w, r, http.StatusOK, []pigtail.Message{})
				return
			}
		}
		view := caller // the master reads every conversation
		if scope.SeesAll(caller) {
			view = scope.Any
		}
		out, err := rl.svc.Store.ListMessagesInScope(f, view)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if out == nil {
			out = []pigtail.Message{}
		}
		rl.redactMessages(caller, out)
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// GetMessage handles GET /messages/{id}; a conversation outside the caller's
// view is 404, like an unknown id.
func (rl *Relay) GetMessage(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, err := rl.svc.Store.GetMessage(chi.URLParam(r, "id"))
		caller := callerOf(r).scope
		if err == nil && !rl.messageVisible(caller, *m) {
			err = store.ErrNotFound // same answer as an unknown id
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such message", nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		one := []pigtail.Message{*m}
		rl.redactMessages(caller, one)
		resp.JSON(w, r, http.StatusOK, one[0])
	}
}

// unknownSession is the answer of claim and wait for a session the registry
// does not know — and, identically, for one the caller may not act as.
func unknownSession(w http.ResponseWriter, r *http.Request, resp Responder) {
	resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound,
		"no such session: it must be registered (the SessionStart hook or any `pigtail msg` does it) by a caller of its own scope", nil)
}

// claim delivers a session's pending messages and clears its marker.
func (rl *Relay) claim(session, via string) ([]pigtail.Message, error) {
	ms, err := rl.svc.Store.ClaimPending(session, via, rl.cfg.Now())
	if err != nil {
		return nil, err
	}
	rl.refreshMarker(session)
	for _, m := range ms {
		rl.publish(pigtail.EventMessageDelivered, m)
	}
	if via == "prompt" && len(ms) > 0 {
		rl.wakeClaimed(session, len(ms)) // D35: the wake's success line
	}
	if ms == nil {
		ms = []pigtail.Message{}
	}
	return ms, nil
}

// ClaimMessages handles POST /messages/claim: atomically deliver every pending
// message of a session. Two claimers racing never get the same message. The
// caller must speak for the session's scope (actingAs).
func (rl *Relay) ClaimMessages(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.ClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Session == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "session is required", nil)
			return
		}
		if req.Via == "" {
			req.Via = "cli"
		}
		if _, ok := rl.actingAs(callerOf(r).scope, req.Session); !ok {
			unknownSession(w, r, resp)
			return
		}
		rl.expire()
		ms, err := rl.claim(req.Session, req.Via)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		rl.redactMessages(callerOf(r).scope, ms)
		resp.JSON(w, r, http.StatusOK, pigtail.ClaimResponse{Messages: ms})
	}
}

// WaitMessages handles GET /messages/wait?session=&timeout=<seconds>: a long
// poll that claims (via=rewake) and returns the session's messages as soon as
// there are any, or an empty list at the timeout. One waiter per session: a
// new one supersedes the old, which returns superseded=true and must exit. A session in
// Standby is never served (D16, sub_x000000000000041): its inbox waits for
// the user's prompt (claude-inbox), so the poll claims nothing and ends empty.
func (rl *Relay) WaitMessages(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("session")
		if session == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "session is required", nil)
			return
		}
		// Before register: a foreign waiter would supersede the session's own.
		if _, ok := rl.actingAs(callerOf(r).scope, session); !ok {
			unknownSession(w, r, resp)
			return
		}
		timeout := defaultWait
		if s := r.URL.Query().Get("timeout"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "timeout: seconds", nil)
				return
			}
			timeout = time.Duration(n) * time.Second
		}
		if timeout > maxWait {
			timeout = maxWait
		}
		via := r.URL.Query().Get("via")
		if via == "" {
			via = "rewake"
		}
		wt := rl.register(session)
		defer rl.unregister(session, wt)
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			if r.Context().Err() != nil {
				return // the watcher left: claim nothing it would never show
			}
			var ms []pigtail.Message
			if held, why := rl.inStandby(session); held {
				rl.noteInboxHeld(session, why) // claim nothing: the message waits for the user's prompt
			} else {
				rl.noteInboxHeld(session, "")
				var err error
				if ms, err = rl.claim(session, via); err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
					return
				}
			}
			if len(ms) > 0 {
				rl.redactMessages(callerOf(r).scope, ms)
				resp.JSON(w, r, http.StatusOK, pigtail.ClaimResponse{Messages: ms})
				return
			}
			select {
			case <-wt.wake:
				continue
			case <-wt.superseded:
				resp.JSON(w, r, http.StatusOK, pigtail.ClaimResponse{Messages: []pigtail.Message{}, Superseded: true})
				return
			case <-timer.C:
				resp.JSON(w, r, http.StatusOK, pigtail.ClaimResponse{Messages: []pigtail.Message{}})
				return
			case <-r.Context().Done():
				return
			}
		}
	}
}

// noteInboxHeld logs "inbox wait held: <why>" once per change of reason and
// session; why "" (served again) forgets it. Its own map, never the wake's
// lastReason, which the sweep forgets for any session with nothing pending.
func (rl *Relay) noteInboxHeld(session, why string) {
	rl.wakeMu.Lock()
	if rl.inboxHeld == nil {
		rl.inboxHeld = map[string]string{}
	}
	same := rl.inboxHeld[session] == why
	if why == "" {
		delete(rl.inboxHeld, session)
	} else {
		rl.inboxHeld[session] = why
	}
	rl.wakeMu.Unlock()
	if !same && why != "" {
		rl.wakeLog("relay: %s inbox wait held: %s", wakeShort(session), why)
	}
}

// AckMessage handles POST /messages/{id}/ack. Only the recipient acks, and
// only from its own scope (actingAs).
func (rl *Relay) AckMessage(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req pigtail.AckRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Session == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "session is required", nil)
			return
		}
		// A session the caller may not act as reads like an unknown message:
		// its ids are not confirmed either way.
		_, ok := rl.actingAs(callerOf(r).scope, req.Session)
		m, err := rl.svc.Store.GetMessage(id)
		if !ok || err != nil {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such message", nil)
			return
		}
		if m.ToSession != req.Session {
			refuse(w, r, resp, "only the recipient of "+id+" can acknowledge it")
			return
		}
		if err := rl.svc.Store.AckMessage(id, rl.cfg.Now()); err != nil {
			refuse(w, r, resp, err.Error())
			return
		}
		rl.refreshMarker(m.ToSession)
		m, _ = rl.svc.Store.GetMessage(id)
		rl.publish(pigtail.EventMessageAcked, *m)
		one := []pigtail.Message{*m}
		rl.redactMessages(callerOf(r).scope, one)
		resp.JSON(w, r, http.StatusOK, one[0])
	}
}
