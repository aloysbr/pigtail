package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/spf13/cobra"
)

// runtimeDaemon is the daemon side of the lease (pkg/pigtail, /runtime/*).
type runtimeDaemon interface {
	RuntimeClaim(ctx context.Context) (pigtail.RuntimeClaimView, error)
	ClaimRuntime(ctx context.Context, req pigtail.RuntimeClaimRequest) (pigtail.RuntimeClaimView, error)
	ReleaseRuntime(ctx context.Context, session string, force bool) (string, error)
}

// runtimeDeps are the seams of `pigtail runtime …`.
type runtimeDeps struct {
	dir     string
	now     func() time.Time
	session string // CLAUDE_CODE_SESSION_ID; "" = the user outside Claude Code
	cwd     string
	head    func(ctx context.Context, dir string) (branch, sha string, dirty bool, err error)
	resolve func(addr string) (id, label, labelScope string, err error)
	self    func(sid string) (pid int, configDir string)
	alive   runtimeclaim.AliveFunc
	reader  func() (scope.Scope, error)
	daemon  runtimeDaemon
	out     io.Writer
	errw    io.Writer
}

// runtimeClaimDir: the lease lives next to maintenance.json, same rule.
func runtimeClaimDir(getenv func(string) string) string { return maintenanceDir(getenv) }

// isDaemonDown: the socket refuses or does not exist — nothing answered.
func isDaemonDown(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

// isOldDaemon: a pigtaild older than /runtime/* answers chi's plain-text 404
// (an *HTTPStatusError); a JSON envelope (*APIError, NOT_FOUND included) is
// a daemon that has the routes.
func isOldDaemon(err error) bool {
	var st *pigtail.HTTPStatusError
	return errors.As(err, &st) && st.Status == 404
}

func (d runtimeDeps) readerScope() scope.Scope {
	if d.reader != nil {
		if r, err := d.reader(); err == nil {
			return r
		}
	}
	return scope.Default()
}

func (d runtimeDeps) path() string { return filepath.Join(d.dir, runtimeclaim.FileName) }

// holderName names the holder as the reader may see it (D23).
func holderName(st runtimeclaim.State, reader scope.Scope) string {
	if st.Session == "" {
		return humanName() + " outside Claude Code"
	}
	return maintenance.SessionName(st.Session, st.Label, st.LabelScope, reader)
}

func sha12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// holderLine is the one line that names a lease and its holder.
func holderLine(st runtimeclaim.State, lv runtimeclaim.Liveness, reader scope.Scope, staleHint string) string {
	s := fmt.Sprintf("runtime held by %s since %s — branch %s @ %s", holderName(st, reader), st.Since.Local().Format("15:04"), st.Branch, sha12(st.SHA))
	if st.Dirty {
		s += " dirty"
	}
	if st.PR > 0 {
		s += fmt.Sprintf(", PR #%d", st.PR)
	}
	s += " — " + st.Reason
	if lv == runtimeclaim.Stale {
		s += " — holder gone (stale): " + staleHint
	}
	return s
}

const (
	takeOverHint = "pigtail runtime claim --take-over (the user only, from a terminal outside Claude Code)"
	forceHint    = "pigtail runtime release --force (the user only, from a terminal outside Claude Code)"
)

// heldRefusal turns a 409 (APIError with details) or a direct HeldError /
// NotHolderError into the refusal that names the holder — read from the file:
// the 409 carries no label, branch nor sha.
func (d runtimeDeps) heldRefusal(err error, hint string) (error, bool) {
	var held *runtimeclaim.HeldError
	var nh *runtimeclaim.NotHolderError
	var api *pigtail.APIError
	switch {
	case errors.As(err, &held):
		return errors.New(holderLine(held.Cur, held.Liveness, d.readerScope(), hint)), true
	case errors.As(err, &nh):
		return errors.New(holderLine(nh.Cur, nh.Liveness, d.readerScope(), hint)), true
	case errors.As(err, &api) && api.Code == pigtail.ErrConflict:
		id, _ := api.Details["id"].(string)
		sess, _ := api.Details["session"].(string)
		lv, _ := api.Details["liveness"].(string)
		st, ok, lerr := runtimeclaim.Load(d.dir)
		if lerr != nil || !ok || st.ID != id {
			return fmt.Errorf("runtime held by session %s (claim %s, %s) — %s no longer names it", sess, id, lv, d.path()), true
		}
		return errors.New(holderLine(st, runtimeclaim.Liveness(lv), d.readerScope(), hint)), true
	}
	return nil, false
}

// liftExpired lifts the lease when its deadline has passed, for a path that
// reads or writes the file itself (the daemon did not answer for this
// directory): one stderr line, never stdout. An error says nothing — the Load
// or Claim that follows reports an unreadable file.
func (d runtimeDeps) liftExpired() bool {
	exp, lifted, err := runtimeclaim.Expire(d.dir, d.now())
	if err != nil || !lifted {
		return false
	}
	fmt.Fprintf(d.errw, "runtime claim %s of %s expired at %s (4 h without a renewing pigtail runtime claim) — lifted in %s\n",
		exp.Lease.ID, holderName(exp.Lease, d.readerScope()), exp.At.Local().Format("15:04"), d.path())
	return true
}

// deadlineSuffix is " — expires HH:MM", empty for a view without a deadline.
func deadlineSuffix(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " — expires " + t.Local().Format("15:04")
}

func runRuntimeClaim(d runtimeDeps, reason string, pr int, takeOver bool) error {
	sid := d.session
	if sid != "" && !maintenance.ValidSession(sid) {
		return fmt.Errorf("CLAUDE_CODE_SESSION_ID is not a session id (%q)", sid)
	}
	if reason == "" || len(reason) > runtimeclaim.MaxReasonBytes {
		return errors.New("--reason is required (≤ 500 bytes)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	branch, sha, dirty, err := d.head(ctx, d.cwd)
	if err != nil {
		return fmt.Errorf("not a git checkout (%s): %w — run it from the checkout whose build is installed", d.cwd, err)
	}
	req := pigtail.RuntimeClaimRequest{Session: sid, Branch: branch, SHA: sha, Dirty: dirty, PR: pr, Reason: reason, TakeOver: takeOver}
	if sid != "" {
		if id, label, sc, err := d.resolve(sid); err == nil && id == sid && label != "" {
			req.Label, req.LabelScope = label, sc
		}
		req.PID, req.ConfigDir = d.self(sid)
	}
	v, err := d.daemon.ClaimRuntime(ctx, req)
	if direct := directReason(err); direct != "" {
		d.liftExpired()
		st, derr := runtimeclaim.Claim(d.dir, runtimeclaim.State{Reason: req.Reason, PR: req.PR, Session: req.Session, Label: req.Label,
			LabelScope: req.LabelScope, PID: req.PID, ConfigDir: req.ConfigDir, Branch: req.Branch, SHA: req.SHA, Dirty: req.Dirty},
			takeOver, d.alive, d.now())
		if derr != nil {
			if ref, ok := d.heldRefusal(derr, takeOverHint); ok {
				return ref
			}
			return fmt.Errorf("runtime claim %s: %w", d.path(), derr)
		}
		fmt.Fprintf(d.errw, "%s — runtime claim written to %s directly\n", direct, d.path())
		v.ID, v.ExpiresAt = st.ID, st.Deadline()
		err = nil
	}
	if err != nil {
		if ref, ok := d.heldRefusal(err, takeOverHint); ok {
			return ref
		}
		return fmt.Errorf("runtime claim: %w", err)
	}
	line := fmt.Sprintf("runtime claimed: %s — branch %s @ %s", v.ID, branch, sha12(sha))
	if dirty {
		line += " dirty"
	}
	if pr > 0 {
		line += fmt.Sprintf(", PR #%d", pr)
	}
	fmt.Fprintln(d.out, line+" — "+reason+deadlineSuffix(v.ExpiresAt))
	return nil
}

// directReason: why the CLI writes the file itself — the daemon is down, or
// older than /runtime/*; "" = the daemon answered for itself.
func directReason(err error) string {
	switch {
	case err == nil:
		return ""
	case isOldDaemon(err):
		return "pigtaild has no /runtime routes (older build)"
	case isDaemonDown(err):
		return "pigtaild unreachable"
	}
	return ""
}

func runRuntimeRelease(d runtimeDeps, force bool) error {
	sid := d.session
	if sid != "" && !maintenance.ValidSession(sid) {
		return fmt.Errorf("CLAUDE_CODE_SESSION_ID is not a session id (%q)", sid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	id, err := d.daemon.ReleaseRuntime(ctx, sid, force)
	if direct := directReason(err); direct != "" {
		lifted := d.liftExpired()
		st, ok, derr := runtimeclaim.Release(d.dir, sid, force, d.alive, d.now())
		if derr != nil {
			if ref, ok := d.heldRefusal(derr, forceHint); ok {
				return ref
			}
			return fmt.Errorf("runtime release %s: %w", d.path(), derr)
		}
		if ok {
			fmt.Fprintf(d.errw, "%s — runtime claim released in %s directly\n", direct, d.path())
			id = st.ID
		} else if lifted {
			fmt.Fprintf(d.errw, "%s — lease expired and lifted, nothing else to release\n", direct)
			id = ""
		} else {
			fmt.Fprintf(d.errw, "%s — no lease was held, nothing written or released\n", direct)
			id = ""
		}
		err = nil
	}
	if err != nil {
		if ref, ok := d.heldRefusal(err, forceHint); ok {
			return ref
		}
		return fmt.Errorf("runtime release: %w", err)
	}
	if id == "" {
		fmt.Fprintln(d.out, "no runtime claim")
		return nil
	}
	fmt.Fprintf(d.out, "runtime claim %s released at %s\n", id, d.now().Local().Format("15:04"))
	return nil
}

func runRuntimeStatus(d runtimeDeps, asJSON bool) error {
	// A reachable daemon reading another directory is told (A8); a daemon
	// down or older than /runtime/* is not a warning: the file is the truth.
	// Whatever did not answer for this directory, the CLI lifts an expired
	// lease itself (stderr only: stdout stays one JSON object).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v, derr := d.daemon.RuntimeClaim(ctx)
	sameDir := derr == nil && canonDir(v.Dir) == canonDir(d.dir)
	if derr == nil && !sameDir {
		fmt.Fprintf(d.errw, "warning: pigtaild reads the runtime claim in %s, this CLI in %s — align PIGTAIL_MAINTENANCE_DIR / PIGTAIL_SOCKET\n", v.Dir, d.dir)
	}
	// A daemon that has the routes but no expires_at is older: no answer.
	if !sameDir || (v.Present && v.ExpiresAt.IsZero() && v.Unreadable == "") {
		d.liftExpired()
	}
	st, ok, err := runtimeclaim.Load(d.dir)
	if err != nil {
		if asJSON {
			_ = json.NewEncoder(d.out).Encode(map[string]any{"dir": d.dir, "held": false, "unreadable": d.path() + ": " + err.Error()})
		}
		return withExitCode(2, fmt.Errorf("runtime claim unreadable: %s: %w", d.path(), err))
	}
	reader := d.readerScope()
	lv := st.Liveness(d.alive)
	if asJSON {
		out := map[string]any{"dir": d.dir, "held": ok}
		if ok {
			out["id"], out["session"], out["name"], out["branch"], out["sha"] = st.ID, st.Session, holderName(st, reader), st.Branch, st.SHA
			out["dirty"], out["pr"], out["reason"], out["since"], out["liveness"] = st.Dirty, st.PR, st.Reason, st.Since, string(lv)
			out["expires_at"] = st.Deadline()
		}
		return json.NewEncoder(d.out).Encode(out)
	}
	if !ok {
		fmt.Fprintln(d.out, "no runtime claim")
		return nil
	}
	fmt.Fprintln(d.out, holderLine(st, lv, reader, takeOverHint))
	fmt.Fprintf(d.out, "  claim %s, liveness %s, expires %s (%s)\n", st.ID, lv, st.Deadline().Local().Format("15:04"), d.path())
	return nil
}

// runtimeCmd is `pigtail runtime claim|release|status` (D3 of
// sub_x000000000000005). No scopedRead/scopedWrite annotation: none of them
// goes through newScopedClient — the /runtime/* routes are classNoSubject,
// and the holder's label is rendered here, per the reader's scope (D23).
// --take-over and release --force are the user's (D6), enforced by the caller's
// guard, not here: the CLI does not detect Claude.
func runtimeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "runtime", Short: "Lease on the installed pigtail runtime (binaries, pigtaild, its plist) while a session installs a build"}
	live := func() runtimeDeps {
		cwd, _ := os.Getwd()
		return runtimeDeps{
			dir: runtimeClaimDir(os.Getenv), now: time.Now, session: os.Getenv("CLAUDE_CODE_SESSION_ID"), cwd: cwd,
			head:    gitbackend.HeadInfo,
			resolve: resolveMaintenancePeer,
			self:    selfSession,
			alive:   claudesession.SessionAlive,
			reader: func() (scope.Scope, error) {
				sc, err := callerScope()
				if err != nil {
					return "", err
				}
				return scope.Parse(sc)
			},
			daemon: newClient(), out: os.Stdout, errw: os.Stderr,
		}
	}
	var reason string
	var pr int
	var takeOver, force, asJSON bool
	claim := &cobra.Command{Use: "claim", Short: "Hold the installed runtime (same session: update)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runRuntimeClaim(live(), reason, pr, takeOver) }}
	claim.Flags().StringVar(&reason, "reason", "", "why (≤ 500 bytes)")
	claim.Flags().IntVar(&pr, "pr", 0, "the PR whose build is installed")
	claim.Flags().BoolVar(&takeOver, "take-over", false, "take over a stale holder (the user only)")
	release := &cobra.Command{Use: "release", Short: "Release the installed runtime", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runRuntimeRelease(live(), force) }}
	release.Flags().BoolVar(&force, "force", false, "release a stale holder's claim, or any as the user outside Claude Code (the user only)")
	status := &cobra.Command{Use: "status", Short: "Show who holds the installed runtime (reads the file, daemon or not)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runRuntimeStatus(live(), asJSON) }}
	status.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.AddCommand(claim, release, status)
	return cmd
}

// selfSession locates the caller's Claude process (pid, config dir) for the
// liveness check; not found → 0, "" (liveness unknown).
func selfSession(sid string) (int, string) {
	tbl, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil {
		return 0, ""
	}
	live, err := claudesession.LiveSessions(tbl.ConfigDirs())
	if err != nil {
		return 0, ""
	}
	for _, l := range live {
		if l.SessionID == sid {
			return l.PID, l.ConfigDir
		}
	}
	return 0, ""
}
