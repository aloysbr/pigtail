package handlers

import (
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// launchProfile is a dispatch's or a run's profile, resolved once before
// anything is written (sub_x000000000000011): its id and its env (nil = no
// profile), possibly switched to another cell by the launch margin guard.
type launchProfile struct {
	ID     string
	Env    map[string]string
	Margin *pigtail.LaunchMargin
}

// resolveLaunchProfile resolves the profile env from pigtail.yaml (the
// request's profile, else the project's default). On failure it writes the
// error response itself and returns ok=false.
func resolveLaunchProfile(w http.ResponseWriter, r *http.Request, resp Responder, proj *pigtail.Project, requested string) (launchProfile, bool) {
	lp := launchProfile{ID: requested}
	if lp.ID == "" {
		lp.ID = proj.DefaultProfileID
	}
	if lp.ID == "" {
		return lp, true
	}
	cfg, cerr := pigtail.LoadProjectConfig(proj.RootPath)
	if cerr != nil {
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidProfile,
			"cannot load project config to resolve profile: "+cerr.Error(),
			map[string]interface{}{"profile_id": lp.ID})
		return lp, false
	}
	prof, ok := cfg.Profile(lp.ID)
	if !ok {
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidProfile,
			"unknown profile_id", map[string]interface{}{"profile_id": lp.ID})
		return lp, false
	}
	lp.Env = resolveProfileEnv(prof)
	return lp, true
}

// marginDeps are the guard's deps on the daemon: the cell table of the
// daemon's config dir, the real env, files and clock, and a closed viewer
// (D11: /dispatch and /run are unscoped, so every text is the bounded form).
func (svc *Services) marginDeps() launchmargin.Deps {
	if svc.MarginDeps != nil {
		return svc.MarginDeps()
	}
	tbl, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil {
		tbl = nil // no table: the guard says unmeasured
	}
	return launchmargin.Deps{Getenv: os.Getenv, ReadFile: os.ReadFile, Now: time.Now, Cells: tbl,
		Viewer: launchmargin.Viewer{}}
}

func (svc *Services) marginSinks(requestedScope string) launchmargin.Sinks {
	reg := svc.MarginSpawns
	if reg == nil {
		reg = spawnreg.Open(spawnreg.DefaultPath())
	}
	js := requestedScope
	if js == "" {
		js = string(scope.Master()) // a launch tied to no cell: the master's view only
	}
	return launchmargin.Sinks{
		Spawns:       reg,
		Journal:      func(e *pigtail.JournalEntry) error { _, err := svc.Store.AddJournal(e); return err },
		Logf:         svc.logf,
		JournalScope: js,
	}
}

// guardLaunch runs the launch margin guard on a resolved profile, before
// anything is written. No profile: nothing to guard. A profile with no
// config_dir, or one that matches no cell, goes on, traced unmeasured. A
// switch replaces CLAUDE_CONFIG_DIR in a copy of the env, for this launch
// only (pigtail.yaml is never touched). The refusal is the error (HTTP 409).
func guardLaunch(svc *Services, lp *launchProfile, force bool) error {
	if lp.Env == nil {
		return nil
	}
	d := svc.marginDeps()
	var dec launchmargin.Decision
	var err error
	dir := lp.Env["CLAUDE_CONFIG_DIR"]
	if c, ok := d.Cells.ByConfigDir(dir); ok {
		var final claudeprofile.Cell
		final, dec, err = launchmargin.Guard(d, c, force)
		if err == nil && dec.Action == launchmargin.ActionSwitched {
			env := make(map[string]string, len(lp.Env))
			for k, v := range lp.Env {
				env[k] = v
			}
			env["CLAUDE_CONFIG_DIR"] = final.ConfigDir
			lp.Env = env
		}
	} else {
		why := "no-cell"
		if dir == "" {
			why = "no-config-dir"
		}
		dec = launchmargin.Unmeasured(d, claudeprofile.Cell{}, why)
	}
	launchmargin.Record(svc.marginSinks(dec.Requested.Scope), dec)
	if err != nil {
		return err
	}
	if dec.Action != launchmargin.ActionOK {
		lp.Margin = &pigtail.LaunchMargin{Decision: dec.Action, Requested: dec.Requested.ID, Final: dec.Final.ID, Warning: dec.Warning}
	}
	return nil
}

// refuseOverMargin writes the guard's refusal: 409 LAUNCH_OVER_MARGIN, the
// bounded message (cell id and release time).
func refuseOverMargin(w http.ResponseWriter, r *http.Request, resp Responder, err error) {
	resp.Error(w, r, http.StatusConflict, pigtail.ErrLaunchOverMargin, err.Error(), nil)
}

// LaunchCell is the session launcher's cell for a scope
// (sub_x000000000000015): the guard's pick (GuardScope, no force). A refusal
// is returned as is (*launchmargin.OverMarginError). When the guard cannot
// measure (no table, rotation off, no candidate), the scope's lowest-rank
// cell runs the launch, traced unmeasured; a scope with no cell is
// launch.ErrNoCell.
func LaunchCell(svc *Services) func(sc string) (claudeprofile.Cell, error) {
	return func(sc string) (claudeprofile.Cell, error) {
		d := svc.marginDeps()
		c, dec, err := launchmargin.GuardScope(d, sc, false)
		var om *launchmargin.OverMarginError
		if errors.As(err, &om) {
			launchmargin.Record(svc.marginSinks(sc), dec)
			return c, err
		}
		if err == nil {
			launchmargin.Record(svc.marginSinks(sc), dec)
			return c, nil
		}
		fb, ok := fallbackCell(d.Cells, sc)
		token := strings.TrimPrefix(dec.Margin, "unmeasured ")
		launchmargin.Record(svc.marginSinks(sc), launchmargin.Unmeasured(d, fb, token))
		if !ok {
			return claudeprofile.Cell{}, launch.ErrNoCell
		}
		return fb, nil
	}
}

// fallbackCell is the scope's cell of lowest Rank (no rank last, then id).
func fallbackCell(tbl *claudeprofile.Table, sc string) (claudeprofile.Cell, bool) {
	if tbl == nil {
		return claudeprofile.Cell{}, false
	}
	want := scope.Canonical(sc)
	var cells []claudeprofile.Cell
	for _, c := range tbl.Cells {
		if scope.Canonical(c.Scope) == want {
			cells = append(cells, c)
		}
	}
	if len(cells) == 0 {
		return claudeprofile.Cell{}, false
	}
	sort.Slice(cells, func(i, j int) bool {
		a, b := cells[i], cells[j]
		switch {
		case a.Rank != nil && b.Rank != nil && *a.Rank != *b.Rank:
			return *a.Rank < *b.Rank
		case (a.Rank == nil) != (b.Rank == nil):
			return a.Rank != nil
		}
		return a.ID < b.ID
	})
	return cells[0], true
}

// StaffingMargin is the staffing scheduler's peek at the launch margin of a
// scope (GuardScope, no force); nothing is traced. A refusal closes the gate
// with the API text; so does a margin it cannot measure (no cell table,
// rotation off, no candidate nor snapshot), "unmeasured: <why>" — the
// scheduler never launches on a margin it did not read.
func StaffingMargin(svc *Services) func(scope string) (bool, string) {
	return func(sc string) (bool, string) {
		_, _, err := launchmargin.GuardScope(svc.marginDeps(), sc, false)
		var om *launchmargin.OverMarginError
		switch {
		case errors.As(err, &om):
			return false, om.APIMessage()
		case err != nil:
			return false, "unmeasured: " + err.Error()
		}
		return true, ""
	}
}

// LaunchCellStaffing is the launcher's cell for a Staffing launch: the guard
// with the weekly budget (GuardScopeStaffing), traced. Every error is returned
// as is; unlike LaunchCell there is no fallback cell: what is not measured
// closes.
func LaunchCellStaffing(svc *Services) func(sc string) (claudeprofile.Cell, error) {
	return func(sc string) (claudeprofile.Cell, error) {
		c, dec, err := launchmargin.GuardScopeStaffing(svc.marginDeps(), sc)
		launchmargin.Record(svc.marginSinks(sc), dec)
		return c, err
	}
}

// StaffingTokenBudget is the staffing scheduler's peek at the weekly budget of
// a scope (GuardScopeStaffing); nothing is traced. A refusal closes the gate
// with the API text; what it cannot measure (no cell table, rotation off, no
// candidate) closes it with "unmeasured: <why>".
func StaffingTokenBudget(svc *Services) func(scope string) (bool, string) {
	return func(sc string) (bool, string) {
		_, _, err := launchmargin.GuardScopeStaffing(svc.marginDeps(), sc)
		var (
			ob *launchmargin.OverBudgetError
			ub *launchmargin.UnmeasuredBudgetError
			om *launchmargin.OverMarginError
		)
		switch {
		case err == nil:
			return true, ""
		case errors.As(err, &ob):
			return false, ob.APIMessage()
		case errors.As(err, &ub):
			return false, ub.APIMessage()
		case errors.As(err, &om):
			return false, om.APIMessage()
		}
		return false, "unmeasured: " + err.Error()
	}
}
