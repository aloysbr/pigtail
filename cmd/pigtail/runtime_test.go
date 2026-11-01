package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var rt0 = time.Date(2026, 10, 18, 15, 4, 0, 0, time.Local)

const shaA = "aaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// fakeRuntimeDaemon answers like a daemon over deps.dir, or with the errors set.
type fakeRuntimeDaemon struct {
	dir                      string
	alive                    runtimeclaim.AliveFunc
	getErr, claimErr, relErr error
	viewDir                  string
	noExpiry                 bool                      // ClaimRuntime answers like a daemon older than expires_at
	view                     *pigtail.RuntimeClaimView // overrides the GET answer (an older daemon)
	claims, releases, getsN  int
}

func (f *fakeRuntimeDaemon) RuntimeClaim(context.Context) (pigtail.RuntimeClaimView, error) {
	f.getsN++
	if f.getErr != nil {
		return pigtail.RuntimeClaimView{}, f.getErr
	}
	if f.view != nil {
		return *f.view, nil
	}
	d := f.dir
	if f.viewDir != "" {
		d = f.viewDir
	}
	return pigtail.RuntimeClaimView{Dir: d}, nil
}

func (f *fakeRuntimeDaemon) ClaimRuntime(_ context.Context, req pigtail.RuntimeClaimRequest) (pigtail.RuntimeClaimView, error) {
	f.claims++
	if f.claimErr != nil {
		return pigtail.RuntimeClaimView{}, f.claimErr
	}
	st, err := runtimeclaim.Claim(f.dir, runtimeclaim.State{Session: req.Session, Label: req.Label, LabelScope: req.LabelScope,
		PID: req.PID, ConfigDir: req.ConfigDir, Branch: req.Branch, SHA: req.SHA, Dirty: req.Dirty, PR: req.PR, Reason: req.Reason},
		req.TakeOver, f.alive, rt0)
	var held *runtimeclaim.HeldError
	if errors.As(err, &held) {
		return pigtail.RuntimeClaimView{}, conflict(held.Cur, held.Liveness)
	}
	if err != nil {
		return pigtail.RuntimeClaimView{}, err
	}
	return pigtail.RuntimeClaimView{Dir: f.dir, Present: true, ID: st.ID, Session: st.Session, ExpiresAt: f.deadline(st)}, nil
}

func (f *fakeRuntimeDaemon) deadline(st runtimeclaim.State) time.Time {
	if f.noExpiry {
		return time.Time{}
	}
	return st.Deadline()
}

func (f *fakeRuntimeDaemon) ReleaseRuntime(_ context.Context, session string, force bool) (string, error) {
	f.releases++
	if f.relErr != nil {
		return "", f.relErr
	}
	st, ok, err := runtimeclaim.Release(f.dir, session, force, f.alive, rt0)
	var nh *runtimeclaim.NotHolderError
	if errors.As(err, &nh) {
		return "", conflict(nh.Cur, nh.Liveness)
	}
	if err != nil || !ok {
		return "", err
	}
	return st.ID, nil
}

// conflict is the daemon's 409: id, session, since, liveness — nothing else.
func conflict(cur runtimeclaim.State, lv runtimeclaim.Liveness) error {
	return &pigtail.APIError{Code: pigtail.ErrConflict, Message: "runtime held by another session",
		Details: map[string]interface{}{"id": cur.ID, "session": cur.Session, "since": cur.Since, "liveness": string(lv)}}
}

func dialRefused() error {
	return &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
}

func rtDeps(t *testing.T, session string) (runtimeDeps, *fakeRuntimeDaemon, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errw bytes.Buffer
	dir := t.TempDir()
	alive := func(string, int, string) bool { return true }
	fd := &fakeRuntimeDaemon{dir: dir, alive: alive}
	d := runtimeDeps{
		dir: dir, now: func() time.Time { return rt0 }, session: session, cwd: "/wt/feat",
		head: func(context.Context, string) (string, string, bool, error) {
			return "feat/install-guard", shaA, false, nil
		},
		resolve: func(a string) (string, string, string, error) {
			return a, "label of " + a, "perso", nil
		},
		self:   func(string) (int, string) { return 4242, "/cfg" },
		alive:  alive,
		reader: func() (scope.Scope, error) { return "perso", nil },
		daemon: fd, out: &out, errw: &errw,
	}
	return d, fd, &out, &errw
}

func TestRuntimeClaimDaemonUp(t *testing.T) {
	t.Parallel()
	d, fd, out, errw := rtDeps(t, "sess-A")
	if err := runRuntimeClaim(d, "acceptance #218", 218, false); err != nil {
		t.Fatalf("claim: %v %s", err, errw)
	}
	st, ok, err := runtimeclaim.Load(d.dir)
	if err != nil || !ok || st.Session != "sess-A" || st.Branch != "feat/install-guard" || st.SHA != shaA || st.PR != 218 ||
		st.PID != 4242 || st.ConfigDir != "/cfg" || st.Label != "label of sess-A" || st.LabelScope != "perso" {
		t.Fatalf("state = %+v %v %v", st, ok, err)
	}
	if fd.claims != 1 || !strings.Contains(out.String(), st.ID) || strings.Contains(errw.String(), "written to") {
		t.Fatalf("out=%s err=%s claims=%d", out, errw, fd.claims)
	}
}

func TestRuntimeClaimDaemonDownWritesFile(t *testing.T) {
	t.Parallel()
	d, fd, _, errw := rtDeps(t, "sess-A")
	fd.claimErr, fd.relErr = dialRefused(), dialRefused()
	if err := runRuntimeClaim(d, "post-merge #219", 219, false); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if st, ok, _ := runtimeclaim.Load(d.dir); !ok || st.Session != "sess-A" {
		t.Fatalf("file not written: %+v", st)
	}
	if !strings.Contains(errw.String(), "written to") || !strings.Contains(errw.String(), filepath.Join(d.dir, runtimeclaim.FileName)) {
		t.Fatalf("stderr = %s", errw)
	}
	errw.Reset()
	if err := runRuntimeRelease(d, false); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok, _ := runtimeclaim.Load(d.dir); ok {
		t.Fatal("release with the daemon down left the file")
	}
}

func TestRuntimeReleaseNoLeaseDaemonDownSaysNothingWritten(t *testing.T) {
	t.Parallel()
	d, fd, out, errw := rtDeps(t, "sess-A")
	fd.relErr = dialRefused()
	if err := runRuntimeRelease(d, false); err != nil {
		t.Fatalf("release: %v", err)
	}
	if strings.Contains(errw.String(), "written to") || !strings.Contains(errw.String(), "no lease was held") ||
		!strings.Contains(errw.String(), "pigtaild unreachable") || !strings.Contains(out.String(), "no runtime claim") {
		t.Fatalf("stderr=%q out=%q", errw, out)
	}
}

func TestRuntimeClaimOldDaemon404WritesFile(t *testing.T) {
	t.Parallel()
	d, fd, _, errw := rtDeps(t, "sess-A")
	old := &pigtail.HTTPStatusError{Status: 404, Body: "404 page not found\n"}
	fd.claimErr, fd.relErr = old, old
	if err := runRuntimeClaim(d, "post-merge #219", 219, false); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, ok, _ := runtimeclaim.Load(d.dir); !ok || !strings.Contains(errw.String(), "no /runtime routes") {
		t.Fatalf("old daemon: file=%v stderr=%s", ok, errw)
	}
	if err := runRuntimeRelease(d, false); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok, _ := runtimeclaim.Load(d.dir); ok {
		t.Fatal("release through an old daemon left the file")
	}
	d2, fd2, _, _ := rtDeps(t, "sess-A")
	fd2.claimErr = &pigtail.APIError{Code: pigtail.ErrNotFound, Message: "nope"}
	if err := runRuntimeClaim(d2, "r", 0, false); exitCode(err) != 1 {
		t.Fatalf("a JSON NOT_FOUND is not an old daemon: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(d2.dir, runtimeclaim.FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a JSON NOT_FOUND wrote the file")
	}
}

func TestRuntimeClaimHeldNamesHolder(t *testing.T) {
	t.Parallel()
	d, fd, out, errw := rtDeps(t, "sess-A")
	if err := runRuntimeClaim(d, "acceptance #218", 218, false); err != nil {
		t.Fatal(err)
	}
	d.session = "sess-B"
	out.Reset()
	err := runRuntimeClaim(d, "post-merge #219", 219, false)
	msg := errText(err, out, errw)
	if exitCode(err) != 1 || !strings.Contains(msg, "label of sess-A") || !strings.Contains(msg, "branch feat/install-guard @ aaaaaaaaaaaa") ||
		!strings.Contains(msg, "PR #218") || !strings.Contains(msg, "acceptance #218") || strings.Contains(msg, "--take-over") {
		t.Fatalf("held: err=%v msg=%s", err, msg)
	}
	if err := runRuntimeRelease(d, false); exitCode(err) != 1 || !strings.Contains(errText(err, out, errw), "label of sess-A") {
		t.Fatalf("release by another session: %v", err)
	}
	// Stale holder: the refusal proposes --take-over.
	fd.alive = func(string, int, string) bool { return false }
	d.alive = fd.alive
	err = runRuntimeClaim(d, "post-merge #219", 219, false)
	if msg := errText(err, out, errw); exitCode(err) != 1 || !strings.Contains(msg, "stale") || !strings.Contains(msg, "--take-over") || !strings.Contains(msg, "from a terminal outside Claude Code") {
		t.Fatalf("stale: %v %s", err, msg)
	}
	// The file gone between the 409 and the read: only id/session of the details.
	fd.claimErr = conflict(runtimeclaim.State{ID: "rtc_gone", Session: "sess-Z"}, runtimeclaim.Alive)
	_ = os.Remove(filepath.Join(d.dir, runtimeclaim.FileName))
	err = runRuntimeClaim(d, "post-merge #219", 219, false)
	if msg := errText(err, out, errw); exitCode(err) != 1 || !strings.Contains(msg, "rtc_gone") || !strings.Contains(msg, "sess-Z") {
		t.Fatalf("file gone: %v %s", err, msg)
	}
}

func errText(err error, bufs ...*bytes.Buffer) string {
	s := ""
	if err != nil {
		s = err.Error()
	}
	for _, b := range bufs {
		s += "\n" + b.String()
	}
	return s
}

func TestRuntimeClaimOutsideGitFails(t *testing.T) {
	t.Parallel()
	d, fd, _, _ := rtDeps(t, "sess-A")
	d.head = func(context.Context, string) (string, string, bool, error) {
		return "", "", false, errors.New("exit status 128")
	}
	err := runRuntimeClaim(d, "r", 0, false)
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "not a git checkout") || fd.claims != 0 {
		t.Fatalf("err = %v, claims = %d", err, fd.claims)
	}
}

func TestRuntimeReleaseRules(t *testing.T) {
	t.Parallel()
	d, fd, out, errw := rtDeps(t, "sess-A")
	if err := runRuntimeRelease(d, false); err != nil || !strings.Contains(out.String(), "no runtime claim") {
		t.Fatalf("nothing held: %v %s", err, out)
	}
	if err := runRuntimeClaim(d, "acceptance", 0, false); err != nil {
		t.Fatal(err)
	}
	d.session = "sess-B"
	if err := runRuntimeRelease(d, false); exitCode(err) != 1 || !strings.Contains(errText(err, out, errw), "label of sess-A") {
		t.Fatalf("non-holder: %v", err)
	}
	// Holder gone: the refusal proposes release --force, from a terminal outside Claude Code.
	fd.alive = func(string, int, string) bool { return false }
	d.alive = fd.alive
	if msg := errText(runRuntimeRelease(d, false), out, errw); !strings.Contains(msg, "release --force") || !strings.Contains(msg, "from a terminal outside Claude Code") {
		t.Fatalf("non-holder stale hint: %s", msg)
	}
	d.session = ""
	out.Reset()
	if err := runRuntimeRelease(d, true); err != nil || !strings.Contains(out.String(), "released") {
		t.Fatalf("Ada --force: %v %s", err, out)
	}
	if _, ok, _ := runtimeclaim.Load(d.dir); ok {
		t.Fatal("still held after Ada --force")
	}
}

func TestRuntimeStatusReadsFileWithoutDaemon(t *testing.T) {
	t.Parallel()
	d, fd, out, errw := rtDeps(t, "sess-A")
	if err := runRuntimeClaim(d, "acceptance", 7, false); err != nil {
		t.Fatal(err)
	}
	fd.getErr = dialRefused()
	out.Reset()
	if err := runRuntimeStatus(d, true); err != nil {
		t.Fatalf("status: %v %s", err, errw)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v["held"] != true || v["liveness"] != "alive" || v["session"] != "sess-A" ||
		v["branch"] != "feat/install-guard" || v["reason"] != "acceptance" || v["name"] != "label of sess-A" {
		t.Fatalf("status --json = %s (%v)", out, err)
	}
	out.Reset()
	if err := runRuntimeStatus(d, false); err != nil || !strings.Contains(out.String(), "label of sess-A") {
		t.Fatalf("status text: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(d.dir, runtimeclaim.FileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runRuntimeStatus(d, false)
	if exitCode(err) != 2 || !strings.Contains(errText(err, out, errw), filepath.Join(d.dir, runtimeclaim.FileName)) {
		t.Fatalf("unreadable: %v", err)
	}
	out.Reset()
	d2, _, out2, _ := rtDeps(t, "sess-A")
	if err := runRuntimeStatus(d2, false); err != nil || !strings.Contains(out2.String(), "no runtime claim") {
		t.Fatalf("nothing: %v %s", err, out2)
	}
}

func TestRuntimeStatusNameFollowsReaderScope(t *testing.T) {
	t.Parallel()
	d, _, out, _ := rtDeps(t, "sess-A-0123456789")
	if err := runRuntimeClaim(d, "acceptance", 0, false); err != nil {
		t.Fatal(err)
	}
	d.reader = func() (scope.Scope, error) { return scope.Parse("globex") }
	out.Reset()
	if err := runRuntimeStatus(d, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "label of") || !strings.Contains(out.String(), `"name":"sess-A-0"`) {
		t.Fatalf("work reader: %s", out)
	}
	out.Reset()
	_ = runRuntimeStatus(d, false)
	if strings.Contains(out.String(), "label of") {
		t.Fatalf("work reader, text: %s", out)
	}
}

func TestRuntimeDirFollowsSocket(t *testing.T) {
	t.Parallel()
	env := map[string]string{"PIGTAIL_SOCKET": "/x/y.sock", "HOME": "/home"}
	if got := runtimeClaimDir(func(k string) string { return env[k] }); got != "/x" {
		t.Fatalf("dir = %q, want /x", got)
	}
}

func TestRuntimeStatusWarnsDaemonOtherDir(t *testing.T) {
	t.Parallel()
	d, fd, out, errw := rtDeps(t, "sess-A")
	fd.viewDir = "/autre"
	if err := runRuntimeStatus(d, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errw.String(), "warning") || !strings.Contains(errw.String(), "/autre") || !strings.Contains(errw.String(), d.dir) {
		t.Fatalf("stderr = %s", errw)
	}
	fd.viewDir = ""
	errw.Reset()
	out.Reset()
	if err := runRuntimeStatus(d, false); err != nil || errw.Len() != 0 {
		t.Fatalf("same dir: %v stderr=%s", err, errw)
	}
	fd.getErr = dialRefused()
	if err := runRuntimeStatus(d, false); err != nil || errw.Len() != 0 {
		t.Fatalf("daemon down: %v stderr=%s", err, errw)
	}
	fd.getErr = &pigtail.HTTPStatusError{Status: 404, Body: "404 page not found\n"}
	if err := runRuntimeStatus(d, false); err != nil || errw.Len() != 0 {
		t.Fatalf("old daemon: %v stderr=%s", err, errw)
	}
}

func atDeadline(d *runtimeDeps) { d.now = func() time.Time { return rt0.Add(runtimeclaim.TTL) } }

func TestRuntimeStatusJSONExpiresAt(t *testing.T) {
	d, _, out, _ := rtDeps(t, "sess-A")
	if err := runRuntimeClaim(d, "acceptance", 0, false); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runRuntimeStatus(d, true); err != nil {
		t.Fatal(err)
	}
	var v struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || !v.ExpiresAt.Equal(rt0.Add(4*time.Hour)) {
		t.Fatalf("status --json = %s (%v)", out, err)
	}
	out.Reset()
	if err := runRuntimeStatus(d, false); err != nil || !strings.Contains(strings.SplitN(out.String(), "\n", 3)[1], "expires 19:04") {
		t.Fatalf("status text = %q (%v)", out, err)
	}
}

func TestRuntimeStatusLiftsWhenDaemonDidNotAnswer(t *testing.T) {
	oneJSON := func(t *testing.T, out *bytes.Buffer) map[string]any {
		t.Helper()
		dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
		var v map[string]any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("stdout = %q (%v)", out, err)
		}
		if err := dec.Decode(&map[string]any{}); !errors.Is(err, io.EOF) {
			t.Fatalf("stdout holds more than one JSON value: %q", out)
		}
		return v
	}
	cases := map[string]func(fd *fakeRuntimeDaemon, d runtimeDeps){
		"daemon down": func(fd *fakeRuntimeDaemon, _ runtimeDeps) { fd.getErr = dialRefused() },
		"other dir":   func(fd *fakeRuntimeDaemon, _ runtimeDeps) { fd.viewDir = "/autre" },
		"older daemon": func(fd *fakeRuntimeDaemon, d runtimeDeps) {
			fd.view = &pigtail.RuntimeClaimView{Dir: d.dir, Present: true, ID: "x"}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			d, fd, out, errw := rtDeps(t, "sess-A")
			if err := runRuntimeClaim(d, "acceptance", 0, false); err != nil {
				t.Fatal(err)
			}
			st, _, _ := runtimeclaim.Load(d.dir)
			setup(fd, d)
			atDeadline(&d)
			out.Reset()
			errw.Reset()
			if err := runRuntimeStatus(d, true); err != nil {
				t.Fatalf("status: %v", err)
			}
			if v := oneJSON(t, out); v["held"] != false {
				t.Fatalf("status --json = %s", out)
			}
			if !strings.Contains(errw.String(), "expired at") || !strings.Contains(errw.String(), st.ID) {
				t.Fatalf("stderr = %q", errw)
			}
			if _, ok, _ := runtimeclaim.Load(d.dir); ok {
				t.Fatal("lease file still there")
			}
		})
	}
	t.Run("daemon answers for this dir", func(t *testing.T) {
		d, _, out, errw := rtDeps(t, "sess-A")
		if err := runRuntimeClaim(d, "acceptance", 0, false); err != nil {
			t.Fatal(err)
		}
		atDeadline(&d)
		out.Reset()
		if err := runRuntimeStatus(d, true); err != nil || errw.Len() != 0 {
			t.Fatalf("status: %v stderr=%q", err, errw)
		}
		if _, ok, _ := runtimeclaim.Load(d.dir); !ok {
			t.Fatal("the CLI lifted although the daemon answered")
		}
	})
	t.Run("unreadable file stays", func(t *testing.T) {
		d, fd, out, errw := rtDeps(t, "sess-A")
		fd.getErr = dialRefused()
		atDeadline(&d)
		if err := os.WriteFile(d.path(), []byte("{nope"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runRuntimeStatus(d, true)
		if exitCode(err) != 2 {
			t.Fatalf("exit = %d (%v)", exitCode(err), err)
		}
		if b, _ := os.ReadFile(d.path()); string(b) != "{nope" {
			t.Fatalf("file changed: %q", b)
		}
		if strings.Contains(errw.String(), "expired at") || !strings.Contains(out.String(), "unreadable") {
			t.Fatalf("out=%q err=%q", out, errw)
		}
	})
}

func TestRuntimeClaimDirectLiftsExpiredOther(t *testing.T) {
	d, fd, out, errw := rtDeps(t, "sess-A")
	if err := runRuntimeClaim(d, "first", 0, false); err != nil {
		t.Fatal(err)
	}
	first, _, _ := runtimeclaim.Load(d.dir)
	d.session = "sess-B"
	atDeadline(&d)
	fd.claimErr = dialRefused()
	out.Reset()
	errw.Reset()
	if err := runRuntimeClaim(d, "second", 0, false); err != nil {
		t.Fatalf("claim: %v", err)
	}
	e := errw.String()
	i, j := strings.Index(e, "expired at"), strings.Index(e, "written to")
	if i < 0 || j < 0 || i > j || !strings.Contains(e, first.ID) {
		t.Fatalf("stderr = %q", e)
	}
	if st, ok, _ := runtimeclaim.Load(d.dir); !ok || st.Session != "sess-B" {
		t.Fatalf("lease = %+v %v", st, ok)
	}
}

func TestRuntimeClaimPrintsDeadline(t *testing.T) {
	d, _, out, _ := rtDeps(t, "sess-A")
	if err := runRuntimeClaim(d, "acceptance", 0, false); err != nil {
		t.Fatal(err)
	}
	if line := strings.TrimSpace(out.String()); !strings.HasSuffix(line, " — expires 19:04") {
		t.Fatalf("stdout = %q", line)
	}
	d2, fd2, out2, _ := rtDeps(t, "sess-A")
	fd2.noExpiry = true
	if err := runRuntimeClaim(d2, "acceptance", 0, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2.String(), "expires") {
		t.Fatalf("older daemon view printed a deadline: %q", out2)
	}
}

func TestRuntimeReleaseDirectOfExpired(t *testing.T) {
	d, fd, out, errw := rtDeps(t, "sess-A")
	if err := runRuntimeClaim(d, "acceptance", 0, false); err != nil {
		t.Fatal(err)
	}
	first, _, _ := runtimeclaim.Load(d.dir)
	atDeadline(&d)
	fd.relErr = dialRefused()
	out.Reset()
	errw.Reset()
	if err := runRuntimeRelease(d, false); err != nil {
		t.Fatalf("release: %v", err)
	}
	if !strings.Contains(out.String(), "no runtime claim") || strings.Contains(out.String(), "nothing written") {
		t.Fatalf("stdout = %q", out)
	}
	e := errw.String()
	if !strings.Contains(e, "expired at") || !strings.Contains(e, first.ID) || !strings.Contains(e, "lease expired and lifted, nothing else to release") ||
		strings.Contains(e, "nothing written") || strings.Contains(e, "no lease was held") {
		t.Fatalf("stderr = %q", e)
	}
}
