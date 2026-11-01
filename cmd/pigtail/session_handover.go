package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/spf13/cobra"
)

// `pigtail session handover` moves a session's transcript to another cell
// BEFORE it is resumed there — for the launcher:
// `c <cell> --resume <sid>` and its relaunch prompt. One call: checks,
// verified copy, source removed, exit. Nothing is quit nor launched.
// Contract: stdout key=value lines, `handover=1` always first (absent = a
// pigtail without this command), then sid src_cell src_scope dst_cell
// dst_scope scope_change cwd src_path dst_path, moved=1; path= per copy (4);
// removed= / would_remove= per copy another one contains (identical or an
// older prefix) — with exit 5 or 0. Humans read stderr.
const (
	exitHandoverNotFound     = 3 // no transcript in any known cell
	exitHandoverAmbiguous    = 4 // several accounts, no single answer: path= lines
	exitHandoverAlreadyThere = 5 // on the target (covered copies removed)
	exitHandoverRunning      = 6 // a Claude process serves the session
	exitHandoverScopeChange  = 7 // scopes differ, no --allow-scope-change
	exitHandoverBusy         = 8 // another pigtail holds this session's lock
)

type handoverOpts struct {
	SessionID, To       string
	DryRun, ScopeChange bool
}

func runHandover(out, warn io.Writer, tbl *claudeprofile.Table, o handoverOpts) error {
	dst, ok := tbl.Cell(o.To)
	if !ok {
		return fmt.Errorf("unknown cell %q", o.To)
	}
	if !o.DryRun {
		unlock, err := claudesession.LockSession(o.SessionID)
		if errors.Is(err, claudesession.ErrSessionBusy) {
			return withExitCode(exitHandoverBusy, fmt.Errorf("another pigtail is moving session %s right now", o.SessionID[:8]))
		}
		if err != nil {
			return err
		}
		defer unlock()
		// A Ctrl-C or a closed tab never cuts a move in half: the call ends with
		// the move done or not started. A SIGKILL between copy and removal leaves
		// two identical copies, which the next call settles.
		signal.Ignore(syscall.SIGINT, syscall.SIGHUP)
		defer signal.Reset(syscall.SIGINT, syscall.SIGHUP)
	}
	found, err := claudesession.FindAll(o.SessionID, tbl.ConfigDirs())
	if err != nil {
		return err
	}
	kv := newKV(out)
	if len(found) == 0 {
		return withExitCode(exitHandoverNotFound, fmt.Errorf("no transcript for session %s in any known cell", o.SessionID))
	}
	tr, stale, ok, err := pickHead(found, dst, tbl)
	if err != nil {
		return err
	}
	if !ok {
		return ambiguous(kv, o.SessionID, found)
	}
	if filepath.Clean(tr.ConfigDir) == filepath.Clean(dst.ConfigDir) {
		return settleDuplicates(kv, tr, stale, dst, tbl, o.DryRun)
	}
	src, ok := tbl.ByConfigDir(tr.ConfigDir)
	if !ok {
		return fmt.Errorf("session %s lives in %s, which no cell covers (add its cell to %s — see docs/CONFIG.md § claude-cells.yaml)", o.SessionID, tr.ConfigDir, claudeprofile.Filename)
	}
	cwd, _, err := claudesession.OriginCwd(tr)
	if err != nil {
		return fmt.Errorf("read the session's launch dir: %w", err)
	}
	dstTr := claudesession.Transcript{ID: tr.ID, ConfigDir: dst.ConfigDir, Slug: tr.Slug}
	change := "0"
	if src.Scope != dst.Scope {
		change = "1"
	}
	for _, p := range [][2]string{{"sid", tr.ID}, {"src_cell", src.ID}, {"src_scope", src.Scope}, {"dst_cell", dst.ID},
		{"dst_scope", dst.Scope}, {"scope_change", change}, {"cwd", cwd}, {"src_path", tr.JSONLPath()}, {"dst_path", dstTr.JSONLPath()}} {
		kv.put(p[0], p[1])
	}
	if kv.err != nil {
		return kv.err
	}
	if !authenticated(dst.ConfigDir) {
		return fmt.Errorf("cell %s is not authenticated (no account in %s)", dst.ID, dst.ConfigDir)
	}
	if sessionRunning(tr.ID, tbl.ConfigDirs()) {
		return withExitCode(exitHandoverRunning, fmt.Errorf("session %s is still running; quit it before resuming it elsewhere", tr.ID[:8]))
	}
	// A stale copy on the target's path is removed before the copy (Copy
	// refuses an occupied target): only the source is checked then.
	occupied := false
	for _, s := range stale {
		occupied = occupied || filepath.Clean(s.JSONLPath()) == filepath.Clean(dstTr.JSONLPath())
	}
	if occupied {
		if _, err := os.Stat(tr.JSONLPath()); err != nil {
			return fmt.Errorf("source transcript: %w", err)
		}
	} else if err := preflight(tr, dstTr); err != nil {
		return err
	}
	if o.DryRun {
		for _, s := range stale {
			kv.put("would_remove", s.JSONLPath())
		}
		return kv.err
	}
	if change == "1" && !o.ScopeChange {
		return withExitCode(exitHandoverScopeChange, fmt.Errorf("refusing a scope change: %s is %q, %s is %q (pass --allow-scope-change once the user agreed)",
			src.ID, src.Scope, dst.ID, dst.Scope))
	}
	// Every refusal is behind us: the covered copies go, then the head moves.
	// A kill in between leaves the head alone at its source — nothing lost.
	n, err := removeStale(kv, stale)
	if err != nil {
		return err
	}
	if err := moveNow(warn, kv, tr, src, dst); err != nil {
		return err
	}
	if n > 0 {
		fmt.Fprintf(warn, "session %s: %s removed\n", tr.ID[:8], coveredCopies(n))
	}
	return nil
}

// pickHead returns the copy that covers every other one (claudesession.Covers)
// and the copies it covers. Candidates are tried by .jsonl size, largest
// first — only a largest copy can cover all the others —, ties broken by: the
// target's copy, then a copy of a cell in the target's scope (no needless
// scope change), then found's order. Identical copies thus keep the target's,
// as before. ok=false: no copy covers all the others, a real fork with no
// single answer (4). Two copies that are one file on disk (two cells on one
// config dir, or a symlink) are refused outright: "removing" one would remove
// the other.
func pickHead(found []claudesession.Transcript, dst claudeprofile.Cell, tbl *claudeprofile.Table) (head claudesession.Transcript, stale []claudesession.Transcript, ok bool, err error) {
	type cand struct {
		i    int
		fi   os.FileInfo
		rank int // 0 target, 1 target's scope, 2 other
	}
	cands := make([]cand, 0, len(found))
	for i := range found {
		fi, err := os.Stat(found[i].JSONLPath())
		if err != nil {
			return head, nil, false, err
		}
		for _, c := range cands {
			if os.SameFile(c.fi, fi) {
				return head, nil, false, fmt.Errorf("%s and %s are one file (two cells on one config dir?) — nothing touched",
					found[c.i].JSONLPath(), found[i].JSONLPath())
			}
		}
		rank := 2
		if filepath.Clean(found[i].ConfigDir) == filepath.Clean(dst.ConfigDir) {
			rank = 0
		} else if c, ok := tbl.ByConfigDir(found[i].ConfigDir); ok && c.Scope == dst.Scope {
			rank = 1
		}
		cands = append(cands, cand{i, fi, rank})
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].fi.Size() != cands[b].fi.Size() {
			return cands[a].fi.Size() > cands[b].fi.Size()
		}
		return cands[a].rank < cands[b].rank
	})
next:
	for _, h := range cands {
		if h.fi.Size() < cands[0].fi.Size() {
			break // a shorter .jsonl never covers a longer one
		}
		for j := range found {
			if j == h.i {
				continue
			}
			c, err := claudesession.Covers(found[h.i], found[j])
			if err != nil {
				return head, nil, false, fmt.Errorf("compare %s with %s: %w", found[h.i].JSONLPath(), found[j].JSONLPath(), err)
			}
			if !c {
				continue next
			}
		}
		for j := range found {
			if j != h.i {
				stale = append(stale, found[j])
			}
		}
		return found[h.i], stale, true, nil
	}
	return head, nil, false, nil
}

// removeStale removes each covered copy (.jsonl and sidecar), removed= per
// copy, nothing on stderr: the outcome's line comes first (the launcher reads
// it). Real calls only — a dry run lists would_remove= itself. The first
// failure stops it: nothing is moved, the head is intact, the next call
// resumes.
func removeStale(kv *kvWriter, stale []claudesession.Transcript) (int, error) {
	n := 0
	for _, tr := range stale {
		if err := removeSource(tr); err != nil {
			return n, fmt.Errorf("remove the stale copy %s: %w — nothing moved", tr.JSONLPath(), err)
		}
		kv.put("removed", tr.JSONLPath())
		n++
	}
	return n, kv.err
}

// coveredCopies: "1 covered copy", "2 covered copies".
func coveredCopies(n int) string {
	if n == 1 {
		return "1 covered copy"
	}
	return fmt.Sprintf("%d covered copies", n)
}

// alreadyOnTarget: nothing to move, but the launcher still needs the launch
// dir (D2) — sid, dst_cell, dst_scope, cwd, dst_path — and a session open
// elsewhere is refused (6), never resumed twice.
func alreadyOnTarget(kv *kvWriter, keep claudesession.Transcript, dst claudeprofile.Cell, tbl *claudeprofile.Table, removed int) error {
	cwd, _, err := claudesession.OriginCwd(keep)
	if err != nil {
		return fmt.Errorf("read the session's launch dir: %w", err)
	}
	for _, p := range [][2]string{{"sid", keep.ID}, {"dst_cell", dst.ID}, {"dst_scope", dst.Scope}, {"cwd", cwd}, {"dst_path", keep.JSONLPath()}} {
		kv.put(p[0], p[1])
	}
	if kv.err != nil {
		return kv.err
	}
	if sessionRunning(keep.ID, tbl.ConfigDirs()) {
		return withExitCode(exitHandoverRunning, fmt.Errorf("session %s is still running; quit it before resuming it again", keep.ID[:8]))
	}
	if removed > 0 {
		return withExitCode(exitHandoverAlreadyThere, fmt.Errorf("session %s already lives on %s (%s removed)", keep.ID[:8], dst.ID, coveredCopies(removed)))
	}
	return withExitCode(exitHandoverAlreadyThere, fmt.Errorf("session %s already lives on %s", keep.ID[:8], dst.ID))
}

// settleDuplicates: the head (the copy covering all the others, pickHead)
// lives on dst. The covered copies — identical ones, as a move killed between
// copy and removal leaves (D10), or older byte-prefixes — hold nothing of
// their own: removed (dry run: would_remove=), then 5. A running session is
// refused (6) before anything is removed.
func settleDuplicates(kv *kvWriter, keep claudesession.Transcript, others []claudesession.Transcript,
	dst claudeprofile.Cell, tbl *claudeprofile.Table, dryRun bool) error {
	if len(others) == 0 {
		return alreadyOnTarget(kv, keep, dst, tbl, 0)
	}
	if sessionRunning(keep.ID, tbl.ConfigDirs()) {
		return withExitCode(exitHandoverRunning, fmt.Errorf("session %s is still running; its covered copies are left for later", keep.ID[:8]))
	}
	n := 0
	if dryRun {
		for _, tr := range others {
			kv.put("would_remove", tr.JSONLPath())
		}
	} else {
		var err error
		if n, err = removeStale(kv, others); err != nil {
			return err
		}
	}
	if kv.err != nil {
		return kv.err
	}
	return alreadyOnTarget(kv, keep, dst, tbl, n) // cwd= for the launcher, then 5
}

// moveNow: verified copy, source-unchanged check, source removed.
func moveNow(warn io.Writer, kv *kvWriter, tr claudesession.Transcript, src, dst claudeprofile.Cell) error {
	before, err := os.Stat(tr.JSONLPath())
	if err != nil {
		return fmt.Errorf("source transcript: %w", err)
	}
	moved, err := copyTranscript(tr, dst.ConfigDir)
	if err != nil {
		return fmt.Errorf("copy: %w — nothing moved", err)
	}
	if now, err := os.Stat(tr.JSONLPath()); err != nil || now.Size() != before.Size() || !now.ModTime().Equal(before.ModTime()) {
		if rmErr := removeSource(moved); rmErr != nil {
			return fmt.Errorf("source changed during the copy and the copy %s could not be withdrawn (%s): remove it by hand", moved.JSONLPath(), rmErr.Error())
		}
		return errors.New("source changed during the copy (session still writing?) — copy withdrawn, nothing moved")
	}
	if err := removeSource(tr); err != nil {
		return fmt.Errorf("copied to %s but %s could not be removed (%s): the next call settles the identical copies", moved.JSONLPath(), tr.JSONLPath(), err.Error())
	}
	kv.put("moved", "1")
	fmt.Fprintf(warn, "session %s moved %s → %s\n", tr.ID[:8], src.ID, dst.ID)
	return kv.err
}

// sessionAliveProbe is a seam, in the style of runningProbe.
var sessionAliveProbe = claudesession.SessionAlive

// sessionRunning: a Claude process serves sid — seen in a process's argv, or
// through a live pid file (sessions/<pid>.json) in any of dirs. A session
// resumed by the /resume picker carries no id in its argv: only its pid file
// shows it.
func sessionRunning(sid string, dirs []string) bool {
	if runningProbe(sid) {
		return true
	}
	for _, cfg := range dirs {
		if n, ok := findNative(cfg, sid); ok && sessionAliveProbe(cfg, n.PID, sid) {
			return true
		}
	}
	return false
}

// kvWriter writes the key=value contract on stdout and keeps the first error.
type kvWriter struct {
	w   io.Writer
	err error
}

func newKV(w io.Writer) *kvWriter { return &kvWriter{w: w} }

// put writes key=value; a value carrying \r or \n would break the contract
// and is refused.
func (k *kvWriter) put(key, value string) {
	if k.err != nil {
		return
	}
	if strings.ContainsAny(value, "\r\n") {
		k.err = fmt.Errorf("refusing to print %s: its value spans several lines (%q)", key, value)
		return
	}
	_, k.err = fmt.Fprintf(k.w, "%s=%s\n", key, value)
}

// ambiguous: the session has several copies and no single answer — one path=
// line per copy, in found's order, then exit 4.
func ambiguous(kv *kvWriter, sid string, found []claudesession.Transcript) error {
	paths := make([]string, 0, len(found))
	for _, tr := range found {
		kv.put("path", tr.JSONLPath())
		paths = append(paths, tr.JSONLPath())
	}
	if kv.err != nil {
		return kv.err
	}
	return withExitCode(exitHandoverAmbiguous, fmt.Errorf("session %s exists in %d accounts with no single answer (%s): keep one by hand, then retry",
		sid, len(found), strings.Join(paths, ", ")))
}

func sessionHandoverCmd() *cobra.Command {
	var to string
	var dryRun, allowScopeChange bool
	cmd := &cobra.Command{
		Use:   "handover <session-id> --to <cell> [--dry-run] [--allow-scope-change]",
		Short: "Move a session's transcript to another cell before resuming it there (launcher)",
		Long: `Move a session's transcript to another cell BEFORE it is resumed there, for the
launcher (c <cell> --resume <id>, and its relaunch prompt). One call: checks
(session not running — argv or pid file —, target free, scope agreed), a
verified copy (sub-agent transcripts first, the .jsonl visible last), the
source removed, exit. Nothing is quit nor launched.

The real call holds a per-session lock and ignores SIGINT and SIGHUP: a Ctrl-C
or a closed tab never cuts a move in half. Copies that another copy contains
— identical, or an older byte-prefix with its sub-agent files — are leftovers:
the longest is kept, the others removed (exit 5 when it is on the target;
otherwise it is then moved, exit 0). Copies that fork are refused (exit 4).

Output: key=value lines on stdout, handover=1 always first, then sid src_cell
src_scope dst_cell dst_scope scope_change cwd src_path dst_path, moved=1;
path= per copy (exit 4); removed= / would_remove= per covered copy (exit 5 or 0).
Messages for humans go to stderr.

Exit codes: 0 moved (or would move, --dry-run), 1 error, 3 not found,
4 several accounts with no single answer, 5 already on the target, 6 a Claude
process serves the session, 7 scope change without --allow-scope-change,
8 another pigtail holds this session's lock.`,
		// Arguments and --to are checked inside RunE, not by cobra before it:
		// handover=1 must come first on every invocation, a refused one included
		// (its absence reads as "pigtail too old" to the launcher).
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true, // the launcher reads stderr
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := announceHandover(cmd); err != nil {
				return err
			}
			if len(args) != 1 {
				return fmt.Errorf("handover takes exactly one session id, got %d argument(s)", len(args))
			}
			if to == "" {
				return errors.New(`required flag "--to" not set`)
			}
			if !validSessionID(args[0]) {
				return fmt.Errorf("session id must be a canonical UUID, got %q", args[0])
			}
			tbl, err := claudeprofile.Load(setup.ConfigDir())
			if err != nil {
				return err
			}
			return runHandover(cmd.OutOrStdout(), cmd.ErrOrStderr(), tbl, handoverOpts{
				SessionID: args[0], To: to, DryRun: dryRun, ScopeChange: allowScopeChange,
			})
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "cell to move the session to")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would happen, touch nothing, take no lock")
	cmd.Flags().BoolVar(&allowScopeChange, "allow-scope-change", false, "accept a move between scopes (once the user agreed)")
	// A flag cobra cannot parse never reaches RunE: announce here too.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		if aerr := announceHandover(c); aerr != nil {
			return aerr
		}
		return err
	})
	return cmd
}

// announceHandover prints the contract's first line, handover=1.
func announceHandover(cmd *cobra.Command) error {
	_, err := fmt.Fprintln(cmd.OutOrStdout(), "handover=1")
	return err
}
