package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/waitdetail"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Typed waits (spec "Sidebar pigtail" §4.8, §5.3): what a session waits for
// from its user. Two session-bound routes of the relay — the caller acts as
// the session, so its scope must be the session's (actingAs), else the
// unknown-session answer:
//
//	POST   /peers/{session}/wait  {type, reason?, source?, details?}  record (last told wins,
//	                                                        except a bare follow-up of a
//	                                                        typed dialog: waittype.Supersedes)
//	DELETE /peers/{session}/wait  ?source=auto|declared|held|all[&holder_pid=N] | ?keep=subject  clear
//
// A held wait (source=held, type mac, holder_pid required) is kept by the
// process `the Mac-hold helper` and ends with it: a bare DELETE clears auto and
// declared only and spares it; source=held clears it, with holder_pid only
// that holder's row; source=all clears the three.
//
// Rows live in session_waits (migration 0013); a marker file per waiting
// session (WaitMarkerDir) lets the prompt hook skip the daemon when there is
// nothing to clear. session.wait_changed never carries the reason.

// SetSessionWait handles POST /peers/{session}/wait.
func (rl *Relay) SetSessionWait(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SessionWaitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		p, ok := rl.actingAs(callerOf(r).scope, chi.URLParam(r, "session"))
		if !ok {
			unknownSession(w, r, resp)
			return
		}
		parse := waittype.Parse
		switch req.Source {
		case pigtail.WaitSourceAuto:
			parse = waittype.ParseAuto // hook-typed failure states (StopFailure)
		case pigtail.WaitSourceHeld:
			parse = waittype.ParseHeld // mac, kept by the Mac-hold helper
		}
		t, err := parse(req.Type)
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		src := req.Source
		if src == "" {
			src = pigtail.WaitSourceDeclared
		}
		if src != pigtail.WaitSourceAuto && src != pigtail.WaitSourceDeclared && src != pigtail.WaitSourceHeld {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "source must be auto, declared or held", nil)
			return
		}
		if src == pigtail.WaitSourceHeld && req.HolderPID <= 0 {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "a held wait needs holder_pid (the process that holds it)", nil)
			return
		}
		reason := strings.Join(strings.Fields(req.Reason), " ")
		if len(reason) > waittype.MaxReasonBytes {
			refuse(w, r, resp, "reason is "+strconv.Itoa(len(reason))+" bytes, the limit is "+strconv.Itoa(waittype.MaxReasonBytes)+": a few words, the details stay in the conversation")
			return
		}
		if name := relay.SecretShape(reason); name != "" {
			refuse(w, r, resp, "reason looks like it carries a secret ("+name+")")
			return
		}
		details, ok := rl.waitDetails(w, r, resp, t, src, req.Details)
		if !ok {
			return
		}
		if src == pigtail.WaitSourceAuto && !rl.autoSupersedes(p.SessionID, t, reason) {
			rl.writePeer(w, r, resp, p.SessionID) // the standing dialog wait stays (D3), its record too
			return
		}
		wait := pigtail.SessionWait{Type: string(t), Reason: reason, Source: src, Since: rl.cfg.Now()}
		if (t == waittype.Limit || t == waittype.Mac) && req.Until != nil {
			u := req.Until.UTC()
			wait.Until = &u
		}
		if src == pigtail.WaitSourceHeld {
			wait.HolderPID = req.HolderPID
		}
		if err := rl.recordWait(*p, wait, details); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		rl.writePeer(w, r, resp, p.SessionID)
	}
}

// waitDetails validates the typed content of a wait (waitdetail.Validate) and
// returns it as the JSON to keep (nil = none). A held wait takes none (400).
// A declared wait's invalid content is a 400 and nothing is written. An auto
// wait comes from a hook that fails open: invalid content is dropped and
// logged, the wait is still recorded; an auto wait that is no dialog keeps
// none. ok=false when the answer was written.
func (rl *Relay) waitDetails(w http.ResponseWriter, r *http.Request, resp Responder, t waittype.Type, src string, raw json.RawMessage) ([]byte, bool) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, true
	}
	switch src {
	case pigtail.WaitSourceHeld:
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "a held wait takes no details", nil)
		return nil, false
	case pigtail.WaitSourceAuto:
		if !waittype.DialogType(t) {
			return nil, true
		}
		d, err := waitdetail.Validate(t, raw)
		if err != nil {
			rl.svc.logf("relay: auto wait %s: details dropped: %v", t, err)
			return nil, true
		}
		return marshalDetails(d), true
	}
	d, err := waitdetail.Validate(t, raw)
	if err != nil {
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
		return nil, false
	}
	return marshalDetails(d), true
}

// marshalDetails is the JSON kept for validated details: the known fields
// only, compact. nil for none.
func marshalDetails(d *pigtail.WaitDetails) []byte {
	if d == nil {
		return nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	return b
}

// autoSupersedes tells whether an automatic wait replaces the session's
// standing one (waittype.Supersedes, D3). A store error fails open: the last
// told wins, as before.
func (rl *Relay) autoSupersedes(session string, t waittype.Type, reason string) bool {
	ws, err := rl.svc.Store.SessionWaits(session)
	if err != nil {
		return true
	}
	for _, w := range ws {
		if w.Source == pigtail.WaitSourceAuto {
			return waittype.Supersedes(waittype.Type(w.Type), w.Reason, t, reason)
		}
	}
	return true
}

// ClearSessionWait handles DELETE /peers/{session}/wait?source=…[&holder_pid=N]. Clearing
// what is not there is a success: the prompt hook clears without knowing.
func (rl *Relay) ClearSessionWait(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := rl.actingAs(callerOf(r).scope, chi.URLParam(r, "session"))
		if !ok {
			unknownSession(w, r, resp)
			return
		}
		q := r.URL.Query()
		if q.Has("keep") {
			// The clear of the done command's prompt — the turn's waits go, the
			// subject's wait the session emitted stays open.
			if q.Get("keep") != "subject" || q.Has("source") || q.Has("holder_pid") {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "keep is subject and goes alone (the turn's waits)", nil)
				return
			}
			if err := rl.ClearWaitsKeepingSubject(p.SessionID); err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			rl.writePeer(w, r, resp, p.SessionID)
			return
		}
		var sources []string
		switch src := q.Get("source"); src {
		case "":
			sources = []string{pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared}
		case "all":
			sources = []string{pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared, pigtail.WaitSourceHeld}
		case pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared, pigtail.WaitSourceHeld:
			sources = []string{src}
		default:
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "source must be auto, declared, held or all", nil)
			return
		}
		if q.Has("holder_pid") {
			pid, err := strconv.Atoi(q.Get("holder_pid"))
			if q.Get("source") != pigtail.WaitSourceHeld || err != nil || pid <= 0 {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "holder_pid is a positive integer and goes with source=held", nil)
				return
			}
			if _, err := rl.ClearHeldWait(p.SessionID, pid); err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			rl.writePeer(w, r, resp, p.SessionID)
			return
		}
		if err := rl.ClearWaits(p.SessionID, sources...); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		rl.writePeer(w, r, resp, p.SessionID)
	}
}

// ClearWaits drops a session's waits of the given sources (none = auto and
// declared: a held wait is never a turn's), refreshes its marker and, when
// something went, publishes the change with the sources it covered. The
// prompt, the turn end and the DELETE route call it: the session is alive and
// moves on, so its subject's wait it emitted goes too.
func (rl *Relay) ClearWaits(session string, sources ...string) error {
	return rl.ClearWaitsKeeping(session, false, sources...)
}

// ClearWaitsKeeping is ClearWaits; with gone — the sidebar loop's clear of
// a session it judged gone — the wait of its subject the session emitted
// stays open (D29-A) and an answer to its subject's wait it was handed but
// never read is kept again (D29-C, as at its end): its marker is refreshed,
// the relay's tick hands the answer to the subject's next session.
func (rl *Relay) ClearWaitsKeeping(session string, gone bool, sources ...string) error {
	if len(sources) == 0 {
		sources = []string{pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared}
	}
	n, closed, reheld, err := rl.svc.Store.ClearSessionWaitsClosing(session, gone, sources...)
	if err != nil {
		return err
	}
	if len(reheld) > 0 {
		rl.refreshMarker(session) // its expired answers left the inbox
	}
	rl.afterClear(session, n > 0, sources, closed)
	return nil
}

// ClearWaitsKeepingSubject is the clear of the done command's prompt
// (waittype.DonePrompt): the turn's waits (auto, declared) go, the wait of
// its subject the session emitted stays open — closing answers nothing, the
// subject's next session receives
// the answer (D29-A, D29-C). The session lives on: nothing is kept again.
func (rl *Relay) ClearWaitsKeepingSubject(session string) error {
	n, closed, _, err := rl.svc.Store.ClearSessionWaitsKeepingSubject(session)
	if err != nil {
		return err
	}
	rl.afterClear(session, n > 0, []string{pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared}, closed)
	return nil
}

// ClearHeldWait drops the session's held wait only if holderPID still holds
// it, refreshes its marker and, when the row went, publishes the change with
// source held. The sidebar loop calls it when a held wait's holder is gone or
// its deadline passed; the DELETE route with holder_pid shares this path.
func (rl *Relay) ClearHeldWait(session string, holderPID int) (bool, error) {
	gone, err := rl.svc.Store.ClearHeldWait(session, holderPID)
	if err != nil {
		return false, err
	}
	rl.afterClear(session, gone, []string{pigtail.WaitSourceHeld}, nil)
	return gone, nil
}

// afterClear refreshes the marker and, when a row went, publishes the change
// and wait.closed (cleared) for each record the clear closed.
func (rl *Relay) afterClear(session string, gone bool, sources, closed []string) {
	rl.refreshWaitMarker(session)
	if !gone {
		return
	}
	if p, err := rl.svc.Store.GetPeer(session); err == nil {
		rl.publishWait(*p, "", strings.Join(sources, ","))
		rl.publishWaitsCleared(*p, closed)
	}
}

func (rl *Relay) writePeer(w http.ResponseWriter, r *http.Request, resp Responder, session string) {
	p, err := rl.svc.Store.GetPeer(session)
	if err != nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		return
	}
	one := []pigtail.PeerSession{*p}
	rl.redactPeers(callerOf(r).scope, one)
	resp.JSON(w, r, http.StatusOK, rl.decorate(one[0]))
}

// restoreEndedWaits gives the session back the declared wait a session end (or
// the sidebar judging it gone) took from it, when the same id registers again
// (cmux resumes `claude --resume <id>` after a restart; D1: an idle pause must
// not stop a session whose wait holds it). Only a registration that
// proves the session lives: the SessionStart hook, or a snapshot whose process
// runs and still names it — else the 30 s snapshot of a dead session would
// restore the wait then see it cleared again. Logged and published like a
// declaration; a store error is logged, never fatal to the registration.
func (rl *Relay) restoreEndedWaits(sessionID, source string) {
	if source != "hook" {
		p, err := rl.svc.Store.GetPeer(sessionID)
		if err != nil || p.PID <= 0 || !rl.sessionAlive(p.ConfigDir, p.PID, sessionID) {
			return
		}
	}
	restored, err := rl.svc.Store.RestoreEndedWaits(sessionID)
	if err != nil {
		rl.svc.logf("wait: %s restore after session end failed: %v", short8(sessionID), err)
		return
	}
	if len(restored) == 0 {
		return
	}
	rl.refreshWaitMarker(sessionID)
	p, err := rl.svc.Store.GetPeer(sessionID)
	if err != nil {
		return
	}
	for _, w := range restored {
		rl.svc.logf("wait: %s restored %s (declared) after session end", short8(sessionID), w.Type)
		rl.publishWait(*p, w.Type, w.Source)
	}
	if ids, err := rl.svc.Store.OpenWaitRecordIDs(sessionID); err == nil {
		for _, id := range ids {
			if rec, err := rl.svc.Store.GetWaitRecord(id); err == nil && rec.Source == pigtail.WaitSourceDeclared {
				rl.publishWaitEvent(pigtail.EventWaitOpened, id, sessionID, sessionScope(*p), pigtail.WaitStateOpen)
			}
		}
	}
}

// publishWait emits session.wait_changed, tagged with the session's scope:
// a default-scope reader never learns a perso session id. Never the reason.
func (rl *Relay) publishWait(p pigtail.PeerSession, typ, source string) {
	if rl.svc.Bus == nil {
		return
	}
	rl.svc.Bus.Publish(pigtail.Event{Type: pigtail.EventSessionWaitChanged, Scope: string(sessionScope(p)), Data: map[string]interface{}{
		"session_id": p.SessionID, "cmux_workspace_id": p.CmuxWorkspaceID, "type": typ, "source": source,
	}})
}

// refreshWaitMarker makes the session's wait marker reflect its rows:
// present while it has a wait, absent otherwise.
func (rl *Relay) refreshWaitMarker(session string) {
	if rl.cfg.WaitMarkerDir == "" || !validSessionKey(session) {
		return
	}
	path := filepath.Join(rl.cfg.WaitMarkerDir, session)
	ws, err := rl.svc.Store.SessionWaits(session)
	if err != nil {
		rl.svc.logf("relay: session waits: %v", err)
		return
	}
	if len(ws) == 0 {
		_ = os.Remove(path)
		return
	}
	if err := os.MkdirAll(rl.cfg.WaitMarkerDir, 0o700); err != nil {
		rl.svc.logf("relay: wait marker dir: %v", err)
		return
	}
	if err := os.WriteFile(path, []byte(ws[len(ws)-1].Type+"\n"), 0o600); err != nil {
		rl.svc.logf("relay: write wait marker: %v", err)
	}
}

// syncWaitMarkers rebuilds the wait markers from the store at start.
func (rl *Relay) syncWaitMarkers() {
	if rl.cfg.WaitMarkerDir == "" {
		return
	}
	all, err := rl.svc.Store.ListSessionWaits()
	if err != nil {
		rl.svc.logf("relay: session waits: %v", err)
		return
	}
	for s := range all {
		rl.refreshWaitMarker(s)
	}
	entries, _ := os.ReadDir(rl.cfg.WaitMarkerDir)
	for _, e := range entries {
		if !e.IsDir() && len(all[e.Name()]) == 0 && !strings.HasPrefix(e.Name(), ".") {
			_ = os.Remove(filepath.Join(rl.cfg.WaitMarkerDir, e.Name()))
		}
	}
}

// short8 is the first 8 characters of a session id, for a log line.
func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
