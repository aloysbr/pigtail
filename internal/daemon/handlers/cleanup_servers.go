package handlers

import (
	"strings"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// stopWorkspaceServers stops the dev servers listening on the workspace's
// ports from inside its worktree (with their descendants) and proves them
// gone. A *devserver.ServersRunningError means one survived or could not be
// attributed: the caller removes nothing. It takes no force flag on purpose:
// --force skips the guards on the user's work, never this proof.
//
// Ports come from .env.local, the row's as the fallback (the same resolver
// rammon and rename use), so a worktree already gone is still handled.
func stopWorkspaceServers(svc *Services, ws *pigtail.Workspace) ([]pigtail.StoppedServer, error) {
	var cfg *pigtail.ProjectConfig
	if dctx, err := devserver.ResolveContextFromPath(ws.Path); err == nil && dctx != nil {
		cfg = dctx.Config
	}
	ports := devserver.WorkspacePorts(ws.Path, cfg, ws.Ports)
	if len(ports) == 0 {
		return nil, nil
	}
	// A deliberate stop: mark it before the kill, or the next sweep reports
	// each server as a death.
	if svc.RAMMonitor != nil {
		svc.RAMMonitor.MarkIntentionalStopsForWorkspace(ws.ID)
	}
	stops, err := devserver.StopWorktreeServers(ws.Path, ports, devserver.StopDeps{
		PortBusy: svc.PortBusy,
		Cwds:     svc.ProcessCwds,
		Parents:  svc.ProcessParents,
		Kill:     svc.KillPIDs,
		Alive:    svc.ProcessAlive,
		Grace:    svc.ServerStopGrace,
	})
	for _, s := range stops {
		svc.logf("cleanup stopped dev server: workspace=%s port=%d kinds=%s pids=%v",
			ws.ID, s.Port, strings.Join(s.Kinds, "+"), s.PIDs)
	}
	return stops, err
}
