package devserver

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseParents(t *testing.T) {
	got := parseParents([]byte("  1     0\n9000001 1\nnot a line\n9000002 9000001\n"))
	want := map[int]int{1: 0, 9000001: 1, 9000002: 9000001}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseParents = %v, want %v", got, want)
	}
}

func TestDescendants(t *testing.T) {
	parents := map[int]int{9000002: 9000001, 9000003: 9000001, 9000004: 9000002, 9000005: 1, 9000001: 9000004}
	got := Descendants(parents, []int{9000001})
	if want := []int{9000002, 9000003, 9000004}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Descendants = %v, want %v", got, want)
	}
	if got := Descendants(nil, []int{9000001}); len(got) != 0 {
		t.Fatalf("Descendants(nil) = %v, want empty", got)
	}
	// pid <= 1 is never a root nor a descendant.
	if got := Descendants(map[int]int{9000001: 1, 9000002: 1}, []int{1}); len(got) != 0 {
		t.Fatalf("root 1 yielded %v", got)
	}
}

func TestStopKillsTheServerTree(t *testing.T) {
	container := t.TempDir()
	mine := filepath.Join(container, "feat-y")
	ctx := testContextWithServer(t, container, mine, "PORT=3013\n")
	oldBusy, oldCwds, oldKill, oldOut, oldParents := portBusy, processCwds, killPIDsFn, out, processParentsFn
	t.Cleanup(func() {
		portBusy, processCwds, killPIDsFn, out, processParentsFn = oldBusy, oldCwds, oldKill, oldOut, oldParents
	})
	portBusy = func(int) (bool, []int, error) { return true, []int{9000001}, nil }
	processCwds = func([]int) (map[int]string, error) { return map[int]string{9000001: mine}, nil }
	processParentsFn = func() (map[int]int, error) {
		return map[int]int{9000002: 9000001, 9000003: 9000001}, nil
	}
	var killed [][]int
	killPIDsFn = func(p []int) { killed = append(killed, p) }
	out = &bytes.Buffer{}

	if err := Stop(ctx, "feat-y", ""); err != nil {
		t.Fatal(err)
	}
	if want := [][]int{{9000001, 9000002, 9000003}}; !reflect.DeepEqual(killed, want) {
		t.Fatalf("killed %v, want %v (the esbuild children must go with vite)", killed, want)
	}
}

func TestStopKillsTheListenerWhenTheTreeIsUnreadable(t *testing.T) {
	container := t.TempDir()
	mine := filepath.Join(container, "feat-y")
	ctx := testContextWithServer(t, container, mine, "PORT=3013\n")
	oldBusy, oldCwds, oldKill, oldOut, oldParents := portBusy, processCwds, killPIDsFn, out, processParentsFn
	t.Cleanup(func() {
		portBusy, processCwds, killPIDsFn, out, processParentsFn = oldBusy, oldCwds, oldKill, oldOut, oldParents
	})
	portBusy = func(int) (bool, []int, error) { return true, []int{9000001}, nil }
	processCwds = func([]int) (map[int]string, error) { return map[int]string{9000001: mine}, nil }
	processParentsFn = func() (map[int]int, error) { return nil, errors.New("ps boom") }
	var killed [][]int
	killPIDsFn = func(p []int) { killed = append(killed, p) }
	var buf bytes.Buffer
	out = &buf

	if err := Stop(ctx, "feat-y", ""); err != nil {
		t.Fatal(err)
	}
	if want := [][]int{{9000001}}; !reflect.DeepEqual(killed, want) {
		t.Fatalf("killed %v, want %v", killed, want)
	}
	if !strings.Contains(buf.String(), "could not list child processes") {
		t.Fatalf("output lacks the warning: %q", buf.String())
	}
}
