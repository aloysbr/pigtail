package devserver

import "github.com/aloysbr/pigtail/pkg/pigtail"

// WorkspacePorts is the one resolver of a workspace's ports, shared by
// rammon and the watchdog so they can never disagree: for every kind (the
// config's and the store's), the port .env.local assigns — what `pigtail dev`
// sources and launches — else the store's, when the file is missing, lacks
// the variable, or holds something that is not a port. cfg may be nil
// (no resolvable pigtail.yaml): default variable names then.
func WorkspacePorts(wtDir string, cfg *pigtail.ProjectConfig, dbPorts map[string]int) map[string]int {
	kinds := map[string]string{} // kind → env var
	for k := range dbPorts {
		kinds[k] = pigtail.DefaultPortEnv(k)
	}
	if cfg != nil {
		for k, ps := range cfg.Ports {
			kinds[k] = pigtail.DefaultPortEnv(k)
			if ps.Env != "" {
				kinds[k] = ps.Env
			}
		}
	}
	out := map[string]int{}
	for k, envVar := range kinds {
		if p, _, err := readPort(wtDir, envVar); err == nil && p > 0 {
			out[k] = p
		} else if dbPorts[k] > 0 {
			out[k] = dbPorts[k]
		}
	}
	return out
}
