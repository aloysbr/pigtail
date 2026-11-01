package testrun

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The write-back: pigtail decides a run's class in Go, before node starts,
// but whether a vitest run is browser-mode is only knowable once the repo's
// config has been *evaluated* — and the argv of a browser run is identical to
// a node one. So the wrapper records what it saw, and the next run reads it.
//
// The first run of a config pigtail has never evaluated is therefore classed
// generically and self-corrects afterwards, exactly like the learned costs.

func TestWrapperWritesBackWhatItResolved(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping wrapper evaluation")
	}
	root := tree(t, map[string]string{
		"vite.config.mjs": "export default { test: { maxWorkers: 4, isolate: false, " +
			"browser: { enabled: true, instances: [{ browser: 'chromium' }] } } };\n",
		"node_modules/vitest/package.json":      `{"name":"vitest","version":"4.1.8","exports":{"./config":"./config/index.mjs"}}`,
		"node_modules/vitest/config/index.mjs":  mergeConfigStub,
		"node_modules/.cache/pigtail/slots.mjs": "export default {};\n",
	})
	dir := filepath.Join(root, "node_modules", ".cache", "pigtail")
	base := filepath.Join(root, "vite.config.mjs")

	cfg, err := RenderVitestConfig(dir, base, filepath.Join(dir, "slots.mjs"), 9)
	if err != nil {
		t.Fatal(err)
	}
	script := `const mod = await import(process.argv[1]);
await (typeof mod.default === 'function' ? mod.default({ mode: 'test' }) : mod.default);`
	if out, err := exec.Command(node, "--input-type=module", "-e", script, cfg).CombinedOutput(); err != nil {
		t.Fatalf("evaluating the generated config failed: %v\n%s", err, out)
	}

	facts, ok := ReadResolved(dir, base)
	if !ok {
		t.Fatal("the wrapper wrote no usable resolved.json")
	}
	if !facts.Browser {
		t.Error("browser mode was not recorded")
	}
	if facts.MaxWorkers != 4 {
		t.Errorf("max_workers = %d, want the repo's own 4", facts.MaxWorkers)
	}
}

func TestReadResolvedIgnoresStaleOrForeignFacts(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "vite.config.ts")
	if err := os.WriteFile(base, []byte("export default {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	write := func(f resolvedFacts) {
		t.Helper()
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(resolvedPath(dir, base), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("no file at all", func(t *testing.T) {
		if _, ok := ReadResolved(dir, base); ok {
			t.Fatal("a missing file must not be usable")
		}
	})

	fresh := resolvedFacts{Config: base, MTimeMS: st.ModTime().UnixMilli(), Browser: true, MaxWorkers: 4}
	t.Run("fresh facts are used", func(t *testing.T) {
		write(fresh)
		if f, ok := ReadResolved(dir, base); !ok || !f.Browser {
			t.Fatalf("fresh facts must be used, got %+v ok=%v", f, ok)
		}
	})

	t.Run("a config edited since is not trusted", func(t *testing.T) {
		stale := fresh
		stale.MTimeMS = st.ModTime().Add(-time.Hour).UnixMilli()
		write(stale)
		if _, ok := ReadResolved(dir, base); ok {
			t.Fatal("facts recorded against an older config must be discarded")
		}
	})

	t.Run("facts about another config are not trusted", func(t *testing.T) {
		foreign := fresh
		foreign.Config = filepath.Join(dir, "somewhere-else.ts")
		write(foreign)
		if _, ok := ReadResolved(dir, base); ok {
			t.Fatal("facts naming another config must be discarded")
		}
	})

	t.Run("unreadable JSON is not an error, just unusable", func(t *testing.T) {
		if err := os.WriteFile(resolvedPath(dir, base), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadResolved(dir, base); ok {
			t.Fatal("a corrupt file must not be usable")
		}
	})
}

func TestResolveClassesABrowserRunFromWhatTheLastRunRecorded(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.config.ts":                 "export default {}\n",
		"node_modules/vitest/package.json": `{"name":"vitest","version":"4.1.8"}`,
	})
	base := filepath.Join(root, "vitest.config.ts")
	dir := filepath.Join(root, "node_modules", ".cache", "pigtail")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Before any facts: a plain vitest run, arbitrated per file as ever.
	plan := Resolve(Request{Cwd: root, Root: root, Budget: 9})
	if !plan.Arbitrated() || plan.Grain != GrainFile || plan.Class != "" {
		t.Fatalf("a config never evaluated must plan as a plain vitest run, got %+v", plan)
	}

	st, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := json.Marshal(resolvedFacts{
		Config: base, MTimeMS: st.ModTime().UnixMilli(), Browser: true, MaxWorkers: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolvedPath(dir, base), facts, 0o600); err != nil {
		t.Fatal(err)
	}

	plan = Resolve(Request{Cwd: root, Root: root, Budget: 9})
	if !plan.Arbitrated() {
		t.Fatal("browser mode must not cost the run its arbitration")
	}
	if plan.Class != "vitest-browser" || plan.Workers != 4 || plan.Grain != GrainRun {
		t.Fatalf("class=%q workers=%d grain=%q, want vitest-browser/4/run", plan.Class, plan.Workers, plan.Grain)
	}
	if len(plan.Notes) == 0 {
		t.Error("the change of class must be said out loud, not inferred from a cost")
	}
}
