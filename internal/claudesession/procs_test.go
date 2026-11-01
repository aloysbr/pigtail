package claudesession

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeProcessesListsEveryClaudeProcess(t *testing.T) {
	saved := psTreeRun
	t.Cleanup(func() { psTreeRun = saved })
	psTreeRun = func() ([]byte, error) {
		return []byte(
			"  100     1 /Users/x/.nvm/versions/node/v22/bin/claude\n" +
				"  200   100 /opt/homebrew/bin/claude --settings /tmp/s --resume bbbbbbbb-0000-0000-0000-000000000002\n" +
				"  500     1 /bin/zsh -l\n" +
				"garbage\n"), nil
	}
	got, err := ClaudeProcesses()
	if err != nil {
		t.Fatal(err)
	}
	want := []Proc{
		{PID: 100, Args: "/Users/x/.nvm/versions/node/v22/bin/claude"},
		{PID: 200, Args: "/opt/homebrew/bin/claude --settings /tmp/s --resume bbbbbbbb-0000-0000-0000-000000000002", Ancestors: []int{100}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudeProcesses = %+v, want %+v", got, want)
	}

	psTreeRun = func() ([]byte, error) { return nil, errors.New("ps boom") }
	if _, err := ClaudeProcesses(); err == nil {
		t.Fatal("a failed listing must be an error, not an empty list")
	}
}

func fixture065(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/ps-cmux-065.txt")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The listing of a NIGHTLY 0.65 tab (read-only ps): each hook child is a
// child of its claude, itself a child of a process outside the listing.
func TestClaudeProcessesCarriesAncestors(t *testing.T) {
	got := ParseClaudeProcs(fixture065(t), -1)
	anc := map[int][]int{}
	for _, p := range got {
		anc[p.PID] = p.Ancestors
	}
	want := map[int][]int{
		61096: {61082},
		61270: {61096, 61082},
		61403: {61096, 61082},
		71731: {71191},
		71960: {71731, 71191},
		72178: {71731, 71191},
	}
	if !reflect.DeepEqual(anc, want) {
		t.Fatalf("ancestors %v, want %v", anc, want)
	}
}

func TestClaudeProcessesAncestorLoopSafe(t *testing.T) {
	out := []byte("10 20 /x/claude\n20 10 /x/claude\n30 30 /x/claude\n40 0 /x/claude\n")
	got := ParseClaudeProcs(out, -1)
	anc := map[int][]int{}
	for _, p := range got {
		anc[p.PID] = p.Ancestors
	}
	want := map[int][]int{10: {20}, 20: {10}, 30: nil, 40: nil}
	if !reflect.DeepEqual(anc, want) {
		t.Fatalf("ancestors %v, want %v", anc, want)
	}
	// a chain longer than 64 hops stops
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "%d %d /x/claude\n", i+1000, i+1001)
	}
	for _, p := range ParseClaudeProcs([]byte(b.String()), -1) {
		if len(p.Ancestors) > 64 {
			t.Fatalf("pid %d: %d ancestors, want at most 64", p.PID, len(p.Ancestors))
		}
	}
}

func TestClaudeProcessesExcludesSelf(t *testing.T) {
	got := ParseClaudeProcs([]byte("10 1 /x/claude\n11 1 /x/claude\n"), 10)
	if len(got) != 1 || got[0].PID != 11 {
		t.Fatalf("got %+v, want only 11", got)
	}
}

// Guard (green on the base): the node local-installer shape stays listed.
func TestClaudeProcessesStillMatchesNodeLocalInstaller(t *testing.T) {
	got := ParseClaudeProcs([]byte("  50     1 node /Users/x/.claude/local/node_modules/.bin/claude --resume aaaaaaaa-0000-0000-0000-000000000001\n"), -1)
	if len(got) != 1 || got[0].PID != 50 {
		t.Fatalf("got %+v, want pid 50 listed", got)
	}
}
