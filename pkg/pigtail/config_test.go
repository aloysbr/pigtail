package pigtail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteThenLoad(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDefaultProjectConfig(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Errorf("version: %d", cfg.Version)
	}
	if cfg.Name != "demo" {
		t.Errorf("name: %s", cfg.Name)
	}
	if cfg.Backend != "git" {
		t.Errorf("backend: %s", cfg.Backend)
	}
	if cfg.Ports["dev"].Range != (PortRange{3000, 3099}) {
		t.Errorf("dev range: %v", cfg.Ports["dev"].Range)
	}
	if cfg.Ports["dev"].Env != "PORT" {
		t.Errorf("dev env: %q, want PORT", cfg.Ports["dev"].Env)
	}
	if cfg.Ports["storybook"].Range != (PortRange{6006, 6099}) {
		t.Errorf("storybook range: %v", cfg.Ports["storybook"].Range)
	}
	if cfg.Ports["storybook"].Env != "STORYBOOK_PORT" {
		t.Errorf("storybook env: %q, want STORYBOOK_PORT", cfg.Ports["storybook"].Env)
	}
	if cfg.MainWorktree != "main" {
		t.Errorf("main_worktree: %q, want \"main\"", cfg.MainWorktree)
	}
	if cfg.Defaults == nil || cfg.Defaults.Agent != "claude" {
		t.Errorf("defaults.agent: %+v", cfg.Defaults)
	}
	if cfg.Defaults.BranchPrefix != "feat/" {
		t.Errorf("defaults.branch_prefix: %q", cfg.Defaults.BranchPrefix)
	}
}

// TestShortFormBackwardsCompat proves a pigtail.yaml authored under the
// pre-PortSpec short syntax keeps working byte-for-byte and yields the
// same env var names it always did.
func TestShortFormBackwardsCompat(t *testing.T) {
	dir := t.TempDir()
	body := `version: 1
name: legacy-app
backend: git
ports:
  dev: [3000, 3099]
  storybook: [6006, 6099]
`
	if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ports["dev"].Range != (PortRange{3000, 3099}) || cfg.Ports["dev"].Env != "PORT" {
		t.Errorf("legacy dev port lost meaning: %+v", cfg.Ports["dev"])
	}
	if cfg.Ports["storybook"].Env != "STORYBOOK_PORT" {
		t.Errorf("legacy storybook env: %q", cfg.Ports["storybook"].Env)
	}
	if cfg.MainWorktree != "main" {
		t.Errorf("legacy file should default main_worktree to \"main\", got %q", cfg.MainWorktree)
	}
	if cfg.Defaults != nil {
		t.Errorf("legacy file should not synthesize defaults: %+v", cfg.Defaults)
	}
}

// TestLongFormPortSpec covers an iOS/Swift-style project with a custom env
// var name on a non-standard port kind.
func TestLongFormPortSpec(t *testing.T) {
	dir := t.TempDir()
	body := `version: 1
name: my-ios-app
backend: git
ports:
  simulator:
    range: [9000, 9099]
    env: PIGTAIL_SIM_PORT
defaults:
  agent: claude
  branch_prefix: research/
`
	if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ports["simulator"].Range != (PortRange{9000, 9099}) {
		t.Errorf("simulator range: %v", cfg.Ports["simulator"].Range)
	}
	if cfg.Ports["simulator"].Env != "PIGTAIL_SIM_PORT" {
		t.Errorf("simulator env: %q", cfg.Ports["simulator"].Env)
	}
	if cfg.Defaults == nil || cfg.Defaults.BranchPrefix != "research/" {
		t.Errorf("defaults: %+v", cfg.Defaults)
	}
}

// TestUnnamedKindDefaultEnv proves a non-Node kind without explicit env
// still gets a sensible default — UPPER + _PORT.
func TestUnnamedKindDefaultEnv(t *testing.T) {
	dir := t.TempDir()
	body := `version: 1
name: x
ports:
  marp: [8080, 8090]
`
	if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ports["marp"].Env != "MARP_PORT" {
		t.Errorf("marp env: %q", cfg.Ports["marp"].Env)
	}
}

func TestWriteRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefaultProjectConfig(dir, "demo"); err == nil {
		t.Fatal("expected error when file already exists")
	}
}

func TestProjectConfigProfileLookup(t *testing.T) {
	cfg := &ProjectConfig{
		Profiles: map[string]Profile{
			"work": {ConfigDir: "/home/u/.claude-work", EnvOverrides: map[string]string{"X": "1"}},
		},
	}
	// found: ID is backfilled from the map key when empty
	p, ok := cfg.Profile("work")
	if !ok {
		t.Fatal("expected profile 'work' to be found")
	}
	if p.ID != "work" {
		t.Fatalf("expected ID backfilled from key, got %q", p.ID)
	}
	if p.ConfigDir != "/home/u/.claude-work" {
		t.Fatalf("wrong config dir: %q", p.ConfigDir)
	}
	// not found
	if _, ok := cfg.Profile("missing"); ok {
		t.Fatal("expected 'missing' to be absent")
	}
	// nil-safe: a nil/empty Profiles map must not panic
	empty := &ProjectConfig{}
	if _, ok := empty.Profile("work"); ok {
		t.Fatal("expected absent on empty config")
	}
}

func TestLoadValidates(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"missing version": `name: x`,
		"wrong version":   "version: 99\nname: x",
		"missing name":    "version: 1",
		"bad range":       "version: 1\nname: x\nports:\n  dev: [3000]",
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProjectConfig(dir); err == nil {
				t.Fatalf("%s: expected error", label)
			}
		})
	}
}

// TestWatchdogDefaults proves a config with no watchdog block loads the
// full default watchdog config (on by default, safe thresholds).
func TestWatchdogDefaults(t *testing.T) {
	dir := t.TempDir()
	body := "version: 1\nname: x\n"
	if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Watchdog
	if w == nil {
		t.Fatal("watchdog: nil after load, want defaults")
	}
	if !w.IsEnabled() {
		t.Error("watchdog: disabled by default, want enabled")
	}
	if w.CPUThreshold != 400 {
		t.Errorf("cpu_threshold: %d, want 400", w.CPUThreshold)
	}
	if w.SampleInterval != 6 || w.ConfirmWindows != 2 || w.ConfirmGap != 10 || w.SweepInterval != 30 {
		t.Errorf("intervals: %+v", w)
	}
}

// TestWatchdogPartialOverride proves user-set fields win and unset fields
// still default.
func TestWatchdogPartialOverride(t *testing.T) {
	dir := t.TempDir()
	body := `version: 1
name: x
watchdog:
  cpu_threshold: 250
  sweep_interval: 60
`
	if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Watchdog
	if w.CPUThreshold != 250 {
		t.Errorf("cpu_threshold: %d, want 250", w.CPUThreshold)
	}
	if w.SweepInterval != 60 {
		t.Errorf("sweep_interval: %d, want 60", w.SweepInterval)
	}
	if w.SampleInterval != 6 || w.ConfirmWindows != 2 || w.ConfirmGap != 10 {
		t.Errorf("unset fields should default: %+v", w)
	}
	if !w.IsEnabled() {
		t.Error("enabled should default to true")
	}
}

// TestWatchdogDisabled proves `enabled: false` survives defaulting.
func TestWatchdogDisabled(t *testing.T) {
	dir := t.TempDir()
	body := "version: 1\nname: x\nwatchdog:\n  enabled: false\n"
	if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Watchdog.IsEnabled() {
		t.Error("watchdog: enabled, want disabled")
	}
}

// TestWatchdogValidation rejects nonsensical values.
func TestWatchdogValidation(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"negative threshold": "version: 1\nname: x\nwatchdog:\n  cpu_threshold: -1\n",
		"negative interval":  "version: 1\nname: x\nwatchdog:\n  sample_interval: -5\n",
		"zero windows":       "version: 1\nname: x\nwatchdog:\n  confirm_windows: -1\n",
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, ProjectConfigFilename), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProjectConfig(dir); err == nil {
				t.Fatalf("%s: expected error", label)
			}
		})
	}
}

// TestWatchdogNilIsEnabled covers the nil-receiver convenience used by the
// daemon when a project config could not be loaded at all.
func TestWatchdogNilIsEnabled(t *testing.T) {
	var w *WatchdogConfig
	if !w.IsEnabled() {
		t.Error("nil watchdog config should report enabled (defaults)")
	}
}
