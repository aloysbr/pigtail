// Package testrun resolves how a test run should be arbitrated, as a value.
//
// Before this package, that resolution was a cascade of early returns inside
// cmd/pigtail/test.go, each printing its own line to stderr and returning
// ("", noop). It worked, but it could not be explained to the user, could not
// be tested without launching vitest, and could not be extended to a second
// runner without duplicating the cascade.
//
// Hence [Plan]: one value that says which runner was found, which layout it
// uses, at what grain slots are taken, what ceiling applies and where it came
// from — and, when arbitration does not apply, a named [Skip] saying why and
// what to do about it. Rendering, explaining and running all read that one
// value.
//
// The invariant every path here serves: arbitration must never be the reason
// tests do not run. Every failure resolves to a Plan that still runs the
// suite, unarbitrated, with a Skip that says so out loud — the silence was the
// real defect (see docs/specs/2026-09-22-mem-admission-design.md §1).
package testrun

// Runner is the test runner a Plan targets.
type Runner string

// The runners pigtail arbitrates. RunnerAuto is the zero value: it means the
// caller did not say, and resolution picks one from what the repo holds.
const (
	RunnerAuto   Runner = ""
	RunnerVitest Runner = "vitest"
	RunnerJest   Runner = "jest"
)

// Layout is the shape of the runner's configuration.
type Layout string

// The layouts pigtail distinguishes. Only LayoutSingle is arbitrated today;
// workspace and projects layouts resolve to a named Skip (PR B covers them).
const (
	LayoutSingle    Layout = "single"
	LayoutWorkspace Layout = "workspace"
	LayoutProjects  Layout = "projects"
	LayoutUnknown   Layout = "unknown"
)

// Grain is what one CPU slot covers for the duration it is held.
type Grain string

// The grains a run can use. GrainFile is vitest's default (one slot per test
// file); GrainNone means no slot is taken at all.
const (
	GrainFile Grain = "file"
	GrainNone Grain = "none"
	// GrainRun means the whole run counts as one, and takes no CPU slot at
	// all. It is what a browser-mode vitest run gets: each of its workers
	// holds a live browser for the run's duration, so serialising its *files*
	// against other runs would keep browsers alive and idle — paying the
	// memory without using the CPU. Such a run is arbitrated by the memory
	// admission's per-class cap instead.
	GrainRun Grain = "run"
)

// Skip codes. They are part of the CLI's contract — `--explain --json` prints
// them and an agent may branch on them — so they are stable strings, not
// free-form prose.
const (
	SkipNoBudget      = "no-cpu-budget"
	SkipNoConfig      = "no-config-found"
	SkipNoVitest      = "vitest-not-resolvable"
	SkipInstallFailed = "client-install-failed"
	SkipRenderFailed  = "config-render-failed"
	// SkipWorkspaceNotStatic covers a workspace file whose project list
	// cannot be read without executing it (inline project objects, a
	// computed array, an interpolated path). Reading it wrong would wrap
	// projects that are not the repo's.
	SkipWorkspaceNotStatic = "workspace-not-static"
	// SkipWorkspaceEmpty covers a workspace file whose patterns match no
	// config here — arbitrating it would arbitrate nothing.
	SkipWorkspaceEmpty = "workspace-matched-no-project"
	SkipNoJest         = "jest-not-resolvable"
	// SkipJestConfigNotRequirable covers an ESM or TypeScript jest config.
	// The wrapper is CommonJS and require()s the repo's own config; it cannot
	// require those, and a wrapper that fails to load would take the whole
	// suite with it — worse than running unarbitrated.
	SkipJestConfigNotRequirable = "jest-config-not-requirable"
)

// Skip says why arbitration does not apply, in terms the user can act on.
type Skip struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

// Project is one configured project inside a workspace layout: the repo's own
// config, the directory that config roots, and the name pigtail falls back to
// when the config names nothing.
//
// Name is a *fallback*, never an override. Vitest names an unnamed project
// after its package.json, and a --project=<name> filter in someone's shell
// history depends on that name staying what it was.
type Project struct {
	ConfigPath string `json:"config_path"`
	Root       string `json:"root"`
	Name       string `json:"name,omitempty"`
}

// Plan is the resolved answer to "how does this run get arbitrated".
//
// A Plan is always usable: Arbitrated() false means "run the suite as-is",
// never "give up".
type Plan struct {
	Runner Runner `json:"runner"`
	Layout Layout `json:"layout"`
	Grain  Grain  `json:"grain"`

	// Cwd is where the run was invoked, Root the worktree it belongs to.
	Cwd  string `json:"cwd"`
	Root string `json:"root"`

	// BaseConfig is the repo's own config the wrapper will import, and
	// NodeModules the directory the wrapper is written under.
	BaseConfig  string `json:"base_config,omitempty"`
	NodeModules string `json:"node_modules,omitempty"`

	// WorkspaceFile is the vitest.workspace.* that declared the projects,
	// for a workspace layout.
	WorkspaceFile string `json:"workspace_file,omitempty"`

	// Projects is one entry per project of a workspace layout, each getting
	// its own generated wrapper.
	Projects []Project `json:"projects,omitempty"`

	// Budget is the daemon's global CPU slot budget, and Ceiling the
	// ceiling that will be applied to the run. Ceiling is the budget unless
	// the repo's own config pins something lower — which only the wrapper
	// can see, at config-evaluation time, hence CeilingFrom below.
	Budget  int `json:"budget"`
	Ceiling int `json:"ceiling"`

	// CeilingFrom says who decides the final ceiling: "pigtail" when the
	// budget is the only cap in play, "min(repo,pigtail)" when the repo's
	// config may lower it further at load time.
	CeilingFrom string `json:"ceiling_from"`

	// Class is the memory-admission class this run should be charged as,
	// when it differs from what the runner alone implies — a browser-mode
	// vitest run costs several times a node one. Empty means "derive it from
	// Runner".
	Class string `json:"class,omitempty"`

	// Workers is how many workers the run is expected to start, when that
	// number drives its cost (a browser per worker). It is what a previous
	// run of this config reported, so it is 0 until one has.
	Workers int `json:"workers,omitempty"`

	// Skips is empty exactly when the run is arbitrated.
	Skips []Skip `json:"skips,omitempty"`

	// Notes carry what the user should know about a plan that is otherwise
	// fine — chiefly a second runner in the same repo, which `pigtail test`
	// does not run but --runner reaches. They are not skips: arbitration
	// applies. --explain prints them; the per-run status line does not, or
	// every run of a two-runner repo would carry the same noise.
	Notes []string `json:"notes,omitempty"`
}

// Arbitrated reports whether the run gets pigtail's arbitration.
func (p Plan) Arbitrated() bool { return len(p.Skips) == 0 }

// WithSkip returns p marked as unarbitrated, for one named reason.
func (p Plan) WithSkip(code, detail, remedy string) Plan {
	p.Grain = GrainNone
	p.Ceiling = 0
	p.CeilingFrom = ""
	p.Skips = append(p.Skips, Skip{Code: code, Detail: detail, Remedy: remedy})
	return p
}
