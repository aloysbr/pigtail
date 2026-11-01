package sessionlaunch

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	wsA = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	wsB = "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
	wsC = "CCCCCCCC-CCCC-4CCC-8CCC-CCCCCCCCCCCC"
)

// TestLiveWorkspacesReportsUnreadableEnv: live sessions are mapped by their
// upper-cased CMUX_WORKSPACE_ID; a pid whose env cannot be read is listed,
// never dropped; one outside cmux is ignored.
func TestLiveWorkspacesReportsUnreadableEnv(t *testing.T) {
	procs := func() ([]claudesession.Proc, error) {
		return []claudesession.Proc{{PID: 1}, {PID: 2}, {PID: 3}, {PID: 4}}, nil
	}
	environ := func(pid int) (string, error) {
		switch pid {
		case 1, 4:
			return "CMUX_WORKSPACE_ID=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa PWD=/x", nil
		case 2:
			return "", errors.New("ps: operation not permitted")
		}
		return "PWD=/y", nil
	}
	live, unreadable, err := LiveWorkspaces(procs, environ)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]int{wsA: {1, 4}}; !reflect.DeepEqual(live, want) {
		t.Fatalf("live = %v, want %v", live, want)
	}
	if !reflect.DeepEqual(unreadable, []int{2}) {
		t.Fatalf("unreadable = %v, want [2]", unreadable)
	}
	if _, _, err := LiveWorkspaces(func() ([]claudesession.Proc, error) { return nil, errors.New("ps failed") }, environ); err == nil {
		t.Fatal("a failed process listing must be an error")
	}
}

// TestLiveConflictIgnoresMarginAndExcluded: a live session on a bound tab or
// on a tab a spawn record names conflicts; a margin record and the excluded
// tab (the caller's own) do not.
func TestLiveConflictIgnoresMarginAndExcluded(t *testing.T) {
	const sub = "sub_x"
	entries := []spawnreg.Entry{
		{SubjectID: sub, Kind: spawnreg.KindMargin, WorkspaceID: wsB},
		{SubjectID: "sub_other", WorkspaceID: wsC},
	}
	live := map[string][]int{wsA: {7}, wsB: {8}, wsC: {9}}
	bindings := []pigtail.SubjectCmuxBinding{{SubjectID: sub, CmuxWorkspaceID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}

	if ws, pids, found := LiveConflict(entries, live, bindings, sub, ""); !found || ws != wsA || !reflect.DeepEqual(pids, []int{7}) {
		t.Fatalf("conflict = %q %v %v, want %s [7] true", ws, pids, found, wsA)
	}
	if ws, _, found := LiveConflict(entries, live, bindings, sub, wsA); found {
		t.Fatalf("the excluded tab %s conflicts", ws)
	}
	spawned := append(entries, spawnreg.Entry{SubjectID: sub, WorkspaceID: wsC})
	if ws, _, found := LiveConflict(spawned, live, nil, sub, ""); !found || ws != wsC {
		t.Fatalf("a spawn record's live tab: %q %v, want %s", ws, found, wsC)
	}
	if ws, _, found := LiveConflict(entries, map[string][]int{wsA: nil}, bindings, sub, ""); found {
		t.Fatalf("a tab without a live pid conflicts: %s", ws)
	}
}
