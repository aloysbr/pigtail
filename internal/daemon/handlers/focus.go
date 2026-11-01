package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Focus spaces (sub_x000000000000007): temporary sidebar folders of themes
// and subjects. A focus belongs to its creator's identity scope and holds
// that scope's themes and subjects only (D6), even for the master with
// --cross-scope. Its end is judged at read by the store, which also holds
// seals under a maintenance freeze (focus D26): nothing here reads the
// freeze.

// Visible texts (focus D24): any change goes back to the user.
const (
	focusNotFoundMsg  = "no such focus"
	focusNameTakenMsg = "focus name unavailable" // focus D15: one body whoever holds the name
	focusMixedMsg     = "a focus holds its own scope's themes and subjects only"
	focusEndedMsg     = "focus already ended"
	focusPastMsg      = "--until is in the past"
)

func registerFocusRoutes(r, fz chi.Router, svc *Services, resp Responder) {
	fz.Post("/focus", CreateFocus(svc, resp))
	r.Get("/focus", ListFocus(svc, resp))
	fz.Post("/focus/{id}/members", AddFocusMember(svc, resp))
	fz.Delete("/focus/{id}/members/{kind}/{member}", RemoveFocusMember(svc, resp))
	fz.Post("/focus/{id}/end", EndFocus(svc, resp))
}

// FocusCreateRequest is the body of POST /focus.
type FocusCreateRequest struct {
	Name    string                `json:"name"`
	EndsAt  *time.Time            `json:"ends_at,omitempty"`
	Members []pigtail.FocusMember `json:"members"`
}

// FocusList is the answer of GET /focus: the note once, not per focus.
type FocusList struct {
	Focus []pigtail.Focus `json:"focus"`
	Note  string          `json:"note,omitempty"`
}

// focusNote says why no folder will show (focus D24); "" when folders run.
func focusNote(svc *Services) string {
	if svc.CmuxSidebar == nil {
		return "folders: sidebar loop not running"
	}
	st := svc.CmuxSidebar.State()
	switch {
	case !st.Folders:
		return "folders: idle (pigtail cmux sidebar on, --cmux-folders)"
	case st.FoldersNote != "":
		return "folders: " + st.FoldersNote
	}
	return ""
}

// CreateFocus handles POST /focus.
func CreateFocus(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req FocusCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if err := space.CheckFocusName(req.Name); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		now := time.Now()
		if req.EndsAt != nil && !req.EndsAt.After(now) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, focusPastMsg, nil)
			return
		}
		own := callerOf(r).scope
		for _, m := range req.Members {
			if !focusMemberAllowed(w, r, svc, resp, m, string(own)) {
				return
			}
		}
		// Focus D15: the name is judged last, here for a space and at the store
		// for another scope's open focus, so both answer alike whatever else
		// the request gets wrong.
		if svc.Spaces != nil {
			if _, taken := svc.Spaces.Get(req.Name); taken {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, focusNameTakenMsg, nil)
				return
			}
		}
		f := &pigtail.Focus{ID: genID(pigtail.PrefixFocus), Name: req.Name, Scope: string(own), EndsAt: req.EndsAt, Members: req.Members}
		if err := svc.Store.CreateFocus(f, now); err != nil {
			focusWriteError(w, r, svc, resp, err)
			return
		}
		f.Note = focusNote(svc)
		resp.JSON(w, r, http.StatusCreated, f)
	}
}

// ListFocus handles GET /focus[?all=1]: the focus the caller may read, open
// ones only without all. A failed seal is logged and the list still served.
func ListFocus(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all := r.URL.Query().Get("all") == "1"
		fs, sealErr, err := svc.Store.ListFocus(time.Now(), all)
		if sealErr != nil {
			svc.logf("%v", sealErr)
		}
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "focus: "+err.Error(), nil)
			return
		}
		c := callerOf(r)
		out := FocusList{Focus: []pigtail.Focus{}, Note: focusNote(svc)}
		for _, f := range fs {
			if scope.CanRead(c.scope, scope.Scope(f.Scope)) {
				out.Focus = append(out.Focus, f)
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// AddFocusMember handles POST /focus/{id}/members {kind, id}.
func AddFocusMember(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := authorizeFocus(w, r, svc, resp, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		var m pigtail.FocusMember
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		// D6: compared with the focus's scope, never the caller's — the
		// master with --cross-scope would otherwise mix its scope in.
		if !focusMemberAllowed(w, r, svc, resp, m, f.Scope) {
			return
		}
		if err := svc.Store.AddFocusMember(f.ID, pigtail.FocusMember{Kind: m.Kind, ID: m.ID}, time.Now()); err != nil {
			focusWriteError(w, r, svc, resp, err)
			return
		}
		focusAnswer(w, r, svc, resp, f.ID)
	}
}

// RemoveFocusMember handles DELETE /focus/{id}/members/{kind}/{member}. A
// member deleted since (missing) can still be removed: no member lookup.
func RemoveFocusMember(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := authorizeFocus(w, r, svc, resp, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		err := svc.Store.RemoveFocusMember(f.ID, chi.URLParam(r, "kind"), chi.URLParam(r, "member"), time.Now())
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such focus member", nil)
			return
		}
		if err != nil {
			focusWriteError(w, r, svc, resp, err)
			return
		}
		focusAnswer(w, r, svc, resp, f.ID)
	}
}

// EndFocus handles POST /focus/{id}/end: the focus ends by hand.
func EndFocus(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := authorizeFocus(w, r, svc, resp, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		ended, err := svc.Store.EndFocus(f.ID, time.Now())
		if err != nil {
			focusWriteError(w, r, svc, resp, err)
			return
		}
		resp.JSON(w, r, http.StatusOK, ended)
	}
}

// authorizeFocus loads a focus for a write: unknown or unreadable → 404 with
// one body; readable but not writable (another scope without --cross-scope)
// → the scope refusal. A failed seal is logged, the lookup still served.
func authorizeFocus(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, id string) (*pigtail.Focus, bool) {
	f, sealErr, err := svc.Store.GetFocus(id, time.Now())
	if sealErr != nil {
		svc.logf("%v", sealErr)
	}
	if errors.Is(err, store.ErrNotFound) {
		resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, focusNotFoundMsg, nil)
		return nil, false
	}
	if err != nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "focus: "+err.Error(), nil)
		return nil, false
	}
	c := callerOf(r)
	if err := authorize(c, scope.Scope(f.Scope), false); err != nil {
		resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, focusNotFoundMsg, nil)
		return nil, false
	}
	if err := authorize(c, scope.Scope(f.Scope), true); err != nil {
		scopeError(w, r, resp, err, focusNotFoundMsg)
		return nil, false
	}
	return f, true
}

// focusMemberAllowed checks one member: a theme or subject the caller may
// read (404, the body of an unknown id, otherwise), of the focus's scope
// (403 otherwise, the master included — D6). ok=false: response written.
func focusMemberAllowed(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, m pigtail.FocusMember, focusScope string) bool {
	var memberScope string
	switch m.Kind {
	case pigtail.FocusMemberTheme:
		th, ok := authorizeTheme(w, r, svc, resp, m.ID, false, "no such theme", nil)
		if !ok {
			return false
		}
		memberScope = th.Scope
	case pigtail.FocusMemberSubject:
		sub, ok := authorizeSubject(w, r, svc, resp, m.ID, false)
		if !ok {
			return false
		}
		memberScope = sub.Scope
	default:
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, `member kind must be "theme" or "subject"`, nil)
		return false
	}
	if memberScope != focusScope {
		resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch, focusMixedMsg, nil)
		return false
	}
	return true
}

// focusWriteError maps a store write error. A failed seal reads "focus:
// seal: …" (500): nothing was written.
func focusWriteError(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, err error) {
	var taken *store.ErrFocusMemberTaken
	switch {
	case errors.Is(err, store.ErrFocusNameTaken):
		resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, focusNameTakenMsg, nil)
	case errors.Is(err, store.ErrFocusEnded):
		resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, focusEndedMsg, nil)
	case errors.Is(err, store.ErrNotFound):
		resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, focusNotFoundMsg, nil)
	case errors.As(err, &taken):
		var details map[string]interface{}
		// The other focus is named only to a caller who may read it.
		if other, _, gerr := svc.Store.GetFocus(taken.FocusID, time.Now()); gerr == nil && scope.CanRead(callerOf(r).scope, scope.Scope(other.Scope)) {
			details = map[string]interface{}{"focus_id": other.ID, "focus_name": other.Name}
		}
		resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, taken.Kind+" "+taken.ID+" is already in an open focus", details)
	default:
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
	}
}

// focusAnswer answers a member write with the focus as it now reads.
func focusAnswer(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, id string) {
	f, sealErr, err := svc.Store.GetFocus(id, time.Now())
	if sealErr != nil {
		svc.logf("%v", sealErr)
	}
	if err != nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "focus: "+err.Error(), nil)
		return
	}
	resp.JSON(w, r, http.StatusOK, f)
}
