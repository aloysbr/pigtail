package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// CmuxFoldersControl is the part of the sidebar loop that toggles the cmux
// folders (D12, cmuxsync.Runner.ToggleFolders). It is asked of
// Services.CmuxSidebar by type, so a loop without it answers like an idle one.
type CmuxFoldersControl interface {
	ToggleFolders(ctx context.Context) (pigtail.CmuxFoldersToggle, error)
}

// statusClientClosedRequest is the nginx convention for a caller that left
// before the answer.
const statusClientClosedRequest = 499

// ToggleCmuxFolders handles POST /cmux/folders/toggle: fold every cmux folder,
// or unfold them all when none is open (D12). The write happens on the loop's
// goroutine; the request waits for it, bounded by its own context. 409 when the
// loop or the folders are idle: nothing was written. The answer names no
// subject and no session.
func ToggleCmuxFolders(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctl, ok := svc.CmuxSidebar.(CmuxFoldersControl)
		if svc.CmuxSidebar == nil || !ok {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, pigtail.ErrCmuxFoldersIdle.Error(), nil)
			return
		}
		res, err := ctl.ToggleFolders(r.Context())
		switch {
		case errors.Is(err, pigtail.ErrCmuxFoldersIdle):
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, pigtail.ErrCmuxFoldersIdle.Error(), nil)
		case errors.Is(err, context.Canceled):
			resp.Error(w, r, statusClientClosedRequest, pigtail.ErrInternal, "request canceled before the folders were toggled", nil)
		case errors.Is(err, context.DeadlineExceeded):
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrInternal, "the sidebar loop did not serve the toggle in time", nil)
		case err != nil:
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		default:
			resp.JSON(w, r, http.StatusOK, res)
		}
	}
}
