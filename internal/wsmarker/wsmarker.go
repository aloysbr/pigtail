// Package wsmarker keeps one file per cmux workspace that is bound to a
// subject or declared off-topic, so a Claude Code hook can tell "nothing to
// remind" with one stat and no fork (subject « Liaison session → sujet », D7).
// The daemon is the only writer; the hook only tests presence.
package wsmarker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// DirName is the marker directory, under the daemon's maintenance dir (daemonMaintenanceDir).
const DirName = "workspace-subject"

// Sync makes dir hold exactly one file per entry of states (workspace UUID → "bound" | "off-topic").
func Sync(dir string, states map[string]string) error {
	for ws := range states {
		if n, err := pigtail.NormalizeCmuxWorkspaceID(ws); err != nil || n != ws {
			return fmt.Errorf("wsmarker: bad workspace id %q", ws)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for ws, st := range states {
		p := filepath.Join(dir, ws)
		want := st + "\n"
		if cur, err := os.ReadFile(p); err == nil && string(cur) == want {
			continue
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, []byte(want), 0o600); err != nil {
			keep(err)
			continue
		}
		keep(os.Rename(tmp, p))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if _, ok := states[name]; !ok {
			keep(os.Remove(filepath.Join(dir, name)))
		}
	}
	return firstErr
}
