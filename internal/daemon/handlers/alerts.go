package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/diskwatch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Alerts (subject « abandon respawn visible », D3/D5): an alert is a
// respawn-abandoned|failed journal line (written by NotifyRespawnEnd) with no
// alert-resolved line of the same run in the same scope. Derived from the
// journal alone — nothing in memory — so a daemon restart keeps them.
//
// A second kind, disk-low (diskwatch), is a machine alert: no scope, no subject,
// shown to every caller, resolved by a disk-ok line and never acknowledged.

const (
	alertResolvedKind   = "alert-resolved"
	alertResolvedPrefix = "alert-resolved:"
	alertsPage          = pigtail.JournalListMax // alert lines per page; every page is read
	alertsIDChunk       = 500                    // ids per resolution query, under SQLite's variable limit
)

// RegisterAlertRoutes mounts the alert routes behind scopeMW: a respawn or
// role alert carries a subject id and title (D19), the machine's disk alert
// carries none (no scope, no subject). Pigtail reads them as the master.
// The ack acts as the session it names (classSessionBound); the resolve
// closes the alerts of a subject in the scopes the caller writes.
// Not behind FreezeMW: a maintenance must not hold an acknowledgement back.
func RegisterAlertRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Group(func(r chi.Router) {
		r.Use(scopeMW(resp))
		r.Get("/alerts", ListAlerts(svc, resp))
		r.Post("/alerts/resolve", ResolveSubjectAlerts(svc, resp))
		r.Post("/alerts/{id}/ack", AckAlert(svc, resp))
	})
}

// openAlerts is every open respawn alert the filter sc lets through, oldest
// first: every alert line, page by page, then the resolutions of their runs
// only — never a window of the latest lines, out of which an open alert would
// vanish or a closed one come back.
func openAlerts(st *store.Store, sc scope.Scope) ([]pigtail.JournalEntry, error) {
	var lines []pigtail.JournalEntry
	var ids []string
	for _, state := range []string{pigtail.CmuxRespawnAbandoned, pigtail.CmuxRespawnFailed} {
		seen := map[string]bool{} // "<scope>|<id>": a line written while paging shifts the pages by one
		for off := 0; ; off += alertsPage {
			es, err := st.ListJournal(store.JournalFilter{Scope: sc, Kind: respawnJournalKindPrefix + state, Limit: alertsPage, Offset: off})
			if err != nil {
				return nil, err
			}
			for _, e := range es {
				if seen[e.Scope+"|"+e.ID] {
					continue
				}
				seen[e.Scope+"|"+e.ID] = true
				if run, ok := respawnRunOf(e); ok {
					lines = append(lines, e)
					ids = append(ids, alertResolvedPrefix+run)
				}
			}
			if len(es) < alertsPage {
				break
			}
		}
	}
	resolved := map[string]bool{} // "<scope>|<run>"
	for len(ids) > 0 {
		n := min(len(ids), alertsIDChunk)
		rs, err := st.ListJournal(store.JournalFilter{Scope: sc, Kind: alertResolvedKind, IDs: ids[:n], Limit: n * 2}) // a run id may sit in two scopes
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			resolved[r.Scope+"|"+strings.TrimPrefix(r.ID, alertResolvedPrefix)] = true
		}
		ids = ids[n:]
	}
	var out []pigtail.JournalEntry
	for _, e := range lines {
		if run, _ := respawnRunOf(e); !resolved[e.Scope+"|"+run] {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// respawnRunOf reads the run id of a respawn line, id "respawn:<run>:<state>".
func respawnRunOf(e pigtail.JournalEntry) (string, bool) {
	state := strings.TrimPrefix(e.Kind, respawnJournalKindPrefix)
	run, ok := strings.CutPrefix(e.ID, "respawn:")
	if !ok {
		return "", false
	}
	run, ok = strings.CutSuffix(run, ":"+state)
	return run, ok && run != ""
}

// respawnDetailOf is the line's detail: its summary without the
// "respawn <state> <run>: " NotifyRespawnEnd puts before it, nor the
// "— no live session to tell" after it. A summary of another shape is shown whole.
func respawnDetailOf(e pigtail.JournalEntry, run string) string {
	s := strings.TrimSuffix(e.Summary, respawnNoOne)
	if d, ok := strings.CutPrefix(s, "respawn "+strings.TrimPrefix(e.Kind, respawnJournalKindPrefix)+" "+run+": "); ok {
		return d
	}
	return s
}

// ListAlerts handles GET /alerts. A store error is a 500 (the bar keeps its
// last list and says the read failed), never an empty list.
func ListAlerts(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := callerOf(r).scope
		sc, err := scope.ListFilter(caller, "")
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		es, err := openAlerts(svc.Store, sc)
		if err != nil {
			svc.logf("alerts: journal unreadable: %v", err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		now := time.Now().UTC()
		out := pigtail.AlertsResponse{Alerts: []pigtail.AlertView{}, ReadAt: now}
		// The disk alert belongs to the machine, not to a scope: it is read by
		// its own query under the master scope, whoever asks, and carries no
		// scope, subject or session (D2, 2026-10-31).
		disk, err := diskwatch.OpenAlert(svc.Store)
		if err != nil {
			svc.logf("alerts: journal unreadable: %v", err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		diskDone := disk == nil
		for _, e := range es {
			if !diskDone && disk.Timestamp.Before(e.Timestamp) {
				out.Alerts = append(out.Alerts, diskAlertView(*disk, now))
				diskDone = true
			}
			run, _ := respawnRunOf(e)
			v := pigtail.AlertView{ID: run, Kind: pigtail.AlertKindRespawn, State: strings.TrimPrefix(e.Kind, respawnJournalKindPrefix),
				Scope: e.Scope, SubjectID: e.SubjectID, Detail: respawnDetailOf(e, run), CreatedAt: e.Timestamp,
				AgeSeconds: max(0, int(now.Sub(e.Timestamp).Seconds()))}
			if e.SubjectID != "" {
				v.SubjectTitle = alertSubjectTitle(svc, caller, e.SubjectID)
			}
			out.Alerts = append(out.Alerts, v)
		}
		if !diskDone {
			out.Alerts = append(out.Alerts, diskAlertView(*disk, now))
		}
		rv, err := roleAlertViews(svc, caller, sc, now)
		if err != nil {
			svc.logf("alerts: journal unreadable: %v", err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		out.Alerts = append(out.Alerts, rv...)
		sort.SliceStable(out.Alerts, func(i, j int) bool {
			if !out.Alerts[i].CreatedAt.Equal(out.Alerts[j].CreatedAt) {
				return out.Alerts[i].CreatedAt.Before(out.Alerts[j].CreatedAt)
			}
			return out.Alerts[i].ID < out.Alerts[j].ID
		})
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// diskAlertView is the alert for an open disk-low line. Its id is the line's;
// Scope stays empty and the detail names the free space only.
func diskAlertView(e pigtail.JournalEntry, now time.Time) pigtail.AlertView {
	detail := "low disk space"
	free, ok := diskwatch.FreeBytesOf(e.Summary)
	if ok {
		detail = fmt.Sprintf("%d GB free on disk", free>>30)
	}
	floor, _ := diskwatch.FloorBytesOf(e.Summary)
	return pigtail.AlertView{ID: e.ID, Kind: pigtail.AlertKindDisk, State: "low", Detail: detail,
		FreeBytes: free, FloorBytes: floor, CreatedAt: e.Timestamp, AgeSeconds: max(0, int(now.Sub(e.Timestamp).Seconds()))}
}

// alertSubjectTitle is the title shown on an alert, "" when the caller cannot
// read the subject's scope or the subject is gone (the bar falls back on the
// id). A store error other than not-found is logged, never silent.
func alertSubjectTitle(svc *Services, caller scope.Scope, id string) string {
	sc, err := svc.Store.SubjectScope(id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			svc.logf("alerts: subject %s unreadable: %v", id, err)
		}
		return ""
	}
	if !scope.CanRead(caller, sc) {
		return ""
	}
	sub, err := svc.Store.GetSubject(id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			svc.logf("alerts: subject %s unreadable: %v", id, err)
		}
		return ""
	}
	return pigtail.ShortTitle(*sub)
}

// alertWriteRefusal is the 403 message of an ack on another scope's alert,
// as journalWriteRefusal: the --cross-scope hint to the master caller only.
func alertWriteRefusal(caller, target scope.Scope) string {
	msg := fmt.Sprintf("a %s caller cannot resolve a %s alert", caller, target)
	if scope.SeesAll(caller) {
		msg += " (a master caller writing another scope: --cross-scope)"
	}
	return msg
}

// alertCauseMax bounds the cause of a resolve (one line).
const alertCauseMax = 120

// resolveAlert writes the resolution line of run (e is its respawn line).
// created is false when the run was resolved already: the stored line is returned.
func resolveAlert(svc *Services, e pigtail.JournalEntry, run, by string, now time.Time) (pigtail.JournalEntry, bool, error) {
	res := pigtail.JournalEntry{ID: alertResolvedPrefix + run, Timestamp: now, Kind: alertResolvedKind, Scope: e.Scope,
		SubjectID: e.SubjectID, Summary: clipRunes("respawn alert "+run+" resolved: "+by, pigtail.JournalSummaryMax),
		Source: "pigtail:alerts/" + run}
	created, err := svc.Store.AddJournal(&res)
	if err == nil && created {
		svc.logf("alert %s resolved: %s", run, by)
	}
	return res, created, err
}

// findRespawnLine is the respawn line of run the filter sc lets through, open or not.
func findRespawnLine(st *store.Store, sc scope.Scope, run string) (*pigtail.JournalEntry, error) {
	es, err := st.ListJournal(store.JournalFilter{Scope: sc, IDs: []string{
		"respawn:" + run + ":" + pigtail.CmuxRespawnAbandoned,
		"respawn:" + run + ":" + pigtail.CmuxRespawnFailed,
	}})
	if err != nil {
		return nil, err
	}
	for i := range es {
		if r, ok := respawnRunOf(es[i]); ok && r == run {
			return &es[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// respawnToldSession is the session the notice of run told, as the line's
// source names it (respawnSource, Task 4); "" when nobody was told.
func respawnToldSession(e pigtail.JournalEntry, run string) string {
	told, ok := strings.CutPrefix(e.Source, "pigtail:respawn/"+run+"?session=")
	if !ok {
		return ""
	}
	return told
}

// AckAlert handles POST /alerts/{id}/ack (D5). With a session_id, the session
// must act in the caller's scope (else the unknown-session answer) and be the
// session the notice told or a session of the alert's subject (else 409,
// nothing written); without one, the alert is acknowledged by hand. Either
// way the caller must write the alert's scope. Idempotent: a second ack says
// the first resolution back.
func AckAlert(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		run := chi.URLParam(r, "id")
		if strings.HasPrefix(run, diskwatch.KindLow+":") {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"the disk alert cannot be acknowledged: it closes by itself once the free space is back above the alert threshold", nil)
			return
		}
		roleAlert := isRoleAlertID(run)
		if !roleAlert && !respawnIDRe.MatchString(run) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, fmt.Sprintf("alert id must match %s, got %q", respawnIDRe, run), nil)
			return
		}
		var req pigtail.AlertAckRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.SessionID != "" && !respawnIDRe.MatchString(req.SessionID) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, fmt.Sprintf("session_id must match %s, got %q", respawnIDRe, req.SessionID), nil)
			return
		}
		if roleAlert {
			ackRoleAlert(w, r, svc, resp, run, req)
			return
		}
		c := callerOf(r)
		sc, err := scope.ListFilter(c.scope, "")
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		notFound := "no alert " + run
		e, err := findRespawnLine(svc.Store, sc, run)
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, notFound, nil)
			return
		}
		if err != nil {
			svc.logf("alerts: journal unreadable: %v", err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		by := "acked by hand"
		if req.SessionID != "" {
			p, ok := sessionActingAs(svc.Store, c.scope, req.SessionID)
			if !ok {
				unknownSession(w, r, resp)
				return
			}
			if req.SessionID != respawnToldSession(*e, run) && (e.SubjectID == "" || p.SubjectID != e.SubjectID) {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "session "+wakeShort(req.SessionID)+" was not told of alert "+run+
					" and is not on its subject: ack it from the session the notice woke, from a session of that subject, or by hand", nil)
				return
			}
			by = "acked by session " + wakeShort(req.SessionID)
		}
		if err := scope.CanWrite(c.scope, scope.Scope(e.Scope), c.cross); err != nil {
			if errors.Is(err, scope.ErrCrossScope) {
				resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch, alertWriteRefusal(c.scope, scope.Scope(e.Scope)), nil)
				return
			}
			scopeError(w, r, resp, err, notFound)
			return
		}
		res, created, err := resolveAlert(svc, *e, run, by, time.Now().UTC())
		if err != nil {
			svc.logf("alert %s: resolution not written: %v", run, err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, pigtail.AlertResolution{ID: run, Resolved: created, Summary: res.Summary})
	}
}

// ResolveSubjectAlerts handles POST /alerts/resolve: a relaunch of the
// subject succeeded (D5), so each of its open alerts in a scope the caller
// writes closes. A failed write is logged and, once the others are tried,
// answered 500 (the worker records it).
func ResolveSubjectAlerts(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.AlertResolveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if !subjectIDRe.MatchString(req.SubjectID) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, fmt.Sprintf("subject_id must match %s, got %q", subjectIDRe, req.SubjectID), nil)
			return
		}
		if req.Cause == "" || strings.ContainsAny(req.Cause, "\r\n") || len(req.Cause) > alertCauseMax {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "cause must be one line of at most 120 bytes", nil)
			return
		}
		c := callerOf(r)
		sc, err := scope.ListFilter(c.scope, "")
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		es, err := openAlerts(svc.Store, sc)
		if err != nil {
			svc.logf("alerts: journal unreadable: %v", err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		now := time.Now().UTC()
		closed := []string{}
		var failed []string
		for _, e := range es {
			if e.SubjectID != req.SubjectID || scope.CanWrite(c.scope, scope.Scope(e.Scope), c.cross) != nil {
				continue
			}
			run, _ := respawnRunOf(e)
			if _, created, err := resolveAlert(svc, e, run, "subject relaunched ("+req.Cause+")", now); err != nil {
				svc.logf("alert %s: resolution not written: %v", run, err)
				failed = append(failed, run)
			} else if created {
				closed = append(closed, run)
			}
		}
		if len(failed) > 0 {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "alerts not resolved: "+strings.Join(failed, ", "), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, pigtail.AlertsResolved{IDs: closed})
	}
}
