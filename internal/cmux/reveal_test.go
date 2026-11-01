package cmux

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// treeFixture: the workspace lives in the second window.
const revealTree = `{"windows":[
 {"id":"6A73C8EF-1392-4C1C-9EFD-0C91EAD493E5","workspaces":[{"id":"64F9131B-F905-4859-A0DC-8103E1FC3154"}]},
 {"id":"11111111-2222-3333-4444-555555555555","workspaces":[{"id":"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"}]}]}`

type revealRec struct {
	calls  [][]string
	opened []string
	ctxDL  []bool
}

func (rr *revealRec) revealer(tree string, runErr error) Revealer {
	return Revealer{
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			_, has := ctx.Deadline()
			rr.ctxDL = append(rr.ctxDL, has)
			rr.calls = append(rr.calls, args)
			if args[0] == "tree" {
				return []byte(tree), runErr
			}
			return nil, nil
		},
		Open: func(ctx context.Context, b string) error {
			_, has := ctx.Deadline()
			rr.ctxDL = append(rr.ctxDL, has)
			rr.opened = append(rr.opened, b)
			return nil
		},
	}
}

// TestRevealArgs: one listing, then select-workspace with --workspace and the
// --window holding it, both UUIDs, then `open -b com.cmuxterm.app`; every
// call under the one bounded deadline.
func TestRevealArgs(t *testing.T) {
	var rr revealRec
	if err := rr.revealer(revealTree, nil).Reveal("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"tree", "--all", "--json", "--id-format", "both"},
		{"select-workspace", "--workspace", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "--window", "11111111-2222-3333-4444-555555555555"},
	}
	if !reflect.DeepEqual(rr.calls, want) {
		t.Fatalf("calls = %q\nwant    %q", rr.calls, want)
	}
	if !reflect.DeepEqual(rr.opened, []string{"com.cmuxterm.app"}) {
		t.Fatalf("opened = %q, want com.cmuxterm.app once", rr.opened)
	}
	for i, has := range rr.ctxDL {
		if !has {
			t.Fatalf("call %d ran with no deadline", i)
		}
	}
	if revealTimeout != 3*time.Second {
		t.Fatalf("revealTimeout = %v, want 3s", revealTimeout)
	}
}

func TestRevealRefusesBeforeAnyExec(t *testing.T) {
	for _, id := range []string{"", "--help", "workspace:3", "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE --x"} {
		var rr revealRec
		err := rr.revealer(revealTree, nil).Reveal(id)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("Reveal(%q) = %v, want ErrInvalidArg", id, err)
		}
		if len(rr.calls)+len(rr.opened) != 0 {
			t.Errorf("Reveal(%q) ran %q / opened %q", id, rr.calls, rr.opened)
		}
	}
}

func TestRevealWorkspaceNotOpen(t *testing.T) {
	var rr revealRec
	err := rr.revealer(revealTree, nil).Reveal("99999999-2222-3333-4444-555555555555")
	if !errors.Is(err, ErrRevealNotOpen) {
		t.Fatalf("err = %v, want ErrRevealNotOpen", err)
	}
	if len(rr.calls) != 1 || len(rr.opened) != 0 {
		t.Fatalf("after a missing workspace: calls %q, opened %q; want the listing alone", rr.calls, rr.opened)
	}
}

func TestRevealListingFailureStops(t *testing.T) {
	var rr revealRec
	boom := errors.New("boom")
	if err := rr.revealer("", boom).Reveal("AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if len(rr.calls) != 1 || len(rr.opened) != 0 {
		t.Fatalf("calls %q, opened %q; want the listing alone", rr.calls, rr.opened)
	}
	// a window id cmux would misread is refused before the select
	rr = revealRec{}
	bad := `{"windows":[{"id":"--help","workspaces":[{"id":"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"}]}]}`
	if err := rr.revealer(bad, nil).Reveal("AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("err = %v, want ErrInvalidArg", err)
	}
	if len(rr.calls) != 1 {
		t.Fatalf("calls %q; want the listing alone", rr.calls)
	}
}

// TestRevealEnvHasNoCmuxButAimedSocket: the default runners take
// envWithoutCmux — no CMUX_* but CMUX_SOCKET_PATH of -cmux-socket.
func TestRevealEnvHasNoCmuxButAimedSocket(t *testing.T) {
	src, err := os.ReadFile("reveal.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "cmd.Env = envWithoutCmux()"); n != 2 {
		t.Fatalf("reveal.go sets envWithoutCmux on %d commands, want 2 (cmux and open)", n)
	}
	t.Setenv("CMUX_SURFACE_ID", "X")
	SetSocketPath("/tmp/aimed.sock")
	defer SetSocketPath("")
	var cm []string
	for _, kv := range envWithoutCmux() {
		if strings.HasPrefix(kv, "CMUX_") {
			cm = append(cm, kv)
		}
	}
	if !reflect.DeepEqual(cm, []string{"CMUX_SOCKET_PATH=/tmp/aimed.sock"}) {
		t.Fatalf("CMUX_* in env = %q", cm)
	}
}
