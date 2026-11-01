package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/spf13/cobra"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// maintenanceDir is where the maintenance state lives — one rule in Go, bash
// and python (Global Constraints): $PIGTAIL_MAINTENANCE_DIR, else the
// directory of --socket / $PIGTAIL_SOCKET, else $XDG_CONFIG_HOME/pigtail,
// else ~/.config/pigtail. Every variable is read through getenv (never
// os.Getenv behind the caller's back), so tests and hooks inject theirs.
func maintenanceDir(getenv func(string) string) string {
	if d := getenv("PIGTAIL_MAINTENANCE_DIR"); d != "" {
		return d
	}
	if flagSocket != "" {
		return filepath.Dir(flagSocket)
	}
	if s := getenv("PIGTAIL_SOCKET"); s != "" {
		return filepath.Dir(s)
	}
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pigtail")
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "pigtail")
	}
	return setup.ConfigDir()
}

type maintDeps struct {
	dir     string
	now     func() time.Time
	resolve func(addr string) (id, label, labelScope string, err error)
	// reader is the caller's scope: who may read a recorded session label
	// (D22). The same view as resolve (D21).
	reader      func() (scope.Scope, error)
	daemonState func() (pigtail.MaintenanceView, error) // GET /maintenance
	sidebar     func() (enabled bool, err error)
	out, errw   io.Writer
	session     string // CLAUDE_CODE_SESSION_ID of the caller; "" = the user outside Claude Code
}

// maintCaller validates the caller's identity once, for on and off: the
// CLAUDE_CODE_SESSION_ID of a Claude session (a `!` typed in Claude Code
// included, D17), or "" for the user outside Claude Code.
func maintCaller(d maintDeps) (string, error) {
	if d.session != "" && !maintenance.ValidSession(d.session) {
		return "", fmt.Errorf("CLAUDE_CODE_SESSION_ID is not a session id (%q)", d.session)
	}
	return d.session, nil
}

// runMaintenanceOn sets a maintenance. The calling session becomes its owner
// (D16): exempt from the freeze and, with the user, the only one that lifts it.
func runMaintenanceOn(d maintDeps, reason, until string, freeze bool, exempt string) error {
	sid, err := maintCaller(d)
	if err != nil {
		return err
	}
	if reason == "" || len(reason) > maintenance.MaxReasonBytes {
		return errors.New("--reason is required (≤ 500 bytes)")
	}
	cur, ok, err := maintenance.Load(d.dir)
	if err != nil { // D8: never stack a new state on an unreadable one
		if sid != "" { // T1: the repair is the user', outside Claude Code
			return errors.New(maintenance.UnreadableForSession(d.dir, err))
		}
		return fmt.Errorf("%s — run `pigtail maintenance off` first", maintenance.UnreadableMessage(d.dir, err))
	}
	if ok && cur.On {
		return fmt.Errorf("maintenance already on since %s (%s), %s", cur.Since.Local().Format("15:04"), cur.ID, cur.SetByEN(readerOf(d)))
	}
	now := d.now()
	st := maintenance.State{ID: maintenance.NewID(), On: true, Reason: reason, Freeze: freeze, Since: now}
	if sid != "" {
		st.Owner, st.OwnerLabel = sid, sid
		if id, label, sc, err := d.resolve(sid); err == nil && id == sid && label != "" {
			st.OwnerLabel, st.OwnerScope = label, sc
		}
	}
	if until != "" {
		u, err := maintenance.ParseUntil(until, now)
		if err != nil {
			return err
		}
		st.Until = &u
	}
	exemptIsOwner := false
	if exempt != "" {
		id, label, sc, err := d.resolve(exempt)
		if err != nil {
			return fmt.Errorf("--exempt %s: %w", exempt, err)
		}
		if sid != "" && id == sid { // T2: the owner is already exempt
			exemptIsOwner = true
		} else {
			st.Exempt, st.ExemptLabel, st.ExemptScope = id, label, sc
		}
	}
	if err := maintenance.Save(d.dir, st); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Join(d.dir, maintenance.FileName), err)
	}
	printSummary(d.out, st, now, readerOf(d))
	if exemptIsOwner {
		fmt.Fprintln(d.out, "  exempt (owner, already exempt)")
	}
	printSidebar(d)
	return notify(d)
}

// runMaintenanceOff lifts the maintenance — only for its owner session (its
// subagents carry the same id) or for the user outside Claude Code (CanLift).
// A refusal writes nothing and does not call the daemon.
func runMaintenanceOff(d maintDeps) error {
	sid, err := maintCaller(d)
	if err != nil {
		return err
	}
	now := d.now()
	st, ok, err := maintenance.Load(d.dir)
	id := st.ID
	byID, byLabel, byScope := "", "", ""
	switch {
	case err != nil && sid != "": // T1: who set it is unreadable too
		return errors.New(maintenance.UnreadableForSession(d.dir, err))
	case err != nil:
		// D8 repair: the unreadable file is deleted like any other state.
		fmt.Fprintf(d.errw, "warning: %s — deleting it\n", maintenance.UnreadableMessage(d.dir, err))
		id = maintenance.RawTokenID(d.dir)
	case !ok || !st.On:
		return errors.New("no maintenance in progress")
	case !st.CanLift(sid):
		return liftRefusal(st, readerOf(d))
	case sid != "":
		byID, byLabel, byScope = st.Owner, st.OwnerLabel, st.OwnerScope
	}
	if err := maintenance.Lift(d.dir, id, now, byID, byLabel, byScope); err != nil {
		return fmt.Errorf("lift %s: %w", filepath.Join(d.dir, maintenance.FileName), err)
	}
	// D4: the sessions it refused read Blocked no more. A marker left behind
	// is inert: its id is no longer a maintenance in progress.
	if err := maintenance.ClearFrozen(d.dir); err != nil {
		fmt.Fprintf(d.errw, "warning: frozen markers not cleared: %v\n", err)
	}
	fmt.Fprintf(d.out, "maintenance %s lifted at %s — state file removed\n", id, now.Local().Format("15:04"))
	return notify(d)
}

func runMaintenanceStatus(d maintDeps, asJSON bool) error {
	st, ok, err := maintenance.Load(d.dir)
	if err != nil {
		return errors.New(maintenance.UnreadableMessage(d.dir, err))
	}
	reader := readerOf(d)
	if asJSON {
		return json.NewEncoder(d.out).Encode(map[string]any{"present": ok, "state": stateFor(st, reader), "phase": st.Phase(d.now()), "dir": d.dir})
	}
	if !ok {
		fmt.Fprintln(d.out, "no maintenance")
		return nil
	}
	if !st.On {
		fmt.Fprintf(d.out, "no maintenance (last: %s lifted at %s)\n", st.ID, st.LiftedAt.Local().Format("02/01 15:04"))
		return nil
	}
	printSummary(d.out, st, d.now(), reader)
	printSidebar(d)
	return nil
}

// readerOf is the caller's scope for naming sessions (D22); unreadable →
// the default scope, the narrowest view: the output must still go out.
func readerOf(d maintDeps) scope.Scope {
	if d.reader != nil {
		if r, err := d.reader(); err == nil {
			return r
		}
	}
	return scope.Default()
}

// stateFor is the state as reader may read it (status --json, D22): the
// label of a session reader cannot see becomes its short id, and its scope
// is emptied — the name of another scope does not go out either (D19).
func stateFor(st maintenance.State, reader scope.Scope) maintenance.State {
	if st.OwnerLabel != "" && !maintenance.LabelVisible(st.OwnerLabel, st.OwnerScope, reader) {
		st.OwnerLabel, st.OwnerScope = maintenance.ShortID(st.Owner), ""
	}
	if st.ExemptLabel != "" && !maintenance.LabelVisible(st.ExemptLabel, st.ExemptScope, reader) {
		st.ExemptLabel, st.ExemptScope = maintenance.ShortID(st.Exempt), ""
	}
	return st
}

// liftRefusal is the refusal of `off` to a session that does not own the
// maintenance (S15); the owner is named as reader may see it (D23).
func liftRefusal(st maintenance.State, reader scope.Scope) error {
	if st.Owner == "" {
		return fmt.Errorf("maintenance %s was set by %s — only they lift it, outside Claude Code — tell them", st.ID, humanName())
	}
	return fmt.Errorf("maintenance %s was set by session %s — only that session, or %s outside Claude Code, can lift it — if it is gone, tell them; do not work around this",
		st.ID, ownerRef(st, reader), humanName())
}

// sessionRef names a recorded session for reader (D22): « label [id] » when
// reader may see it and the label is not the id itself (no « <uuid> [<uuid>]
// »); the full id when the label is the id; else the short id alone.
func sessionRef(id, label, labelScope string, reader scope.Scope) string {
	switch {
	case label != "" && label != id && maintenance.LabelVisible(label, labelScope, reader):
		return label + " [" + id + "]"
	case label == "" || label == id:
		if maintenance.LabelVisible(label, labelScope, reader) {
			return id
		}
	}
	return maintenance.ShortID(id)
}

// ownerRef names the owner session for reader (sessionRef).
func ownerRef(st maintenance.State, reader scope.Scope) string {
	return sessionRef(st.Owner, st.OwnerLabel, st.OwnerScope, reader)
}

func printSummary(w io.Writer, st maintenance.State, now time.Time, reader scope.Scope) {
	fmt.Fprintf(w, "maintenance %s ON since %s — %s\n", st.ID, st.Since.Local().Format("02/01 15:04"), st.Reason)
	if st.Until != nil {
		late := ""
		if st.Late(now) {
			late = " — LATE"
		}
		fmt.Fprintf(w, "  until  %s%s (shown only, never expires)\n", st.Until.Local().Format("02/01 15:04"), late)
	}
	freeze := "off"
	if st.Freeze {
		freeze = "ON (" + maintenance.FreezeSummary() + ")"
	}
	fmt.Fprintf(w, "  freeze %s\n", freeze)
	if st.Owner != "" {
		fmt.Fprintf(w, "  owner  %s\n", ownerRef(st, reader))
	} else {
		fmt.Fprintf(w, "  owner  %s (outside Claude Code)\n", humanName())
	}
	if st.Exempt != "" {
		fmt.Fprintf(w, "  exempt %s\n", sessionRef(st.Exempt, st.ExemptLabel, st.ExemptScope, reader))
	}
}

// notify checks that the daemon reads the state just written (the hook
// delivers the notice from the file, D12). Daemon
// unreachable: warning, exit 0 — nothing writes through it, and it reads the
// file when it comes back. Daemon UP but reading another state: error, exit 1
// — it would accept writes unfrozen and show no pill.
func notify(d maintDeps) error {
	v, err := d.daemonState()
	var api *pigtail.APIError
	var status *pigtail.HTTPStatusError
	switch {
	case errors.As(err, &api):
		// JSON error envelope: e.g. 500 "maintenance not wired".
		return fmt.Errorf("daemon answers but does not apply maintenance (%s): freeze and pill NOT active — restart pigtaild from this build", api.Code)
	case errors.As(err, &status):
		// Any other HTTP status — chi's plain-text "404 page not found" from a
		// pigtaild older than this build: the daemon answers, it does not apply.
		return fmt.Errorf("daemon answers but does not apply maintenance (HTTP %d): freeze and pill NOT active — restart pigtaild from this build", status.Status)
	}
	if err != nil { // transport only: no socket, connection refused, timeout
		fmt.Fprintf(d.errw, "warning: daemon unreachable (%v) — freeze and pill take effect when pigtaild reads %s; sessions get the notice at their next action\n", err, d.dir)
		return nil
	}
	// One structural criterion, for on and off alike: the daemon reads the
	// directory this CLI just wrote in (symlinks resolved on both sides).
	if canonDir(v.Dir) != canonDir(d.dir) {
		return fmt.Errorf("daemon reads %s: freeze and pill NOT active (this CLI wrote in %s) — align PIGTAIL_MAINTENANCE_DIR / PIGTAIL_SOCKET between this shell and pigtaild",
			v.Dir, d.dir)
	}
	freeze := "off"
	if v.Freeze {
		freeze = "on"
	}
	fmt.Fprintf(d.out, "  daemon reads %s: freeze %s\n", v.Dir, freeze)
	return nil
}

// canonDir resolves symlinks and cleans; an unresolvable path is compared as
// its absolute clean form.
func canonDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	if a, err := filepath.Abs(p); err == nil {
		return filepath.Clean(a)
	}
	return filepath.Clean(p)
}

func printSidebar(d maintDeps) {
	on, err := d.sidebar()
	switch {
	case err != nil:
		fmt.Fprintf(d.out, "  sidebar pill: unknown (daemon unreachable: %v)\n", err)
	case !on:
		fmt.Fprintln(d.out, "  sidebar pill: off (pigtail cmux sidebar on)")
	default:
		fmt.Fprintln(d.out, "  sidebar pill: on")
	}
}

func maintenanceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "maintenance", Short: "System maintenance notice and freeze (set by a session or by the user — lifted by its owner or by the user)"}
	live := func() maintDeps {
		return maintDeps{
			dir: maintenanceDir(os.Getenv), now: time.Now, out: os.Stdout, errw: os.Stderr,
			session: os.Getenv("CLAUDE_CODE_SESSION_ID"),
			resolve: resolveMaintenancePeer,
			reader: func() (scope.Scope, error) {
				sc, err := callerScope()
				if err != nil {
					return "", err
				}
				return scope.Parse(sc)
			},
			daemonState: func() (pigtail.MaintenanceView, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				v, err := newClient().Maintenance(ctx)
				if err != nil {
					return pigtail.MaintenanceView{}, err
				}
				return *v, nil
			},
			sidebar: func() (bool, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				st, err := newClient().CmuxSidebar(ctx)
				if err != nil {
					return false, err
				}
				return st.Enabled, nil
			},
		}
	}
	var reason, until, exempt string
	var freeze, asJSON bool
	on := &cobra.Command{Use: "on", Short: "Put the system in maintenance", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runMaintenanceOn(live(), reason, until, freeze, exempt) }}
	on.Flags().StringVar(&reason, "reason", "", "why (shown to every session, ≤ 500 bytes)")
	on.Flags().StringVar(&until, "until", "", "planned end, shown only: 2h, 18:30, 2026-10-05T09:00")
	on.Flags().BoolVar(&freeze, "freeze", false, "refuse subject writes, pigtail new, pigtail dispatch and pigtail session spawn (the notice also names settings.yaml maintenance.freeze_also_refuses)")
	on.Flags().StringVar(&exempt, "exempt", "", "another session not frozen (name, title or id); the session that runs it is always exempt")
	off := &cobra.Command{Use: "off", Short: "Lift the maintenance", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runMaintenanceOff(live()) }}
	status := &cobra.Command{Use: "status", Short: "Show the maintenance state", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runMaintenanceStatus(live(), asJSON) }}
	status.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.AddCommand(on, off, status)
	return cmd
}

// resolveMaintenancePeer resolves --exempt (D21) with the caller's own view:
// the master cell sees every session, any other cell only its scope's, and
// outside any cell resolveScope's default (or --scope / PIGTAIL_SCOPE). A full
// session id is always accepted as is — that is how a session of another
// scope gets exempted — and its label is read only from the caller's view, so
// no other scope's label reaches the command's output; the label it records
// is shown later, in status and in every session's notice, only to readers
// who can see that session (D22). labelScope is the labelled session's scope
// (the master's when it has none, D17), "" when the label is the id.
func resolveMaintenancePeer(addr string) (id, label, labelScope string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a := strings.TrimSpace(addr)
	if uuidRe.MatchString(a) {
		if sc, err := callerScope(); err == nil {
			if ps, err := newClient().WithScope(sc).Peers(ctx, false); err == nil {
				for _, p := range ps {
					if p.SessionID == a {
						return a, firstNonEmptyStr(p.Label, peers.Label(p)), labelScopeOf(p), nil
					}
				}
			}
		}
		return a, a, "", nil
	}
	sc, err := callerScope()
	if err != nil {
		return "", "", "", err
	}
	ps, err := newClient().WithScope(sc).Peers(ctx, false)
	if err != nil {
		return "", "", "", errors.New("daemon unreachable — pass the full session id (pigtail session ls)")
	}
	p, _, err := peers.ResolveLevel(ps, a)
	if err != nil {
		return "", "", "", err
	}
	return p.SessionID, firstNonEmptyStr(p.Label, peers.Label(p)), labelScopeOf(p), nil
}

// labelScopeOf is the scope a session's label belongs to: the session's own,
// canonical; a session with no scope is the master's (D17).
func labelScopeOf(p pigtail.PeerSession) string {
	if strings.TrimSpace(p.Scope) == "" {
		return string(scope.Master())
	}
	return string(scope.Canonical(p.Scope))
}
