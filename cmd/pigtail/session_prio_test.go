package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/testutil"
)

// `pigtail session prio [--clear] [--json]` (D3, D18, D19, D25, D28, D31): the
// prio is cmux's own pin on the session's tab (CMUX_WORKSPACE_ID), read in
// `workspace list` before any write.

const (
	prioWS  = "6B1C2D3E-4F50-4A6B-8C7D-9E0F1A2B3C4D"
	prioW1  = "11111111-2222-4333-8444-555555555555"
	prioW2  = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	otherWS = "0F0E0D0C-0B0A-4909-8807-060504030201"
)

// fakeCmux answers the argv pigtail sends: list-windows, workspace list per
// window, workspace-action pin|unpin (recorded, applied). failOn makes one
// action ("pin" or "unpin") fail; onAction runs before an action applies.
type fakeCmux struct {
	windows  []string
	members  map[string][]string // window → workspaces
	pinned   map[string]bool
	calls    []string
	failOn   string
	onAction func(action string)
}

func newFakeCmux() *fakeCmux {
	return &fakeCmux{
		windows: []string{prioW1, prioW2},
		members: map[string][]string{prioW1: {otherWS}, prioW2: {prioWS}},
		pinned:  map[string]bool{},
	}
}

func (f *fakeCmux) run(args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case call == "list-windows --json":
		var ws []map[string]any
		for i, w := range f.windows {
			ws = append(ws, map[string]any{"id": w, "index": i})
		}
		return json.Marshal(ws)
	case len(args) == 5 && args[0] == "workspace" && args[1] == "list":
		var rows []map[string]any
		for i, id := range f.members[args[3]] {
			rows = append(rows, map[string]any{"id": id, "index": i, "pinned": f.pinned[id]})
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		return json.Marshal(map[string]any{"workspaces": rows})
	case len(args) == 5 && args[0] == "workspace-action":
		action, ws := args[2], args[4]
		if f.onAction != nil {
			f.onAction(action)
		}
		if action == f.failOn {
			return nil, errors.New("cmux workspace-action: exit status 1: Error: injected")
		}
		f.pinned[ws] = action == "pin"
		return []byte("OK"), nil
	}
	return nil, fmt.Errorf("fake cmux: unexpected %q", call)
}

func (f *fakeCmux) actions() []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "workspace-action") {
			out = append(out, c)
		}
	}
	return out
}

// ops are the real pin operations over the fake cmux's argv.
func (f *fakeCmux) ops() cmuxPinOps {
	return cmuxPinOpsWith(cmux.SidebarCLI{Run: f.run}, cmux.FoldersCLI{Run: f.run})
}

// useFakeCmux aims every pin operation of the package (prio and standby) at f.
func useFakeCmux(t *testing.T, f *fakeCmux) {
	t.Helper()
	testutil.SerialOnly(t) // swaps cmuxPinFor
	saved := cmuxPinFor
	cmuxPinFor = func(func(string) string) cmuxPinOps { return f.ops() }
	t.Cleanup(func() { cmuxPinFor = saved })
}

// seams records what sessionPrio asks of cmux and of the daemon.
type seams struct {
	pinned      bool
	pins        []string
	cleared     []string
	clearErr    error
	isPinnedErr error
}

func (s *seams) deps() prioDeps {
	return prioDeps{
		cmuxPinOps: cmuxPinOps{
			isPinned: func(string) (bool, error) { return s.pinned, s.isPinnedErr },
			pin: func(ws string, on bool) error {
				s.pins = append(s.pins, fmt.Sprintf("%s %v", ws, on))
				s.pinned = on
				return nil
			},
		},
		clearStandby: func(_ context.Context, session string) error {
			s.cleared = append(s.cleared, session)
			return s.clearErr
		},
	}
}

func prioWith(env map[string]string, d prioDeps, clear, asJSON bool) (string, string, int) {
	var out, errOut bytes.Buffer
	err := runSessionPrioWith(context.Background(), &out, &errOut, envOf(env), clear, asJSON, d)
	return out.String(), errOut.String(), exitCode(err)
}

var inTab = map[string]string{"CLAUDE_CODE_SESSION_ID": "s-p", cmux.EnvWorkspaceID: prioWS}

func TestSessionPrioPinsOwnWorkspace(t *testing.T) {
	t.Parallel()
	s := &seams{}
	out, errOut, code := prioWith(inTab, s.deps(), false, false)
	if out != "prio: pinned\n" || errOut != "" || code != 0 {
		t.Fatalf("got %q %q %d", out, errOut, code)
	}
	if !reflect.DeepEqual(s.pins, []string{prioWS + " true"}) {
		t.Fatalf("pins = %q", s.pins)
	}
	if !reflect.DeepEqual(s.cleared, []string{"s-p"}) {
		t.Fatalf("standby not lifted: %q", s.cleared)
	}
}

func TestSessionPrioAlreadyPinnedNoop(t *testing.T) {
	t.Parallel()
	s := &seams{pinned: true}
	out, _, code := prioWith(inTab, s.deps(), false, true)
	if code != 0 || len(s.pins) != 0 {
		t.Fatalf("code %d pins %q", code, s.pins)
	}
	want := `{"session_id":"s-p","workspace_id":"` + prioWS + `","pinned":true,"changed":false}` + "\n"
	if out != want {
		t.Fatalf("json = %q, want %q", out, want)
	}
	if len(s.cleared) != 1 {
		t.Fatalf("standby not lifted on an already pinned tab: %q", s.cleared)
	}
	if out, _, _ := prioWith(inTab, (&seams{pinned: true}).deps(), false, false); out != "prio: pinned\n" {
		t.Fatalf("text = %q", out)
	}
}

func TestWorkspacePinnedSecondWindow(t *testing.T) {
	t.Parallel()
	f := newFakeCmux()
	f.pinned[prioWS] = true
	got, err := workspacePinned(cmux.SidebarCLI{Run: f.run}, prioWS)
	if err != nil || !got {
		t.Fatalf("workspacePinned = %v, %v", got, err)
	}
	want := []string{"list-windows --json", "workspace list --window " + prioW1 + " --json", "workspace list --window " + prioW2 + " --json"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls =\n%q\nwant\n%q", f.calls, want)
	}
}

func TestWorkspacePinnedNotFound(t *testing.T) {
	t.Parallel()
	f := newFakeCmux()
	f.members[prioW2] = nil
	_, err := workspacePinned(cmux.SidebarCLI{Run: f.run}, prioWS)
	if err == nil || err.Error() != "workspace "+prioWS+" not found in cmux" {
		t.Fatalf("err = %v", err)
	}
	for _, clear := range []bool{false, true} {
		d := prioDeps{cmuxPinOps: f.ops(), clearStandby: func(context.Context, string) error { return nil }}
		_, errOut, code := prioWith(inTab, d, clear, false)
		if code == 0 || !strings.Contains(errOut, "workspace "+prioWS+" not found in cmux") {
			t.Fatalf("clear=%v: code %d stderr %q", clear, code, errOut)
		}
	}
	if a := f.actions(); len(a) != 0 {
		t.Fatalf("a write without a found tab: %q", a)
	}
}

func TestSessionPrioClearUnpins(t *testing.T) {
	t.Parallel()
	f := newFakeCmux()
	f.pinned[prioWS] = true
	d := prioDeps{cmuxPinOps: f.ops(), clearStandby: func(context.Context, string) error {
		t.Error("--clear touched the Standby")
		return nil
	}}
	out, _, code := prioWith(inTab, d, true, false)
	if out != "prio: unpinned\n" || code != 0 {
		t.Fatalf("got %q %d", out, code)
	}
	if a := f.actions(); !reflect.DeepEqual(a, []string{"workspace-action --action unpin --workspace " + prioWS}) {
		t.Fatalf("actions = %q", a)
	}
	f.pinned[prioWS] = true
	out, _, _ = prioWith(inTab, d, true, true)
	want := `{"session_id":"s-p","workspace_id":"` + prioWS + `","pinned":false,"changed":true}` + "\n"
	if out != want {
		t.Fatalf("json = %q, want %q", out, want)
	}
}

// Point 5: --clear needs CMUX_WORKSPACE_ID only — no session id, a daemon
// that knows no session: the tab is unpinned, exit 0, the daemon untouched.
func TestSessionPrioClearNeedsOnlyWorkspace(t *testing.T) {
	srv := startStandbyServer(t)
	f := newFakeCmux()
	f.pinned[prioWS] = true
	useFakeCmux(t, f)
	var out, errOut bytes.Buffer
	err := runSessionPrio(context.Background(), &out, &errOut, envOf(map[string]string{cmux.EnvWorkspaceID: prioWS}), true, false)
	if exitCode(err) != 0 || out.String() != "prio: unpinned\n" || errOut.String() != "" {
		t.Fatalf("got %q %q %v", out.String(), errOut.String(), err)
	}
	if a := f.actions(); !reflect.DeepEqual(a, []string{"workspace-action --action unpin --workspace " + prioWS}) {
		t.Fatalf("actions = %q", a)
	}
	if c := srv.calls(); len(c) != 0 {
		t.Fatalf("--clear called the daemon: %q", c)
	}
}

func TestSessionPrioClearNotPinnedIsNoop(t *testing.T) {
	t.Parallel()
	f := newFakeCmux()
	d := prioDeps{cmuxPinOps: f.ops(), clearStandby: func(context.Context, string) error { return nil }}
	out, errOut, code := prioWith(inTab, d, true, true)
	want := `{"session_id":"s-p","workspace_id":"` + prioWS + `","pinned":false,"changed":false}` + "\n"
	if out != want || errOut != "" || code != 0 {
		t.Fatalf("got %q %q %d, want %q", out, errOut, code, want)
	}
	if out, _, code := prioWith(inTab, d, true, false); out != "prio: not pinned\n" || code != 0 {
		t.Fatalf("text = %q %d", out, code)
	}
	if a := f.actions(); len(a) != 0 {
		t.Fatalf("a write on an unpinned tab: %q", a)
	}
}

// D25: the daemon down, the pin is posed anyway: success with a warning.
func TestSessionPrioDaemonDownKeepsPin(t *testing.T) {
	t.Parallel()
	down := fmt.Errorf("dial unix /x/p.sock: connect: %w", syscall.ECONNREFUSED)
	s := &seams{clearErr: down}
	out, errOut, code := prioWith(inTab, s.deps(), false, true)
	want := `{"session_id":"s-p","workspace_id":"` + prioWS + `","pinned":true,"changed":true,"warning":"daemon unreachable: standby not cleared"}` + "\n"
	if out != want || errOut != "" || code != 0 {
		t.Fatalf("got %q %q %d, want %q", out, errOut, code, want)
	}
	if !reflect.DeepEqual(s.pins, []string{prioWS + " true"}) {
		t.Fatalf("pins = %q", s.pins)
	}
	s = &seams{clearErr: down}
	if out, _, code := prioWith(inTab, s.deps(), false, false); out != "prio: pinned\nwarning: daemon unreachable: standby not cleared\n" || code != 0 {
		t.Fatalf("text = %q %d", out, code)
	}
}

func TestSessionPrioOutsideCmuxFails(t *testing.T) {
	t.Parallel()
	for _, clear := range []bool{false, true} {
		s := &seams{}
		out, errOut, code := prioWith(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-p"}, s.deps(), clear, false)
		if out != "" || code == 0 || errOut != "Error: not in a cmux workspace: nothing pinned\n" {
			t.Fatalf("clear=%v: got %q %q %d", clear, out, errOut, code)
		}
		if len(s.pins) != 0 || len(s.cleared) != 0 {
			t.Fatalf("clear=%v: acted outside cmux: %q %q", clear, s.pins, s.cleared)
		}
	}
}

// Copy D22 (t2).
func TestSessionPrioHelpDescribesPin(t *testing.T) {
	t.Parallel()
	cmd := sessionPrioCmd()
	if !strings.Contains(cmd.Long, "Pins this session's cmux tab") || strings.Contains(cmd.Long, "« Prio »") {
		t.Fatalf("Long =\n%s", cmd.Long)
	}
	if cmd.Short != "Pin this session's tab (prio)" {
		t.Fatalf("Short = %q", cmd.Short)
	}
	if u := cmd.Flags().Lookup("clear").Usage; u != "unpin it" {
		t.Fatalf("--clear usage = %q", u)
	}
	if u := cmd.Flags().Lookup("json").Usage; u != "print the result as JSON" || strings.Contains(u, "daemon") {
		t.Fatalf("--json usage = %q", u)
	}
}

func standbyIn(t *testing.T, env map[string]string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runSessionStandby(context.Background(), &out, &errOut, envOf(env), false, false)
	return out.String(), errOut.String(), exitCode(err)
}

// D18, D29: Standby unpins first, then sets the Standby.
func TestSessionStandbyUnpins(t *testing.T) {
	srv := startStandbyServer(t)
	freeBgDeps(t)
	f := newFakeCmux()
	f.pinned[prioWS] = true
	f.onAction = func(string) {
		if n := srv.count("PUT /peers/s-p/standby"); n != 0 {
			t.Errorf("unpin after the Standby (%d PUT)", n)
		}
	}
	useFakeCmux(t, f)
	if _, errOut, code := standbyIn(t, inTab); code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if a := f.actions(); !reflect.DeepEqual(a, []string{"workspace-action --action unpin --workspace " + prioWS}) {
		t.Fatalf("actions = %q", a)
	}
	if srv.count("PUT /peers/s-p/standby") == 0 {
		t.Fatalf("no Standby: %q", srv.calls())
	}
}

func TestSessionStandbyNotPinnedNoUnpin(t *testing.T) {
	srv := startStandbyServer(t)
	freeBgDeps(t)
	f := newFakeCmux()
	useFakeCmux(t, f)
	out, errOut, code := standbyIn(t, inTab)
	if code != 0 || strings.Contains(out+errOut, "unpin failed") {
		t.Fatalf("got %q %q %d", out, errOut, code)
	}
	if a := f.actions(); len(a) != 0 {
		t.Fatalf("an unpin on an unpinned tab: %q", a)
	}
	if srv.count("PUT /peers/s-p/standby") == 0 {
		t.Fatalf("no Standby: %q", srv.calls())
	}
}

// The unpin fails: no Standby written, the reason alone on stderr (Copy D23).
func TestSessionStandbyUnpinFailsNoStandby(t *testing.T) {
	srv := startStandbyServer(t)
	freeBgDeps(t)
	f := newFakeCmux()
	f.pinned[prioWS] = true
	f.failOn = "unpin"
	useFakeCmux(t, f)
	out, errOut, code := standbyIn(t, inTab)
	want := "standby: not set — unpin failed: cmux workspace-action: exit status 1: Error: injected\n"
	if code == 0 || errOut != want || strings.Contains(out, "unpin failed") || out != "" {
		t.Fatalf("got out %q stderr %q code %d, want stderr %q", out, errOut, code, want)
	}
	if n := srv.count("PUT /peers/s-p/standby"); n != 0 {
		t.Fatalf("Standby written after a failed unpin: %q", srv.calls())
	}
}
