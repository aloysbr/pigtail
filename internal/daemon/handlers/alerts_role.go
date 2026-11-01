package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Role alerts (role supervision): a role-alert journal row with no
// role-recovered row of its key. They close by recovery (the sweep) or by an
// ack, which writes alert-resolved:<id>. Derived from the journal alone.

// roleAlertViews is every open (not acked, not recovered) role alert the
// filter sc lets through.
func roleAlertViews(svc *Services, caller, sc scope.Scope, now time.Time) ([]pigtail.AlertView, error) {
	rows, err := svc.Store.OpenRoleAlerts(sc)
	if err != nil {
		return nil, err
	}
	out := make([]pigtail.AlertView, 0, len(rows))
	for _, a := range rows {
		v := pigtail.AlertView{ID: a.ID, Kind: pigtail.AlertKindRole, State: roleStateOf(a.Source), Scope: a.Scope,
			SubjectID: a.SubjectID, Detail: a.Summary, CreatedAt: a.Timestamp,
			AgeSeconds: max(0, int(now.Sub(a.Timestamp).Seconds()))}
		if a.SubjectID != "" {
			v.SubjectTitle = alertSubjectTitle(svc, caller, a.SubjectID)
		}
		out = append(out, v)
	}
	return out, nil
}

// roleStateOf reads state= from "pigtail:roles/<name>?state=<state>".
func roleStateOf(source string) string {
	_, q, ok := strings.Cut(source, "?state=")
	if !ok {
		return ""
	}
	return q
}

// findRoleAlertLine is the role alert row with that id the filter sc lets through.
func findRoleAlertLine(st *store.Store, sc scope.Scope, id string) (*pigtail.JournalEntry, error) {
	es, err := st.ListJournal(store.JournalFilter{Scope: sc, Kind: store.RoleAlertKind, IDs: []string{id}})
	if err != nil {
		return nil, err
	}
	if len(es) == 0 {
		return nil, store.ErrNotFound
	}
	return &es[0], nil
}

// ackRoleAlert is AckAlert for a role alert id: by hand, or from a session of
// the alert's subject; idempotent.
func ackRoleAlert(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, id string, req pigtail.AlertAckRequest) {
	c := callerOf(r)
	sc, err := scope.ListFilter(c.scope, "")
	if err != nil {
		scopeError(w, r, resp, err, "")
		return
	}
	notFound := "no alert " + id
	e, err := findRoleAlertLine(svc.Store, sc, id)
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
		if e.SubjectID == "" || p.SubjectID != e.SubjectID {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "session "+wakeShort(req.SessionID)+" is not on the subject of alert "+id+
				": ack it from a session of that subject, or by hand", nil)
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
	res := pigtail.JournalEntry{ID: alertResolvedPrefix + id, Timestamp: time.Now().UTC(), Kind: alertResolvedKind, Scope: e.Scope,
		SubjectID: e.SubjectID, Summary: clipRunes(fmt.Sprintf("role alert %s resolved: %s", id, by), pigtail.JournalSummaryMax),
		Source: "pigtail:alerts/" + id}
	created, err := svc.Store.AddJournal(&res)
	if err != nil {
		svc.logf("alert %s: resolution not written: %v", id, err)
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		return
	}
	if created {
		svc.logf("alert %s resolved: %s", id, by)
	}
	resp.JSON(w, r, http.StatusOK, pigtail.AlertResolution{ID: id, Resolved: created, Summary: res.Summary})
}

// isRoleAlertID reports whether id names a role alert.
func isRoleAlertID(id string) bool {
	_, _, ok := roles.ParseAlertID(id)
	return ok
}
