package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/staticsrv"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Subject outputs routes (docs/wire-protocol.md § Subject outputs): the
// registry of the artifacts produced for a subject. Registered in the subject
// group, behind scopeMW; the subject of the URL is authorized like any
// subject route (an invisible subject answers 404 no such subject).

const msgNoSuchOutput = "no such output"

// AddSubjectOutput handles POST /subjects/{id}/outputs. 201 created, 200
// already registered (the stored row, unchanged: the first writer wins).
func AddSubjectOutput(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var o pigtail.SubjectOutput
		if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		sub, ok := authorizeSubject(w, r, svc, resp, id, true)
		if !ok {
			return
		}
		if s := strings.TrimSpace(o.SubjectID); s != "" && s != id {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				fmt.Sprintf("subject_id %s differs from the subject of the URL %s", s, id), nil)
			return
		}
		o.SubjectID = id
		// Server-owned fields: never taken from the body.
		o.Seq, o.PurgedAt, o.CreatedAt, o.Exists, o.ServedURL = 0, nil, 0, nil, ""
		if err := o.Normalize(); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		stored, created, err := svc.Store.AddSubjectOutput(r.Context(), o)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		// Published after the write, only when created; it never changes the answer.
		if created && svc.Bus != nil {
			svc.Bus.Publish(pigtail.Event{Type: pigtail.EventSubjectOutputAdded, Scope: sub.Scope, Data: map[string]interface{}{
				"subject_id": stored.SubjectID, "seq": stored.Seq, "id": stored.ID, "kind": stored.Kind, "key": stored.Key,
			}})
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		resp.JSON(w, r, status, pigtail.SubjectOutputAddResult{Output: stored, Created: created})
	}
}

// ListSubjectOutputs handles GET /subjects/{id}/outputs?since=&kind=&session=&limit=.
// since is an exclusive seq; every row carries exists.
func ListSubjectOutputs(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, ok := authorizeSubject(w, r, svc, resp, id, false); !ok {
			return
		}
		q := r.URL.Query()
		f := store.OutputFilter{Kind: q.Get("kind"), SessionID: q.Get("session"), Limit: pigtail.OutputListDefault}
		if v := q.Get("since"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "since must be a seq >= 0", nil)
				return
			}
			f.SinceSeq = n
		}
		if f.Kind != "" && !slices.Contains(pigtail.OutputKinds, f.Kind) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				fmt.Sprintf("kind %q must be one of %s", f.Kind, strings.Join(pigtail.OutputKinds, " ")), nil)
			return
		}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > pigtail.OutputListMax {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
					fmt.Sprintf("limit must be 1..%d", pigtail.OutputListMax), nil)
				return
			}
			f.Limit = n
		}
		out, err := svc.Store.ListSubjectOutputs(r.Context(), id, f)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		urlOf := svc.outputsURLs()
		for i := range out {
			pigtail.StatExists(&out[i])
			fillServedURL(urlOf, &out[i])
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// ListSubjectOutputGroups handles GET /subjects/{id}/outputs/groups: the
// subject's mockups and reports grouped by rounds (pigtail.GroupOutputs),
// exists and served_url computed per output before grouping. Same
// authorization as GET /subjects/{id}/outputs.
func ListSubjectOutputGroups(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, ok := authorizeSubject(w, r, svc, resp, id, false); !ok {
			return
		}
		out, err := svc.Store.ListSubjectOutputs(r.Context(), id, store.OutputFilter{Limit: pigtail.OutputListMax})
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		urlOf := svc.outputsURLs()
		for i := range out {
			pigtail.StatExists(&out[i])
			fillServedURL(urlOf, &out[i])
		}
		resp.JSON(w, r, http.StatusOK, pigtail.GroupOutputs(out))
	}
}

// PurgeSubjectOutput handles POST /subjects/{id}/outputs/purge {key}: sets
// purged_at once (the first date is kept); the row stays. An unknown key on a
// visible subject answers 404 no such output.
func PurgeSubjectOutput(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req pigtail.SubjectOutputPurgeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if _, ok := authorizeSubject(w, r, svc, resp, id, true); !ok {
			return
		}
		key := strings.TrimSpace(req.Key)
		if key == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "key is required", nil)
			return
		}
		if strings.HasPrefix(key, "/") {
			key = filepath.Clean(key)
		}
		o, err := svc.Store.PurgeSubjectOutput(r.Context(), id, key, time.Now().UnixMilli())
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgNoSuchOutput, nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, o) // no exists: GET only
	}
}

// outputsURLs reads the served_url source of this request, nil when none.
func (svc *Services) outputsURLs() func(string) string {
	if svc.OutputsURLs == nil {
		return nil
	}
	return svc.OutputsURLs()
}

// fillServedURL sets o.ServedURL when the outputs server opens o: a kind it
// serves, a path on disk, not purged, and its own index opening the path
// (urlOf; a refused folder — $HOME, a launch dir, a .git — gives "").
// Exists must be set.
func fillServedURL(urlOf func(string) string, o *pigtail.SubjectOutput) {
	o.ServedURL = ""
	if urlOf == nil || o.Path == "" || o.PurgedAt != nil || o.Exists == nil || !*o.Exists || !staticsrv.Serves(o.Kind) {
		return
	}
	o.ServedURL = urlOf(o.Path)
}

// withOutputStats fills the outputs counters of GET /subjects/{id}.
func withOutputStats(r *http.Request, svc *Services, sub *pigtail.Subject) error {
	n, last, err := svc.Store.SubjectOutputStats(r.Context(), sub.ID)
	if err != nil {
		return err
	}
	sub.OutputsCount = &n
	if n > 0 {
		sub.LastOutputTS = &last
	}
	return nil
}
