package testrun

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The jest wrapper is evaluated with the real Node for the same reason the
// vitest one is: asserting on the rendered *text* is what let PR A's bug live
// for weeks. What matters is the object jest ends up holding.
func evalJestWrapper(t *testing.T, baseBody string, budget int) map[string]any {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping wrapper evaluation")
	}

	root := tree(t, map[string]string{
		"jest.config.js":                             baseBody,
		"node_modules/jest/package.json":             jestInstalled,
		"node_modules/.cache/pigtail/jest-slots.cjs": "module.exports = {};\n",
	})
	dir := filepath.Join(root, "node_modules", ".cache", "pigtail")
	cfgPath, err := RenderJestConfig(dir, filepath.Join(root, "jest.config.js"), filepath.Join(dir, "jest-slots.cjs"), budget)
	if err != nil {
		t.Fatalf("RenderJestConfig: %v", err)
	}

	// Jest itself calls the exported function and awaits it; so does this.
	runner := filepath.Join(root, "run.cjs")
	script := "const f = require(" + jsString(cfgPath) + ");\n" +
		"Promise.resolve(typeof f === 'function' ? f() : f).then((c) => {\n" +
		"  process.stdout.write(JSON.stringify(c));\n" +
		"});\n"
	if err := os.WriteFile(runner, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(node, runner).CombinedOutput() //nolint:gosec // node from PATH, script from t.TempDir
	if err != nil {
		t.Fatalf("evaluating the wrapper failed: %v\n%s", err, out)
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("wrapper output is not JSON: %v\n%s", err, out)
	}
	return cfg
}

// The invariant, on the runner that made it urgent: a large monorepo's
// jest peaks at ~16 GiB, so a ceiling it pinned must survive pigtail.
func TestJestWrapperNeverRaisesTheRepoCeiling(t *testing.T) {
	cfg := evalJestWrapper(t, "module.exports = { maxWorkers: 4 };", 9)
	if got := cfg["maxWorkers"]; got != float64(4) {
		t.Errorf("maxWorkers = %v, want 4 — pigtail must never raise a ceiling the repo set", got)
	}
}

func TestJestWrapperAppliesTheBudgetWhenTheRepoPinsNothing(t *testing.T) {
	cfg := evalJestWrapper(t, "module.exports = { testEnvironment: 'node' };", 9)
	if got := cfg["maxWorkers"]; got != float64(9) {
		t.Errorf("maxWorkers = %v, want the budget 9", got)
	}
	if got := cfg["testEnvironment"]; got != "node" {
		t.Errorf("testEnvironment = %v, want the repo's own value kept", got)
	}
}

// A percentage is what jest accepts and what a repo tuning itself tends to
// write; it must be compared, not passed through.
func TestJestWrapperUnderstandsAPercentageCeiling(t *testing.T) {
	cfg := evalJestWrapper(t, "module.exports = { maxWorkers: '10%' };", 9)
	got, ok := cfg["maxWorkers"].(float64)
	if !ok {
		t.Fatalf("maxWorkers = %v, want a resolved number", cfg["maxWorkers"])
	}
	if got > 9 || got < 1 {
		t.Errorf("maxWorkers = %v, want 10%% of this machine's CPUs, capped at 9", got)
	}
}

// next/jest's createJestConfig returns a function returning a promise. A
// wrapper that did not await it would hand jest a Promise as its config.
func TestJestWrapperAwaitsAnAsyncConfig(t *testing.T) {
	cfg := evalJestWrapper(t, "module.exports = async () => ({ maxWorkers: 3, displayName: 'async' });", 9)
	if got := cfg["maxWorkers"]; got != float64(3) {
		t.Errorf("maxWorkers = %v, want 3 from the awaited config", got)
	}
	if got := cfg["displayName"]; got != "async" {
		t.Errorf("displayName = %v, want the awaited config's own value", got)
	}
}

// Dropping the repo's setup files would take its matchers and polyfills with
// them — the arbiter breaking the suite it is supposed to schedule.
func TestJestWrapperConcatenatesSetupFiles(t *testing.T) {
	cfg := evalJestWrapper(t, "module.exports = { setupFilesAfterEnv: ['<rootDir>/jest.setup.js'] };", 9)
	files, ok := cfg["setupFilesAfterEnv"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("setupFilesAfterEnv = %v, want the repo's own plus pigtail's", cfg["setupFilesAfterEnv"])
	}
	if files[0] != "<rootDir>/jest.setup.js" {
		t.Errorf("first setup file = %v, want the repo's own kept first", files[0])
	}
	if !strings.HasSuffix(files[1].(string), "jest-slots.cjs") {
		t.Errorf("last setup file = %v, want pigtail's appended last", files[1])
	}
}

// --config re-roots the run at the config file's directory. Without this, every
// <rootDir>/src/… in the repo's config would point inside node_modules/.cache.
func TestJestWrapperPinsRootDirBackToTheRepo(t *testing.T) {
	cfg := evalJestWrapper(t, "module.exports = {};", 9)
	rootDir, _ := cfg["rootDir"].(string)
	if rootDir == "" || strings.Contains(rootDir, filepath.Join("node_modules", ".cache")) {
		t.Fatalf("rootDir = %q, want the directory holding the repo's own config", rootDir)
	}
	if filepath.Base(rootDir) == "pigtail" {
		t.Errorf("rootDir = %q, want the repo root, not the cache", rootDir)
	}
}

// A relative rootDir is resolved by jest against the config file's directory,
// so the wrapper has to resolve it against the *original* one.
func TestJestWrapperResolvesARelativeRootDir(t *testing.T) {
	cfg := evalJestWrapper(t, "module.exports = { rootDir: 'src' };", 9)
	rootDir, _ := cfg["rootDir"].(string)
	if filepath.Base(rootDir) != "src" || strings.Contains(rootDir, ".cache") {
		t.Errorf("rootDir = %q, want <repo>/src", rootDir)
	}
}
