package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// CreateProject handles POST /projects. Idempotent, and re-registering a known
// project REFRESHES the settings its `pigtail.yaml` owns.
//
// It used to return the stored row untouched. That made the config file
// decorative after the first registration: the daemon reads a project's
// settings from the database, so editing `pigtail.yaml` afterwards changed
// nothing while `pigtail init` still answered "registered". Measured 2026-09-04:
// a `base_ref: origin/main` sat in a yaml for two months without effect, and
// worktrees kept branching 27, 47 and 53 commits behind a stale local main.
//
// A silent no-op on a config the user just edited is worse than an error: it
// answers success, and the divergence is only discovered by reading the DB.
func CreateProject(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p pigtail.Project
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if p.ID == "" || p.RootPath == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "id and root_path are required", nil)
			return
		}
		if p.DefaultBackend == "" {
			p.DefaultBackend = "git"
		}
		if existing, err := svc.Store.GetProject(p.ID); err == nil {
			// Carry over what the daemon owns and the config file never
			// carries, so a re-registration cannot blank it.
			if p.DefaultProfileID == "" {
				p.DefaultProfileID = existing.DefaultProfileID
			}
			if p.Remote == nil {
				p.Remote = existing.Remote
			}
			if err := svc.Store.UpdateProjectConfig(&p); err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			updated, err := svc.Store.GetProject(p.ID)
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			resp.JSON(w, r, http.StatusOK, updated)
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if err := svc.Store.CreateProject(&p); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		svc.Bus.Publish(pigtail.Event{
			Type: pigtail.EventProjectCreated,
			Data: map[string]interface{}{"project_id": p.ID, "root_path": p.RootPath},
		})
		resp.JSON(w, r, http.StatusCreated, p)
	}
}

// ListProjects handles GET /projects.
func ListProjects(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.Store.ListProjects()
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
