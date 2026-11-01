package testrun

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests evaluate the generated wrapper with the real Node, because that
// is the only place the bug this file guards against ever showed: every Go
// assertion on the rendered *text* passed while the *evaluated* config handed
// vitest nine workers where the repo asked for four.
//
// The vitest/config stub reproduces mergeConfig's one relevant behaviour —
// scalars from the second argument overwrite those from the first — which is
// precisely how `maxWorkers: <budget>` used to erase a repo's own pin. A stub
// rather than a real install keeps the test reproducible in CI, where there is
// no npm install to lean on.
const mergeConfigStub = `export function mergeConfig(a, b) {
  const out = { ...a };
  for (const [k, v] of Object.entries(b)) {
    out[k] = v && typeof v === 'object' && !Array.isArray(v) && a[k] && typeof a[k] === 'object'
      ? mergeConfig(a[k], v)
      : v;
  }
  return out;
}
`

// evalWrapper renders the wrapper against a base config and returns the
// resolved `test` section as vitest would receive it.
func evalWrapper(t *testing.T, baseBody string, budget int) map[string]any {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping wrapper evaluation")
	}

	root := tree(t, map[string]string{
		// A .mjs base so plain Node can import it: under vitest the base is
		// bundled by esbuild first, which is orthogonal to what is tested here.
		"vite.config.mjs":                       baseBody,
		"node_modules/vitest/package.json":      `{"name":"vitest","version":"3.2.4","exports":{"./config":"./config/index.mjs"}}`,
		"node_modules/vitest/config/index.mjs":  mergeConfigStub,
		"node_modules/.cache/pigtail/slots.mjs": "export default {};\n",
	})
	dir := filepath.Join(root, "node_modules", ".cache", "pigtail")

	cfg, err := RenderVitestConfig(dir, filepath.Join(root, "vite.config.mjs"), filepath.Join(dir, "slots.mjs"), budget)
	if err != nil {
		t.Fatalf("RenderVitestConfig: %v", err)
	}

	script := `const mod = await import(process.argv[1]);
const resolved = await (typeof mod.default === 'function' ? mod.default({ mode: 'test' }) : mod.default);
process.stdout.write(JSON.stringify(resolved.test ?? {}));`
	cmd := exec.Command(node, "--input-type=module", "-e", script, cfg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating the generated config failed: %v\n%s", err, out)
	}
	var test map[string]any
	if err := json.Unmarshal(out, &test); err != nil {
		t.Fatalf("config did not resolve to JSON: %v\n%s", err, out)
	}
	return test
}

// The regression this whole PR exists for. A large monorepo pins
// maxWorkers: 4 for its browser-mode Storybook suite — four Chromium
// processes, measured and deliberate. pigtail used to hand vitest nine.
func TestWrapperNeverRaisesTheRepoCeiling(t *testing.T) {
	test := evalWrapper(t, "export default { test: { maxWorkers: 4 } };\n", 9)

	if got := test["maxWorkers"]; got != float64(4) {
		t.Errorf("maxWorkers = %v, want 4 — pigtail must never raise a ceiling the repo set", got)
	}
	// Every pool falls back to maxWorkers when it sets nothing itself, so
	// capping the pools at the budget would reintroduce the same bug through
	// the back door.
	pools, _ := test["poolOptions"].(map[string]any)
	for _, pool := range []struct{ name, key string }{
		{"forks", "maxForks"}, {"threads", "maxThreads"},
		{"vmForks", "maxForks"}, {"vmThreads", "maxThreads"},
	} {
		p, _ := pools[pool.name].(map[string]any)
		if got := p[pool.key]; got != float64(4) {
			t.Errorf("poolOptions.%s.%s = %v, want 4", pool.name, pool.key, got)
		}
	}
}

func TestWrapperLowersACeilingAboveTheBudget(t *testing.T) {
	test := evalWrapper(t, "export default { test: { maxWorkers: 20 } };\n", 9)
	if got := test["maxWorkers"]; got != float64(9) {
		t.Errorf("maxWorkers = %v, want the budget 9", got)
	}
}

func TestWrapperAppliesTheBudgetWhenTheRepoSetsNoCeiling(t *testing.T) {
	test := evalWrapper(t, "export default { test: {} };\n", 9)
	if got := test["maxWorkers"]; got != float64(9) {
		t.Errorf("maxWorkers = %v, want the budget 9", got)
	}
}

// A percentage is a ceiling too: "25%" of 18 cores is below a budget of 9 and
// must survive.
func TestWrapperHonoursAPercentageCeiling(t *testing.T) {
	test := evalWrapper(t, `export default { test: { maxWorkers: '25%' } };`+"\n", 9)
	got, ok := test["maxWorkers"].(float64)
	if !ok {
		t.Fatalf("maxWorkers = %v, want a resolved number", test["maxWorkers"])
	}
	if got > 9 || got < 1 {
		t.Errorf("maxWorkers = %v, want a percentage resolved to at most the budget", got)
	}
}

// A pool ceiling of its own is respected, and still capped by the budget.
func TestWrapperRespectsPerPoolCeilings(t *testing.T) {
	test := evalWrapper(t,
		"export default { test: { maxWorkers: 8, poolOptions: { forks: { maxForks: 2 }, threads: { maxThreads: 30 } } } };\n", 9)
	pools, _ := test["poolOptions"].(map[string]any)
	forks, _ := pools["forks"].(map[string]any)
	threads, _ := pools["threads"].(map[string]any)
	if got := forks["maxForks"]; got != float64(2) {
		t.Errorf("poolOptions.forks.maxForks = %v, want the repo's own 2", got)
	}
	if got := threads["maxThreads"]; got != float64(8) {
		t.Errorf("poolOptions.threads.maxThreads = %v, want 8 (repo maxWorkers, below the budget)", got)
	}
}

// min > max makes tinypool throw before a single test runs, so a minimum above
// the effective ceiling must come down with it.
func TestWrapperClampsMinimumsToTheEffectiveCeiling(t *testing.T) {
	test := evalWrapper(t, "export default { test: { maxWorkers: 4, minWorkers: 6 } };\n", 9)
	maxW, _ := test["maxWorkers"].(float64)
	minW, _ := test["minWorkers"].(float64)
	if minW > maxW {
		t.Errorf("minWorkers %v > maxWorkers %v — tinypool throws a RangeError on that", minW, maxW)
	}
}

// The repo's own setup files survive, and pigtail's is added exactly once.
func TestWrapperAppendsSetupFileWithoutLosingTheRepos(t *testing.T) {
	test := evalWrapper(t, "export default { test: { setupFiles: ['./repo-setup.ts'] } };\n", 9)
	files, _ := test["setupFiles"].([]any)
	if len(files) != 2 {
		t.Fatalf("setupFiles = %v, want the repo's own plus pigtail's", files)
	}
	if first, _ := files[0].(string); first != "./repo-setup.ts" {
		t.Errorf("setupFiles[0] = %v, want the repo's own kept first", files[0])
	}
	if last, _ := files[1].(string); !strings.HasSuffix(last, "slots.mjs") {
		t.Errorf("setupFiles[1] = %v, want pigtail's slots.mjs", files[1])
	}
}

// Belt and braces: the generated file must be syntactically valid on its own.
func TestGeneratedConfigParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	dir := t.TempDir()
	cfg, err := RenderVitestConfig(dir, filepath.Join(dir, "vite.config.mjs"), filepath.Join(dir, "slots.mjs"), 9)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", cfg).CombinedOutput(); err != nil {
		t.Fatalf("node --check failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Fatal(err)
	}
}

// A browser-mode run takes no CPU slot, so its wrapper must add no setup
// file — and above all must not push an empty path into setupFiles, which
// vitest would try to load, killing the suite arbitration is supposed to
// leave untouched.
func TestWrapperWithoutSetupFileAddsNone(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping wrapper evaluation")
	}
	root := tree(t, map[string]string{
		"vite.config.mjs":                      "export default { test: { setupFiles: ['./repo-setup.ts'], maxWorkers: 4 } };\n",
		"node_modules/vitest/package.json":     `{"name":"vitest","version":"4.1.8","exports":{"./config":"./config/index.mjs"}}`,
		"node_modules/vitest/config/index.mjs": mergeConfigStub,
	})
	dir := filepath.Join(root, "node_modules", ".cache", "pigtail")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := RenderVitestConfig(dir, filepath.Join(root, "vite.config.mjs"), "", 9)
	if err != nil {
		t.Fatal(err)
	}
	script := `const mod = await import(process.argv[1]);
const resolved = await (typeof mod.default === 'function' ? mod.default({ mode: 'test' }) : mod.default);
process.stdout.write(JSON.stringify(resolved.test ?? {}));`
	out, err := exec.Command(node, "--input-type=module", "-e", script, cfg).CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating the generated config failed: %v\n%s", err, out)
	}
	var test map[string]any
	if err := json.Unmarshal(out, &test); err != nil {
		t.Fatalf("config did not resolve to JSON: %v\n%s", err, out)
	}
	files, _ := test["setupFiles"].([]any)
	if len(files) != 1 || files[0] != "./repo-setup.ts" {
		t.Fatalf("setupFiles = %v, want the repo's own alone", test["setupFiles"])
	}
	if got := test["maxWorkers"]; got != float64(4) {
		t.Errorf("the ceiling must still apply without a slot setup: maxWorkers = %v", got)
	}
}
