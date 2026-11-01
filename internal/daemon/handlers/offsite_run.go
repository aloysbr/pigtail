package handlers

import (
	"net/http"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// OffsiteRunner is the subset of internal/daemon's offsiteBackupper this
// handler needs, crossing the package boundary as an interface:
// offsiteBackupper itself stays unexported (this package must not import
// internal/daemon — that is the import cycle daemon -> handlers already
// forbids the other direction of), only its RunOffsiteNow and
// RunOffsiteFallbackNow methods (internal/daemon/offsite_run.go) do, via
// Services.Offsite.
type OffsiteRunner interface {
	// RunOffsiteNow starts an immediate primary copy unless one is already
	// in flight, returning whether it actually started.
	RunOffsiteNow(dbPath, destDir string) bool
	// RunOffsiteFallbackNow runs one fallback exercise immediately,
	// bypassing the 6h due-check — a human asked for this, explicitly, right
	// now — but never the correction guards the daemon's own scheduled
	// exercise applies: an empty, malformed, or same-as-primary destination,
	// or a store with no file-backed state to persist to, still exercises
	// nothing. configured reports whether a fallback destination string is
	// set at all, independent of whether it is valid.
	RunOffsiteFallbackNow(dbPath, destDir, fallbackDest string) (started, configured bool)
}

// RunOffsiteBackup handles POST /backup/offsite/run: "I just fixed
// something, prove it right now" instead of waiting for the hourly primary
// rotation or the six-hourly fallback exercise.
//
// It triggers BOTH routes, deliberately. The reason to press the button is
// "I just fixed something, prove it now", and the outage that motivated this
// whole feature lived on the *fallback* route, at six-hour
// cadence — a button that only restarted the primary copy would leave
// exactly the failure this exists to surface invisible for up to six more
// hours. Each route is guarded by its own in-flight field
// (offsiteBackupper's inFlight and fbInFlight, deliberately distinct fields
// — see that struct's own doc comment in internal/daemon/offsite.go): the
// primary may start while the fallback is still busy, and vice versa.
//
// 200, never 409. This repo's own precedent (CleanupWorkspace, cleanup.go)
// answers 409 because it guards a single action. This route starts two
// independent ones — the primary can start while the fallback is still in
// flight — and one conflict status cannot express that. "already in
// progress" reads instead as one of the three response booleans coming back
// false, on exactly the same footing as "nothing configured":
//
//	started                     -> 200, all three booleans
//	primary copy already busy   -> 200, offsite_started: false
//	no fallback configured      -> 200, fallback_started: false, fallback_configured: false
//	no primary destination      -> 400 INVALID_REQUEST
//
// The response never waits on either route's own I/O: RunOffsiteNow and
// RunOffsiteFallbackNow both return as soon as they have decided whether to
// start, before either one's copy or probe has run — see their own doc
// comments (internal/daemon/offsite_run.go). It also never returns a fresh
// verdict: the copy is asynchronous, so a verdict read at this instant would
// be whatever the last cycle left behind, not this run's own result — poll
// GET /backup afterward instead.
func RunOffsiteBackup(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.OffsiteDestDir == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"no offsite destination is configured", nil)
			return
		}

		out := pigtail.BackupRunResponse{}
		if svc.Store != nil && svc.Offsite != nil {
			dbPath := svc.Store.Path()
			out.OffsiteStarted = svc.Offsite.RunOffsiteNow(dbPath, svc.OffsiteDestDir)
			out.FallbackStarted, out.FallbackConfigured = svc.Offsite.RunOffsiteFallbackNow(
				dbPath, svc.OffsiteDestDir, svc.OffsiteFallbackDest)
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
