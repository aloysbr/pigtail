package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Where the target cell came from, as the dry-run and --json name it.
const (
	originCaller  = "caller's cell"
	originFlag    = "--cell"
	originAccount = "same account %s in the subject's scope"
	// originRotation: the account rotation picked it (claudeusage.Pick).
	originRotation = "rotation: account %s"
	// originMarginSwitch: the launch margin guard moved --cell to a free
	// account of its scope (the account named only to a caller that may).
	originMarginSwitch        = "margin switch"
	originMarginSwitchAccount = "margin switch: account %s"
)

// resolveTargetCell picks the cell the new session runs under. With a subject,
// its scope must be the subject's (invariant). A non-master caller only ever
// holds subjects of its own scope (the read 404s otherwise), so naming the
// subject's cells names its own.
//
// Every launch goes through the launch margin guard (sub_x000000000000011,
// D2): with a subject and no --cell, the scope form (launchmargin.GuardScope,
// whose pick is the rotation's); with --cell, the cell form
// (launchmargin.Guard): an over-margin account is switched to a free one of
// the same scope, a whole scope over the margin is refused (exit 3) unless
// force (--over-margin). The checks that follow run on the final cell.
func resolveTargetCell(cs callerSession, flagCell string, sub *pigtail.Subject, force bool) (claudeprofile.Cell, string, spawnRotation, error) {
	cells := cs.Cells
	var target claudeprofile.Cell
	var rot spawnRotation
	origin := originCaller
	deps := launchDeps(cells, cellViewer(cs.Cell))
	// Account rotation (D1, D6): with a subject and no --cell, the account
	// is picked in the subject's scope; any failure falls back to the base
	// choice below and says so.
	picked := false
	if flagCell == "" && sub != nil && sub.Scope != "" {
		rot.Tried = true
		c, dec, err := launchmargin.GuardScope(deps, sub.Scope, force)
		rot.Margin = &dec
		var om *launchmargin.OverMarginError
		switch {
		case errors.As(err, &om):
			rot.Choice = dec.Choice
			return target, "", rot, withExitCode(3, spawnErr("%s", launchmargin.Render(dec, err)))
		case err == nil:
			rot.Choice = dec.Choice
			target, origin, picked = c, fmt.Sprintf(originRotation, dec.Choice.Label), true
			if dec.Action == launchmargin.ActionForced {
				rot.MarginWarnings = append(rot.MarginWarnings, launchmargin.Render(dec, nil))
			}
		}
		rot.err = err // unmeasured: the base choice below, said
	}
	switch {
	case picked:
	case flagCell != "":
		c, ok := cells.Cell(flagCell)
		if !ok || !cellIDRe.MatchString(flagCell) {
			return target, "", rot, spawnErr("unknown cell %q — pass one of: %s", flagCell, strings.Join(cells.IDs(), ","))
		}
		target, origin = c, originFlag
		final, dec, err := launchmargin.Guard(deps, c, force)
		rot.Margin = &dec
		if err != nil {
			return target, "", rot, withExitCode(3, spawnErr("%s", launchmargin.Render(dec, err)))
		}
		if dec.Action == launchmargin.ActionSwitched {
			target, origin = final, originMarginSwitch
			if !deps.Viewer.Bounded(c.Scope) {
				origin = fmt.Sprintf(originMarginSwitchAccount, dec.Choice.Label)
			}
		}
		if w := launchmargin.Render(dec, nil); w != "" {
			rot.MarginWarnings = append(rot.MarginWarnings, w)
		}
	case sub != nil && sub.Scope != "" && sub.Scope != cs.Cell.Scope:
		// master --cross-scope (readCallerSubject already required it)
		cands := cells.SameAccountIn(cs.Cell.Account, sub.Scope)
		if len(cands) == 0 {
			acc := cs.Cell.Account
			if acc == "" {
				acc = "?"
			}
			return target, "", rot, spawnErr("no cell of subject %s's scope shares account %s with cell %s — pass --cell <id> (candidates: %s)",
				sub.ID, acc, cs.Cell.ID, strings.Join(cells.IDsInScope(sub.Scope), ","))
		}
		target, origin = cands[0], fmt.Sprintf(originAccount, cs.Cell.Account)
	default:
		target = cs.Cell
	}
	if sub != nil && sub.Scope != "" && target.Scope != sub.Scope {
		return target, "", rot, spawnErr("cell %s does not run subject %s's scope — pick one of its cells: --cell %s, or drop --subject to launch a subjectless session there",
			target.ID, sub.ID, strings.Join(cells.IDsInScope(sub.Scope), ","))
	}
	// T13: only another cell's dir is checked — the caller's is proven by the
	// live calling session, and withCells / withCellScopes never create it.
	if target.ID != cs.Cell.ID {
		if fi, err := os.Stat(target.ConfigDir); err != nil || !fi.IsDir() {
			return target, "", rot, spawnErr("cell %s's config dir %s does not exist — create it (CLAUDE_CONFIG_DIR=<dir> claude, then /login; see docs/CONFIG.md § claude-cells.yaml), or pick another --cell", target.ID, target.ConfigDir)
		}
	}
	if rot.Tried {
		fallback := fmt.Sprintf("falls back to cell %s (%s)", target.ID, origin)
		if picked {
			fallback = ""
		}
		rot.Warnings = rotationWarnings(derefChoice(rot.Choice), rot.err, fallback)
	}
	return target, origin, rot, nil
}

// spawnRotation is what the account rotation did for a spawn: Tried false
// when it was not asked (--cell, no subject); Choice nil when it failed —
// Warnings then say why and what the base chose instead.
type spawnRotation struct {
	Tried    bool
	Choice   *claudeusage.Choice
	Warnings []string
	err      error
	// Margin is the launch margin guard's decision (nil: the guard did not
	// run — no subject and no --cell); MarginWarnings, the lines it prints
	// on stderr, independent of Tried.
	Margin         *launchmargin.Decision
	MarginWarnings []string
}

// launchDeps builds the launch margin guard's dependencies for a CLI launch
// (seam: tests count the guard's calls through it).
var launchDeps = func(cells *claudeprofile.Table, v launchmargin.Viewer) launchmargin.Deps {
	return launchmargin.Deps{Getenv: os.Getenv, ReadFile: os.ReadFile, Now: time.Now, Cells: cells, Viewer: v}
}

// cellViewer: the caller the guard bounds its texts for, from its cell (D10).
func cellViewer(c claudeprofile.Cell) launchmargin.Viewer {
	return launchmargin.Viewer{Master: scope.SeesAll(scope.Canonical(c.Scope)), Scope: c.Scope}
}

// marginJSON is the "margin" object of spawn --json and the dry-run: null when
// the guard did not run.
func (r spawnRotation) marginJSON() any {
	if r.Margin == nil {
		return nil
	}
	return map[string]any{"decision": r.Margin.Action, "requested": r.Margin.Requested.ID, "final": r.Margin.Final.ID}
}

func derefChoice(c *claudeusage.Choice) claudeusage.Choice {
	if c == nil {
		return claudeusage.Choice{}
	}
	return *c
}

// checkSubjectless holds a subjectless spawn (D1) to its contract: only into
// another scope's cell (R4, D4), never with --cross-scope (R6, T10), always
// with an explicit --cwd (R5).
func checkSubjectless(req spawnRequest, cs callerSession, target claudeprofile.Cell) error {
	if target.Scope == cs.Cell.Scope {
		return spawnErr("cell %s runs this session's own scope: a subjectless spawn only goes to another scope's cell — create the subject first (skill subject-capture, or pigtail subject create --theme <theme_id> --title <title>), then pass --subject <id>", target.ID)
	}
	if req.CrossScope {
		return spawnErr("--cross-scope has no effect without --subject — drop it (a subjectless spawn writes nothing to another scope)")
	}
	if req.Cwd == "" {
		return spawnErr("a subjectless spawn needs --cwd <dir> — pass the directory the new session starts in")
	}
	if req.NewWorktree && req.Branch == "" { // R18
		return spawnErr("--new-worktree without --subject needs --branch <type/slug> — name the branch to create")
	}
	return nil
}

// checkCwdScope refuses a subjectless spawn's cwd that does not belong to the
// target cell's scope: the longest cwd_home containing it (T1) must be one of
// that scope's cells' (R11–R14). Names cell ids and homes, never a scope.
func checkCwdScope(cells *claudeprofile.Table, cwd string, target claudeprofile.Cell) error {
	h, err := cells.HomeFor(cwd)
	var amb *claudeprofile.AmbiguousHomeError
	switch {
	case errors.Is(err, claudeprofile.ErrNoHomes):
		return spawnErr("claude-cells.yaml carries no cwd_home — set cwd_home on its cells (see docs/CONFIG.md § claude-cells.yaml), then retry")
	case errors.Is(err, claudeprofile.ErrNoHome):
		return spawnErr("cwd %s is under no cell's home — pass a --cwd under cell %s's home (%s)", cwd, target.ID, homeOr(target))
	case errors.As(err, &amb):
		return spawnErr("cwd %s: cells %s of different scopes share the home %s — fix cwd_home in claude-cells.yaml (see docs/CONFIG.md § claude-cells.yaml)", cwd, strings.Join(amb.Cells, ","), amb.Home)
	case err != nil:
		return spawnErr("%v", err)
	case h.Scope != target.Scope:
		return spawnErr("cwd %s is under the home of cells %s (%s), not cell %s's — pass a --cwd under %s's home (%s)",
			cwd, strings.Join(h.Cells, ","), h.Path, target.ID, target.ID, homeOr(target))
	}
	return nil
}

func homeOr(c claudeprofile.Cell) string {
	if c.CwdHome == "" {
		return "none declared"
	}
	return c.CwdHome
}

// json is the "rotation" object of spawn --json: null when not tried.
func (r spawnRotation) json() any {
	if !r.Tried {
		return nil
	}
	return map[string]any{"choice": r.Choice, "warnings": append([]string{}, r.Warnings...)}
}

// rankingLine is one candidate of the rotation ranking, for the dry-run.
func rankingLine(c claudeusage.Candidate) string {
	win := func(name string, v *claudeusage.WindowView) string {
		switch {
		case v == nil:
			return name + " —"
		case v.ResetPassed:
			return name + " 0% (reset passed)"
		case v.ResetsAt == nil:
			return fmt.Sprintf("%s %d%%", name, v.Percent)
		}
		return fmt.Sprintf("%s %d%% (resets %s)", name, v.Percent, v.ResetsAt.Local().Format("02/01 15:04"))
	}
	line := fmt.Sprintf("%s on %s — %s, %s, %s", c.Label, c.Cell.ID, c.Group, win("7d", c.SevenDay), win("5h", c.FiveHour))
	if c.Age != nil {
		line += ", read " + claudeusage.FormatAge(*c.Age) + " ago"
	}
	if c.FreesAt != nil {
		line += ", frees up " + c.FreesAt.Local().Format("02/01 15:04")
	}
	return line
}
