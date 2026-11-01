package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// callerSession is the Claude session running this pigtail, as spawn and
// respawn both need it: read from the calling Claude process, never from this
// process's own environment.
type callerSession struct {
	Proc      claudesession.Proc
	Env       string // the calling Claude's rendered environment (processEnviron), read with cmux.EnvValue
	Cell      claudeprofile.Cell
	Cells     *claudeprofile.Table // the whole cell table, for a spawn's target cell
	Subject   pigtail.Subject
	SessionID string
	Perm      permDecision
}

// resolveCaller runs the shared guards: calling Claude, its environment, config
// dir = this process's, a cell, the subject and its scope, the session id and
// its fresh permission record, the strictest mode. cmd ("spawn" | "respawn")
// names the command in the guard texts; errf prefixes messages
// ("session spawn: " / "session respawn: "). It composes resolveCallerCell,
// readCallerSubject and resolveCallerPerm, in that order — respawn's refusal
// order depends on it.
func resolveCaller(ctx context.Context, c *pigtail.Client, cmd, subjectID string, crossScope bool,
	errf func(string, ...any) error) (callerSession, error) {
	cs, err := resolveCallerCell(cmd, errf)
	if err != nil {
		return cs, err
	}
	if err := readCallerSubject(ctx, c, &cs, subjectID, crossScope, errf); err != nil {
		return cs, err
	}
	if err := resolveCallerPerm(&cs, cmd, errf); err != nil {
		return cs, err
	}
	return cs, nil
}

// resolveCallerCell finds the calling Claude, reads its environment, checks
// its config dir is this process's and resolves the cell owning it.
func resolveCallerCell(cmd string, errf func(string, ...any) error) (callerSession, error) {
	var cs callerSession
	// The cell is the calling session's, read from its own process — never
	// from this pigtail's environment, which anyone can set on the command line.
	caller, found, err := spawnCallerClaude()
	if err != nil {
		return cs, retryable(retryShort, errf("find the calling Claude: %v", err))
	}
	if !found {
		return cs, errf("no Claude session above this pigtail — %s only runs from a Claude session's Bash tool", cmd)
	}
	cs.Proc = caller
	callerEnv, err := processEnviron(caller.PID)
	if err != nil {
		return cs, errf("cannot read the environment of the calling Claude (pid %d): %v — run %s from a Claude session of your own user, then retry", caller.PID, err, cmd)
	}
	cs.Env = callerEnv
	callerCfg, _ := cmux.EnvValue(callerEnv, "CLAUDE_CONFIG_DIR")
	if callerCfg == "" {
		callerCfg = claudeConfigDir(func(string) string { return "" })
	}
	if own := claudeConfigDir(os.Getenv); canonicalPath(own) != canonicalPath(callerCfg) {
		return cs, errf("this pigtail runs with CLAUDE_CONFIG_DIR=%s but the calling session (pid %d) with %s — the cell is the session's, refusing", own, caller.PID, callerCfg)
	}
	cells, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil {
		return cs, errf("%v", err)
	}
	cell, ok := cells.ByConfigDir(callerCfg)
	if !ok || !cellIDRe.MatchString(cell.ID) {
		return cs, errf("no Claude cell owns CLAUDE_CONFIG_DIR=%s — %s only runs from a Claude started on a cell's config dir (see docs/CONFIG.md § claude-cells.yaml)", callerCfg, cmd)
	}
	cs.Cell = cell
	cs.Cells = cells
	return cs, nil
}

// readCallerSubject reads the subject (404 → the non-master « not found ») and
// refuses another scope's subject without crossScope, against the caller's cell.
func readCallerSubject(ctx context.Context, c *pigtail.Client, cs *callerSession, subjectID string, crossScope bool,
	errf func(string, ...any) error) error {
	sub, err := c.Subject(ctx, subjectID)
	if err != nil {
		if isNotFound(err) {
			return errf("subject %s not found (a non-master cell never sees another scope's subjects) — check the id with pigtail subject list", subjectID)
		}
		return retryable(retryShort, errf("read subject %s: %v", subjectID, err))
	}
	if sub.Scope != "" && sub.Scope != cs.Cell.Scope && !crossScope {
		return errf("subject %s is %s, this session's cell %s is %s — pass --cross-scope only on the user's explicit OK", sub.ID, sub.Scope, cs.Cell.ID, cs.Cell.Scope)
	}
	cs.Subject = *sub
	return nil
}

// resolveCallerPerm reads the caller cell's default mode, the calling session's
// id and its fresh permission record, and decides the strictest mode. It reads
// the CALLER's cell: the session id and its default mode belong to the calling
// session, whatever cell a spawn targets.
func resolveCallerPerm(cs *callerSession, cmd string, errf func(string, ...any) error) error {
	def, err := spawnCellDefaultMode(cs.Cell.ConfigDir)
	if err != nil {
		return errf("%v", err)
	}
	sid, ok := spawnCallerSession(cs.Cell.ConfigDir, cs.Proc.PID)
	if !ok {
		return retryable(retryShort, errf("cannot tell the calling session's id (no %s/sessions/%d.json) — retry once the session has fully started; %s only runs from a live Claude session", cs.Cell.ConfigDir, cs.Proc.PID, cmd))
	}
	current, err := spawnPermissionStamp(sid)
	if err != nil {
		// readPermissionStamp's fix names spawn; a respawn refusal must never
		// tell the agent to re-run spawn.
		err = errors.New(strings.ReplaceAll(err.Error(), "pigtail session spawn", "pigtail session "+cmd))
		return errf("current permission mode unknown: %v", err)
	}
	cs.SessionID = sid
	if cs.Perm, err = resolvePermissionMode(cs.Proc.Args, def, current); err != nil {
		return errf("%v", err)
	}
	return nil
}
