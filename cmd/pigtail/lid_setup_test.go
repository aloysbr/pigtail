package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type probeStep struct {
	resp pigtail.LidProbeResponse
	err  error
}

type rootCall struct {
	script string
	tty    bool
}

// setupStub replaces every seam of lid_setup.go: no test ever runs sudo,
// osascript, pmset or reaches a daemon.
type setupStub struct {
	probes    []probeStep
	probeN    int
	roots     []rootCall
	rootErrs  []error // per root call; missing = nil
	tty       bool
	user      string
	userErr   error
	userCalls int
	pm        string
	pmErr     error
	offErr    error
	offCalls  int
}

func stubSetup(t *testing.T) *setupStub {
	t.Helper()
	testutil.SerialOnly(t) // swaps lidRootExec
	if runtime.GOOS != "darwin" {
		t.Skip("pigtail lid setup is macOS only")
	}
	s := &setupStub{user: "ada", pm: " SleepDisabled\t\t0\n"}
	oldRoot, oldTTY, oldProbe, oldUser, oldPM, oldOff := lidRootExec, lidStdinTTY, lidDaemonProbe, lidUser, lidReadPM, lidDaemonOff
	t.Cleanup(func() {
		lidRootExec, lidStdinTTY, lidDaemonProbe, lidUser, lidReadPM, lidDaemonOff = oldRoot, oldTTY, oldProbe, oldUser, oldPM, oldOff
	})
	lidRootExec = func(script string, tty bool) error {
		s.roots = append(s.roots, rootCall{script, tty})
		if i := len(s.roots) - 1; i < len(s.rootErrs) {
			return s.rootErrs[i]
		}
		return nil
	}
	lidStdinTTY = func() bool { return s.tty }
	lidDaemonProbe = func() (pigtail.LidProbeResponse, error) {
		if s.probeN >= len(s.probes) {
			t.Fatalf("unexpected probe call #%d", s.probeN+1)
		}
		p := s.probes[s.probeN]
		s.probeN++
		return p.resp, p.err
	}
	lidUser = func() (string, error) { s.userCalls++; return s.user, s.userErr }
	lidReadPM = func() (string, error) { return s.pm, s.pmErr }
	lidDaemonOff = func() error { s.offCalls++; return s.offErr }
	return s
}

var (
	ruleOK    = probeStep{resp: pigtail.LidProbeResponse{RuleOK: true}}
	ruleNotOK = probeStep{resp: pigtail.LidProbeResponse{RuleOK: false}}
	ruleBoom  = probeStep{err: &pigtail.APIError{Code: pigtail.ErrInternal, Message: "pmset -g: boom"}}
)

func mustContain(t *testing.T, what, got string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(got, sub) {
			t.Errorf("%s %q does not contain %q", what, got, sub)
		}
	}
}

func TestSetupAlreadyDone(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{ruleOK}
	var w bytes.Buffer
	if err := runLidSetup(&w, false); err != nil {
		t.Fatal(err)
	}
	if len(s.roots) != 0 {
		t.Fatalf("root calls: %v", s.roots)
	}
	mustContain(t, "output", w.String(), "already set up")
}

func testSetupInstalls(t *testing.T, tty bool) {
	s := stubSetup(t)
	s.tty = tty
	s.probes = []probeStep{ruleNotOK, ruleOK}
	var w bytes.Buffer
	if err := runLidSetup(&w, false); err != nil {
		t.Fatal(err)
	}
	want, _ := lid.InstallScript("ada")
	if len(s.roots) != 1 || s.roots[0].script != want || s.roots[0].tty != tty {
		t.Fatalf("root calls: %+v", s.roots)
	}
	sc := s.roots[0].script
	if !strings.HasPrefix(sc, "/bin/mkdir -p -m 0755 /etc/sudoers.d") {
		t.Errorf("script = %s", sc)
	}
	mustContain(t, "script", sc,
		"'# Managed by pigtail lid setup. Remove with: pigtail lid setup --remove'",
		"'ada ALL=(root) NOPASSWD: /usr/bin/pmset -a disablesleep 0, /usr/bin/pmset -a disablesleep 1'")
	for _, ref := range []string{"/var/", "/tmp", "/Users"} {
		if strings.Contains(sc, ref) {
			t.Errorf("script references %q", ref)
		}
	}
	mustContain(t, "output", w.String(), "sleep control set up")
	if s.probeN != 2 {
		t.Fatalf("probe calls = %d, want 2", s.probeN)
	}
}

func TestSetupInstallsViaDialogWithoutTTY(t *testing.T) { testSetupInstalls(t, false) }

func TestSetupUsesSudoInTTY(t *testing.T) { testSetupInstalls(t, true) }

func TestSetupRuleStillMissingRemovesIt(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{ruleNotOK, ruleNotOK}
	err := runLidSetup(&bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("setup succeeded with an inoperative rule")
	}
	if len(s.roots) != 2 || s.roots[1].script != lid.RemoveScript() {
		t.Fatalf("root calls: %+v", s.roots)
	}
	mustContain(t, "error", err.Error(), "#includedir", "/etc/sudoers", "removed")

	s = stubSetup(t)
	s.probes = []probeStep{ruleNotOK, ruleNotOK}
	s.rootErrs = []error{nil, errors.New("cancelled")}
	err = runLidSetup(&bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("no error")
	}
	mustContain(t, "error", err.Error(), "removing it failed", "sudo /bin/rm -f /etc/sudoers.d/pigtail-lid")
}

func TestSetupRootFails(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{ruleNotOK}
	s.rootErrs = []error{errors.New("User canceled. (-128)")}
	var w bytes.Buffer
	err := runLidSetup(&w, false)
	if err == nil || !strings.Contains(err.Error(), "setup failed") {
		t.Fatalf("err = %v", err)
	}
	if len(s.roots) != 1 || strings.Contains(w.String(), "set up:") {
		t.Fatalf("roots %+v, output %q", s.roots, w.String())
	}
}

func TestSetupRefusesReservedUser(t *testing.T) {
	s := stubSetup(t)
	s.user = "ALL"
	s.probes = []probeStep{ruleNotOK}
	if err := runLidSetup(&bytes.Buffer{}, false); err == nil {
		t.Fatal("setup accepted user ALL")
	}
	if len(s.roots) != 0 {
		t.Fatalf("root calls: %+v", s.roots)
	}
}

func TestRemoveRefusesWhileOnAndOffFails(t *testing.T) {
	s := stubSetup(t)
	s.pm = " SleepDisabled\t\t1\n"
	s.offErr = errors.New("sleep control is not set up")
	err := runLidSetup(&bytes.Buffer{}, true)
	if err == nil || !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("err = %v", err)
	}
	if len(s.roots) != 0 {
		t.Fatalf("root calls: %+v", s.roots)
	}
}

func TestRemoveRefusesWhenStateUnreadable(t *testing.T) {
	s := stubSetup(t)
	s.pmErr = errors.New("boom")
	err := runLidSetup(&bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("no error")
	}
	mustContain(t, "error", err.Error(), "refusing to remove", "boom")
	if len(s.roots) != 0 || s.offCalls != 0 {
		t.Fatalf("roots %+v, off calls %d", s.roots, s.offCalls)
	}
}

func TestRemoveTurnsOffThenRemoves(t *testing.T) {
	s := stubSetup(t)
	s.pm = " SleepDisabled\t\t1\n"
	s.probes = []probeStep{ruleNotOK}
	var w bytes.Buffer
	if err := runLidSetup(&w, true); err != nil {
		t.Fatal(err)
	}
	if s.offCalls != 1 || len(s.roots) != 1 || s.roots[0].script != lid.RemoveScript() {
		t.Fatalf("off calls %d, roots %+v", s.offCalls, s.roots)
	}
	mustContain(t, "output", w.String(), "sleep control rule removed")
}

func TestSetupCheckUnreadableBefore(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{ruleBoom}
	err := runLidSetup(&bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("no error")
	}
	mustContain(t, "error", err.Error(), "cannot check the sudo rule", "boom")
	if len(s.roots) != 0 || s.userCalls != 0 {
		t.Fatalf("roots %+v, user calls %d", s.roots, s.userCalls)
	}
}

func TestSetupDaemonUnreachable(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{{err: errors.New("dial unix /x/pigtail.sock: connect: no such file or directory")}}
	err := runLidSetup(&bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("no error")
	}
	mustContain(t, "error", err.Error(), "pigtaild is not reachable", "start pigtaild", "no such file")
	if len(s.roots) != 0 || s.probeN != 1 {
		t.Fatalf("roots %+v, probes %d", s.roots, s.probeN)
	}
}

func TestSetupDaemonTimeout(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{{err: fmt.Errorf("probe: %w", context.DeadlineExceeded)}}
	err := runLidSetup(&bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("no error")
	}
	mustContain(t, "error", err.Error(), "did not answer within 90 s", "pigtail lid status")
	if strings.Contains(err.Error(), "not reachable") || len(s.roots) != 0 {
		t.Fatalf("err %v, roots %+v", err, s.roots)
	}
}

func TestSetupDaemonOutdated(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{{err: &pigtail.HTTPStatusError{Status: 404, Body: "404 page not found\n"}}}
	err := runLidSetup(&bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("no error")
	}
	mustContain(t, "error", err.Error(), "answered HTTP 404", "restart it")
	if strings.Contains(err.Error(), "not reachable") || len(s.roots) != 0 {
		t.Fatalf("err %v, roots %+v", err, s.roots)
	}
}

func TestSetupCheckUnreadableAfter(t *testing.T) {
	for _, after := range []probeStep{ruleBoom, {err: errors.New("dial unix /x/pigtail.sock: connect: connection refused")}} {
		s := stubSetup(t)
		s.probes = []probeStep{ruleNotOK, after}
		var w bytes.Buffer
		err := runLidSetup(&w, false)
		if err == nil {
			t.Fatal("no error")
		}
		mustContain(t, "error", err.Error(), "rule installed but could not be verified", "rerun pigtail lid setup")
		if after.err != nil && strings.Contains(after.err.Error(), "refused") {
			mustContain(t, "error", err.Error(), "pigtaild is not reachable")
		} else {
			mustContain(t, "error", err.Error(), "boom")
		}
		if len(s.roots) != 1 || strings.Contains(w.String(), "sleep control set up") {
			t.Fatalf("roots %+v, output %q", s.roots, w.String())
		}
	}
}

func TestSetupPrintsNoTimerWarning(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{{resp: pigtail.LidProbeResponse{RuleOK: true, Warning: "sleep is disabled with no timer — run pigtail lid off"}}}
	var w bytes.Buffer
	if err := runLidSetup(&w, false); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "output", w.String(), "warning: sleep is disabled with no timer — run pigtail lid off", "already set up")
}

func TestRemoveRuleStillActive(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{ruleOK}
	var w bytes.Buffer
	err := runLidSetup(&w, true)
	if err == nil || !strings.Contains(err.Error(), "rule still active after removal") {
		t.Fatalf("err = %v", err)
	}
	if len(s.roots) != 1 || s.roots[0].script != lid.RemoveScript() || strings.Contains(w.String(), "sleep control rule removed") {
		t.Fatalf("roots %+v, output %q", s.roots, w.String())
	}
}

func TestRemoveProbeErrorAfterRemoval(t *testing.T) {
	s := stubSetup(t)
	s.probes = []probeStep{ruleBoom}
	err := runLidSetup(&bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("no error")
	}
	mustContain(t, "error", err.Error(), "rule removed but the removal could not be verified", "boom")
	if len(s.roots) != 1 {
		t.Fatalf("roots %+v", s.roots)
	}
}
