package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RunWorkspaceHooks re-runs the post-create hooks of an existing workspace
// (D5): the same detached path and budget as at creation (startHooks), the
// outcome recorded. Once the hooks end it answers the workspace whatever the
// outcome — a failed hook is data, not a transport error. A client that
// leaves does not stop the hooks.
func RunWorkspaceHooks(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ws, err := svc.Store.GetWorkspace(id)
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrWorkspaceNotFound, "no such workspace", map[string]interface{}{"id": id})
			return
		} else if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		proj, err := svc.Store.GetProject(ws.ProjectID)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "project "+ws.ProjectID+": "+err.Error(), nil)
			return
		}
		cfg, err := pigtail.LoadProjectConfig(proj.RootPath)
		if err != nil || len(cfg.Hooks) == 0 {
			msg := "project " + proj.Name + " declares no post-create hooks"
			if err != nil {
				msg = "read " + pigtail.ProjectConfigFilename + ": " + err.Error()
			}
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, msg, nil)
			return
		}
		logPath := devserver.LogPath(ws.Path, "hooks")
		if err := svc.Store.ClaimWorkspaceHooks(id, logPath); errors.Is(err, store.ErrHooksRunning) {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "post-create hooks already running — log: "+logPath, nil)
			return
		} else if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		select {
		case <-startHooks(svc, ws.ID, ws.Path, cfg.Hooks):
		case <-r.Context().Done():
			return // the hooks run on to their end; their result is recorded
		}
		out, err := svc.Store.GetWorkspace(id)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if svc.Backend != nil {
			out.DisplayRef = svc.Backend.DisplayRef(out)
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
