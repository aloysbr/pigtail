package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Enriched waits, read side (sub_x000000000000026, Task 5):
//
//	GET /waits?all=1   the open waits the caller may read, oldest first;
//	                   all=1 adds the snoozed ones; subject=<id> and
//	                   session=<id> narrow them; closed=<0..5> appends up to
//	                   that many closed ones, most recent first, with their
//	                   answer (sub_x000000000000025)
//	GET /waits/{id}    one wait, open or closed, with its answer
//	POST /waits/{id}/snooze  hides an open wait until a time (SnoozeWait)
//
// A wait is read under the scope of its session at declaration. One the
// caller may not read answers exactly as an unknown id. Titles follow D23:
// the subject of a scope the caller may not read is dropped (its id and its
// title), and so is the workspace title of a session bound to it — the
// session reads by its own name (redactPeers, then labelOf).

// ListWaits handles GET /waits.
func (rl *Relay) ListWaits(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := callerOf(r).scope
		q := r.URL.Query()
		all := q.Get("all")
		closed := 0
		if v := q.Get("closed"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > store.MaxClosedWaitHistory {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
					fmt.Sprintf("closed must be 0..%d", store.MaxClosedWaitHistory), nil)
				return
			}
			closed = n
		}
		f := store.WaitRecordFilter{Scope: scope.Any, Now: rl.cfg.Now(), IncludeSnoozed: all == "1" || all == "true",
			SubjectID: strings.TrimSpace(q.Get("subject")), SessionID: strings.TrimSpace(q.Get("session"))}
		recs, err := rl.svc.Store.ListOpenWaitRecords(f)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if closed > 0 {
			// The history is read in the caller's scope, so `closed` counts
			// readable records only; waitReadable below still decides.
			hf := f
			if !scope.SeesAll(caller) {
				hf.Scope = caller
			}
			old, err := rl.svc.Store.ListRecentClosedWaitRecords(hf, closed)
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			recs = append(recs, old...)
		}
		out := make([]pigtail.Wait, 0, len(recs))
		for i := range recs {
			if waitReadable(caller, &recs[i]) {
				out = append(out, rl.waitView(caller, &recs[i]))
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// GetWait handles GET /waits/{id}.
func (rl *Relay) GetWait(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, ok := rl.waitFor(r, chi.URLParam(r, "id"))
		if !ok {
			noSuchWait(w, r, resp)
			return
		}
		resp.JSON(w, r, http.StatusOK, rl.waitView(callerOf(r).scope, rec))
	}
}

// noSuchWait is the one answer for an unknown wait and for one the caller
// may not read.
func noSuchWait(w http.ResponseWriter, r *http.Request, resp Responder) {
	resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such wait", nil)
}

// waitFor returns the wait id when the caller may read it; false for an
// unknown id and for a wait of a scope the caller may not read alike. A store
// error reads as unknown too (fail-closed) and is logged.
func (rl *Relay) waitFor(r *http.Request, id string) (*store.WaitRecord, bool) {
	rec, err := rl.svc.Store.GetWaitRecord(id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			rl.svc.logf("relay: wait %s: %v", id, err)
		}
		return nil, false
	}
	if !waitReadable(callerOf(r).scope, rec) {
		return nil, false
	}
	return rec, true
}

// waitReadable reports whether caller may read the wait. A record with no
// scope is the master's (D17).
func waitReadable(caller scope.Scope, rec *store.WaitRecord) bool {
	sc := scope.Master()
	if strings.TrimSpace(rec.Scope) != "" {
		sc = scope.Canonical(rec.Scope)
	}
	return scope.CanRead(caller, sc)
}

// waitView is the wait as caller reads it: its session's label and its
// subject's short title, each redacted under D23.
func (rl *Relay) waitView(caller scope.Scope, rec *store.WaitRecord) pigtail.Wait {
	w := rec.Wait
	if p, err := rl.svc.Store.GetPeer(w.SessionID); err == nil {
		one := []pigtail.PeerSession{*p}
		rl.redactPeers(caller, one)
		w.SessionLabel = rl.labelOf(&one[0])
		w.SessionEnded = rl.emitterGone(*p) // D29-A: the wait outlives it
	}
	w.DeliveredToLabel = ""
	if w.DeliveredTo != "" {
		w.DeliveredToLabel = w.DeliveredTo
		if p, err := rl.svc.Store.GetPeer(w.DeliveredTo); err == nil {
			one := []pigtail.PeerSession{*p}
			rl.redactPeers(caller, one) // D23: the recipient's title under the caller's scope
			w.DeliveredToLabel = rl.labelOf(&one[0])
		}
	}
	w.SubjectShortTitle = ""
	if w.SubjectID != "" {
		if !rl.subjectVisible(caller, w.SubjectID) {
			w.SubjectID = ""
		} else if sub, err := rl.svc.Store.GetSubject(w.SubjectID); err == nil {
			w.SubjectShortTitle = pigtail.ShortTitle(*sub)
		}
	}
	return w
}

// SnoozeWait handles POST /waits/{id}/snooze (Task 11): the open wait leaves
// GET /waits until the given time. No timer: the return is computed at read
// against cfg.Now (ListOpenWaitRecords). The session is not touched — no
// message, no marker, no Standby change. Refused from an agent session (D3), like the answer. Same scope rules as the answer: an
// unreadable wait reads as unknown, a write needs CanWrite.
func (rl *Relay) SnoozeWait(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// D3: like the answer, a guard against an agent's mistake, checked
		// first: the CLI of an agent session sends its session id.
		if r.Header.Get(pigtail.HeaderSession) != "" {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrUnauthorized, "snooze from your terminal, not from an agent session", nil)
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
		var req pigtail.WaitSnoozeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.Until.IsZero() || !req.Until.After(rl.cfg.Now()) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "until must be a time in the future", nil)
			return
		}
		done, err := rl.svc.Store.SnoozeWait(id, req.Until)
		switch {
		case errors.Is(err, store.ErrNotFound):
			noSuchWait(w, r, resp)
			return
		case errors.Is(err, store.ErrWaitClosed):
			state := "closed"
			if cur, gerr := rl.svc.Store.GetWaitRecord(id); gerr == nil {
				state = cur.State
			}
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "wait "+id+" is "+state, nil)
			return
		case err != nil:
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		rl.publishWaitSnoozed(done)
		resp.JSON(w, r, http.StatusOK, rl.waitView(c.scope, done))
	}
}
