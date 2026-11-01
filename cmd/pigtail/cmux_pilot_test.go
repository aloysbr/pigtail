package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/testutil"
)

func runPilot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"cmux", "pilot"}, args...))
	err := root.Execute()
	return out.String(), err
}

// stubPilot replaces the three seams: the running cmux's pid, the current
// photo's bindings (surface → session, account), the scope check.
func stubPilot(t *testing.T, pid int, bound map[string][2]string, scopeOK bool) {
	t.Helper()
	testutil.SerialOnly(t) // swaps pilotSentinel
	oldS, oldB, oldOK := pilotSentinel, pilotBinding, pilotScopeOK
	t.Cleanup(func() { pilotSentinel, pilotBinding, pilotScopeOK = oldS, oldB, oldOK })
	pilotSentinel = func() (int, error) { return pid, nil }
	pilotBinding = func(surface string) (string, string, error) {
		for k, v := range bound {
			if strings.EqualFold(k, surface) {
				return v[0], v[1], nil
			}
		}
		return "", "", errors.New("no bound session on that surface in cmux's current photo")
	}
	pilotScopeOK = func(string) (bool, error) { return scopeOK, nil }
}

// D4: default = the caller's own surface and session; --surface names
// another pane, its session read from the current photo's binding (revue 1
// #12); refused outside cmux, with no surface, or on a bad UUID.
func TestCmuxPilotSetsMarker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CMUX_SURFACE_ID", "11111111-2222-3333-4444-555555555555")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-pilot")
	stubPilot(t, 4242, map[string][2]string{"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE": {"sid-other", "/cfg/x"}}, true)
	out, err := runPilot(t)
	if err != nil {
		t.Fatal(err)
	}
	m, err := cmuxcheck.LoadPilot(cmuxcheck.StateDir())
	if err != nil || m.SurfaceID != "11111111-2222-3333-4444-555555555555" || m.SessionID != "sid-pilot" || m.CmuxPID != 4242 {
		t.Fatalf("marker %+v, %v", m, err)
	}
	if !strings.Contains(out, "valid for the next cmux launch") {
		t.Fatalf("output %q", out)
	}
	if out, err := runPilot(t, "--show"); err != nil || !strings.Contains(out, "11111111-2222-3333-4444-555555555555") || !strings.Contains(out, "not used yet") {
		t.Fatalf("--show: %q, %v", out, err)
	}
	if _, err := runPilot(t, "--surface", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"); err != nil {
		t.Fatal(err)
	}
	if m, _ := cmuxcheck.LoadPilot(cmuxcheck.StateDir()); m.SessionID != "sid-other" || m.SurfaceID != "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE" {
		t.Fatalf("--surface: marker %+v, want upper case and the binding's session", m)
	}
	if _, err := runPilot(t, "--surface", "99999999-bbbb-cccc-dddd-eeeeeeeeeeee"); err == nil {
		t.Fatal("unbound surface: want an error")
	}
	t.Setenv("CMUX_SURFACE_ID", "")
	if _, err := runPilot(t); err == nil {
		t.Fatal("no surface: want an error")
	}
	if _, err := runPilot(t, "--surface", "not-a-uuid"); err == nil {
		t.Fatal("bad UUID: want an error")
	}
	if _, err := runPilot(t, "--clear"); err != nil {
		t.Fatal(err)
	}
	if out, err := runPilot(t, "--show"); err != nil || !strings.Contains(out, "no pilot set") {
		t.Fatalf("--show after --clear: %q, %v", out, err)
	}
}

// Revue 1 #12 (CLAUDE.md § Isolation criterion): a cell may not point
// pigtaild's typing at a pane bound to another scope — refused, nothing
// written, the other scope never named.
func TestCmuxPilotSurfaceOtherScopeRefused(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubPilot(t, 4242, map[string][2]string{"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE": {"sid-other", "/cfg/x"}}, false)
	_, err := runPilot(t, "--surface", "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE")
	if err == nil || !strings.Contains(err.Error(), "no session of this scope") {
		t.Fatalf("err %v, want a refusal", err)
	}
	if _, err := cmuxcheck.LoadPilot(cmuxcheck.StateDir()); err == nil {
		t.Fatal("a marker was written")
	}
}

// Revue PR B M6 (CLAUDE.md § Subject scope, isolation criterion): outside
// the master, the default path and --clear never overwrite nor clear a
// pending marker whose surface is bound to another scope's session — the
// same rule as --surface; the other scope is never named. A marker already
// consumed by a launch is anyone's to replace.
func TestCmuxPilotKeepsAnotherScopesMarker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CMUX_SURFACE_ID", "11111111-2222-3333-4444-555555555555")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-mine")
	stubPilot(t, 4242, map[string][2]string{
		"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE": {"sid-master", "/cfg/master"},
		"11111111-2222-3333-4444-555555555555": {"sid-mine", "/cfg/mine"},
	}, true)
	pilotScopeOK = func(cfg string) (bool, error) { return cfg == "/cfg/mine", nil }
	theirs := cmuxcheck.PilotMarker{SurfaceID: "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", SessionID: "sid-master", CmuxPID: 4242, At: time.Now()}
	if err := cmuxcheck.SavePilot(cmuxcheck.StateDir(), theirs); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--clear"}} {
		_, err := runPilot(t, args...)
		if err == nil || !strings.Contains(err.Error(), "another scope") || strings.Contains(err.Error(), "AAAAAAAA") {
			t.Fatalf("%v: err %v, want a refusal naming nothing", args, err)
		}
		if m, err := cmuxcheck.LoadPilot(cmuxcheck.StateDir()); err != nil || m.SurfaceID != theirs.SurfaceID {
			t.Fatalf("%v: marker %+v, %v — want it kept", args, m, err)
		}
	}
	theirs.UsedBy = 9
	if err := cmuxcheck.SavePilot(cmuxcheck.StateDir(), theirs); err != nil {
		t.Fatal(err)
	}
	if _, err := runPilot(t); err != nil {
		t.Fatalf("consumed marker: %v", err)
	}
	if m, _ := cmuxcheck.LoadPilot(cmuxcheck.StateDir()); m.SessionID != "sid-mine" {
		t.Fatalf("marker %+v, want the caller's", m)
	}
}
