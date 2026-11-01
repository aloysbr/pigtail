package testrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pickJest reports whether this run targets jest.
//
// An explicit --runner always wins. Left to itself, pigtail picks jest only
// when the repo has no vitest config at all: `pigtail test` has always meant
// "run vitest", and a repo holding both (a large monorepo holds jest for
// units and vitest for Storybook) would otherwise change runner under the
// user's feet. The second runner is surfaced as a note instead.
func pickJest(req Request) bool {
	switch req.Runner {
	case RunnerJest:
		return true
	case RunnerVitest:
		return false
	}
	if findUp(req.Cwd, req.Root, vitestConfigNames) != "" {
		return false
	}
	if findUp(req.Cwd, req.Root, vitestWorkspaceNames) != "" {
		return false
	}
	return findUp(req.Cwd, req.Root, jestConfigNames) != ""
}

// resolveJest completes a jest plan, or names why it stays unarbitrated.
func resolveJest(p Plan, req Request) Plan {
	p.Runner = RunnerJest

	base := findUp(req.Cwd, req.Root, jestConfigNames)
	if base == "" {
		return p.WithSkip(SkipNoConfig,
			"no jest config found between here and the worktree root",
			"run from the directory that holds jest.config.js, as you would run jest itself")
	}
	p.BaseConfig = base

	if why := notRequirable(base); why != "" {
		return p.WithSkip(SkipJestConfigNotRequirable,
			fmt.Sprintf("%s is %s, and pigtail's wrapper is CommonJS: it cannot require it", filepath.Base(base), why),
			"run it through `pigtail admit -- npm test` so it at least takes a memory lease")
	}

	nm := resolvePackageNodeModules(filepath.Dir(base), req.Root, "jest")
	if nm == "" {
		return p.WithSkip(SkipNoJest,
			"no node_modules containing jest between the config and the worktree root",
			"install dependencies (pigtail runs the repo's own jest binary)")
	}
	p.NodeModules = nm

	p.Ceiling = req.Budget
	p.CeilingFrom = "min(repo,pigtail)"
	return p
}

// notRequirable says why a jest config cannot be require()d from CommonJS, or
// "" when it can. Extension first, then package.json "type" for a bare .js.
func notRequirable(config string) string {
	switch strings.ToLower(filepath.Ext(config)) {
	case ".mjs":
		return "an ES module"
	case ".ts", ".mts", ".cts":
		return "TypeScript, which needs the repo's own loader"
	case ".cjs", ".json":
		return ""
	}
	if packageType(filepath.Dir(config)) == "module" {
		return `a .js file in a package.json with "type": "module", so it is an ES module`
	}
	return ""
}

// packageType reads the nearest package.json's "type", walking up from dir.
// Unreadable or absent reads as CommonJS, which is Node's own default.
func packageType(dir string) string {
	for {
		b, err := os.ReadFile(filepath.Join(dir, "package.json")) //nolint:gosec // a path derived from the user's own cwd
		if err == nil {
			var pkg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(b, &pkg) == nil {
				return pkg.Type
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
