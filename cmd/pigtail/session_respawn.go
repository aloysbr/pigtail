package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/relaunchfile"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The fresh-relaunch contract with the `c` zsh launcher (
// the shell launcher, proto 4): the launcher exports CLAUDE_LAUNCH_PROTO into
// Claude's environment, and on Claude's exit honours <key>.handoff =
// « fresh <cell> <sid> <pid> <mode> [<model>] » (the 6th field, proto 4, is a
// model alias, absent = the cell's default) with <key>.prompt (one line) by
// starting `claude --permission-mode <mode> [--model <model>] "<prompt>"` in
// the same tab — provided
// <key>.sid (CLAUDE_RELAUNCH_FILE) holds that same <sid>. The SessionEnd hook
// writes it on a normal exit; the worker writes it too before its SIGTERM,
// which skips the SessionEnd hooks.
const (
	envLaunchProto   = "CLAUDE_LAUNCH_PROTO" // exported by the launcher, proto ≥ 2 = fresh handoff
	launchProtoFresh = 2
	// launchProtoFreshCell: the launcher accepts a fresh relaunch on another
	// cell than the tab's (account rotation) from proto 3.
	launchProtoFreshCell = 3
	// launchProtoModel: the launcher reads a 6th handoff field (the fresh
	// session's model alias) from proto 4.
	launchProtoModel   = 4
	respawnIdleDefault = 10 * time.Minute
	respawnIdleMin     = time.Minute
	respawnIdleMax     = 2 * time.Hour
)

// respawnModels: the model aliases --model accepts, and only them.
var respawnModels = []string{"sonnet", "opus", "haiku", "fable"}

// Respawn results, as the registry records them.
const (
	respawnArmed     = "armed"
	respawnDone      = "respawned"
	respawnFailed    = "failed"
	respawnAbandoned = "abandoned"
)

// Seams: every outside call the respawn command makes. Tests replace them all.
var (
	respawnCurrentSurfaceRef = cmux.CurrentSurfaceRef
	respawnNow               = time.Now
	respawnPidAlive          = claudesession.PidAlive
	// respawnArm starts the detached worker and waits for its handshake
	// (armRespawn, set in init: it reads respawnArm's own seams).
	respawnArm func(reg *spawnreg.Registry, run respawnRun, stderr io.Writer) (int, error)
	// respawnPreTrust pre-accepts the folder trust under the fresh cell when
	// the rotation picked another cell (as spawn does).
	respawnPreTrust = preTrustLaunch
)

type respawnRequest struct {
	SubjectID, Prompt  string
	Model              string
	CrossScope, DryRun bool
	IdleTimeout        time.Duration
	OverMargin         bool // --over-margin: respawn even past the usage margin (decision forced)
}

// respawnRun is what the foreground hands the worker (JSON, 0600, in
// $XDG_STATE_HOME/pigtail/respawn/<id>.json). Also the dry-run's content.
type respawnRun struct {
	ID           string `json:"id"`
	SubjectID    string `json:"subject_id"`
	SubjectTitle string `json:"subject_title"`
	Cell         string `json:"cell"`       // the replaced session's cell
	ConfigDir    string `json:"config_dir"` // its config dir
	// FreshCell, FreshConfigDir: the fresh session's cell when the account
	// rotation picked another one; "" = Cell (and a run file of an older
	// pigtail). Read through fresh().
	FreshCell      string        `json:"fresh_cell,omitempty"`
	FreshConfigDir string        `json:"fresh_config_dir,omitempty"`
	Cwd            string        `json:"cwd"`
	ClaudePID      int           `json:"claude_pid"`
	SessionID      string        `json:"session_id"`
	SurfaceID      string        `json:"surface_id"`   // CMUX_SURFACE_ID (UUID) of the caller Claude
	SurfaceRef     string        `json:"surface_ref"`  // surface:N
	WorkspaceID    string        `json:"workspace_id"` // upper case
	RelaunchFile   string        `json:"relaunch_file"`
	Relaunch       string        `json:"relaunch"` // relaunchLauncher | relaunchShell
	Mode           string        `json:"permission_mode"`
	LaunchMode     string        `json:"launch_mode"`
	LaunchOrigin   string        `json:"launch_mode_origin"`
	CurrentMode    string        `json:"current_mode"`
	Prompt         string        `json:"prompt"`
	Model          string        `json:"model,omitempty"` // the fresh session's model alias; "" = the cell's default
	LaunchProto    int           `json:"launch_proto"`
	Bound          bool          `json:"bound"`       // workspace already bound to the subject
	CrossScope     bool          `json:"cross_scope"` // the worker's client uses WithCrossScopeWrite (attach)
	IdleTimeout    time.Duration `json:"idle_timeout"`
	ForegroundPID  int           `json:"foreground_pid"`

	warnings []string // dry-run only: what a real call would refuse on
	// rotation: the "rotation: …" lines (stderr, --json warnings) and how the
	// fresh cell was chosen; never in the run file.
	rotation       []string
	rotationOrigin string
	// margin: the launch margin guard's decision, traced after the plan.
	margin *launchmargin.Decision
}

// fresh is the cell the fresh session runs on: FreshCell, else Cell.
func (r respawnRun) fresh() (id, dir string) {
	if r.FreshCell == "" {
		return r.Cell, r.ConfigDir
	}
	return r.FreshCell, r.FreshConfigDir
}

func respawnErr(format string, a ...any) error { return fmt.Errorf("session respawn: "+format, a...) }

// respawnDir holds the run files and logs: <id>.json, <id>.log.
func respawnDir() string {
	return filepath.Join(filepath.Dir(spawnreg.DefaultPath()), "respawn")
}

func respawnRunPath(id string) string { return filepath.Join(respawnDir(), id+".json") }
func respawnLogPath(id string) string { return filepath.Join(respawnDir(), id+".log") }

// newRespawnID is time-based and unique: UTC millis + 4 random hex.
func newRespawnID(now time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102T150405.000") + "-" + hex.EncodeToString(b)
}

// launchProtoOf reads CLAUDE_LAUNCH_PROTO from the calling Claude's
// environment: 0 when absent or not a number.
func launchProtoOf(env string) (int, string) {
	v, ok := cmux.EnvValue(env, envLaunchProto)
	if !ok || v == "" {
		return 0, "none"
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, strconv.Quote(v)
	}
	return n, v
}

// protoRefusal: why this tab cannot hand a fresh relaunch. A relaunch file
// with an older proto = an old launcher: re-sourcing is the fix. (No proto and
// no file is not a refusal: the tab's shell takes the relaunch.)
func protoRefusal(protoText, file string) error {
	if file == "" {
		protoText += " without " + relaunchfile.EnvFile
	}
	return respawnErr("this tab's launcher speaks proto %s (fresh relaunch needs ≥ %d): re-source it (`exec zsh` in this tab, or a new tab), relaunch Claude with c, then retry — nothing done", protoText, launchProtoFresh)
}

func planRespawn(ctx context.Context, c *pigtail.Client, reg *spawnreg.Registry, req respawnRequest) (respawnRun, error) {
	var r respawnRun
	if err := validatePrompt(req.Prompt); err != nil {
		return r, respawnErr("%v", err)
	}
	// The launcher reads the prompt file with `read -r`, which strips edge
	// whitespace: the fresh session would then not be recognised after the
	// old one is closed. Refused here, before anything is touched.
	if strings.TrimFunc(req.Prompt, unicode.IsSpace) != req.Prompt {
		return r, respawnErr("--prompt has leading or trailing whitespace — pass it trimmed, e.g. --prompt '/example-skill resume <id>'")
	}
	if req.Model != "" && !slices.Contains(respawnModels, req.Model) {
		return r, respawnErr("--model %q is not a model alias — pass %s (or leave it out for the cell's default); nothing done", req.Model, strings.Join(respawnModels[:len(respawnModels)-1], ", ")+" or "+respawnModels[len(respawnModels)-1])
	}
	if req.IdleTimeout < respawnIdleMin || req.IdleTimeout > respawnIdleMax {
		return r, respawnErr("--idle-timeout %s is out of range — pass a value in %s..%s", req.IdleTimeout, respawnIdleMin, respawnIdleMax)
	}
	r.WorkspaceID = strings.ToUpper(os.Getenv(cmux.EnvWorkspaceID))
	if r.WorkspaceID == "" {
		return r, respawnErr("not in a cmux pane ($%s unset) — respawn replaces the session of the cmux tab it runs in", cmux.EnvWorkspaceID)
	}

	caller, err := resolveCaller(ctx, c, "respawn", req.SubjectID, req.CrossScope, respawnErr)
	if err != nil {
		return r, err
	}
	sub := caller.Subject
	if err := sdkLiveGuard(ctx, c, sub.ID, respawnErr); err != nil {
		return r, err
	}
	r.SubjectID, r.SubjectTitle = sub.ID, pigtail.ShortTitle(sub)
	r.Cell, r.ConfigDir = caller.Cell.ID, caller.Cell.ConfigDir
	r.ClaudePID, r.SessionID = caller.Proc.PID, caller.SessionID
	r.Mode, r.LaunchMode, r.LaunchOrigin, r.CurrentMode = caller.Perm.Mode, caller.Perm.Launch, caller.Perm.Origin, caller.Perm.Current
	r.Prompt, r.CrossScope, r.IdleTimeout, r.ForegroundPID = req.Prompt, req.CrossScope, req.IdleTimeout, os.Getpid()
	r.Model = req.Model

	// The launcher: proto 2 hands a fresh relaunch, and its relaunch file keys
	// the handoff and prompt files.
	proto, protoText := launchProtoOf(caller.Env)
	file, _ := cmux.EnvValue(caller.Env, relaunchfile.EnvFile)
	switch {
	case proto >= launchProtoFresh && file != "":
		r.Relaunch, r.LaunchProto, r.RelaunchFile = relaunchLauncher, proto, file
	case protoText == "none" && file == "":
		// Not started by c (cmux resumed it): its tab's shell takes the relaunch.
		if err := shellPathOK(respawnScriptPath(newRespawnID(respawnNow()))); err != nil {
			return r, respawnErr("%v — nothing done", err)
		}
		r.Relaunch = relaunchShell
	default:
		return r, protoRefusal(protoText, file)
	}

	if err := pickRespawnCell(&r, caller, req.OverMargin); err != nil {
		return r, err
	}
	pickRespawnModel(&r)

	surface, _ := cmux.EnvValue(caller.Env, cmux.EnvSurfaceID)
	if surface == "" {
		return r, respawnErr("the calling Claude carries no %s — respawn only runs from a Claude in a cmux tab", cmux.EnvSurfaceID)
	}
	r.SurfaceID = surface
	if r.SurfaceRef, err = respawnCurrentSurfaceRef(); err != nil {
		return r, retryable(retryShort, respawnErr("locate this tab in cmux: %v — retry; if it persists, check `cmux identify`", err))
	}
	if r.SurfaceRef == "" {
		return r, respawnErr("cmux cannot name this tab's surface ($%s unset in this pigtail) — run respawn from the session's own Bash tool", cmux.EnvSurfaceID)
	}

	cwd, _ := cmux.EnvValue(caller.Env, "PWD")
	if cwd == "" {
		return r, respawnErr("the calling Claude's environment carries no PWD — the fresh session's directory cannot be verified; relaunch Claude from its directory, then retry")
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return r, respawnErr("the session's directory %s is gone — respawn relaunches there; use pigtail session spawn --cwd <dir> instead", cwd)
	}
	if strings.ContainsAny(cwd, " \t\n") {
		return r, respawnErr("the session's directory %q contains whitespace — the relaunch could not be verified (ps -E); use pigtail session spawn from a path without spaces", cwd)
	}
	r.Cwd = cwd

	// The caller workspace's binding: another subject → a respawn here would
	// mis-bind. A 404 is unbound — or bound to a subject this cell may not
	// read (a work cell in a perso-bound workspace): the worker's attach then
	// answers a conflict, recorded as a warning.
	lk, err := c.CmuxWorkspace(ctx, r.WorkspaceID)
	switch {
	case err == nil && lk.Subject.ID != "" && lk.Subject.ID != sub.ID:
		return r, respawnErr("workspace bound to %s — respawn there would mis-bind; use `pigtail session spawn` for a subject change", lk.Subject.ID)
	case err == nil:
		r.Bound = lk.Subject.ID == sub.ID
	case isNotFound(err):
	default:
		return r, retryable(retryShort, respawnErr("read the binding of workspace %s: %v — retry once the daemon answers", r.WorkspaceID, err))
	}

	// Another live session of the subject elsewhere (the session replaced is
	// never counted). No burst check: a respawn replaces, net zero.
	live, unreadable, err := liveWorkspaces()
	if err != nil {
		return r, retryable(retryShort, respawnErr("list Claude processes: %v — retry", err))
	}
	if len(unreadable) > 0 {
		if !req.DryRun {
			return r, retryable(retryShort, respawnErr("%d Claude process(es) have an unreadable environment (pid %v) — another live session of the subject cannot be ruled out; retry, or look at those processes", len(unreadable), unreadable))
		}
		r.warnings = append(r.warnings, fmt.Sprintf("%d Claude process(es) have an unreadable environment (pid %v): a real respawn would refuse", len(unreadable), unreadable))
	}
	entries, err := reg.Entries()
	if err != nil {
		return r, retryable(retryShort, respawnErr("%v", err))
	}
	bindings, err := c.CmuxWorkspaces(ctx, sub.ID)
	if err != nil {
		return r, retryable(retryShort, respawnErr("read workspaces of %s: %v", sub.ID, err))
	}
	candidates := map[string]bool{}
	for _, b := range bindings {
		candidates[strings.ToUpper(b.CmuxWorkspaceID)] = true
	}
	for _, e := range entries {
		if e.SubjectID == sub.ID && e.Kind != spawnreg.KindRespawn && e.Kind != spawnreg.KindMargin && e.WorkspaceID != "" {
			candidates[e.WorkspaceID] = true
		}
	}
	for ws := range candidates {
		if ws != r.WorkspaceID && len(live[ws]) > 0 {
			return r, respawnErr("subject %s already has a live session in workspace %s (pid %v) — finish or close it first", sub.ID, ws, live[ws])
		}
	}

	// One armed run per surface: an agent retrying must not arm twice.
	for _, e := range spawnreg.Respawns(entries, "") {
		if e.SurfaceID == r.SurfaceID && e.Result == respawnArmed && respawnPidAlive(e.WorkerPID) {
			return r, retryable(retryLong, respawnErr("a respawn is already armed for surface %s (run %s) — wait for it, or look at pigtail session respawn --status", r.SurfaceRef, e.ID))
		}
	}
	return r, nil
}

func printRespawnPlan(w io.Writer, r respawnRun) {
	freshID, freshDir := r.fresh()
	fmt.Fprintf(w, "pigtail session respawn (dry-run) — nothing armed\n")
	fmt.Fprintf(w, "  subject   %s (%s)\n", r.SubjectID, r.SubjectTitle)
	fmt.Fprintf(w, "  cell      %s (%s)\n", r.Cell, r.ConfigDir)
	if r.FreshCell != "" {
		fmt.Fprintf(w, "  fresh     cell %s (%s) — %s\n", freshID, freshDir, r.rotationOrigin)
	}
	fmt.Fprintf(w, "  cwd       %s\n", r.Cwd)
	fmt.Fprintf(w, "  surface   %s (%s)\n", r.SurfaceRef, r.SurfaceID)
	fmt.Fprintf(w, "  mode      %s (launch %s, %s; current %s) — passed as --permission-mode\n", r.Mode, r.LaunchMode, r.LaunchOrigin, r.CurrentMode)
	fmt.Fprintf(w, "  prompt    %s\n", r.Prompt)
	if r.Model != "" {
		fmt.Fprintf(w, "  model     %s — passed as --model\n", r.Model)
	} else {
		fmt.Fprintf(w, "  model     cell default (no --model)\n")
	}
	if r.Relaunch == relaunchShell {
		fmt.Fprintf(w, "  launcher  none (not started by c) — relaunch typed into this tab's shell: c %s, or claude on %s without c\n", freshID, freshDir)
	} else {
		fmt.Fprintf(w, "  launcher  proto %d (%s)\n", r.LaunchProto, r.RelaunchFile)
	}
	fmt.Fprintf(w, "  idle      %s\n", r.IdleTimeout)
	if r.Bound {
		fmt.Fprintf(w, "  binding   workspace already bound to %s\n", r.SubjectID)
	} else {
		fmt.Fprintf(w, "  binding   workspace bound to %s once the fresh session is verified\n", r.SubjectID)
	}
	for _, s := range r.warnings {
		fmt.Fprintf(w, "⚠ %s\n", s)
	}
}

func printRespawnPlanJSON(w io.Writer, r respawnRun) error {
	warnings := append(append([]string{}, r.warnings...), r.rotation...)
	freshID, freshDir := r.fresh()
	b, err := json.Marshal(map[string]any{
		"dry_run": true, "subject_id": r.SubjectID, "subject_title": r.SubjectTitle,
		"cell": r.Cell, "config_dir": r.ConfigDir, "fresh_cell": freshID, "fresh_config_dir": freshDir, "cwd": r.Cwd,
		"surface_ref": r.SurfaceRef, "surface_id": r.SurfaceID, "workspace_id": r.WorkspaceID,
		"permission_mode": r.Mode, "launch_mode": r.LaunchMode, "launch_mode_origin": r.LaunchOrigin, "current_mode": r.CurrentMode,
		"prompt": r.Prompt, "model": r.Model, "launch_proto": r.LaunchProto, "relaunch_file": r.RelaunchFile, "relaunch": r.Relaunch,
		"idle_timeout": r.IdleTimeout.String(), "bound": r.Bound, "warnings": warnings,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// printRespawnArmed is the foreground's last word on a real call.
func printRespawnArmed(w io.Writer, r respawnRun, workerPID int, asJSON bool) error {
	freshID, _ := r.fresh()
	if asJSON {
		b, err := json.Marshal(map[string]any{
			"armed": true, "id": r.ID, "worker_pid": workerPID, "surface_ref": r.SurfaceRef,
			"permission_mode": r.Mode, "prompt": r.Prompt, "model": r.Model, "log": respawnLogPath(r.ID),
			"cell": r.Cell, "fresh_cell": freshID, "warnings": append([]string{}, r.rotation...),
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	model := r.Model
	if model == "" {
		model = "cell default"
	}
	_, err := fmt.Fprintf(w, "respawn armed (run %s): this session closes once idle, then a fresh one starts in %s — cell %s, mode %s, model %s, prompt: %s\n",
		r.ID, r.SurfaceRef, freshID, r.Mode, model, r.Prompt)
	return err
}

// runRespawn is the command body (spawn's runSpawn shape): it holds the
// registry lock from planRespawn to the end of the arming — so two calls can
// never both pass the already-armed check — and prints the plan (dry), the
// status (status) or the armed line.
func runRespawn(c *pigtail.Client, req respawnRequest, dry, asJSON, status bool, stdout, stderr io.Writer) error {
	if status {
		return printRespawnStatus(stdout, stderr, req.SubjectID, asJSON)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reg := spawnreg.Open(spawnreg.DefaultPath())
	unlock, err := reg.Lock()
	if err != nil {
		return spawnJSONError(stdout, retryable(retryShort, respawnErr("%v", err)), dry, asJSON)
	}
	defer unlock()
	r, err := planRespawn(ctx, c, reg, req)
	if !dry {
		recordLaunchMargin(c, r.margin, req.SubjectID, err, stderr)
	}
	if err != nil {
		return spawnJSONError(stdout, err, dry, asJSON)
	}
	for _, w := range r.rotation {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	if dry {
		if asJSON {
			return printRespawnPlanJSON(stdout, r)
		}
		printRespawnPlan(stdout, r)
		return nil
	}
	// One record, one real respawn, consumed once every guard has passed (the
	// only place): a refused or failed arm leaves it consumed — the hook
	// writes a new one on the next call.
	if err := spawnConsumeStamp(r.SessionID); err != nil {
		return spawnJSONError(stdout, respawnErr("cannot consume the permission record of session %s: %v — nothing armed", r.SessionID, err), dry, asJSON)
	}
	// The fresh session on another cell: its folder trust first, as spawn
	// does; a failure is said, never fatal (the trust dialog then shows).
	if r.FreshCell != "" {
		if pt := respawnPreTrust(ctx, c, r.FreshConfigDir, r.Cwd); pt.Outcome == claudeconfig.Failed {
			fmt.Fprintf(stderr, "warning: trust: not pre-accepted in cell %s (%s) — the fresh session may stop on the trust dialog\n", r.FreshCell, pt.Reason)
		}
	}
	r.ID = newRespawnID(respawnNow())
	pid, err := respawnArm(reg, r, stderr)
	if err != nil {
		return spawnJSONError(stdout, retryable(retryArm, err), dry, asJSON)
	}
	return printRespawnArmed(stdout, r, pid, asJSON)
}

// statusScope is the scope of this process's cell: --status needs no calling
// Claude and no permission record (it is the availability probe).
func statusScope() (string, *claudeprofile.Table, error) {
	cells, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, respawnErr("%v", err)
	}
	sc, err := resolveScope(cells, claudeConfigDir(os.Getenv), scopeOverride())
	if err != nil {
		return "", nil, respawnErr("%v", err)
	}
	return sc, cells, nil
}

// respawnStatusRows is what --status shows: the latest record per run of the
// caller's scope, and how many runs of another scope it hid. A record
// whose cell is empty or unknown is never shown nor counted (fail closed); a
// --subject of the other scope shows nothing, like an unknown subject, and
// counts its runs.
func respawnStatusRows(subjectID string) ([]spawnreg.Entry, int, error) {
	sc, cells, err := statusScope()
	if err != nil {
		return nil, 0, err
	}
	entries, err := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if err != nil {
		return nil, 0, respawnErr("%v", err)
	}
	var out []spawnreg.Entry
	hidden := 0
	for _, e := range spawnreg.Respawns(entries, subjectID) {
		cell, ok := cells.Cell(e.Cell)
		switch {
		case e.Cell == "" || !ok:
			continue
		case cell.Scope != sc:
			hidden++
			continue
		}
		out = append(out, e)
	}
	return out, hidden, nil
}

func respawnResultText(e spawnreg.Entry) string {
	if e.Result == respawnArmed && !respawnPidAlive(e.WorkerPID) {
		return "armed (worker gone)"
	}
	return e.Result
}

func printRespawnStatus(w, errw io.Writer, subjectID string, asJSON bool) error {
	rows, hidden, err := respawnStatusRows(subjectID)
	if err != nil {
		return err
	}
	if hidden > 0 {
		fmt.Fprintf(errw, "%d respawn runs of another scope hidden\n", hidden)
	}
	if asJSON {
		out := make([]map[string]any, 0, len(rows))
		for _, e := range rows {
			out = append(out, map[string]any{
				"id": e.ID, "ts": e.TS, "subject_id": e.SubjectID, "workspace_id": e.WorkspaceID,
				"surface_ref": e.SurfaceRef, "surface_id": e.SurfaceID, "cell": e.Cell,
				"permission_mode": e.Mode, "prompt": e.Prompt, "result": e.Result,
				"worker_pid": e.WorkerPID, "worker_alive": e.WorkerPID > 0 && respawnPidAlive(e.WorkerPID),
				"old_session_id": e.OldSessionID, "pid": e.PID, "new_session_id": e.NewSessionID, "detail": e.Detail,
			})
			if e.FreshCell != "" { // pinned keys unchanged for a run on one cell
				out[len(out)-1]["fresh_cell"] = e.FreshCell
			}
			if e.Model != "" { // pinned keys unchanged for a run without a model
				out[len(out)-1]["model"] = e.Model
			}
		}
		b, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "no respawn recorded")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTS\tSUBJECT\tSURFACE\tCELL\tRESULT\tPID\tNEW SESSION\tDETAIL")
	for _, e := range rows {
		pid := "-"
		if e.PID > 0 {
			pid = strconv.Itoa(e.PID)
		}
		sid := e.NewSessionID
		if sid == "" {
			sid = "-"
		}
		cell := e.Cell
		if e.FreshCell != "" && e.FreshCell != e.Cell {
			cell += "→" + e.FreshCell
		}
		if e.Model != "" {
			cell += " (" + e.Model + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.TS.Local().Format("2006-01-02 15:04:05"), e.SubjectID,
			e.SurfaceRef, cell, respawnResultText(e), pid, sid, cmux.OneLine(e.Detail, 160))
	}
	return tw.Flush()
}

func sessionRespawnCmd() *cobra.Command {
	var req respawnRequest
	var dryRun, asJSON, status bool
	var worker string
	cmd := &cobra.Command{
		Use:         "respawn --subject <id> --prompt <line>",
		Annotations: scopedWrite(),
		Short:       "Replace this Claude session by a fresh one in the same cmux tab",
		Long: `Run from a Claude session's Bash tool, arms a detached worker that waits
until the session is idle — no draft in its input, no background agent or
shell — then closes it (SIGTERM) and has the launcher start a fresh Claude in
the same cmux tab: same cell, same directory, the strictest of the launch and
current permission modes, --prompt as its first message, on --model (an alias:
sonnet, opus, haiku or fable) when given, else the cell's default model; a
launcher older than proto 4 cannot pass a model, so it is dropped with a
warning. It then verifies the
new process, rewrites cmux's resume binding (new session id and mode), binds
the workspace to the subject and records the run. The command itself returns within seconds with « respawn armed (run …) ».

Refuses, arming nothing: outside cmux or a cell; no Claude above; the caller's
environment unreadable; a config dir other than the caller's; a subject not
found, or of the other scope without --cross-scope; a caller workspace bound to
another subject; another live session of the subject elsewhere; a launcher
older than proto 2 (a CLAUDE_RELAUNCH_FILE with CLAUDE_LAUNCH_PROTO < 2); an
invalid prompt (or one with leading or trailing whitespace); no fresh
permission record; bypassPermissions; a respawn already armed for this tab; a
flag given twice, or a bool flag given a value (--dry-run=false). A real call
consumes the permission record.

A respawn starts a fresh conversation, so it goes through the launch margin
guard: the fresh session runs on the account under the usage margin whose
7-day window renews first in the caller's scope; when every account of the
scope is at or above ` + claudeusage.CeilingHelp + `, the respawn is refused
(exit 3, the first release time; the calling session stays), unless
--over-margin. A launcher older than proto 3 cannot switch account: with the
caller's own account over the margin, the respawn is refused (exit 3,
re-source c), unless --over-margin, which keeps the caller's cell.

Waits: the worker waits up to --idle-timeout (1m..2h) for an idle session,
re-checking just before closing it. Nothing is ever typed into Claude. At the timeout it
gives up: nothing closed, result « abandoned ». A session in Standby
(/standby) is never closed: the worker gives up at once, « abandoned ». If the old session does not
exit within 20 s, or the fresh one is not seen within 30 s: « failed », the
handoff withdrawn (the launcher then shows its own c: line and ↻ prompt).

A Claude the launcher did not start (neither variable: cmux resumed it) is
relaunched through its tab's shell: once it has exited and the shell is back at
its prompt, the worker types ` + "` source <run>.relaunch.zsh`" + `, which runs c <cell> in
the session's directory with the mode and the prompt; a shell not at its prompt
within 10 s → failed, nothing typed.

How it goes shows as a pill on this tab's workspace in the cmux sidebar: an
orange "Respawn · <reason>" while a draft, background work or an unreadable
screen holds it, then "Respawn · abandoned" if it gives up; a red
"Blocked · respawn failed" if the old session closed and the fresh one was not
seen. The abandoned one stays until the next prompt typed in this tab (read in
its transcript, also while Claude stays busy on background work, a prompt typed
during a turn included) or a relaunch, the failed one until a relaunch, or a
session read busy after an idle read; nothing once respawned. The details, warnings included, are in the registry (pigtail
session respawn --status [--subject <id>] [--json]) and the run log.

Needs the launcher contract (proto 2; --model needs proto 4).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if worker != "" {
				var others []string
				for _, name := range []string{"subject", "prompt", "model", "idle-timeout", "dry-run", "json", "status", "cross-scope", "over-margin"} {
					if cmd.Flags().Changed(name) {
						others = append(others, "--"+name)
					}
				}
				if len(others) > 0 {
					return respawnErr("--worker runs alone (got %s) — it is started by respawn itself, never by hand", strings.Join(others, " "))
				}
				run, err := enterWorkerMode(worker)
				if err != nil {
					return err
				}
				c, err := newScopedClient()
				if err != nil {
					return respawnErr("worker: %v", err)
				}
				if run.CrossScope {
					c = c.WithCrossScopeWrite()
				}
				return runWorkerMode(c, run, cmd.OutOrStdout())
			}
			if !status {
				var missing []string
				if req.SubjectID == "" {
					missing = append(missing, `"subject"`)
				}
				if req.Prompt == "" {
					missing = append(missing, `"prompt"`)
				}
				if len(missing) > 0 {
					return fmt.Errorf("required flag(s) %s not set", strings.Join(missing, ", "))
				}
			}
			if status {
				return runRespawn(nil, req, false, asJSON, true, cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if req.CrossScope {
				c = c.WithCrossScopeWrite()
			}
			req.DryRun = dryRun
			return runRespawn(c, req, dryRun, asJSON, false, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&req.SubjectID, "subject", "", "subject id (required, except with --status)")
	cmd.Flags().StringVar(&req.Prompt, "prompt", "", "the fresh session's first message, one line (required, except with --status)")
	cmd.Flags().StringVar(&req.Model, "model", "", "the fresh session's model alias (sonnet, opus, haiku, fable); absent = the cell's default")
	cmd.Flags().BoolVar(&req.OverMargin, "over-margin", false, "respawn even when every account of the scope is over the usage margin (decision forced)")
	cmd.Flags().DurationVar(&req.IdleTimeout, "idle-timeout", respawnIdleDefault, "how long the worker waits for an idle session before giving up (1m..2h)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "run every check, print what would be armed (as JSON with --json), arm nothing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result, the dry-run plan, the status or the refusal as JSON")
	cmd.Flags().BoolVar(&status, "status", false, "list respawn runs of this cell's scope (with --subject: of that subject); runs of another scope are counted on stderr")
	cmd.Flags().StringVar(&worker, "worker", "", "internal: run an armed respawn from its run file (started by respawn itself)")
	_ = cmd.Flags().MarkHidden("worker")
	addCrossScopeFlag(cmd, &req.CrossScope)
	strictRespawnFlags(cmd)
	return cmd
}

// flagValue is pflag.Value, named here so pflag stays an indirect dependency.
type flagValue interface {
	String() string
	Set(string) error
	Type() string
}

// strictFlag wraps a flag's value so it can be set once, and a bool flag only
// in its bare form. pflag keeps a flag's last value: `--dry-run
// --dry-run=false` would otherwise run a real respawn behind a guard that saw
// --dry-run (defense in depth behind the caller's own guard).
type strictFlag struct {
	flagValue
	name string
	sets int
}

func (f *strictFlag) Set(v string) error {
	f.sets++
	if f.sets > 1 {
		return respawnErr("--%s given more than once — pass it once, nothing done", f.name)
	}
	if f.Type() == "bool" && v != "true" {
		return respawnErr("--%s takes no value (got %q) — pass --%s alone, or leave it out; nothing done", f.name, v, f.name)
	}
	return f.flagValue.Set(v)
}

// strictRespawnFlags makes every flag of session respawn strict. A bool given
// as --flag=true reaches Set exactly as the bare --flag does: it is accepted,
// being the same request; any other value is refused.
func strictRespawnFlags(cmd *cobra.Command) {
	for _, name := range []string{"subject", "prompt", "model", "idle-timeout", "dry-run", "json", "status", "cross-scope", "worker"} {
		if fl := cmd.Flags().Lookup(name); fl != nil {
			fl.Value = &strictFlag{flagValue: fl.Value, name: name}
		}
	}
}

// Arming: the foreground hands the run to a detached worker and waits for its
// handshake (the registry record result=armed) before returning.
var (
	respawnArmTimeout = 5 * time.Second
	respawnArmPoll    = 50 * time.Millisecond
	// respawnWorkerArgv is the worker's argv after the executable; tests
	// point it at a helper.
	respawnWorkerArgv = func(_, runPath string) []string { return []string{"session", "respawn", "--worker", runPath} }
)

func init() { respawnArm = armRespawn }

// startDetachedWorkerCmd starts self with args in its own session: the
// detaching code armRespawn uses, which the tests drive with a helper argv.
func startDetachedWorkerCmd(self string, args, extraEnv []string, logPath string) (int, error) {
	pid, _, err := startDetachedProcess(self, args, extraEnv, logPath)
	return pid, err
}

// startDetachedProcess starts `<self> <args>` — for armRespawn, `session
// respawn --worker <run>` — in its own session (Setsid): no controlling
// terminal, stdin /dev/null, stdout and stderr appended to logPath. Claude
// Code kills a Bash tool's process group when the session ends; a new session
// is out of that group. The worker is reaped by its own goroutine (done is
// closed once waited for), so a killed worker never reads alive as a zombie.
func startDetachedProcess(self string, args, extraEnv []string, logPath string) (int, *detached, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, nil, err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = logf.Close() }()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = devnull.Close() }()
	cmd := exec.Command(self, args...) //nolint:gosec // self is this pigtail binary (os.Executable), args are respawn's own
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	d := &detached{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(d.done) }()
	return cmd.Process.Pid, d, nil
}

// detached is a started worker, reaped by its own goroutine.
type detached struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// kill stops the worker and waits until it is reaped.
func (d *detached) kill() {
	_ = d.cmd.Process.Kill()
	<-d.done
}

// writeRespawnRun writes the run file atomically, 0600.
func writeRespawnRun(path string, run respawnRun) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return relaunchfile.WriteAtomic(path, append(b, '\n'))
}

// readRespawnRun decodes a run file, as the --worker mode reads it.
func readRespawnRun(path string) (respawnRun, error) {
	var run respawnRun
	b, err := os.ReadFile(path)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal(b, &run); err != nil {
		return run, fmt.Errorf("run file %s: %w", path, err)
	}
	if run.Relaunch == "" {
		run.Relaunch = relaunchLauncher // a run file written before
	}
	if run.ID == "" || run.ClaudePID <= 0 || run.SurfaceID == "" || (run.Relaunch == relaunchLauncher && run.RelaunchFile == "") {
		return run, fmt.Errorf("run file %s is incomplete (id, claude pid, surface, relaunch file)", path)
	}
	return run, nil
}

// armRespawn writes the run file (0600), starts the worker, and waits up to
// respawnArmTimeout for it to append the registry record result=armed; on
// timeout it kills the worker and returns an error. The caller holds the
// registry lock; the worker's append is lock-free.
func armRespawn(reg *spawnreg.Registry, run respawnRun, stderr io.Writer) (int, error) {
	runPath, logPath := respawnRunPath(run.ID), respawnLogPath(run.ID)
	if err := writeRespawnRun(runPath, run); err != nil {
		return 0, respawnErr("write the run file: %v — nothing armed", err)
	}
	self, err := os.Executable()
	if err != nil {
		return 0, respawnErr("locate this pigtail binary: %v — nothing armed", err)
	}
	pid, d, err := startDetachedProcess(self, respawnWorkerArgv(self, runPath), nil, logPath)
	if err != nil {
		return 0, respawnErr("start the worker: %v — nothing armed", err)
	}
	deadline := time.Now().Add(respawnArmTimeout)
	for {
		if es, err := reg.Entries(); err == nil {
			for _, e := range es {
				if e.Kind == spawnreg.KindRespawn && e.ID == run.ID && e.Result == respawnArmed {
					return pid, nil
				}
			}
		}
		select {
		case <-d.done:
			return 0, respawnErr("the worker exited before arming — nothing armed (log %s)", logPath)
		default:
		}
		if time.Now().After(deadline) {
			d.kill()
			return 0, respawnErr("worker did not arm within %s — nothing armed (log %s)", respawnArmTimeout, logPath)
		}
		time.Sleep(respawnArmPoll)
	}
}

// pickRespawnModel drops --model when the tab's launcher (proto < 4) cannot
// pass it: the fresh session then runs on the cell's default model, said in
// r.rotation like the cell fallback. The shell route types `c … --model`, any
// model.
func pickRespawnModel(r *respawnRun) {
	if r.Model != "" && r.Relaunch == relaunchLauncher && r.LaunchProto < launchProtoModel {
		r.rotation = append(r.rotation, fmt.Sprintf("model: this tab's launcher speaks proto %d (a fresh session on a chosen model needs ≥ %d: re-source it) — fresh session on the cell's default model",
			r.LaunchProto, launchProtoModel))
		r.Model = ""
	}
}

// pickRespawnCell runs the account rotation for a respawn through
// the launch margin guard: the
// fresh session goes to the account the guard's pick chooses in the caller
// cell's scope (never another scope, --cross-scope included). Every account
// of the scope over the margin → refused (exit 3) before anything is armed,
// unless force (--over-margin). A pick that cannot measure keeps the caller's
// cell, said in r.rotation; so does an old launcher (proto < 3), which
// refuses a fresh relaunch on another cell — unless the caller's own account
// is over the margin: then the respawn is refused, force keeping the
// caller's cell. The shell route types `c <cell>`: any cell.
func pickRespawnCell(r *respawnRun, caller callerSession, force bool) error {
	if err := guardRespawnCell(r, caller, force); err != nil {
		return err
	}
	// --over-margin says so on stderr, like spawn.
	if r.margin != nil && r.margin.Action == launchmargin.ActionForced && r.margin.Warning != "" {
		r.rotation = append(r.rotation, r.margin.Warning)
	}
	return nil
}

func guardRespawnCell(r *respawnRun, caller callerSession, force bool) error {
	stays := "fresh session stays on " + caller.Cell.ID
	deps := launchDeps(caller.Cells, cellViewer(caller.Cell))
	cell, dec, err := launchmargin.GuardScope(deps, caller.Cell.Scope, force)
	r.margin = &dec
	var om *launchmargin.OverMarginError
	switch {
	case errors.As(err, &om):
		return withExitCode(3, respawnErr("respawn refused: %s", launchmargin.Render(dec, err)))
	case err != nil:
		r.rotation = rotationWarnings(derefChoice(dec.Choice), err, stays)
		return nil
	}
	ch := *dec.Choice
	if cell.ID == caller.Cell.ID {
		r.rotation = rotationWarnings(ch, nil, "")
		return nil
	}
	if r.Relaunch == relaunchLauncher && r.LaunchProto < launchProtoFreshCell {
		if force {
			forced := launchmargin.ForcedOn(deps, dec, caller.Cell)
			r.margin = &forced
		} else if launchmargin.AccountOver(deps, caller.Cell, dec) {
			refused, rerr := launchmargin.LauncherProtoRefusal(dec, caller.Cell)
			r.margin = &refused
			return withExitCode(3, respawnErr("%v", rerr))
		}
		r.rotation = []string{fmt.Sprintf("rotation: this tab's launcher speaks proto %d (a fresh session on another cell needs ≥ %d: re-source it) — %s",
			r.LaunchProto, launchProtoFreshCell, stays)}
		return nil
	}
	r.FreshCell, r.FreshConfigDir = cell.ID, cell.ConfigDir
	r.rotationOrigin = fmt.Sprintf(originRotation, ch.Label)
	r.rotation = rotationWarnings(ch, nil, "")
	return nil
}
