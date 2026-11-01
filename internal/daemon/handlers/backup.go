package handlers

import (
	"net/http"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Backup handles GET /backup — the daemon's classification of the offsite
// backup destination.
//
// It holds no state of its own: every call re-reads offsite-state.json via
// store.ReadOffsiteState and re-classifies it via store.ClassifyOffsite. That
// is deliberate, not an oversight — the file is what `pigtail db list` also
// reads, with the daemon stopped, and the whole point of this endpoint is
// that a live client and that offline command agree, because they are
// reading the same fact rather than two copies of it that could drift.
//
// The same file also carries the fallback exercise's own report (task 8),
// classified here too via store.ClassifyOffsiteFallback and mapped onto the
// optional OffsiteView.Fallback — still no state of its own, still a re-read
// and re-classification on every call.
func Backup(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := pigtail.BackupResponse{}
		if svc.Store != nil {
			state := store.ReadOffsiteState(svc.Store.Path())
			if state.Configured() {
				now := time.Now()
				health := store.ClassifyOffsite(state, now)
				view := &pigtail.OffsiteView{
					DestDir:             state.DestDir,
					Status:              health.Status,
					Severity:            health.Severity,
					Detail:              store.RenderOffsiteDetail(health),
					ConsecutiveFailures: health.ConsecutiveFailures,
					SameDevice:          health.SameDevice,
					LastErr:             health.LastErr,
				}
				if health.HasLastOK {
					secs := int(health.LastOKAge.Seconds())
					view.LastOKAgeSeconds = &secs
				}
				if !state.LastTryAt.IsZero() {
					secs := int(now.Sub(state.LastTryAt).Seconds())
					view.LastTryAgeSeconds = &secs
				}

				fallback := store.ClassifyOffsiteFallback(state, health, now)
				if fallback.Configured {
					fview := &pigtail.OffsiteFallbackView{
						Dest:                fallback.Dest,
						Status:              fallback.Status,
						Severity:            fallback.Severity,
						Detail:              store.RenderOffsiteFallbackDetail(fallback),
						ConsecutiveFailures: fallback.ConsecutiveFailures,
						LastErr:             fallback.LastErr,
						Via:                 fallback.Via,
					}
					if fallback.HasLastOK {
						secs := int(fallback.LastOKAge.Seconds())
						fview.LastOKAgeSeconds = &secs
					}
					if fallback.HasLastTry {
						secs := int(fallback.LastTryAge.Seconds())
						fview.LastTryAgeSeconds = &secs
					}
					view.Fallback = fview
				}

				out.Offsite = view
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
