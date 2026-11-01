package daemon

import (
	"os"
	"sort"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// StoreEnvSources lists, for the allocator, every live workspace's .env.local
// and its port variables (all kinds of its project). Live = row in the store
// and directory on disk, the same test rammon applies (liveWorkspaces).
func StoreEnvSources(st *store.Store) ports.EnvLister { return storeEnvSources{st} }

type storeEnvSources struct{ st *store.Store }

func (s storeEnvSources) EnvSources() ([]ports.EnvSource, error) {
	wss, err := s.st.ListWorkspaces("")
	if err != nil {
		return nil, err
	}
	projects := map[string]*pigtail.Project{}
	var out []ports.EnvSource
	for _, ws := range wss {
		if ws.Path == "" {
			continue
		}
		if _, err := os.Stat(ws.Path); err != nil {
			continue
		}
		p, ok := projects[ws.ProjectID]
		if !ok {
			p, _ = s.st.GetProject(ws.ProjectID) // unknown project: fall back to the workspace's own kinds
			projects[ws.ProjectID] = p
		}
		kinds := map[string]bool{}
		var overrides map[string]string
		if p != nil {
			overrides = p.PortEnvs
			for k := range p.PortRanges {
				kinds[k] = true
			}
		}
		for k := range ws.Ports {
			kinds[k] = true
		}
		vars := make([]string, 0, len(kinds))
		for k := range kinds {
			vars = append(vars, pigtail.PortEnvName(k, overrides))
		}
		sort.Strings(vars)
		out = append(out, ports.EnvSource{Dir: ws.Path, Vars: vars})
	}
	return out, nil
}
