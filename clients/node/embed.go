// Package nodeclient carries pigtail's zero-dependency Node CPU slot client
// and installs it wherever a run needs it.
//
// The files are embedded rather than looked up on disk because an installed
// `pigtail` binary has no source tree to look in — and because the target repo
// must contain nothing of pigtail's. `pigtail test` writes them into the repo's
// node_modules cache for the duration of a run.
//
// Four files, two runners: client.cjs is the transport, client.mjs re-exports
// it for vitest's ESM setup, and slots.mjs / jest-slots.cjs are the per-runner
// setup files. Jest's has to be CommonJS — it is loaded from under
// node_modules, which transformIgnorePatterns keeps out of every transform.
package nodeclient

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed client.cjs client.mjs slots.mjs jest-slots.cjs
var files embed.FS

// installed are the files Install writes, transport first so a setup file is
// never on disk without the module it loads.
var installed = []string{"client.cjs", "client.mjs", "slots.mjs", "jest-slots.cjs"}

// Setup holds the per-runner setup file paths Install wrote.
type Setup struct {
	Vitest string
	Jest   string
}

// Install writes the Node client into dir and returns the setup file paths.
// Overwrites what is there: it runs before every `pigtail test`, and a stale
// copy from an older pigtail would be worse than a rewrite.
func Install(dir string) (Setup, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Setup{}, fmt.Errorf("create client dir: %w", err)
	}
	for _, name := range installed {
		data, err := files.ReadFile(name)
		if err != nil {
			return Setup{}, fmt.Errorf("read embedded %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil { //nolint:gosec // setup files the repo's own runner reads
			return Setup{}, fmt.Errorf("write %s: %w", name, err)
		}
	}
	return Setup{
		Vitest: filepath.Join(dir, "slots.mjs"),
		Jest:   filepath.Join(dir, "jest-slots.cjs"),
	}, nil
}
