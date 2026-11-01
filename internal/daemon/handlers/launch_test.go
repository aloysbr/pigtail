package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeLauncher answers Launch with a fixed result, and Get from a map.
type fakeLauncher struct {
	err      error
	getErr   error // Get's error, when set
	out      pigtail.SessionLaunch
	launched []string
	byID     map[string]pigtail.SessionLaunch
}

func (f *fakeLauncher) Launch(_ context.Context, sub pigtail.Subject) (pigtail.SessionLaunch, error) {
	f.launched = append(f.launched, sub.ID)
	if f.err != nil {
		return pigtail.SessionLaunch{}, f.err
	}
	out := f.out
	out.SubjectID = sub.ID
	return out, nil
}

func (f *fakeLauncher) Get(id string) (pigtail.SessionLaunch, bool, error) {
	if f.getErr != nil {
		return pigtail.SessionLaunch{}, false, f.getErr
	}
	s, ok := f.byID[id]
	return s, ok, nil
}

// launchFixture is scopedFixture (sub_t work, sub_p perso) with a fake
// launcher on the subject routes.
func launchFixture(t *testing.T, fl *fakeLauncher) *chi.Mux {
	t.Helper()
	_, _, svc := scopedFixtureSvc(t)
	if fl != nil {
		svc.Launch = fl
	}
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, detailResponder{})
	return r
}

func pendingLaunch() pigtail.SessionLaunch {
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	return pigtail.SessionLaunch{LaunchID: "ln_1", State: pigtail.LaunchPending, Origin: "api",
		WorkspaceID: "6F0C", WorkspaceRef: "workspace:12", Cell: "mp", PermissionMode: "auto", Cwd: "/w",
		StartedAt: at, UpdatedAt: at}
}

func TestLaunchRouteAcceptsOwnScope(t *testing.T) {
	t.Parallel()
	fl := &fakeLauncher{out: pendingLaunch()}
	r := launchFixture(t, fl)
	for _, c := range []caller{perso, work} {
		sub := "sub_p"
		if c == work {
			sub = "sub_t"
		}
		rec := doAs(t, r, c, "POST", "/subjects/"+sub+"/launch", nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d: %s", c, rec.Code, rec.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"launch_id", "subject_id", "state", "cause", "origin", "workspace_id", "workspace_ref", "cell", "permission_mode", "cwd", "started_at", "updated_at", "deduplicated"} {
			if _, ok := got[k]; !ok {
				t.Fatalf("%s: key %q missing: %s", c, k, rec.Body)
			}
		}
		if got["subject_id"] != sub || got["state"] != "pending" {
			t.Fatalf("%s: body = %s", c, rec.Body)
		}
	}
	// an empty JSON object is a body too
	rec := doAs(t, r, perso, "POST", "/subjects/sub_p/launch", map[string]any{})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("{} body: %d %s", rec.Code, rec.Body)
	}
}

func TestLaunchRouteHidesOtherScope(t *testing.T) {
	t.Parallel()
	fl := &fakeLauncher{out: pendingLaunch()}
	r := launchFixture(t, fl)
	for _, tc := range []struct {
		c   caller
		sub string
	}{{work, "sub_p"}, {workCross, "sub_p"}, {perso, "sub_t"}, {persoCross, "sub_t"}, {none, "sub_p"}} {
		hidden := doAs(t, r, tc.c, "POST", "/subjects/"+tc.sub+"/launch", nil)
		unknown := doAs(t, r, tc.c, "POST", "/subjects/sub_nope/launch", nil)
		if hidden.Code != http.StatusNotFound || hidden.Body.String() != unknown.Body.String() {
			t.Fatalf("%s on %s: %d %s, unknown %d %s", tc.c, tc.sub, hidden.Code, hidden.Body, unknown.Code, unknown.Body)
		}
	}
	if len(fl.launched) != 0 {
		t.Fatalf("launched %v across scopes", fl.launched)
	}
}

func TestLaunchRouteReplayDedupes(t *testing.T) {
	t.Parallel()
	out := pendingLaunch()
	out.Deduplicated = true
	r := launchFixture(t, &fakeLauncher{out: out})
	rec := doAs(t, r, perso, "POST", "/subjects/sub_p/launch", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deduplicated":true`) || !strings.Contains(rec.Body.String(), `"launch_id":"ln_1"`) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
}

func TestLaunchRouteMapsRefusals(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   pigtail.ErrorCode
	}{
		{&launch.Refusal{Code: pigtail.ErrSessionLive, Status: 409, Msg: "live", Details: map[string]any{"subject_id": "sub_p", "workspace_id": "W", "pids": []int{1}}}, 409, pigtail.ErrSessionLive},
		{&launch.Refusal{Code: pigtail.ErrLaunchInProgress, Status: 409, Msg: "in progress", Details: map[string]any{"subject_id": "sub_p", "launch_id": "ln_c"}}, 409, pigtail.ErrLaunchInProgress},
		{&launch.Refusal{Code: pigtail.ErrLaunchBurst, Status: 429, Msg: "burst"}, 429, pigtail.ErrLaunchBurst},
		{&launch.Refusal{Code: pigtail.ErrNoCell, Status: 409, Msg: "no cell"}, 409, pigtail.ErrNoCell},
		{&launch.Refusal{Code: pigtail.ErrBypassRefused, Status: 403, Msg: "bypass"}, 403, pigtail.ErrBypassRefused},
		{&launch.Refusal{Code: pigtail.ErrCellModeUnreadable, Status: 503, Msg: "mode"}, 503, pigtail.ErrCellModeUnreadable},
		{&launch.Refusal{Code: pigtail.ErrNoLaunchDir, Status: 409, Msg: "dir"}, 409, pigtail.ErrNoLaunchDir},
		{&launch.Refusal{Code: pigtail.ErrCmuxUnreachable, Status: 503, Msg: "cmux"}, 503, pigtail.ErrCmuxUnreachable},
		{&launch.Refusal{Code: pigtail.ErrCmuxCreateFailed, Status: 502, Msg: "create"}, 502, pigtail.ErrCmuxCreateFailed},
		{&launch.Refusal{Code: pigtail.ErrLiveUnknown, Status: 503, Msg: "live?"}, 503, pigtail.ErrLiveUnknown},
		{&launch.Refusal{Code: pigtail.ErrRegistryUnreadable, Status: 503, Msg: "registry"}, 503, pigtail.ErrRegistryUnreadable},
		{overMargin(t), 409, pigtail.ErrLaunchOverMargin},
		{errors.New("boom"), 500, pigtail.ErrInternal},
	} {
		r := launchFixture(t, &fakeLauncher{err: tc.err})
		rec := doAs(t, r, perso, "POST", "/subjects/sub_p/launch", nil)
		var body struct {
			Code    pigtail.ErrorCode `json:"code"`
			Message string            `json:"message"`
			Details map[string]any    `json:"details"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.status || body.Code != tc.code {
			t.Errorf("%v: %d %s, want %d %s", tc.err, rec.Code, body.Code, tc.status, tc.code)
		}
		var ref *launch.Refusal
		if errors.As(tc.err, &ref) && ref.Details != nil && body.Details["subject_id"] != "sub_p" {
			t.Errorf("%s: details = %v", tc.code, body.Details)
		}
		if tc.code == pigtail.ErrLaunchOverMargin {
			if strings.Contains(body.Message, "over-margin") || body.Details != nil {
				t.Errorf("over margin answered %q, details %v", body.Message, body.Details)
			}
		}
	}
	t.Run("launcher off", func(t *testing.T) {
		r := launchFixture(t, nil)
		rec := doAs(t, r, perso, "POST", "/subjects/sub_p/launch", nil)
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), "launcher off") || !strings.Contains(rec.Body.String(), string(pigtail.ErrInternal)) {
			t.Fatalf("launcher off: %d %s", rec.Code, rec.Body)
		}
	})
}

// overMargin is a real OverMarginError, from the guard itself.
func overMargin(t *testing.T) error {
	t.Helper()
	rig := newMarginRig(t, "/cells/mp")
	rig.saturate(true)
	d := rig.svc.MarginDeps()
	_, _, err := launchmargin.Guard(d, d.Cells.Cells["mp"], false)
	var om *launchmargin.OverMarginError
	if !errors.As(err, &om) {
		t.Fatalf("guard = %v, want an OverMarginError", err)
	}
	if !strings.Contains(err.Error(), "--over-margin") {
		t.Fatalf("test error lacks the CLI advice: %q", err)
	}
	return err
}

func TestGetLaunchScoped(t *testing.T) {
	t.Parallel()
	fl := &fakeLauncher{byID: map[string]pigtail.SessionLaunch{
		"ln_p":    {LaunchID: "ln_p", SubjectID: "sub_p", State: pigtail.LaunchLaunched, Origin: "api"},
		"ln_t":    {LaunchID: "ln_t", SubjectID: "sub_t", State: pigtail.LaunchPending, Origin: "cli"},
		"ln_cli":  {LaunchID: "ln_cli", State: pigtail.LaunchLaunched, Origin: "cli"},
		"ln_gone": {LaunchID: "ln_gone", SubjectID: "sub_deleted", State: pigtail.LaunchLaunched, Origin: "cli"},
	}}
	r := launchFixture(t, fl)
	unknown := func(c caller) string {
		return doAs(t, r, c, "GET", "/launches/ln_nope", nil).Body.String()
	}
	for _, tc := range []struct {
		c    caller
		id   string
		want int
	}{
		{perso, "ln_p", 200}, {work, "ln_t", 200},
		{work, "ln_p", 404}, {workCross, "ln_p", 404}, {perso, "ln_t", 404}, {persoCross, "ln_t", 404},
		{perso, "ln_cli", 200}, {work, "ln_cli", 404},
		{perso, "ln_gone", 404},
	} {
		rec := doAs(t, r, tc.c, "GET", "/launches/"+tc.id, nil)
		if rec.Code != tc.want {
			t.Errorf("%s GET %s: %d %s, want %d", tc.c, tc.id, rec.Code, rec.Body, tc.want)
			continue
		}
		if tc.want == 404 && rec.Body.String() != unknown(tc.c) {
			t.Errorf("%s GET %s: %s differs from an unknown id %s", tc.c, tc.id, rec.Body, unknown(tc.c))
		}
		if tc.want == 200 && !strings.Contains(rec.Body.String(), `"launch_id":"`+tc.id+`"`) {
			t.Errorf("%s GET %s: %s", tc.c, tc.id, rec.Body)
		}
	}
	if !strings.Contains(unknown(perso), "no such launch") {
		t.Fatalf("unknown launch = %s", unknown(perso))
	}
}

func TestGetLaunchRefusals(t *testing.T) {
	t.Parallel()
	t.Run("launcher off", func(t *testing.T) {
		r := launchFixture(t, nil)
		rec := doAs(t, r, perso, "GET", "/launches/ln_p", nil)
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), "launcher off") || !strings.Contains(rec.Body.String(), string(pigtail.ErrInternal)) {
			t.Fatalf("launcher off: %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("registry unreadable", func(t *testing.T) {
		r := launchFixture(t, &fakeLauncher{getErr: errors.New("read spawns.jsonl: input/output error")})
		rec := doAs(t, r, perso, "GET", "/launches/ln_p", nil)
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), string(pigtail.ErrRegistryUnreadable)) {
			t.Fatalf("registry unreadable: %d %s", rec.Code, rec.Body)
		}
	})
}

// recordingLauncher is fakeLauncher keeping the subjects it was handed.
type recordingLauncher struct {
	fakeLauncher
	subs []pigtail.Subject
}

func (f *recordingLauncher) Launch(ctx context.Context, sub pigtail.Subject) (pigtail.SessionLaunch, error) {
	f.subs = append(f.subs, sub)
	return f.fakeLauncher.Launch(ctx, sub)
}

// webLaunchFixture is scopedFixture with a recording launcher, its store
// and the non-master subject (sub_t).
func webLaunchFixture(t *testing.T, fl *recordingLauncher) (*chi.Mux, *store.Store) {
	t.Helper()
	_, st, svc := scopedFixtureSvc(t)
	svc.Launch = fl
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, detailResponder{})
	return r, st
}

// The web front launches a subject of any scope (D1), with that subject's scope.
func TestLaunchRouteWebAnyScope(t *testing.T) {
	t.Parallel()
	fl := &recordingLauncher{fakeLauncher: fakeLauncher{out: pendingLaunch()}}
	r, st := webLaunchFixture(t, fl)
	other, err := st.GetSubject("sub_t")
	if err != nil || scope.Canonical(other.Scope) == scope.Master() {
		t.Fatalf("sub_t must be outside the master: %+v %v", other, err)
	}
	rec := doWeb(t, r, "POST", "/subjects/sub_t/launch", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("web front launch of %s's subject = %d %s, want 202", other.Scope, rec.Code, rec.Body)
	}
	if len(fl.subs) != 1 || fl.subs[0].ID != "sub_t" || fl.subs[0].Scope != other.Scope {
		t.Fatalf("launcher got %+v, want sub_t of scope %s", fl.subs, other.Scope)
	}
}

// The CLI keeps the lot 2 rule: the master with its opt-in does not launch
// another scope's subject.
func TestLaunchRouteCLIStillHidesOtherScope(t *testing.T) {
	t.Parallel()
	fl := &recordingLauncher{fakeLauncher: fakeLauncher{out: pendingLaunch()}}
	r, _ := webLaunchFixture(t, fl)
	rec := doAs(t, r, caller{scope: string(scope.Master()), cross: true}, "POST", "/subjects/sub_t/launch", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("master CLI with cross on another scope = %d %s, want 404", rec.Code, rec.Body)
	}
	if len(fl.subs) != 0 {
		t.Fatalf("launched %+v", fl.subs)
	}
}

// The web front follows a launch of any scope; the CLI master still does not.
func TestGetLaunchWebAnyScope(t *testing.T) {
	t.Parallel()
	l := pendingLaunch()
	l.SubjectID = "sub_t"
	fl := &recordingLauncher{fakeLauncher: fakeLauncher{byID: map[string]pigtail.SessionLaunch{"ln_1": l}}}
	r, _ := webLaunchFixture(t, fl)
	if rec := doWeb(t, r, "GET", "/launches/ln_1", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sub_t"`) {
		t.Fatalf("web front GET of another scope's launch = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, caller{scope: string(scope.Master()), cross: true}, "GET", "/launches/ln_1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("master CLI GET of another scope's launch = %d %s, want 404", rec.Code, rec.Body)
	}
}
