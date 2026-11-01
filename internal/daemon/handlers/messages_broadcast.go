package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// BroadcastMessage handles POST /messages/broadcast: one notice to every live
// session the sender reaches, the sender excluded — its own scope, or every
// scope for the master with the cross-scope opt-in (D2). Request-level rules
// (sender, opt-in, body) refuse the whole call; past them each recipient is
// checked by deliver like a single send, and refused alone (T3). 200 even
// when nothing was sent: the per-recipient results say why, and the CLI
// fails when Sent is 0.
func (rl *Relay) BroadcastMessage(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.BroadcastRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		c := callerOf(r)
		from, ok := rl.actingAs(c.scope, req.From)
		if !ok { // same answer as POST /messages
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"unknown sender session "+strconv.Quote(req.From)+": register it first (POST /peers; `pigtail msg send` does it for you)", nil)
			return
		}
		senderScope := actingScope(*from)
		if c.cross && !scope.SeesAll(senderScope) {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch,
				"--cross-scope widens nothing from this cell: only the master cell sends to another scope", nil)
			return
		}
		if err := relay.CheckBody(req.Body); err != nil {
			refuse(w, r, resp, err.Error())
			return
		}
		one := []pigtail.PeerSession{*from}
		rl.redactPeers(senderScope, one)
		fromP := one[0]
		fromP.Label = rl.labelOf(&fromP)

		live, err := rl.livePeers()
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		var (
			targets []pigtail.PeerSession
			skipped int // live sessions of other scopes left out (C7)
		)
		for _, p := range live {
			if p.SessionID == fromP.SessionID {
				continue
			}
			if c.cross { // the master, checked above: every scope it sees
				if !peerVisible(senderScope, p) {
					continue
				}
			} else if sessionScope(p) != senderScope {
				skipped++
				continue
			}
			targets = append(targets, p)
		}
		rl.redactPeers(senderScope, targets)

		out := pigtail.BroadcastResponse{Results: []pigtail.BroadcastResult{}}
		if scope.SeesAll(senderScope) && !c.cross {
			// C7: only the master gets the count — it sees every scope; a
			// non-master's count would reveal the others (D19).
			out.OtherScopesSkipped = skipped
		}
		ls := rl.sendListing() // one cmux listing for every recipient
		for _, to := range targets {
			to.Label = rl.labelOf(&to)
			res, fail := rl.deliver(fromP, to, pigtail.MessageKindNotice, genID(pigtail.PrefixThread), "", req.Body, req.TTLSeconds, c.cross, ls)
			if fail != nil {
				out.Refused++
				out.Results = append(out.Results, pigtail.BroadcastResult{To: to, Refused: fail.code, Reason: fail.msg})
				continue
			}
			out.Sent++
			out.Results = append(out.Results, pigtail.BroadcastResult{
				To: res.To, MessageID: res.Message.ID, Pending: res.Pending, Delivery: res.Delivery,
			})
		}
		rl.auditSender(fromP, rl.cfg.Now(), fmt.Sprintf("→ %d sessions [notice broadcast: %d sent, %d refused]: %s",
			len(targets), out.Sent, out.Refused, relay.FirstLine(req.Body, 160)))
		resp.JSON(w, r, http.StatusOK, out)
	}
}
