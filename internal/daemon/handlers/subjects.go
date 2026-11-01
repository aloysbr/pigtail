package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RegisterSubjectRoutes wires every theme/subject/phase/log route onto r,
// behind scopeMW (scope isolation, see subjects_scope.go). Called from
// server.buildRouter and from handler tests.
func RegisterSubjectRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Group(func(r chi.Router) {
		r.Use(scopeMW(resp))
		registerSubjectRoutes(r, svc, resp)
	})
}

func registerSubjectRoutes(r chi.Router, svc *Services, resp Responder) {
	// Writes sit behind FreezeMW (maintenance --freeze, D1); reads and the
	// session<->subject link (registerCmuxBindingRoutes) stay out of it.
	fz := r.With(FreezeMW(svc, resp))
	fz.Post("/themes", CreateTheme(svc, resp))
	r.Get("/themes", ListThemes(svc, resp))
	r.Get("/spaces", ListSpaces(svc, resp))
	fz.Patch("/themes/{id}", UpdateTheme(svc, resp))
	fz.Delete("/themes/{id}", DeleteTheme(svc, resp))
	fz.Post("/subjects", CreateSubject(svc, resp))
	r.Get("/subjects", ListSubjects(svc, resp))
	r.Get("/subjects/next", NextSubject(svc, resp)) // static route; chi prioritises it over /subjects/{id}
	// A read (subjects_suggest.go): on r, not fz, so it answers under
	// maintenance --freeze; static, so chi prioritises it over POST /subjects/{id}.
	r.Post("/subjects/suggest", SuggestSubjects(svc, resp))
	// A search that records itself (subjects_find.go): on r, not fz, like
	// suggest; static, so chi prioritises it over POST /subjects/{id}.
	r.Post("/subjects/find", FindSubjects(svc, resp))
	// The choice of a search (subjects_find.go): out of the freeze, like the link.
	r.Post("/subject-finds/{fid}/pick", PickSubjectFind(svc, resp))
	// Atomic rank changes (subject_ranks.go): a write behind the freeze;
	// static, so chi prioritises it over POST /subjects/{id}.
	fz.Post("/subjects/ranks", SetRanks(svc, resp))
	r.Get("/subjects/{id}", GetSubject(svc, resp))
	r.Get("/subjects/{id}/worktree", SubjectWorktree(svc, resp))
	fz.Post("/subjects/{id}", UpdateSubject(svc, resp))
	fz.Post("/subjects/{id}/phases", CreatePhase(svc, resp))
	r.Get("/subjects/{id}/phases", ListPhases(svc, resp))
	fz.Post("/phases/{id}", UpdatePhase(svc, resp))
	fz.Post("/subjects/{id}/log", AppendLog(svc, resp))
	r.Get("/subjects/{id}/log", ListLog(svc, resp))
	// subject outputs (subject_outputs.go): the artifacts produced for a subject
	fz.Post("/subjects/{id}/outputs", AddSubjectOutput(svc, resp))
	r.Get("/subjects/{id}/outputs", ListSubjectOutputs(svc, resp))
	r.Get("/subjects/{id}/outputs/groups", ListSubjectOutputGroups(svc, resp))
	fz.Post("/subjects/{id}/outputs/purge", PurgeSubjectOutput(svc, resp))
	// system journal (journal.go): transversal index of pointers, scoped per row
	fz.Post("/journal", AddJournal(svc, resp))
	r.Get("/journal", ListJournal(svc, resp))
	// focus spaces (focus.go): temporary folders, own scope only (D6)
	registerFocusRoutes(r, fz, svc, resp)
	// captures (captures.go): lightweight ideas under a theme (A3)
	registerCaptureRoutes(r, fz, svc, resp)
	registerCmuxBindingRoutes(r, svc, resp)
	// session launch (launch.go): a write behind the freeze, its read beside
	registerLaunchRoutes(r, fz, svc, resp)
	// SDK sessions (sdk_sessions.go): reads beside, writes behind the freeze
	registerSDKRoutes(r, fz, svc, resp)
	// staffing scheduler (staffing.go): its settings behind the freeze, its reads beside
	registerStaffingRoutes(r, fz, svc, resp)
}

// CreateTheme handles POST /themes. Mints a thm_ id and created_at when absent.
func CreateTheme(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var th pigtail.Theme
		if err := json.NewDecoder(r.Body).Decode(&th); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if th.Name == "" || th.Kind == "" || th.VaultLink == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "name, kind and vault_link are required", nil)
			return
		}
		kind, ok := pigtail.NormalizeThemeKind(th.Kind)
		if !ok {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, `kind must be "project" or "ongoing"`, nil)
			return
		}
		th.Kind = kind
		// The vault link decides the scope; a client-sent scope is ignored.
		// Another scope's root reads, to a caller that cannot read it, like a
		// root no scope declares (D19); the 403 below is the master's only.
		sc, err := scope.FromVaultLinkFor(callerOf(r).scope, th.VaultLink)
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		th.Scope = string(sc)
		if err := pigtail.ValidThemeDeadline(th.Deadline); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		if err := checkSpace(svc, callerOf(r).scope, th.Scope, th.Space); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		// Creating is a write on the new theme's scope. Nothing exists yet to
		// hide, so a default-scope caller creating a perso theme gets 403, not 404.
		c := callerOf(r)
		if err := scope.CanWrite(c.scope, sc, c.cross); err != nil {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch,
				fmt.Sprintf("a %s caller cannot create a %s theme", c.scope, sc), nil)
			return
		}
		if th.ID == "" {
			th.ID = genID(pigtail.PrefixTheme)
		}
		if th.CreatedAt.IsZero() {
			th.CreatedAt = time.Now().UTC()
		}
		if err := svc.Store.CreateTheme(&th); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusCreated, th)
	}
}

// ListThemes handles GET /themes?scope=. The query only narrows what the
// caller may see (see scope.ListFilter).
func ListThemes(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, err := scope.ListFilter(callerOf(r).scope, r.URL.Query().Get("scope"))
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		out, err := svc.Store.ListThemes(sc)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// checkSpace refuses a space the table does not declare for the theme's
// scope. Unknown and another scope's answer alike (D19): the message never
// echoes the requested name, which may be another scope's space.
func checkSpace(svc *Services, caller scope.Scope, themeScope, name string) error {
	if name == "" {
		return nil
	}
	var names []string
	if svc.Spaces != nil {
		if sp, ok := svc.Spaces.Get(name); ok && sp.Scope == themeScope {
			return nil
		}
		for _, s := range svc.Spaces.VisibleTo(caller, scope.Active()) {
			if s.Scope == themeScope {
				names = append(names, s.Name)
			}
		}
	}
	return fmt.Errorf("unknown space (known: %s)", strings.Join(names, ", "))
}

// ListSpaces handles GET /spaces: the spaces the caller may name, in folder
// order (D6). Behind scopeMW: a non-master caller learns only its own scope's.
func ListSpaces(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := []pigtail.Space{}
		if svc.Spaces != nil {
			for _, s := range svc.Spaces.VisibleTo(callerOf(r).scope, scope.Active()) {
				out = append(out, pigtail.Space{Name: s.Name, Scope: s.Scope})
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// themeUpdate is the PATCH /themes/{id} body. Pointers distinguish "field
// omitted" (nil → leave unchanged) from "field set to empty string".
type themeUpdate struct {
	Name      *string `json:"name"`
	VaultLink *string `json:"vault_link"`
	Space     *string `json:"space"`
	Deadline  *string `json:"deadline"`
	Carried   *bool   `json:"carried"`
}

// UpdateTheme handles PATCH /themes/{id}. Partial update of name, vault_link,
// space, deadline and/or carried; id/kind/created_at are immutable. Updates the DB row only — never moves
// vault files. 404 when the theme is absent, 400 when no settable field is
// provided.
func UpdateTheme(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var in themeUpdate
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if in.Name == nil && in.VaultLink == nil && in.Space == nil && in.Deadline == nil && in.Carried == nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "nothing to update: provide name, vault_link, space, deadline and/or carried", nil)
			return
		}
		cur, ok := authorizeTheme(w, r, svc, resp, id, true, "no such theme", nil)
		if !ok {
			return
		}
		// A theme never changes scope: its subjects' ranks and visibility
		// hang on it. The new link must keep the same Globex/ or Perso/ prefix.
		if in.VaultLink != nil {
			sc, err := scope.FromVaultLinkFor(callerOf(r).scope, *in.VaultLink)
			if err != nil {
				scopeError(w, r, resp, err, "")
				return
			}
			if string(sc) != cur.Scope {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
					fmt.Sprintf("a theme cannot move from %s to %s — create a new theme and reassign its subjects", cur.Scope, sc), nil)
				return
			}
		}
		if in.Space != nil {
			if err := checkSpace(svc, callerOf(r).scope, cur.Scope, *in.Space); err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
				return
			}
		}
		if in.Deadline != nil {
			if err := pigtail.ValidThemeDeadline(*in.Deadline); err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
				return
			}
		}
		if err := svc.Store.UpdateTheme(id, pigtail.ThemeUpdate{Name: in.Name, VaultLink: in.VaultLink, Space: in.Space, Deadline: in.Deadline, Carried: in.Carried}); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such theme", nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		th, err := svc.Store.GetTheme(id)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, th)
	}
}

// DeleteTheme handles DELETE /themes/{id}. Refuses with 409 CONFLICT when any
// subject is still linked to the theme (reassign them first). Deletes only when
// the theme has zero subjects. No cascade. Updates the DB only — vault files are
// the caller's responsibility. 404 when the theme is absent.
func DeleteTheme(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, ok := authorizeTheme(w, r, svc, resp, id, true, "no such theme", nil); !ok {
			return
		}
		n, err := svc.Store.CountSubjectsByTheme(id)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if n > 0 {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
				fmt.Sprintf("cannot delete theme %s: %d subject(s) still linked — reassign them first", id, n),
				map[string]interface{}{"theme_id": id, "subjects": n})
			return
		}
		nc, err := svc.Store.CountOpenCapturesByTheme(id)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if nc > 0 {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
				fmt.Sprintf("cannot delete theme %s: %d open capture(s) still linked — promote or delete them first", id, nc),
				map[string]interface{}{"theme_id": id, "captures": nc})
			return
		}
		if err := svc.Store.DeleteTheme(id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such theme", nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, map[string]string{"id": id})
	}
}

// CreateSubject handles POST /subjects. Mints sub_ id, slug (from title when
// absent), default status=inbox, and timestamps server-side.
func CreateSubject(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sub pigtail.Subject
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if sub.ThemeID == "" || sub.Title == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "theme_id and title are required", nil)
			return
		}
		if !normalizeShortTitle(w, r, resp, &sub) {
			return
		}
		th, ok := authorizeTheme(w, r, svc, resp, sub.ThemeID, true, "unknown theme_id", map[string]interface{}{"theme_id": sub.ThemeID})
		if !ok {
			return
		}
		if err := checkDeps(svc, scope.Scope(th.Scope), sub.ID, sub.Deps, nil); err != nil {
			depFailure(w, r, resp, err)
			return
		}
		if sub.ID == "" {
			sub.ID = genID(pigtail.PrefixSubject)
		}
		if sub.Slug == "" {
			sub.Slug = slugify(sub.Title)
		}
		if sub.Status == "" {
			sub.Status = pigtail.StatusInbox
		}
		if err := pigtail.ValidateSubjectStatus(sub.Status); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		now := time.Now().UTC()
		sub.CreatedAt, sub.UpdatedAt = now, now
		sub.Scope = th.Scope
		if err := svc.Store.CreateSubject(&sub); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		svc.subjectsChanged()
		resp.JSON(w, r, http.StatusCreated, sub)
	}
}

// GetSubject handles GET /subjects/{id}.
func GetSubject(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := authorizeSubject(w, r, svc, resp, chi.URLParam(r, "id"), false)
		if !ok {
			return
		}
		if err := withCmuxWorkspaces(svc, sub); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if err := withOutputStats(r, svc, sub); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if err := withBlockedBy(r, svc, sub); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, sub)
	}
}

// withBlockedBy fills sub.BlockedBy (D30): the dependencies the store's
// SatisfiesDeps rule leaves unsatisfied, [] when none; and the transitive
// BlockedByAll, the direct Blocks and the transitive BlocksAll, always
// non-nil there. GET /subjects/:id only.
func withBlockedBy(_ *http.Request, svc *Services, sub *pigtail.Subject) error {
	ids, err := svc.Store.SubjectBlockedBy(sub)
	if err != nil {
		return err
	}
	sub.BlockedBy = &ids
	byAll, blocks, blocksAll, err := svc.Store.SubjectDepChains(sub)
	if err != nil {
		return err
	}
	sub.BlockedByAll, sub.Blocks, sub.BlocksAll = &byAll, &blocks, &blocksAll
	return nil
}

// SubjectWorktree handles GET /subjects/{id}/worktree. It returns the live
// (non-terminal) workspace linked via a phase to the subject — skipping phases
// whose workspace is shipped/archived or whose row is gone — or 404 if none.
func SubjectWorktree(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, ok := authorizeSubject(w, r, svc, resp, id, false); !ok {
			return
		}
		ws, err := svc.Store.WorkspaceForSubject(id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no live (non-terminal) worktree for subject", nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, ws)
	}
}

// ListSubjects handles GET /subjects?theme=&status=&source=&scope=. An unknown
// status answers 400 like create and update (M8). The scope
// query only narrows what the caller may see (see scope.ListFilter).
func ListSubjects(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, err := scope.ListFilter(callerOf(r).scope, r.URL.Query().Get("scope"))
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		if status := r.URL.Query().Get("status"); status != "" {
			if err := pigtail.ValidateSubjectStatus(status); err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
				return
			}
		}
		out, err := svc.Store.ListSubjects(store.SubjectFilter{
			ThemeID: r.URL.Query().Get("theme"), Status: r.URL.Query().Get("status"), Source: r.URL.Query().Get("source"),
			Scope: sc,
		})
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// UpdateSubject handles POST /subjects/{id}: a partial update. Only the keys
// present in the body are written, the others keep their value — see
// mergeSubjectPatch. A client echoing back the whole struct still works. The
// server preserves id/created_at/scope and refreshes updated_at.
func UpdateSubject(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		existing, ok := authorizeSubject(w, r, svc, resp, id, true)
		if !ok {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "read body: "+err.Error(), nil)
			return
		}
		if callerOf(r).web {
			if msg := webPatchRefusal(existing.Status, body); msg != "" {
				resp.Error(w, r, http.StatusForbidden, pigtail.ErrForbiddenForWeb, msg, nil)
				return
			}
		}
		in, err := mergeSubjectPatch(*existing, body)
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		if in.Status == "" {
			in.Status = existing.Status
		}
		if err := pigtail.ValidateSubjectStatus(in.Status); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		if !normalizeShortTitle(w, r, resp, &in) {
			return
		}
		in.ID = id
		in.CreatedAt = existing.CreatedAt
		if in.Slug == "" {
			in.Slug = existing.Slug
		}
		if in.ThemeID == "" {
			in.ThemeID = existing.ThemeID
		}
		// Theme reassignment: validate the target theme exists for the caller
		// and stays in the subject's scope. (No-op when the theme is
		// unchanged.) Moving vault prose is the caller's job.
		if in.ThemeID != existing.ThemeID {
			th, ok := authorizeTheme(w, r, svc, resp, in.ThemeID, true, "unknown theme", map[string]interface{}{"theme_id": in.ThemeID})
			if !ok {
				return
			}
			if th.Scope != existing.Scope {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
					fmt.Sprintf("a subject cannot move from %s to %s — pick a %s theme", existing.Scope, th.Scope, existing.Scope), nil)
				return
			}
		}
		if err := checkDeps(svc, scope.Scope(existing.Scope), id, in.Deps, existing.Deps); err != nil {
			depFailure(w, r, resp, err)
			return
		}
		in.UpdatedAt = time.Now().UTC()
		in.Scope = existing.Scope
		if err := svc.Store.UpdateSubject(&in); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		svc.subjectsChanged()
		// Re-read: a theme change cleared the dragged space (D8), and `in` was
		// built on the state from before it.
		out, err := svc.Store.GetSubject(id)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// webStatuses are the statuses the web front writes and the ones a
// subject it writes may be in (D3): the open ones and parked. done, dropped
// and captured are never written nor left by the cookie.
var webStatuses = map[string]bool{
	pigtail.StatusInbox: true, pigtail.StatusUnderstanding: true, pigtail.StatusDesigning: true,
	pigtail.StatusBuilding: true, pigtail.StatusParked: true,
}

// webPatchRefusal returns why the web front may not apply body to a
// subject in status current ("" = allowed): the body's one key is status,
// its value an open status or parked, and the subject is open or parked.
func webPatchRefusal(current string, body []byte) string {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(body, &patch); err != nil {
		return "the web front writes a subject's status only: invalid JSON body"
	}
	raw, ok := patch["status"]
	if len(patch) != 1 || !ok {
		return "the web front writes a subject's status only"
	}
	var next string
	if err := json.Unmarshal(raw, &next); err != nil || !webStatuses[next] {
		return "the web front writes an open status or parked only, never done, dropped or captured"
	}
	if !webStatuses[current] {
		return fmt.Sprintf("the web front does not reopen a %s subject", current)
	}
	return ""
}

// normalizeShortTitle trims sub.ShortTitle in place and answers 400 when it
// is not a single line of at most pigtail.ShortTitleMax runes. "" is valid:
// the label is then derived from the title (pigtail.ShortTitle).
func normalizeShortTitle(w http.ResponseWriter, r *http.Request, resp Responder, sub *pigtail.Subject) bool {
	st, err := pigtail.ValidateShortTitle(sub.ShortTitle)
	if err != nil {
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
		return false
	}
	sub.ShortTitle = st
	return true
}

// NextSubject handles GET /subjects/next?scope=. Next is per scope: the
// caller's own by default (see scope.NextFilter).
func NextSubject(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, err := scope.NextFilter(callerOf(r).scope, r.URL.Query().Get("scope"))
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		sub, err := svc.Store.NextSubject(sc)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no actionable subject", nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, sub)
	}
}

// slugify lowercases, strips accents/punctuation, and hyphenates whitespace.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == ' ' || r == '-' || r == '_':
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		default:
			// drop accents/punctuation: map common accented letters to ASCII
			if rep, ok := accentMap[r]; ok {
				b.WriteString(rep)
				prevHyphen = false
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

var accentMap = map[rune]string{
	'é': "e", 'è': "e", 'ê': "e", 'ë': "e",
	'à': "a", 'â': "a", 'ä': "a",
	'î': "i", 'ï': "i",
	'ô': "o", 'ö': "o",
	'ù': "u", 'û': "u", 'ü': "u",
	'ç': "c",
}

// CreatePhase handles POST /subjects/{id}/phases. Mints phs_ id, default
// state=pending, created_at server-side.
func CreatePhase(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subjectID := chi.URLParam(r, "id")
		if _, ok := authorizeSubject(w, r, svc, resp, subjectID, true); !ok {
			return
		}
		var ph pigtail.SubjectPhase
		if err := json.NewDecoder(r.Body).Decode(&ph); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if ph.Kind == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "kind is required", nil)
			return
		}
		ph.SubjectID = subjectID
		if ph.ID == "" {
			ph.ID = genID(pigtail.PrefixPhase)
		}
		if ph.State == "" {
			ph.State = pigtail.PhasePending
		}
		if ph.CreatedAt.IsZero() {
			ph.CreatedAt = time.Now().UTC()
		}
		if err := svc.Store.CreatePhase(&ph); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusCreated, ph)
	}
}

// ListPhases handles GET /subjects/{id}/phases.
func ListPhases(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, ok := authorizeSubject(w, r, svc, resp, id, false); !ok {
			return
		}
		out, err := svc.Store.ListPhasesBySubject(id)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// phaseUpdate is the POST /phases/{id} body.
type phaseUpdate struct {
	State       string `json:"state"`
	WorkspaceID string `json:"workspace_id"`
}

// UpdatePhase handles POST /phases/{id}.
func UpdatePhase(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in phaseUpdate
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if in.State == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "state is required", nil)
			return
		}
		// Resolve phase → subject → theme: a phase has the scope of its
		// subject, and a hidden one reads exactly like an unknown phase.
		const msg = "no such phase"
		ph, err := svc.Store.GetPhase(chi.URLParam(r, "id"))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msg, nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		sc, err := svc.Store.SubjectScope(ph.SubjectID)
		if errors.Is(err, store.ErrNotFound) {
			sc, err = scope.Master(), nil // orphan phase: fail-closed, like a themeless subject
		}
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if err := authorize(callerOf(r), sc, true); err != nil {
			scopeError(w, r, resp, err, msg)
			return
		}
		err = svc.Store.UpdatePhase(ph.ID, in.State, in.WorkspaceID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such phase", nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		svc.subjectsChanged()
		resp.JSON(w, r, http.StatusOK, map[string]string{"id": chi.URLParam(r, "id"), "state": in.State})
	}
}

// AppendLog handles POST /subjects/{id}/log. Mints log_ id, ts server-side.
func AppendLog(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subjectID := chi.URLParam(r, "id")
		if _, ok := authorizeSubject(w, r, svc, resp, subjectID, true); !ok {
			return
		}
		var e pigtail.SubjectLogEntry
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if e.Kind == "" || e.Text == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "kind and text are required", nil)
			return
		}
		e.SubjectID = subjectID
		if e.ID == "" {
			e.ID = genID(pigtail.PrefixLog)
		}
		if e.Timestamp.IsZero() {
			e.Timestamp = time.Now().UTC()
		}
		if err := svc.Store.AppendLog(&e); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusCreated, e)
	}
}

// ListLog handles GET /subjects/{id}/log?since=<unixmillis>.
func ListLog(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var since time.Time
		if v := r.URL.Query().Get("since"); v != "" {
			ms, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "since must be unix millis", nil)
				return
			}
			since = time.UnixMilli(ms).UTC()
		}
		id := chi.URLParam(r, "id")
		if _, ok := authorizeSubject(w, r, svc, resp, id, false); !ok {
			return
		}
		out, err := svc.Store.ListLog(id, since)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
