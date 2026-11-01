package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// worktreeDaemon: the real subject routes + GET /projects (one project whose
// main worktree is a real git repo's main checkout) + POST /workspaces/new
// (records the request, creates the dir, stores the workspace so
// GET /subjects/{id}/worktree finds it once bound). newErr answers with the
// real daemon's status for its code; BACKEND_ERROR is answered like a
// post-create hook failure — AFTER the dir and the store row exist
// (dispatch.go:404 persistWorkspace, then :413 the hook's 500).
func worktreeDaemon(t *testing.T, root string, newErr *pigtail.APIError) (*stubDaemon, *store.Store, *[]pigtail.NewWorkspaceRequest) {
	t.Helper()
	var got []pigtail.NewWorkspaceRequest
	proj := pigtail.Project{ID: "prj_r", Name: "r", RootPath: root, MainWorktree: "main", DefaultBackend: "git"}
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		r.Get("/projects", func(w http.ResponseWriter, _ *http.Request) { writeData(w, 200, []pigtail.Project{proj}) })
		r.Post("/workspaces/new", func(w http.ResponseWriter, req *http.Request) {
			var in pigtail.NewWorkspaceRequest
			_ = json.NewDecoder(req.Body).Decode(&in)
			got = append(got, in)
			create := func() pigtail.Workspace {
				ws := pigtail.Workspace{ID: "ws_new", ProjectID: in.ProjectID, Intent: in.Intent, State: pigtail.StatePlanning,
					Path: gitbackend.WorktreePathFor(&proj, in.BranchOverride)}
				_ = os.MkdirAll(ws.Path, 0o700)
				if err := st.CreateWorkspace(&ws); err != nil {
					t.Fatal(err)
				}
				return ws
			}
			if newErr != nil {
				status, ok := map[pigtail.ErrorCode]int{
					pigtail.ErrMaintenanceFrozen: http.StatusLocked,   // FreezeMW (handlers/maintenance.go:63,86)
					pigtail.ErrConflict:          http.StatusConflict, // statusForCode (dispatch.go:524)
					pigtail.ErrPortExhausted:     http.StatusConflict,
					pigtail.ErrProjectNotInit:    http.StatusConflict,
					pigtail.ErrBackendError:      http.StatusInternalServerError, // post-create hook (dispatch.go:413)
				}[newErr.Code]
				if !ok {
					status = http.StatusBadRequest
				}
				if newErr.Code == pigtail.ErrBackendError {
					create() // the worktree exists and is persisted before the hook fails
				}
				envelopeResponder{}.Error(w, req, status, newErr.Code, newErr.Message, nil)
				return
			}
			writeData(w, 201, create())
		})
	})
	return s, st, &got
}

func gitMain(t *testing.T, root string) string {
	main := filepath.Join(root, "main")
	for _, a := range [][]string{
		{"init", "-q", "-b", "main", main},
		{"-C", main, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	return main
}

func TestSpawnNewWorktreeBindsAndLaunches(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	root := t.TempDir()
	main := gitMain(t, root)
	s, st, got := worktreeDaemon(t, root, nil)
	f := installSpawnFake(t) // leaves spawnMainCheckoutRoot real: main is a real repo
	want := filepath.Join(root, "feat-perso-one")
	f.launchesAs(persoCellDir(t), want)
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt)
	if err != nil {
		t.Fatalf("spawn --new-worktree = %v", err)
	}
	if len(*got) != 1 || (*got)[0].BranchOverride != "feat/perso-one" || (*got)[0].ProjectID != "prj_r" {
		t.Fatalf("create request = %+v", *got)
	}
	if f.created[0].Cwd != want {
		t.Fatalf("launched in %q, want %q", f.created[0].Cwd, want)
	}
	if !strings.Contains(out, "worktree: created ws_new") || !strings.Contains(out, "bound to sub_p") {
		t.Fatalf("result = %s", out)
	}
	// AC5 « the next spawn finds it »: the daemon answers the subject's worktree…
	if ws, err := st.WorkspaceForSubject("sub_p"); err != nil || ws.ID != "ws_new" || ws.Path != want {
		t.Fatalf("GET /subjects/sub_p/worktree = %+v %v, want ws_new at %s", ws, err, want)
	}
	// …and a next spawn without --cwd launches there. The session just launched
	// (fake pid 5000, CMUX_WORKSPACE_ID=spawnNewWS, attached + in the registry)
	// would trip the live-session guard (session_spawn.go:214-233, « already has
	// a live session »): it ends first, as a real one would before a re-spawn.
	f.procs = slices.DeleteFunc(f.procs, func(p claudesession.Proc) bool { return p.PID == 5000 })
	f.created = nil
	f.stamp = "auto"
	out, _, err = runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--dry-run")
	if err != nil || !strings.Contains(out, "cwd       "+want) {
		t.Fatalf("next spawn did not find the bound worktree: %v\n%s", err, out)
	}
}

func TestSpawnNewWorktreeDryRunCreatesNothing(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	root := t.TempDir()
	main := gitMain(t, root)
	s, _, got := worktreeDaemon(t, root, nil)
	installSpawnFake(t)
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt, "--dry-run")
	if err != nil || len(*got) != 0 || strings.Contains(errOut, "creating worktree") {
		t.Fatalf("dry-run = %v, requests %+v, stderr %q (no T15 line: nothing is created)", err, *got, errOut)
	}
	if !strings.Contains(out, "worktree  would create "+filepath.Join(root, "feat-perso-one")+" · branch feat/perso-one · project prj_r · bound to sub_p") {
		t.Fatalf("dry-run text:\n%s", out)
	}
	out, _, err = runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt, "--dry-run", "--json")
	var v struct {
		NewWorktree struct {
			ProjectID   string `json:"project_id"`
			Branch      string `json:"branch"`
			Path        string `json:"path"`
			BindSubject string `json:"bind_subject"`
		} `json:"new_worktree"`
	}
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v.NewWorktree.ProjectID != "prj_r" || v.NewWorktree.Branch != "feat/perso-one" ||
		v.NewWorktree.Path != filepath.Join(root, "feat-perso-one") || v.NewWorktree.BindSubject != "sub_p" || len(*got) != 0 {
		t.Fatalf("dry-run json = %v %+v\n%s", err, v, out)
	}
}

// AC5 / § How each failure shows: every failure after the worktree exists names
// it (note()), in the text and in the --json error object (spawnJSONFailure +
// spawnResult.failureFields, #174).
func TestSpawnNewWorktreeFailuresNameTheWorktree(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *spawnFake, cfg, cwd string)
		want  string
	}{
		{"cmux down", func(_ *testing.T, f *spawnFake, _, _ string) { f.launchErr = errors.New("cmux down") }, "cmux new-workspace failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			root := t.TempDir()
			main := gitMain(t, root)
			wt := filepath.Join(root, "feat-perso-one")
			s, _, _ := worktreeDaemon(t, root, nil)
			f := installSpawnFake(t)
			c.setup(t, f, persoCellDir(t), wt)
			out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt, "--json")
			if err == nil || !strings.Contains(err.Error(), c.want) ||
				!strings.Contains(err.Error(), "worktree ws_new stays at "+wt+" (bound to sub_p): reuse it with --cwd "+wt) ||
				!strings.Contains(err.Error(), "pigtail clean ws_new") {
				t.Fatalf("%s after create = %v", c.name, err)
			}
			var v struct {
				Error       string `json:"error"`
				NewWorktree struct {
					ID, Path, Branch string
					Bound            bool
				} `json:"new_worktree"`
			}
			if jerr := json.Unmarshal([]byte(out), &v); jerr != nil || v.Error != err.Error() ||
				v.NewWorktree.ID != "ws_new" || v.NewWorktree.Path != wt || v.NewWorktree.Branch != "feat/perso-one" || !v.NewWorktree.Bound {
				t.Fatalf("json error = %+v (%v)\n%s", v, jerr, out)
			}
		})
	}
}

// Bind (phase) fails: the launch goes on, one ⚠ names the worktree, the result
// says "bound": false.
func TestSpawnNewWorktreeBindFailureWarnsAndLaunches(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	root := t.TempDir()
	main := gitMain(t, root)
	wt := filepath.Join(root, "feat-perso-one")
	s, st, _ := worktreeDaemon(t, root, nil)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), wt)
	spawnCreatePhase = func(context.Context, *pigtail.Client, string, pigtail.SubjectPhase) error {
		return errors.New("phase refused")
	} // restored by installSpawnFake's cleanup
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt, "--json")
	if err != nil {
		t.Fatalf("a bind failure must not fail the launch: %v", err)
	}
	if !strings.Contains(errOut, "⚠ worktree ws_new ("+wt+") created but not bound to sub_p (phase refused) — the next spawn will not find it: pass --cwd "+wt) {
		t.Fatalf("stderr = %q", errOut)
	}
	var v struct {
		NewWorktree struct {
			ID    string `json:"id"`
			Bound bool   `json:"bound"`
		} `json:"new_worktree"`
		Warnings []string `json:"warnings"`
	}
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil || v.NewWorktree.ID != "ws_new" || v.NewWorktree.Bound ||
		!slices.ContainsFunc(v.Warnings, func(w string) bool { return strings.Contains(w, "created but not bound to sub_p") }) {
		t.Fatalf("json = %+v (%v)\n%s", v, jerr, out)
	}
	if _, err := st.WorkspaceForSubject("sub_p"); err == nil {
		t.Fatal("the worktree was bound although the phase write failed")
	}
}

// The daemon's path is the truth (withCwd): when it differs from the predicted
// one, the launcher's dir (PIGTAIL_SPAWN_CWD in Spec.Env — launchEnv,
// session_spawn_line.go:175-176) and Spec.Cwd follow the daemon's.
func TestSpawnNewWorktreeLaunchesInTheDaemonsPath(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	root := t.TempDir()
	main := gitMain(t, root)
	predicted := filepath.Join(root, "feat-perso-one")
	actual := mk(t, filepath.Join(root, "feat-perso-one-2"))
	s, _, _ := worktreeDaemon(t, root, nil)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), actual)
	spawnCreateWorktree = func(ctx context.Context, c *pigtail.Client, req pigtail.NewWorkspaceRequest) (*pigtail.Workspace, error) {
		ws, err := c.New(ctx, req) // the store row (bindable), then the daemon "moved" it
		if err != nil {
			return nil, err
		}
		ws.Path = actual
		return ws, nil
	} // restored by installSpawnFake's cleanup
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt)
	if err != nil {
		t.Fatalf("spawn = %v", err)
	}
	spec := f.created[0]
	if spec.Cwd != actual {
		t.Fatalf("Spec.Cwd = %q, want the daemon's %q", spec.Cwd, actual)
	}
	if !slices.Contains(spec.Env, "PIGTAIL_SPAWN_CWD="+actual) || slices.Contains(spec.Env, "PIGTAIL_SPAWN_CWD="+predicted) {
		t.Fatalf("launcher dir not rebuilt: %v", spec.Env)
	}
	if n := len(slices.DeleteFunc(slices.Clone(spec.Env), func(e string) bool { return !strings.HasPrefix(e, "PIGTAIL_SPAWN_CWD=") })); n != 1 {
		t.Fatalf("%d PIGTAIL_SPAWN_CWD entries, want 1: %v", n, spec.Env)
	}
	if !strings.Contains(out, "cwd "+actual) {
		t.Fatalf("result = %s", out)
	}
}

// Spec row 15: subjectless --new-worktree --branch from another
// scope's cell creates an UNBOUND worktree and launches in it; default name =
// the branch (T11); nothing of the subject layer is touched.
func TestSpawnSubjectlessNewWorktreeUnbound(t *testing.T) {
	dirs, dev, _ := fourCells(t, "wa") // non-master work caller
	root := filepath.Join(dev, "home", "r")
	main := gitMain(t, root)
	wt := filepath.Join(root, "feat-x")
	s, st, got := worktreeDaemon(t, root, nil)
	f := installSpawnFake(t)
	f.launchesAs(dirs["hb"], wt)
	out, _, err := runSpawnCLI(t, s, "--cell", "hb", "--new-worktree", "--branch", "feat/x", "--cwd", main, "--prompt", spawnPrompt, "--dry-run", "--json")
	var v struct {
		NewWorktree struct {
			ProjectID   string `json:"project_id"`
			Branch      string `json:"branch"`
			Path        string `json:"path"`
			BindSubject string `json:"bind_subject"`
		} `json:"new_worktree"`
		Subjectless bool `json:"subjectless"`
	}
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || !v.Subjectless || v.NewWorktree.Branch != "feat/x" ||
		v.NewWorktree.Path != wt || v.NewWorktree.BindSubject != "" || v.NewWorktree.ProjectID != "prj_r" {
		t.Fatalf("--new-worktree dry-run json = %v %+v\n%s", err, v, out)
	}
	out, _, err = runSpawnCLI(t, s, "--cell", "hb", "--new-worktree", "--branch", "feat/x", "--cwd", main, "--prompt", spawnPrompt)
	if err != nil {
		t.Fatalf("--new-worktree = %v", err)
	}
	if len(*got) != 1 || (*got)[0].BranchOverride != "feat/x" || (*got)[0].ProjectID != "prj_r" {
		t.Fatalf("create request = %+v", *got)
	}
	if spec := f.created[0]; spec.Cwd != wt || spec.Name != "feat/x" || !strings.Contains(spec.Command, " c hb ") {
		t.Fatalf("launch = cwd %q name %q command %q", spec.Cwd, spec.Name, spec.Command)
	}
	if !strings.Contains(out, "worktree: created ws_new") || !strings.Contains(out, "unbound") {
		t.Fatalf("result = %s", out)
	}
	if ps, err := st.ListPhasesBySubject("sub_p"); err != nil || len(ps) != 0 {
		t.Fatalf("a subjectless spawn wrote a phase: %+v %v", ps, err)
	}
	if got := subjectCalls(s); len(got) != 0 { // no live entry at either call: no tally read
		t.Errorf("touched a subject route: %v", got)
	}
	es := spawnEntries(t)
	if len(es) != 1 || es[0].SubjectID != "" || es[0].Cell != "hb" || es[0].Cwd != wt {
		t.Fatalf("registry = %+v", es)
	}
}

// § How each failure shows: a clean daemon refusal (nothing created) says « not
// created » with ITS next step; --branch only for a collision (409 CONFLICT, the
// code gitBackend.Create's collision guard really returns, git.go:164-181).
func TestSpawnNewWorktreeCreateRefused(t *testing.T) {
	cases := []struct {
		name   string
		code   pigtail.ErrorCode
		msg    string
		want   string
		forbid string
	}{
		{"branch collision", pigtail.ErrConflict, `branch "feat/perso-one" already exists`, "pass --branch <another name>", ""},
		{"no free port", pigtail.ErrPortExhausted, `no free port in range 3000-3099 for kind "dev"`, "pigtail clean --merged", "--branch"},
		{"project not initialised", pigtail.ErrProjectNotInit, "project not initialised", "pigtail init", "--branch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			root := t.TempDir()
			main := gitMain(t, root)
			s, _, got := worktreeDaemon(t, root, &pigtail.APIError{Code: c.code, Message: c.msg})
			f := installSpawnFake(t)
			_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt)
			if err == nil || !strings.Contains(err.Error(), "worktree not created") || !strings.Contains(err.Error(), "nothing launched") ||
				!strings.Contains(err.Error(), c.msg) || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "may exist") ||
				(c.forbid != "" && strings.Contains(err.Error(), c.forbid)) || len(*got) != 1 || len(f.created) != 0 {
				t.Fatalf("%s = %v, requests %d, created %d", c.name, err, len(*got), len(f.created))
			}
		})
	}
}

// A post-create hook failure answers 500 AFTER the worktree is persisted
// (dispatch.go:404,413): never « not created », never « pass --branch » — the
// predicted path and branch, and how to find and remove it.
func TestSpawnNewWorktreeHookFailureMayExist(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	root := t.TempDir()
	main := gitMain(t, root)
	wt := filepath.Join(root, "feat-perso-one")
	s, st, _ := worktreeDaemon(t, root, &pigtail.APIError{Code: pigtail.ErrBackendError, Message: "post-create hook: npm ci exited 1"})
	f := installSpawnFake(t)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt)
	if err == nil || !strings.Contains(err.Error(), "worktree creation did not complete") || !strings.Contains(err.Error(), "post-create hook: npm ci exited 1") ||
		!strings.Contains(err.Error(), "a worktree may exist at "+wt+" (branch feat/perso-one)") ||
		!strings.Contains(err.Error(), "pigtail list") || !strings.Contains(err.Error(), "pigtail clean") ||
		strings.Contains(err.Error(), "not created") || strings.Contains(err.Error(), "--branch") || len(f.created) != 0 {
		t.Fatalf("hook failure after create = %v, created %d", err, len(f.created))
	}
	if ws, gerr := st.GetWorkspace("ws_new"); gerr != nil || ws.Path != wt {
		t.Fatalf("the stub did not persist the worktree first: %+v %v", ws, gerr)
	}
}

// Client timeout: the create runs under devserver.NewWorkspaceTimeout (pigtail new's
// budget, not the run's 2 min), and a timeout says « may exist » — a client
// that goes still cancels the worktree creation itself (Backend.Create runs on
// r.Context()), but once the worktree exists its post-create hooks run on to
// their end on the daemon's context, their outcome shown by pigtail list.
func TestSpawnNewWorktreeTimeoutMayExist(t *testing.T) {
	if devserver.NewWorkspaceTimeout < devserver.HooksTimeout+time.Minute {
		t.Fatalf("devserver.NewWorkspaceTimeout %v does not cover the hooks' own bound %v", devserver.NewWorkspaceTimeout, devserver.HooksTimeout)
	}
	withCells(t, func(_, perso string) string { return perso })
	root := t.TempDir()
	main := gitMain(t, root)
	wt := filepath.Join(root, "feat-perso-one")
	s, _, _ := worktreeDaemon(t, root, nil)
	f := installSpawnFake(t)
	var left time.Duration
	spawnCreateWorktree = func(ctx context.Context, _ *pigtail.Client, _ pigtail.NewWorkspaceRequest) (*pigtail.Workspace, error) {
		if d, ok := ctx.Deadline(); ok {
			left = time.Until(d)
		}
		return nil, &url.Error{Op: "Post", URL: "http://pigtail/workspaces/new", Err: context.DeadlineExceeded}
	} // restored by installSpawnFake's cleanup
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt)
	if left < devserver.NewWorkspaceTimeout-30*time.Second {
		t.Fatalf("create deadline in %v, want about devserver.NewWorkspaceTimeout (%v)", left, devserver.NewWorkspaceTimeout)
	}
	if err == nil || !strings.Contains(err.Error(), "worktree creation did not complete") ||
		!strings.Contains(err.Error(), "a worktree may exist at "+wt+" (branch feat/perso-one)") || !strings.Contains(err.Error(), "pigtail clean") ||
		strings.Contains(err.Error(), "not created") || strings.Contains(err.Error(), "--branch") || len(f.created) != 0 {
		t.Fatalf("timeout = %v, created %d", err, len(f.created))
	}
}

// T15 / AC5: a create killed by its caller (Ctrl-C, kill) prints no error at
// all, so the predicted path, branch and find/clean step go to stderr BEFORE the
// request — text and --json alike, stdout staying one JSON object. Ordering pinned:
// the create seam records what stderr already held when the create was asked.
func TestSpawnNewWorktreeAnnouncesBeforeCreating(t *testing.T) {
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			root := t.TempDir()
			main := gitMain(t, root)
			wt := filepath.Join(root, "feat-perso-one")
			s, _, _ := worktreeDaemon(t, root, nil)
			f := installSpawnFake(t)
			var o, e bytes.Buffer
			atCreate, asked := "", 0
			spawnCreateWorktree = func(context.Context, *pigtail.Client, pigtail.NewWorkspaceRequest) (*pigtail.Workspace, error) {
				asked++
				atCreate = e.String() // executeSpawn's goroutine: no race
				return nil, &url.Error{Op: "Post", URL: "http://pigtail/workspaces/new", Err: context.Canceled}
			} // restored by installSpawnFake's cleanup
			args := []string{"spawn", "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt}
			if mode == "json" {
				args = append(args, "--json")
			}
			var err error
			withStubDaemon(t, s, func() { // runSpawnCLI's body, with the buffers in hand
				cmd := sessionCmd()
				silenceUsageOnRunE(cmd)
				cmd.SetOut(&o)
				cmd.SetErr(&e)
				cmd.SetArgs(args)
				err = cmd.Execute()
			})
			want := "creating worktree " + wt + " (branch feat/perso-one) — "
			if asked != 1 || !strings.Contains(atCreate, want) || !strings.Contains(atCreate, "pigtail list") ||
				!strings.Contains(atCreate, "pigtail clean <id>") || !strings.Contains(atCreate, "--cwd "+wt) {
				t.Fatalf("stderr when the create was asked (%d calls) = %q, want %q before it", asked, atCreate, want)
			}
			if strings.Count(e.String(), "creating worktree ") != 1 || strings.Contains(o.String(), "creating worktree") {
				t.Fatalf("the T15 line goes to stderr once: stdout %q stderr %q", o.String(), e.String())
			}
			if mode == "json" {
				var v map[string]any
				if jerr := json.Unmarshal(o.Bytes(), &v); jerr != nil {
					t.Fatalf("--json stdout is not one JSON object: %v\n%s", jerr, o.String())
				}
			}
			if err == nil || !strings.Contains(err.Error(), "may exist at "+wt) || len(f.created) != 0 {
				t.Fatalf("err = %v, created %d", err, len(f.created))
			}
		})
	}
}

// T16, rewritten: the real spawn takes the registry lock
// only for the short check before the create. Blocked on it, it says so once on
// stderr past spawnLockNotice — the new wording, no « 7 min », no Bash timeout —
// then succeeds; uncontended, it says nothing. flock is per open file, so a
// lock held by the test blocks this process's spawn.
func TestSpawnSaysItWaitsForTheRegistryLock(t *testing.T) {
	for _, contended := range []bool{false, true} {
		t.Run(fmt.Sprintf("contended=%v", contended), func(t *testing.T) {
			cwd := t.TempDir()
			withCells(t, func(_, perso string) string { return perso })
			s, _ := realSubjectDaemon(t)
			f := installSpawnFake(t)
			f.launchesAs(persoCellDir(t), cwd)
			spawnLockNotice = 20 * time.Millisecond // restored by installSpawnFake's cleanup
			if contended {
				unlock, err := spawnreg.Open(spawnreg.DefaultPath()).Lock()
				if err != nil {
					t.Fatal(err)
				}
				time.AfterFunc(300*time.Millisecond, unlock)
			}
			out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--json")
			if err != nil {
				t.Fatalf("spawn = %v, stderr %q", err, errOut)
			}
			n := strings.Count(errOut, "session spawn: waiting for the spawn registry lock")
			if !contended {
				if n != 0 {
					t.Fatalf("uncontended stderr %q", errOut)
				}
			} else if n != 1 || !strings.Contains(errOut, "is checking the registry") || !strings.Contains(errOut, "nothing is created while waiting") ||
				strings.Contains(errOut, "7 min") || strings.Contains(errOut, "Bash timeout") {
				t.Fatalf("contended stderr %q", errOut)
			}
			var v map[string]any
			if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
				t.Fatalf("--json stdout is not one JSON object: %v\n%s", jerr, out)
			}
		})
	}
}

// --dry-run takes no lock: an order check, not a duration — the lock is held
// until the dry-run has finished, with a wide margin before failing.
func TestSpawnDryRunTakesNoLock(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	spawnLockNotice = 20 * time.Millisecond
	reg := spawnreg.Open(spawnreg.DefaultPath())
	unlock, err := reg.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	type result struct {
		errOut string
		err    error
	}
	done := make(chan result, 1)
	args := []string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"}
	withStubDaemon(t, s, func() {
		go func() {
			var o, e bytes.Buffer
			cmd := sessionCmd()
			silenceUsageOnRunE(cmd)
			cmd.SetOut(&o)
			cmd.SetErr(&e)
			cmd.SetArgs(append([]string{"spawn"}, args...))
			err := cmd.Execute()
			done <- result{e.String(), err}
		}()
		select {
		case r := <-done:
			if r.err != nil || strings.Contains(r.errOut, "waiting for the spawn registry lock") {
				t.Fatalf("dry-run = %v, stderr %q", r.err, r.errOut)
			}
		case <-time.After(10 * time.Second):
			unlock()
			<-done
			t.Fatal("the dry-run waited for the registry lock")
		}
	})
	if es, _ := reg.Entries(); len(es) != 0 {
		t.Fatalf("dry-run wrote the registry: %+v", es)
	}
}

// § How each failure shows, « freeze set between plan and create »: the daemon's
// 423 on /workspaces/new gets its own next step, not the collision's.
func TestSpawnNewWorktreeFrozenMidRun(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	root := t.TempDir()
	main := gitMain(t, root)
	s, _, got := worktreeDaemon(t, root, &pigtail.APIError{Code: pigtail.ErrMaintenanceFrozen, Message: "maintenance on: the sync tool"})
	f := installSpawnFake(t) // PIGTAIL_MAINTENANCE_DIR = an empty dir (session_spawn_test.go:64): spawnFrozen passes
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt)
	if err == nil || !strings.Contains(err.Error(), "worktree not created") || !strings.Contains(err.Error(), "maintenance on: the sync tool") ||
		!strings.Contains(err.Error(), "nothing launched; retry once the maintenance is lifted") ||
		strings.Contains(err.Error(), "--branch") || len(*got) != 1 || len(f.created) != 0 {
		t.Fatalf("frozen mid-run = %v, requests %d, created %d", err, len(*got), len(f.created))
	}
}

func TestSpawnNewWorktreeRefusals(t *testing.T) {
	cases := []struct {
		name  string
		args  func(main, linked, plain string) []string
		setup func(t *testing.T, st *store.Store, root string)
		want  string
	}{
		{"shared main checkout without the flag", func(main, _, _ string) []string { return []string{"--subject", "sub_p", "--cwd", main} }, nil,
			"pass --new-worktree"},
		{"branch without the flag", func(main, _, _ string) []string {
			return []string{"--subject", "sub_p", "--cwd", main, "--branch", "x/y"}
		}, nil,
			"--branch only names the branch --new-worktree creates"},
		{"linked worktree", func(_, linked, _ string) []string {
			return []string{"--subject", "sub_p", "--new-worktree", "--cwd", linked}
		}, nil,
			"is a linked worktree"},
		{"not a repo", func(_, _, plain string) []string {
			return []string{"--subject", "sub_p", "--new-worktree", "--cwd", plain}
		}, nil,
			"is not in a git repository"},
		{"no cwd", func(_, _, _ string) []string { return []string{"--subject", "sub_p", "--new-worktree"} }, nil,
			"--new-worktree needs --cwd <repo main checkout>"},
		{"subject already has one", func(main, _, _ string) []string {
			return []string{"--subject", "sub_p", "--new-worktree", "--cwd", main}
		},
			func(t *testing.T, st *store.Store, root string) {
				p := filepath.Join(root, "feat-old")
				_ = os.MkdirAll(p, 0o700)
				_ = st.CreateWorkspace(&pigtail.Workspace{ID: "ws_old", ProjectID: "prj_r", Path: p, State: pigtail.StateBuilding})
				_ = st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_1", SubjectID: "sub_p", Kind: "implement", State: "active", WorkspaceID: "ws_old", CreatedAt: time.Now()})
			}, "already has worktree ws_old"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			root := t.TempDir()
			main := gitMain(t, root)
			linked := filepath.Join(root, "feat-x")
			if out, err := exec.Command("git", "-C", main, "worktree", "add", "-q", "-b", "feat/x", linked).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			s, st, got := worktreeDaemon(t, root, nil)
			if c.setup != nil {
				c.setup(t, st, root)
			}
			f := installSpawnFake(t)
			f.mainCk = true // the main checkout has a linked worktree: shared
			args := append(c.args(main, linked, t.TempDir()), "--prompt", spawnPrompt)
			_, _, err := runSpawnCLI(t, s, args...)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), " — ") {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if len(*got) != 0 || len(f.created) != 0 {
				t.Fatal("a refusal created something")
			}
		})
	}
}

// R19 lists the repo's linked worktrees under the target cell's home (at most 5,
// sorted, "+N more"), never the main checkout nor a worktree under another scope's home
// (T14: relevance — only what the target cell works in). Text and --json.
func TestSpawnNewWorktreeNotAProjectListsWorktrees(t *testing.T) {
	_, dev, work := fourCells(t, "ha") // target = ha (sub_p is perso), home dev
	root := filepath.Join(dev, "home", "r")
	main := gitMain(t, root)
	var want []string
	for i := 0; i < 6; i++ {
		wt := filepath.Join(root, "w"+strconv.Itoa(i))
		if out, err := exec.Command("git", "-C", main, "worktree", "add", "-q", "-b", "w"+strconv.Itoa(i), wt).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		r, _ := filepath.EvalSymlinks(wt)
		want = append(want, r)
	}
	other := filepath.Join(work, "feat-other-scope-wt")
	if out, err := exec.Command("git", "-C", main, "worktree", "add", "-q", "-b", "other", other).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s, _, got := worktreeDaemon(t, t.TempDir(), nil) // its one project is elsewhere
	f := installSpawnFake(t)
	args := []string{"--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt}
	_, _, err := runSpawnCLI(t, s, args...)
	wantList := "pass one of its worktrees with --cwd: " + strings.Join(want[:5], ", ") + " (+1 more)"
	if err == nil || !strings.Contains(err.Error(), "is not the main worktree of a registered pigtail project") ||
		!strings.Contains(err.Error(), wantList) || !strings.Contains(err.Error(), " — ") {
		t.Fatalf("R19 = %v, want %q", err, wantList)
	}
	rm, _ := filepath.EvalSymlinks(main)
	if strings.Contains(err.Error(), "other-scope-wt") || strings.Contains(err.Error(), rm+",") || strings.Contains(err.Error(), want[5]) {
		t.Fatalf("R19 lists a filtered, main or 6th path: %v", err)
	}
	out, _, jerr := runSpawnCLI(t, s, append(args, "--json")...)
	var v struct {
		Error string `json:"error"`
	}
	if jerr == nil || json.Unmarshal([]byte(out), &v) != nil || v.Error != jerr.Error() || !strings.Contains(v.Error, wantList) {
		t.Fatalf("R19 --json = %v %+v\n%s", jerr, v, out)
	}
	if len(*got) != 0 || len(f.created) != 0 {
		t.Fatal("R19 created something")
	}
}

func TestSpawnNewWorktreeNotAProjectNoWorktree(t *testing.T) {
	_, dev, _ := fourCells(t, "ha")
	main := gitMain(t, filepath.Join(dev, "home", "r")) // no linked worktree
	s, _, got := worktreeDaemon(t, t.TempDir(), nil)
	f := installSpawnFake(t)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt)
	if err == nil || !strings.Contains(err.Error(), "has no linked worktree under cell ha's home") ||
		!strings.Contains(err.Error(), "worktree add") || !strings.Contains(err.Error(), "pigtail init") {
		t.Fatalf("R19 empty = %v", err)
	}
	if len(*got) != 0 || len(f.created) != 0 {
		t.Fatal("R19 created something")
	}
}

// R19 without H1: a table with no cwd_home cannot run the T14 filter — R13's text,
// never a false « no linked worktree under cell <c>'s home ».
func TestSpawnNewWorktreeNotAProjectWithoutHomes(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso }) // no cwd_home
	root := t.TempDir()
	main := gitMain(t, root)
	if out, err := exec.Command("git", "-C", main, "worktree", "add", "-q", "-b", "w0", filepath.Join(root, "w0")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s, _, got := worktreeDaemon(t, t.TempDir(), nil) // its one project is elsewhere
	f := installSpawnFake(t)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--new-worktree", "--cwd", main, "--prompt", spawnPrompt)
	if err == nil || !strings.Contains(err.Error(), "claude-cells.yaml carries no cwd_home") ||
		strings.Contains(err.Error(), "has no linked worktree") || !strings.Contains(err.Error(), " — ") {
		t.Fatalf("R19 without homes = %v", err)
	}
	if len(*got) != 0 || len(f.created) != 0 {
		t.Fatal("R19 created something")
	}
}

// TestNewWorkspaceTimeoutWithinReserveCap ties spawnreg's local ReserveCap to
// the create's own budget: a reserve without launch-ref stays in progress as
// long as a --new-worktree create may run, plus the margin. It fails if one
// moves alone.
func TestNewWorkspaceTimeoutWithinReserveCap(t *testing.T) {
	t.Parallel()
	if devserver.NewWorkspaceTimeout+spawnreg.ReserveMargin != spawnreg.ReserveCap {
		t.Fatalf("newWorkspaceTimeout %v + ReserveMargin %v != spawnreg.ReserveCap %v", devserver.NewWorkspaceTimeout, spawnreg.ReserveMargin, spawnreg.ReserveCap)
	}
}
