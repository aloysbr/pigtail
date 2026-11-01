package testrun

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aloysbr/pigtail/internal/heavycmd"
)

// vitestConfigNames are tried in order: a dedicated vitest config wins over a
// vite config that merely carries a `test` key. The .cts/.cjs spellings are
// here because vitest accepts them; leaving them out did not make such a repo
// fail, it made it run unarbitrated while looking configless — the silent
// failure mode this package exists to end.
var vitestConfigNames = []string{
	"vitest.config.ts", "vitest.config.mts", "vitest.config.cts",
	"vitest.config.js", "vitest.config.mjs", "vitest.config.cjs",
	"vite.config.ts", "vite.config.mts", "vite.config.cts",
	"vite.config.js", "vite.config.mjs", "vite.config.cjs",
}

// vitestWorkspaceNames mark a layout whose projects each need their own
// wrapper — pigtail does not arbitrate those yet (PR B), and says so.
var vitestWorkspaceNames = []string{
	"vitest.workspace.ts", "vitest.workspace.mts", "vitest.workspace.js",
	"vitest.workspace.mjs", "vitest.workspace.json",
}

// jestConfigNames identify a jest repo, in the order jest itself prefers.
var jestConfigNames = []string{
	"jest.config.js", "jest.config.cjs", "jest.config.mjs",
	"jest.config.ts", "jest.config.mts", "jest.config.cts",
	"jest.config.json",
}

// Request is what the caller knows before any resolution: where the run was
// invoked, which worktree it belongs to, and the daemon's slot budget (0 when
// the daemon is unreachable).
type Request struct {
	Cwd    string
	Root   string
	Budget int

	// Runner forces one runner. Zero value [RunnerAuto] lets Resolve pick.
	Runner Runner
}

// Resolve answers how this run gets arbitrated, without writing anything.
//
// Every path returns a usable Plan. An unarbitrated one carries a named Skip
// with a detail and, where there is one, a remedy — the caller runs the suite
// either way.
func Resolve(req Request) Plan {
	p := Plan{
		Runner: RunnerVitest,
		Layout: LayoutSingle,
		Grain:  GrainFile,
		Cwd:    req.Cwd,
		Root:   req.Root,
		Budget: req.Budget,
	}

	if req.Budget <= 0 {
		return p.WithSkip(SkipNoBudget,
			"the daemon did not answer with a CPU slot budget",
			"check `pigtail slots`; the daemon may be down (a run without it is a plain vitest run)")
	}

	if pickJest(req) {
		return resolveJest(p, req)
	}
	if jest := findUp(req.Cwd, req.Root, jestConfigNames); jest != "" {
		p.Notes = append(p.Notes, fmt.Sprintf(
			"this repo also has jest (%s) — `pigtail test --runner=jest` arbitrates that suite",
			filepath.Base(jest)))
	}

	if ws := findUp(req.Cwd, req.Root, vitestWorkspaceNames); ws != "" {
		return resolveWorkspace(p, req, ws)
	}

	base := findUp(req.Cwd, req.Root, vitestConfigNames)
	if base == "" {
		return p.WithSkip(SkipNoConfig,
			"no vitest or vite config found between here and the worktree root",
			"run from the directory that holds the config, as you would run vitest itself")
	}
	p.BaseConfig = base

	// The wrapper does `import { mergeConfig } from 'vitest/config'`, and Vite
	// externalises bare specifiers when it bundles a config file — so Node
	// resolves that import from the wrapper's own directory, walking up for a
	// node_modules that actually contains vitest. <root>/node_modules merely
	// existing proves nothing: in a workspace without hoisting it always
	// exists while vitest lives in packages/x/node_modules, and a wrapper
	// placed at the root would fail to load, killing the whole run.
	nm := resolvePackageNodeModules(filepath.Dir(base), req.Root, "vitest")
	if nm == "" {
		return p.WithSkip(SkipNoVitest,
			"no node_modules containing vitest between the config and the worktree root",
			"install dependencies (the wrapper imports 'vitest/config' from where it is written)")
	}
	p.NodeModules = nm

	p.Ceiling = req.Budget
	// Only the wrapper can see the repo's own ceiling, at config-evaluation
	// time — so pigtail promises the budget as an upper bound and lets the
	// repo lower it further. It never raises one: see MinCeiling.
	p.CeilingFrom = "min(repo,pigtail)"
	return applyResolvedFacts(p)
}

// applyResolvedFacts folds in what a previous run of this config recorded
// about itself (see resolvedFacts). Today that is one thing: browser mode,
// which changes both what the run is charged and how it is arbitrated.
//
// It never *removes* arbitration — a run with no facts yet is planned exactly
// as before — so a first run is never worse off than it was.
func applyResolvedFacts(p Plan) Plan {
	if p.NodeModules == "" || p.BaseConfig == "" {
		return p
	}
	facts, ok := ReadResolved(filepath.Join(p.NodeModules, ".cache", "pigtail"), p.BaseConfig)
	if !ok || !facts.Browser {
		return p
	}
	p.Class = string(heavycmd.ClassVitestBrowser)
	p.Workers = facts.MaxWorkers
	// No CPU slot: the scarce resource of a browser run is the browser, and
	// the memory admission's per-class cap is what arbitrates it.
	p.Grain = GrainRun
	p.Notes = append(p.Notes, fmt.Sprintf(
		"browser mode (recorded by the last run): charged as %s for ~%d live browsers, "+
			"and held to the per-class cap instead of taking CPU slots",
		heavycmd.ClassVitestBrowser, facts.MaxWorkers))
	return p
}

// resolveWorkspace plans a run whose projects are declared by a workspace
// file: read its patterns, expand them, and describe one project per config.
//
// The projects are resolved here, before anything is written, because the
// list is what `--explain` prints. A layout pigtail cannot read resolves to a
// named skip and still runs the suite — unarbitrated, and saying so.
func resolveWorkspace(p Plan, req Request, ws string) Plan {
	p.Layout = LayoutWorkspace
	p.WorkspaceFile = ws
	wsDir := filepath.Dir(ws)

	patterns, err := WorkspacePatterns(ws)
	if err != nil {
		return p.WithSkip(SkipWorkspaceNotStatic,
			fmt.Sprintf("%s: %v", filepath.Base(ws), err),
			"run the suite through `pigtail admit -- <your test command>` so it at least takes a memory lease")
	}

	configs, err := ExpandGlobs(wsDir, patterns)
	if err != nil {
		return p.WithSkip(SkipWorkspaceNotStatic,
			fmt.Sprintf("could not expand the patterns in %s: %v", filepath.Base(ws), err),
			"run the suite through `pigtail admit -- <your test command>` so it at least takes a memory lease")
	}
	if len(configs) == 0 {
		return p.WithSkip(SkipWorkspaceEmpty,
			fmt.Sprintf("%s matched no project config in this checkout", filepath.Base(ws)),
			"check the patterns in the workspace file, or install dependencies if the projects are not checked out")
	}
	p.Projects = describeProjects(wsDir, configs)

	// The root config, when there is one, carries options that belong to the
	// run as a whole (reporters, coverage, the ceiling). It is optional: a
	// repo may declare only a workspace file.
	if base := findUp(wsDir, req.Root, vitestConfigNames); base != "" {
		p.BaseConfig = base
	}

	// Every wrapper is written into one cache directory and each does
	// `import { mergeConfig } from 'vitest/config'`, so that directory is the
	// one where vitest resolves. A project's *own* imports are unaffected:
	// vite bundles each project config from where it lives, so a dependency
	// installed only under apps/web still resolves from apps/web.
	nm := resolvePackageNodeModules(wsDir, req.Root, "vitest")
	if nm == "" {
		return p.WithSkip(SkipNoVitest,
			"no node_modules containing vitest between the workspace file and the worktree root",
			"install dependencies (the wrappers import 'vitest/config' from where they are written)")
	}
	p.NodeModules = nm

	p.Ceiling = req.Budget
	p.CeilingFrom = "min(repo,pigtail)"
	return p
}

// findUp returns the first of names found in cwd or any directory above it, up
// to and including root, mirroring vitest's own upward config search. It never
// escapes the worktree: a config above root belongs to another checkout.
func findUp(cwd, root string, names []string) string {
	dir := cwd
	for {
		for _, name := range names {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		if dir == root {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without ever matching root itself
			// (cwd was not inside root) — stop rather than loop forever.
			return ""
		}
		dir = parent
	}
}

// resolvePackageNodeModules returns the nearest node_modules at or above dir
// that actually *contains* pkg, never looking above root.
//
// Containing the package is the whole point: <root>/node_modules exists in
// every workspace, hoisted or not, so its mere existence proves nothing about
// whether `import 'vitest/config'` from a file underneath it will resolve.
// Only the package directory being there does. Stat follows symlinks, so
// pnpm's node_modules/vitest -> .pnpm/… link answers yes, as it should.
func resolvePackageNodeModules(dir, root, pkg string) string {
	for {
		nm := filepath.Join(dir, "node_modules")
		if _, err := os.Stat(filepath.Join(nm, pkg)); err == nil {
			return nm
		}
		if dir == root {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
