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
	"sync"
	"text/tabwriter"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/relaunchfile"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/spf13/cobra"
)

// sessionRow is one Claude session, as seen from cmux plus the config dirs.
type sessionRow struct {
	SurfaceRef string
	SessionID  string
	Cwd        string
	CellID     string
	ConfigDir  string
	// Slug is the transcript's real project-directory name, as resolved by
	// claudesession.Find. It is captured here rather than derived from Cwd
	// later: Cwd comes from cmux.CwdFrom, which is empty when a binding
	// doesn't match the expected shape, and slugifying an empty cwd would
	// silently point at the wrong path.
	Slug string
}

// inventory walks cmux surfaces and reports the Claude sessions behind them.
//
// It keys on surfaces, never on running processes: a session that is not
// currently running still exists, still has a transcript, and still has to be
// moved. Enumerating processes silently skips those.
//
// A surface whose binding lookup fails is skipped here, same as a surface
// with no binding at all — inventory has no channel to report which is
// which. Callers that need to distinguish "no session" from "could not tell"
// (so a pane never goes silently missing) must call binding themselves and
// track the failures; see listSessions.
//
// A session whose transcript exists in two accounts at once is a different
// matter: it is not "no session", it is a session with no single answer, and
// claudesession.Find refuses to invent one. Those are returned as ambiguous
// rather than skipped, so no caller acts on a picture that quietly dropped a
// pane it could see perfectly well.
func inventory(refs []string, binding func(string) (string, error), tbl *claudeprofile.Table) (rows []sessionRow, ambiguous []error) {
	for _, ref := range refs {
		b, err := binding(ref)
		if err != nil || b == "" {
			continue
		}
		id := cmux.SessionIDFrom(b)
		if id == "" {
			continue
		}
		tr, ok, err := claudesession.Find(id, tbl.ConfigDirs())
		if err != nil {
			ambiguous = append(ambiguous, fmt.Errorf("%s: %w", ref, err))
			continue
		}
		if !ok {
			continue
		}
		cell, _ := tbl.ByConfigDir(tr.ConfigDir)
		rows = append(rows, sessionRow{
			SurfaceRef: ref,
			SessionID:  id,
			Cwd:        cmux.CwdFrom(b),
			CellID:     cell.ID,
			ConfigDir:  tr.ConfigDir,
			Slug:       tr.Slug,
		})
	}
	return rows, ambiguous
}

func sessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Inspect, spawn, respawn, move and hand over Claude Code sessions between accounts, say what one waits for",
	}
	cmd.AddCommand(sessionLsCmd())
	cmd.AddCommand(sessionMoveCmd())
	cmd.AddCommand(sessionHandoverCmd())
	cmd.AddCommand(sessionSpawnCmd())
	cmd.AddCommand(sessionRespawnCmd())
	cmd.AddCommand(sessionResumeCmd())
	cmd.AddCommand(sessionWaitCmd())
	cmd.AddCommand(sessionStandbyCmd())
	cmd.AddCommand(sessionPrioCmd())
	cmd.AddCommand(sessionPrioCheckCmd())
	cmd.AddCommand(sessionHandbacksCmd())
	cmd.AddCommand(sessionRotateCmd())
	cmd.AddCommand(sessionCostCmd())
	cmd.AddCommand(sessionGateCmd())
	return cmd
}

// trackedInventory builds the row list via inventory while recording which
// surfaces could not be read at all — a binding lookup error, distinct from a
// surface that simply has no binding (which inventory already treats as "no
// session" and skips silently). Shared by listSessions and sessionMoveCmd:
// both need to warn on an unreadable surface instead of acting on a partial
// picture in silence — with --all especially, an unreadable surface may be
// exactly the session that should have moved. Ambiguous sessions (a transcript
// hosted by two accounts at once) come back from inventory and are passed
// through for the same reason.
func trackedInventory(refs []string, binding func(string) (string, error), tbl *claudeprofile.Table) (rows []sessionRow, unreadable []string, ambiguous []error) {
	tracked := func(ref string) (string, error) {
		b, err := binding(ref)
		if err != nil {
			unreadable = append(unreadable, ref)
		}
		return b, err
	}
	rows, ambiguous = inventory(refs, tracked, tbl)
	return rows, unreadable, ambiguous
}

func sessionLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "ls",
		Short:       "List Claude sessions and the account each one runs on",
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			tbl, err := claudeprofile.Load(setup.ConfigDir())
			if err != nil {
				return err
			}
			refs, err := cmux.SurfaceRefs()
			if err != nil {
				return err
			}
			// The relay columns come back through the caller's scope: a work
			// cell does not see the perso subject another session works on.
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			rows := listSessions(refs, cmux.ResumeBinding, tbl, relayPeers(cmd.Context(), c, cmd.ErrOrStderr()), cmd.OutOrStdout(), cmd.ErrOrStderr())
			reportUnbound(cmd.ErrOrStderr(), rows, tbl)
			return nil
		},
	}
}

// listSessions runs the full `session ls` pipeline against already-fetched
// surface refs: it wraps binding to track which surfaces could not be read
// (distinct from a surface that simply has no binding), builds the row list
// via inventory, and renders both the table (to out) and the unreadable-
// surface warning (to warn).
//
// Split out from sessionLsCmd's RunE, and taking plain io.Writers instead of
// a *cobra.Command, precisely so a test can assert on both streams without a
// real cmux process — see cmd/pigtail/servers.go's printServers for the same
// pattern. A surface whose lookup fails must still be named on warn: this is
// the behaviour that keeps a pane's session from going invisible, so it has
// to be exercised directly rather than trusted to "it compiled".
//
// It returns the rows it printed, so the caller can tell which sessions cmux
// already knows about (see reportUnbound).
//
// peersByID, when non-nil, adds the relay's columns (subject, worktree, cmux
// title, native name, state, unread count) — see session_relay.go.
func listSessions(refs []string, binding func(string) (string, error), tbl *claudeprofile.Table, peersByID map[string]pigtail.PeerSession, out, warn io.Writer) []sessionRow {
	rows, unreadable, ambiguous := trackedInventory(refs, binding, tbl)

	if len(unreadable) > 0 {
		fmt.Fprintf(warn, "warning: could not read %d surface(s): %s\n", len(unreadable), strings.Join(unreadable, ", "))
	}
	for _, err := range ambiguous {
		fmt.Fprintf(warn, "warning: %v\n", err)
	}
	if peersByID != nil {
		printSessionsWide(out, rows, peersByID, time.Now(), dirExists)
	} else {
		printSessions(out, rows)
	}
	return rows
}

// printSessions renders the session table, or a one-line "nothing found"
// message when rows is empty.
func printSessions(w io.Writer, rows []sessionRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "no Claude session found")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PANE\tCELL\tSESSION\tCWD")
	for _, r := range rows {
		// The full 36-char id, not an 8-char prefix: `move --session` takes
		// nothing else (it validates a canonical UUID), so a truncated id
		// would mean the only two subcommands this feature ships do not
		// compose. CWD is the long column anyway.
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.SurfaceRef, r.CellID, r.SessionID, r.Cwd)
	}
	tw.Flush()
}

// validateMove rejects the two moves that produce a broken session.
//
// Cross-scope: a transcript carries references to skills and MCP servers that
// only exist in its scope. Moving it to another scope yields a session that
// looks fine and silently misses half its tools — refuse rather than degrade.
func validateMove(src, dst claudeprofile.Cell) error {
	if src.ID == dst.ID {
		return fmt.Errorf("session already runs on cell %s", dst.ID)
	}
	if src.Scope != dst.Scope {
		return fmt.Errorf("refusing cross-scope move: %s is %q, %s is %q",
			src.ID, src.Scope, dst.ID, dst.Scope)
	}
	return nil
}

// authenticated reports whether a Claude config dir has an account bound to it.
// Only the presence of oauthAccount is read; the file's contents never surface
// in output or errors.
func authenticated(configDir string) bool {
	b, err := os.ReadFile(filepath.Join(configDir, ".claude.json"))
	if err != nil {
		return false
	}
	var probe struct {
		OAuthAccount *struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	return probe.OAuthAccount != nil && probe.OAuthAccount.EmailAddress != ""
}

// sessionIDRe matches a canonical Claude session id: 8-4-4-4-12 lowercase hex
// digits, nothing else.
var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validSessionID reports whether id is a canonical UUID. claudesession.Find
// turns the id into a glob pattern (`projects/*/<id>.jsonl`); an id carrying
// glob metacharacters (*, ?, [, ]) would silently change what that pattern
// matches, so --session is rejected outright unless it is exactly a UUID.
func validSessionID(id string) bool {
	return sessionIDRe.MatchString(id)
}

func sessionMoveCmd() *cobra.Command {
	var all, dryRun, keepSource bool
	var from, session string

	cmd := &cobra.Command{
		Use:   "move <cell>",
		Short: "Move a Claude session to another account",
		Long: "Move the current pane's session — or every session of an account with --all —\n" +
			"to another cell: relocate its transcript, relaunch it, and rewrite the pane's\n" +
			"cmux resume binding.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			// Validate --session before it reaches any lookup: claudesession.Find
			// builds a glob pattern from it, so a value with glob metacharacters
			// would change match semantics rather than simply fail to match.
			if session != "" && !validSessionID(session) {
				return fmt.Errorf("invalid --session %q: expected a canonical UUID (8-4-4-4-12 lowercase hex)", session)
			}
			// --from only means anything with --all; silently ignoring it would
			// let a typo'd invocation move the wrong thing without any signal.
			if from != "" && !all {
				return errors.New("--from requires --all")
			}

			tbl, err := claudeprofile.Load(setup.ConfigDir())
			if err != nil {
				return err
			}
			dst, ok := tbl.Cell(args[0])
			if !ok {
				return fmt.Errorf("unknown cell %q", args[0])
			}
			if !authenticated(dst.ConfigDir) {
				return fmt.Errorf("cell %s is not authenticated (no account in %s)", dst.ID, dst.ConfigDir)
			}
			refs, err := cmux.SurfaceRefs()
			if err != nil {
				return err
			}
			rows, unreadable, ambiguous := trackedInventory(refs, cmux.ResumeBinding, tbl)
			if len(unreadable) > 0 {
				// Never act on a partial picture in silence: with --all especially,
				// an unreadable surface may be a session that should have moved.
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not read %d surface(s): %s\n", len(unreadable), strings.Join(unreadable, ", "))
			}
			for _, err := range ambiguous {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
			}

			// A session named explicitly may have no pane at all.
			if session != "" {
				tr, ok, err := claudesession.Find(session, tbl.ConfigDirs())
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("no transcript for session %s in any known cell", session)
				}
				src, ok := tbl.ByConfigDir(tr.ConfigDir)
				if !ok {
					// Discarding this ok let a zero Cell{} reach validateMove,
					// which then reported `refusing cross-scope move: is "",
					// mt is "work"` — a scope mismatch that does not exist. The
					// truth is that the cell table covers no such config dir.
					return fmt.Errorf("session %s lives in %s, which no cell in %s covers (add its cell — see docs/CONFIG.md § claude-cells.yaml)",
						session, tr.ConfigDir, claudeprofile.Filename)
				}
				for _, r := range rows {
					if r.SessionID == session {
						// A named single session is never wrapped with
						// withIncompletePicture — see moveResult.
						return moveResult(moveOne(out, cmd.ErrOrStderr(), r, dst, tbl, dryRun, keepSource), false, unreadable, ambiguous)
					}
				}
				return moveResult(moveDetached(out, cmd.ErrOrStderr(), tr, src, dst, dryRun, keepSource), false, unreadable, ambiguous)
			}

			self, err := callingSession()
			if err != nil {
				return err
			}
			targets, skipped, err := selectTargets(rows, all, from, tbl, self)
			if err != nil {
				return err
			}
			if skipped != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "skipping %s (%s): it is the session running this command (move it on its own, from its pane, without --all)\n", skipped.SessionID[:8], skipped.SurfaceRef)
			}
			if len(targets) == 0 {
				return errors.New("no session to move")
			}
			return moveResult(moveAll(out, cmd.ErrOrStderr(), targets, dst, tbl, dryRun, keepSource), all, unreadable, ambiguous)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "move every session of the source account, selected by walking cmux panes (a detached session needs --session instead)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen, change nothing")
	cmd.Flags().BoolVar(&keepSource, "keep-source", false, "keep the transcript in the source account")
	cmd.Flags().StringVar(&from, "from", "", "source cell (required with --all outside a cmux pane)")
	cmd.Flags().StringVar(&session, "session", "", "move this session id, even if it has no cmux pane")
	cmd.MarkFlagsMutuallyExclusive("session", "all")
	return cmd
}

// withIncompletePicture folds the surfaces the command could not resolve — a
// cmux binding lookup that failed, or a session hosted by two accounts at
// once — into the error a move returns, alongside the sessions that failed
// outright. Called only through moveResult, and only for --all; see there
// for why.
func withIncompletePicture(err error, unreadable []string, ambiguous []error) error {
	var parts []string
	if len(unreadable) > 0 {
		parts = append(parts, fmt.Sprintf("%d surface(s) could not be read: %s", len(unreadable), strings.Join(unreadable, ", ")))
	}
	for _, a := range ambiguous {
		parts = append(parts, a.Error())
	}
	if len(parts) == 0 {
		return err
	}
	incomplete := fmt.Sprintf("this move ran on an incomplete picture and may have skipped a session — %s", strings.Join(parts, "; "))
	if err == nil {
		return errors.New(incomplete)
	}
	return fmt.Errorf("%w; %s", err, incomplete)
}

// moveResult decides whether an inventory-wide problem (an unreadable
// surface, or a session ambiguous across two accounts) should affect this
// move's exit code, on top of err itself.
//
// `--all` keeps the old, broad behaviour: an unreadable or ambiguous surface
// may be exactly the session that should have been part of the batch, and
// the exit code is the only thing a script reads — reporting success for a
// batch that silently skipped a pane is the same half-completed outcome the
// stat guard was ruled on, so it exits non-zero.
//
// A single-session move (the default current-pane move, or --session) does
// not: a stale surface elsewhere in the workspace — cmux not running for it,
// an unrelated transcript hosted by two accounts at once — has nothing to do
// with the one session this invocation targeted, and folding it in would
// turn a fully successful move into a failure on a workspace that merely has
// other issues (observed live: eight sessions dual-hosted across two
// accounts, which would fail every single-session move). The warning is
// still printed unconditionally by the caller before this runs, on every
// path — this only governs the exit code. A single-session ambiguity, if
// any, already came back directly from claudesession.Find at the call site
// rather than through the ambiguous slice, so it is not lost by skipping
// withIncompletePicture here.
func moveResult(err error, all bool, unreadable []string, ambiguous []error) error {
	if !all {
		return err
	}
	return withIncompletePicture(err, unreadable, ambiguous)
}

// caller identifies the Claude session running `pigtail session move`, by
// the two keys a row can be matched on. Either one is enough.
type caller struct {
	// SurfaceRef is the caller's cmux pane, as a short `surface:N` ref.
	SurfaceRef string
	// SessionID is the caller's own Claude session id, from
	// $CLAUDE_CODE_SESSION_ID — set by Claude Code for every command it runs.
	SessionID string
}

// is reports whether r is the calling session. The session id is checked on
// top of the pane on purpose: the pane lookup depends on cmux answering, and
// the one mistake this guards against — typing /exit into the session that
// runs the batch — must not hinge on a single signal.
func (c caller) is(r sessionRow) bool {
	return (c.SurfaceRef != "" && r.SurfaceRef == c.SurfaceRef) ||
		(c.SessionID != "" && r.SessionID == c.SessionID)
}

// callingSession resolves the caller's identity from its environment. Both
// keys are inherited environment, not process ancestry: the Bash tool of
// Claude Code detaches every command (setsid), so no process-tree walk from
// here reliably reaches the claude process.
//
// Inside a cmux pane whose ref cannot be resolved, this fails rather than
// returning a partial identity: the default move would not find its pane,
// and --all would lose one of the two keys that keep it from selecting
// itself.
func callingSession() (caller, error) {
	ref, err := cmux.CurrentSurfaceRef()
	if err != nil {
		return caller{}, fmt.Errorf("could not identify the calling pane: %w", err)
	}
	return caller{SurfaceRef: ref, SessionID: os.Getenv("CLAUDE_CODE_SESSION_ID")}, nil
}

// selectTargets picks the sessions to move: the current pane's by default, or
// every session of the source account with --all.
//
// With --all, the calling session is left out and returned as skipped:
// moving it types /exit into the very session running this command, which
// quits it mid-turn and takes the rest of the batch down with it. There is
// deliberately no opt-in: the calling session is moved on its own, by the
// default (current pane) move.
func selectTargets(rows []sessionRow, all bool, from string, tbl *claudeprofile.Table, self caller) (targets []sessionRow, skipped *sessionRow, err error) {
	if !all {
		for _, r := range rows {
			if self.SurfaceRef != "" && r.SurfaceRef == self.SurfaceRef {
				return []sessionRow{r}, nil, nil
			}
		}
		return nil, nil, errors.New("no Claude session in the current pane (use --all, or run this from the pane you want to move)")
	}
	srcID := from
	if srcID == "" {
		for _, r := range rows {
			if self.SurfaceRef != "" && r.SurfaceRef == self.SurfaceRef {
				srcID = r.CellID
			}
		}
	}
	if srcID == "" {
		return nil, nil, errors.New("--all outside a cmux pane needs --from <cell>")
	}
	if _, ok := tbl.Cell(srcID); !ok {
		return nil, nil, fmt.Errorf("unknown cell %q", srcID)
	}
	var mine *sessionRow
	for _, r := range rows {
		if r.CellID != srcID {
			continue
		}
		if self.is(r) {
			mine = &r
			continue
		}
		targets = append(targets, r)
	}
	return targets, mine, nil
}

// moveAll runs moveOne for every target in order, continuing past a failure
// so one bad session never blocks the rest of an --all batch, and returns an
// aggregate error when at least one target failed — so $? reflects a partial
// failure of the batch instead of silently reporting success. Each failure is
// also reported individually on warn as it happens.
//
// Extracted out of sessionMoveCmd's RunE specifically so it can be exercised
// with the same moveOne seam (sendLine, copyTranscript, etc.) as every other
// moveOne test, without a real cmux CLI or claudeprofile.Load: those live
// earlier in RunE, before targets are even selected.
func moveAll(out, warn io.Writer, targets []sessionRow, dst claudeprofile.Cell, tbl *claudeprofile.Table, dryRun, keepSource bool) error {
	var failures []string
	for _, r := range targets {
		if err := moveOne(out, warn, r, dst, tbl, dryRun, keepSource); err != nil {
			fmt.Fprintf(warn, "  ✗ %s: %v\n", r.SessionID[:8], err)
			failures = append(failures, r.SessionID[:8])
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d of %d session(s) failed to move: %s", len(failures), len(targets), strings.Join(failures, ", "))
	}
	return nil
}

// The operations moveOne and moveDetached perform, as package-level vars in
// the style of claudesession's psRun seam: injected so tests can record call
// order and fail one specific step, without a real cmux CLI or a live Claude
// process. Production code leaves these at their real implementations; tests
// save, override and restore them (see stubMoveOps in session_move_test.go).
var (
	sendLine         = cmux.SendLine
	waitExit         = claudesession.WaitExit
	runningProbe     = claudesession.Running
	copyTranscript   = claudesession.Copy
	setResumeBinding = cmux.SetResumeBinding
	removeSource     = claudesession.RemoveSource
	readScreen       = cmux.ReadScreen
	sendKey          = cmux.SendKey
	// movePreTrust pre-accepts folder trust in the target cell before the
	// relaunch, inherited from a trusted ancestor (spawn's rule, D0-D3). It
	// only lists workspaces, a layer unscoped by decision: the plain client is
	// right here, and newScopedClient stays confined to RunE literals (the
	// scope guard, TestScopedCommandsDeclareAccess).
	movePreTrust = func(configDir, cwd string) claudeconfig.Result {
		return preTrustLaunch(context.Background(), newClient(), configDir, cwd)
	}
)

// preflight checks what makes a move fail before anything has been touched:
// the source transcript is there, and the destination has no transcript for
// that id already. It runs before the first SendLine on the pane path and
// before the running probe on the detached one — failing here leaves the
// session alive and untouched, instead of failing inside Copy after /exit and
// up to 20s of waiting for nothing.
//
// It is a shared helper, not a copy per path, on purpose: moveOne and
// moveDetached drifting apart on exactly this block is what produced two
// separate Critical findings on this branch. A third instance of that bug
// class should be impossible to write, not merely caught in review.
func preflight(tr, dstTr claudesession.Transcript) error {
	if _, err := os.Stat(tr.JSONLPath()); err != nil {
		return fmt.Errorf("source transcript: %w", err)
	}
	if _, err := os.Stat(dstTr.JSONLPath()); err == nil {
		return fmt.Errorf("target already has a transcript for %s: %s", tr.ID, dstTr.JSONLPath())
	}
	return nil
}

// verifyUnchanged is the belt-and-braces guard between the copy and the
// deletion: never delete a source that changed since the stat taken right
// before the copy, or one that can no longer even be verified. Either case
// means the primary defence (Running/WaitExit) got it wrong — exactly when
// the user needs to know. It reports a failure, not a silent success: on warn
// (stderr), and with a non-nil error, so it counts toward the --all aggregate
// error instead of leaving $? at 0 with a duplicate quietly left behind.
//
// Shared by both move paths for the same reason preflight is — including the
// two error strings, which are the part a drift would make inconsistent.
func verifyUnchanged(warn io.Writer, tr claudesession.Transcript, beforeCopy os.FileInfo, dstPath string) error {
	beforeDelete, err := os.Stat(tr.JSONLPath())
	if err != nil {
		fmt.Fprintf(warn, "   ✗ could not verify the source is unchanged (%v) — kept; destination copy exists at %s\n", err, dstPath)
		return fmt.Errorf("source not removed: could not verify it is unchanged: %w", err)
	}
	if beforeDelete.Size() != beforeCopy.Size() || !beforeDelete.ModTime().Equal(beforeCopy.ModTime()) {
		fmt.Fprintf(warn, "   ✗ source changed during the copy (session still writing?) — kept; destination copy exists at %s\n", dstPath)
		return errors.New("source not removed: it changed during the copy")
	}
	return nil
}

// moveOne runs the ordered sequence for a single session: quit, wait for the
// process to actually be gone, copy the transcript, relaunch on the new
// account, rewrite the cmux resume binding, and only then remove the source.
// The source transcript is removed last, so every earlier failure leaves a
// resumable state behind — this order must never change, and the wait must
// never be replaced by a sleep.
//
// The relaunch route (typed line, or handoff to the launcher) is chosen
// before /exit from the Claude process's environment, and the quit wait
// watches that process's pid too. After the relaunch, confirmRelaunch must
// see a new Claude process resuming the session on dst before the binding is
// rewritten or the source removed: see session_relaunch.go.
//
// The pre-flight (preflight) runs before the first SendLine, and before the
// dry run prints anything: a dry run that skipped the checks would report a
// move that cannot happen, which is the one thing it exists to tell you.
//
// Even after WaitExit reports the process gone, the source is stat'd again
// immediately before Copy and once more immediately before RemoveSource
// (verifyUnchanged): if its size or mtime moved between those two points, the
// session was still writing — or WaitExit's process listing was stale — and
// the source is kept rather than deleted. A duplicate on both sides is
// recoverable; a deleted live transcript is not.
func moveOne(out, warn io.Writer, r sessionRow, dst claudeprofile.Cell, tbl *claudeprofile.Table, dryRun, keepSource bool) error {
	return moveOneGuarded(out, warn, r, dst, tbl, dryRun, keepSource, nil)
}

// The two keystroke stages a move guard is asked about (moveOneGuarded).
const (
	moveStageQuit     = "quit"     // right before /exit
	moveStageRelaunch = "relaunch" // right before the typed resume line (shell route)
)

// errAfterExit is a move failure after /exit was typed and before the
// relaunch was confirmed: the session has quit, so it is no longer live.
// Stage is copy, handoff, relaunch or confirm; Run is the line that resumes
// it by hand. Its message is the underlying error's, unchanged.
type errAfterExit struct {
	Stage string
	Err   error
	Run   string
}

func (e *errAfterExit) Error() string { return e.Err.Error() }
func (e *errAfterExit) Unwrap() error { return e.Err }

// errAfterRelaunch is a move failure once the relaunch on dst was confirmed:
// the session already runs there. Stage is binding, verify or remove.
type errAfterRelaunch struct {
	Stage string
	Err   error
}

func (e *errAfterRelaunch) Error() string { return e.Err.Error() }
func (e *errAfterRelaunch) Unwrap() error { return e.Err }

// moveOneGuarded is moveOne with a keystroke guard: guard(stage), when not
// nil, is called right before /exit (moveStageQuit) and right before the
// typed resume line of the shell route (moveStageRelaunch); an error stops
// the move there, nothing typed. The handoff route types nothing after /exit
// and asks nothing. The manual session move passes nil: its keystrokes stay
// unguarded, as they always were. The account rotation's worker passes its
// guard (session_rotate.go). Failures after /exit come back as *errAfterExit,
// after the confirmed relaunch as *errAfterRelaunch, with the same messages.
func moveOneGuarded(out, warn io.Writer, r sessionRow, dst claudeprofile.Cell, tbl *claudeprofile.Table, dryRun, keepSource bool, guard func(stage string) error) error {
	src, ok := tbl.Cell(r.CellID)
	if !ok {
		return fmt.Errorf("session sits in an unknown config dir: %s", r.ConfigDir)
	}
	if err := validateMove(src, dst); err != nil {
		return err
	}

	tr := claudesession.Transcript{ID: r.SessionID, ConfigDir: r.ConfigDir, Slug: r.Slug}
	dstTr := claudesession.Transcript{ID: r.SessionID, ConfigDir: dst.ConfigDir, Slug: r.Slug}
	if err := preflight(tr, dstTr); err != nil {
		return err
	}
	route, err := routeRelaunch(src, r.SessionID)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "── %s  %s → %s  (%s)\n", r.SessionID[:8], src.ID, dst.ID, r.SurfaceRef)
	if dryRun {
		fmt.Fprintf(out, "   would quit, copy %s → %s, relaunch %s, check it runs on %s, rebind, remove source\n",
			tr.JSONLPath(), dstTr.JSONLPath(), route.describe(), dst.ID)
		return nil
	}
	unlock, err := lockMove(r.SessionID)
	if err != nil {
		return err
	}
	defer unlock()

	// Under the launcher the pane is never typed into after /exit: the
	// launcher's one-key prompt would eat the first character. The pending
	// handoff tells it to wait for this move instead of prompting.
	// The relaunched Claude is told apart from every older one by this
	// listing, taken before anything is sent: no listing, no move.
	before, err := claudePIDs()
	if err != nil {
		return fmt.Errorf("cannot list processes, so the relaunch could not be checked — nothing touched: %w", err)
	}

	// <key>.sid goes first, the pending handoff last: the handoff stays the
	// signal, and the launcher honours it only with a .sid holding the same
	// id — which pigtail no longer leaves to the SessionEnd hook alone.
	var sid *relaunchfile.SidClaim
	abandon := func() {
		removeHandoff(route.HandoffPath)
		sid.Release()
	}
	if route.Kind == relaunchHandoff {
		c, err := relaunchfile.ClaimSid(route.SidPath, r.SessionID)
		if err != nil {
			return err
		}
		sid = c
		if err := writeHandoff(route.HandoffPath, handoffPending, dst.ID, r.SessionID); err != nil {
			sid.Release()
			return err
		}
	}
	// From here on a Ctrl-C (or SIGTERM) withdraws the handoff and says what
	// state the move was left in — the source is always still there — with
	// the recovery fitting the route, instead of exiting mute.
	var progress struct {
		sync.Mutex
		copied string // target transcript path once the copy has landed
	}
	defer interruptNotify(func() {
		abandon()
		progress.Lock()
		copied := progress.copied
		progress.Unlock()
		for _, line := range interruptRecovery(route, r, src, dst, tr.JSONLPath(), copied, resolveWrapper(io.Discard)) {
			fmt.Fprintf(warn, "   %s\n", line)
		}
	})()

	if guard != nil {
		if err := guard(moveStageQuit); err != nil {
			abandon()
			return fmt.Errorf("quit: %w", err)
		}
	}
	if err := sendLine(r.SurfaceRef, "/exit"); err != nil {
		abandon()
		return fmt.Errorf("quit: %w", err)
	}
	// Observe the process actually go, never sleep a fixed amount: a session
	// mid-tool-use swallows /exit into its prompt, and copying a transcript
	// still being written would corrupt the move.
	// quitProbe also watches the pid found before /exit: a Claude started by
	// a bare `c <cell>` has no session id in argv for runningProbe to see.
	if err := waitExit(r.SessionID, quitTimeout, quitInterval, quitProbe(route.PID)); err != nil {
		abandon()
		return cancelExitDialog(warn, r.SurfaceRef, err)
	}
	fmt.Fprintln(out, "   ✓ session quit")
	// The line that resumes the session by hand once it has quit: on src
	// until the copy has landed, on dst after.
	runLine := func(cell claudeprofile.Cell) string {
		if route.Kind == relaunchHandoff {
			return fmt.Sprintf("c %s --resume %s", cell.ID, r.SessionID)
		}
		return cmux.ResumeLine(resolveWrapper(io.Discard), cell.ConfigDir, r.SessionID)
	}
	afterExit := func(stage string, err error, cell claudeprofile.Cell) error {
		return &errAfterExit{Stage: stage, Err: err, Run: runLine(cell)}
	}

	beforeCopy, err := os.Stat(tr.JSONLPath())
	if err != nil {
		abandon()
		return afterExit("copy", fmt.Errorf("source transcript vanished before copy: %w", err), src)
	}

	moved, err := copyTranscript(tr, dst.ConfigDir)
	if err != nil {
		abandon()
		return afterExit("copy", fmt.Errorf("copy: %w", err), src)
	}
	fmt.Fprintf(out, "   ✓ transcript copied to %s\n", moved.JSONLPath())
	progress.Lock()
	progress.copied = moved.JSONLPath()
	progress.Unlock()

	switch pt := movePreTrust(dst.ConfigDir, r.Cwd); pt.Outcome {
	case claudeconfig.Written:
		fmt.Fprintf(out, "   ✓ folder trust pre-accepted on %s (%s)\n", dst.ID, pt.Key)
	case claudeconfig.Failed:
		fmt.Fprintf(warn, "   ⚠ folder trust not pre-accepted on %s: %s — the relaunched session may stop on \"trust this folder\"\n", dst.ID, pt.Reason)
	}

	wrapper := resolveWrapper(warn)
	if route.Kind == relaunchHandoff {
		if err := writeHandoff(route.HandoffPath, handoffReady, dst.ID, r.SessionID); err != nil {
			abandon()
			return afterExit("handoff", fmt.Errorf("relaunch: %w", err), dst)
		}
	} else {
		if guard != nil {
			if err := guard(moveStageRelaunch); err != nil {
				return afterExit("relaunch", err, dst)
			}
		}
		if err := sendLine(r.SurfaceRef, cmux.ResumeLine(wrapper, dst.ConfigDir, r.SessionID)); err != nil {
			return afterExit("relaunch", fmt.Errorf("relaunch: %w", err), dst)
		}
	}
	pid, err := confirmRelaunch(relaunchCheck{SessionID: r.SessionID, Dst: dst, Before: before, Surface: route.Surface})
	if err != nil {
		abandon()
		fmt.Fprintf(warn, "   ✗ %v\n", err)
		fmt.Fprintf(warn, "     source kept: %s — a copy already exists at %s\n", tr.JSONLPath(), moved.JSONLPath())
		for _, line := range relaunchRecovery(err, route, r, dst, tr.JSONLPath(), wrapper) {
			fmt.Fprintf(warn, "     %s\n", line)
		}
		return afterExit("confirm", err, dst)
	}
	fmt.Fprintf(out, "   ✓ relaunched on %s (pid %d)\n", dst.ID, pid)

	if err := setResumeBinding(r.SurfaceRef, r.Cwd, r.SessionID, dst.ConfigDir, wrapper); err != nil {
		// A rebinding failure stops the move here, before deletion, regardless
		// of --keep-source: the transcript now lives at the destination, but
		// the pane's resume binding still names the source account. On the
		// next cmux restart that pane would run `claude --resume <id>` under
		// the source account and find nothing — a pane pointing at a deleted
		// live transcript is unrecoverable without hand-editing the binding.
		// A duplicate transcript on both sides is merely annoying.
		fixCmd := resumeBindingFixCommand(r, dst, wrapper)
		// This is a failure of the move, not a warning: it goes to stderr and
		// the returned error makes it count toward the --all aggregate error,
		// same as every other failure in this function.
		fmt.Fprintf(warn, "   ✗ resume binding not rewritten: %v\n", err)
		fmt.Fprintf(warn, "     source kept on purpose — the pane would otherwise restore onto %s where the transcript no longer is.\n", src.ID)
		fmt.Fprintf(warn, "     fix it with:  %s\n", fixCmd)
		// Re-running is not an option once the copy has landed: the pre-flight
		// check above would refuse it as "target already has a transcript".
		// The only remaining step is a manual delete of the source.
		fmt.Fprintf(warn, "     once fixed, delete the source transcript by hand: %s\n", tr.JSONLPath())
		return &errAfterRelaunch{Stage: "binding", Err: fmt.Errorf("resume binding not rewritten: %w", err)}
	}
	fmt.Fprintln(out, "   ✓ resume binding rewritten")

	if keepSource {
		fmt.Fprintln(out, "   · source kept (--keep-source)")
		return nil
	}

	if err := verifyUnchanged(warn, tr, beforeCopy, moved.JSONLPath()); err != nil {
		return &errAfterRelaunch{Stage: "verify", Err: err}
	}

	if err := removeSource(tr); err != nil {
		return &errAfterRelaunch{Stage: "remove", Err: fmt.Errorf("remove source: %w", err)}
	}
	fmt.Fprintf(out, "   ✓ source removed — session now on %s\n", dst.ID)
	return nil
}

// resumeBindingFixCommand is the cmux command that points pane r's resume
// binding at dst — what the user runs when pigtail could not do it.
func resumeBindingFixCommand(r sessionRow, dst claudeprofile.Cell, wrapper string) string {
	launcher := "claude"
	if wrapper != "" {
		launcher = wrapper
	}
	return fmt.Sprintf(
		"cmux surface resume set --surface %s --cwd %s --kind agent --checkpoint %s -- /usr/bin/env CLAUDE_CONFIG_DIR=%s %s --resume %s --permission-mode auto",
		r.SurfaceRef, r.Cwd, r.SessionID, dst.ConfigDir, launcher, r.SessionID,
	)
}

// cancelExitDialog runs when the session did not quit in time after /exit.
// The usual reason is background work: /exit then opens Claude Code's
// "Background work is running" dialog with "Exit and stop tasks" preselected,
// and leaving it open means one stray Enter in that pane kills the session's
// subagents and shells. So the pane is read, and if that dialog is on screen,
// Escape (Stay) closes it and the session carries on untouched.
//
// Escape is pressed only when the dialog is seen. Without it the session is
// most likely busy mid-turn, and Escape there interrupts the running turn —
// so an unreadable screen presses nothing either, and asks the user to look.
//
// The move fails in every case: the session did not quit, so nothing was
// copied. The returned error keeps waitErr and says why when it is known.
func cancelExitDialog(warn io.Writer, ref string, waitErr error) error {
	screen, err := readScreen(ref)
	if err != nil {
		fmt.Fprintf(warn, "   ✗ could not read %s to check for an open exit dialog (%v) — look at that pane; if Claude Code asks to stop background work, press Escape\n", ref, err)
		return waitErr
	}
	if !claudesession.ExitDialogShown(screen) {
		return waitErr
	}
	if err := sendKey(ref, "Escape"); err != nil {
		fmt.Fprintf(warn, "   ✗ Claude Code's exit dialog is open in %s and could not be dismissed (%v) — press Escape there, Enter would stop its background work\n", ref, err)
	} else {
		fmt.Fprintf(warn, "   · exit dialog dismissed (Stay) — the session keeps running with its background work\n")
	}
	return fmt.Errorf("session has background work running, not moved (let it finish, then retry): %w", waitErr)
}

// lockMove takes the per-session move lock for a real move: session move and
// session handover never run on one session at once.
func lockMove(id string) (func(), error) {
	unlock, err := claudesession.LockSession(id)
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", id[:8], err)
	}
	return unlock, nil
}

// moveDetached handles a session that is not in a cmux pane: the transcript is
// relocated, but nothing can be quit, relaunched or rebound, so the command
// hands the user the line to run. Without a pane there is also no process to
// wait on, so the caller must be sure the session is not running — hence the
// explicit --session flag rather than any implicit selection.
// The transcript is passed already resolved: its slug cannot be recomputed from
// a working directory here, and cannot be inverted back into one either (the
// slug maps both '/' and '.' onto '-', losing the distinction).
//
// This is the weakest path in the feature — there is no pane to observe, so
// runningProbe is the only gate — which is why it carries the same
// protections moveOne has, not a lighter version of them, and through the
// very same helpers: preflight before anything is touched, and
// verifyUnchanged around the copy so it never deletes a source that changed
// underneath it.
//
// The dry run runs both the pre-flight and the running probe before printing:
// a still-running session and a destination conflict are the two reasons this
// path refuses to move anything, so a dry run that skipped them would promise
// a move that cannot happen.
func moveDetached(out, warn io.Writer, tr claudesession.Transcript, src, dst claudeprofile.Cell, dryRun, keepSource bool) error {
	id := tr.ID
	if err := validateMove(src, dst); err != nil {
		return err
	}

	dstTr := claudesession.Transcript{ID: id, ConfigDir: dst.ConfigDir, Slug: tr.Slug}
	if err := preflight(tr, dstTr); err != nil {
		return err
	}
	if runningProbe(id) {
		return fmt.Errorf("session %s is still running; quit it before moving it from outside its pane", id[:8])
	}

	fmt.Fprintf(out, "── %s  %s → %s  (no cmux pane)\n", id[:8], src.ID, dst.ID)
	if dryRun {
		fmt.Fprintf(out, "   would copy %s → %s, remove source\n", tr.JSONLPath(), dstTr.JSONLPath())
		return nil
	}
	unlock, err := lockMove(id)
	if err != nil {
		return err
	}
	defer unlock()

	beforeCopy, err := os.Stat(tr.JSONLPath())
	if err != nil {
		return fmt.Errorf("source transcript vanished before copy: %w", err)
	}

	moved, err := copyTranscript(tr, dst.ConfigDir)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	fmt.Fprintf(out, "   ✓ transcript copied to %s\n", moved.JSONLPath())

	if !keepSource {
		if err := verifyUnchanged(warn, tr, beforeCopy, moved.JSONLPath()); err != nil {
			return err
		}
		if err := removeSource(tr); err != nil {
			return fmt.Errorf("remove source: %w", err)
		}
	}
	fmt.Fprintf(out, "   ✓ transcript moved. Resume it with:\n     cr %s -r %s\n", dst.ID, id)
	return nil
}
