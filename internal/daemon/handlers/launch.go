package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// SessionLauncher launches a session on a subject and reads a launch back
// (internal/daemon/launch).
type SessionLauncher interface {
	Launch(ctx context.Context, sub pigtail.Subject) (pigtail.SessionLaunch, error)
	Get(id string) (pigtail.SessionLaunch, bool, error)
}

// Registered with the subject routes, behind scopeMW: the launch is a write
// behind the freeze (fz), its read is not.
func registerLaunchRoutes(r chi.Router, fz chi.Router, svc *Services, resp Responder) {
	fz.Post("/subjects/{id}/launch", LaunchSubject(svc, resp))
	r.Get("/launches/{id}", GetLaunch(svc, resp))
}

func launcherOff(w http.ResponseWriter, r *http.Request, resp Responder) {
	resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrInternal, "launcher off", nil)
}

// LaunchSubject handles POST /subjects/{id}/launch: a session on a subject
// of the caller's own scope — another scope's subject answers like an
// unknown id, master and cross-scope opt-in included — or, for the web
// web front (D1), of any scope. 202 for a new launch, 200 for an API launch
// already in flight (deduplicated).
func LaunchSubject(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := authorizeSubject(w, r, svc, resp, chi.URLParam(r, "id"), false)
		if !ok {
			return
		}
		if c := callerOf(r); !c.web && scope.Canonical(sub.Scope) != c.scope {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such subject", nil)
			return
		}
		if svc.Launch == nil {
			launcherOff(w, r, resp)
			return
		}
		out, err := svc.Launch.Launch(r.Context(), *sub)
		var ref *launch.Refusal
		var om *launchmargin.OverMarginError
		switch {
		case errors.As(err, &ref):
			resp.Error(w, r, ref.Status, ref.Code, ref.Msg, ref.Details)
		case errors.As(err, &om):
			refuseOverMargin(w, r, resp, errors.New(om.APIMessage()))
		case err != nil:
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		case out.Deduplicated:
			resp.JSON(w, r, http.StatusOK, out)
		default:
			resp.JSON(w, r, http.StatusAccepted, out)
		}
	}
}

// GetLaunch handles GET /launches/{id}: any launch the registry holds, CLI
// included, if its subject is of the caller's own scope (any scope for the
// web front). Unknown, another scope's, or a subjectless launch for anyone
// but the master: 404, one body.
func GetLaunch(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const msg = "no such launch"
		notFound := func() { resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msg, nil) }
		if svc.Launch == nil {
			launcherOff(w, r, resp)
			return
		}
		l, ok, err := svc.Launch.Get(chi.URLParam(r, "id"))
		if err != nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrRegistryUnreadable, err.Error(), nil)
			return
		}
		if !ok {
			notFound()
			return
		}
		caller := callerOf(r)
		if l.SubjectID == "" {
			if caller.scope != scope.Master() {
				notFound()
				return
			}
			resp.JSON(w, r, http.StatusOK, l)
			return
		}
		sub, err := svc.Store.GetSubject(l.SubjectID)
		if errors.Is(err, store.ErrNotFound) {
			notFound()
			return
		}
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if !caller.web && scope.Canonical(sub.Scope) != caller.scope {
			notFound()
			return
		}
		resp.JSON(w, r, http.StatusOK, l)
	}
}
