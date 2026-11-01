// Package wspath matches a directory against pigtail workspaces. The relay
// and the focus resolver share it, so both read a cwd the same way.
package wspath

import (
	"path/filepath"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Within reports whether path is root or lies under it.
func Within(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// Deepest returns the workspace whose non-empty Path contains cwd and whose
// path is the longest; false when none does.
func Deepest(cwd string, ws []pigtail.Workspace) (pigtail.Workspace, bool) {
	if cwd == "" {
		return pigtail.Workspace{}, false
	}
	best := ""
	var out pigtail.Workspace
	for _, w := range ws {
		if w.Path == "" || !Within(cwd, w.Path) {
			continue
		}
		if len(w.Path) > len(best) {
			best, out = w.Path, w
		}
	}
	return out, best != ""
}
