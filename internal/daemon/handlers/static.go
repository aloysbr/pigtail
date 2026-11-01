package handlers

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// staticPortKind is the port-allocator "kind" used for static servers. They
// belong to no project; the daemon owns a single global range for this kind.
const staticPortKind = "static"

// RegisterStaticServerRoutes wires the /static-servers endpoints onto r.
// Called from server.buildRouter and from handler tests.
func RegisterStaticServerRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Post("/static-servers", CreateStaticServer(svc, resp))
	r.Get("/static-servers", ListStaticServers(svc, resp))
	r.Post("/static-servers/stop-all", StopAllStaticServers(svc, resp)) // static route before /{name}/...
	r.Delete("/static-servers/{name}", DeleteStaticServer(svc, resp))
	r.Post("/static-servers/{name}/start", StartStaticServer(svc, resp))
	r.Post("/static-servers/{name}/stop", StopStaticServer(svc, resp))
}

// CreateStaticServerRequest is the body of POST /static-servers.
type CreateStaticServerRequest struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	Port int    `json:"port,omitempty"`
}

// StaticServerView is a static-server record annotated with live running
// state. running_port mirrors the bound port when up (equals the record port).
type StaticServerView struct {
	pigtail.StaticServer
	Running     bool `json:"running"`
	RunningPort int  `json:"running_port,omitempty"`
}

// CreateStaticServer handles POST /static-servers — add and persist a record.
func CreateStaticServer(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateStaticServerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.Name == "" || req.Dir == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "name and dir are required", nil)
			return
		}

		// Resolve dir to absolute and validate it exists and is a directory.
		dir, err := filepath.Abs(req.Dir)
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "resolve dir: "+err.Error(), nil)
			return
		}
		info, err := os.Stat(dir)
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "dir does not exist: "+dir, map[string]interface{}{"dir": dir})
			return
		}
		if !info.IsDir() {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "not a directory: "+dir, map[string]interface{}{"dir": dir})
			return
		}

		// Reject duplicate names early (the column is UNIQUE, but a clear 409 beats a 500).
		if _, err := svc.Store.GetStaticServerByName(req.Name); err == nil {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "static server already exists: "+req.Name, map[string]interface{}{"name": req.Name})
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		// Resolve the port: allocate when unset, validate when given.
		port := req.Port
		allocated := false
		if port == 0 {
			if svc.Ports == nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "no port allocator wired", nil)
				return
			}
			p, err := svc.Ports.Allocate(map[string]pigtail.PortRange{staticPortKind: svc.StaticPortRange}, staticPortKind)
			if err != nil {
				var apiErr *pigtail.APIError
				if errors.As(err, &apiErr) {
					resp.Error(w, r, statusForCode(apiErr.Code), apiErr.Code, apiErr.Message, apiErr.Details)
					return
				}
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			port = p
			allocated = true
		} else {
			used, err := svc.Store.UsedPorts(staticPortKind)
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "list used ports: "+err.Error(), nil)
				return
			}
			if used[port] {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "port already in use", map[string]interface{}{"port": port})
				return
			}
			if !portBindable(port) {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "port not bindable", map[string]interface{}{"port": port})
				return
			}
		}

		ss := pigtail.StaticServer{
			ID:   genID(pigtail.PrefixStaticServer),
			Name: req.Name,
			Dir:  dir,
			Port: port,
		}
		if err := svc.Store.CreateStaticServer(&ss); err != nil {
			if allocated {
				svc.Ports.Release(staticPortKind, port)
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "persist static server: "+err.Error(), nil)
			return
		}
		// Persisted: the port is now visible via store.UsedPorts; drop it from
		// the allocator's in-flight set (allocate→persist→Confirm ordering).
		if allocated {
			svc.Ports.Confirm(staticPortKind, port)
		}

		resp.JSON(w, r, http.StatusCreated, ss)
	}
}

// ListStaticServers handles GET /static-servers — records + live running state.
func ListStaticServers(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		records, err := svc.Store.ListStaticServers()
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		out := make([]StaticServerView, 0, len(records))
		for _, ss := range records {
			v := StaticServerView{StaticServer: ss}
			if svc.StaticMgr != nil {
				if port, ok := svc.StaticMgr.Running(ss.Name); ok {
					v.Running = true
					v.RunningPort = port
				}
			}
			out = append(out, v)
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// DeleteStaticServer handles DELETE /static-servers/{name} — stop if running,
// then delete the record (name→id, then DeleteStaticServer by id).
func DeleteStaticServer(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		ss, err := svc.Store.GetStaticServerByName(name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such static server", map[string]interface{}{"name": name})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if svc.StaticMgr != nil {
			if _, ok := svc.StaticMgr.Running(name); ok {
				_ = svc.StaticMgr.Stop(name)
			}
		}
		if err := svc.Store.DeleteStaticServer(ss.ID); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "delete static server: "+err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, map[string]interface{}{"name": name, "id": ss.ID})
	}
}

// StartStaticServer handles POST /static-servers/{name}/start.
func StartStaticServer(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		ss, err := svc.Store.GetStaticServerByName(name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such static server", map[string]interface{}{"name": name})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if svc.StaticMgr == nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "no static-server manager wired", nil)
			return
		}
		if err := svc.StaticMgr.Start(*ss); err != nil {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, err.Error(), map[string]interface{}{"name": name})
			return
		}
		resp.JSON(w, r, http.StatusOK, StaticServerView{StaticServer: *ss, Running: true, RunningPort: ss.Port})
	}
}

// StopStaticServer handles POST /static-servers/{name}/stop.
func StopStaticServer(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if _, err := svc.Store.GetStaticServerByName(name); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such static server", map[string]interface{}{"name": name})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if svc.StaticMgr == nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "no static-server manager wired", nil)
			return
		}
		if err := svc.StaticMgr.Stop(name); err != nil {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, err.Error(), map[string]interface{}{"name": name})
			return
		}
		resp.JSON(w, r, http.StatusOK, map[string]interface{}{"name": name, "running": false})
	}
}

// StopAllStaticServers handles POST /static-servers/stop-all.
func StopAllStaticServers(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var stopped []string
		if svc.StaticMgr != nil {
			stopped = svc.StaticMgr.List()
			svc.StaticMgr.StopAll()
		}
		resp.JSON(w, r, http.StatusOK, map[string]interface{}{"stopped": stopped})
	}
}

// portBindable probes whether 127.0.0.1:<port> can be bound right now.
func portBindable(port int) bool {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
