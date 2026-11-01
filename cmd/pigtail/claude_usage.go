package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/usagerefresh"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/spf13/cobra"
)

// usageDeps: everything `pigtail usage` touches, injected (maintenance status
// model). The % come from the cells' .claude.json files only; the SESSIONS
// column alone asks the local daemon (peerSessions), and does without it.
type usageDeps struct {
	cellsDir     string // pigtail config dir holding claude-cells.yaml
	getenv       func(string) string
	readFile     func(string) ([]byte, error)
	now          func() time.Time
	loc          *time.Location // display only; every computation is UTC
	out, errw    io.Writer
	peerSessions func() ([]pigtail.PeerSession, error) // the daemon's session table, as the caller's scope sees it

	refresh        claudeusage.LaunchFunc // nil: no --refresh; else runs claude -p /usage on a cell
	refreshTimeout time.Duration          // per account

	// stateDir is pigtail's state directory (spawns.jsonl's): the refresh
	// lock and the daemon's usage-refresh.json live there.
	stateDir string
	// refreshLock takes the inter-process refresh lock (claudeusage.TryRefreshLock):
	// --refresh and the daemon's sweep never run claude -p /usage together.
	refreshLock func(stateDir string) (unlock func(), held bool, err error)

	// marginSinks are where usage pick traces a launch margin decision; nil:
	// not traced (tests that do not look at traces).
	marginSinks func() launchmargin.Sinks
}

// pickOpts: usage pick --cell / --over-margin.
type pickOpts struct {
	cell  string
	force bool
}

// usageRefreshTimeout bounds one account's claude -p /usage.
const usageRefreshTimeout = 60 * time.Second

func usageCmd() *cobra.Command {
	var asJSON, refresh bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Claude plan usage per account (7-day and 5-hour windows), from the cells' local cache",
		Long: `Reads the usage cache Claude Code keeps in each cell's .claude.json (no
network call), one line per Claude account: capacity, 7-day and 5-hour usage
with their resets (local time), age of the reading, sessions, cells. The head
line is the weighted 7-day usage (weight = the account's capacity) over every
account with a known capacity and a 7-day reading, the same from any cell.

SESSIONS is busy/open Claude sessions per account, from the pigtail daemon's
session table (the one behind pigtail session ls): only the sessions the
calling cell's scope may see, marked * when that is not all of them. Daemon
unreachable: SESSIONS shows — with a warning, the rest is unchanged.

A reading older than 1h is marked "!": Claude Code itself ignores it;
pigtail usage --refresh refreshes it. A window whose reset has passed shows 0%.

With --refresh, each account's cache is refreshed first, one account after
the other: claude -p /usage (a local Claude Code command: no model call, no
token) runs on one cell per account, a cell of the caller's scope first, then
the usual view is printed. From the master scope every account is refreshed;
from any other scope, only the accounts with a cell of that scope. A reading
less than 60s old is left as is (fresh). A failed refresh shows on its
account (REFRESH column, a "refresh failed:" line, "refresh" in --json); the
rest of the report is unchanged. --refresh and the daemon's background
refresh never run claude -p /usage together: while the other holds the lock
(usage-refresh.lock in pigtail's state directory), --refresh launches nothing,
says so on stderr and prints the cached view (exit 0).

The daemon refreshes, every 10 min, the accounts whose reading is older than
20 min, to keep them under 30 min (pigtaild -usage-refresh); one being refreshed or failing can pass it. Its last failures show as "background
refresh failed:" lines ("background_refresh" in --json).

Exit 1 when the cell table is missing or empty, when a cell of another scope asks for
the perso view (--scope perso), or when no cell could be read.

Exit 2 when --refresh failed on at least one account (the whole report is
still printed).

pigtail usage pick (--scope <s> | --cwd <dir>) prints the cell a launch
without --cell runs on: the account whose 7-day window renews first among
those under ` + claudeusage.CeilingHelp + `, fresh readings (30 min)
first; pigtail usage pick --cell <c> guards a launch on cell c (kept, switched
to a free account of its scope, or refused). Both exit 3 when every account of
the scope is over the margin (--over-margin launches anyway); see pigtail
usage pick --help.`,
		Args: cobra.NoArgs,
		// SESSIONS reads POST /peers through the caller's scope, like
		// `session ls`: the daemon filters what this cell may see.
		Annotations: scopedRead(),
		RunE: func(_ *cobra.Command, _ []string) error {
			d := usageDeps{
				cellsDir: setup.ConfigDir(), getenv: os.Getenv, readFile: os.ReadFile,
				now: time.Now, loc: time.Local, out: os.Stdout, errw: os.Stderr,
				peerSessions: func() ([]pigtail.PeerSession, error) {
					c, err := newScopedClient()
					if err != nil {
						return nil, err
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					// the same local snapshot session ls sends; its presence
					// warnings belong to session ls, not here
					ps, _ := snapshot(readSelfEnv(os.Getenv), io.Discard)
					return c.PostPeers(ctx, pigtail.PeersRequest{Peers: ps})
				},
			}
			d.stateDir = filepath.Dir(spawnreg.DefaultPath())
			if refresh {
				d.refresh = usagerefresh.New(os.Environ()).Launch
				d.refreshTimeout = usageRefreshTimeout
				d.refreshLock = claudeusage.TryRefreshLock
			}
			return runUsage(d, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the full report as JSON (per-model weekly limit, extra usage)")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "refresh each account's usage cache first (claude -p /usage, one account at a time, no tokens)")
	cmd.AddCommand(usagePickCmd())
	return cmd
}

func usagePickCmd() *cobra.Command {
	var asJSON bool
	var cwd string
	var o pickOpts
	cmd := &cobra.Command{
		Use:   "pick",
		Short: "Print the cell a launch runs on (account rotation, launch margin guard)",
		Long: `Picks the Claude account a launch runs on, among the logged-in cells of one
scope, from the same cached readings as pigtail usage (no network call): the
account under ` + claudeusage.CeilingHelp + ` (a window whose reset has
passed counts 0%) whose 7-day window renews first. Fresh readings (30 min or
less) come first, then stale ones, then accounts with no reading; ties go to
the first declared cell. The account's cell is its first declared cell of the
scope.

Exactly one of --cell <c>, --scope <s> (the scope to pick in) and --cwd <dir>
(the scope whose cell home holds dir). A non-master cell picks for its own
scope only with --scope / --cwd. A caller outside any cell (an app with an
empty environment) counts as the default scope (fail-closed): --scope / --cwd
for a scope other than the default works, its --json is the reduced form.

--cell <c> guards a launch on cell c (the launch margin guard): c is kept when
its account is under the margin or has no reading; when it is over the margin,
the launch moves to a free account of c's scope ("warning: launch margin: …"
on stderr); when every account of the scope is over the margin, the launch is
refused (exit 3, the first release time on stderr). c's scope is read from the
cell table, not from the calling cell: a launch on a cell of another scope is
allowed, its output then names no other cell, account or scope — except the
chosen cell's profile dir, whose path may spell its scope (--json is reduced
to cell, config_dir, decision, requested, frees_at).

--over-margin launches anyway: the requested cell (--cell, nothing read) or
the pick (--scope / --cwd), decision "forced".

Every decision but ok is traced: a "margin" line in spawns.jsonl and a
launch-margin journal row (pigtail journal list --kind launch-margin). When
the guard cannot measure (PIGTAIL_ROTATION=off, no candidate) with --cell, the
requested cell is printed with a warning (decision "unmeasured").

Stdout: the cell id alone (--json: the whole choice and ranking, or the
reduced form above, always with "config_dir" (the chosen cell's profile dir),
plus "decision" ok|switched|forced|unmeasured|refused, "requested", "frees_at").
Pick warnings (stale reading) go to stderr as "warning: rotation: …".

Exit 1 when no cell of the scope can be picked (each cell's reason on
stderr), or when PIGTAIL_ROTATION=off with --scope / --cwd.
Exit 3 when every account of the scope is over the margin (refused; pass
--over-margin to launch anyway).`,
		Args: cobra.NoArgs,
		// Its one write, the launch margin journal row, stays in the caller's
		// own scope (never crosses): a read for the --cross-scope guard.
		Annotations: scopedRead(),
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUsagePick(usageDeps{
				cellsDir: setup.ConfigDir(), getenv: os.Getenv, readFile: os.ReadFile,
				now: time.Now, loc: time.Local, out: os.Stdout, errw: os.Stderr,
				marginSinks: func() launchmargin.Sinks {
					return cliMarginSinks(func(e *pigtail.JournalEntry) error {
						c, err := newScopedClient()
						if err != nil {
							return err
						}
						return addMarginJournal(c, e)
					}, os.Stderr)
				},
			}, cwd, asJSON, o)
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "pick in the scope whose cell home (cwd_home) holds this directory")
	cmd.Flags().StringVar(&o.cell, "cell", "", "guard a launch on this cell: kept under the margin, switched to a free account of its scope, refused (exit 3) when the whole scope is over")
	cmd.Flags().BoolVar(&o.force, "over-margin", false, "launch anyway when over the margin (decision forced)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the whole choice (ranking, warnings, decision) as JSON")
	return cmd
}

// runUsagePick: --scope comes from the persistent flag (flagScope), never
// from PIGTAIL_SCOPE: the launcher names the scope it means. --cell takes its
// scope from the cell table, never from the caller (resolveScope does not
// apply): the guard runs on that cell's scope, its output bounded for a
// non-master caller of another scope (D19/D23, D10).
func runUsagePick(d usageDeps, cwd string, asJSON bool, opts ...pickOpts) error {
	var o pickOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	tbl, err := claudeprofile.Load(d.cellsDir)
	if err != nil {
		return err
	}
	deps := launchmargin.Deps{Getenv: d.getenv, ReadFile: d.readFile, Cells: tbl,
		Now: func() time.Time { return d.now().In(d.displayLoc()) }, Viewer: marginViewer(tbl, d.getenv)}
	if o.cell != "" {
		if flagScope != "" || cwd != "" {
			return fmt.Errorf("usage pick: --cell excludes --scope and --cwd")
		}
		c, ok := tbl.Cell(o.cell)
		if !ok || !cellIDRe.MatchString(o.cell) {
			return fmt.Errorf("usage pick: unknown cell %q — pass one of: %s", o.cell, strings.Join(tbl.IDs(), ","))
		}
		final, dec, gerr := launchmargin.Guard(deps, c, o.force)
		return d.pickOutcome(final, dec, gerr, asJSON, deps.Viewer)
	}
	if (flagScope == "") == (cwd == "") {
		return fmt.Errorf("usage pick: give exactly one of --scope and --cwd")
	}
	target := flagScope
	if cwd != "" {
		h, err := tbl.HomeFor(cwd)
		var amb *claudeprofile.AmbiguousHomeError
		switch {
		case errors.Is(err, claudeprofile.ErrNoHomes):
			return fmt.Errorf("claude-cells.yaml carries no cwd_home — set cwd_home on its cells (see docs/CONFIG.md § claude-cells.yaml), then retry")
		case errors.Is(err, claudeprofile.ErrNoHome):
			return fmt.Errorf("cwd %s is under no cell's home — pass --scope, or a --cwd under a cell's home", cwd)
		case errors.As(err, &amb):
			return fmt.Errorf("cwd %s: cells %s of different scopes share the home %s — fix cwd_home in claude-cells.yaml (see docs/CONFIG.md § claude-cells.yaml)", cwd, strings.Join(amb.Cells, ","), amb.Home)
		case err != nil:
			return err
		}
		target = h.Scope
	}
	sc, err := scope.Parse(target)
	if err != nil {
		return fmt.Errorf("usage pick: %w", err)
	}
	// resolveScope refuses a non-master cell asking for another scope (D19).
	if _, err := resolveScope(tbl, claudeConfigDir(d.getenv), string(sc)); err != nil {
		return err
	}
	cell, dec, err := launchmargin.GuardScope(deps, string(sc), o.force)
	var om *launchmargin.OverMarginError
	if err != nil && !errors.As(err, &om) {
		// unmeasured in the scope form: nothing picked, exit 1 as before
		for _, w := range rotationWarnings(derefChoice(dec.Choice), err, "") {
			fmt.Fprintf(d.errw, "warning: %s\n", w)
		}
		return withExitCode(1, fmt.Errorf("usage pick: no cell picked in scope %s", sc))
	}
	if om == nil { // a refusal replaces the pick's own over-margin warning
		for _, w := range rotationWarnings(derefChoice(dec.Choice), nil, "") {
			fmt.Fprintf(d.errw, "warning: %s\n", w)
		}
	}
	return d.pickOutcome(cell, dec, err, asJSON, deps.Viewer)
}

func (d usageDeps) displayLoc() *time.Location {
	if d.loc == nil {
		return time.UTC
	}
	return d.loc
}

// pickOutcome prints a guard decision: the launch margin warning on stderr,
// the cell on stdout (--json: the choice plus config_dir, decision, requested,
// frees_at; bounded to {cell, config_dir, decision, requested, frees_at} for a
// caller that may not name the requested cell's scope), exit 3 on a refusal; and traces every
// decision but ok.
func (d usageDeps) pickOutcome(cell claudeprofile.Cell, dec launchmargin.Decision, gerr error, asJSON bool, v launchmargin.Viewer) error {
	if d.marginSinks != nil {
		launchmargin.Record(d.marginSinks(), dec)
	}
	var om *launchmargin.OverMarginError
	if errors.As(gerr, &om) {
		return withExitCode(3, fmt.Errorf("usage pick: %s", launchmargin.Render(dec, gerr)))
	}
	if gerr != nil {
		return gerr
	}
	if w := launchmargin.Render(dec, nil); w != "" {
		fmt.Fprintf(d.errw, "warning: %s\n", w)
	}
	if !asJSON {
		fmt.Fprintln(d.out, cell.ID)
		return nil
	}
	enc := json.NewEncoder(d.out)
	enc.SetEscapeHTML(false)
	if v.Bounded(dec.Requested.Scope) {
		return enc.Encode(map[string]any{"cell": cell.ID, "config_dir": cell.ConfigDir, "decision": dec.Action, "requested": dec.Requested.ID, "frees_at": dec.FreesAt})
	}
	out := map[string]any{}
	if dec.Choice != nil {
		b, err := json.Marshal(dec.Choice)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return err
		}
	}
	out["cell"] = cell.UsageInput()
	out["config_dir"] = cell.ConfigDir
	out["decision"], out["requested"], out["frees_at"] = dec.Action, dec.Requested.ID, dec.FreesAt
	return enc.Encode(out)
}

func runUsage(d usageDeps, asJSON bool) error {
	tbl, err := claudeprofile.Load(d.cellsDir)
	if err != nil {
		return err
	}
	dirs := tbl.ConfigDirs() // never a glob: ~/.claude-work and backups are not cells
	if len(dirs) == 0 {
		return fmt.Errorf("%s lists no cell — see docs/CONFIG.md § claude-cells.yaml", filepath.Join(d.cellsDir, claudeprofile.Filename))
	}
	override := flagScope
	if override == "" {
		override = d.getenv(envScope)
	}
	sc, err := resolveScope(tbl, claudeConfigDir(d.getenv), override)
	if err != nil {
		return err
	}
	cells := CellsFrom(tbl)
	snaps := claudeusage.ReadTable(cells, d.readFile)
	var refreshed []claudeusage.RefreshResult
	ran := false
	var lockErr error
	if d.refresh != nil {
		ran, lockErr = runLockedRefresh(d, func() {
			// D2: one account after the other, then the usual view, read again.
			refreshed = claudeusage.Refresh(context.Background(),
				claudeusage.RefreshTargets(snaps, scope.Scope(sc)),
				claudeusage.RefreshDeps{
					Launch: d.refresh, ReadFile: d.readFile, Now: d.now, Timeout: d.refreshTimeout,
					Progress: func(tg claudeusage.RefreshTarget) { fmt.Fprintf(d.errw, "refreshing cell %s\n", tg.Cell.ID) },
				})
		})
		if ran {
			snaps = claudeusage.ReadTable(cells, d.readFile)
		}
	}
	rep := claudeusage.Build(snaps, scope.Scope(sc), d.now())
	if ran {
		rep.AttachRefresh(refreshed)
	}
	ps, perr := d.peerSessions()
	if perr != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("sessions unavailable (%v) — SESSIONS shown as —", perr))
	}
	rep.AttachSessions(sessionRefs(ps), perr == nil) // never changes the exit code (D2)
	var bg *claudeusage.BackgroundState
	if d.stateDir != "" {
		var berr error
		if bg, berr = readBackgroundRefresh(backgroundStatePath(d.stateDir)); berr != nil {
			rep.Warnings = append(rep.Warnings, "background refresh state unreadable — "+berr.Error())
		}
	}
	bgView, bgLines := backgroundRefreshOf(bg, rep, scope.Scope(sc), d.now())
	for _, w := range rep.Warnings {
		fmt.Fprintf(d.errw, "warning: %s\n", w)
	}
	if asJSON {
		enc := json.NewEncoder(d.out)
		enc.SetEscapeHTML(false) // extra_usage/spend: the cache's values, compacted, nothing escaped (AC2)
		// background_refresh beside the report's own keys (omitempty: E1's
		// contract unchanged when the daemon never swept)
		if err := enc.Encode(struct {
			claudeusage.Report
			BackgroundRefresh *backgroundRefreshView `json:"background_refresh,omitempty"`
		}{rep, bgView}); err != nil {
			return err
		}
	} else {
		renderUsageText(d.out, rep, d.loc, bgLines...)
	}
	// Refresh failures first: exit 2 wins over E1's exit 1. Counted on the
	// refresh results, not on the report: an account whose only cell became
	// unreadable has left the report, its failure still counts — and when no
	// cell is readable any more after the launches, it is still exit 2 with
	// every failure shown, never "no cell could be read".
	if lockErr != nil {
		return withExitCode(2, fmt.Errorf("usage refresh not run: %w", lockErr))
	}
	if failed, attempted := claudeusage.RefreshCounts(refreshed); failed > 0 {
		return withExitCode(2, fmt.Errorf("refresh failed for %d of %d accounts", failed, attempted))
	}
	if rep.CellsRead == 0 { // every cell, every scope (the count shown is the visible ones, D19)
		if len(rep.Unreadable) == 0 { // all hidden from this scope (D19): never count them (D6)
			return fmt.Errorf("no cell could be read")
		}
		return fmt.Errorf("no cell could be read (%d unreadable)", len(rep.Unreadable))
	}
	return nil
}

// runLockedRefresh runs refresh under the inter-process refresh lock. Held
// by another refresh (the daemon's sweep): nothing runs, a warning says so,
// ran=false and no error (the report comes from the cache, exit 0). The lock
// cannot be taken: nothing runs either — never a refresh unlocked — and the
// error is returned (said, exit 2).
func runLockedRefresh(d usageDeps, refresh func()) (ran bool, err error) {
	if d.refreshLock == nil {
		err = errors.New("no refresh lock configured")
	} else {
		var unlock func()
		var held bool
		unlock, held, err = d.refreshLock(d.stateDir)
		if err == nil && held {
			fmt.Fprintf(d.errw, "warning: usage refresh skipped — another refresh is running (%s)\n", claudeusage.RefreshLockPath(d.stateDir))
			return false, nil
		}
		if err == nil {
			defer unlock()
			refresh()
			return true, nil
		}
	}
	fmt.Fprintf(d.errw, "warning: usage refresh not run — %v\n", err)
	return false, err
}

// CellsFrom is the cell table as claudeusage reads it, in config-dir order
// (cell id): never a glob, ~/.claude-work and backups are not cells.
func CellsFrom(tbl *claudeprofile.Table) []claudeusage.CellInput {
	dirs := tbl.ConfigDirs()
	out := make([]claudeusage.CellInput, 0, len(dirs))
	for _, dir := range dirs {
		c, _ := tbl.ByConfigDir(dir)
		out = append(out, c.UsageInput())
	}
	return out
}

// sessionRefs keeps what SESSIONS counts from the daemon's rows: open = not
// ended and not dead; busy = open and Claude's native status is busy or shell
// (shell: Claude Code is running a Bash-tool command — a turn in progress, D7).
func sessionRefs(ps []pigtail.PeerSession) []claudeusage.SessionRef {
	out := make([]claudeusage.SessionRef, 0, len(ps))
	for _, p := range ps {
		open := p.EndedAt == nil && (p.Alive == nil || *p.Alive)
		out = append(out, claudeusage.SessionRef{ID: p.SessionID, CellID: p.CellID, Open: open, Busy: open && (p.Status == "busy" || p.Status == "shell")})
	}
	return out
}

// backgroundRefreshView is the daemon's last background refresh as the
// caller may see it (D19): only the failures of accounts in the caller's
// report, a cell named only when the caller sees it.
type backgroundRefreshView struct {
	At        time.Time               `json:"at"`
	ResultsAt *time.Time              `json:"results_at"`
	Skipped   string                  `json:"skipped,omitempty"`
	Error     string                  `json:"error,omitempty"`
	Failures  []backgroundFailureView `json:"failures"`
}

type backgroundFailureView struct {
	Label       string `json:"label"`
	AccountUUID string `json:"account_uuid"`
	Cell        string `json:"cell,omitempty"`
	Error       string `json:"error"`
	AgeSeconds  int64  `json:"age_seconds"`
}

// backgroundRefreshOf filters the state for the caller and renders its lines
// (backgroundFailureLine, the rotation's rendering too). nil state: nothing.
func backgroundRefreshOf(st *claudeusage.BackgroundState, rep claudeusage.Report, caller scope.Scope, now time.Time) (*backgroundRefreshView, []string) {
	if st == nil {
		return nil, nil
	}
	listed := map[string]bool{}
	for _, a := range rep.Accounts {
		listed[a.AccountUUID] = true
	}
	v := &backgroundRefreshView{At: st.At, ResultsAt: st.ResultsAt, Skipped: st.Skipped, Error: st.Error, Failures: []backgroundFailureView{}}
	var lines []string
	for _, res := range backgroundFailures(st) {
		if !listed[res.AccountUUID] { // an account the caller sees no cell of (D19)
			continue
		}
		f := backgroundFailureView{Label: res.Label, AccountUUID: res.AccountUUID, Error: res.Error,
			AgeSeconds: int64(backgroundAge(st, now) / time.Second)}
		if claudeusage.CellVisible(caller, res.CellScope) {
			f.Cell = res.Cell
		}
		v.Failures = append(v.Failures, f)
		lines = append(lines, backgroundFailureLine(res, st, caller, now))
	}
	return v, lines
}

// budgetCell is an account's BUDGET column: used/budget, then what is left or
// until when it is over; "?" when it is not measured.
func budgetCell(b claudeusage.BudgetView, loc *time.Location) string {
	switch b.State {
	case claudeusage.BudgetWithin:
		return fmt.Sprintf("%.0f/%.0f%% (left %.0f)", b.Used, b.BudgetPct, b.Left)
	case claudeusage.BudgetOver:
		s := fmt.Sprintf("%.0f/%.0f%% over", b.Used, b.BudgetPct)
		if b.FreesAt != nil {
			s += " until " + b.FreesAt.In(loc).Format("Mon 15:04")
		}
		return s
	}
	return "?"
}

// renderUsageText prints the report; extra lines (background refresh
// failures) come after the refresh failures, before the notes.
func renderUsageText(w io.Writer, r claudeusage.Report, loc *time.Location, extra ...string) {
	when := func(t *time.Time) string { return t.In(loc).Format("02/01 15:04") }
	head := "—"
	if r.Weighted7dUsed != nil {
		head = fmt.Sprintf("%d%%", *r.Weighted7dUsed)
	}
	// D8: the same head line from any cell — every account with a known
	// capacity and a 7-day reading, whatever the caller's scope.
	fmt.Fprintf(w, "7d used (weighted): %s\n\n", head)

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	// Spec 15: outside perso the view is partial by construction, sessions
	// available or not (scope package, no spelt scope name).
	partial := !scope.SeesAll(scope.Scope(r.CallerScope))
	sessHead := "SESSIONS"
	if partial {
		sessHead = "SESSIONS*"
	}
	withRefresh := false
	for _, a := range r.Accounts {
		withRefresh = withRefresh || a.Refresh != nil
	}
	refHead := ""
	if withRefresh {
		refHead = "REFRESH\t"
	}
	fmt.Fprintln(tw, "ACCOUNT\t7D\t7D RESET\t5H\t5H RESET\tBUDGET\tAGE\t"+refHead+sessHead+"\tCAP\tCELLS\tFLAGS")
	stale := false
	for _, a := range r.Accounts {
		capS := "?"
		if a.Capacity != nil {
			capS = fmt.Sprintf("×%d", *a.Capacity)
		}
		p7, r7, p5, r5, age := "—", "—", "—", "—", "—"
		if rd := a.Reading; rd != nil {
			p7, r7 = windowCells(rd.SevenDay, when)
			p5, r5 = windowCells(rd.FiveHour, when)
			age = claudeusage.FormatAge(time.Duration(rd.AgeSeconds) * time.Second)
			if rd.Stale {
				age += "!"
				stale = true
			}
		}
		sess := "—"
		if a.SessionsBusy != nil && a.SessionsOpen != nil {
			sess = fmt.Sprintf("%d/%d", *a.SessionsBusy, *a.SessionsOpen)
		}
		ids := make([]string, 0, len(a.Cells))
		for _, c := range a.Cells {
			ids = append(ids, c.ID)
		}
		ref := ""
		if withRefresh {
			ref = refreshCell(a.Refresh) + "\t"
		}
		flags := make([]string, 0, len(a.Flags))
		for _, f := range a.Flags {
			if f == "cap" && a.Reading != nil {
				f = creditsFlag(a.Reading.Spend)
			}
			flags = append(flags, f)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s%s\t%s\t%s\t%s\n",
			a.Label, p7, r7, p5, r5, budgetCell(a.Budget, loc), age, ref, sess, capS, strings.Join(ids, ","), strings.Join(flags, ","))
	}
	_ = tw.Flush()
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	for _, u := range r.Unreadable {
		fmt.Fprintf(w, "unreadable: %s — %s — not counted\n", u.ID, u.Error)
	}
	for _, a := range r.Accounts {
		if a.Refresh != nil && a.Refresh.Status == claudeusage.RefreshFailed {
			fmt.Fprintf(w, "refresh failed: %s (cell %s) — %s\n", a.Label, a.Refresh.Cell, a.Refresh.Error)
		}
	}
	for _, x := range r.RefreshFailures { // accounts that left the report
		fmt.Fprintf(w, "refresh failed: %s (cell %s) — %s\n", x.Label, x.Cell, x.Error)
	}
	for _, l := range extra {
		fmt.Fprintln(w, l)
	}
	for _, a := range r.Accounts {
		if a.Budget.State == claudeusage.BudgetUnmeasured {
			fmt.Fprintf(w, "budget: %s: unmeasured: %s — Staffing will not launch on this account\n", a.Label, a.Budget.Why)
		}
	}
	var notes []string
	if stale {
		notes = append(notes, "! reading older than 1h: Claude Code itself ignores it — pigtail usage --refresh refreshes it")
	}
	if partial { // the caller's own scope, as pigtail resolved it — never another scope's name
		notes = append(notes, fmt.Sprintf("* %s-visible sessions only", r.CallerScope))
	}
	if len(notes) > 0 {
		fmt.Fprintln(w)
		for _, n := range notes {
			fmt.Fprintln(w, n)
		}
	}
}

// refreshCell is the REFRESH column: ok, fresh, failed, or — (skipped).
func refreshCell(x *claudeusage.RefreshResult) string {
	switch {
	case x == nil || x.Status == claudeusage.RefreshSkipped:
		return "—"
	case x.Status == claudeusage.RefreshOK:
		return "ok"
	}
	return string(x.Status) // fresh, failed
}

// creditsFlag spells the "cap" flag for the table: the extra-usage credits
// beyond the plan are spent ("credits=105/100$"), not the plan itself.
// The JSON keeps "cap"; an unreadable spend falls back to "credits-cap".
func creditsFlag(spend json.RawMessage) string {
	type money struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
		Exponent    int    `json:"exponent"`
	}
	var sp struct {
		Used  *money `json:"used"`
		Limit *money `json:"limit"`
	}
	if json.Unmarshal(spend, &sp) != nil || sp.Used == nil || sp.Limit == nil {
		return "credits-cap"
	}
	major := func(m *money) int64 {
		d := int64(1)
		for i := 0; i < m.Exponent; i++ {
			d *= 10
		}
		return (m.AmountMinor + d/2) / d
	}
	sym := map[string]string{"EUR": "€", "USD": "$"}[sp.Limit.Currency]
	if sym == "" {
		sym = " " + sp.Limit.Currency
	}
	return fmt.Sprintf("credits=%d/%d%s", major(sp.Used), major(sp.Limit), sym)
}

func windowCells(v *claudeusage.WindowView, when func(*time.Time) string) (pct, reset string) {
	switch {
	case v == nil:
		return "—", "—"
	case v.ResetPassed:
		return "0%", "passed"
	case v.ResetsAt == nil && v.RawPercent == 0:
		// Not started: a window opens at the first message, so no reset
		// exists yet — "?" would read as a missing reading.
		return "0%", "—"
	case v.ResetsAt == nil:
		return fmt.Sprintf("%d%%", v.Percent), "?"
	}
	return fmt.Sprintf("%d%%", v.Percent), when(v.ResetsAt)
}
