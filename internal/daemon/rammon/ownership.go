package rammon

import (
	"sort"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// PortConflict is a configured server whose port is held, but not by a
// process of its own worktree. Recorded by snapshot(); never acted on.
type PortConflict struct {
	WorkspaceID  string
	Path         string // the conflicted workspace's path
	Server, Kind string
	Port         int // the port .env.local names (the effective one)
	// RegisteredPort is the registry's port for Kind (0 when none).
	RegisteredPort int
	// Reason is ConflictForeignListener or ConflictEnvDiverges.
	Reason            string
	PIDs              []int  // the foreign holders
	HolderPath        string // the owning worktree's path, else the holder's cwd, "" when unknown
	HolderWorkspaceID string // the live workspace containing that cwd, "" when none
}

// Reasons of a PortConflict.
const (
	// ConflictForeignListener: the port is held, but not by a process of the
	// workspace's own worktree.
	ConflictForeignListener = "foreign_listener"
	// ConflictEnvDiverges: .env.local names another port than the registry.
	ConflictEnvDiverges = "env_diverges"
)

// cwdsOf resolves every pid of the pass in one call. nil func: attribution off.
func (m *RAMMonitor) cwdsOf(probes map[int]portProbe) (map[int]string, error) {
	if m.cwds == nil {
		return nil, nil
	}
	seen := map[int]bool{}
	var pids []int
	for _, pr := range probes {
		for _, p := range pr.pids {
			if !seen[p] {
				seen[p] = true
				pids = append(pids, p)
			}
		}
	}
	sort.Ints(pids)
	return devserver.ProbeCwds(m.cwds, pids) // retried once: lsof fails under load
}

// attribute is a probe as seen by one workspace. With attribution off
// (cwds == nil && cwdErr == nil) it is the probe unchanged. A failed cwd
// probe makes the pair undecided. Otherwise only owned pids remain; foreign
// lists the others.
func (m *RAMMonitor) attribute(pr portProbe, wsPath string, cwds map[int]string, cwdErr error) (own portProbe, foreign map[int]string) {
	if pr.err != nil || !pr.busy {
		return pr, nil
	}
	if cwdErr != nil {
		return portProbe{err: cwdErr}, nil
	}
	if m.cwds == nil {
		return pr, nil
	}
	owned, foreign := devserver.SplitByCwd(pr.pids, cwds, wsPath)
	return portProbe{busy: len(owned) > 0, pids: owned}, foreign
}

// holderOf names the live workspace whose path contains cwd (longest match).
func holderOf(cwd string, wss []pigtail.Workspace) (path, id string) {
	if cwd == "" {
		return "", ""
	}
	best := -1
	for i, ws := range wss {
		if ws.Path != "" && devserver.Within(cwd, ws.Path) && (best < 0 || len(ws.Path) > len(wss[best].Path)) {
			best = i
		}
	}
	if best < 0 {
		return cwd, ""
	}
	return wss[best].Path, wss[best].ID
}

// AttributesByCwd reports whether listeners are attributed by cwd (the
// daemon wires it; tests built with New alone leave it off).
func (m *RAMMonitor) AttributesByCwd() bool { return m.cwds != nil }

// Conflicts returns the conflicts the last snapshot recorded.
func (m *RAMMonitor) Conflicts() []PortConflict {
	m.conflictsMu.Lock()
	defer m.conflictsMu.Unlock()
	return append([]PortConflict(nil), m.conflicts...)
}

func (m *RAMMonitor) setConflicts(c []PortConflict) {
	m.conflictsMu.Lock()
	m.conflicts = c
	m.conflictsMu.Unlock()
}

// foreignConflict builds the conflict a foreign-only listener makes for
// (ws, srv): the first foreign pid (sorted) names the holder.
func foreignConflict(ws pigtail.Workspace, srv ServerProc, foreign map[int]string, wss []pigtail.Workspace) PortConflict {
	pids := make([]int, 0, len(foreign))
	for p := range foreign {
		pids = append(pids, p)
	}
	sort.Ints(pids)
	path, id := holderOf(foreign[pids[0]], wss)
	return PortConflict{
		WorkspaceID: ws.ID, Path: ws.Path, Server: srv.Name, Kind: srv.Kind, Port: srv.Port,
		RegisteredPort: srv.DBPort, Reason: ConflictForeignListener,
		PIDs: pids, HolderPath: path, HolderWorkspaceID: id,
	}
}
