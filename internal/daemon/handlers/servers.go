package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RegisterServerRoutes wires the /servers endpoints onto r.
func RegisterServerRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/servers", ListServers(svc, resp))
	r.Post("/servers/stop-all", StopAllServers(svc, resp))   // static route before /{ws}/...
	r.Post("/servers/stop-idle", StopIdleServers(svc, resp)) // static route before /{ws}/...
	r.Post("/servers/stopped", MarkServerStopped(svc, resp)) // static route before /{ws}/...
	r.Post("/servers/{ws}/{server}/pin", PinServer(svc, resp))
	r.Delete("/servers/{ws}/{server}/pin", UnpinServer(svc, resp))
	r.Post("/servers/{ws}/{server}/stop", StopServer(svc, resp))
}

// ListServers handles GET /servers — running dev servers with memory/idle + headroom.
func ListServers(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := pigtail.ServersResponse{Servers: []pigtail.ServerView{}}
		if svc.RAMMonitor != nil {
			stats, avail, floor := svc.RAMMonitor.Inspect()
			out.AvailableBytes, out.FloorBytes = avail, floor
			for _, s := range stats {
				var pinned int64
				if !s.PinnedUntil.IsZero() {
					pinned = s.PinnedUntil.Unix()
				}
				// OwnerAlive is only meaningful once a server is claimed: an
				// unclaimed server (Owner == "") must emit neither field, but
				// devserver.OwnerAlive("") itself reports true (the opaque,
				// "cannot interrogate" branch) — computing it unconditionally
				// would defeat owner_alive's omitempty. Leaving it false here
				// keeps the zero value, so the field is omitted.
				var ownerAlive bool
				if s.Owner != "" {
					ownerAlive = devserver.OwnerAlive(s.Owner)
				}
				out.Servers = append(out.Servers, pigtail.ServerView{
					WorkspaceID:       s.WorkspaceID,
					Server:            s.Server,
					Branch:            s.Branch,
					Port:              s.Port,
					RSSBytes:          s.MemBytes,
					IdleSeconds:       int(s.IdleFor.Seconds()),
					PinnedUnixSeconds: pinned,
					PinnedDurable:     s.PinDurable,
					Owner:             s.Owner,
					OwnerAlive:        ownerAlive,
				})
			}
			// Read after Inspect: the conflicts its snapshot just recorded.
			// Each fix is planned against PATCH …/ports' own refusals, every
			// question asked in one batch (resyncTable). Kinds without a
			// declared server are out of scope (D1).
			conflicts := svc.RAMMonitor.Conflicts()
			running := make(map[serverAt]string, len(stats))
			for _, s := range stats {
				running[serverAt{s.WorkspaceID, s.Port}] = s.Server
			}
			var fixes []string
			if len(conflicts) > 0 {
				fixes = portFixes(conflicts, running, resyncTable(svc.Ports, conflicts, svc.logf), svc.logf)
			}
			for i, c := range conflicts {
				out.PortConflicts = append(out.PortConflicts, pigtail.PortConflictView{
					WorkspaceID: c.WorkspaceID, Server: c.Server, Kind: c.Kind, Port: c.Port,
					RegisteredPort: c.RegisteredPort, Reason: c.Reason,
					HolderPath: c.HolderPath, HolderWorkspaceID: c.HolderWorkspaceID, Fix: fixes[i],
				})
			}
			now := svc.RAMMonitor.Now()
			for _, d := range svc.RAMMonitor.RecentDeaths(now) {
				// A death restored from a state file older than Servers
				// carries only Server; the wire always names at least it.
				servers := d.Servers
				if len(servers) == 0 && d.Server != "" {
					servers = []string{d.Server}
				}
				out.RecentDeaths = append(out.RecentDeaths, pigtail.DeadServerView{
					WorkspaceID: d.WorkspaceID,
					Server:      d.Server,
					Servers:     servers,
					Port:        d.Port,
					AgoSeconds:  int(now.Sub(d.At).Seconds()),
				})
			}
		}
		// Main-worktree freshness: served from the daemon's last known verdict,
		// never computed here. "Is my main fresh?" is asked before sending an
		// agent to read it, from a command that has to stay instant — a display
		// path that shelled out to git would make the freshness check the
		// slowest command in the tool, and a slow check is one nobody runs.
		now := time.Now()
		for _, m := range svc.MainFresh.Snapshot() {
			out.MainWorktrees = append(out.MainWorktrees, pigtail.MainWorktreeView{
				ProjectID:         m.ProjectID,
				ProjectName:       m.ProjectName,
				Path:              m.Path,
				Base:              m.Base,
				Action:            string(m.Action),
				Reason:            m.Reason,
				Behind:            m.Behind,
				CheckedAgoSeconds: int(now.Sub(m.At).Seconds()),
			})
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// allowedPinDurations is the closed set of timed pins the API accepts (matches the menu bar); pigtail.PinUntilUnpinned is the durable one.
var allowedPinDurations = map[string]time.Duration{
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
}

// PinServer handles POST /servers/{ws}/{server}/pin?duration=30m|1h|4h|until-unpinned.
func PinServer(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws := chi.URLParam(r, "ws")
		server := chi.URLParam(r, "server")
		q := r.URL.Query().Get("duration")
		d, timed := allowedPinDurations[q]
		durable := q == pigtail.PinUntilUnpinned
		if !timed && !durable {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"duration must be one of 30m,1h,4h,"+pigtail.PinUntilUnpinned,
				map[string]interface{}{"duration": q})
			return
		}
		ok := svc.RAMMonitor != nil
		if ok && durable {
			ok = svc.RAMMonitor.PinUntilUnpinned(ws, server)
		} else if ok {
			ok = svc.RAMMonitor.Pin(ws, server, d)
		}
		if !ok {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such running server",
				map[string]interface{}{"workspace": ws, "server": server})
			return
		}
		out := map[string]interface{}{"workspace": ws, "server": server, "pinned_until": int64(0)}
		if durable {
			out["pinned_durable"] = true
		} else {
			out["pinned_until"] = time.Now().Add(d).Unix()
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// UnpinServer handles DELETE /servers/{ws}/{server}/pin. Idempotent.
func UnpinServer(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws := chi.URLParam(r, "ws")
		server := chi.URLParam(r, "server")
		if svc.RAMMonitor != nil {
			svc.RAMMonitor.Unpin(ws, server)
		}
		resp.JSON(w, r, http.StatusOK, map[string]interface{}{"workspace": ws, "server": server, "pinned_until": 0})
	}
}

// StopServer handles POST /servers/{ws}/{server}/stop — kill the running server now.
func StopServer(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws := chi.URLParam(r, "ws")
		server := chi.URLParam(r, "server")
		if svc.RAMMonitor == nil || !svc.RAMMonitor.StopServer(ws, server) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such running server",
				map[string]interface{}{"workspace": ws, "server": server})
			return
		}
		resp.JSON(w, r, http.StatusOK, map[string]interface{}{"workspace": ws, "server": server, "stopped": true})
	}
}

// StopAllServers handles POST /servers/stop-all.
func StopAllServers(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := [][2]string{}
		if svc.RAMMonitor != nil {
			out = svc.RAMMonitor.StopAll()
		}
		stopped := make([]map[string]string, 0, len(out))
		for _, p := range out {
			stopped = append(stopped, map[string]string{"workspace": p[0], "server": p[1]})
		}
		resp.JSON(w, r, http.StatusOK, map[string]interface{}{"stopped": stopped})
	}
}

// serverStoppedRequest is the body of POST /servers/stopped.
type serverStoppedRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Server      string `json:"server"`
}

// MarkServerStopped handles POST /servers/stopped — the local CLI telling the
// daemon that a dev server it is about to stop losing its port is going away
// on purpose, so the death detector does not report it. Best-effort by design
// on the caller's side: a daemon that never hears about a stop reports a
// harmless false death, never the reverse.
func MarkServerStopped(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req serverStoppedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.WorkspaceID == "" || req.Server == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"workspace_id and server are required", map[string]interface{}{
					"workspace_id": req.WorkspaceID, "server": req.Server,
				})
			return
		}
		if svc.RAMMonitor != nil {
			svc.RAMMonitor.MarkIntentionalStop(req.WorkspaceID, req.Server)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// stopIdleRequest is the body of POST /servers/stop-idle.
type stopIdleRequest struct {
	GraceSeconds int `json:"grace_seconds"`
}

// StopIdleServers handles POST /servers/stop-idle — stops every dev server
// idle at least grace_seconds (or the daemon's configured idle-purge grace
// when grace_seconds is omitted/zero), sparing pinned servers. Unlike the
// silent evictions the daemon performs on its own (idle-purge, battery, the
// server cap), this reports every server's outcome — including the ones it
// spared and why. That's the point of the command: it names the equivalent
// of what those paths kill quietly.
func StopIdleServers(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req stopIdleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.GraceSeconds < 0 {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "grace_seconds must be >= 0", nil)
			return
		}
		out := pigtail.StopIdleResponse{Servers: []pigtail.StopIdleServerView{}}
		if svc.RAMMonitor == nil {
			resp.JSON(w, r, http.StatusOK, out)
			return
		}
		grace := time.Duration(req.GraceSeconds) * time.Second
		if grace <= 0 {
			grace = svc.RAMMonitor.IdlePurgeGrace()
		}
		if grace <= 0 {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				"no grace_seconds given and idle-purge is disabled (grace=0) — pass an explicit duration", nil)
			return
		}
		for _, res := range svc.RAMMonitor.StopIdle(grace) {
			out.Servers = append(out.Servers, pigtail.StopIdleServerView{
				WorkspaceID: res.WorkspaceID,
				Server:      res.Server,
				Port:        res.Port,
				IdleSeconds: res.IdleSeconds,
				Stopped:     res.Stopped,
				Reason:      res.Reason,
			})
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
