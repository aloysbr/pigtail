package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/staffing"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The staffing scheduler wired in pigtaild (sub_x000000000000004, Task 7):
// its routes, its tours on events and subject writes, its place in the
// session budget, its settings across a restart.

// staffingRig is a daemon whose launcher runs on simulated cmux and cells,
// with one ready subject of the master scope (sub_r, understanding, rank 1).
type staffingRig struct {
	srv      *Server
	h        http.Handler
	tours    chan pigtail.StaffingDecision
	mu       sync.Mutex
	launched []string // the scheduler's own launches (its fake Launch)
}

func (rig *staffingRig) launches() int {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	return len(rig.launched)
}

type staffingRigOpts struct {
	dbPath string // "" = a temp dir's
	budget sessbudget.Config
	every  time.Duration
}

func newStaffingRig(t *testing.T, o staffingRigOpts) *staffingRig {
	t.Helper()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	if o.dbPath == "" {
		o.dbPath = filepath.Join(t.TempDir(), "pigtail.db")
	}
	st, err := store.Open(o.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	master := string(scope.Master())
	now := time.Now()
	if _, err := st.GetSubject("sub_r"); err != nil {
		if err := st.CreateTheme(&pigtail.Theme{ID: "thm_r", Name: "R", Kind: "ongoing", Scope: master, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateSubject(&pigtail.Subject{ID: "sub_r", ThemeID: "thm_r", Title: "Ready one", ShortTitle: "Ready one",
			Slug: "r", Status: pigtail.StatusUnderstanding, Rank: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	rig := &staffingRig{tours: make(chan pigtail.StaffingDecision, 64)}
	f := &wiringCmux{}
	cellDir, launchDir := t.TempDir(), t.TempDir()
	reg := filepath.Join(t.TempDir(), "spawns.jsonl")
	sock := testSocketPath(t)
	every := o.every
	if every == 0 {
		every = time.Hour
	}
	cfg := Config{SocketPath: sock, DBPath: o.dbPath, SessionBudget: o.budget,
		LaunchOverrides: func(d *launch.Deps) {
			f.wire(d)
			d.Registry = spawnreg.Open(reg)
			d.Cell = func(string) (claudeprofile.Cell, error) {
				return claudeprofile.Cell{ID: "mp", ConfigDir: cellDir, Scope: master}, nil
			}
			d.LaunchDir = func(string) (string, bool) { return launchDir, true }
			d.Mode = func(string) (string, error) { return "auto", nil }
			d.Procs = func() ([]claudesession.Proc, error) { return nil, nil }
			d.Environ = func(int) (string, error) { return "", nil }
			d.PollEvery = 5 * time.Millisecond
			d.Logf = t.Logf
		},
		StaffingOverrides: func(d *staffing.Deps) {
			d.Every = every
			d.Margin = func(string) (bool, string) { return true, "" } // no real accounts in a test
			d.TokenBudget = func(string) (bool, string) { return true, "" }
			d.Machine = func() (bool, string) { return true, "" }
			d.Launch = func(_ context.Context, sub pigtail.Subject) (pigtail.SessionLaunch, error) {
				rig.mu.Lock()
				defer rig.mu.Unlock()
				rig.launched = append(rig.launched, sub.ID)
				return pigtail.SessionLaunch{LaunchID: "ln_fake", SubjectID: sub.ID, State: pigtail.LaunchPending, StartedAt: time.Now()}, nil
			}
			d.Logf = t.Logf
			d.AfterTour = func(dec pigtail.StaffingDecision) {
				select {
				case rig.tours <- dec:
				default:
				}
			}
		}}
	if o.budget.Ceiling > 0 {
		cfg.SessionSampleSeconds = 60
	}
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if srv.launcher != nil {
		srv.launcher.Start(ctx)
	}
	srv.startStaffing(ctx, func(fn func()) { wg.Add(1); go func() { defer wg.Done(); fn() }() })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		if srv.launcher != nil {
			srv.launcher.Wait()
		}
	})
	rig.srv, rig.h = srv, srv.router
	return rig
}

// serve sends a master request, with the cross-scope opt-in when cross.
func (rig *staffingRig) serve(t *testing.T, method, path string, body any, cross bool) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = ""
	req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
	if cross {
		req.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, req)
	return rec
}

func decodeData[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body, err)
	}
	return env.Data
}

// tour waits for the next tour, at most within.
func (rig *staffingRig) tour(t *testing.T, within time.Duration) pigtail.StaffingDecision {
	t.Helper()
	select {
	case d := <-rig.tours:
		return d
	case <-time.After(within):
		t.Fatalf("no tour within %s", within)
	}
	return pigtail.StaffingDecision{}
}

// drain empties the tours already run (the one at start).
func (rig *staffingRig) drain(t *testing.T) {
	t.Helper()
	rig.tour(t, 2*time.Second)
	for {
		select {
		case <-rig.tours:
		case <-time.After(100 * time.Millisecond):
			return
		}
	}
}

func TestServerWiresStaffing(t *testing.T) {
	rig := newStaffingRig(t, staffingRigOpts{})
	if rig.srv.staffing == nil {
		t.Fatal("no scheduler wired")
	}
	rec := rig.serve(t, "GET", "/staffing", nil, false)
	st := decodeData[pigtail.StaffingState](t, rec)
	if rec.Code != http.StatusOK || st.Mode != pigtail.StaffingAdvise || st.Ceiling != 3 || st.Launcher != pigtail.StaffingLauncherPilot {
		t.Fatalf("GET /staffing = %d %s", rec.Code, rec.Body)
	}
}

func TestStaffingKickedByWaitChange(t *testing.T) {
	rig := newStaffingRig(t, staffingRigOpts{every: time.Hour})
	rig.drain(t)
	rig.srv.bus.Publish(pigtail.Event{Type: pigtail.EventSessionWaitChanged, Scope: string(scope.Master())})
	rig.tour(t, 2*time.Second)
}

func TestStaffingKickedBySubjectUpdate(t *testing.T) {
	rig := newStaffingRig(t, staffingRigOpts{every: time.Hour})
	rig.drain(t)
	if rec := rig.serve(t, "POST", "/subjects/sub_r", map[string]any{"rank": 2}, false); rec.Code != http.StatusOK {
		t.Fatalf("POST /subjects/sub_r = %d %s", rec.Code, rec.Body)
	}
	rig.tour(t, 2*time.Second)
}

func TestSessionBudgetFullHoldsSchedulerNotManualLaunch(t *testing.T) {
	rig := newStaffingRig(t, staffingRigOpts{every: time.Hour,
		budget: sessbudget.Config{Ceiling: 1, Floor: 1}})
	rig.srv.sessionBudget.Observe(pigtail.SessionSample{TS: time.Now(), Live: 1, Busy: 1})
	rig.drain(t)
	if rec := rig.serve(t, "POST", "/staffing", map[string]any{"mode": "launch"}, true); rec.Code != http.StatusOK {
		t.Fatalf("POST /staffing = %d %s", rec.Code, rec.Body)
	}
	for {
		d := rig.tour(t, 2*time.Second)
		if d.Mode != pigtail.StaffingLaunch {
			continue
		}
		if d.Gate != pigtail.StaffingGateSessionBudgetFull || d.Outcome != pigtail.StaffingOutcomeIdle {
			t.Fatalf("tour in launch with a full budget: %+v", d)
		}
		break
	}
	if n := rig.launches(); n != 0 {
		t.Fatalf("the scheduler launched %d times past a full budget", n)
	}
	rec := rig.serve(t, "POST", "/subjects/sub_r/launch", nil, false)
	if rec.Code != http.StatusAccepted || decodeData[pigtail.SessionLaunch](t, rec).State != pigtail.LaunchPending {
		t.Fatalf("a manual launch with a full budget = %d %s, want 202 pending", rec.Code, rec.Body)
	}
}

func TestStaffingSurvivesRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "pigtail.db")
	rig := newStaffingRig(t, staffingRigOpts{dbPath: db})
	if rec := rig.serve(t, "POST", "/staffing", map[string]any{"mode": "launch", "ceiling": 2}, true); rec.Code != http.StatusOK {
		t.Fatalf("POST /staffing = %d %s", rec.Code, rec.Body)
	}
	rig2 := newStaffingRig(t, staffingRigOpts{dbPath: db})
	st := decodeData[pigtail.StaffingState](t, rig2.serve(t, "GET", "/staffing", nil, false))
	if st.Mode != pigtail.StaffingLaunch || st.Ceiling != 2 {
		t.Fatalf("after a restart: %+v", st)
	}
	ds := decodeData[[]pigtail.StaffingDecision](t, rig2.serve(t, "GET", "/staffing/decisions", nil, false))
	found := false
	for _, d := range ds {
		found = found || d.Outcome == pigtail.StaffingOutcomeSettingsChanged
	}
	if !found {
		t.Fatalf("journal not read back: %+v", ds)
	}
}

// budgetWireRig is a daemon with nothing replaced on the budget's path: the
// real scheduler wiring (Margin, TokenBudget, Launch) and the real launcher
// (Cell, StaffingCell), on a cell table and a .claude.json in temp dirs, read
// through XDG_CONFIG_HOME by the guard's own deps. cmux is simulated.
type budgetWireRig struct {
	*staffingRig
	f *wiringCmux
}

// newBudgetWireRig: one master-scope account, its 7-day window at used7d and
// resetting in 84 h (half the week: the default budget allows 40 %), its
// reading age old. override edits the scheduler's deps.
func newBudgetWireRig(t *testing.T, used7d float64, age time.Duration, override func(*staffing.Deps)) *budgetWireRig {
	t.Helper()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	t.Setenv("PIGTAIL_ROTATION", "")
	cfgHome, cellDir := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // the margin traces' spawn registry
	master := string(scope.Master())
	table := fmt.Sprintf("cells:\n  mp: { config_dir: %s, scope: %s, account: m, rank: 0, cwd_home: %s }\n", cellDir, master, cellDir)
	if err := os.MkdirAll(filepath.Join(cfgHome, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgHome, "pigtail", claudeprofile.Filename), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	const uuid = "11111111-1111-4111-8111-111111111111"
	now := time.Now().UTC()
	const stamp = "2006-01-02T15:04:05.000000+00:00"
	usage := fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q},
"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":%q,"utilization":{"five_hour":{"utilization":5,"resets_at":%q},"seven_day":{"utilization":%v,"resets_at":%q}}}}`,
		uuid, now.Add(-age).UnixMilli(), uuid, now.Add(time.Hour).Format(stamp), used7d, now.Add(84*time.Hour).Format(stamp))
	if err := os.WriteFile(filepath.Join(cellDir, claudeusage.FileName), []byte(usage), 0o600); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(t.TempDir(), "pigtail.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_r", Name: "R", Kind: "ongoing", Scope: master, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_r", ThemeID: "thm_r", Title: "Ready one", ShortTitle: "Ready one",
		Slug: "r", Status: pigtail.StatusUnderstanding, Rank: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	rig := &budgetWireRig{staffingRig: &staffingRig{tours: make(chan pigtail.StaffingDecision, 64)}, f: &wiringCmux{}}
	launchDir, reg := t.TempDir(), filepath.Join(t.TempDir(), "spawns.jsonl")
	cfg := Config{SocketPath: testSocketPath(t), DBPath: dbPath, SessionBudget: sessbudget.Config{Ceiling: 3, Floor: 1}, SessionSampleSeconds: 60,
		LaunchOverrides: func(d *launch.Deps) {
			rig.f.wire(d)
			d.Registry = spawnreg.Open(reg)
			d.LaunchDir = func(string) (string, bool) { return launchDir, true }
			d.Mode = func(string) (string, error) { return "auto", nil }
			d.Procs = func() ([]claudesession.Proc, error) { return nil, nil }
			d.Environ = func(int) (string, error) { return "", nil }
			d.PollEvery = 5 * time.Millisecond
			d.Logf = t.Logf
		},
		StaffingOverrides: func(d *staffing.Deps) {
			d.Every = time.Hour
			d.Machine = func() (bool, string) { return true, "" }
			d.Logf = t.Logf
			d.AfterTour = func(dec pigtail.StaffingDecision) {
				select {
				case rig.tours <- dec:
				default:
				}
			}
			if override != nil {
				override(d)
			}
		}}
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.sessionBudget.Observe(pigtail.SessionSample{TS: time.Now(), Live: 0, Busy: 0})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	srv.launcher.Start(ctx)
	srv.startStaffing(ctx, func(fn func()) { wg.Add(1); go func() { defer wg.Done(); fn() }() })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		srv.launcher.Wait()
	})
	rig.srv, rig.h = srv, srv.router
	return rig
}

func (rig *budgetWireRig) created() int {
	rig.f.mu.Lock()
	defer rig.f.mu.Unlock()
	return len(rig.f.specs)
}

// launchTour turns the mode to launch and returns its first tour's decision.
func (rig *budgetWireRig) launchTour(t *testing.T) pigtail.StaffingDecision {
	t.Helper()
	rig.drain(t)
	if rec := rig.serve(t, "POST", "/staffing", map[string]any{"mode": "launch"}, true); rec.Code != http.StatusOK {
		t.Fatalf("POST /staffing = %d %s", rec.Code, rec.Body)
	}
	for {
		if d := rig.tour(t, 5*time.Second); d.Mode == pigtail.StaffingLaunch && d.Outcome != pigtail.StaffingOutcomeSettingsChanged {
			return d
		}
	}
}

func TestWireStaffingUsesBudgetPaths(t *testing.T) {
	t.Run("the gate refuses an over-budget account, the API launch does not", func(t *testing.T) {
		rig := newBudgetWireRig(t, 60, time.Minute, nil)
		d := rig.launchTour(t)
		if d.Outcome != pigtail.StaffingOutcomeIdle || d.Gate != pigtail.StaffingGateOverBudget || !strings.Contains(d.Reason, "weekly token budget") {
			t.Fatalf("tour = %+v, want idle over_budget", d)
		}
		if n := rig.created(); n != 0 {
			t.Fatalf("%d tabs created past the budget", n)
		}
		// D6: the same subject, the same account, through the API.
		rec := rig.serve(t, "POST", "/subjects/sub_r/launch", nil, false)
		if rec.Code != http.StatusAccepted || decodeData[pigtail.SessionLaunch](t, rec).State != pigtail.LaunchPending {
			t.Fatalf("API launch over budget = %d %s, want 202 pending", rec.Code, rec.Body)
		}
		if n := rig.created(); n != 1 {
			t.Fatalf("API launch: %d tabs", n)
		}
	})

	t.Run("a Staffing launch picks through the budget, past the gate", func(t *testing.T) {
		rig := newBudgetWireRig(t, 60, time.Minute, func(d *staffing.Deps) {
			d.TokenBudget = func(string) (bool, string) { return true, "" } // the race: the gate saw it open
		})
		d := rig.launchTour(t)
		if d.Outcome != pigtail.StaffingOutcomeLaunchRefused || !strings.Contains(d.Cause, "weekly token budget") {
			t.Fatalf("tour = %+v, want launch_refused with the budget's text", d)
		}
		if n := rig.created(); n != 0 {
			t.Fatalf("%d tabs: the launch ran on the plain picker", n)
		}
	})

	t.Run("a reading of 31 min closes as unmeasured", func(t *testing.T) {
		rig := newBudgetWireRig(t, 1, 31*time.Minute, nil)
		d := rig.launchTour(t)
		if d.Outcome != pigtail.StaffingOutcomeIdle || d.Gate != pigtail.StaffingGateOverBudget || !strings.HasPrefix(d.Reason, "unmeasured: ") {
			t.Fatalf("tour = %+v, want idle over_budget unmeasured", d)
		}
		if n := rig.created(); n != 0 {
			t.Fatalf("%d tabs on an unmeasured account", n)
		}
	})
}
