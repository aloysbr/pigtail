package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/admission"
	"github.com/aloysbr/pigtail/internal/daemon/focuspri"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// admissionProgressEvery paces the "queued" lines of a waiting request. The
// client prints at its own, slower pace; these lines exist so it always has a
// recent position to print, and so a dead daemon is noticed by silence.
const admissionProgressEvery = 5 * time.Second

// RegisterAdmissionRoutes wires the /admission endpoints onto r.
func RegisterAdmissionRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/admission", GetAdmission(svc, resp))
	r.Post("/admission/leases", AcquireAdmission(svc, resp))
	r.Post("/admission/leases/{id}/exit", ReportAdmissionExit(svc, resp))
	r.Get("/admission/history", GetAdmissionHistory(svc, resp))
}

// ReportAdmissionExit handles POST /admission/leases/{id}/exit — the exit
// status of the command a lease admitted. The lease is the open connection,
// which carries no way to say how the command ended, so the client reports it
// here before hanging up; the history line written at the release keeps it.
func ReportAdmissionExit(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body pigtail.AdmissionExit
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ExitCode == nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "exit_code is required", nil)
			return
		}
		if svc.Admission == nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrAdmissionDisabled,
				"no admission controller is running", nil)
			return
		}
		if !svc.Admission.ReportExit(chi.URLParam(r, "id"), *body.ExitCode) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such lease", nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, map[string]bool{"recorded": true})
	}
}

// GetAdmissionHistory handles GET /admission/history?since=<RFC3339> — the
// commands that ended since then, oldest first. Served by the daemon rather
// than read off disk by the CLI: only the daemon knows where its database
// (and so the file) lives, and the floor the simulation needs.
func GetAdmissionHistory(svc *Services, resp Responder) http.HandlerFunc {
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
		out := pigtail.AdmissionHistory{Records: []pigtail.AdmissionRecord{}}
		if svc.Admission != nil {
			out.FloorBytes = svc.Admission.Status().Config.FloorBytes
			recs, err := svc.Admission.History(since)
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			out.Records = recs
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// GetAdmission handles GET /admission — the controller's whole state. Always
// 200, with max_jobs 0 when no controller is running.
func GetAdmission(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := pigtail.AdmissionStatus{
			Leases: []pigtail.AdmissionLeaseView{},
			Queue:  []pigtail.AdmissionWaiterView{},
			Costs:  []pigtail.AdmissionCostView{},
		}
		if svc.Admission != nil {
			svc.Admission.RefreshIfStale()
			fillAdmissionStatus(&out, svc.Admission.Status())
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

func fillAdmissionStatus(out *pigtail.AdmissionStatus, st admission.Status) {
	out.MaxJobs = st.Config.MaxJobs
	out.FloorBytes = st.Config.FloorBytes
	out.DiskFloorBytes = st.Config.DiskFloorBytes
	out.DiskKnown = st.Reading.DiskKnown
	out.DiskFreeBytes = st.Reading.DiskFree
	out.PressureGate = st.Config.PressureLevel
	out.PressureFloorFactor = st.Config.PressureFloorFactor
	out.MaxStarveSeconds = int(st.Config.MaxStarve.Seconds())
	out.SwapoutGateBytesPerSec = st.Config.SwapoutBytesPerSec
	if len(st.Config.ClassLimits) > 0 {
		out.ClassLimits = map[string]int{}
		for k, v := range st.Config.ClassLimits {
			out.ClassLimits[k] = v
		}
	}
	out.TotalBytes = st.Total
	out.AvailableBytes = st.Reading.Available
	out.EffectiveBytes = st.Effective
	out.Pressure = st.Reading.Pressure
	out.SwapoutBytesPerSec = st.Reading.SwapoutBytesPerSec
	out.CompressorBytes = st.Reading.CompressorBytes
	out.UnusedBytes = st.Reading.UnusedBytes
	out.CompressRateBytesPerSec = st.Reading.CompressBytesPerSec
	out.CompressGateBytesPerSec = st.Config.CompressBytesPerSec
	if st.Config.CompressCalmWindow > 0 && len(st.Config.CompressCalmClasses) > 0 {
		out.CompressCalmWindowSeconds = int(st.Config.CompressCalmWindow.Seconds())
		out.CompressCalmRateBytesPerSec = st.Reading.CompressCalmBytesPerSec
		out.CompressCalmClasses = append([]string(nil), st.Config.CompressCalmClasses...)
		out.CompressCalmMaxStarveSeconds = int(st.Config.CompressCalmMaxStarve.Seconds())
	}
	if !st.ReadAt.IsZero() {
		out.ReadAgeSeconds = int(time.Since(st.ReadAt).Seconds())
	}
	for _, l := range st.Leases {
		out.Leases = append(out.Leases, pigtail.AdmissionLeaseView{
			ID: l.ID, Group: l.Group, Class: l.Class, Label: l.Label,
			CostBytes: l.Cost, RSSBytes: l.Mem, PeakBytes: l.Peak, AgeSeconds: int(l.Age.Seconds()),
			WaitedMS: int(l.Waited.Milliseconds()),
		})
	}
	for _, q := range st.Queue {
		out.Queue = append(out.Queue, pigtail.AdmissionWaiterView{
			Group: q.Group, Class: q.Class, Label: q.Label,
			CostBytes: q.Cost, AgeSeconds: int(q.Age.Seconds()), Reason: string(q.Reason),
			Focus: q.Focus,
		})
	}
	for _, c := range st.Costs {
		out.Costs = append(out.Costs, pigtail.AdmissionCostView{
			Class: c.Class, Repo: c.Repo, Workers: c.Workers, EWMABytes: c.EWMA, Samples: c.N,
		})
	}
}

// AcquireAdmission handles POST /admission/leases.
//
// The response is an NDJSON stream of {"data": AdmissionUpdate} lines: a
// "queued" line at once when the request has to wait, another every
// admissionProgressEvery, then one "granted" line — after which the handler
// blocks on the request context until the client goes away. That connection
// is the lease, exactly as for CPU slots (see AcquireCPUSlot): whatever ends
// the client, the kernel closes the socket and the deferred Release frees the
// reservation. A client that hangs up while queued is withdrawn from the
// queue the same way.
func AcquireAdmission(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.AdmissionRequest
		if !decodeLeaseBody(w, r, resp, &req) {
			return
		}
		if req.Group == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "group is required", nil)
			return
		}
		if svc.Admission == nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrAdmissionDisabled,
				"no admission controller is running", nil)
			return
		}
		flusher := leaseFlusher(w, r, resp)
		if flusher == nil {
			return
		}

		// Resolved once, here, before Enqueue and outside the controller: from
		// the resolver's in-memory snapshot, so it neither blocks nor fails.
		// The flag is fixed for the whole wait.
		focus := svc.FocusPri != nil && svc.FocusPri.IsFocus(focuspri.Caller{
			SessionID: req.SessionID, CmuxWorkspaceID: req.CmuxWorkspaceID, Cwd: req.Cwd,
		})
		ticket, err := svc.Admission.Enqueue(admission.Request{
			Group: req.Group, Class: req.Class, Label: req.Label, Repo: req.Repo,
			PID: req.PID, CostHint: req.CostHintBytes, CostPrior: req.CostPriorBytes,
			Workers: req.Workers, Focus: focus, ParentLease: req.ParentLease,
		})
		if err != nil {
			if errors.Is(err, admission.ErrDisabled) {
				resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrAdmissionDisabled,
					"admission is disabled (-admit-max-jobs=0)", nil)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		setStreamHeaders(w)
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		send := func(u pigtail.AdmissionUpdate) bool {
			if err := enc.Encode(map[string]any{"data": u}); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		queued := func() bool {
			info := svc.Admission.Position(ticket)
			return send(pigtail.AdmissionUpdate{
				State: pigtail.AdmissionQueued, Position: info.Position, Queued: info.Queued,
				Running: info.Running, AvailableBytes: info.Available, EffectiveBytes: info.Effective,
				NeedBytes: info.Need, FloorBytes: info.Floor, Reason: string(info.Reason),
				DiskFreeBytes: info.DiskFree, DiskFloorBytes: info.DiskFloor,
			})
		}

		select {
		case <-ticket.Ready():
		default:
			if !queued() {
				svc.Admission.Cancel(ticket)
				return
			}
			tick := time.NewTicker(admissionProgressEvery)
			defer tick.Stop()
		wait:
			for {
				select {
				case <-ticket.Ready():
					break wait
				case <-tick.C:
					if !queued() {
						svc.Admission.Cancel(ticket)
						return
					}
				case <-r.Context().Done():
					svc.Admission.Cancel(ticket)
					return
				}
			}
		}

		grant := svc.Admission.Grant(ticket)
		defer svc.Admission.Release(grant)
		if !send(pigtail.AdmissionUpdate{
			State: pigtail.AdmissionGranted, LeaseID: grant.ID, CostBytes: grant.Cost,
			WaitedMS: int(grant.Waited.Milliseconds()), Degraded: grant.Degraded,
		}) {
			return
		}
		<-r.Context().Done()
	}
}
