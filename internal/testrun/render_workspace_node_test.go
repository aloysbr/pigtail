package testrun

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// These tests evaluate the generated workspace wrappers with the real Node,
// for the same reason the single-layout ones do: every property that matters
// here (the project root, the repo's own project name, the setup files the
// repo already had) is decided when the config is *evaluated*, and an
// assertion on the generated text would pass while vitest received something
// else.
//
// What they encode was measured against real vitest 3.2.7 and 4.1.11 in a
// throwaway fixture, because the docs do not say it:
//
//   - a project's root comes from test.root, NOT from the Vite-level `root`.
//     Setting only `root` leaves the project rooted at the wrapper's own
//     directory, and the run reports "No test files found" — green-looking and
//     completely empty.
//   - vitest 4 rejects a project config whose filename does not match
//     /^vite(?:st)?(?:\.[\w-]+)?\.config\./ (see render_workspace_test.go).
//   - extends/inline objects lose the repo's own project name, which would
//     silently break a --project=<name> filter, so the wrapper only fills a
//     name in when the repo set none.

// evalProjectWrapper renders one project wrapper against a base config and
// returns the `test` section as vitest would receive it.
func evalProjectWrapper(t *testing.T, baseBody string, budget int) map[string]any {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping wrapper evaluation")
	}

	root := tree(t, map[string]string{
		"libs/ui/vite.config.mjs":               baseBody,
		"libs/ui/package.json":                  `{"name":"@app/ui"}`,
		"node_modules/vitest/package.json":      `{"name":"vitest","version":"3.2.7","exports":{"./config":"./config/index.mjs"}}`,
		"node_modules/vitest/config/index.mjs":  mergeConfigStub,
		"node_modules/.cache/pigtail/slots.mjs": "export default {};\n",
	})
	dir := filepath.Join(root, "node_modules", ".cache", "pigtail")

	projects := describeProjects(root, []string{filepath.Join(root, "libs/ui/vite.config.mjs")})
	written, err := renderProjectWrappers(dir, projects, filepath.Join(dir, "slots.mjs"), budget)
	if err != nil {
		t.Fatalf("renderProjectWrappers: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("got %d wrappers, want 1", len(written))
	}

	script := `const mod = await import(process.argv[1]);
const resolved = await (typeof mod.default === 'function' ? mod.default({ mode: 'test' }) : mod.default);
process.stdout.write(JSON.stringify(resolved.test ?? {}));`
	out, err := exec.Command(node, "--input-type=module", "-e", script, written[0]).CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating the project wrapper failed: %v\n%s", err, out)
	}
	var test map[string]any
	if jerr := json.Unmarshal(out, &test); jerr != nil {
		t.Fatalf("wrapper did not resolve to JSON: %v\n%s", jerr, out)
	}
	return test
}

// The one that makes the difference between arbitrating a large monorepo and running
// nothing at all: without test.root, every project is rooted at the pigtail
// cache directory and no test file is ever found.
func TestProjectWrapperSetsTestRoot(t *testing.T) {
	test := evalProjectWrapper(t, "export default { test: {} };\n", 9)

	root, _ := test["root"].(string)
	if filepath.Base(root) != "ui" {
		t.Errorf("test.root = %q, want the project's own directory — otherwise vitest finds no test files", root)
	}
}

// A repo that already set its root keeps it: pigtail supplies a default, it
// does not overrule a decision.
func TestProjectWrapperKeepsTheRepoRoot(t *testing.T) {
	test := evalProjectWrapper(t, "export default { test: { root: '/tmp/chosen-by-the-repo' } };\n", 9)

	if got := test["root"]; got != "/tmp/chosen-by-the-repo" {
		t.Errorf("test.root = %v, want the repo's own value", got)
	}
}

func TestProjectWrapperKeepsTheRepoProjectName(t *testing.T) {
	test := evalProjectWrapper(t, "export default { test: { name: 'chosen-by-the-repo' } };\n", 9)

	if got := test["name"]; got != "chosen-by-the-repo" {
		t.Errorf("test.name = %v, want the repo's own name — a --project filter depends on it", got)
	}
}

// With no name of its own, the project gets the name vitest itself would give
// it: the package.json name.
func TestProjectWrapperFillsInThePackageName(t *testing.T) {
	test := evalProjectWrapper(t, "export default { test: {} };\n", 9)

	if got := test["name"]; got != "@app/ui" {
		t.Errorf("test.name = %v, want @app/ui from package.json", got)
	}
}

func TestProjectWrapperAppendsSetupFilesInsteadOfReplacing(t *testing.T) {
	test := evalProjectWrapper(t, "export default { test: { setupFiles: ['./repo-setup.ts'] } };\n", 9)

	files, _ := test["setupFiles"].([]any)
	if len(files) != 2 {
		t.Fatalf("setupFiles = %v, want the repo's own plus pigtail's", test["setupFiles"])
	}
	if files[0] != "./repo-setup.ts" {
		t.Errorf("setupFiles[0] = %v, want the repo's own setup first", files[0])
	}
	if last, _ := files[1].(string); filepath.Base(last) != "slots.mjs" {
		t.Errorf("setupFiles[1] = %v, want pigtail's slot client last", files[1])
	}
}

// The invariant of PR A, restated per project: a pin below the budget is kept.
func TestProjectWrapperNeverRaisesAProjectCeiling(t *testing.T) {
	test := evalProjectWrapper(t, "export default { test: { maxWorkers: 2 } };\n", 9)

	if got := test["maxWorkers"]; got != float64(2) {
		t.Errorf("maxWorkers = %v, want the repo's own 2", got)
	}
}

func TestProjectWrapperLowersAProjectCeilingAboveBudget(t *testing.T) {
	test := evalProjectWrapper(t, "export default { test: { maxWorkers: 16 } };\n", 9)

	if got := test["maxWorkers"]; got != float64(9) {
		t.Errorf("maxWorkers = %v, want the budget 9", got)
	}
}

// Vitest 4 refuses projects that differ in maxWorkers while sharing a
// sequence.groupOrder — measured on 4.1.11, and it happens without pigtail
// too. So a project the repo left unpinned must stay unpinned: adding a
// ceiling only some projects carry would manufacture that very divergence.
func TestProjectWrapperAddsNoCeilingWhereTheRepoSetNone(t *testing.T) {
	test := evalProjectWrapper(t, "export default { test: {} };\n", 9)

	if _, ok := test["maxWorkers"]; ok {
		t.Errorf("maxWorkers = %v, want it left unset — the root config carries the ceiling", test["maxWorkers"])
	}
}
