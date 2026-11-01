package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var mt0 = time.Date(2026, 10, 4, 14, 0, 0, 0, time.Local)

func deps(t *testing.T, daemonErr error) (maintDeps, *bytes.Buffer, *bytes.Buffer, *int) {
	var out, errw bytes.Buffer
	calls := 0
	d := maintDeps{
		dir: t.TempDir(), now: func() time.Time { return mt0 },
		resolve: func(a string) (string, string, string, error) {
			if a == "lead-scope" {
				return "sid-lead", "lead-scope", "perso", nil
			}
			return "", "", "", errors.New("no session matches")
		},
		// The caller reads from the perso cell (the « (h1) » of the labels
		// below): it sees every session it names (D22).
		reader: func() (scope.Scope, error) { return "perso", nil },
		out:    &out, errw: &errw,
	}
	fakeDaemon(&d, &calls, daemonErr)
	return d, &out, &errw, &calls
}

// fakeDaemon answers like a daemon reading d.dir (what GET /maintenance says).
func fakeDaemon(d *maintDeps, calls *int, daemonErr error) {
	d.daemonState = func() (pigtail.MaintenanceView, error) {
		*calls++
		if daemonErr != nil {
			return pigtail.MaintenanceView{}, daemonErr
		}
		st, ok, err := maintenance.Load(d.dir)
		v := pigtail.MaintenanceView{Dir: d.dir}
		if err != nil {
			v.Present, v.Unreadable = true, err.Error()
		} else if ok && st.On {
			v.Present, v.ID, v.On, v.Freeze = true, st.ID, true, st.Freeze
		}
		return v, nil
	}
	d.sidebar = func() (bool, error) { return true, nil }
}

// One criterion (t14): the daemon must read the directory the CLI wrote in.
// Another directory → exit 1 for on and off, whatever it holds; the same
// directory reached through a symlink → success.
func TestMaintenanceDaemonDirCriterion(t *testing.T) {
	t.Parallel()
	other := t.TempDir()
	withOn := t.TempDir()
	_ = maintenance.Save(withOn, maintenance.State{ID: "mnt_other", On: true, Since: time.Now()})
	viewOf := func(dir string) pigtail.MaintenanceView {
		st, ok, _ := maintenance.Load(dir)
		return pigtail.MaintenanceView{Dir: dir, Present: ok && st.On, ID: st.ID, On: st.On}
	}
	cases := []struct {
		name  string
		off   bool
		dir   func(d maintDeps) string
		wantE bool
	}{
		{"on, other empty dir", false, func(maintDeps) string { return other }, true},
		{"off, other empty dir", true, func(maintDeps) string { return other }, true},
		{"off, other dir with an on", true, func(maintDeps) string { return withOn }, true},
		{"on, same dir via symlink", false, func(d maintDeps) string {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(d.dir, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, false},
	}
	for _, c := range cases {
		d, _, _, _ := deps(t, nil)
		if c.off {
			if err := runMaintenanceOn(d, "bascule", "", true, ""); err != nil {
				t.Fatal(err)
			}
		}
		dd := c.dir(d)
		d.daemonState = func() (pigtail.MaintenanceView, error) { return viewOf(dd), nil }
		var err error
		if c.off {
			err = runMaintenanceOff(d)
		} else {
			err = runMaintenanceOn(d, "bascule", "", true, "")
		}
		gotE := err != nil && strings.Contains(err.Error(), "daemon reads "+dd+": freeze and pill NOT active")
		if gotE != c.wantE || (!c.wantE && err != nil) {
			t.Fatalf("%s: err=%v", c.name, err)
		}
	}
}

// Against REAL servers through the REAL client: an old pigtaild (chi's
// plain-text 404) and an unwired one (JSON 500) both answer → exit 1; only
// a transport error (nothing listens) is a warning with exit 0.
func TestMaintenanceOnDaemonDoesNotApply(t *testing.T) {
	t.Parallel()
	for name, h := range map[string]http.HandlerFunc{
		"old pigtaild, plain 404": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, // "404 page not found"
		"unwired, JSON 500": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"maintenance not wired"}}`))
		},
	} {
		srv := httptest.NewServer(h)
		c := pigtail.NewHTTPClient(strings.TrimPrefix(srv.URL, "http://"), "")
		d, _, errw, _ := deps(t, nil)
		d.daemonState = func() (pigtail.MaintenanceView, error) {
			v, err := c.Maintenance(context.Background())
			if err != nil {
				return pigtail.MaintenanceView{}, err
			}
			return *v, nil
		}
		err := runMaintenanceOn(d, "bascule", "", true, "")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "daemon answers but does not apply maintenance") ||
			!strings.Contains(err.Error(), "restart pigtaild from this build") || strings.Contains(errw.String(), "unreachable") {
			t.Fatalf("%s: err=%v stderr=%s", name, err, errw.String())
		}
	}
	// Transport error: a closed server.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	c := pigtail.NewHTTPClient(addr, "")
	d, _, errw, _ := deps(t, nil)
	d.daemonState = func() (pigtail.MaintenanceView, error) {
		v, err := c.Maintenance(context.Background())
		if err != nil {
			return pigtail.MaintenanceView{}, err
		}
		return *v, nil
	}
	if err := runMaintenanceOn(d, "bascule", "", true, ""); err != nil || !strings.Contains(errw.String(), "warning: daemon unreachable") {
		t.Fatalf("transport error must warn with exit 0: err=%v stderr=%s", err, errw.String())
	}
}

func TestMaintenanceOnPrintsDaemonAndSidebar(t *testing.T) {
	t.Parallel()
	d, out, _, _ := deps(t, nil)
	d.sidebar = func() (bool, error) { return false, nil }
	if err := runMaintenanceOn(d, "bascule", "", false, ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"daemon reads " + d.dir + ": freeze off", "sidebar pill: off (pigtail cmux sidebar on)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	out.Reset()
	if err := runMaintenanceStatus(d, false); err != nil || !strings.Contains(out.String(), "sidebar pill: off (pigtail cmux sidebar on)") {
		t.Fatalf("status: %v %s", err, out)
	}
}

func TestMaintenanceOnWritesChecksAndRefusesTwice(t *testing.T) {
	t.Parallel()
	d, out, _, calls := deps(t, nil)
	if err := runMaintenanceOn(d, "bascule", "2h", true, "lead-scope"); err != nil {
		t.Fatal(err)
	}
	st, ok, _ := maintenance.Load(d.dir)
	if !ok || !st.On || !st.Freeze || st.Exempt != "sid-lead" || st.Until == nil || *calls != 1 {
		t.Fatalf("state %+v calls %d", st, *calls)
	}
	if !strings.Contains(out.String(), "freeze") || !strings.Contains(out.String(), "lead-scope") {
		t.Fatalf("summary: %s", out)
	}
	if err := runMaintenanceOn(d, "encore", "", false, ""); err == nil || !strings.Contains(err.Error(), "already on") {
		t.Fatalf("second on: %v", err)
	}
}

func TestMaintenanceOnDaemonDownStillWrites(t *testing.T) {
	t.Parallel()
	d, _, errw, _ := deps(t, errors.New("dial unix: connection refused"))
	if err := runMaintenanceOn(d, "bascule", "", false, ""); err != nil {
		t.Fatalf("must not fail: %v", err)
	}
	if _, ok, _ := maintenance.Load(d.dir); !ok {
		t.Fatal("state not written")
	}
	if !strings.Contains(errw.String(), "warning: daemon unreachable") {
		t.Fatalf("stderr: %s", errw)
	}
}

func TestMaintenanceOnBadExemptWritesNothing(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	if err := runMaintenanceOn(d, "bascule", "", true, "nobody"); err == nil {
		t.Fatal("accepted")
	}
	if _, ok, _ := maintenance.Load(d.dir); ok {
		t.Fatal("state written despite a bad --exempt")
	}
	if err := runMaintenanceOn(d, "", "", false, ""); err == nil {
		t.Fatal("empty reason accepted")
	}
}

// D8: off repairs an unreadable state; on refuses on it; status says how to repair.
func TestMaintenanceOffRepairsCorruptState(t *testing.T) {
	t.Parallel()
	d, _, errw, calls := deps(t, nil)
	_ = os.WriteFile(filepath.Join(d.dir, maintenance.FileName), []byte("{"), 0o600)
	if err := runMaintenanceOn(d, "x", "", false, ""); err == nil || !strings.Contains(err.Error(), "pigtail maintenance off") {
		t.Fatalf("on over a corrupt state: %v", err)
	}
	if err := runMaintenanceStatus(d, false); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("status: %v", err)
	}
	if err := runMaintenanceOff(d); err != nil {
		t.Fatalf("off must repair: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.dir, maintenance.FileName)); !os.IsNotExist(err) || *calls != 1 {
		t.Fatalf("off on a corrupt file must delete it: %v, calls %d", err, *calls)
	}
	if st, _, err := maintenance.Load(d.dir); err != nil || st.FrozenFor("x") {
		t.Fatalf("after off: %+v %v", st, err)
	}
	if !strings.Contains(errw.String(), "deleting it") {
		t.Fatalf("stderr: %s", errw)
	}
	tok, _ := os.ReadFile(filepath.Join(d.dir, maintenance.TokenName))
	if !strings.Contains(string(tok), " off ") {
		t.Fatalf("token not rewritten: %q", tok)
	}
}

func TestMaintenanceOffAndStatus(t *testing.T) {
	t.Parallel()
	d, out, _, calls := deps(t, nil)
	if err := runMaintenanceOff(d); err == nil {
		t.Fatal("off without maintenance")
	}
	_ = runMaintenanceOn(d, "bascule", "", false, "")
	if err := runMaintenanceOff(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.dir, maintenance.FileName)); !os.IsNotExist(err) || *calls != 2 {
		t.Fatalf("off must delete the state file: %v, calls %d", err, *calls)
	}
	if err := runMaintenanceOff(d); err == nil {
		t.Fatal("second off: nothing in progress")
	}
	out.Reset()
	if err := runMaintenanceStatus(d, false); err != nil || !strings.Contains(out.String(), "lifted") {
		t.Fatalf("status: %v %s", err, out)
	}
}

// Contract table for the state directory, identical to the shell hook's maintenance-dir test (M1, cases C1–C5).
func TestMaintenanceDirContract(t *testing.T) {
	old := flagSocket
	flagSocket = ""
	defer func() { flagSocket = old }()
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"C1", map[string]string{"PIGTAIL_MAINTENANCE_DIR": "/a", "PIGTAIL_SOCKET": "/b/s"}, "/a"},
		{"C2", map[string]string{"PIGTAIL_SOCKET": "/b/s"}, "/b"},
		{"C3", map[string]string{"XDG_CONFIG_HOME": "/x"}, "/x/pigtail"},
		{"C4", map[string]string{"HOME": "/h"}, "/h/.config/pigtail"},
		{"C5", map[string]string{"PIGTAIL_SOCKET": "/b/s", "XDG_CONFIG_HOME": "/x"}, "/b"},
	}
	for _, c := range cases {
		for _, k := range []string{"PIGTAIL_MAINTENANCE_DIR", "PIGTAIL_SOCKET", "XDG_CONFIG_HOME", "HOME"} {
			t.Setenv(k, c.env[k])
		}
		if got := maintenanceDir(os.Getenv); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// maintenanceDir reads every variable through the getenv it is given, never
// os.Getenv behind its back (review t16): the process environment says one
// socket, the injected one another — the injected one wins.
func TestMaintenanceDirUsesInjectedGetenv(t *testing.T) {
	old := flagSocket
	flagSocket = ""
	defer func() { flagSocket = old }()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	t.Setenv("PIGTAIL_SOCKET", "/process/s")
	env := map[string]string{"PIGTAIL_SOCKET": "/injected/s"}
	if got := maintenanceDir(func(k string) string { return env[k] }); got != "/injected" {
		t.Fatalf("maintenanceDir = %q, want /injected", got)
	}
	env = map[string]string{"HOME": "/h"}
	if got := maintenanceDir(func(k string) string { return env[k] }); got != "/h/.config/pigtail" {
		t.Fatalf("maintenanceDir = %q, want /h/.config/pigtail", got)
	}
}

// The freeze summary lists what the freeze refuses, like the notice's GEL line.
func TestMaintenanceSummaryFreezeList(t *testing.T) {
	t.Parallel()
	d, out, _, _ := deps(t, nil)
	if err := runMaintenanceOn(d, "bascule", "", true, ""); err != nil {
		t.Fatal(err)
	}
	want := "freeze ON (subject writes, pigtail new, pigtail dispatch, pigtail session spawn)"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("on summary: %s", out)
	}
	out.Reset()
	if err := runMaintenanceStatus(d, false); err != nil || !strings.Contains(out.String(), want) {
		t.Fatalf("status: %v %s", err, out)
	}
}

func asSession(d maintDeps, sid string) maintDeps {
	d.session = sid
	prev := d.resolve
	d.resolve = func(a string) (string, string, string, error) {
		if a == "11111111-1111-1111-1111-111111111111" {
			return a, "Alpha · pigtail (h1)", "perso", nil
		}
		return prev(a)
	}
	return d
}

const sidA, sidB = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"

func TestMaintenanceOnRecordsOwner(t *testing.T) {
	t.Parallel()
	d, out, _, _ := deps(t, nil)
	if err := runMaintenanceOn(asSession(d, sidA), "bascule", "", true, ""); err != nil {
		t.Fatal(err)
	}
	st, _, _ := maintenance.Load(d.dir)
	if st.Owner != sidA || st.OwnerLabel != "Alpha · pigtail (h1)" || !strings.Contains(out.String(), "owner  Alpha · pigtail (h1) ["+sidA+"]") {
		t.Fatalf("%+v\n%s", st, out)
	}
}

func TestMaintenanceOnOwnerLabelFallsBackToID(t *testing.T) { // and the id is printed once, never « <uuid> [<uuid>] »
	t.Parallel()
	d, out, _, _ := deps(t, nil)
	d.session = sidB // resolve() of deps() does not know it
	if err := runMaintenanceOn(d, "bascule", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := maintenance.Load(d.dir); st.Owner != sidB || st.OwnerLabel != sidB || !strings.Contains(out.String(), "owner  "+sidB+"\n") || strings.Contains(out.String(), "["+sidB+"]") {
		t.Fatalf("%+v\n%s", st, out)
	}
}

func TestMaintenanceOnFromTerminalOwnerIsAda(t *testing.T) {
	t.Parallel()
	d, out, _, _ := deps(t, nil)
	if err := runMaintenanceOn(d, "bascule", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := maintenance.Load(d.dir); st.Owner != "" || !strings.Contains(out.String(), "owner  the user (outside Claude Code)") {
		t.Fatalf("%+v\n%s", st, out)
	}
}

func TestMaintenanceOnInvalidSessionID(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	d.session = "../x"
	if err := runMaintenanceOn(d, "bascule", "", false, ""); err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_SESSION_ID is not a session id") {
		t.Fatalf("%v", err)
	}
	if _, ok, _ := maintenance.Load(d.dir); ok {
		t.Fatal("state written")
	}
}

func TestMaintenanceOnExemptPlusOwner(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	if err := runMaintenanceOn(asSession(d, sidA), "bascule", "", true, "lead-scope"); err != nil {
		t.Fatal(err)
	}
	st, _, _ := maintenance.Load(d.dir)
	if st.Owner != sidA || st.Exempt != "sid-lead" || st.FrozenFor(sidA) || st.FrozenFor("sid-lead") || !st.FrozenFor(sidB) {
		t.Fatalf("%+v", st)
	}
}

func TestMaintenanceOnExemptIsOwnerIgnored(t *testing.T) {
	t.Parallel()
	d, out, _, _ := deps(t, nil)
	d = asSession(d, sidA)
	prev := d.resolve
	d.resolve = func(a string) (string, string, string, error) {
		if a == "me" {
			return sidA, "Alpha · pigtail (h1)", "perso", nil
		}
		return prev(a)
	}
	if err := runMaintenanceOn(d, "bascule", "", true, "me"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := maintenance.Load(d.dir); st.Exempt != "" || st.Owner != sidA || !strings.Contains(out.String(), "exempt (owner, already exempt)") {
		t.Fatalf("%+v\n%s", st, out)
	}
}

func TestMaintenanceOffInvalidSessionID(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = runMaintenanceOn(d, "bascule", "", false, "")
	before := stateBytes(t, d.dir)
	d.session = "../x"
	if err := runMaintenanceOff(d); err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_SESSION_ID is not a session id") {
		t.Fatalf("%v", err)
	}
	if stateBytes(t, d.dir) != before {
		t.Fatal("state touched")
	}
}

func TestMaintenanceOnUnreadableFromSession(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = os.WriteFile(filepath.Join(d.dir, maintenance.FileName), []byte("{"), 0o600)
	if err := runMaintenanceOn(asSession(d, sidA), "x", "", false, ""); err == nil ||
		!strings.Contains(err.Error(), "only the user, outside Claude Code, repairs it; do not run `off`") {
		t.Fatalf("%v", err)
	}
}

func TestMaintenanceOnAlreadyOnNamesOwner(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = runMaintenanceOn(asSession(d, sidA), "bascule", "", false, "")
	err := runMaintenanceOn(asSession(d, sidB), "autre", "", false, "")
	if err == nil || !strings.Contains(err.Error(), "already on") || !strings.Contains(err.Error(), "set by session Alpha · pigtail (h1)") {
		t.Fatalf("%v", err)
	}
}

func stateBytes(t *testing.T, dir string) string {
	var b strings.Builder
	for _, n := range []string{maintenance.FileName, maintenance.TokenName, maintenance.LiftedName} {
		raw, _ := os.ReadFile(filepath.Join(dir, n))
		fi, _ := os.Stat(filepath.Join(dir, n))
		mt := ""
		if fi != nil {
			mt = fi.ModTime().String()
		}
		b.WriteString(n + "=" + string(raw) + "@" + mt + "\n")
	}
	return b.String()
}

func TestMaintenanceOffByOtherSessionRefused(t *testing.T) {
	t.Parallel()
	d, _, _, calls := deps(t, nil)
	_ = runMaintenanceOn(asSession(d, sidA), "bascule", "", true, "")
	before, n := stateBytes(t, d.dir), *calls
	err := runMaintenanceOff(asSession(d, sidB))
	if err == nil || exitCode(err) != 1 || !strings.Contains(err.Error(), "set by session Alpha · pigtail (h1) ["+sidA+"]") ||
		!strings.Contains(err.Error(), "only that session, or the user outside Claude Code, can lift it") ||
		!strings.Contains(err.Error(), "do not work around this") {
		t.Fatalf("%v", err)
	}
	if stateBytes(t, d.dir) != before || *calls != n {
		t.Fatal("a refused off must touch nothing, daemon included")
	}
}

func TestMaintenanceOffByExemptRefused(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = runMaintenanceOn(asSession(d, sidA), "bascule", "", true, "lead-scope")
	if err := runMaintenanceOff(asSession(d, "sid-lead")); err == nil {
		t.Fatal("the exempt session must not lift")
	}
}

func TestMaintenanceOffByOwner(t *testing.T) { // a subagent of the owner carries the same id: same path
	t.Parallel()
	d, out, _, _ := deps(t, nil)
	_ = runMaintenanceOn(asSession(d, sidA), "bascule", "", true, "")
	if err := runMaintenanceOff(asSession(d, sidA)); err != nil {
		t.Fatal(err)
	}
	st, _, _ := maintenance.Load(d.dir)
	if st.On || st.LiftedBy != "Alpha · pigtail (h1)" || !strings.Contains(out.String(), "lifted") {
		t.Fatalf("%+v\n%s", st, out)
	}
}

func TestMaintenanceOffByAda(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = runMaintenanceOn(asSession(d, sidA), "bascule", "", true, "")
	if err := runMaintenanceOff(d); err != nil { // no session: Ada outside Claude Code (dead owner case)
		t.Fatal(err)
	}
	if st, _, _ := maintenance.Load(d.dir); st.On || !st.LiftedByUser || st.LiftedBy != "" {
		t.Fatalf("%+v", st)
	}
}

func TestMaintenanceOffAdaOwnedRefusesSessions(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = runMaintenanceOn(d, "bascule", "", false, "")
	if err := runMaintenanceOff(asSession(d, sidA)); err == nil || !strings.Contains(err.Error(), "set by the user — only they lift it") {
		t.Fatalf("%v", err)
	}
}

func TestMaintenanceOffUnreadableRefusesSessions(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = os.WriteFile(filepath.Join(d.dir, maintenance.FileName), []byte(`{"id":"mnt_a","owner":"`+sidA), 0o600)
	err := runMaintenanceOff(asSession(d, sidA)) // even the former owner: unreadable too (T1)
	if err == nil || !strings.Contains(err.Error(), "unreadable") || !strings.Contains(err.Error(), "only the user, outside Claude Code, repairs it; do not run `off`") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(d.dir, maintenance.FileName)); err != nil {
		t.Fatal("a refused repair must leave the file for Ada")
	}
}

func TestMaintenanceStatusShowsOwner(t *testing.T) {
	t.Parallel()
	d, out, _, _ := deps(t, nil)
	_ = runMaintenanceOn(asSession(d, sidA), "bascule", "", false, "")
	out.Reset()
	if err := runMaintenanceStatus(d, false); err != nil || !strings.Contains(out.String(), "owner  Alpha · pigtail (h1)") {
		t.Fatalf("%v %s", err, out)
	}
	out.Reset()
	if err := runMaintenanceStatus(d, true); err != nil || !strings.Contains(out.String(), `"owner":"`+sidA+`"`) ||
		!strings.Contains(out.String(), `"owner_label":"Alpha · pigtail (h1)"`) {
		t.Fatalf("json: %v %s", err, out)
	}
}

func TestMaintenanceHelpNamesOwner(t *testing.T) {
	t.Parallel()
	c := maintenanceCmd()
	if strings.Contains(c.Short, "the user only") || !strings.Contains(c.Short, "lifted by its owner or by the user") {
		t.Fatalf("%q", c.Short)
	}
}

// cobra reads a `backticked` word in a flag usage as the value name: the
// --exempt help must keep "string" and say who is always exempt in plain words.
func TestMaintenanceOnExemptHelp(t *testing.T) {
	t.Parallel()
	on, _, err := maintenanceCmd().Find([]string{"on"})
	if err != nil {
		t.Fatal(err)
	}
	u := on.Flags().FlagUsages()
	if !strings.Contains(u, "--exempt string") || !strings.Contains(u, "the session that runs it is always exempt") || strings.Contains(u, "--exempt on") {
		t.Fatalf("%s", u)
	}
}

// D4: the lift clears the frozen markers; a refused lift leaves them.
func TestMaintenanceOffClearsFrozenMarkers(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = runMaintenanceOn(asSession(d, sidA), "bascule", "", true, "")
	st, _, _ := maintenance.Load(d.dir)
	if err := maintenance.MarkFrozen(d.dir, "s-x", st.ID); err != nil {
		t.Fatal(err)
	}
	if err := runMaintenanceOff(asSession(d, sidB)); err == nil {
		t.Fatal("another session lifted")
	}
	if _, err := os.Stat(filepath.Join(d.dir, maintenance.FrozenDirName, "s-x")); err != nil {
		t.Fatalf("a refused lift removed the markers: %v", err)
	}
	if err := runMaintenanceOff(asSession(d, sidA)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.dir, maintenance.FrozenDirName)); !os.IsNotExist(err) {
		t.Fatalf("markers left after the lift: %v", err)
	}
}

// Ada outside Claude Code lifts too: the markers go with it.
func TestMaintenanceOffByAdaClearsFrozenMarkers(t *testing.T) {
	t.Parallel()
	d, _, _, _ := deps(t, nil)
	_ = runMaintenanceOn(asSession(d, sidA), "bascule", "", true, "")
	st, _, _ := maintenance.Load(d.dir)
	_ = maintenance.MarkFrozen(d.dir, "s-x", st.ID)
	if err := runMaintenanceOff(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.dir, maintenance.FrozenDirName)); !os.IsNotExist(err) {
		t.Fatalf("markers left after the lift: %v", err)
	}
}
