package handlers

import (
	"github.com/aloysbr/pigtail/internal/devserver"
)

// listenerOf probes port and classifies its listeners for wsPath
// (devserver.ClassifyListeners: owned, foreign, or owner unknown when the cwd
// probe failed twice). Unknown is never killed, never called "ours", and
// never named another worktree. err is the port probe's own failure.
func listenerOf(svc *Services, port int, wsPath string) (devserver.Ownership, error) {
	probe := svc.PortBusy
	if probe == nil {
		probe = devserver.IsPortBusy
	}
	busy, pids, err := probe(port)
	if err != nil || !busy {
		return devserver.Ownership{}, err
	}
	cwdsOf := svc.ProcessCwds
	if cwdsOf == nil {
		cwdsOf = devserver.ProcessCwds
	}
	return devserver.ClassifyListeners(pids, wsPath, cwdsOf), nil
}

// heldElsewhereDetails are the 409 details of a port not held by the
// workspace's own server: the holder's worktree ("" when unknown), the pids,
// and owner_unknown when the cwd probe failed.
func heldElsewhereDetails(o devserver.Ownership, containerDir, kind string, port int) map[string]interface{} {
	d := map[string]interface{}{"kind": kind, "port": port, "holder_path": o.HolderPath(containerDir), "pids": o.ForeignPIDs()}
	if o.Unknown() {
		d["owner_unknown"] = true
	}
	return d
}

func killPIDs(svc *Services, pids []int) {
	if svc.KillPIDs != nil {
		svc.KillPIDs(pids)
		return
	}
	devserver.KillPIDs(pids)
}
