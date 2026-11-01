package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Explicit link between a subject and a cmux workspace (subject_cmux_bindings).
// Registered with the subject routes, so behind scopeMW; every route checks
// the subject with authorizeSubject. A workspace carries one subject: the
// first link wins, and a second one is 409 unless the caller asks to replace
// it AND may write the subject holding it. The 409 never names that subject,
// so a default-scope caller learns only that some link exists.

func registerCmuxBindingRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Post("/subjects/{id}/cmux-workspaces", AttachCmuxWorkspace(svc, resp))
	r.Get("/subjects/{id}/cmux-workspaces", ListCmuxWorkspaces(svc, resp))
	r.Delete("/subjects/{id}/cmux-workspaces/{wsid}", DetachCmuxWorkspace(svc, resp))
	r.Get("/cmux-workspaces/{wsid}", LookupCmuxWorkspace(svc, resp))
	registerCmuxSidebarRoutes(r, svc, resp)
	registerOffTopicRoutes(r, svc, resp)
}

const (
	msgCmuxAlreadyBound = "cmux workspace already bound to another subject (attach --replace to move it)"
	msgCmuxNotBound     = "no subject bound to this cmux workspace"
	msgCmuxBoundHidden  = "cmux workspace bound to a subject this scope cannot read"
)

// AttachCmuxWorkspace handles POST /subjects/{id}/cmux-workspaces.
//
//	201 — new link (or a link moved here with replace)
//	200 — the workspace was already bound to this subject (idempotent;
//	      retitle resets the title/description owners)
//	409 — bound to another subject, and no replace or no write on that one
func AttachCmuxWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req pigtail.AttachCmuxWorkspaceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(req.CmuxWorkspaceID)
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		if req.Source == "" {
			req.Source = pigtail.CmuxSourceAttach
		}
		if !pigtail.ValidCmuxBindingSource(req.Source) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				`source must be "attach", "subject-work" or "ptnew"`, nil)
			return
		}
		if _, ok := authorizeSubject(w, r, svc, resp, id, true); !ok {
			return
		}
		// A find_id is checked before anything is linked: a bad one refuses the attach.
		if req.FindID != "" {
			if code, apiCode, msg := findForPick(svc, callerOf(r), req.FindID, id); code != 0 {
				resp.Error(w, r, code, apiCode, msg, nil)
				return
			}
		}
		cur, err := svc.Store.GetCmuxBinding(wsID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			err = bindNew(svc, id, wsID, req.Source)
			if errors.Is(err, store.ErrCmuxBound) {
				// Lost a race with another first link.
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, msgCmuxAlreadyBound, nil)
				return
			}
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			finishAttach(w, r, svc, resp, wsID, http.StatusCreated, id, req.FindID)
			return
		case err != nil:
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		case cur.SubjectID == id:
			if req.Retitle {
				if err := svc.Store.ResetCmuxOwners(wsID); err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
					return
				}
				nudgeSidebar(svc)
			}
			linkLiftsOffTopic(svc, id, wsID)
			finishAttach(w, r, svc, resp, wsID, http.StatusOK, id, req.FindID)
			return
		}
		// Bound to another subject: replace only with write access to it.
		// Every refusal is the same 409, whatever the holder's scope.
		if !req.Replace || !mayWriteSubject(svc, callerOf(r), cur.SubjectID) {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, msgCmuxAlreadyBound, nil)
			return
		}
		err = svc.Store.ReplaceCmuxBinding(wsID, cur.SubjectID, id, req.Source, time.Now().UTC())
		if errors.Is(err, store.ErrCmuxBound) {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, msgCmuxAlreadyBound, nil)
			return
		}
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		logCmuxLink(svc, cur.SubjectID, "workspace cmux "+wsID+" délié : remplacé par un autre sujet")
		logCmuxLink(svc, id, "workspace cmux "+wsID+" lié ("+req.Source+"), remplace le lien précédent")
		if req.Retitle {
			if err := svc.Store.ResetCmuxOwners(wsID); err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
		}
		nudgeSidebar(svc)
		linkLiftsOffTopic(svc, id, wsID)
		finishAttach(w, r, svc, resp, wsID, http.StatusCreated, id, req.FindID)
	}
}

// BindLaunched binds the tab pigtaild's launcher created (wsID) to its
// subject without an HTTP call: the same first link as AttachCmuxWorkspace,
// source ptnew. A tab already bound answers store.ErrCmuxBound and is never
// replaced. Scope is the caller's to check (the launch handler did).
func BindLaunched(svc *Services, subjectID, wsID string) error {
	ws, err := pigtail.NormalizeCmuxWorkspaceID(wsID)
	if err != nil {
		return err
	}
	return bindNew(svc, subjectID, ws, pigtail.CmuxSourcePtnew)
}

// bindNew is the new-link path: create the binding, note it in the
// subject's log, nudge the sidebar, lift the tab's off-topic declaration.
// wsID is already normalized. store.ErrCmuxBound when another link won.
func bindNew(svc *Services, subjectID, wsID, source string) error {
	b := &pigtail.SubjectCmuxBinding{CmuxWorkspaceID: wsID, SubjectID: subjectID, Source: source, BoundAt: time.Now().UTC()}
	if err := svc.Store.CreateCmuxBinding(b); err != nil {
		return err
	}
	logCmuxLink(svc, subjectID, "workspace cmux "+wsID+" lié ("+source+")")
	nudgeSidebar(svc)
	linkLiftsOffTopic(svc, subjectID, wsID)
	return nil
}

// linkLiftsOffTopic drops the workspace's off-topic declaration on every successful attach (the link wins, S5),
// notes it in the subject's log, and resyncs the markers.
func linkLiftsOffTopic(svc *Services, subjectID, wsID string) {
	if err := svc.Store.DeleteOffTopic(wsID); err == nil {
		logCmuxLink(svc, subjectID, "workspace cmux "+wsID+" : hors sujet levé par le lien")
	}
	syncMarkers(svc)
}

// nudgeSidebar asks the sidebar loop, when there is one, for a pass with
// fresh cmux listings: a new or moved link shows at once.
func nudgeSidebar(svc *Services) {
	if svc.CmuxSidebar != nil {
		svc.CmuxSidebar.Nudge()
	}
}

// logCmuxLink notes a link change in the subject's log, so who took or lost a
// workspace stays traceable. Best effort: the link is already written, and a
// failed note must not turn it into an error.
func logCmuxLink(svc *Services, subjectID, text string) {
	e := &pigtail.SubjectLogEntry{ID: genID(pigtail.PrefixLog), SubjectID: subjectID, Timestamp: time.Now().UTC(), Kind: "note", Text: text}
	if err := svc.Store.AppendLog(e); err != nil && svc.Logf != nil {
		svc.Logf("cmux link: log on %s: %v", subjectID, err)
	}
}

// mayWriteSubject reports whether the caller may write subject id. An id the
// store no longer knows (orphan binding) is writable only by the master:
// fail closed for any other scope, like the relay's unknown subjects.
func mayWriteSubject(svc *Services, c callerScope, id string) bool {
	sc, err := svc.Store.SubjectScope(id)
	if err != nil {
		return scope.SeesAll(c.scope)
	}
	return authorize(c, sc, true) == nil
}

func writeBinding(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, wsID string, code int) {
	b, err := svc.Store.GetCmuxBinding(wsID)
	if err != nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		return
	}
	resp.JSON(w, r, code, b)
}

// ListCmuxWorkspaces handles GET /subjects/{id}/cmux-workspaces.
func ListCmuxWorkspaces(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, ok := authorizeSubject(w, r, svc, resp, id, false); !ok {
			return
		}
		out, err := svc.Store.ListCmuxBindings(id)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// DetachCmuxWorkspace handles DELETE /subjects/{id}/cmux-workspaces/{wsid}.
// A workspace not bound to this subject is 404: a route scoped to one subject
// never unbinds another's workspace.
func DetachCmuxWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		if _, ok := authorizeSubject(w, r, svc, resp, id, true); !ok {
			return
		}
		// The binding as it was, so the sidebar loop can give the workspace
		// its title and description back once the link is gone.
		before, _ := svc.Store.GetCmuxBinding(wsID)
		if err := svc.Store.DeleteCmuxBinding(id, wsID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "cmux workspace not bound to this subject", nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		logCmuxLink(svc, id, "workspace cmux "+wsID+" délié")
		syncMarkers(svc)
		if svc.CmuxSidebar != nil && before != nil {
			svc.CmuxSidebar.Released(*before)
		}
		resp.JSON(w, r, http.StatusOK, map[string]string{"cmux_workspace_id": wsID})
	}
}

// LookupCmuxWorkspace handles GET /cmux-workspaces/{wsid}: the binding and a
// summary of its subject. An unbound workspace is 404; one bound to a subject
// the caller may not read is 404 too, with its own message and
// details.bound_elsewhere, naming nothing of the subject (D7). An orphan
// binding (its subject gone) stays the unbound 404.
func LookupCmuxWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		b, err := svc.Store.GetCmuxBinding(wsID)
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgCmuxNotBound, nil)
			return
		}
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		sub, err := svc.Store.GetSubject(b.SubjectID)
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgCmuxNotBound, nil)
			return
		}
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if err := authorize(callerOf(r), scope.Scope(sub.Scope), false); err != nil {
			// D7: say a link exists (as attach's 409 already does), never whose.
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgCmuxBoundHidden,
				map[string]interface{}{pigtail.DetailBoundElsewhere: true})
			return
		}
		resp.JSON(w, r, http.StatusOK, pigtail.CmuxWorkspaceLookup{
			Binding: *b,
			Subject: pigtail.CmuxWorkspaceSubject{ID: sub.ID, Slug: sub.Slug, ShortTitle: pigtail.ShortTitle(*sub), Scope: sub.Scope},
		})
	}
}

// withCmuxWorkspaces fills sub.CmuxWorkspaces from the bindings.
func withCmuxWorkspaces(svc *Services, sub *pigtail.Subject) error {
	bs, err := svc.Store.ListCmuxBindings(sub.ID)
	if err != nil {
		return err
	}
	sub.CmuxWorkspaces = nil
	for _, b := range bs {
		sub.CmuxWorkspaces = append(sub.CmuxWorkspaces, b.CmuxWorkspaceID)
	}
	return nil
}
