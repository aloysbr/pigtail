package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// GetMaintenance tells what the daemon reads: the CLI compares it with the
// state it just wrote (same dir, same id), so a daemon looking elsewhere —
// not frozen, no pill — is caught at `on`/`off` time. Never the reason nor the
// exempt session.
func GetMaintenance(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.Maintenance == nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "maintenance not wired", nil)
			return
		}
		dir := svc.Maintenance.Dir()
		out := map[string]any{"dir": dir, "present": false}
		st, ok, err := svc.Maintenance.Current()
		switch {
		case err != nil:
			out["present"], out["unreadable"] = true, maintenance.UnreadableMessage(dir, err)
		case ok && st.On:
			out["present"], out["id"], out["on"], out["freeze"], out["late"] = true, st.ID, true, st.Freeze, st.Late(time.Now())
		case ok: // lifted, file gone, token remembers the id
			out["id"] = st.ID
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

type freezeProbeKey struct{}

// FreezeMW refuses a write while the maintenance is on with --freeze, unless
// the caller is its owner or the exempt session (X-Pigtail-Session,
// State.FrozenFor). An unreadable state is CLOSED (D8): every caller is
// refused, the owner and the exempt one included — who they are is
// unreadable too — with the path and the repair.
func FreezeMW(svc *Services, resp Responder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Context().Value(freezeProbeKey{}) != nil {
				w.Header().Set("X-Pigtail-Freeze-Probe", "1")
				return
			}
			if svc.Maintenance == nil {
				unwiredOnce.Do(func() { svc.logf("maintenance: FreezeMW has no state watch — writes are NOT guarded (wiring bug)") })
				w.Header().Set("X-Pigtail-Freeze", "unwired")
			} else {
				st, ok, err := svc.Maintenance.Current()
				if err != nil {
					msg := maintenance.UnreadableMessage(svc.Maintenance.Dir(), err)
					unreadableLog.once(svc, msg) // one line per distinct error, like the pill
					resp.Error(w, r, http.StatusLocked, pigtail.ErrMaintenanceFrozen, msg,
						map[string]interface{}{"state": "unreadable", "path": filepath.Join(svc.Maintenance.Dir(), maintenance.FileName)})
					return
				}
				unreadableLog.reset() // readable again: the next unreadable state is a new incident
				if ok && st.FrozenFor(r.Header.Get(pigtail.HeaderSession)) {
					// D4: the refused session reads "Blocked · freeze" in the
					// sidebar until the lift; a header-less caller is nobody.
					if sid := r.Header.Get(pigtail.HeaderSession); sid != "" {
						if err := maintenance.MarkFrozen(svc.Maintenance.Dir(), sid, st.ID); err != nil {
							// The session id is the caller's header: not logged (log injection).
							svc.logf("maintenance: frozen marker not written for a refused session")
						}
					}
					reader, err := scope.FromHeader(r.Header.Get(pigtail.HeaderScope))
					if err != nil { // the refusal still goes out, read with the narrowest view (D23)
						reader = scope.Default()
					}
					msg := maintenance.FrozenMessage(st, reader)
					details := map[string]interface{}{"id": st.ID, "since": st.Since, "late": st.Late(time.Now())}
					if st.Until != nil {
						details["until"] = *st.Until
					}
					resp.Error(w, r, http.StatusLocked, pigtail.ErrMaintenanceFrozen, msg, details)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// unreadableLog logs an unreadable state once per distinct message (same rule
// as the sidebar pill), not once per refused request. A readable state in
// between resets it.
var unreadableLog distinctLog

type distinctLog struct {
	mu   sync.Mutex
	last string
}

func (l *distinctLog) once(svc *Services, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if msg != l.last {
		l.last = msg
		svc.logf("maintenance: %s", msg)
	}
}

func (l *distinctLog) reset() {
	l.mu.Lock()
	l.last = ""
	l.mu.Unlock()
}

// unwiredOnce: one log line per daemon, not one per request. Package-level on
// purpose (the test resets it with `unwiredOnce = sync.Once{}`).
var unwiredOnce sync.Once

// IsFreezeMW identifies a FreezeMW by behaviour (see IsScopeMW).
func IsFreezeMW(mw func(http.Handler) http.Handler) bool {
	q := httptest.NewRequest(http.MethodPost, "/", nil)
	q = q.WithContext(context.WithValue(q.Context(), freezeProbeKey{}, true))
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, q)
	return rec.Header().Get("X-Pigtail-Freeze-Probe") == "1"
}
