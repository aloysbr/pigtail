package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	msgOffTopicBound   = "cmux workspace bound to a subject (detach it first)"
	msgOffTopicMissing = "no off-topic declaration on this cmux workspace"
	offTopicReasonMax  = 200
	// msgOffTopicNoSpace is the 400 of a first declaration that resolves no
	// space (D4); %s is the scope's space names, then the no-subject folder
	// (space.NoSubject, « Inbox »).
	msgOffTopicNoSpace = "space: name one with --space (known: %s), or cite a sub_… whose space is known"
)

// offTopicKnownSpaces renders "<spaces of the caller's scope>, Inbox": the names checkSpace accepts for this caller, then the sentinel.
func offTopicKnownSpaces(svc *Services, c callerScope) string {
	var names []string
	if svc.Spaces != nil {
		for _, s := range svc.Spaces.VisibleTo(c.scope, scope.Active()) {
			if s.Scope == string(c.scope) {
				names = append(names, s.Name)
			}
		}
	}
	names = append(names, space.NoSubject)
	return strings.Join(names, ", ")
}

func registerOffTopicRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/cmux-workspaces/{wsid}/state", CmuxWorkspaceStateHandler(svc, resp))
	r.Post("/cmux-workspaces/{wsid}/off-topic", DeclareOffTopic(svc, resp))
	r.Delete("/cmux-workspaces/{wsid}/off-topic", LiftOffTopic(svc, resp))
	r.Get("/off-topic-workspaces", ListOffTopic(svc, resp))
	r.Patch("/cmux-workspaces/{wsid}/off-topic/space", SetOffTopicSpace(svc, resp)) // master only (A8), outside the freeze like declare/lift
}

func syncMarkers(svc *Services) {
	if svc.SyncWorkspaceMarkers != nil {
		svc.SyncWorkspaceMarkers()
	}
}

// CmuxWorkspaceStateHandler handles GET /cmux-workspaces/{wsid}/state.
// A link to a subject the caller may not read answers bound-elsewhere, naming
// nothing of the subject (D25, as the lookup's 404 bound_elsewhere — D7 — and
// attach's 409); an orphan link (its subject gone) answers unbound.
func CmuxWorkspaceStateHandler(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		c := callerOf(r)
		out := pigtail.CmuxWorkspaceState{WorkspaceID: wsID, State: pigtail.WorkspaceStateUnbound}
		b, err := svc.Store.GetCmuxBinding(wsID)
		switch {
		case err == nil:
			sub, serr := svc.Store.GetSubject(b.SubjectID)
			if serr != nil && !errors.Is(serr, store.ErrNotFound) { // as LookupCmuxWorkspace: a db error is 500, only an orphan link reads unbound
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, serr.Error(), nil)
				return
			}
			if serr == nil {
				if authorize(c, scope.Scope(sub.Scope), false) == nil {
					out.State = pigtail.WorkspaceStateBound
					out.Subject = &pigtail.CmuxWorkspaceSubject{ID: sub.ID, Slug: sub.Slug, ShortTitle: pigtail.ShortTitle(*sub), Scope: sub.Scope}
				} else {
					out.State = pigtail.WorkspaceStateBoundElsewhere
				}
			}
			resp.JSON(w, r, http.StatusOK, out)
			return
		case !errors.Is(err, store.ErrNotFound):
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		o, err := svc.Store.GetOffTopic(wsID)
		switch {
		case err == nil:
			out.State = pigtail.WorkspaceStateOffTopic
			if authorize(c, scope.Scope(o.Scope), false) == nil {
				if err := resolveOffTopic(svc, c, o); err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
					return
				}
				out.OffTopic = o
			}
		case !errors.Is(err, store.ErrNotFound):
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// DeclareOffTopic handles POST /cmux-workspaces/{wsid}/off-topic.
//
//	201 — declared; 200 — already declared (row unchanged, except its space:
//	a different space on a row of the caller's own scope replaces the stored
//	one, D6 of sub_x000000000000019; a row the caller may not read answers its
//	workspace and declared_at only); 409 — the workspace is bound (the subject
//	is never named); 400 — bad reason, or a space the caller's scope does not
//	declare (the name is never echoed, D19).
func DeclareOffTopic(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		var req pigtail.DeclareOffTopicRequest
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
				return
			}
		}
		req.Reason = strings.TrimSpace(req.Reason)
		if strings.ContainsAny(req.Reason, "\r\n") || utf8.RuneCountInString(req.Reason) > offTopicReasonMax {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "reason: one line, 200 characters at most", nil)
			return
		}
		c := callerOf(r)
		req.Space = strings.TrimSpace(req.Space)
		// The no-subject folder name (space.NoSubject, « Inbox ») is the sentinel:
		// filed there, no pigtail space to check. The former label
		// (space.FormerNoSubject) is no sentinel: no input reads it as a former
		// name, so it is checked like any name.
		if req.Space != space.NoSubject {
			if err := checkSpace(svc, c.scope, string(c.scope), req.Space); err != nil { // Q1: the caller's scope's spaces only
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
				return
			}
		}
		if _, err := svc.Store.GetCmuxBinding(wsID); err == nil {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, msgOffTopicBound, nil)
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		o := &pigtail.WorkspaceOffTopic{CmuxWorkspaceID: wsID, Scope: string(c.scope), SessionID: req.SessionID, Reason: req.Reason, Space: req.Space, DeclaredAt: time.Now().UTC()}
		// The space depends on the request alone: resolve it before any write, so
		// a failed resolution leaves no row, journal line, marker or nudge behind.
		if err := resolveOffTopic(svc, c, o); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		// D4 (sub_x000000000000033): a first declaration must resolve a space.
		// An existing row keeps its filing: a redeclaration is never refused.
		if req.Space == "" && o.ResolvedSpace == "" {
			if _, err := svc.Store.GetOffTopic(wsID); errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, fmt.Sprintf(msgOffTopicNoSpace, offTopicKnownSpaces(svc, c)), nil)
				return
			} else if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
		}
		for try := 0; ; try++ {
			err := svc.Store.CreateOffTopic(o)
			if errors.Is(err, store.ErrOffTopicExists) {
				cur, gone, err := redeclareOffTopic(svc, c, wsID, req.Space)
				if gone && try == 0 { // lifted between the insert and now: declare afresh
					continue
				}
				if gone {
					err = store.ErrNotFound
				}
				if err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
					return
				}
				resp.JSON(w, r, http.StatusOK, cur)
				return
			}
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			break
		}
		journalOffTopic(svc, o)
		syncMarkers(svc)
		nudgeSidebar(svc)
		resp.JSON(w, r, http.StatusCreated, o)
	}
}

// redeclareOffTopic answers a declaration on a workspace already declared: the
// row as the caller may see it, with a new space when it may write it (D6, Q5).
// gone reports that the row vanished meanwhile (a concurrent lift).
func redeclareOffTopic(svc *Services, c callerScope, wsID, space string) (cur *pigtail.WorkspaceOffTopic, gone bool, err error) {
	cur, err = svc.Store.GetOffTopic(wsID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if space != "" && space != cur.Space && cur.Scope == string(c.scope) && authorize(c, scope.Scope(cur.Scope), true) == nil {
		if err := svc.Store.SetOffTopicSpace(wsID, space); errors.Is(err, store.ErrNotFound) {
			return nil, true, nil
		} else if err != nil {
			return nil, false, err
		}
		cur.Space = space
		nudgeSidebar(svc)
	}
	if authorize(c, scope.Scope(cur.Scope), false) != nil { // another scope's row: no reason, session, scope or space
		return &pigtail.WorkspaceOffTopic{CmuxWorkspaceID: cur.CmuxWorkspaceID, DeclaredAt: cur.DeclaredAt}, false, nil
	}
	if err := resolveOffTopic(svc, c, cur); err != nil {
		return nil, false, err
	}
	return cur, false, nil
}

// journalOffTopic writes one pointer line in the caller's scope (perso, the master, is a concrete scope too).
func journalOffTopic(svc *Services, o *pigtail.WorkspaceOffTopic) {
	sum := "workspace cmux " + o.CmuxWorkspaceID[:8] + " déclaré hors sujet"
	if o.Reason != "" {
		sum += " : " + o.Reason
	}
	if utf8.RuneCountInString(sum) > 280 {
		sum = string([]rune(sum)[:280])
	}
	e := &pigtail.JournalEntry{ID: "offtopic:" + o.CmuxWorkspaceID + ":" + o.DeclaredAt.Format("20060102T150405.000"),
		Timestamp: o.DeclaredAt, Kind: "workspace-off-topic", Scope: o.Scope, Summary: sum, Source: "pigtail"}
	if _, err := svc.Store.AddJournal(e); err != nil && svc.Logf != nil {
		svc.Logf("off-topic: journal line for %s: %v", o.CmuxWorkspaceID, err)
	}
}

// LiftOffTopic handles DELETE /cmux-workspaces/{wsid}/off-topic[?session_id=]. A row the
// caller may not write, or (with session_id) declared by another session or none, is the same 404 as no row.
func LiftOffTopic(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		o, err := svc.Store.GetOffTopic(wsID)
		sid := r.URL.Query().Get("session_id") // D8: SessionEnd lifts only its own declaration
		if errors.Is(err, store.ErrNotFound) || (err == nil && (authorize(callerOf(r), scope.Scope(o.Scope), true) != nil || (sid != "" && o.SessionID != sid))) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgOffTopicMissing, nil)
			return
		}
		if err == nil {
			err = svc.Store.DeleteOffTopic(wsID)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		syncMarkers(svc)
		if svc.CmuxSidebar != nil {
			svc.CmuxSidebar.Nudge()
		}
		resp.JSON(w, r, http.StatusOK, map[string]string{"cmux_workspace_id": wsID})
	}
}

// ListOffTopic handles GET /off-topic-workspaces: the rows the caller may read.
func ListOffTopic(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all, err := svc.Store.ListOffTopic()
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		c := callerOf(r)
		out := []pigtail.WorkspaceOffTopic{}
		for _, o := range all {
			if authorize(c, scope.Scope(o.Scope), false) == nil {
				if err := resolveOffTopic(svc, c, &o); err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
					return
				}
				out = append(out, o)
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// resolveOffTopic fills o.ResolvedSpace (D1, sub_x000000000000019): its
// explicit space, else the one inferred from a sub_… cited in its reason
// (space.OffTopicSpace, read as the declaration's scope), kept only when the
// space table declares it and the caller may read its scope. A store error is
// returned, never read as « no space ».
func resolveOffTopic(svc *Services, c callerScope, o *pigtail.WorkspaceOffTopic) error {
	o.ResolvedSpace = ""
	if o.Space == space.NoSubject { // the sentinel: filed in « Inbox », the CLI reads it as resolved
		o.ResolvedSpace = space.NoSubject
		return nil
	}
	sp, _, err := space.OffTopicSpace(*o, space.Lookup{
		Subject:  svc.Store.GetSubject,
		Theme:    svc.Store.GetTheme,
		NotFound: func(err error) bool { return errors.Is(err, store.ErrNotFound) },
		KnownSpace: func(name, sc string) bool {
			if svc.Spaces == nil {
				return true
			}
			d, ok := svc.Spaces.Get(name)
			return ok && d.Scope == sc
		},
	})
	if err != nil {
		return err
	}
	if sp == "" || svc.Spaces == nil {
		return nil
	}
	if d, ok := svc.Spaces.Get(sp); ok && scope.CanRead(c.scope, scope.Scope(d.Scope)) {
		o.ResolvedSpace = sp
	}
	return nil
}

// SetOffTopicSpace handles PATCH /cmux-workspaces/{wsid}/off-topic/space
// {space} (A8): the master rewrites the space of an off-topic row, and nothing
// else (session_id, reason, scope and declared_at stay), so a reassignment
// never loses what lift + declare would. space "" is accepted (the daemon then
// infers it). Master only: a non-master caller gets 403 SCOPE_MISMATCH before
// any lookup; a row of another scope needs --cross-scope (403 without). The
// space is validated by checkSpace against the row's scope BEFORE any write
// (the lesson of declare, sub_x000000000000019): no error can follow the write.
func SetOffTopicSpace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		c := callerOf(r)
		if !scope.SeesAll(c.scope) {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch, "only the master cell reassigns an off-topic space", nil)
			return
		}
		var req struct {
			Space string `json:"space"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		req.Space = strings.TrimSpace(req.Space)
		o, err := svc.Store.GetOffTopic(wsID)
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgOffTopicMissing, nil)
			return
		}
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if err := authorize(c, scope.Scope(o.Scope), true); err != nil {
			scopeError(w, r, resp, err, msgOffTopicMissing)
			return
		}
		if err := checkSpace(svc, c.scope, o.Scope, req.Space); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		if err := svc.Store.SetOffTopicSpace(wsID, req.Space); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgOffTopicMissing, nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		o.Space = req.Space
		_ = resolveOffTopic(svc, c, o) // best effort: the write is done, the answer only decorates it
		nudgeSidebar(svc)
		resp.JSON(w, r, http.StatusOK, o)
	}
}
