package launch

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/internal/sessionlaunch"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	winA = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	winB = "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
	wsX  = "CCCCCCCC-CCCC-4CCC-8CCC-CCCCCCCCCCCC"
)

// fakeStore is the launcher's store, in memory.
type fakeStore struct {
	mu       sync.Mutex
	subjects map[string]pigtail.Subject
	ws       map[string]*pigtail.Workspace
	wsErr    error
	bindings map[string][]pigtail.SubjectCmuxBinding
	peers    []pigtail.PeerSession
	peersErr []error // returned in turn by ListPeers, then nil
	// peersFn, when set, answers ListPeers (after peersErr).
	peersFn func() []pigtail.PeerSession
}

func (s *fakeStore) GetSubject(id string) (*pigtail.Subject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subjects[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &sub, nil
}

func (s *fakeStore) WorkspaceForSubject(id string) (*pigtail.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsErr != nil {
		return nil, s.wsErr
	}
	if w, ok := s.ws[id]; ok {
		return w, nil
	}
	return nil, store.ErrNotFound
}

func (s *fakeStore) ListCmuxBindings(id string) ([]pigtail.SubjectCmuxBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bindings[id], nil
}

func (s *fakeStore) ListPeers(bool) ([]pigtail.PeerSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.peersErr) > 0 {
		err := s.peersErr[0]
		s.peersErr = s.peersErr[1:]
		return nil, err
	}
	if s.peersFn != nil {
		fn := s.peersFn
		s.mu.Unlock()
		defer s.mu.Lock()
		return fn(), nil
	}
	return append([]pigtail.PeerSession(nil), s.peers...), nil
}

type harness struct {
	t         *testing.T
	f         *fakeCmux
	st        *fakeStore
	reg       *spawnreg.Registry
	regFile   string
	d         Deps
	sub       pigtail.Subject
	cell      claudeprofile.Cell
	launchDir string
	mu        sync.Mutex
	binds     []string
	events    []pigtail.Event
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, f: newFakeCmux(winA)}
	h.sub = pigtail.Subject{ID: "sub_x", Title: "Web launch", Scope: "perso"}
	h.st = &fakeStore{subjects: map[string]pigtail.Subject{h.sub.ID: h.sub}, ws: map[string]*pigtail.Workspace{}, bindings: map[string][]pigtail.SubjectCmuxBinding{}}
	h.regFile = filepath.Join(t.TempDir(), "spawns.jsonl")
	h.reg = spawnreg.Open(h.regFile)
	h.cell = claudeprofile.Cell{ID: "mp", ConfigDir: t.TempDir(), Scope: "perso"}
	h.launchDir = t.TempDir()
	h.d = Deps{
		Store:     h.st,
		Registry:  h.reg,
		Cell:      func(string) (claudeprofile.Cell, error) { return h.cell, nil },
		LaunchDir: func(sc string) (string, bool) { return h.launchDir, sc == "perso" },
		Mode:      func(string) (string, error) { return "auto", nil },
		Procs:     func() ([]claudesession.Proc, error) { return nil, nil },
		Environ:   func(int) (string, error) { return "", nil },
		Alive:     func(pid int) bool { return pid == os.Getpid() },
		Bind: func(sub, ws string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.binds = append(h.binds, sub+" "+ws)
			return nil
		},
		Publish: func(e pigtail.Event) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.events = append(h.events, e)
		},
		Logf: t.Logf,
	}
	h.f.wire(&h.d)
	return h
}

// launcher builds a Launcher on a daemon context cancelled, then waited for,
// in t.Cleanup: registered after the TempDirs, so it runs before their removal
// and no confirm goroutine writes into the registry once the test is over.
func (h *harness) launcher() *Launcher {
	h.t.Helper()
	l := New(h.d)
	ctx, cancel := context.WithCancel(context.Background())
	l.Start(ctx)
	h.t.Cleanup(func() { cancel(); l.Wait() })
	return l
}

func (h *harness) launch() (pigtail.SessionLaunch, error) {
	return h.launcher().Launch(context.Background(), h.sub)
}

func refusalOf(t *testing.T, err error, code pigtail.ErrorCode, status int) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v (%T), want a %s refusal", err, err, code)
	}
	if r.Code != code || r.Status != status {
		t.Fatalf("refusal = %s/%d (%s), want %s/%d", r.Code, r.Status, r.Msg, code, status)
	}
	if r.Details["subject_id"] != "sub_x" {
		t.Fatalf("details lack subject_id: %v", r.Details)
	}
	return r
}

func TestLaunchCreatesPendingTab(t *testing.T) {
	h := newHarness(t)
	got, err := h.launch()
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pigtail.LaunchPending || got.Origin != "api" || got.Deduplicated {
		t.Fatalf("launch = %+v", got)
	}
	if !strings.HasPrefix(got.LaunchID, "ln_") || got.WorkspaceID == "" || got.WorkspaceRef == "" {
		t.Fatalf("launch lacks its id or tab: %+v", got)
	}
	if got.WorkspaceID != strings.ToUpper(got.WorkspaceID) {
		t.Fatalf("workspace id not upper case: %s", got.WorkspaceID)
	}
	if got.Cell != "mp" || got.PermissionMode != "auto" || got.Cwd != h.launchDir {
		t.Fatalf("launch = %+v", got)
	}
	if n := h.f.creates(); n != 1 {
		t.Fatalf("%d NewWorkspace, want 1", n)
	}
	if len(h.binds) != 1 || h.binds[0] != "sub_x "+got.WorkspaceID {
		t.Fatalf("binds = %v", h.binds)
	}
	if spec := h.f.lastSpec(); spec.Name != pigtail.ShortTitle(h.sub) {
		t.Fatalf("tab name = %q", spec.Name)
	}
}

func TestLaunchUsesWorktreeElseLaunchDir(t *testing.T) {
	t.Run("worktree", func(t *testing.T) {
		h := newHarness(t)
		wt := t.TempDir()
		h.st.ws["sub_x"] = &pigtail.Workspace{ID: "ws1", Path: wt}
		var trusted string
		h.d.PreTrust = func(cfg, p string) { trusted = cfg + "|" + p }
		got, err := h.launch()
		if err != nil {
			t.Fatal(err)
		}
		if got.Cwd != wt || h.f.lastSpec().Cwd != wt {
			t.Fatalf("cwd = %s, want the worktree %s", got.Cwd, wt)
		}
		if trusted != h.cell.ConfigDir+"|"+wt {
			t.Fatalf("pre-trust = %q", trusted)
		}
	})
	t.Run("worktree gone", func(t *testing.T) {
		h := newHarness(t)
		h.st.ws["sub_x"] = &pigtail.Workspace{ID: "ws1", Path: filepath.Join(t.TempDir(), "gone")}
		h.d.PreTrust = func(string, string) { t.Error("pre-trust of a launch_dir") }
		got, err := h.launch()
		if err != nil {
			t.Fatal(err)
		}
		if got.Cwd != h.launchDir {
			t.Fatalf("cwd = %s, want the launch_dir", got.Cwd)
		}
	})
	t.Run("no worktree", func(t *testing.T) {
		h := newHarness(t)
		got, err := h.launch()
		if err != nil {
			t.Fatal(err)
		}
		if got.Cwd != h.launchDir {
			t.Fatalf("cwd = %s, want the launch_dir", got.Cwd)
		}
	})
	t.Run("neither", func(t *testing.T) {
		h := newHarness(t)
		h.d.LaunchDir = func(string) (string, bool) { return "", false }
		_, err := h.launch()
		r := refusalOf(t, err, pigtail.ErrNoLaunchDir, http.StatusConflict)
		if r.Details["scope"] != "perso" {
			t.Fatalf("details = %v", r.Details)
		}
		if h.f.creates() != 0 {
			t.Fatal("a tab was created")
		}
	})
	t.Run("launch_dir missing on disk", func(t *testing.T) {
		h := newHarness(t)
		h.launchDir = filepath.Join(t.TempDir(), "nope")
		_, err := h.launch()
		refusalOf(t, err, pigtail.ErrNoLaunchDir, http.StatusConflict)
	})
}

func TestLaunchRefusesNoCell(t *testing.T) {
	t.Run("scope without a cell", func(t *testing.T) {
		h := newHarness(t)
		h.d.Cell = func(string) (claudeprofile.Cell, error) { return claudeprofile.Cell{}, ErrNoCell }
		_, err := h.launch()
		r := refusalOf(t, err, pigtail.ErrNoCell, http.StatusConflict)
		if r.Details["scope"] != "perso" {
			t.Fatalf("details = %v", r.Details)
		}
		if h.f.creates() != 0 {
			t.Fatal("a tab was created")
		}
	})
	t.Run("config dir missing", func(t *testing.T) {
		h := newHarness(t)
		h.cell.ConfigDir = filepath.Join(t.TempDir(), "absent")
		_, err := h.launch()
		refusalOf(t, err, pigtail.ErrNoCell, http.StatusConflict)
		if h.f.creates() != 0 {
			t.Fatal("a tab was created")
		}
	})
}

// cArgv runs the launch line's inner script under zsh -f with a stub c that
// prints its argv (pattern of TestLaunchLineExecutes).
func cArgv(t *testing.T, spec cmux.WorkspaceSpec) string {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	inner := strings.Replace(innerScript(t, spec.Command), sessionlaunch.LaunchTail, "exit 0", 1)
	stub := `c() { print -r -- "GATE=${PIGTAIL_GATE_PASSED-unset}"; for a in "$@"; do print -r -- "ARG=$a"; done }; `
	cmd := exec.Command(zsh, "-f", "-c", "C_LAUNCH_PROTO=1; "+stub+inner)
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, sessionlaunch.GatePassedVar+"=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, spec.Env...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// innerScript undoes the outer `/bin/zsh -i -c '<inner>'` quoting (a copy of
// cmd/pigtail's helper).
func innerScript(t *testing.T, line string) string {
	t.Helper()
	const pfx = "/bin/zsh -i -c "
	if !strings.HasPrefix(line, pfx) {
		t.Fatalf("unexpected line %q", line)
	}
	out, err := exec.Command("/bin/sh", "-c", `printf %s `+strings.TrimPrefix(line, pfx)).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestLaunchPassesCellDefaultMode(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{{"auto", "auto"}, {"", "default"}, {"plan", "plan"}} {
		t.Run("mode "+tc.mode, func(t *testing.T) {
			h := newHarness(t)
			h.d.Mode = func(string) (string, error) { return tc.mode, nil }
			got, err := h.launch()
			if err != nil {
				t.Fatal(err)
			}
			if got.PermissionMode != tc.want {
				t.Fatalf("mode = %q, want %q", got.PermissionMode, tc.want)
			}
			out := cArgv(t, h.f.lastSpec())
			want := "GATE=unset\nARG=mp\nARG=--permission-mode\nARG=" + tc.want + "\nARG=" + usersettings.Defaults().LaunchPrompt("sub_x", "Web launch") + "\n"
			if out != want {
				t.Fatalf("c saw\n%s\nwant\n%s", out, want)
			}
		})
	}
	t.Run("bypass", func(t *testing.T) {
		h := newHarness(t)
		h.d.Mode = func(string) (string, error) { return "bypassPermissions", nil }
		_, err := h.launch()
		r := refusalOf(t, err, pigtail.ErrBypassRefused, http.StatusForbidden)
		if r.Details["cell"] != "mp" || h.f.creates() != 0 {
			t.Fatalf("details = %v, creates = %d", r.Details, h.f.creates())
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.d.Mode = func(string) (string, error) { return "", errors.New("parse settings.json: bad") }
		_, err := h.launch()
		r := refusalOf(t, err, pigtail.ErrCellModeUnreadable, http.StatusServiceUnavailable)
		if r.Details["cell"] != "mp" || r.Details["cause"] == "" || h.f.creates() != 0 {
			t.Fatalf("details = %v", r.Details)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		h := newHarness(t)
		h.d.Mode = func(string) (string, error) { return "yolo", nil }
		_, err := h.launch()
		refusalOf(t, err, pigtail.ErrCellModeUnreadable, http.StatusServiceUnavailable)
	})
}

func TestLaunchHasNoGatePrefix(t *testing.T) {
	h := newHarness(t)
	if _, err := h.launch(); err != nil {
		t.Fatal(err)
	}
	spec := h.f.lastSpec()
	if strings.Contains(spec.Command, sessionlaunch.GatePassedVar) {
		t.Fatalf("the line names %s: %s", sessionlaunch.GatePassedVar, spec.Command)
	}
	for _, e := range spec.Env {
		if strings.HasPrefix(e, sessionlaunch.GatePassedVar+"=") {
			t.Fatalf("env carries %s", e)
		}
	}
}

func TestLaunchPromptNonASCIIByEnv(t *testing.T) {
	h := newHarness(t)
	h.sub.ID = "sub_é"
	h.sub.Title = "Café « é »"
	h.st.subjects[h.sub.ID] = h.sub
	if _, err := h.launch(); err != nil {
		t.Fatal(err)
	}
	spec := h.f.lastSpec()
	for i := 0; i < len(spec.Command); i++ {
		if spec.Command[i] >= 0x80 {
			t.Fatalf("non-ASCII byte in the line: %s", spec.Command)
		}
	}
	found := false
	for _, e := range spec.Env {
		if e == sessionlaunch.EnvSpawnPrompt+"="+usersettings.Defaults().LaunchPrompt("sub_é", "Café « é »") {
			found = true
		}
	}
	if !found {
		t.Fatalf("prompt not in env: %q", spec.Env)
	}
	if !strings.Contains(strings.Join(spec.Env, "\n"), "CLAUDE_CONFIG_DIR="+h.cell.ConfigDir) {
		t.Fatalf("env lacks the cell: %q", spec.Env)
	}
}

func TestLaunchPlacesAtFolderBottom(t *testing.T) {
	h := newHarness(t)
	h.f.windows = []string{winA, winB}
	h.f.groups[winA] = []cmux.Group{{Name: "Build", Ref: "workspace_group:3", Pinned: true}}
	h.f.groups[winB] = []cmux.Group{{Name: "Inbox", Ref: "workspace_group:5"}, {Name: "Build", Ref: "workspace_group:7"}}
	h.d.Folders = func() (bool, []string) { return true, []string{winA, winB} }
	h.d.FolderOrder = func() []string { return []string{"Build", "Inbox"} }
	var gotOrder []string
	h.d.Folder = func(sub string, folders []string) (string, error) {
		gotOrder = folders
		return "Build", nil
	}
	if _, err := h.launch(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotOrder, ",") != "Build,Inbox" {
		t.Fatalf("folder order = %v", gotOrder)
	}
	args := strings.Join(cmux.NewWorkspaceArgs(h.f.lastSpec()), " ")
	if !strings.Contains(args, "--window "+winB+" --group workspace_group:7 --group-placement end") {
		t.Fatalf("argv = %s", args)
	}
}

func TestLaunchFoldersOffMakesNoFolderCall(t *testing.T) {
	h := newHarness(t)
	h.d.Folders = func() (bool, []string) { return false, []string{winA} }
	h.d.Folder = func(string, []string) (string, error) {
		t.Error("folder computed with the folders off")
		return "", nil
	}
	if _, err := h.launch(); err != nil {
		t.Fatal(err)
	}
	if h.f.nGroups != 0 {
		t.Fatalf("%d Groups calls with the folders off", h.f.nGroups)
	}
	spec := h.f.lastSpec()
	if spec.WindowID != "" || spec.GroupRef != "" || spec.CallerRef != "" {
		t.Fatalf("placement set: %+v", spec)
	}
}

func TestLaunchDedupesAPIPending(t *testing.T) {
	h := newHarness(t)
	first, err := h.launch()
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.launch()
	if err != nil {
		t.Fatal(err)
	}
	if second.LaunchID != first.LaunchID || !second.Deduplicated || second.WorkspaceID != first.WorkspaceID {
		t.Fatalf("second = %+v, first = %+v", second, first)
	}
	if n := h.f.creates(); n != 1 {
		t.Fatalf("%d NewWorkspace, want 1", n)
	}
}

func (h *harness) appendEntry(e spawnreg.Entry) {
	h.t.Helper()
	if err := h.reg.Append(e); err != nil {
		h.t.Fatal(err)
	}
}

func TestLaunchRefusesCLIPending(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC()
	h.appendEntry(spawnreg.Entry{TS: now, Kind: spawnreg.KindLaunchReserve, ID: "ln_cli", SubjectID: "sub_x", Origin: "cli", OwnerPID: os.Getpid(), Result: spawnreg.ResultPending})
	h.appendEntry(spawnreg.Entry{TS: now, Kind: spawnreg.KindLaunchRef, ID: "ln_cli", SubjectID: "sub_x", Origin: "cli", WorkspaceID: wsX, WorkspaceRef: "workspace:4", Result: spawnreg.ResultPending})
	_, err := h.launch()
	r := refusalOf(t, err, pigtail.ErrLaunchInProgress, http.StatusConflict)
	if r.Details["launch_id"] != "ln_cli" || r.Details["workspace_ref"] != "workspace:4" {
		t.Fatalf("details = %v", r.Details)
	}
	if h.f.creates() != 0 {
		t.Fatal("a tab was created")
	}
}

func TestLaunchRefusesLiveSession(t *testing.T) {
	h := newHarness(t)
	h.st.bindings["sub_x"] = []pigtail.SubjectCmuxBinding{{CmuxWorkspaceID: wsX, SubjectID: "sub_x"}}
	h.d.Procs = func() ([]claudesession.Proc, error) { return []claudesession.Proc{{PID: 4242}}, nil }
	h.d.Environ = func(int) (string, error) { return "CMUX_WORKSPACE_ID=" + strings.ToLower(wsX) + " PWD=/x", nil }
	_, err := h.launch()
	r := refusalOf(t, err, pigtail.ErrSessionLive, http.StatusConflict)
	if r.Details["workspace_id"] != wsX {
		t.Fatalf("details = %v", r.Details)
	}
	if h.f.creates() != 0 {
		t.Fatal("a tab was created")
	}
}

func TestLaunchRefusesBurst(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC()
	for i := 0; i < sessionlaunch.BurstMax; i++ {
		h.appendEntry(spawnreg.Entry{TS: now.Add(-time.Duration(i) * time.Minute), SubjectID: "sub_other", WorkspaceID: "W", Result: spawnreg.ResultLaunched})
	}
	_, err := h.launch()
	r := refusalOf(t, err, pigtail.ErrLaunchBurst, http.StatusTooManyRequests)
	if r.Details["recent"] != sessionlaunch.BurstMax || r.Details["frees_at"] == nil {
		t.Fatalf("details = %v", r.Details)
	}
	if h.f.creates() != 0 {
		t.Fatal("a tab was created")
	}
}

func TestLaunchRefusesUnreadableLive(t *testing.T) {
	h := newHarness(t)
	h.d.Procs = func() ([]claudesession.Proc, error) { return []claudesession.Proc{{PID: 7}}, nil }
	h.d.Environ = func(int) (string, error) { return "", errors.New("operation not permitted") }
	_, err := h.launch()
	r := refusalOf(t, err, pigtail.ErrLiveUnknown, http.StatusServiceUnavailable)
	if pids, _ := r.Details["pids"].([]int); len(pids) != 1 || pids[0] != 7 {
		t.Fatalf("details = %v", r.Details)
	}
	t.Run("listing fails", func(t *testing.T) {
		h := newHarness(t)
		h.d.Procs = func() ([]claudesession.Proc, error) { return nil, errors.New("ps failed") }
		_, err := h.launch()
		refusalOf(t, err, pigtail.ErrLiveUnknown, http.StatusServiceUnavailable)
	})
}

func TestLaunchCmuxUnreachableCreatesNothing(t *testing.T) {
	h := newHarness(t)
	h.f.windowsErr = errors.New("socket refused")
	_, err := h.launch()
	r := refusalOf(t, err, pigtail.ErrCmuxUnreachable, http.StatusServiceUnavailable)
	if r.Details["cause"] == "" {
		t.Fatalf("details = %v", r.Details)
	}
	entries, _ := h.reg.Entries()
	if h.f.creates() != 0 || len(entries) != 0 {
		t.Fatalf("creates = %d, registry = %v", h.f.creates(), entries)
	}
	t.Run("tab listing fails", func(t *testing.T) {
		h := newHarness(t)
		h.f.listErr = errors.New("workspace list timed out")
		_, err := h.launch()
		refusalOf(t, err, pigtail.ErrCmuxUnreachable, http.StatusServiceUnavailable)
		if h.f.creates() != 0 {
			t.Fatal("a tab was created")
		}
	})
}

func TestLaunchCreateFailedNoRetry(t *testing.T) {
	for name, set := range map[string]func(*fakeCmux){
		"error":      func(f *fakeCmux) { f.createErr = errors.New("new-workspace: exit 1") },
		"no new tab": func(f *fakeCmux) { f.createNone = true },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			set(h.f)
			_, err := h.launch()
			refusalOf(t, err, pigtail.ErrCmuxCreateFailed, http.StatusBadGateway)
			if n := h.f.creates(); n != 1 {
				t.Fatalf("%d NewWorkspace, want exactly 1", n)
			}
			if len(h.binds) != 0 {
				t.Fatal("bound a tab that does not exist")
			}
		})
	}
}

func TestLaunchRegistryUnreadable(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.regFile, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := h.launch()
	refusalOf(t, err, pigtail.ErrRegistryUnreadable, http.StatusServiceUnavailable)
	if h.f.creates() != 0 {
		t.Fatal("a tab was created")
	}
}

func TestLaunchOverMarginPassesThrough(t *testing.T) {
	h := newHarness(t)
	om := &launchmargin.OverMarginError{Scope: "perso"}
	h.d.Cell = func(string) (claudeprofile.Cell, error) { return claudeprofile.Cell{}, om }
	_, err := h.launch()
	var got *launchmargin.OverMarginError
	if !errors.As(err, &got) || got != om {
		t.Fatalf("err = %v (%T), want the OverMarginError as is", err, err)
	}
	if h.f.creates() != 0 {
		t.Fatal("a tab was created")
	}
}

// The cell asked is the subject's scope's, whatever the daemon's own (D1):
// the web front launches a subject of any scope.
func TestLaunchUsesSubjectScopeCell(t *testing.T) {
	h := newHarness(t)
	var other scope.Scope
	for _, n := range scope.Active().Names() {
		if n != scope.Master() {
			other = n
			break
		}
	}
	if other == "" {
		t.Fatal("the scope table declares no non-master scope")
	}
	h.sub.Scope = string(other)
	h.st.subjects[h.sub.ID] = h.sub
	var asked []string
	h.d.Cell = func(sc string) (claudeprofile.Cell, error) {
		asked = append(asked, sc)
		return claudeprofile.Cell{ID: "ts", ConfigDir: h.cell.ConfigDir, Scope: sc}, nil
	}
	h.d.LaunchDir = func(sc string) (string, bool) { return h.launchDir, sc == string(other) }
	got, err := h.launch()
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != string(other) || got.Cell != "ts" {
		t.Fatalf("cells asked = %v, launch cell %q: want the subject's scope %s", asked, got.Cell, other)
	}
}

// tab_title is the name the launcher gives the cmux tab: the subject's
// ShortTitle, cut when derived from a long title; the same on a dedup.
func TestLaunchTabTitleIsTheTabName(t *testing.T) {
	h := newHarness(t)
	h.sub.Title = "A subject title far longer than the derived short title bound"
	h.sub.ShortTitle = ""
	if len([]rune(h.sub.Title)) <= pigtail.ShortTitleDerivedMax {
		t.Fatal("the title must exceed ShortTitleDerivedMax")
	}
	h.st.subjects[h.sub.ID] = h.sub
	l := h.launcher()
	got, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	name := h.f.lastSpec().Name
	if got.TabTitle == "" || got.TabTitle != name || got.TabTitle != pigtail.ShortTitle(h.sub) || len(got.TabTitle) >= len(h.sub.Title) {
		t.Fatalf("tab_title = %q, tab name %q, ShortTitle %q", got.TabTitle, name, pigtail.ShortTitle(h.sub))
	}
	again, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Deduplicated || again.TabTitle != name {
		t.Fatalf("dedup: tab_title = %q (deduplicated %v), want %q", again.TabTitle, again.Deduplicated, name)
	}
}

// D6: without the Staffing mark the budget-aware picker is never read.
func TestLaunchWithoutStaffingMarkIgnoresBudget(t *testing.T) {
	h := newHarness(t)
	h.d.StaffingCell = func(string) (claudeprofile.Cell, error) {
		return claudeprofile.Cell{}, &launchmargin.OverBudgetError{Scope: "perso"}
	}
	got, err := h.launcher().Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if got.Cell != h.cell.ID || h.f.creates() != 1 {
		t.Fatalf("launch = %+v, creates = %d", got, h.f.creates())
	}
}

func TestLaunchWithStaffingMarkUsesStaffingCell(t *testing.T) {
	h := newHarness(t)
	ob := &launchmargin.OverBudgetError{Scope: "perso"}
	h.d.Cell = func(string) (claudeprofile.Cell, error) {
		t.Error("the Staffing path read the plain picker")
		return h.cell, nil
	}
	h.d.StaffingCell = func(string) (claudeprofile.Cell, error) { return claudeprofile.Cell{}, ob }
	_, err := h.launcher().Launch(WithStaffing(context.Background()), h.sub)
	var got *launchmargin.OverBudgetError
	if !errors.As(err, &got) || got != ob {
		t.Fatalf("err = %v (%T), want the OverBudgetError as is", err, err)
	}
	if h.f.creates() != 0 {
		t.Fatal("a tab was created")
	}
	entries, rerr := h.reg.Entries()
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) == 0 {
		t.Fatal("no launch entry: the refusal left no trace")
	}
	last := entries[len(entries)-1]
	if last.Kind != spawnreg.KindLaunchResult || last.Result != spawnreg.ResultFailed || !strings.Contains(last.Detail, "weekly token budget") {
		t.Fatalf("registry = %+v, want a failed launch with its cause", entries)
	}

	// The mark on a launcher that picks the cell by the budget: it runs there.
	h = newHarness(t)
	h.d.Cell = func(string) (claudeprofile.Cell, error) {
		t.Error("the Staffing path read the plain picker")
		return h.cell, nil
	}
	h.d.StaffingCell = func(string) (claudeprofile.Cell, error) { return h.cell, nil }
	if _, err := h.launcher().Launch(WithStaffing(context.Background()), h.sub); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if h.f.creates() != 1 {
		t.Fatalf("creates = %d", h.f.creates())
	}
}

// The mark with no budget-aware picker closes; it never falls back on Cell.
func TestLaunchWithStaffingMarkWithoutPickerCloses(t *testing.T) {
	h := newHarness(t)
	h.d.Cell = func(string) (claudeprofile.Cell, error) {
		t.Error("fell back on the plain picker")
		return h.cell, nil
	}
	_, err := h.launcher().Launch(WithStaffing(context.Background()), h.sub)
	if err == nil || !strings.Contains(err.Error(), "staffing launch: no budget-aware cell picker") {
		t.Fatalf("err = %v", err)
	}
	if h.f.creates() != 0 {
		t.Fatal("a tab was created")
	}
}

// sdkHarness wires a real sdksession.Registry (temp dir, fake processes) as the
// launcher's SDK view.
func (h *harness) withSDK() *sdksession.Registry {
	h.t.Helper()
	sr := &sdksession.Registry{
		Dir:    h.t.TempDir(),
		Alive:  func(int) bool { return false },
		Procs:  func() ([]claudesession.Proc, error) { return nil, nil },
		Native: func(string, int) (claudesession.Native, bool) { return claudesession.Native{}, false },
		Kill:   func(int) error { return nil },
	}
	h.d.SDKLive = func(subID string) (string, bool) {
		_ = sr.SettleSubject(subID)
		return sr.LiveForSubject(subID)
	}
	return sr
}

// reserveSDK writes a fresh meta.json for the subject: a launch under way.
func reserveSDK(t *testing.T, sr *sdksession.Registry, id, subID string) {
	t.Helper()
	dir := filepath.Join(sr.Dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sdksession.Meta{Version: 1, ID: id, SubjectID: subID, Cwd: "/w", ConfigDir: "/c", CreatedAt: time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchRefusesSDKLive(t *testing.T) {
	h := newHarness(t)
	h.d.SDKLive = func(string) (string, bool) { return "sdk_x", true }
	_, err := h.launch()
	r := refusalOf(t, err, pigtail.ErrSessionLive, http.StatusConflict)
	if r.Details["sdk_session_id"] != "sdk_x" || h.f.creates() != 0 {
		t.Fatalf("details = %v creates=%d", r.Details, h.f.creates())
	}
}

func TestResolveSameRefusalsAsLaunch(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		h := newHarness(t)
		got, err := h.launcher().Resolve(h.sub)
		if err != nil {
			t.Fatal(err)
		}
		if got.Cell.ID != "mp" || got.Mode != "auto" || got.Cwd != h.launchDir {
			t.Fatalf("resolved = %+v", got)
		}
	})
	t.Run("no cell", func(t *testing.T) {
		h := newHarness(t)
		h.d.Cell = func(string) (claudeprofile.Cell, error) { return claudeprofile.Cell{}, ErrNoCell }
		_, err := h.launcher().Resolve(h.sub)
		refusalOf(t, err, pigtail.ErrNoCell, http.StatusConflict)
	})
	t.Run("margin passes through", func(t *testing.T) {
		h := newHarness(t)
		want := &launchmargin.OverMarginError{}
		h.d.Cell = func(string) (claudeprofile.Cell, error) { return claudeprofile.Cell{}, want }
		_, err := h.launcher().Resolve(h.sub)
		if !errors.Is(err, want) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bypass", func(t *testing.T) {
		h := newHarness(t)
		h.d.Mode = func(string) (string, error) { return "bypassPermissions", nil }
		_, err := h.launcher().Resolve(h.sub)
		refusalOf(t, err, pigtail.ErrBypassRefused, http.StatusForbidden)
	})
}

// The TUI launch keeps accepting a cell whose default mode is auto.
func TestTUILaunchOnAutoCellUnchanged(t *testing.T) {
	h := newHarness(t) // Mode: auto
	h.withSDK()
	got, err := h.launch()
	if err != nil || got.PermissionMode != "auto" {
		t.Fatalf("launch = %+v, %v", got, err)
	}
}

// H6 (spikes/sdk/RESULTS.md): auto is refused on the SDK path; the seam holds the
// measured value, both values are covered.
func TestSDKReservationAutoPerH6(t *testing.T) {
	t.Run("measured: refused", func(t *testing.T) {
		h := newHarness(t) // Mode: auto, SDKAcceptsAuto false = the measure
		h.withSDK()
		called := false
		_, err := h.launcher().WithSDKReservation(h.sub, func(Resolved) (string, error) { called = true; return "sdk_a", nil })
		refusalOf(t, err, pigtail.ErrBypassRefused, http.StatusForbidden)
		if called {
			t.Fatal("fn ran on an auto cell")
		}
	})
	t.Run("accepted", func(t *testing.T) {
		h := newHarness(t)
		h.withSDK()
		h.d.SDKAcceptsAuto = true
		var got Resolved
		id, err := h.launcher().WithSDKReservation(h.sub, func(r Resolved) (string, error) { got = r; return "sdk_a", nil })
		if err != nil || id != "sdk_a" || got.Mode != "auto" || got.Cwd != h.launchDir {
			t.Fatalf("id=%q err=%v resolved=%+v", id, err, got)
		}
	})
	t.Run("default mode passes", func(t *testing.T) {
		h := newHarness(t)
		h.withSDK()
		h.d.Mode = func(string) (string, error) { return "", nil }
		var got Resolved
		if _, err := h.launcher().WithSDKReservation(h.sub, func(r Resolved) (string, error) { got = r; return "sdk_a", nil }); err != nil || got.Mode != "default" {
			t.Fatalf("err=%v resolved=%+v", err, got)
		}
	})
}

func TestSDKAndTUIConcurrentLaunch(t *testing.T) {
	t.Run("two sdk reservations", func(t *testing.T) {
		h := newHarness(t)
		h.d.Mode = func(string) (string, error) { return "default", nil }
		sr := h.withSDK()
		l := h.launcher()
		var calls atomic.Int32
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = l.WithSDKReservation(h.sub, func(Resolved) (string, error) {
					n := calls.Add(1)
					reserveSDK(t, sr, "sdk_"+strconv.Itoa(int(n)), h.sub.ID)
					time.Sleep(30 * time.Millisecond)
					return "sdk_" + strconv.Itoa(int(n)), nil
				})
			}()
		}
		wg.Wait()
		if calls.Load() != 1 {
			t.Fatalf("fn ran %d times, want 1", calls.Load())
		}
		var refused int
		for _, e := range errs {
			if e != nil {
				refusalOf(t, e, pigtail.ErrSessionLive, http.StatusConflict)
				refused++
			}
		}
		if refused != 1 {
			t.Fatalf("refused %d, want 1 (%v)", refused, errs)
		}
	})
	t.Run("tui and sdk", func(t *testing.T) {
		h := newHarness(t)
		h.d.Mode = func(string) (string, error) { return "default", nil }
		sr := h.withSDK()
		l := h.launcher()
		var sdkRan atomic.Int32
		var wg sync.WaitGroup
		var tuiErr, sdkErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, tuiErr = l.Launch(context.Background(), h.sub) }()
		go func() {
			defer wg.Done()
			_, sdkErr = l.WithSDKReservation(h.sub, func(Resolved) (string, error) {
				sdkRan.Add(1)
				reserveSDK(t, sr, "sdk_1", h.sub.ID)
				return "sdk_1", nil
			})
		}()
		wg.Wait()
		tuiOK, sdkOK := tuiErr == nil, sdkErr == nil
		if tuiOK == sdkOK {
			t.Fatalf("tui err=%v sdk err=%v: exactly one must pass", tuiErr, sdkErr)
		}
	})
}

func TestSDKReservationBlocksTUILaunch(t *testing.T) {
	h := newHarness(t)
	sr := h.withSDK()
	reserveSDK(t, sr, "sdk_r", h.sub.ID) // meta.json without host.json: starting, live
	_, err := h.launch()
	r := refusalOf(t, err, pigtail.ErrSessionLive, http.StatusConflict)
	if r.Details["sdk_session_id"] != "sdk_r" {
		t.Fatalf("details = %v", r.Details)
	}
}

func TestLaunchSettlesSDKOrphanBeforeDeciding(t *testing.T) {
	h := newHarness(t)
	h.d.Mode = func(string) (string, error) { return "default", nil }
	dir := t.TempDir()
	const sess = "1da9ff5f-4306-4adf-aa01-d368f0e3e006"
	var killed []int
	alive := map[int]bool{4243: true}
	sr := &sdksession.Registry{
		Dir:   dir,
		Alive: func(pid int) bool { return alive[pid] },
		Procs: func() ([]claudesession.Proc, error) {
			return []claudesession.Proc{{PID: 4243, Args: "claude --session-id " + sess}}, nil
		},
		Native: func(string, int) (claudesession.Native, bool) { return claudesession.Native{}, false },
		Kill:   func(pid int) error { killed = append(killed, pid); alive[pid] = false; return nil },
		Now:    time.Now,
	}
	meta := sdksession.Meta{Version: 1, ID: "sdk_o", SubjectID: h.sub.ID, Cwd: "/w", ConfigDir: "/c", SessionUUID: sess, HostPID: 4242, CreatedAt: time.Now().Add(-time.Hour)}
	host := sdksession.HostFile{Version: 1, HostPID: 4242, ClaudePIDs: []int{4243}, ClaudeSessionID: sess, State: "running"}
	for name, v := range map[string]any{"meta.json": meta, "host.json": host} {
		if err := os.MkdirAll(filepath.Join(dir, "sdk_o"), 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, "sdk_o", name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.d.SDKLive = func(subID string) (string, bool) {
		_ = sr.SettleSubject(subID)
		return sr.LiveForSubject(subID)
	}
	if _, err := h.launch(); err != nil {
		t.Fatalf("proved orphan: launch must pass after the kill: %v", err)
	}
	if len(killed) != 1 || killed[0] != 4243 {
		t.Fatalf("killed = %v", killed)
	}
	// An orphan whose identity is not proven blocks.
	h2 := newHarness(t)
	h2.d.SDKLive = func(subID string) (string, bool) {
		alive := map[int]bool{4243: true}
		sr2 := &sdksession.Registry{Dir: dir, Alive: func(p int) bool { return alive[p] },
			Procs:  func() ([]claudesession.Proc, error) { return []claudesession.Proc{{PID: 4243, Args: "claude"}}, nil },
			Native: func(string, int) (claudesession.Native, bool) { return claudesession.Native{}, false },
			Kill:   func(int) error { t.Error("unproved claude was killed"); return nil }}
		_ = sr2.SettleSubject(subID)
		return sr2.LiveForSubject(subID)
	}
	_, err := h2.launch()
	refusalOf(t, err, pigtail.ErrSessionLive, http.StatusConflict)
}

func TestLaunchUsesSettingsTemplate(t *testing.T) {
	h := newHarness(t)
	if _, err := h.launch(); err != nil {
		t.Fatal(err)
	}
	if got := promptEnv(h.f.lastSpec()); !strings.HasPrefix(got, "Work on pigtail subject sub_x: Web launch.") {
		t.Fatalf("default prompt = %q", got)
	}
	h2 := newHarness(t)
	s := usersettings.Defaults()
	s.Launch.PromptTemplate = "/x {id}"
	h2.d.Prompt = s.LaunchPrompt
	if _, err := h2.launch(); err != nil {
		t.Fatal(err)
	}
	if got := promptEnv(h2.f.lastSpec()); got != "/x sub_x" {
		t.Fatalf("custom prompt = %q", got)
	}
}

func promptEnv(spec cmux.WorkspaceSpec) string {
	for _, e := range spec.Env {
		if v, ok := strings.CutPrefix(e, sessionlaunch.EnvSpawnPrompt+"="); ok {
			return v
		}
	}
	return ""
}
