package devserver

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOwnedListenersUnknownCwdIsForeign(t *testing.T) {
	wt := t.TempDir()
	oldBusy, oldCwds := portBusy, processCwds
	t.Cleanup(func() { portBusy, processCwds = oldBusy, oldCwds })
	portBusy = func(int) (bool, []int, error) { return true, []int{1, 2, 3}, nil }
	processCwds = func([]int) (map[int]string, error) { return map[int]string{1: wt, 2: "/elsewhere"}, nil }

	o, err := OwnedListeners(3013, wt)
	if err != nil || len(o.PIDs) == 0 || !reflect.DeepEqual(o.Owned, []int{1}) {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if o.Foreign[2] != "/elsewhere" || o.Foreign[3] != "" {
		t.Fatalf("foreign %v (pid 3: unknown cwd, still foreign)", o.Foreign)
	}

	processCwds = func([]int) (map[int]string, error) { return nil, errors.New("lsof: EMFILE") }
	o, _ = OwnedListeners(3013, wt)
	if len(o.Owned) != 0 || !o.Unknown() || !reflect.DeepEqual(o.ForeignPIDs(), []int{1, 2, 3}) {
		t.Fatalf("a failed cwd probe must attribute nothing: %+v", o)
	}
}

func TestOwnershipDescribeForeign(t *testing.T) {
	c := t.TempDir()
	o := Ownership{PIDs: []int{4242}, Foreign: map[int]string{4242: filepath.Join(c, "feat-x", "apps", "web")}}
	if got := o.Describe(c); got != "held by another worktree ("+filepath.Join(c, "feat-x")+", pid 4242)" {
		t.Fatalf("got %q", got)
	}
	o = Ownership{PIDs: []int{7}, Foreign: map[int]string{7: ""}}
	if got := o.Describe(c); got != "held by another worktree (unknown holder, pid 7)" {
		t.Fatalf("got %q", got)
	}
}
