package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Role routes (role supervision, docs/wire-protocol.md § Roles). Behind
// scopeMW; the writes behind FreezeMW like POST /journal. A role's scope is
// its subject's, so every route is scoped like the subject's.

// RegisterRoleRoutes mounts /roles.
func RegisterRoleRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Group(func(r chi.Router) {
		r.Use(scopeMW(resp))
		fz := r.With(FreezeMW(svc, resp))
		r.Get("/roles", ListRoles(svc, resp))
		r.Get("/roles/status", RolesStatus(svc, resp))
		fz.Post("/roles", UpsertRole(svc, resp))
		fz.Delete("/roles/{name}", DeleteRole(svc, resp))
	})
}

// UpsertRole handles POST /roles.
func UpsertRole(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.RoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		switch {
		case !roles.ValidName(req.Name):
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "name must match ^[a-z0-9][a-z0-9-]{0,31}$", nil)
			return
		case req.CadenceMS <= 0:
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "cadence must be positive", nil)
			return
		case req.GraceMS < 0 || req.StallMS < 0:
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "grace and stall must not be negative", nil)
			return
		case !subjectIDRe.MatchString(req.SubjectID):
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, fmt.Sprintf("subject_id must match %s, got %q", subjectIDRe, req.SubjectID), nil)
			return
		}
		sub, ok := authorizeSubject(w, r, svc, resp, req.SubjectID, true)
		if !ok {
			return
		}
		sc := sub.Scope
		if sc == "" {
			sc = string(scope.Master()) // themeless subject: fail-closed, like the journal
		}
		now := time.Now().UTC()
		role := pigtail.Role{Scope: sc, Name: req.Name, SubjectID: req.SubjectID,
			Cadence: time.Duration(req.CadenceMS) * time.Millisecond,
			Grace:   time.Duration(req.GraceMS) * time.Millisecond, Stall: time.Duration(req.StallMS) * time.Millisecond,
			CreatedAt: now, UpdatedAt: now}
		if role.Grace == 0 {
			role.Grace = pigtail.DefaultRoleGrace
		}
		if role.Stall == 0 {
			role.Stall = pigtail.DefaultRoleStall
		}
		if err := svc.Store.UpsertRole(&role); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		stored, err := svc.Store.GetRole(scope.Scope(sc), req.Name)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, stored)
	}
}

// ListRoles handles GET /roles[?scope=].
func ListRoles(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rs, ok := visibleRoles(w, r, svc, resp)
		if !ok {
			return
		}
		resp.JSON(w, r, http.StatusOK, pigtail.RolesResponse{Roles: rs})
	}
}

func visibleRoles(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder) ([]pigtail.Role, bool) {
	sc, err := scope.ListFilter(callerOf(r).scope, r.URL.Query().Get("scope"))
	if err != nil {
		scopeError(w, r, resp, err, "")
		return nil, false
	}
	rs, err := svc.Store.ListRoles(sc)
	if err != nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		return nil, false
	}
	if rs == nil {
		rs = []pigtail.Role{}
	}
	return rs, true
}

// RolesStatus handles GET /roles/status: the sweep's last verdict of each
// role the caller reads. A role the sweep has not judged yet reads unknown;
// with the watch off every role does, and watch is false.
func RolesStatus(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rs, ok := visibleRoles(w, r, svc, resp)
		if !ok {
			return
		}
		var snap []pigtail.RoleStatus
		watch := false
		if svc.RoleSnapshot != nil {
			snap, watch = svc.RoleSnapshot()
		}
		by := map[string]pigtail.RoleStatus{}
		for _, s := range snap {
			by[s.Role.Scope+"|"+s.Role.Name] = s
		}
		out := pigtail.RolesStatusResponse{Watch: watch, Roles: []pigtail.RoleStatus{}}
		for _, role := range rs {
			s, found := by[role.Scope+"|"+role.Name]
			switch {
			case found && watch:
				s.Role = role // the row as stored now
			case !watch:
				s = pigtail.RoleStatus{Role: role, State: string(roles.StateUnknown), Reason: "role watch is off (pigtaild -role-watch=false)"}
			default:
				s = pigtail.RoleStatus{Role: role, State: string(roles.StateUnknown), Reason: "not judged yet"}
			}
			out.Roles = append(out.Roles, s)
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// DeleteRole handles DELETE /roles/{name}[?scope=]. Its open episodes are
// closed first (alert-resolved when not acked, role-recovered « role removed »),
// so a later re-add never raises the same key again.
func DeleteRole(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		const notFound = "no such role"
		sc, err := scope.ListFilter(callerOf(r).scope, r.URL.Query().Get("scope"))
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		all, err := svc.Store.ListRoles(sc)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		var match []pigtail.Role
		for _, x := range all {
			if x.Name == name {
				match = append(match, x)
			}
		}
		switch {
		case len(match) == 0:
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, notFound, nil)
			return
		case len(match) > 1:
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "several roles are named "+name+": name the scope", nil)
			return
		}
		role := match[0]
		if err := authorize(callerOf(r), scope.Scope(role.Scope), true); err != nil {
			scopeError(w, r, resp, err, notFound)
			return
		}
		if err := CloseRoleEpisodes(svc.Store, role, time.Now().UTC()); err != nil {
			svc.logf("roles: closing the episodes of %s: %v", name, err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if err := svc.Store.DeleteRole(scope.Scope(role.Scope), role.Name); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, notFound, nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, map[string]string{"name": name, "scope": role.Scope})
	}
}

// CloseRoleEpisodes writes, for each unrecovered alert of the role, the ack
// (when missing) and the recovery « role removed ».
func CloseRoleEpisodes(st *store.Store, role pigtail.Role, now time.Time) error {
	rows, err := st.UnrecoveredRoleAlerts(scope.Scope(role.Scope))
	if err != nil {
		return err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Key.Before(rows[j].Key) })
	for _, a := range rows {
		if a.Name != role.Name {
			continue
		}
		if !a.Acked {
			e := pigtail.JournalEntry{ID: alertResolvedPrefix + a.ID, Timestamp: now, Kind: alertResolvedKind, Scope: a.Scope,
				SubjectID: a.SubjectID, Summary: clipRunes("role alert "+a.ID+" resolved: role removed", pigtail.JournalSummaryMax),
				Source: "pigtail:alerts/" + a.ID}
			if _, err := st.AddJournal(&e); err != nil {
				return err
			}
		}
		e := pigtail.JournalEntry{ID: roles.RecoveredID(a.Name, a.Key), Timestamp: now, Kind: store.RoleRecoveredKind, Scope: a.Scope,
			SubjectID: a.SubjectID, Summary: "Role " + a.Name + " removed", Source: "pigtail:roles/" + a.Name + "?state=removed"}
		if _, err := st.AddJournal(&e); err != nil {
			return err
		}
	}
	return nil
}
