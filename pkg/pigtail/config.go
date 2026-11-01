package pigtail

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProjectConfigFilename is the file pigtail looks for at a project's root.
const ProjectConfigFilename = "pigtail.yaml"

// ProjectConfig is the on-disk representation of a pigtail project. It is
// authored by the user (via `pigtail init` then by hand) and consumed by
// both the CLI and the daemon.
//
// Backwards compatibility: new fields are additive. Removing or renaming
// a field bumps Version.
type ProjectConfig struct {
	Version      int                   `yaml:"version"`
	Name         string                `yaml:"name"`
	Backend      string                `yaml:"backend,omitempty"`       // default: "git"
	MainWorktree string                `yaml:"main_worktree,omitempty"` // default: "main"
	Ports        map[string]PortSpec   `yaml:"ports,omitempty"`
	Servers      map[string]ServerSpec `yaml:"servers,omitempty"`
	Hooks        []HookSpec            `yaml:"hooks,omitempty"`
	Defaults     *ProjectDefaults      `yaml:"defaults,omitempty"`
	Profiles     map[string]Profile    `yaml:"profiles,omitempty"`
	Watchdog     *WatchdogConfig       `yaml:"watchdog,omitempty"`
}

// Profile returns the named profile from the project config. The map key is
// used as the profile ID when the entry itself does not set one. Returns
// (nil, false) when absent.
func (c *ProjectConfig) Profile(id string) (*Profile, bool) {
	if c == nil || c.Profiles == nil {
		return nil, false
	}
	p, ok := c.Profiles[id]
	if !ok {
		return nil, false
	}
	if p.ID == "" {
		p.ID = id
	}
	return &p, true
}

// HookSpec describes a shell command to run after workspace creation.
type HookSpec struct {
	Name    string `yaml:"name"`
	Command string `yaml:"command"`
	IfFile  string `yaml:"if_file,omitempty"` // only run if this file exists in the worktree
}

// ServerSpec describes a named dev server that pigtail can start/stop.
type ServerSpec struct {
	Command  string `yaml:"command"`
	PortKind string `yaml:"port_kind"`
}

// ProjectDefaults carries project-level defaults applied to dispatches when
// the request doesn't override them. They are NOT runtime overrides:
// explicit per-request flags (e.g. `--branch`) still win.
type ProjectDefaults struct {
	// Agent is the default agent_kind for dispatches without `--agent`.
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`
	// BaseRef is the default base for new branches. Prefer a remote-tracking
	// ref (e.g. "origin/main") over a bare local one ("main", "develop"):
	// pigtail auto-fetches the base ref's remote before branching, so a
	// remote-tracking base is fresh at branch time.
	BaseRef string `yaml:"base_ref,omitempty" json:"base_ref,omitempty"`
	// BranchPrefix is the default slug prefix (e.g. "feat/", "research/").
	BranchPrefix string `yaml:"branch_prefix,omitempty" json:"branch_prefix,omitempty"`
}

// WatchdogConfig tunes the daemon's runaway dev-server watchdog: a sweep
// that kills servers stuck pegging the CPU (e.g. a Turbopack recompile
// loop). A server only qualifies when its process subtree sustains
// CPUThreshold %CPU across ConfirmWindows sampling windows of
// SampleInterval seconds, separated by ConfirmGap seconds — so an idle
// server being browsed (~0% CPU) or a short legitimate HMR recompile is
// never killed.
//
// The block is optional: when omitted the watchdog runs with the defaults
// below. Set `enabled: false` to opt this project out.
type WatchdogConfig struct {
	// Enabled toggles the watchdog for this project. nil → enabled.
	Enabled *bool `yaml:"enabled,omitempty"`
	// CPUThreshold is the sustained %CPU (100 = one core) that marks a
	// runaway. Default 400.
	CPUThreshold int `yaml:"cpu_threshold,omitempty"`
	// SampleInterval is the length of one CPU sampling window, in seconds.
	// Default 6.
	SampleInterval int `yaml:"sample_interval,omitempty"`
	// ConfirmWindows is how many hot windows are required before a kill.
	// Default 2.
	ConfirmWindows int `yaml:"confirm_windows,omitempty"`
	// ConfirmGap is the pause between confirmation windows, in seconds.
	// Default 10.
	ConfirmGap int `yaml:"confirm_gap,omitempty"`
	// SweepInterval is the pause between full sweeps of all workspaces,
	// in seconds. Default 30.
	SweepInterval int `yaml:"sweep_interval,omitempty"`
}

// WatchdogDefaults returns a WatchdogConfig with every field set to its
// default value (enabled, 400% threshold, 6s windows ×2, 10s gap, 30s sweep).
func WatchdogDefaults() *WatchdogConfig {
	enabled := true
	return &WatchdogConfig{
		Enabled:        &enabled,
		CPUThreshold:   400,
		SampleInterval: 6,
		ConfirmWindows: 2,
		ConfirmGap:     10,
		SweepInterval:  30,
	}
}

// withDefaults fills zero-valued fields from WatchdogDefaults. Safe on nil:
// it then returns the full default config.
func (w *WatchdogConfig) withDefaults() *WatchdogConfig {
	def := WatchdogDefaults()
	if w == nil {
		return def
	}
	out := *w
	if out.Enabled == nil {
		out.Enabled = def.Enabled
	}
	if out.CPUThreshold == 0 {
		out.CPUThreshold = def.CPUThreshold
	}
	if out.SampleInterval == 0 {
		out.SampleInterval = def.SampleInterval
	}
	if out.ConfirmWindows == 0 {
		out.ConfirmWindows = def.ConfirmWindows
	}
	if out.ConfirmGap == 0 {
		out.ConfirmGap = def.ConfirmGap
	}
	if out.SweepInterval == 0 {
		out.SweepInterval = def.SweepInterval
	}
	return &out
}

// Validate rejects nonsensical watchdog values. Call after withDefaults.
func (w *WatchdogConfig) Validate() error {
	if w.CPUThreshold <= 0 {
		return fmt.Errorf("watchdog: cpu_threshold must be > 0, got %d", w.CPUThreshold)
	}
	if w.SampleInterval <= 0 || w.ConfirmGap <= 0 || w.SweepInterval <= 0 {
		return errors.New("watchdog: intervals must be > 0")
	}
	if w.ConfirmWindows < 1 {
		return fmt.Errorf("watchdog: confirm_windows must be >= 1, got %d", w.ConfirmWindows)
	}
	return nil
}

// IsEnabled reports whether the watchdog should run for this config.
// nil receiver → true (on by default).
func (w *WatchdogConfig) IsEnabled() bool {
	if w == nil || w.Enabled == nil {
		return true
	}
	return *w.Enabled
}

// PortSpec describes one named port kind: the allowed [min, max] range and
// the env var name written into .env.local. Authored in pigtail.yaml as
// either:
//
//	dev: [3000, 3099]                       # short form (legacy)
//	dev: { range: [3000, 3099], env: PORT } # long form
//
// In the short form, the env defaults to the kind-specific known mapping
// ("dev" → PORT, "storybook" → STORYBOOK_PORT) or "<UPPER>_PORT" otherwise.
type PortSpec struct {
	Range PortRange `yaml:"range" json:"range"`
	Env   string    `yaml:"env,omitempty" json:"env,omitempty"`
}

// LoadProjectConfig reads pigtail.yaml from the given directory.
func LoadProjectConfig(dir string) (*ProjectConfig, error) {
	path := dir + "/" + ProjectConfigFilename
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ProjectConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version == 0 {
		return nil, errors.New("pigtail.yaml: missing `version` field")
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("pigtail.yaml: unsupported version %d (expected 1)", cfg.Version)
	}
	if cfg.Name == "" {
		return nil, errors.New("pigtail.yaml: missing `name` field")
	}
	if cfg.Backend == "" {
		cfg.Backend = "git"
	}
	if cfg.MainWorktree == "" {
		cfg.MainWorktree = "main"
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]ServerSpec{
			"dev":       {Command: "npm run dev", PortKind: "dev"},
			"storybook": {Command: "npm run storybook:dev", PortKind: "storybook"},
		}
	}
	if cfg.Hooks == nil {
		cfg.Hooks = []HookSpec{
			{Name: "deps", Command: "npm install --prefer-offline", IfFile: "package.json"},
		}
	}
	for kind, spec := range cfg.Ports {
		if spec.Range[0] == 0 && spec.Range[1] == 0 {
			return nil, fmt.Errorf("pigtail.yaml: port %q has no range", kind)
		}
		if spec.Env == "" {
			spec.Env = DefaultPortEnv(kind)
			cfg.Ports[kind] = spec
		}
	}
	cfg.Watchdog = cfg.Watchdog.withDefaults()
	if err := cfg.Watchdog.Validate(); err != nil {
		return nil, fmt.Errorf("pigtail.yaml: %w", err)
	}
	return &cfg, nil
}

// DefaultPortEnv maps a port kind to the env var name consumers expect when
// the user did not specify one explicitly:
//
//	"dev"       → "PORT"
//	"storybook" → "STORYBOOK_PORT"
//	other       → "<UPPER>_PORT"
//
// This keeps the historical defaults stable while letting non-Node projects
// override the env name to anything they want.
func DefaultPortEnv(kind string) string {
	switch kind {
	case "dev":
		return "PORT"
	case "storybook":
		return "STORYBOOK_PORT"
	default:
		return strings.ToUpper(kind) + "_PORT"
	}
}

// PortEnvName is the env var carrying a port kind in .env.local: the
// project's override when set, else DefaultPortEnv. The one resolver the
// writer (internal/backend/git) and the readers (allocator, resync) share.
func PortEnvName(kind string, overrides map[string]string) string {
	if v, ok := overrides[kind]; ok && v != "" {
		return v
	}
	return DefaultPortEnv(kind)
}

// WriteDefaultProjectConfig writes a minimal pigtail.yaml at the given
// directory. Returns an error if the file already exists.
func WriteDefaultProjectConfig(dir, name string) error {
	path := dir + "/" + ProjectConfigFilename
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	body := fmt.Sprintf(`version: 1
name: %s
backend: git

# Subdirectory name for the main worktree (the branch checked out as "main").
# Worktrees created by pigtail dispatch live as siblings of this directory,
# all inside the project container.
main_worktree: main

# Named port ranges allocated to workspaces. Short form below is enough for
# Node/Next/Storybook projects (env var names default to PORT, STORYBOOK_PORT).
# For non-Node projects, use the long form to set an explicit env var:
#   simulator:
#     range: [9000, 9099]
#     env: PIGTAIL_SIM_PORT
ports:
  dev: [3000, 3099]
  storybook: [6006, 6099]

# Project-wide defaults applied to dispatches that don't override them.
defaults:
  agent: claude
  # base_ref: the ref new branches start from. Prefer a remote-tracking ref
  # (e.g. origin/main) over a bare local "main": pigtail auto-fetches the
  # base ref's remote right before branching, so a remote-tracking base is at
  # the true remote tip at branch time — a bare local "main" only refreshes
  # when you pull it yourself. (Fetch is best-effort: offline still works;
  # pass --no-fetch to skip it.)
  base_ref: origin/main
  branch_prefix: feat/

# Runaway dev-server watchdog (daemon-side). Enabled by default with the
# values below; uncomment to tune, or set enabled: false to opt out.
# A server is killed only when its process subtree sustains cpu_threshold
# %%CPU across confirm_windows sampling windows — an idle server or a short
# HMR recompile is never touched.
# watchdog:
#   enabled: true
#   cpu_threshold: 400    # sustained %%CPU (100 = one core)
#   sample_interval: 6    # seconds per sampling window
#   confirm_windows: 2    # hot windows required before a kill
#   confirm_gap: 10       # seconds between confirmation windows
#   sweep_interval: 30    # seconds between sweeps of all workspaces
`, name)
	return os.WriteFile(path, []byte(body), 0o644)
}

// UnmarshalYAML decodes a PortSpec from either:
//
//	[min, max]                            # short form
//	{ range: [min, max], env: NAME }      # long form
func (s *PortSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var pair []int
		if err := value.Decode(&pair); err != nil {
			return err
		}
		if len(pair) != 2 {
			return fmt.Errorf("port range must be [min, max], got %v", pair)
		}
		s.Range = PortRange{pair[0], pair[1]}
		return nil
	case yaml.MappingNode:
		var aux struct {
			Range PortRange `yaml:"range"`
			Env   string    `yaml:"env"`
		}
		if err := value.Decode(&aux); err != nil {
			return err
		}
		s.Range = aux.Range
		s.Env = aux.Env
		return nil
	default:
		return fmt.Errorf("port spec must be a list or mapping, got kind %d", value.Kind)
	}
}

// MarshalYAML emits a PortSpec in long form when an env override is set,
// otherwise the short [min, max] list.
func (s PortSpec) MarshalYAML() (interface{}, error) {
	if s.Env == "" {
		return []int{s.Range[0], s.Range[1]}, nil
	}
	return map[string]interface{}{
		"range": []int{s.Range[0], s.Range[1]},
		"env":   s.Env,
	}, nil
}

// UnmarshalYAML lets PortRange decode from a 2-element list `[min, max]`.
func (p *PortRange) UnmarshalYAML(value *yaml.Node) error {
	var pair []int
	if err := value.Decode(&pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return fmt.Errorf("port range must be [min, max], got %v", pair)
	}
	p[0], p[1] = pair[0], pair[1]
	return nil
}

// MarshalYAML emits PortRange as a 2-element list.
func (p PortRange) MarshalYAML() (interface{}, error) {
	return []int{p[0], p[1]}, nil
}
