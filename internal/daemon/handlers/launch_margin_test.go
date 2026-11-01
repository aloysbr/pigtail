package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/agent"
	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The launch margin guard on /dispatch and /workspaces/{id}/run
// (sub_x000000000000011, D6, D11): an over-margin profile switches to a free
// account of its scope, a saturated scope is refused (409) before anything is
// created, every text is the bounded form.

var marginNow = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

const (
	marginUUIDM = "11111111-1111-4111-8111-111111111111"
	marginUUIDJ = "33333333-3333-4333-8333-333333333333"
)

// marginUsage is a .claude.json with a 7-day window at p7 %, resetting in r7.
func marginUsage(uuid string, p7 float64, r7 time.Duration) []byte {
	return []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q},
"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":%q,"utilization":{"five_hour":{"utilization":5,"resets_at":%q},"seven_day":{"utilization":%v,"resets_at":%q}}}}`,
		uuid, marginNow.Add(-time.Minute).UnixMilli(), uuid,
		marginNow.Add(time.Hour).Format("2006-01-02T15:04:05.000000+00:00"), p7,
		marginNow.Add(r7).Format("2006-01-02T15:04:05.000000+00:00")))
}

// marginRig is a dispatch/run rig: cells mp (account m) and jp (account j)
// in the perso scope, a project whose profile p points at mp, fakes for the
// backend and the agent, and the guard's deps served from memory.
type marginRig struct {
	svc      *Services
	proj     *pigtail.Project
	yaml     []byte
	files    map[string][]byte
	env      map[string]string
	created  int
	persists int
	spawned  []map[string]string
	reg      *spawnreg.Registry
	regPath  string
}

type marginBackend struct {
	backend.Backend
	rig *marginRig
}

func (b marginBackend) Kind() string { return "fake" }
func (b marginBackend) Create(_ context.Context, p *pigtail.Project, _ backend.CreateIntent) (*pigtail.Workspace, error) {
	b.rig.created++
	return &pigtail.Workspace{ID: "ws_margin", ProjectID: p.ID, Path: p.RootPath, State: pigtail.StateBuilding, BackendKind: "fake"}, nil
}

type marginAgent struct {
	agent.Agent
	rig *marginRig
}

func (a marginAgent) Kind() string { return "fake" }
func (a marginAgent) Spawn(_ context.Context, ws *pigtail.Workspace, in agent.SpawnIntent) (*pigtail.AgentSession, error) {
	a.rig.spawned = append(a.rig.spawned, in.ExtraEnv)
	return &pigtail.AgentSession{ID: fmt.Sprintf("ag_%d", len(a.rig.spawned)), WorkspaceID: ws.ID, AgentKind: "claude",
		State: pigtail.AgentStateRunning, StartedAt: marginNow}, nil
}

func newMarginRig(t *testing.T, profileDir string) *marginRig {
	t.Helper()
	var calls []string
	stubDispatchPreTrust(t, claudeconfig.Result{}, &calls)
	root := t.TempDir()
	rig := &marginRig{files: map[string][]byte{}, env: map[string]string{},
		yaml: []byte("version: 1\nname: t\nprofiles:\n  p:\n    config_dir: " + profileDir + "\n")}
	if err := os.WriteFile(filepath.Join(root, pigtail.ProjectConfigFilename), rig.yaml, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rig.proj = &pigtail.Project{ID: "prj_m", Name: "m", RootPath: root, MainWorktree: "main", DefaultBackend: "fake", DefaultProfileID: "p"}
	if err := st.CreateProject(rig.proj); err != nil {
		t.Fatal(err)
	}
	r0, r1 := 0, 1
	tbl := &claudeprofile.Table{Cells: map[string]claudeprofile.Cell{
		"mp": {ID: "mp", ConfigDir: "/cells/mp", Scope: "perso", Account: "m", Rank: &r0},
		"jp": {ID: "jp", ConfigDir: "/cells/jp", Scope: "perso", Account: "j", Rank: &r1},
	}}
	rig.regPath = filepath.Join(t.TempDir(), "spawns.jsonl")
	rig.reg = spawnreg.Open(rig.regPath)
	rig.svc = &Services{Store: st, Bus: events.New(), Logf: func(string, ...any) {}, MarginSpawns: rig.reg,
		MarginDeps: func() launchmargin.Deps {
			return launchmargin.Deps{
				Getenv: func(k string) string { return rig.env[k] },
				ReadFile: func(p string) ([]byte, error) {
					if b, ok := rig.files[p]; ok {
						return b, nil
					}
					return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
				},
				Now:   func() time.Time { return marginNow },
				Cells: tbl,
			}
		}}
	rig.svc.Backend = marginBackend{rig: rig}
	rig.svc.Agent = marginAgent{rig: rig}
	rig.svc.CreateWorkspace = func(ws *pigtail.Workspace) error { rig.persists++; return st.CreateWorkspace(ws) }
	return rig
}

func (rig *marginRig) usage(cell, uuid string, p7 float64, r7 time.Duration) {
	rig.files[filepath.Join("/cells/"+cell, claudeusage.FileName)] = marginUsage(uuid, p7, r7)
}

// saturate: m over the margin; all: j too.
func (rig *marginRig) saturate(all bool) {
	rig.usage("mp", marginUUIDM, 97, 2*24*time.Hour)
	if all {
		rig.usage("jp", marginUUIDJ, 96, 30*time.Hour)
	} else {
		rig.usage("jp", marginUUIDJ, 40, 3*24*time.Hour)
	}
}

func (rig *marginRig) dispatch(t *testing.T, req pigtail.DispatchRequest) *httptest.ResponseRecorder {
	t.Helper()
	req.ProjectID, req.Intent = rig.proj.ID, "margin"
	return callRoute(t, "/dispatch", Dispatch(rig.svc, testResponder{}), req)
}

func (rig *marginRig) run(t *testing.T, req pigtail.RunRequest) *httptest.ResponseRecorder {
	t.Helper()
	ws := &pigtail.Workspace{ID: "ws_run", ProjectID: rig.proj.ID, Path: rig.proj.RootPath, State: pigtail.StateBuilding, BackendKind: "fake"}
	if err := rig.svc.Store.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	req.Intent = "margin"
	r := chi.NewRouter()
	r.Post("/workspaces/{id}/run", RunAgent(rig.svc, testResponder{}))
	buf, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/workspaces/ws_run/run", bytes.NewReader(buf)))
	return rec
}

func (rig *marginRig) marginRows(t *testing.T) ([]spawnreg.Entry, []pigtail.JournalEntry) {
	t.Helper()
	es, err := rig.reg.Entries()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := rig.svc.Store.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: launchmargin.JournalKind})
	if err != nil {
		t.Fatal(err)
	}
	return es, rows
}

func launchedDir(t *testing.T, rig *marginRig) string {
	t.Helper()
	if len(rig.spawned) != 1 {
		t.Fatalf("agent spawned %d times, want 1", len(rig.spawned))
	}
	return rig.spawned[0]["CLAUDE_CONFIG_DIR"]
}

func TestDispatchSwitchesOverMarginProfile(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(false)
	rec := rig.dispatch(t, pigtail.DispatchRequest{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if dir := launchedDir(t, rig); dir != "/cells/jp" {
		t.Fatalf("launched under %q, want the free account's /cells/jp", dir)
	}
	if b, _ := os.ReadFile(filepath.Join(rig.proj.RootPath, pigtail.ProjectConfigFilename)); !bytes.Equal(b, rig.yaml) {
		t.Fatalf("pigtail.yaml changed: %s", b)
	}
}

func TestDispatchRefusesWhenAllOver(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(true)
	rec := rig.dispatch(t, pigtail.DispatchRequest{})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), string(pigtail.ErrLaunchOverMargin)) {
		t.Fatalf("status %d body %s, want 409 LAUNCH_OVER_MARGIN", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "first release at ") || !strings.Contains(rec.Body.String(), "over-margin") {
		t.Fatalf("refusal lacks the release time or the way out: %s", rec.Body)
	}
	if rig.created != 0 || rig.persists != 0 || len(rig.spawned) != 0 {
		t.Fatalf("a refusal created %d workspaces, persisted %d, spawned %d", rig.created, rig.persists, len(rig.spawned))
	}
}

func TestDispatchOverMarginFlagPasses(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(true)
	rec := rig.dispatch(t, pigtail.DispatchRequest{OverMargin: true})
	var resp pigtail.DispatchResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusCreated || launchedDir(t, rig) != "/cells/mp" || resp.Margin == nil || resp.Margin.Decision != "forced" {
		t.Fatalf("status %d body %s, want forced on mp", rec.Code, rec.Body)
	}
}

func TestRunAgentSwitchesOverMarginProfile(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(false)
	rec := rig.run(t, pigtail.RunRequest{})
	if rec.Code != http.StatusCreated || launchedDir(t, rig) != "/cells/jp" {
		t.Fatalf("status %d body %s, want a run on /cells/jp", rec.Code, rec.Body)
	}
}

func TestRunAgentRefusesWhenAllOver(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(true)
	rec := rig.run(t, pigtail.RunRequest{PlanContent: "plan", PlanFilename: "p.md"})
	if rec.Code != http.StatusConflict || len(rig.spawned) != 0 {
		t.Fatalf("status %d spawned %d, want 409 and nothing", rec.Code, len(rig.spawned))
	}
	if _, err := os.Stat(filepath.Join(rig.proj.RootPath, ".pigtail", "p.md")); err == nil {
		t.Fatal("a refused run wrote its plan file")
	}
}

func TestDispatchSwitchWritesMarginEntry(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(false)
	if rec := rig.dispatch(t, pigtail.DispatchRequest{}); rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	es, rows := rig.marginRows(t)
	if len(es) != 1 || es[0].Kind != spawnreg.KindMargin || es[0].Cell != "mp" || es[0].FreshCell != "jp" || es[0].Result != "switched" {
		t.Fatalf("spawn registry = %+v", es)
	}
	if len(rows) != 1 || rows[0].Scope != "perso" || rows[0].Source != "pigtail:spawns" {
		t.Fatalf("journal = %+v, want one row in the cell's scope", rows)
	}
}

func TestDispatchResponseCarriesMargin(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(false)
	rec := rig.dispatch(t, pigtail.DispatchRequest{})
	var resp pigtail.DispatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Margin == nil {
		t.Fatalf("body %s: no margin", rec.Body)
	}
	if m := resp.Margin; m.Decision != "switched" || m.Requested != "mp" || m.Final != "jp" || !strings.HasPrefix(m.Warning, "launch margin: ") {
		t.Fatalf("margin = %+v", m)
	}
}

func TestRunResponseCarriesMargin(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(false)
	rec := rig.run(t, pigtail.RunRequest{})
	var resp pigtail.RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Margin == nil || resp.Margin.Decision != "switched" || resp.Margin.Final != "jp" {
		t.Fatalf("body %s, want margin switched to jp", rec.Body)
	}
}

// D11: the daemon cannot know the caller's scope — every text is bounded,
// for every caller (cell ids and release time, no account, no scope name).
func TestDispatchMarginTextBounded(t *testing.T) {
	for _, all := range []bool{false, true} {
		rig := newMarginRig(t, "/cells/mp")
		rig.saturate(all)
		rec := rig.dispatch(t, pigtail.DispatchRequest{})
		es, rows := rig.marginRows(t)
		if len(es) != 1 || len(rows) != 1 {
			t.Fatalf("all=%v: entries %+v journal %+v", all, es, rows)
		}
		raw, err := os.ReadFile(rig.regPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{rec.Body.String(), rows[0].Summary, string(raw)} {
			if strings.Contains(text, "perso") || strings.Contains(text, "(account") || strings.Contains(text, "first release: ") {
				t.Fatalf("all=%v: unbounded text %q", all, text)
			}
		}
	}
}

// Pins (green on base and branch): a profile the guard cannot link to a cell,
// or a guard that cannot measure, launches as before.
func TestDispatchProfileWithoutCellUntouched(t *testing.T) {
	rig := newMarginRig(t, "/elsewhere")
	rig.saturate(true)
	rec := rig.dispatch(t, pigtail.DispatchRequest{})
	if rec.Code != http.StatusCreated || launchedDir(t, rig) != "/elsewhere" {
		t.Fatalf("status %d body %s, want the profile's own dir", rec.Code, rec.Body)
	}
}

func TestDispatchUnmeasuredPasses(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(true)
	rig.env[launchmargin.EnvRotation] = "off"
	rec := rig.dispatch(t, pigtail.DispatchRequest{})
	if rec.Code != http.StatusCreated || launchedDir(t, rig) != "/cells/mp" {
		t.Fatalf("status %d body %s, want mp unchanged", rec.Code, rec.Body)
	}
}

// LaunchCell is the session launcher's cell (sub_x000000000000015): the
// guard's pick in the scope, the lowest-rank cell when it cannot measure.

func TestLaunchCellFallsBackWhenUnmeasured(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.env[launchmargin.EnvRotation] = "off"
	c, err := LaunchCell(rig.svc)("perso")
	if err != nil || c.ID != "mp" {
		t.Fatalf("LaunchCell = %+v, %v; want mp (lowest rank)", c, err)
	}
	es, rows := rig.marginRows(t)
	if len(es) != 1 || es[0].Result != launchmargin.ActionUnmeasured || es[0].Cell != "mp" {
		t.Fatalf("spawn trace = %+v", es)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Summary, "rotation-off") {
		t.Fatalf("journal = %+v", rows)
	}
}

func TestLaunchCellFallbackOrder(t *testing.T) {
	t.Parallel()
	r1, r5 := 1, 5
	tbl := &claudeprofile.Table{Cells: map[string]claudeprofile.Cell{
		"zz": {ID: "zz", ConfigDir: "/cells/zz", Scope: "perso"},
		"bb": {ID: "bb", ConfigDir: "/cells/bb", Scope: "perso", Rank: &r5},
		"aa": {ID: "aa", ConfigDir: "/cells/aa", Scope: "perso"},
		"cc": {ID: "cc", ConfigDir: "/cells/cc", Scope: "perso", Rank: &r1},
	}}
	got := []string{}
	for len(tbl.Cells) > 0 {
		c, ok := fallbackCell(tbl, "perso")
		if !ok {
			t.Fatal("no fallback")
		}
		got = append(got, c.ID)
		delete(tbl.Cells, c.ID)
	}
	if strings.Join(got, ",") != "cc,bb,aa,zz" {
		t.Fatalf("order = %v", got)
	}
}

func TestLaunchCellRefusesOverMargin(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(true)
	_, err := LaunchCell(rig.svc)("perso")
	var om *launchmargin.OverMarginError
	if !errors.As(err, &om) {
		t.Fatalf("LaunchCell = %v, want an OverMarginError", err)
	}
}

func TestLaunchCellNoCell(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	_, err := LaunchCell(rig.svc)("scope-with-no-cell")
	if !errors.Is(err, launch.ErrNoCell) {
		t.Fatalf("LaunchCell = %v, want launch.ErrNoCell", err)
	}
}

// marginStale is a reading of the given age, under every margin.
func marginStale(uuid string, age time.Duration) []byte {
	return []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q},
"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":%q,"utilization":{"five_hour":{"utilization":5,"resets_at":%q},"seven_day":{"utilization":1,"resets_at":%q}}}}`,
		uuid, marginNow.Add(-age).UnixMilli(), uuid,
		marginNow.Add(time.Hour).Format("2006-01-02T15:04:05.000000+00:00"),
		marginNow.Add(84*time.Hour).Format("2006-01-02T15:04:05.000000+00:00")))
}

// What is not measured closes: never the fallback cell LaunchCell takes.
func TestLaunchCellStaffingNoFallbackWhenUnmeasured(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.env[launchmargin.EnvRotation] = "off"
	if c, err := LaunchCell(rig.svc)("perso"); err != nil || c.ID != "mp" {
		t.Fatalf("LaunchCell = %+v, %v; want the fallback", c, err)
	}
	c, err := LaunchCellStaffing(rig.svc)("perso")
	if !errors.Is(err, launchmargin.ErrRotationOff) || c.ID != "" {
		t.Fatalf("rotation off: LaunchCellStaffing = %+v, %v", c, err)
	}

	rig = newMarginRig(t, "/cells/mp")
	deps := rig.svc.MarginDeps
	rig.svc.MarginDeps = func() launchmargin.Deps { d := deps(); d.Cells = nil; return d }
	c, err = LaunchCellStaffing(rig.svc)("perso")
	if err == nil || c.ID != "" {
		t.Fatalf("no table: LaunchCellStaffing = %+v, %v", c, err)
	}
}

func TestLaunchCellStaffingOverBudgetAsIs(t *testing.T) {
	rig := newMarginRig(t, "/cells/mp")
	rig.usage("mp", marginUUIDM, 60, 84*time.Hour)
	rig.usage("jp", marginUUIDJ, 55, 84*time.Hour)
	c, err := LaunchCellStaffing(rig.svc)("perso")
	var ob *launchmargin.OverBudgetError
	if !errors.As(err, &ob) || c.ID != "" {
		t.Fatalf("LaunchCellStaffing = %+v, %v", c, err)
	}
	if es, rows := rig.marginRows(t); len(es) != 1 || es[0].Result != launchmargin.ActionRefused || len(rows) != 1 {
		t.Fatalf("trace = %+v / %+v", es, rows)
	}
	// D6: the same accounts run a launch that is not Staffing's.
	if c, err := LaunchCell(rig.svc)("perso"); err != nil || c.ID == "" {
		t.Fatalf("LaunchCell = %+v, %v", c, err)
	}
}

func TestStaffingTokenBudgetPeek(t *testing.T) {
	peek := func(rig *marginRig) (bool, string) { return StaffingTokenBudget(rig.svc)("perso") }

	rig := newMarginRig(t, "/cells/mp")
	rig.usage("mp", marginUUIDM, 30, 4*24*time.Hour)
	rig.usage("jp", marginUUIDJ, 30, 4*24*time.Hour)
	if ok, why := peek(rig); !ok || why != "" {
		t.Fatalf("within: %v %q", ok, why)
	}

	rig = newMarginRig(t, "/cells/mp")
	rig.usage("mp", marginUUIDM, 60, 84*time.Hour)
	rig.usage("jp", marginUUIDJ, 55, 84*time.Hour)
	if ok, why := peek(rig); ok || !strings.Contains(why, "weekly token budget") {
		t.Fatalf("over: %v %q", ok, why)
	}
	if es, rows := rig.marginRows(t); len(es) != 0 || len(rows) != 0 {
		t.Fatalf("a peek left a trace: %+v / %+v", es, rows)
	}

	rig = newMarginRig(t, "/cells/mp")
	rig.usage("mp", marginUUIDM, 60, 84*time.Hour)
	// j within its budget, held by a 5-hour window at its ceiling only.
	rig.files[filepath.Join("/cells/jp", claudeusage.FileName)] = []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q},
"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":%q,"utilization":{"five_hour":{"utilization":95,"resets_at":%q},"seven_day":{"utilization":5,"resets_at":%q}}}}`,
		marginUUIDJ, marginNow.Add(-time.Minute).UnixMilli(), marginUUIDJ,
		marginNow.Add(time.Hour).Format("2006-01-02T15:04:05.000000+00:00"),
		marginNow.Add(4*24*time.Hour).Format("2006-01-02T15:04:05.000000+00:00")))
	if ok, why := peek(rig); ok || !strings.Contains(why, "margin or weekly budget") {
		t.Fatalf("mixed: %v %q", ok, why)
	}

	rig = newMarginRig(t, "/cells/mp")
	rig.files[filepath.Join("/cells/mp", claudeusage.FileName)] = marginStale(marginUUIDM, 31*time.Minute)
	rig.files[filepath.Join("/cells/jp", claudeusage.FileName)] = marginStale(marginUUIDJ, 31*time.Minute)
	if ok, why := peek(rig); ok || !strings.HasPrefix(why, "unmeasured: ") {
		t.Fatalf("stale: %v %q", ok, why)
	}

	rig = newMarginRig(t, "/cells/mp")
	deps := rig.svc.MarginDeps
	rig.svc.MarginDeps = func() launchmargin.Deps { d := deps(); d.Cells = nil; return d }
	if ok, why := peek(rig); ok || !strings.HasPrefix(why, "unmeasured: ") {
		t.Fatalf("no table: %v %q", ok, why)
	}

	rig = newMarginRig(t, "/cells/mp")
	rig.env[launchmargin.EnvRotation] = "off"
	if ok, why := peek(rig); ok || !strings.HasPrefix(why, "unmeasured: ") {
		t.Fatalf("rotation off: %v %q", ok, why)
	}
}
