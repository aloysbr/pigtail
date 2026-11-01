package testrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures are built in t.TempDir rather than committed under testdata/
// because every one of them needs a node_modules/vitest directory: committing
// a fake node_modules invites every tool that skips node_modules by default to
// skip the fixture too, and it would be the one part of the tree nobody reads.
// tree() keeps the arrangement declarative all the same.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A vitest install is a directory; its package.json is what a later PR reads
// for the major version.
const vitestInstalled = `{"name":"vitest","version":"3.2.4"}`

func TestResolveArbitratesASingleConfig(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.config.ts":                 "export default {}",
		"node_modules/vitest/package.json": vitestInstalled,
	})
	p := Resolve(Request{Cwd: root, Root: root, Budget: 9})

	if !p.Arbitrated() {
		t.Fatalf("plan is not arbitrated: %+v", p.Skips)
	}
	if p.Runner != RunnerVitest || p.Layout != LayoutSingle || p.Grain != GrainFile {
		t.Errorf("runner/layout/grain = %s/%s/%s, want vitest/single/file", p.Runner, p.Layout, p.Grain)
	}
	if want := filepath.Join(root, "vitest.config.ts"); p.BaseConfig != want {
		t.Errorf("BaseConfig = %q, want %q", p.BaseConfig, want)
	}
	if want := filepath.Join(root, "node_modules"); p.NodeModules != want {
		t.Errorf("NodeModules = %q, want %q", p.NodeModules, want)
	}
	// The budget is the only ceiling pigtail knows here; the repo's own
	// config may still lower it when the wrapper evaluates it.
	if p.Ceiling != 9 || p.CeilingFrom != "min(repo,pigtail)" {
		t.Errorf("ceiling = %d from %q, want 9 from min(repo,pigtail)", p.Ceiling, p.CeilingFrom)
	}
}

// Each of these used to be a bare stderr line and a silent unarbitrated run.
// They must now be named, so --explain can print them and an agent can act.
func TestResolveNamesEverySkip(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		req   func(root string) Request
		want  string
	}{
		{
			name:  "no config anywhere",
			files: map[string]string{"node_modules/vitest/package.json": vitestInstalled},
			want:  SkipNoConfig,
		},
		{
			name:  "config but vitest not installed",
			files: map[string]string{"vitest.config.ts": "export default {}"},
			want:  SkipNoVitest,
		},
		{
			name: "daemon unreachable, no budget",
			files: map[string]string{
				"vitest.config.ts":                 "export default {}",
				"node_modules/vitest/package.json": vitestInstalled,
			},
			req:  func(root string) Request { return Request{Cwd: root, Root: root, Budget: 0} },
			want: SkipNoBudget,
		},
		{
			// A workspace file whose patterns match nothing in this
			// checkout: arbitrating zero projects would run zero tests, so
			// it says so instead.
			name: "workspace layout matching no project",
			files: map[string]string{
				"vitest.workspace.ts":              "export default ['packages/*/vite.config.ts']",
				"node_modules/vitest/package.json": vitestInstalled,
			},
			want: SkipWorkspaceEmpty,
		},
		{
			// Inline project objects cannot be read without executing the
			// file; a named skip beats a wrapper built on a guess.
			name: "workspace file that is not a static list",
			files: map[string]string{
				"vitest.workspace.ts":              "export default [{ test: { name: 'a' } }]",
				"node_modules/vitest/package.json": vitestInstalled,
			},
			want: SkipWorkspaceNotStatic,
		},
		{
			// Jest with no jest installed: the config is found, the binary
			// is not, and saying so beats "no vitest/vite config found".
			name: "jest repo with no jest installed",
			files: map[string]string{
				"jest.config.js": "module.exports = {}",
			},
			want: SkipNoJest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := tree(t, tc.files)
			req := Request{Cwd: root, Root: root, Budget: 9}
			if tc.req != nil {
				req = tc.req(root)
			}
			p := Resolve(req)
			if p.Arbitrated() {
				t.Fatalf("plan should not be arbitrated: %+v", p)
			}
			if p.Skips[0].Code != tc.want {
				t.Errorf("skip code = %q, want %q (detail: %s)", p.Skips[0].Code, tc.want, p.Skips[0].Detail)
			}
			if p.Skips[0].Detail == "" {
				t.Error("a skip with no detail tells the user nothing")
			}
			// Whatever the reason, the suite still runs.
			if p.Grain != GrainNone {
				t.Errorf("grain = %s, want none when unarbitrated", p.Grain)
			}
		})
	}
}

// A jest repo must be reported as a jest repo. Until this PR that meant a
// named skip; now it means an arbitrated jest plan, which is the same promise
// kept further: the runner the repo actually uses is the one pigtail handles.
func TestResolveNamesJestRunner(t *testing.T) {
	root := tree(t, map[string]string{
		"jest.config.js":                 "module.exports = {}",
		"node_modules/jest/package.json": `{"name":"jest"}`,
	})
	p := Resolve(Request{Cwd: root, Root: root, Budget: 9})
	if p.Runner != RunnerJest {
		t.Errorf("runner = %s, want jest", p.Runner)
	}
	if !p.Arbitrated() {
		t.Errorf("a jest repo with jest installed must be arbitrated, got %+v", p.Skips)
	}
}

// The .cts/.cjs spellings were missing from the candidate list, so a repo
// using them looked configless and ran unarbitrated without anyone knowing.
func TestResolveFindsCtsAndCjsConfigs(t *testing.T) {
	for _, name := range []string{"vitest.config.cts", "vitest.config.cjs", "vite.config.cts", "vite.config.cjs"} {
		t.Run(name, func(t *testing.T) {
			root := tree(t, map[string]string{
				name:                               "module.exports = {}",
				"node_modules/vitest/package.json": vitestInstalled,
			})
			p := Resolve(Request{Cwd: root, Root: root, Budget: 9})
			if want := filepath.Join(root, name); p.BaseConfig != want {
				t.Errorf("BaseConfig = %q, want %q", p.BaseConfig, want)
			}
		})
	}
}

// A dedicated vitest config wins over a vite config that merely carries a
// `test` key, and a project's own config wins over one further up.
func TestResolvePrefersNearestAndMostSpecificConfig(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.config.ts":                 "export default {}",
		"apps/web/vite.config.ts":          "export default {}",
		"apps/web/vitest.config.mts":       "export default {}",
		"node_modules/vitest/package.json": vitestInstalled,
	})
	cwd := filepath.Join(root, "apps", "web")
	p := Resolve(Request{Cwd: cwd, Root: root, Budget: 9})
	if want := filepath.Join(cwd, "vitest.config.mts"); p.BaseConfig != want {
		t.Errorf("BaseConfig = %q, want the nearest, most specific config %q", p.BaseConfig, want)
	}
}

// The wrapper is written under the node_modules that actually has vitest: the
// wrapper's first import is 'vitest/config', resolved from its own directory.
func TestResolvePicksTheNodeModulesThatHasVitest(t *testing.T) {
	root := tree(t, map[string]string{
		"node_modules/react/package.json":             `{"name":"react"}`,
		"packages/x/vitest.config.ts":                 "export default {}",
		"packages/x/node_modules/vitest/package.json": vitestInstalled,
	})
	cwd := filepath.Join(root, "packages", "x")
	p := Resolve(Request{Cwd: cwd, Root: root, Budget: 9})
	if want := filepath.Join(cwd, "node_modules"); p.NodeModules != want {
		t.Errorf("NodeModules = %q, want %q", p.NodeModules, want)
	}
}

// Neither search may escape the worktree: a config or an install above it
// belongs to another checkout.
func TestResolveStopsAtTheWorktreeRoot(t *testing.T) {
	outer := tree(t, map[string]string{
		"vitest.config.ts":                 "export default {}",
		"node_modules/vitest/package.json": vitestInstalled,
	})
	root := filepath.Join(outer, "worktree")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	p := Resolve(Request{Cwd: root, Root: root, Budget: 9})
	if p.Arbitrated() {
		t.Fatalf("picked up something above the worktree root: %+v", p)
	}
	if p.Skips[0].Code != SkipNoConfig {
		t.Errorf("skip = %q, want %q", p.Skips[0].Code, SkipNoConfig)
	}
}

// The layout a large monorepo has: a workspace file at the root, projects one and two
// levels down, and vitest hoisted to the root node_modules. Every one of its
// projects must be found and wrapped, from the root and from inside a project
// alike — the config search walks up, so both land on the same workspace file.
func TestWorkspaceLayoutIsArbitrated(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.workspace.ts":              "export default ['**/*/vite.config.ts', '**/*/vitest.config.ts']",
		"apps/web/vite.config.ts":          "export default {}",
		"apps/web/vitest.config.ts":        "export default {}",
		"libs/ui/vitest.config.ts":         "export default {}",
		"node_modules/vitest/package.json": vitestInstalled,
	})

	for _, cwd := range []string{root, filepath.Join(root, "apps", "web")} {
		p := Resolve(Request{Cwd: cwd, Root: root, Budget: 9})
		if !p.Arbitrated() {
			t.Fatalf("from %s: not arbitrated (%v)", cwd, p.Skips)
		}
		if p.Layout != LayoutWorkspace {
			t.Errorf("from %s: layout = %q, want %q", cwd, p.Layout, LayoutWorkspace)
		}
		if len(p.Projects) != 3 {
			t.Errorf("from %s: %d projects, want 3", cwd, len(p.Projects))
		}
		if p.Grain != GrainFile {
			t.Errorf("from %s: grain = %q, want %q", cwd, p.Grain, GrainFile)
		}
	}
}

// A workspace file pigtail cannot read must still send the user somewhere
// useful: the memory lease is available to any command, arbitrated or not.
func TestWorkspaceSkipRemedyPointsAtAdmit(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.workspace.ts":              "export default [{ test: { name: 'a' } }]",
		"node_modules/vitest/package.json": vitestInstalled,
	})
	p := Resolve(Request{Cwd: root, Root: root, Budget: 9})
	if !strings.Contains(p.Skips[0].Remedy, "pigtail admit") {
		t.Errorf("remedy should point at the one thing that does help: %q", p.Skips[0].Remedy)
	}
}

// A jest install, same shape as vitestInstalled: the directory is what makes
// `jest` resolvable from a wrapper written under node_modules.
const jestInstalled = `{"name":"jest","version":"29.7.0"}`

func TestResolveArbitratesJest(t *testing.T) {
	root := tree(t, map[string]string{
		"jest.config.js":                 "module.exports = {}",
		"node_modules/jest/package.json": jestInstalled,
	})
	p := Resolve(Request{Cwd: root, Root: root, Budget: 9})

	if !p.Arbitrated() {
		t.Fatalf("plan is not arbitrated: %+v", p.Skips)
	}
	if p.Runner != RunnerJest || p.Layout != LayoutSingle || p.Grain != GrainFile {
		t.Errorf("runner/layout/grain = %s/%s/%s, want jest/single/file", p.Runner, p.Layout, p.Grain)
	}
	if want := filepath.Join(root, "jest.config.js"); p.BaseConfig != want {
		t.Errorf("BaseConfig = %q, want %q", p.BaseConfig, want)
	}
}

// With both runners installed, `pigtail test` keeps meaning vitest — that is
// what the command has always run, and PR A only just gave this very repo
// (a large monorepo) a working vitest ceiling. Taking arbitration away to
// signal ambiguity would trade a fixed bug for a regression. The second runner
// is reported as a note instead, and --runner=jest reaches it.
func TestResolveKeepsVitestWhenBothRunnersArePresent(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.config.ts":                 "export default {}",
		"jest.config.js":                   "module.exports = {}",
		"node_modules/vitest/package.json": vitestInstalled,
		"node_modules/jest/package.json":   jestInstalled,
	})
	p := Resolve(Request{Cwd: root, Root: root, Budget: 9})

	if !p.Arbitrated() || p.Runner != RunnerVitest {
		t.Fatalf("runner = %s, arbitrated = %v; want an arbitrated vitest plan", p.Runner, p.Arbitrated())
	}
	if len(p.Notes) == 0 || !strings.Contains(strings.Join(p.Notes, " "), "--runner=jest") {
		t.Errorf("notes = %v, want one pointing at --runner=jest", p.Notes)
	}
}

func TestResolveHonoursAnExplicitRunner(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.config.ts":                 "export default {}",
		"jest.config.js":                   "module.exports = {}",
		"node_modules/vitest/package.json": vitestInstalled,
		"node_modules/jest/package.json":   jestInstalled,
	})
	p := Resolve(Request{Cwd: root, Root: root, Budget: 9, Runner: RunnerJest})

	if !p.Arbitrated() || p.Runner != RunnerJest {
		t.Fatalf("runner = %s, arbitrated = %v; want an arbitrated jest plan", p.Runner, p.Arbitrated())
	}
}

// Every one of these still runs the suite — unarbitrated, with a named reason.
func TestResolveNamesWhyJestIsNotArbitrated(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name: "no jest installed",
			files: map[string]string{
				"jest.config.js": "module.exports = {}",
			},
			want: SkipNoJest,
		},
		{
			// The wrapper is CommonJS and requires the repo's config. An ESM
			// or TypeScript config cannot be required from it, and guessing
			// would break the run rather than leave it unarbitrated.
			name: "ESM config",
			files: map[string]string{
				"jest.config.mjs":                "export default {}",
				"node_modules/jest/package.json": jestInstalled,
			},
			want: SkipJestConfigNotRequirable,
		},
		{
			name: "TypeScript config",
			files: map[string]string{
				"jest.config.ts":                 "export default {}",
				"node_modules/jest/package.json": jestInstalled,
			},
			want: SkipJestConfigNotRequirable,
		},
		{
			// type: module makes a bare .js file ESM too.
			name: "js config in an ESM package",
			files: map[string]string{
				"package.json":                   `{"type":"module"}`,
				"jest.config.js":                 "export default {}",
				"node_modules/jest/package.json": jestInstalled,
			},
			want: SkipJestConfigNotRequirable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := tree(t, tc.files)
			p := Resolve(Request{Cwd: root, Root: root, Budget: 9, Runner: RunnerJest})
			if p.Arbitrated() {
				t.Fatalf("plan is arbitrated, want skip %s", tc.want)
			}
			if p.Skips[0].Code != tc.want {
				t.Errorf("skip = %q, want %q", p.Skips[0].Code, tc.want)
			}
			if p.Skips[0].Detail == "" || p.Skips[0].Remedy == "" {
				t.Errorf("skip %q must carry a detail and a remedy, got %+v", tc.want, p.Skips[0])
			}
		})
	}
}
