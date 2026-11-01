package handlers

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Standby (sub_x000000000000041, D4, D16, D24): a session parked by /standby.
// Two session-bound routes of the relay — the caller acts as the session
// (actingAs), else the unknown-session answer:
//
//	PUT    /peers/{session}/standby   enter Standby (a new /standby starts over)
//	DELETE /peers/{session}/standby   leave it (a success when not in Standby)
//
// Rows live in session_standby (migration 0023) and outlive SessionEnd (D24).
// A marker file per standby session (StandbyMarkerDir) is the hooks' and the
// CLI's reading of the same state. One predicate in the daemon: inStandby —
// the only reader of GetStandby outside the store
// (TestStandbyReadOnlyThroughPredicate).

// inStandby is the daemon's Standby predicate: a row → (true, "standby
// (D16)"); a read error → (true, "standby unreadable: …"), never "not in
// Standby"; no row → (false, "").
func (rl *Relay) inStandby(session string) (bool, string) {
	sb, err := rl.standbyRow(session)
	if err != nil {
		return true, "standby unreadable: " + err.Error()
	}
	if sb != nil {
		return true, standbyReason
	}
	return false, ""
}

// standbyRow reads the session's Standby row (nil = none) through the same
// seam as inStandby: for a reader that needs the row's fields (the idle
// pause's compaction check, T-k).
func (rl *Relay) standbyRow(session string) (*pigtail.SessionStandby, error) {
	get := rl.getStandby
	if get == nil {
		get = rl.svc.Store.GetStandby
	}
	return get(session)
}

// standbyReason is inStandby's reason for a readable Standby row.
const standbyReason = "standby (D16)"

// SetSessionStandby handles PUT /peers/{session}/standby.
func (rl *Relay) SetSessionStandby(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := rl.actingAs(callerOf(r).scope, chi.URLParam(r, "session"))
		if !ok {
			unknownSession(w, r, resp)
			return
		}
		now := rl.cfg.Now()
		if err := rl.svc.Store.EnterStandby(p.SessionID, now); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		rl.refreshStandbyMarker(p.SessionID)
		rl.publishStandby(*p, true)
		// What EnterStandby wrote: a fresh row, no compaction yet, and why
		// the tab will not move into « Standby », if it will not. The pin
		// (the prio) is taken off by the CLI before this call (D18).
		resp.JSON(w, r, http.StatusOK, pigtail.SessionStandby{
			SessionID: p.SessionID, Since: now.UTC().Truncate(time.Millisecond),
			FoldersOff: rl.standbyFoldersOff(),
		})
	}
}

// standbyFoldersOff is why the sidebar loop will not file a standby tab in
// the « Standby » folder: the loop off, or its folder arrangement off. ""
// when the folders act, or when no loop is wired (no guess).
func (rl *Relay) standbyFoldersOff() string {
	if rl.svc.CmuxSidebar == nil {
		return ""
	}
	st := rl.svc.CmuxSidebar.State()
	switch {
	case !st.Enabled:
		return "cmux sidebar off"
	case !st.Folders:
		return "pigtaild --cmux-folders=false"
	}
	return ""
}

// ClearSessionStandby handles DELETE /peers/{session}/standby. Clearing what
// is not there is a success: the prompt hook clears on its marker alone.
func (rl *Relay) ClearSessionStandby(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := rl.actingAs(callerOf(r).scope, chi.URLParam(r, "session"))
		if !ok {
			unknownSession(w, r, resp)
			return
		}
		gone, err := rl.svc.Store.ClearStandby(p.SessionID, rl.cfg.Now())
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		rl.refreshStandbyMarker(p.SessionID)
		if gone {
			rl.publishStandby(*p, false)
		}
		resp.JSON(w, r, http.StatusOK, map[string]any{"session_id": p.SessionID, "cleared": gone})
	}
}

// EndStandbyGone ends the Standby of a session whose cmux workspace is gone
// (two successful listings without it, cmuxsync closeMissing): row, marker,
// session.standby_changed (standby:false) and a log line. A row already gone is
// a success and publishes nothing.
func (rl *Relay) EndStandbyGone(session, workspace string) error {
	gone, err := rl.svc.Store.ClearStandby(session, rl.cfg.Now())
	if err != nil {
		return err
	}
	rl.refreshStandbyMarker(session)
	if !gone {
		return nil
	}
	p := pigtail.PeerSession{SessionID: session, CmuxWorkspaceID: workspace}
	if got, err := rl.svc.Store.GetPeer(session); err == nil && got != nil {
		p = *got
	}
	rl.publishStandby(p, false)
	rl.svc.logf("relay: %s standby ended: workspace %s closed", session, workspace)
	return nil
}

// EndStandbyPinned ends the Standby of a session whose tab was pinned by hand
// (D29 of sub_x000000000000014: a pin is a prio, the last gesture wins; read
// by cmuxsync in the listing): row, marker, session.standby_changed
// (standby:false) and a log line. A row already gone is a success and
// publishes nothing.
func (rl *Relay) EndStandbyPinned(session, workspace string) error {
	gone, err := rl.svc.Store.ClearStandby(session, rl.cfg.Now())
	if err != nil {
		return err
	}
	rl.refreshStandbyMarker(session)
	if !gone {
		return nil
	}
	p := pigtail.PeerSession{SessionID: session, CmuxWorkspaceID: workspace}
	if got, err := rl.svc.Store.GetPeer(session); err == nil && got != nil {
		p = *got
	}
	rl.publishStandby(p, false)
	rl.svc.logf("relay: standby cleared by pin: %s (workspace %s)", session, workspace)
	return nil
}

// publishStandby emits session.standby_changed, tagged with the session's
// scope like publishWait.
func (rl *Relay) publishStandby(p pigtail.PeerSession, standby bool) {
	if rl.svc.Bus == nil {
		return
	}
	rl.svc.Bus.Publish(pigtail.Event{Type: pigtail.EventSessionStandbyChanged, Scope: string(sessionScope(p)), Data: map[string]interface{}{
		"session_id": p.SessionID, "cmux_workspace_id": p.CmuxWorkspaceID, "standby": standby,
	}})
}

// refreshStandbyMarker makes the session's Standby marker reflect the
// predicate: present while inStandby holds (an unreadable row included),
// absent otherwise.
func (rl *Relay) refreshStandbyMarker(session string) {
	if rl.cfg.StandbyMarkerDir == "" || !validSessionKey(session) {
		return
	}
	path := filepath.Join(rl.cfg.StandbyMarkerDir, session)
	if in, why := rl.inStandby(session); !in {
		_ = os.Remove(path)
		return
	} else if strings.HasPrefix(why, "standby unreadable") {
		rl.svc.logf("relay: %s %s: marker kept", session, why)
	}
	rl.writeStandbyMarker(session)
}

func (rl *Relay) writeStandbyMarker(session string) {
	if err := os.MkdirAll(rl.cfg.StandbyMarkerDir, 0o700); err != nil {
		rl.svc.logf("relay: standby marker dir: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(rl.cfg.StandbyMarkerDir, session), []byte("standby\n"), 0o600); err != nil {
		rl.svc.logf("relay: write standby marker: %v", err)
	}
}

// syncStandbyMarkers rebuilds the Standby markers from the table at start,
// in one read. A read error leaves the markers as they are: a marker read as
// absent would let a hook take a standby session for a free one.
func (rl *Relay) syncStandbyMarkers() {
	if rl.cfg.StandbyMarkerDir == "" {
		return
	}
	all, err := rl.svc.Store.ListStandby()
	if err != nil {
		rl.svc.logf("relay: list standby: %v", err)
		return
	}
	keep := map[string]bool{}
	for _, sb := range all {
		if validSessionKey(sb.SessionID) {
			keep[sb.SessionID] = true
			rl.writeStandbyMarker(sb.SessionID)
		}
	}
	entries, _ := os.ReadDir(rl.cfg.StandbyMarkerDir)
	for _, e := range entries {
		if !e.IsDir() && !keep[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
			_ = os.Remove(filepath.Join(rl.cfg.StandbyMarkerDir, e.Name()))
		}
	}
}

// InStandby is inStandby for the daemon's input probe (sub_x000000000000024,
// D16): a Standby row → (true, nil); an unreadable row → (true, err), never
// "not in Standby"; no row → (false, nil). It wraps the one predicate, no
// new reader of the table.
func (rl *Relay) InStandby(session string) (bool, error) {
	in, why := rl.inStandby(session)
	if in && why != standbyReason {
		return true, errors.New(why)
	}
	return in, nil
}
