package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// registerCaptureRoutes wires the capture routes (A3): a capture is a
// lightweight idea under a theme. Writes sit behind the freeze (fz), the
// reads do not. Same scope middleware as /subjects: the scope derives from
// the theme, never from a stored column.
func registerCaptureRoutes(r, fz chi.Router, svc *Services, resp Responder) {
	fz.Post("/captures", AddCapture(svc, resp))
	r.Get("/captures", ListCaptures(svc, resp))
	fz.Post("/captures/promote", PromoteCaptures(svc, resp))
	fz.Delete("/captures/{id}", DeleteCapture(svc, resp))
}

type addCaptureBody struct {
	ThemeID       string `json:"theme_id"`
	Text          string `json:"text"`
	FromSubjectID string `json:"from_subject_id"`
}

// AddCapture handles POST /captures {theme_id, text, from_subject_id?}.
func AddCapture(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b addCaptureBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if b.FromSubjectID != "" && callerOf(r).web {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrForbiddenForWeb,
				"the web front does not capture a subject (it would close it): capture without from_subject_id", nil)
			return
		}
		b.Text = strings.TrimSpace(b.Text)
		if b.ThemeID == "" || b.Text == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "theme_id and text are required", nil)
			return
		}
		if _, ok := authorizeTheme(w, r, svc, resp, b.ThemeID, true, "unknown theme_id", map[string]interface{}{"theme_id": b.ThemeID}); !ok {
			return
		}
		c := pigtail.Capture{ID: genID(pigtail.PrefixCapture), ThemeID: b.ThemeID, Text: b.Text, FromSubjectID: b.FromSubjectID, CreatedAt: time.Now().UTC()}
		if err := svc.Store.AddCapture(&c, genID(pigtail.PrefixLog)); err != nil {
			captureStoreError(w, r, resp, err)
			return
		}
		got, err := svc.Store.GetCapture(c.ID)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusCreated, got)
	}
}

// ListCaptures handles GET /captures?theme=<id>&open=1&scope=.
func ListCaptures(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sc, err := scope.ListFilter(callerOf(r).scope, q.Get("scope"))
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		if t := q.Get("theme"); t != "" {
			// A theme the caller cannot read is unknown to it (D19).
			if _, ok := authorizeTheme(w, r, svc, resp, t, false, "unknown theme", nil); !ok {
				return
			}
		}
		open := q.Get("open")
		out, err := svc.Store.ListCaptures(store.CaptureFilter{
			ThemeID: q.Get("theme"), OpenOnly: open == "1" || open == "true", Scope: sc,
		})
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

type promoteBody struct {
	CaptureIDs []string `json:"capture_ids"`
	Title      string   `json:"title"`
	ShortTitle string   `json:"short_title"`
}

// PromoteCaptures handles POST /captures/promote: open captures of one theme
// become one subject (status=inbox, the rules of POST /subjects).
func PromoteCaptures(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b promoteBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		b.Title = strings.TrimSpace(b.Title)
		if len(b.CaptureIDs) == 0 || b.Title == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "capture_ids and title are required", nil)
			return
		}
		first, err := svc.Store.GetCapture(b.CaptureIDs[0])
		if err != nil {
			captureStoreError(w, r, resp, err)
			return
		}
		th, ok := authorizeTheme(w, r, svc, resp, first.ThemeID, true, "no such capture", nil)
		if !ok {
			return
		}
		sub := pigtail.Subject{ThemeID: th.ID, Title: b.Title, ShortTitle: b.ShortTitle}
		if !normalizeShortTitle(w, r, resp, &sub) {
			return
		}
		now := time.Now().UTC()
		sub.ID, sub.Slug, sub.Status = genID(pigtail.PrefixSubject), slugify(b.Title), pigtail.StatusInbox
		sub.Source, sub.CreatedAt, sub.UpdatedAt, sub.Scope = pigtail.SourceLocal, now, now, th.Scope
		caps, err := svc.Store.PromoteCaptures(b.CaptureIDs, &sub)
		if err != nil {
			captureStoreError(w, r, resp, err)
			return
		}
		svc.subjectsChanged()
		resp.JSON(w, r, http.StatusCreated, map[string]any{"subject": sub, "captures": caps})
	}
}

// DeleteCapture handles DELETE /captures/{id} (undo, C13).
func DeleteCapture(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		c, err := svc.Store.GetCapture(id)
		if err != nil {
			captureStoreError(w, r, resp, err)
			return
		}
		if _, ok := authorizeTheme(w, r, svc, resp, c.ThemeID, true, "no such capture", nil); !ok {
			return
		}
		if c.FromSubjectID != "" && callerOf(r).web {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrForbiddenForWeb,
				"the web front does not undo a subject's capture (it would reopen the subject)", nil)
			return
		}
		del, sub, err := svc.Store.DeleteCapture(id, genID(pigtail.PrefixLog))
		if err != nil {
			captureStoreError(w, r, resp, err)
			return
		}
		out := map[string]any{"capture": del}
		if sub != nil {
			out["subject"] = sub
		} else if del.FromSubjectID != "" {
			out["note"] = "subject " + del.FromSubjectID + " changed since the capture: its status was not touched"
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

func captureStoreError(w http.ResponseWriter, r *http.Request, resp Responder, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such capture or subject", nil)
	case errors.Is(err, store.ErrCaptureOtherTheme), errors.Is(err, store.ErrCapturesMixedThemes), errors.Is(err, store.ErrCapturePromoted), errors.Is(err, store.ErrCaptureClosed):
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
	default:
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
	}
}
