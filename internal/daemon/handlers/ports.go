package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// PatchWorkspacePorts handles PATCH /workspaces/{id}/ports.
//
//	resync:   registry ← .env.local (what `pigtail dev` launches)
//	reassign: fresh port (allocator rules) → .env.local → registry
//
// Unscoped (workspace layer) and outside the maintenance freeze, like rename.
func PatchWorkspacePorts(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.PatchPortsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		id := chi.URLParam(r, "id")
		ws, err := svc.Store.GetWorkspace(id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrWorkspaceNotFound, "no such workspace", map[string]interface{}{"id": id})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		proj, err := svc.Store.GetProject(ws.ProjectID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrProjectNotFound, "workspace's project not found", map[string]interface{}{"project_id": ws.ProjectID})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if req.Mode != pigtail.PortsModeResync && req.Mode != pigtail.PortsModeReassign {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, `mode must be "resync" or "reassign"`, map[string]interface{}{"mode": req.Mode})
			return
		}
		pf, ok := svc.Backend.(backend.PortFile)
		if !ok {
			resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError, "backend keeps no port file", nil)
			return
		}
		kinds := make([]string, 0, len(proj.PortRanges))
		for k := range proj.PortRanges {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		if req.Mode == pigtail.PortsModeResync {
			resyncPorts(svc, resp, w, r, ws, proj, pf, kinds)
			return
		}
		reassignPorts(svc, resp, w, r, ws, proj, pf, kinds, req.Kinds)
	}
}

// reservation is a port held pending in the allocator until the registry
// write succeeds (Confirm) or the request is abandoned (Release).
type reservation struct {
	kind string
	port int
}

func settleReservations(svc *Services, held []reservation, confirm bool) {
	if svc.Ports == nil {
		return
	}
	for _, h := range held {
		if confirm {
			svc.Ports.Confirm(h.kind, h.port)
		} else {
			svc.Ports.Release(h.kind, h.port)
		}
	}
}

func updateWorkspacePorts(svc *Services, id string, p map[string]int) error {
	if svc.UpdateWorkspacePorts != nil {
		return svc.UpdateWorkspacePorts(id, p)
	}
	return svc.Store.UpdateWorkspacePorts(id, p)
}

func copyPorts(p map[string]int) map[string]int {
	out := make(map[string]int, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

// heldByOther returns the id of another live workspace whose registry has
// port (any kind), "" when none.
func heldByOther(svc *Services, wsID string, port int) (string, error) {
	wss, err := svc.Store.ListWorkspaces("")
	if err != nil {
		return "", err
	}
	for _, o := range wss {
		if o.ID == wsID || o.Path == "" {
			continue
		}
		if _, err := os.Stat(o.Path); err != nil {
			continue
		}
		for _, p := range o.Ports {
			if p == port {
				return o.ID, nil
			}
		}
	}
	return "", nil
}

func resyncPorts(svc *Services, resp Responder, w http.ResponseWriter, r *http.Request,
	ws *pigtail.Workspace, proj *pigtail.Project, pf backend.PortFile, kinds []string) {
	filePorts, err := pf.ReadPorts(ws, kinds, proj.PortEnvs)
	if err != nil {
		resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError, err.Error(), nil)
		return
	}
	merged := copyPorts(ws.Ports)
	changes := []pigtail.PortChange{}
	var held []reservation
	fail := func(status int, code pigtail.ErrorCode, msg string, details map[string]interface{}) {
		settleReservations(svc, held, false)
		resp.Error(w, r, status, code, msg, details)
	}
	for _, kind := range kinds {
		port, ok := filePorts[kind]
		if !ok || port == ws.Ports[kind] {
			continue
		}
		own, perr := listenerOf(svc, port, ws.Path)
		if perr != nil {
			fail(http.StatusUnprocessableEntity, pigtail.ErrBackendError, fmt.Sprintf("could not probe port %d: %v", port, perr), nil)
			return
		}
		if own.HeldElsewhere() {
			msg := fmt.Sprintf(`port %d (%s) is %s — run "pigtail ports reassign %s --kind %s" instead`,
				port, kind, own.Describe(filepath.Dir(ws.Path)), ws.ID, kind)
			if own.Unknown() {
				msg = fmt.Sprintf("port %d (%s): %s — registry unchanged; retry", port, kind, own.Describe(filepath.Dir(ws.Path)))
			}
			fail(http.StatusConflict, pigtail.ErrConflict, msg, heldElsewhereDetails(own, filepath.Dir(ws.Path), kind, port))
			return
		}
		if svc.Ports != nil {
			if rerr := svc.Ports.Reserve(kind, port, ws.Path, ws.Ports[kind]); rerr != nil {
				var apiErr *pigtail.APIError
				if !errors.As(rerr, &apiErr) {
					fail(http.StatusInternalServerError, pigtail.ErrInternal, rerr.Error(), nil)
					return
				}
				details := apiErr.Details
				msg := apiErr.Message
				if details["reason"] == "registered" {
					other, _ := heldByOther(svc, ws.ID, port)
					name := other
					if name == "" {
						name = "a static server or a workspace whose directory is gone"
					} else {
						name = "workspace " + name
					}
					msg = fmt.Sprintf(`port %d (%s) is registered to %s — run "pigtail ports reassign %s --kind %s" instead`,
						port, kind, name, ws.ID, kind)
					details["held_by_workspace"] = other
				}
				fail(statusForCode(apiErr.Code), apiErr.Code, msg, details)
				return
			}
			held = append(held, reservation{kind, port})
		}
		changes = append(changes, pigtail.PortChange{Kind: kind, From: ws.Ports[kind], To: port})
		merged[kind] = port
	}
	if len(changes) == 0 {
		resp.JSON(w, r, http.StatusOK, pigtail.PatchPortsResponse{WorkspaceID: ws.ID, Ports: ws.Ports, Changed: changes})
		return
	}
	if err := updateWorkspacePorts(svc, ws.ID, merged); err != nil {
		fail(http.StatusInternalServerError, pigtail.ErrInternal, "could not record the ports: "+err.Error(), nil)
		return
	}
	settleReservations(svc, held, true)
	resp.JSON(w, r, http.StatusOK, pigtail.PatchPortsResponse{WorkspaceID: ws.ID, Ports: merged, Changed: changes})
}

func reassignPorts(svc *Services, resp Responder, w http.ResponseWriter, r *http.Request,
	ws *pigtail.Workspace, proj *pigtail.Project, pf backend.PortFile, kinds, asked []string) {
	if len(asked) > 0 {
		for _, k := range asked {
			if _, ok := proj.PortRanges[k]; !ok {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
					fmt.Sprintf("unknown port kind %q for this project", k), map[string]interface{}{"kind": k})
				return
			}
		}
		kinds = append([]string(nil), asked...)
		sort.Strings(kinds)
	}
	if svc.Ports == nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "no port allocator configured", nil)
		return
	}
	// The current port is the one .env.local names (what runs), else the
	// registry's. An unreadable file falls back to the registry: reassign is
	// the repair for exactly such a file.
	filePorts, err := pf.ReadPorts(ws, kinds, proj.PortEnvs)
	if err != nil {
		filePorts = nil
	}
	current := map[string]int{}
	for _, kind := range kinds {
		cur := filePorts[kind]
		if cur == 0 {
			cur = ws.Ports[kind]
		}
		current[kind] = cur
		if cur <= 0 {
			continue
		}
		own, perr := listenerOf(svc, cur, ws.Path)
		if perr != nil {
			resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError, fmt.Sprintf("could not probe port %d: %v", cur, perr), nil)
			return
		}
		if len(own.Owned) > 0 {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
				fmt.Sprintf(`%s server on :%d is running — run "pigtail stop" first`, kind, cur),
				map[string]interface{}{"kind": kind, "port": cur, "pids": own.Owned})
			return
		}
		if own.Unknown() {
			// It may be this workspace's own server: moving the port under a
			// running server is what "pigtail stop first" guards against.
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
				fmt.Sprintf("%s port :%d: %s — not reassigned; retry", kind, cur, own.Describe(filepath.Dir(ws.Path))),
				heldElsewhereDetails(own, filepath.Dir(ws.Path), kind, cur))
			return
		}
		// Held only by another worktree's process: the case to repair.
	}
	var held []reservation
	fresh := map[string]int{}
	for _, kind := range kinds {
		p, aerr := svc.Ports.Allocate(proj.PortRanges, kind)
		if aerr != nil {
			settleReservations(svc, held, false)
			var apiErr *pigtail.APIError
			if errors.As(aerr, &apiErr) {
				resp.Error(w, r, statusForCode(apiErr.Code), apiErr.Code, apiErr.Message, apiErr.Details)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, aerr.Error(), nil)
			return
		}
		held = append(held, reservation{kind, p})
		fresh[kind] = p
	}
	restore, err := pf.WritePorts(ws, fresh, proj.PortEnvs)
	if err != nil {
		settleReservations(svc, held, false)
		resp.Error(w, r, http.StatusUnprocessableEntity, pigtail.ErrBackendError, "write .env.local: "+err.Error(), nil)
		return
	}
	merged := copyPorts(ws.Ports)
	for k, p := range fresh {
		merged[k] = p
	}
	if err := updateWorkspacePorts(svc, ws.ID, merged); err != nil {
		settleReservations(svc, held, false)
		envPath := filepath.Join(ws.Path, ".env.local")
		if rerr := restore(); rerr != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal,
				fmt.Sprintf("could not record the new ports: %v — and restoring %s failed too: %v; that file now names the new ports while the registry keeps the old ones",
					err, envPath, rerr),
				map[string]interface{}{"path": envPath})
			return
		}
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal,
			fmt.Sprintf("could not record the new ports: %v — .env.local restored, nothing changed", err), nil)
		return
	}
	settleReservations(svc, held, true)
	changes := []pigtail.PortChange{}
	for _, kind := range kinds {
		changes = append(changes, pigtail.PortChange{Kind: kind, From: current[kind], To: fresh[kind]})
	}
	resp.JSON(w, r, http.StatusOK, pigtail.PatchPortsResponse{WorkspaceID: ws.ID, Ports: merged, Changed: changes})
}
