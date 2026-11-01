package handlers

import (
	"strings"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Scope isolation of the relay. The relay has its own rule for sending
// (relay.CheckScope: within a scope any kind; across scopes only a service
// notice from the master cell with the opt-in, by the sessions' cells); this
// file covers what the relay *shows* and *who may act as a
// session*:
//
//   - a session is seen only by a caller that may read its scope: a caller of
//     another scope gets no row, no id, no label of it — not from /peers, not
//     as an addressing candidate. A session with no scope is seen by the
//     master only (D17) and acted on as the default scope (actingScope);
//   - a visible session's subject (subject_id, subject_slug, and the label
//     built from them) is stripped when the caller may not read that subject.
//     An unknown subject id is the master's: fail-closed;
//   - a message belongs to the scope of its sessions; a caller sees the
//     conversations of the scopes it may read, and nothing of the others. A
//     notice belongs to its recipient's scope: its sender, from a scope the
//     caller cannot read, reads as pigtail (redactMessages);
//   - acting as a session (send as, claim, wait, ack, end) needs the caller's
//     scope to EQUAL the session's registered scope (actingAs). Otherwise the
//     route answers exactly as for an unknown session. Equality, not "may
//     read": the master does not consume another scope's session inbox either.
//
// The caller is the X-Pigtail-Scope header (scopeMW, absent = the default
// scope) on every
// relay route. Subjects on POST /messages are seen from the sender's scope.

// subjectVisible reports whether caller may read the subject id. An id the
// store does not know (orphan) or cannot answer for is visible to the master
// only.
func (rl *Relay) subjectVisible(caller scope.Scope, id string) bool {
	if scope.SeesAll(caller) {
		return true
	}
	sc, err := rl.svc.Store.SubjectScope(id)
	if err != nil {
		return false
	}
	return scope.CanRead(caller, sc)
}

// redactPeers strips, in place, the subject of every peer whose subject caller
// may not read, and the cmux title that may show an unreadable subject's short
// title (D23): the tab bound to that subject, the tab of an ended row reading
// that stamp, and a tab bound to another subject caller may not read. Call it
// before decorate, which builds the label from what is left.
func (rl *Relay) redactPeers(caller scope.Scope, ps []pigtail.PeerSession) {
	if scope.SeesAll(caller) {
		return
	}
	seen := map[string]bool{}
	visible := func(id string) bool {
		ok, known := seen[id]
		if !known {
			ok = rl.subjectVisible(caller, id)
			seen[id] = ok
		}
		return ok
	}
	for i := range ps {
		// An ended row reads its stamp while its tab may be linked to
		// another subject, whose short title the stored cmux_title can hold.
		if b := ps[i].CmuxBindingSubjectID; b != "" && b != ps[i].SubjectID && !visible(b) {
			ps[i].CmuxTitle, ps[i].TitleRedacted = "", true
		}
		id := ps[i].SubjectID
		if id == "" {
			continue
		}
		if !visible(id) {
			ps[i].SubjectID, ps[i].SubjectSlug, ps[i].SubjectShortTitle = "", "", ""
			// A bound workspace shows the subject's title; so does a tab an
			// ended row reads its stamp in while the tab is linked elsewhere
			// (or to nothing): its stored title may be the unreadable subject's.
			if ps[i].CmuxBound || (ps[i].EndedAt != nil && ps[i].SubjectStamp == id && ps[i].CmuxWorkspaceID != "") {
				ps[i].CmuxTitle, ps[i].CmuxBound = "", false
				ps[i].TitleRedacted = true // labelOf never puts a live title back
			}
		}
	}
}

// sessionScope is the scope under which a peer is SEEN: its cell's, canonical.
// A session with no scope is seen by the master only (D17), on every path —
// /peers, GET /messages (whose SQL already keeps an empty scope out of a non-master view),
// message and wait events. One whose scope the table does not declare keeps
// that raw name, which only the master may read too.
func sessionScope(p pigtail.PeerSession) scope.Scope {
	if strings.TrimSpace(p.Scope) == "" {
		return scope.Master()
	}
	return scope.Canonical(p.Scope)
}

// actingScope is the scope a caller must have to ACT as the peer (actingAs)
// and from which its subjects are seen when it sends (POST /messages). A
// session with no scope is the default scope, like a caller outside any cell:
// never the master — acting as it must not widen what it sees.
func actingScope(p pigtail.PeerSession) scope.Scope {
	if strings.TrimSpace(p.Scope) == "" {
		return scope.Default()
	}
	return scope.Canonical(p.Scope)
}

// messageVisible reports whether caller may read m: every session of the
// conversation (the system sender aside) must run on a scope caller can read.
// A session the registry no longer knows, or with no scope, is the master's.
// The sender of a notice is not checked either — a notice belongs to its
// recipient's scope (D1); redactMessages hides its sender.
func (rl *Relay) messageVisible(caller scope.Scope, m pigtail.Message) bool {
	if scope.SeesAll(caller) {
		return true
	}
	for _, s := range []string{m.FromSession, m.ToSession} {
		if pigtail.NonPeerSender(s) || (s == m.FromSession && m.Kind == pigtail.MessageKindNotice) {
			continue
		}
		p, err := rl.svc.Store.GetPeer(s)
		if err != nil || !scope.CanRead(caller, sessionScope(*p)) {
			return false
		}
	}
	return true
}

// sessionReadable reports whether caller may read the session id (the system
// sender always). An unknown session is the master's: fail-closed.
func (rl *Relay) sessionReadable(caller scope.Scope, id string) bool {
	if pigtail.NonPeerSender(id) || scope.SeesAll(caller) {
		return true
	}
	p, err := rl.svc.Store.GetPeer(id)
	return err == nil && scope.CanRead(caller, sessionScope(*p))
}

// redactMessages strips, in place, what caller may not read: a subject of
// another scope, and the sender of a notice from a scope caller cannot read —
// it reads as pigtail, labelled relay.NoticeSenderLabel (D23, C6).
func (rl *Relay) redactMessages(caller scope.Scope, ms []pigtail.Message) {
	if scope.SeesAll(caller) {
		return
	}
	for i := range ms {
		if ms[i].SubjectID != "" && !rl.subjectVisible(caller, ms[i].SubjectID) {
			ms[i].SubjectID = ""
		}
		if ms[i].Kind == pigtail.MessageKindNotice && !rl.sessionReadable(caller, ms[i].FromSession) {
			ms[i].FromSession, ms[i].FromLabel = pigtail.SystemSender, relay.NoticeSenderLabel
		}
	}
}

// peerVisible reports whether caller may see the session p at all.
func peerVisible(caller scope.Scope, p pigtail.PeerSession) bool {
	return scope.CanRead(caller, sessionScope(p))
}

// visiblePeers keeps the sessions caller may see, in place.
func visiblePeers(caller scope.Scope, ps []pigtail.PeerSession) []pigtail.PeerSession {
	out := ps[:0]
	for _, p := range ps {
		if peerVisible(caller, p) {
			out = append(out, p)
		}
	}
	return out
}

// actingAs returns the registered session id, when caller may act as it: the
// session is known and its scope is the caller's. ok=false for an unknown
// session and for one of another scope alike — the routes answer both the
// same way, so a caller cannot probe for sessions it may not see.
func (rl *Relay) actingAs(caller scope.Scope, sessionID string) (*pigtail.PeerSession, bool) {
	return sessionActingAs(rl.svc.Store, caller, sessionID)
}

// sessionActingAs is actingAs on a store: the one definition of "caller may
// act as this session", shared with the routes outside the relay (the alert ack).
func sessionActingAs(st *store.Store, caller scope.Scope, sessionID string) (*pigtail.PeerSession, bool) {
	p, err := st.GetPeer(sessionID)
	if err != nil || actingScope(*p) != caller {
		return nil, false
	}
	return p, true
}
