package sessionlaunch

import (
	"sort"
	"strings"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// LiveWorkspaces maps each cmux workspace UUID (upper case) to the pids of the
// Claude processes running in it, and lists the Claude pids whose environment
// could not be read: each of them could be a live session of the subject that
// cannot be ruled out. procs lists the Claude processes
// (claudesession.ClaudeProcesses), environ reads one's raw environment
// (cmux.ProcessEnviron).
func LiveWorkspaces(procs func() ([]claudesession.Proc, error), environ func(pid int) (string, error)) (map[string][]int, []int, error) {
	ps, err := procs()
	if err != nil {
		return nil, nil, err
	}
	m := map[string][]int{}
	var unreadable []int
	for _, p := range ps {
		env, err := environ(p.PID)
		if err != nil {
			unreadable = append(unreadable, p.PID)
			continue
		}
		if ws, ok := cmux.EnvValue(env, cmux.EnvWorkspaceID); ok && ws != "" {
			ws = strings.ToUpper(ws)
			m[ws] = append(m[ws], p.PID)
		}
	}
	return m, unreadable, nil
}

// LiveConflict finds a live session among the subject's candidate tabs: the
// tabs bound to it and those its launch records name (a margin record does
// not name a tab of its own). excludeWS (the caller's own tab, upper case)
// never conflicts. The candidates are read in sorted order, so the answer is
// stable.
func LiveConflict(entries []spawnreg.Entry, live map[string][]int, bindings []pigtail.SubjectCmuxBinding, subjectID, excludeWS string) (ws string, pids []int, found bool) {
	candidates := map[string]bool{}
	for _, b := range bindings {
		candidates[strings.ToUpper(b.CmuxWorkspaceID)] = true
	}
	for _, e := range entries {
		if e.SubjectID != "" && e.SubjectID == subjectID && e.Kind != spawnreg.KindMargin && e.WorkspaceID != "" {
			candidates[e.WorkspaceID] = true
		}
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if id != excludeWS && len(live[id]) > 0 {
			return id, live[id], true
		}
	}
	return "", nil, false
}
