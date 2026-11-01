package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/testutil"
)

// `pigtail session prio-check --session S [--workspace W] --json` (maintenance
// window, contract C2): is W's tab pinned in cmux (the prio since #244)? Read
// by the CLI alone, no daemon route.

func prioCheckRun(t *testing.T, f *fakeCmux, getenv func(string) string, session, workspace string, asJSON bool) (string, string, int) {
	t.Helper()
	testutil.SerialOnly(t) // swaps prioCheckLister
	saved := prioCheckLister
	prioCheckLister = func(func(string) string) pinLister { return cmux.SidebarCLI{Run: f.run} }
	t.Cleanup(func() { prioCheckLister = saved })
	var out, errOut bytes.Buffer
	err := runSessionPrioCheck(&out, &errOut, getenv, session, workspace, asJSON)
	return out.String(), errOut.String(), exitCode(err)
}

const (
	checkYes = `{"prio":true,"available":true}` + "\n"
	checkNo  = `{"prio":false,"available":true}` + "\n"
	checkOff = `{"prio":false,"available":false}` + "\n"
)

func TestPrioCheckPinned(t *testing.T) {
	f := newFakeCmux()
	f.pinned[prioWS] = true
	if out, errOut, code := prioCheckRun(t, f, noEnv, "s1", prioWS, true); out != checkYes || errOut != "" || code != 0 {
		t.Fatalf("= %q %q %d", out, errOut, code)
	}
	if out, _, _ := prioCheckRun(t, f, noEnv, "s1", prioWS, false); out != "prio-check: yes\n" {
		t.Fatalf("plain = %q", out)
	}
}

func TestPrioCheckNotPinned(t *testing.T) {
	f := newFakeCmux()
	if out, _, code := prioCheckRun(t, f, noEnv, "s1", prioWS, true); out != checkNo || code != 0 {
		t.Fatalf("unpinned = %q %d", out, code)
	}
	// A tab absent from every window is no pin, not an outage.
	if out, _, code := prioCheckRun(t, f, noEnv, "s1", "00000000-0000-4000-8000-000000000000", true); out != checkNo || code != 0 {
		t.Fatalf("absent = %q %d", out, code)
	}
}

func TestPrioCheckWorkspaceCaseInsensitive(t *testing.T) {
	f := newFakeCmux()
	f.pinned[prioWS] = true
	if out, _, _ := prioCheckRun(t, f, noEnv, "s1", "6b1c2d3e-4f50-4a6b-8c7d-9e0f1a2b3c4d", true); out != checkYes {
		t.Fatalf("lower case = %q", out)
	}
}

func TestPrioCheckEmptyWorkspace(t *testing.T) {
	f := newFakeCmux()
	f.pinned[prioWS] = true
	if out, _, code := prioCheckRun(t, f, noEnv, "s1", "  ", true); out != checkNo || code != 0 {
		t.Fatalf("= %q %d", out, code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("an empty workspace asked cmux: %q", f.calls)
	}
}

func TestPrioCheckUnavailableWhenCmuxFails(t *testing.T) {
	f := newFakeCmux()
	f.windows = nil
	run := func(...string) ([]byte, error) { return nil, errors.New("cmux: socket timeout") }
	saved := prioCheckLister
	prioCheckLister = func(func(string) string) pinLister { return cmux.SidebarCLI{Run: run} }
	t.Cleanup(func() { prioCheckLister = saved })
	var out, errOut bytes.Buffer
	err := runSessionPrioCheck(&out, &errOut, noEnv, "s1", prioWS, true)
	if out.String() != checkOff || exitCode(err) != 0 {
		t.Fatalf("= %q %v: want available:false, exit 0", out.String(), err)
	}
}

func TestPrioCheckOnlyBooleans(t *testing.T) {
	f := newFakeCmux()
	f.pinned[prioWS] = true
	out, _, _ := prioCheckRun(t, f, noEnv, "secret-session", prioWS, true)
	var m map[string]any
	if err := jsonUnmarshal(out, &m); err != nil || len(m) != 2 {
		t.Fatalf("body %q: want exactly prio and available", out)
	}
	for k, v := range m {
		if _, ok := v.(bool); !ok || (k != "prio" && k != "available") {
			t.Fatalf("%s=%v", k, v)
		}
	}
}

func TestPrioCheckNoCmuxEnvNeeded(t *testing.T) {
	f := newFakeCmux()
	f.pinned[prioWS] = true
	// A detached runner: no CMUX_SOCKET_PATH, no CMUX_WORKSPACE_ID.
	if out, _, code := prioCheckRun(t, f, func(string) string { return "" }, "s1", prioWS, true); out != checkYes || code != 0 {
		t.Fatalf("= %q %d", out, code)
	}
}

func TestPrioCheckBadUsage(t *testing.T) {
	f := newFakeCmux()
	if _, errOut, code := prioCheckRun(t, f, noEnv, "", "", true); code != 2 || errOut == "" {
		t.Fatalf("neither flag = exit %d %q, want 2", code, errOut)
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
