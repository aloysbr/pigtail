package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/cpuslots"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RegisterCPUSlotRoutes wires the /cpu/slots endpoints onto r.
func RegisterCPUSlotRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/cpu/slots", GetCPUSlots(svc, resp))
	r.Post("/cpu/slots", AcquireCPUSlot(svc, resp))
	r.Get("/cpu/slots/history", GetCPUSlotsHistory(svc, resp))
}

// GetCPUSlotsHistory handles GET /cpu/slots/history?since=<RFC3339> — the
// slot holds (and queued waits) that ended since then, oldest first. Served by
// the daemon: only it knows where its database, and so the file, lives.
func GetCPUSlotsHistory(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var since time.Time
		if v := r.URL.Query().Get("since"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "since: want RFC3339", nil)
				return
			}
			since = t
		}
		out := pigtail.CPUSlotHistory{Records: []pigtail.CPUSlotRecord{}}
		if svc.CPUSlots != nil {
			recs, err := svc.CPUSlots.History(since)
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			out.Records = recs
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// GetCPUSlots handles GET /cpu/slots — the arbiter's whole state. Always 200,
// with budget 0 when no arbiter is running: "how many slots are there" has a
// truthful answer even when the answer is none.
func GetCPUSlots(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := pigtail.CPUSlotsResponse{Groups: []pigtail.CPUSlotGroupView{}}
		if svc.CPUSlots != nil {
			st := svc.CPUSlots.Status()
			out.Budget, out.Held, out.Waiting = st.Budget, st.Held, st.Waiting
			for _, g := range st.Groups {
				out.Groups = append(out.Groups, pigtail.CPUSlotGroupView{
					Group: g.Group, Held: g.Held, Waiting: g.Waiting, Share: g.Share,
				})
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// AcquireCPUSlot handles POST /cpu/slots.
//
// The response is streamed and then the connection is *held*: the grant is
// flushed as soon as the slot is attributed, and the handler blocks on the
// request context until the client goes away. That connection is the slot.
// Whatever ends the client — a finished test file, a killed worker, a Ctrl-C,
// the OOM killer, a daemon restart — the kernel closes the socket, the
// context fires, and the deferred Release hands the slot to the next waiter.
// No heartbeat, no TTL, no PID to watch.
//
// This works because the daemon's http.Server deliberately has no
// WriteTimeout (server.go, for /events), and socket connections are trusted.
func AcquireCPUSlot(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.CPUSlotRequest
		if !decodeLeaseBody(w, r, resp, &req) {
			return
		}
		if req.Group == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "group is required", nil)
			return
		}
		if svc.CPUSlots == nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrCPUSlotsDisabled,
				"no cpu slot arbiter is running", nil)
			return
		}
		flusher := leaseFlusher(w, r, resp)
		if flusher == nil {
			return
		}

		grant, err := svc.CPUSlots.Acquire(r.Context(), req.Group, req.Label)
		if err != nil {
			if errors.Is(err, cpuslots.ErrDisabled) {
				resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrCPUSlotsDisabled,
					"cpu slot arbiter is disabled (-cpu-slots=0)", nil)
				return
			}
			return // the client hung up while queued; there is nobody to answer
		}
		defer svc.CPUSlots.Release(grant)

		setStreamHeaders(w)
		resp.JSON(w, r, http.StatusOK, pigtail.CPUSlotGrant{
			Granted:  true,
			WaitedMS: int(grant.Waited.Milliseconds()),
			Degraded: grant.Degraded,
		})
		flusher.Flush()

		<-r.Context().Done()
	}
}
