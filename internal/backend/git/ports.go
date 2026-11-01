package git

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/envlocal"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var _ backend.PortFile = (*Backend)(nil)

// ReadPorts reads the workspace's own .env.local: kind → port for every kind
// the file assigns. A value that is not a port is an error — resync must not
// write a guess into the store.
func (b *Backend) ReadPorts(ws *pigtail.Workspace, kinds []string, envs map[string]string) (map[string]int, error) {
	path := filepath.Join(ws.Path, ".env.local")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	out := map[string]int{}
	for _, k := range kinds {
		name := envVarName(k, envs)
		val, ok := envlocal.Lookup(data, name)
		if !ok {
			continue
		}
		p, err := strconv.Atoi(val)
		if err != nil || p <= 0 {
			return nil, fmt.Errorf("invalid %s value in %s: %q", name, path, val)
		}
		out[k] = p
	}
	return out, nil
}

// WritePorts rewrites the given kinds' variables in the workspace's own
// .env.local through mergeEnvLocal — the path `pigtail new` uses — leaving
// every other line as it is. restore puts the previous bytes back (or removes
// the file it created), so a caller whose next step fails leaves no
// half-applied change behind.
func (b *Backend) WritePorts(ws *pigtail.Workspace, ports map[string]int, envs map[string]string) (func() error, error) {
	path := filepath.Join(ws.Path, ".env.local")
	existing, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if fi, serr := os.Stat(path); serr == nil {
		mode = fi.Mode().Perm()
	}
	restore := func() error {
		if !existed {
			return os.Remove(path)
		}
		return os.WriteFile(path, existing, mode)
	}
	kinds := make([]string, 0, len(ports))
	for k := range ports {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	assigns := make([]envAssign, 0, len(kinds))
	for _, k := range kinds {
		assigns = append(assigns, envAssign{envVarName(k, envs), strconv.Itoa(ports[k])})
	}
	if err := os.WriteFile(path, []byte(mergeEnvLocal(existing, assigns)), mode); err != nil {
		return nil, err
	}
	return restore, nil
}
